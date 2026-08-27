/*
 * Copyright 2026 Seamless Middleware Technologies S.L and/or its affiliates
 * and other contributors as indicated by the @author tags.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package resolver

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"consent-owner-resolver/internal/contract"
)

// contractLookup is the subset of the facade client the matcher needs, kept as an
// interface so tests can substitute a stub.
type contractLookup interface {
	SignedContracts(ctx context.Context, providerSD, consumerSD string) ([]contract.Contract, error)
	DataResources(ctx context.Context, c contract.Contract) ([]contract.DataResource, error)
}

// contractMatcher derives the governing data resource from the CONTRACT instead of
// static configuration:
//
//   - the owner still comes from the data (the ownerPtr field), so the requestor
//     never determines ownership;
//   - the consumer (from ResolveRequest.Parties) plus the provider identify the
//     signed contract;
//   - the requested object's URI is matched against each rule's ODRL asset target
//     (`assetTarget`, preserved by the consent-facade); the matching contract's
//     PII data resource becomes the claim's DataResource;
//   - `containsPII` on that catalog resource decides whether consent is required
//     at all, so no static flag is needed.
//
// Matching is plain-URI equality. Richer targets (an ODRL AssetCollection with
// refinements) need the contract model to carry them; see owner-plan.md phase B.
type contractMatcher struct {
	client       contractLookup
	providerSD   string
	items        string
	itemsIsArray bool
	ownerPtr     string
	uriPtr       string
	participant  string
}

// defaultURIPointer is where the requested object's URI is read from when the
// matcher config does not say otherwise (NGSI-LD and most JSON-LD payloads).
const defaultURIPointer = "/id"

func newContractMatcher(m rawMatcher, client contractLookup, providerSD string) (Matcher, error) {
	if client == nil {
		return nil, errors.New("contract matcher: contractService is not configured")
	}
	// providerSD may be empty: the provider self-description is usually supplied
	// per request (parties.provider) because it embeds an id only known once the
	// participant is registered. It is then required at request time.
	if m.Owner == "" {
		return nil, errors.New("contract matcher: 'owner' pointer is required")
	}
	uriPtr := m.URIPointer
	if uriPtr == "" {
		uriPtr = defaultURIPointer
	}
	return &contractMatcher{
		client:       client,
		providerSD:   providerSD,
		items:        m.Items,
		itemsIsArray: m.ItemsIsArray,
		ownerPtr:     m.Owner,
		uriPtr:       uriPtr,
		participant:  m.Participant,
	}, nil
}

// Claims resolves one claim per data item. It fetches the contracts once per
// request and reuses them for every item.
func (m *contractMatcher) Claims(ctx context.Context, req ResolveRequest, decoded interface{}) ([]Claim, error) {
	if decoded == nil {
		return nil, errors.New("contract matcher: a JSON body is required but none was decoded")
	}
	consumer := ""
	provider := m.providerSD
	if req.Parties != nil {
		consumer = req.Parties.Consumer
		if req.Parties.Provider != "" {
			provider = req.Parties.Provider
		}
	}
	if consumer == "" {
		return nil, errors.New("contract matcher: parties.consumer is required to identify the contract")
	}
	if provider == "" {
		return nil, errors.New("contract matcher: no provider self-description (set parties.provider or contractService.providerSelfDescription)")
	}

	contracts, err := m.client.SignedContracts(ctx, provider, consumer)
	if err != nil {
		return nil, fmt.Errorf("contract matcher: contract lookup failed: %w", err)
	}
	if len(contracts) == 0 {
		return nil, fmt.Errorf("contract matcher: no signed contract between %q and %q", provider, consumer)
	}

	root, ok := getJSONPointer(decoded, m.items)
	if !ok {
		return nil, fmt.Errorf("contract matcher: no data at items pointer %q", m.items)
	}
	if !m.itemsIsArray {
		claim, err := m.claimForItem(ctx, contracts, root, m.items)
		if err != nil {
			return nil, err
		}
		return []Claim{claim}, nil
	}
	arr, ok := root.([]interface{})
	if !ok {
		return nil, fmt.Errorf("contract matcher: value at %q is not an array", m.items)
	}
	claims := make([]Claim, 0, len(arr))
	for i, item := range arr {
		claim, err := m.claimForItem(ctx, contracts, item, joinPointer(m.items, strconv.Itoa(i)))
		if err != nil {
			return nil, err
		}
		claims = append(claims, claim)
	}
	return claims, nil
}

func (m *contractMatcher) claimForItem(ctx context.Context, contracts []contract.Contract, item interface{}, selector string) (Claim, error) {
	owner, ok := pointerString(item, m.ownerPtr)
	if !ok || owner == "" {
		return Claim{}, fmt.Errorf("contract matcher: no owner at %q", m.ownerPtr)
	}
	requestedURI, ok := pointerString(item, m.uriPtr)
	if !ok || requestedURI == "" {
		return Claim{}, fmt.Errorf("contract matcher: no requested-object URI at %q", m.uriPtr)
	}

	governing, found := findContractForTarget(contracts, requestedURI)
	if !found {
		return Claim{}, fmt.Errorf("contract matcher: no contract rule targets %q", requestedURI)
	}

	resources, err := m.client.DataResources(ctx, governing)
	if err != nil {
		return Claim{}, fmt.Errorf("contract matcher: resolving data resources of contract %q: %w", governing.ID, err)
	}
	resource, found := firstPIIResource(resources)
	if !found {
		// The contract governs this object but declares no PII resource: nothing to
		// gate. Emitting no resource keeps the claim owner-level rather than
		// silently allowing.
		return Claim{
			Selector:    Selector{Type: SelectorJSONPointer, Value: selector},
			OwnerID:     owner,
			Participant: m.participant,
		}, nil
	}
	return Claim{
		Selector:     Selector{Type: SelectorJSONPointer, Value: selector},
		OwnerID:      owner,
		DataResource: resource.ID,
		Participant:  m.participant,
	}, nil
}

// findContractForTarget returns the first contract having a permission whose ODRL
// asset target equals the requested URI.
func findContractForTarget(contracts []contract.Contract, requestedURI string) (contract.Contract, bool) {
	for _, c := range contracts {
		for _, policy := range c.Policy {
			for _, rule := range policy.Permission {
				if rule.AssetTarget == requestedURI {
					return c, true
				}
			}
		}
	}
	return contract.Contract{}, false
}

// firstPIIResource returns the first data resource flagged as carrying personal
// data - the one a consent decision applies to.
func firstPIIResource(resources []contract.DataResource) (contract.DataResource, bool) {
	for _, r := range resources {
		if r.ContainsPII {
			return r, true
		}
	}
	return contract.DataResource{}, false
}
