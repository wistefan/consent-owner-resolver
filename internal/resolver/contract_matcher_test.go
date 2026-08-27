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
	"testing"

	"consent-owner-resolver/internal/contract"
)

const (
	testProviderSD = "http://facade/participants/urn:ngsi-ld:organization:prov"
	testConsumerSD = "did:web:fancy-marketplace.biz"
	testAssetURI   = "urn:ngsi-ld:PersonalProfile:alice"
	testResourceID = "http://facade/catalog/dataresources/default~urn:ngsi-ld:product-specification:1"
)

// stubLookup is a contractLookup returning canned data.
type stubLookup struct {
	contracts     []contract.Contract
	resources     []contract.DataResource
	contractsErr  error
	resourcesErr  error
	gotProvider   string
	gotConsumer   string
	contractCalls int
	resourceCalls int
}

func (s *stubLookup) SignedContracts(_ context.Context, providerSD, consumerSD string) ([]contract.Contract, error) {
	s.contractCalls++
	s.gotProvider, s.gotConsumer = providerSD, consumerSD
	return s.contracts, s.contractsErr
}

func (s *stubLookup) DataResources(_ context.Context, _ contract.Contract) ([]contract.DataResource, error) {
	s.resourceCalls++
	return s.resources, s.resourcesErr
}

// contractWithTarget builds a signed contract whose single ODRL permission
// targets `target`. The contract id is derived from the target so that two
// fixtures are two DISTINCT contracts - which is what per-contract caching keys
// on.
func contractWithTarget(target string) contract.Contract {
	return contract.Contract{
		ID:              "default~urn:ngsi-ld:agreement:" + target,
		Status:          "signed",
		ServiceOffering: "http://facade/catalog/serviceofferings/default~urn:ngsi-ld:agreement:1",
		Policy: []contract.Policy{{
			UID:        "urn:policy:profile",
			Permission: []contract.Rule{{Target: "http://facade/catalog/serviceofferings/x", AssetTarget: target, Action: "use"}},
		}},
	}
}

func piiResources() []contract.DataResource {
	return []contract.DataResource{
		{ID: "http://facade/catalog/dataresources/non-pii", ContainsPII: false},
		{ID: testResourceID, Name: "Personal Profile", ContainsPII: true},
	}
}

func newContractResolver(t *testing.T, stub contractLookup, itemsIsArray bool) Matcher {
	t.Helper()
	m, err := newContractMatcher(rawMatcher{
		Type:         "contract",
		Owner:        "/dataOwner/value",
		Items:        "",
		ItemsIsArray: itemsIsArray,
	}, stub, testProviderSD)
	if err != nil {
		t.Fatalf("newContractMatcher: %v", err)
	}
	return m
}

func entity(id, owner string) map[string]interface{} {
	return map[string]interface{}{
		"id":        id,
		"type":      "PersonalProfile",
		"dataOwner": map[string]interface{}{"type": "Property", "value": owner},
	}
}

func requestWithConsumer(consumer string) ResolveRequest {
	return ResolveRequest{
		Resource: Resource{Service: "mp-data-service", Path: "/ngsi-ld/v1/entities/" + testAssetURI},
		Parties:  &Parties{Consumer: consumer},
	}
}

