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

// maxResponseBytes caps a single facade response. The inbound side is protected
// by http.MaxBytesReader; this is the same protection for the side the resolver
// calls, so a facade that misbehaves (or is impersonated) cannot exhaust the
// resolver's memory on the request hot path. 4 MiB is orders of magnitude above
// any real contract or catalog document.
const maxResponseBytes = 4 << 20

// drainBytes is how much of a discarded response body is read before closing, so
// the underlying connection can be reused.
const drainBytes = 4 << 10

// StatusSigned is the only contract status this resolver treats as governing.
// A contract in any other state - terminated, revoked, pending, or with no
// status at all - is ignored. Honouring a terminated agreement in the component
// that gates personal data is the wrong direction to fail, so an unrecognised
// status fails CLOSED.
const StatusSigned = "signed"

// Rule is one ODRL permission/prohibition of a contract policy.
type Rule struct {
	// Target is the normalized target (the service-offering URL) the
	// consent-manager expects. The resolver matches on AssetTarget instead,
	// because it needs the concrete data object rather than the offering; Target
	// is carried so the value is available to callers that speak the
	// consent-manager's vocabulary.
	Target string `json:"target"`
	// AssetTarget is the target the source agreement carried - the asset URI that
	// identifies the concrete data object. Requires a consent-facade that
	// preserves it.
	AssetTarget string `json:"assetTarget"`
	// Action is the ODRL action (e.g. "use"). NOT evaluated in v1: the plugin
	// gates read access to personal data, and the resolver's answer is
	// action-independent. Refining a claim per action needs the plugin to send
	// the intended action, which is phase B.
	Action string `json:"action"`
}

// Policy is an ODRL policy of a contract.
type Policy struct {
	UID        string `json:"uid"`
	Permission []Rule `json:"permission"`
	// Prohibition rules disqualify a contract from governing their target: a
	// policy that both permits and prohibits an asset is not a grant. See
	// resolver.findContractForTarget.
	Prohibition []Rule `json:"prohibition"`
}

// Contract is the (subset of the) projected bilateral contract.
type Contract struct {
	ID string `json:"_id"`
	// Status is the contract state; only StatusSigned governs (see IsSigned).
	Status          string   `json:"status"`
	DataProvider    string   `json:"dataProvider"`
	DataConsumer    string   `json:"dataConsumer"`
	ServiceOffering string   `json:"serviceOffering"`
	Policy          []Policy `json:"policy"`
}

// IsSigned reports whether the contract is in the one state that governs an
// exchange. The comparison is case-insensitive; anything else - including an
// absent status - is not signed.
func (c Contract) IsSigned() bool {
	return strings.EqualFold(c.Status, StatusSigned)
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

// participantEncoding is the base64 alphabet used for participant
// self-description URLs in facade path parameters.
//
// It MUST match what the consent-facade decodes - this is a wire contract, not
// an implementation detail, and TestEncodeParticipant pins it.
//
// Raw URL encoding (RFC 4648 §5, unpadded) rather than the standard alphabet,
// because every character it can emit is already safe in a path segment:
//
//	standard : A-Za-z0-9 + / =   -- `/` would go on the wire as %2F, which nginx
//	                                and APISIX commonly normalize back to `/`,
//	                                silently changing the path; `=` padding is
//	                                always present for inputs of the wrong length
//	raw url  : A-Za-z0-9 - _     -- nothing to escape, nothing to normalize
//
// The `+` and `/` cases only arise for SD URLs containing `?`, `>` or `~`, which
// ordinary ones do not - but `~` is used throughout this project's identifiers
// (`default~urn:ngsi-ld:agreement:1`), so the class is reachable rather than
// theoretical.
var participantEncoding = base64.RawURLEncoding

// encodeParticipant renders a participant self-description URL the way the
// facade's path parameters expect it (see participantEncoding). The result needs
// no further escaping: the alphabet contains no character that is special in a
// path segment.
func encodeParticipant(selfDescriptionURL string) string {
	return participantEncoding.EncodeToString([]byte(selfDescriptionURL))
}

// SignedContracts returns the SIGNED contracts between a provider and a
// consumer, both given as participant self-description URLs. An unverified pair
// yields no contracts (and no error), and so does a pair whose contracts are all
// in some other state (terminated, revoked, pending, ...) - see IsSigned.
func (c *Client) SignedContracts(ctx context.Context, providerSD, consumerSD string) ([]Contract, error) {
	endpoint := fmt.Sprintf("%s/verify/%s/%s", c.baseURL,
		encodeParticipant(providerSD), encodeParticipant(consumerSD))
	var out verifyResponse
	if err := c.getJSON(ctx, endpoint, &out); err != nil {
		return nil, err
	}
	if !out.Verified {
		return nil, nil
	}
	signed := make([]Contract, 0, len(out.Contracts))
	for _, contract := range out.Contracts {
		if contract.IsSigned() {
			signed = append(signed, contract)
		}
	}
	return signed, nil
}

// localize rewrites an absolute facade URL so it is fetched from THIS client's
// base, keeping only its path. Contract and catalog ids are minted with the
// canonical (authority) facade base - that is the vocabulary consents are
// expressed in - while a provider-local facade instance serves the same paths.
// Rewriting the host lets the resolver read locally without changing any id.
//
// It always rebuilds, or fails. Returning the input unchanged for a URL it could
// not rewrite would mean fetching whatever host that URL named - and the input
// comes from the facade's response, i.e. from outside this process. A URL with
// no path (`http://some-internal-host`) is exactly such a case.
func (c *Client) localize(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("contract client: %q is not a usable url: %w", rawURL, err)
	}
	if parsed.Path == "" || parsed.Path == "/" {
		return "", fmt.Errorf("contract client: %q has no path to localize", rawURL)
	}
	localized := c.baseURL + parsed.Path
	if parsed.RawQuery != "" {
		localized += "?" + parsed.RawQuery
	}
	return localized, nil
}

// DataResources dereferences a contract's service offering and returns the
// catalog data resources it bundles. Fetches are localized (see localize); the
// returned IDs stay canonical so they match Consent.data[].resource.
func (c *Client) DataResources(ctx context.Context, contract Contract) ([]DataResource, error) {
	if contract.ServiceOffering == "" {
		return nil, nil
	}
	offeringURL, err := c.localize(contract.ServiceOffering)
	if err != nil {
		return nil, err
	}
	var offering serviceOffering
	if err := c.getJSON(ctx, offeringURL, &offering); err != nil {
		return nil, err
	}
	resources := make([]DataResource, 0, len(offering.DataResources))
	for _, ref := range offering.DataResources {
		resourceURL, err := c.localize(ref)
		if err != nil {
			return nil, err
		}
		var dr DataResource
		if err := c.getJSON(ctx, resourceURL, &dr); err != nil {
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
	// Status first: the body of an error response is of no interest, and reading
	// a large one in full before discarding it is pure waste. A bounded drain
	// still lets the connection be reused.
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, drainBytes))
		return fmt.Errorf("contract client: %q returned status %d", endpoint, resp.StatusCode)
	}
	// Read one byte past the cap so an oversized response is reported as such,
	// rather than silently truncated into a confusing JSON syntax error.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("contract client: read response from %q: %w", endpoint, err)
	}
	if len(body) > maxResponseBytes {
		return fmt.Errorf("contract client: response from %q exceeds the %d byte limit", endpoint, maxResponseBytes)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("contract client: decode response from %q: %w", endpoint, err)
	}
	return nil
}
