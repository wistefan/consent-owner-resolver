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

// Package resolver defines the OwnerResolver contract and a configurable,
// data-format-agnostic implementation of it.
//
// The OwnerResolver answers two questions about a piece of data flowing through
// the consent-plugin, WITHOUT ever seeing the requestor:
//
//	(a) does this data require a consent check?  -> ResolveResult.ConsentRequired
//	(b) who is the data owner (the subject)?     -> ResolveResult.Claims[i].OwnerID
//
// The decision unit downstream is (owner x dataResource): a single owner may
// have consented to some data objects but not others, so every claim carries
// both an owner and the resource it concerns.
package resolver

import (
	"context"
	"encoding/json"
)

// Selector types identify WHERE in the payload a claim applies. The plugin uses
// this only for v2 field-level filtering; v1 (deny_all) ignores it.
const (
	// SelectorWhole means the claim covers the entire payload — the natural
	// granularity for an opaque single file or a single object. Not redactable.
	SelectorWhole = "whole"
	// SelectorJSONPointer means the claim covers the JSON value at Selector.Value
	// (an RFC6901 pointer; "" = the whole document). Redactable in v2.
	SelectorJSONPointer = "json-pointer"
)

// Owner-id interpretation schemes. They tell the plugin (and through it the
// consent-manager) what an OwnerID actually IS, so a typo here silently changes
// which lookup the consent check performs — which is why Parse validates them.
const (
	// SchemeIdentifier: the owner id is the consent-manager's per-participant
	// identifier for the subject. The default.
	SchemeIdentifier = "identifier"
	// SchemeEmail: the owner id is an email address.
	SchemeEmail = "email"
	// SchemeDID: the owner id is a decentralized identifier.
	SchemeDID = "did"
)

// Body encodings for the (optional) payload delivered to the resolver.
const (
	// EncodingNone: the body is not sent; the resolver must decide from Resource
	// alone (e.g. large or opaque files identified by their path).
	EncodingNone = "none"
	// EncodingJSON: Body.Content is the payload as raw JSON.
	EncodingJSON = "json"
	// EncodingBase64: Body.Content is a JSON string holding base64-encoded bytes
	// (for opaque payloads). Decoded to JSON when it happens to be JSON, else the
	// resolver works from Resource.
	EncodingBase64 = "base64"
)

// Resource describes the data and its provenance. It deliberately carries NO
// requestor/caller identity — that independence is the whole point.
type Resource struct {
	// Service is the logical dataset/route id, configured on the plugin route.
	Service string `json:"service"`
	// Method is the upstream HTTP method (informational).
	Method string `json:"method"`
	// Path is the upstream request path — the primary handle for opaque data.
	Path string `json:"path"`
	// ContentType is the payload media type (informational).
	ContentType string `json:"contentType"`
}

// Body carries the payload when the plugin chooses to send it.
type Body struct {
	// Encoding is one of EncodingNone, EncodingJSON, EncodingBase64.
	Encoding string `json:"encoding"`
	// Content is the payload; shape depends on Encoding. Omitted for EncodingNone.
	Content json.RawMessage `json:"content,omitempty"`
}

// Parties names the participants of the exchange. It exists ONLY so the governing
// contract can be identified (a contract is provider<->consumer by definition).
// It must never be used to determine the data owner - that always comes from the
// data itself.
type Parties struct {
	// Consumer is the requesting participant, e.g. the DID that issued the
	// credential presented with the request.
	Consumer string `json:"consumer,omitempty"`
	// Provider optionally overrides the configured provider self-description.
	Provider string `json:"provider,omitempty"`
}

// ResolveRequest is the body of POST /resolve.
type ResolveRequest struct {
	Resource Resource `json:"resource"`
	Parties  *Parties `json:"parties,omitempty"`
	Body     *Body    `json:"body,omitempty"`
}

// Selector locates a claim within the payload. See the Selector* constants.
type Selector struct {
	Type  string `json:"type"`
	Value string `json:"value,omitempty"`
}

// Claim is one (owner x dataResource) requirement found in the data.
type Claim struct {
	// Selector locates the data this claim covers (for v2 filtering).
	Selector Selector `json:"selector"`
	// OwnerID is the data owner (subject), in the provider's terms — interpreted
	// per ResolveResult.Scheme.
	OwnerID string `json:"ownerId"`
	// Participant optionally scopes the owner lookup to a specific participant.
	Participant string `json:"participant,omitempty"`
	// DataResource identifies WHAT this data is; when set it MUST use the same
	// vocabulary the privacy notice / consent are expressed in
	// (Consent.data[].resource). Optional: omit it for owner-level consent (the
	// current default), where the decision is per-owner and carries no resource.
	DataResource string `json:"dataResource,omitempty"`
}

// ResolveResult is the response of POST /resolve.
type ResolveResult struct {
	// ConsentRequired answers (a): does this payload need any consent check?
	ConsentRequired bool `json:"consentRequired"`
	// Scheme tells the plugin how to interpret every OwnerID; one of
	// SchemeIdentifier, SchemeEmail or SchemeDID.
	Scheme string `json:"scheme,omitempty"`
	// Claims answers (b): one entry per data item / subject. Empty when
	// ConsentRequired is false. Always serialized as a list, never as null -
	// the consumer is a Lua plugin whose cjson decodes null to a lightuserdata
	// that cannot be iterated.
	Claims []Claim `json:"claims"`
}

// Resolver is the OwnerResolver interface. The HTTP handler is one binding; an
// in-process implementation could be another.
type Resolver interface {
	Resolve(ctx context.Context, req ResolveRequest) (ResolveResult, error)
}