func TestContractMatcher_ResourceFromContractTarget(t *testing.T) {
	stub := &stubLookup{contracts: []contract.Contract{contractWithTarget(testAssetURI)}, resources: piiResources()}
	m := newContractResolver(t, stub, false)

	claims, err := m.Claims(context.Background(), requestWithConsumer(testConsumerSD), entity(testAssetURI, "did:key:zOwner"))
	if err != nil {
		t.Fatalf("Claims: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("want 1 claim, got %d", len(claims))
	}
	if claims[0].OwnerID != "did:key:zOwner" {
		t.Fatalf("owner must come from the DATA, got %q", claims[0].OwnerID)
	}
	if claims[0].DataResource != testResourceID {
		t.Fatalf("resource must be the contract's PII data resource, got %q", claims[0].DataResource)
	}
	if stub.gotProvider != testProviderSD || stub.gotConsumer != testConsumerSD {
		t.Fatalf("contract looked up with wrong parties: %q / %q", stub.gotProvider, stub.gotConsumer)
	}
}

func TestContractMatcher_Errors(t *testing.T) {
	cases := map[string]struct {
		stub *stubLookup
		req  ResolveRequest
		item interface{}
	}{
		"no consumer given": {
			stub: &stubLookup{contracts: []contract.Contract{contractWithTarget(testAssetURI)}, resources: piiResources()},
			req:  ResolveRequest{Resource: Resource{Path: "/x"}},
			item: entity(testAssetURI, "did:key:zOwner"),
		},
		"no signed contract": {
			stub: &stubLookup{contracts: nil, resources: piiResources()},
			req:  requestWithConsumer(testConsumerSD),
			item: entity(testAssetURI, "did:key:zOwner"),
		},
		"no rule targets the object": {
			stub: &stubLookup{contracts: []contract.Contract{contractWithTarget("urn:ngsi-ld:PersonalProfile:someone-else")}, resources: piiResources()},
			req:  requestWithConsumer(testConsumerSD),
			item: entity(testAssetURI, "did:key:zOwner"),
		},
		"owner missing from data": {
			stub: &stubLookup{contracts: []contract.Contract{contractWithTarget(testAssetURI)}, resources: piiResources()},
			req:  requestWithConsumer(testConsumerSD),
			item: map[string]interface{}{"id": testAssetURI},
		},
		"facade unreachable": {
			stub: &stubLookup{contractsErr: errors.New("boom")},
			req:  requestWithConsumer(testConsumerSD),
			item: entity(testAssetURI, "did:key:zOwner"),
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			m := newContractResolver(t, c.stub, false)
			if _, err := m.Claims(context.Background(), c.req, c.item); err == nil {
				t.Fatalf("expected an error for %s (must fail closed)", name)
			}
		})
	}
}

func TestContractMatcher_CollectionOneClaimPerItem(t *testing.T) {
	other := "urn:ngsi-ld:PersonalProfile:bob"
	c1 := contractWithTarget(testAssetURI)
	c2 := contractWithTarget(other)
	stub := &stubLookup{contracts: []contract.Contract{c1, c2}, resources: piiResources()}
	m := newContractResolver(t, stub, true)

	body := []interface{}{entity(testAssetURI, "did:key:zAlice"), entity(other, "did:key:zBob")}
	claims, err := m.Claims(context.Background(), requestWithConsumer(testConsumerSD), body)
	if err != nil {
		t.Fatalf("Claims: %v", err)
	}
	if len(claims) != 2 {
		t.Fatalf("want 2 claims, got %d", len(claims))
	}
	if claims[0].OwnerID != "did:key:zAlice" || claims[1].OwnerID != "did:key:zBob" {
		t.Fatalf("owners wrong: %+v", claims)
	}
	if claims[0].Selector.Value != "/0" || claims[1].Selector.Value != "/1" {
		t.Fatalf("selectors wrong: %+v", claims)
	}
	if stub.contractCalls != 1 {
		t.Fatalf("contracts should be fetched once per request, got %d calls", stub.contractCalls)
	}
	// Two items, two DISTINCT governing contracts -> two resource lookups, not
	// one per item.
	if stub.resourceCalls != 2 {
		t.Fatalf("data resources should be fetched once per contract, got %d calls", stub.resourceCalls)
	}
}

func TestContractMatcher_ResourcesFetchedOncePerContract(t *testing.T) {
	// All items are governed by the SAME contract: the facade must be asked for
	// its data resources exactly once, however long the collection is.
	const items = 50
	stub := &stubLookup{contracts: []contract.Contract{contractWithTarget(testAssetURI)}, resources: piiResources()}
	m := newContractResolver(t, stub, true)

	body := make([]interface{}, 0, items)
	for i := 0; i < items; i++ {
		body = append(body, entity(testAssetURI, "did:key:zOwner"))
	}
	claims, err := m.Claims(context.Background(), requestWithConsumer(testConsumerSD), body)
	if err != nil {
		t.Fatalf("Claims: %v", err)
	}
	if len(claims) != items {
		t.Fatalf("want %d claims, got %d", items, len(claims))
	}
	if stub.contractCalls != 1 {
		t.Fatalf("want 1 contract lookup, got %d", stub.contractCalls)
	}
	if stub.resourceCalls != 1 {
		t.Fatalf("want 1 data-resource lookup for %d items sharing a contract, got %d", items, stub.resourceCalls)
	}
}

