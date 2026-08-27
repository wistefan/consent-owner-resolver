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
	"regexp"
	"strconv"
)

// Matcher turns a matched request (plus the request body, decoded on demand)
// into a set of claims. Returning an error means "consent is required but the
// owner could not be resolved" — the plugin then applies its fail policy.
//
// Returning zero claims with no error means "matched, and there is genuinely no
// subject in this payload". The ONLY way to reach it is an empty collection
// (`itemsIsArray` over `[]`): nothing to gate, so nothing to claim. It must
// never mean "I could not find the owner" — a matcher that cannot find one
// errors. `consentRequired` stays whatever the rule says either way, so the
// answer never silently degrades to "no consent needed".
//
// The body arrives as a Payload rather than an already-decoded value so that a
// matcher which never reads it (path, static) cannot fail on an undecodable
// payload.
type Matcher interface {
	Claims(ctx context.Context, req ResolveRequest, body Payload) ([]Claim, error)
}

// --- path matcher: format-agnostic; derives the owner from the request path ---

// pathMatcher extracts the owner (and optionally the resource) from the request
// path via a regexp with named groups. It needs no body, so it works for opaque
// single files identified purely by their path.
type pathMatcher struct {
	re          *regexp.Regexp
	ownerGroup  int
	resGroup    int // -1 when the pattern has no (?P<resource>...) group
	resource    string
	participant string
}

func newPathMatcher(m rawMatcher) (Matcher, error) {
	if m.Pattern == "" {
		return nil, errors.New("path matcher: 'pattern' is required")
	}
	re, err := regexp.Compile(m.Pattern)
	if err != nil {
		return nil, fmt.Errorf("path matcher: invalid pattern: %w", err)
	}
	ownerGroup, resGroup := -1, -1
	for i, name := range re.SubexpNames() {
		switch name {
		case "owner":
			ownerGroup = i
		case "resource":
			resGroup = i
		}
	}
	if ownerGroup < 0 {
		return nil, errors.New("path matcher: pattern must contain a named group (?P<owner>...)")
	}
	// resource is optional (owner-level consent); a (?P<resource>...) group or a
	// fixed 'resource' may be supplied when per-resource consent is used.
	return &pathMatcher{re: re, ownerGroup: ownerGroup, resGroup: resGroup, resource: m.Resource, participant: m.Participant}, nil
}

func (m *pathMatcher) Claims(_ context.Context, req ResolveRequest, _ Payload) ([]Claim, error) {
	sub := m.re.FindStringSubmatch(req.Resource.Path)
	if sub == nil {
		return nil, fmt.Errorf("path matcher: path %q did not match the extraction pattern", req.Resource.Path)
	}
	owner := sub[m.ownerGroup]
	if owner == "" {
		return nil, fmt.Errorf("path matcher: empty owner extracted from %q", req.Resource.Path)
	}
	resource := m.resource
	if m.resGroup >= 0 && sub[m.resGroup] != "" {
		resource = sub[m.resGroup]
	}
	return []Claim{{
		Selector:     Selector{Type: SelectorWhole},
		OwnerID:      owner,
		DataResource: resource,
		Participant:  m.participant,
	}}, nil
}

// --- json matcher: structured data; owner read from within each item ----------

// jsonMatcher extracts, per data item, the owner and resource from a decoded
// JSON body. When ItemsIsArray is set, each element of the collection at Items
// yields its own claim (multi-subject responses); otherwise the whole body (or
// the value at Items) is a single item.
type jsonMatcher struct {
	items        string
	itemsIsArray bool
	ownerPtr     string
	resourcePtr  string
	resource     string
	participant  string
}

func newJSONMatcher(m rawMatcher) (Matcher, error) {
	if m.Owner == "" {
		return nil, errors.New("json matcher: 'owner' pointer is required")
	}
	// resource is optional (owner-level consent); 'resourcePointer' or a fixed
	// 'resource' may be supplied when per-resource consent is used.
	return &jsonMatcher{
		items:        m.Items,
		itemsIsArray: m.ItemsIsArray,
		ownerPtr:     m.Owner,
		resourcePtr:  m.ResourcePointer,
		resource:     m.Resource,
		participant:  m.Participant,
	}, nil
}

func (m *jsonMatcher) Claims(_ context.Context, _ ResolveRequest, body Payload) ([]Claim, error) {
	decoded, err := body.JSON()
	if err != nil {
		return nil, err
	}
	if decoded == nil {
		return nil, errors.New("json matcher: a JSON body is required but none was decoded")
	}
	root, ok := getJSONPointer(decoded, m.items)
	if !ok {
		return nil, fmt.Errorf("json matcher: no data at items pointer %q", m.items)
	}
	if !m.itemsIsArray {
		claim, err := m.claimForItem(root, m.items)
		if err != nil {
			return nil, err
		}
		return []Claim{claim}, nil
	}
	arr, ok := root.([]interface{})
	if !ok {
		return nil, fmt.Errorf("json matcher: value at %q is not an array", m.items)
	}
	claims := make([]Claim, 0, len(arr))
	for i, item := range arr {
		claim, err := m.claimForItem(item, joinPointer(m.items, strconv.Itoa(i)))
		if err != nil {
			return nil, err
		}
		claims = append(claims, claim)
	}
	return claims, nil
}

func (m *jsonMatcher) claimForItem(item interface{}, selector string) (Claim, error) {
	owner, ok := pointerString(item, m.ownerPtr)
	if !ok || owner == "" {
		return Claim{}, fmt.Errorf("json matcher: no owner at %q", m.ownerPtr)
	}
	resource := m.resource
	if m.resourcePtr != "" {
		r, ok := pointerString(item, m.resourcePtr)
		if !ok || r == "" {
			return Claim{}, fmt.Errorf("json matcher: no resource at %q", m.resourcePtr)
		}
		resource = r
	}
	return Claim{
		Selector:     Selector{Type: SelectorJSONPointer, Value: selector},
		OwnerID:      owner,
		DataResource: resource,
		Participant:  m.participant,
	}, nil
}

// --- static matcher: fixed answer, for tests / always-gated routes ------------

// staticMatcher answers with a fixed owner, whatever the request. Its `owner` is
// required: a static rule with no owner paired with consentRequired:true would
// tell the plugin "consent is required, but there is nothing to check it
// against" on every single request.
type staticMatcher struct {
	owner       string
	resource    string
	participant string
}

func newStaticMatcher(m rawMatcher) (Matcher, error) {
	if m.Owner == "" {
		return nil, errors.New("static matcher: 'owner' is required (a static rule with no owner can never produce a claim)")
	}
	return &staticMatcher{owner: m.Owner, resource: m.Resource, participant: m.Participant}, nil
}

func (m *staticMatcher) Claims(_ context.Context, _ ResolveRequest, _ Payload) ([]Claim, error) {
	return []Claim{{
		Selector:     Selector{Type: SelectorWhole},
		OwnerID:      m.owner,
		DataResource: m.resource,
		Participant:  m.participant,
	}}, nil
}
