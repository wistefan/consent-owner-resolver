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

// Package contract resolves, for a provider↔consumer pair, the signed contract
// that governs an exchange and the data resources it covers.
//
// It talks to a consent-facade (a provider-local instance), which projects the
// provider's own TMForum agreements into contracts and a catalog. The chain is:
//
//	GET /verify/{providerSD64}/{consumerSD64}   -> the signed contract(s)
//	contract.policy[].permission[].assetTarget  -> the ODRL target (asset URI)
//	GET contract.serviceOffering                -> dataResources[]
//	GET <dataResource>                          -> { @id, containsPII }
//
// The consumer identifies which CONTRACT applies; it never identifies the data
// owner. Ownership stays with the data (see the resolver package).
package contract

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultTimeoutMs is the default per-call timeout against the facade.
const DefaultTimeoutMs = 3000

// Rule is one ODRL permission/prohibition of a contract policy.
type Rule struct {
	// Target is the normalized target (the service-offering URL) the
	// consent-manager expects.
	Target string `json:"target"`
	// AssetTarget is the target the source agreement carried - the asset URI that
	// identifies the concrete data object. Requires a consent-facade that
	// preserves it.
	AssetTarget string `json:"assetTarget"`
	Action      string `json:"action"`
}

// Policy is an ODRL policy of a contract.
type Policy struct {
	UID         string `json:"uid"`
	Permission  []Rule `json:"permission"`
	Prohibition []Rule `json:"prohibition"`
}

// Contract is the (subset of the) projected bilateral contract.
type Contract struct {
	ID              string   `json:"_id"`
	Status          string   `json:"status"`
	DataProvider    string   `json:"dataProvider"`
	DataConsumer    string   `json:"dataConsumer"`
	ServiceOffering string   `json:"serviceOffering"`
	Policy          []Policy `json:"policy"`
}

type verifyResponse struct {
	Verified  bool       `json:"verified"`
	Contracts []Contract `json:"contracts"`
}

type serviceOffering struct {
	DataResources []string `json:"dataResources"`
}

// DataResource is the catalog self-description of a data resource. Its ID is the
// vocabulary consents are expressed in (Consent.data[].resource).
type DataResource struct {
	ID          string `json:"@id"`
	Name        string `json:"name"`
	ContainsPII bool   `json:"containsPII"`
}

// Client queries a consent-facade for contracts and catalog resources.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient builds a facade client for the given base URL (e.g.
// http://consent-facade.provider.svc.cluster.local:8080).
func NewClient(baseURL string, timeoutMs int) *Client {
	if timeoutMs <= 0 {
		timeoutMs = DefaultTimeoutMs
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond},
	}
}

// encodeParticipant renders a participant self-description URL the way the
// facade's path parameters expect it (base64 of the SD URL).
func encodeParticipant(selfDescriptionURL string) string {
	return base64.StdEncoding.EncodeToString([]byte(selfDescriptionURL))
}

// SignedContracts returns the signed contracts between a provider and a consumer,
// both given as participant self-description URLs. An unverified pair yields no
// contracts (and no error).
func (c *Client) SignedContracts(ctx context.Context, providerSD, consumerSD string) ([]Contract, error) {
	endpoint := fmt.Sprintf("%s/verify/%s/%s", c.baseURL,
		url.PathEscape(encodeParticipant(providerSD)), url.PathEscape(encodeParticipant(consumerSD)))
	var out verifyResponse
	if err := c.getJSON(ctx, endpoint, &out); err != nil {
		return nil, err
	}
	if !out.Verified {
		return nil, nil
	}
	return out.Contracts, nil
}

// localize rewrites an absolute facade URL so it is fetched from THIS client's
// base, keeping only its path. Contract and catalog ids are minted with the
// canonical (authority) facade base - that is the vocabulary consents are
// expressed in - while a provider-local facade instance serves the same paths.
// Rewriting the host lets the resolver read locally without changing any id.
func (c *Client) localize(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Path == "" {
		return rawURL
	}
	localized := c.baseURL + parsed.Path
	if parsed.RawQuery != "" {
		localized += "?" + parsed.RawQuery
	}
	return localized
}

// DataResources dereferences a contract's service offering and returns the
// catalog data resources it bundles. Fetches are localized (see localize); the
// returned IDs stay canonical so they match Consent.data[].resource.
func (c *Client) DataResources(ctx context.Context, contract Contract) ([]DataResource, error) {
	if contract.ServiceOffering == "" {
		return nil, nil
	}
	var offering serviceOffering
	if err := c.getJSON(ctx, c.localize(contract.ServiceOffering), &offering); err != nil {
		return nil, err
	}
	resources := make([]DataResource, 0, len(offering.DataResources))
	for _, ref := range offering.DataResources {
		var dr DataResource
		if err := c.getJSON(ctx, c.localize(ref), &dr); err != nil {
			return nil, err
		}
		// Keep the canonical id: `ref` (and the document's own @id) are minted with
		// the authority facade base, which is what consents reference.
		if dr.ID == "" {
			dr.ID = ref
		}
		resources = append(resources, dr)
	}
	return resources, nil
}

func (c *Client) getJSON(ctx context.Context, endpoint string, target interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("contract client: create request for %q: %w", endpoint, err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("contract client: request %q failed: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("contract client: read response from %q: %w", endpoint, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("contract client: %q returned status %d", endpoint, resp.StatusCode)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("contract client: decode response from %q: %w", endpoint, err)
	}
	return nil
}