func TestContractMatcher_ProhibitedTargetDoesNotGovern(t *testing.T) {
	// A policy that both permits and prohibits the asset is not a grant: with no
	// other candidate the matcher must fail closed.
	prohibiting := contractWithTarget(testAssetURI)
	prohibiting.Policy[0].Prohibition = []contract.Rule{{AssetTarget: testAssetURI, Action: "use"}}
	stub := &stubLookup{contracts: []contract.Contract{prohibiting}, resources: piiResources()}
	m := newContractResolver(t, stub, false)

	if _, err := m.Claims(context.Background(), requestWithConsumer(testConsumerSD), entity(testAssetURI, "did:key:zOwner")); err == nil {
		t.Fatal("a prohibited asset must not be governed by that contract")
	}
}

func TestContractMatcher_ProhibitionOnAnotherAssetIsIrrelevant(t *testing.T) {
	governing := contractWithTarget(testAssetURI)
	governing.Policy[0].Prohibition = []contract.Rule{{AssetTarget: "urn:ngsi-ld:PersonalProfile:bob", Action: "use"}}
	stub := &stubLookup{contracts: []contract.Contract{governing}, resources: piiResources()}
	m := newContractResolver(t, stub, false)

	claims, err := m.Claims(context.Background(), requestWithConsumer(testConsumerSD), entity(testAssetURI, "did:key:zOwner"))
	if err != nil {
		t.Fatalf("Claims: %v", err)
	}
	if len(claims) != 1 || claims[0].DataResource != testResourceID {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestContractMatcher_NoPIIResourceStaysOwnerLevel(t *testing.T) {
	stub := &stubLookup{
		contracts: []contract.Contract{contractWithTarget(testAssetURI)},
		resources: []contract.DataResource{{ID: "http://facade/catalog/dataresources/plain", ContainsPII: false}},
	}
	m := newContractResolver(t, stub, false)
	claims, err := m.Claims(context.Background(), requestWithConsumer(testConsumerSD), entity(testAssetURI, "did:key:zOwner"))
	if err != nil {
		t.Fatalf("Claims: %v", err)
	}
	if claims[0].DataResource != "" {
		t.Fatalf("no PII resource => claim must stay owner-level, got %q", claims[0].DataResource)
	}
}

func TestContractMatcher_ProviderFromRequestOverridesConfig(t *testing.T) {
	stub := &stubLookup{contracts: []contract.Contract{contractWithTarget(testAssetURI)}, resources: piiResources()}
	// no configured provider SD - it must come from the request
	m, err := newContractMatcher(rawMatcher{Type: "contract", Owner: "/dataOwner/value"}, stub, "")
	if err != nil {
		t.Fatalf("newContractMatcher: %v", err)
	}
	req := requestWithConsumer(testConsumerSD)
	req.Parties.Provider = "http://facade/participants/urn:ngsi-ld:organization:from-request"
	if _, err := m.Claims(context.Background(), req, entity(testAssetURI, "did:key:zOwner")); err != nil {
		t.Fatalf("Claims: %v", err)
	}
	if stub.gotProvider != req.Parties.Provider {
		t.Fatalf("provider must come from the request, got %q", stub.gotProvider)
	}
}

func TestContractMatcher_NoProviderAnywhereFailsClosed(t *testing.T) {
	stub := &stubLookup{contracts: []contract.Contract{contractWithTarget(testAssetURI)}, resources: piiResources()}
	m, err := newContractMatcher(rawMatcher{Type: "contract", Owner: "/dataOwner/value"}, stub, "")
	if err != nil {
		t.Fatalf("newContractMatcher: %v", err)
	}
	if _, err := m.Claims(context.Background(), requestWithConsumer(testConsumerSD), entity(testAssetURI, "did:key:zOwner")); err == nil {
		t.Fatal("expected an error when no provider self-description is available")
	}
}
