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
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func mustParse(t *testing.T, cfg string) *ConfigResolver {
	t.Helper()
	r, err := Parse([]byte(cfg))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return r
}

func jsonBody(t *testing.T, v interface{}) *Body {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return &Body{Encoding: EncodingJSON, Content: raw}
}

func TestResolve_JSONSingleEntity(t *testing.T) {
	r := mustParse(t, `{
	  "rules": [{
	    "name": "profiles",
	    "match": {"service": "mp-data-service"},
	    "consentRequired": true,
	    "matcher": {"type": "json", "owner": "/dataOwner", "resource": "urn:profile"}
	  }]
	}`)

	req := ResolveRequest{
		Resource: Resource{Service: "mp-data-service", Method: "GET", Path: "/entities/alice"},
		Body:     jsonBody(t, map[string]interface{}{"id": "alice", "dataOwner": "alice-42"}),
	}
	res, err := r.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !res.ConsentRequired || res.Scheme != "identifier" {
		t.Fatalf("unexpected result meta: %+v", res)
	}
	if len(res.Claims) != 1 {
		t.Fatalf("want 1 claim, got %d", len(res.Claims))
	}
	c := res.Claims[0]
	if c.OwnerID != "alice-42" || c.DataResource != "urn:profile" {
		t.Fatalf("unexpected claim: %+v", c)
	}
	if c.Selector.Type != SelectorJSONPointer || c.Selector.Value != "" {
		t.Fatalf("unexpected selector: %+v", c.Selector)
	}
}

func TestResolve_JSONArrayMultiSubject(t *testing.T) {
	r := mustParse(t, `{
	  "rules": [{
	    "match": {"service": "svc"},
	    "consentRequired": true,
	    "matcher": {"type": "json", "items": "", "itemsIsArray": true, "owner": "/owner", "resourcePointer": "/kind"}
	  }]
	}`)

	body := jsonBody(t, []map[string]interface{}{
		{"owner": "alice-42", "kind": "urn:zip"},
		{"owner": "bob-7", "kind": "urn:street"},
	})
	res, err := r.Resolve(context.Background(), ResolveRequest{
		Resource: Resource{Service: "svc", Path: "/things"},
		Body:     body,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(res.Claims) != 2 {
		t.Fatalf("want 2 claims, got %d", len(res.Claims))
	}
	if res.Claims[0].OwnerID != "alice-42" || res.Claims[0].DataResource != "urn:zip" || res.Claims[0].Selector.Value != "/0" {
		t.Fatalf("claim0 wrong: %+v", res.Claims[0])
	}
	if res.Claims[1].OwnerID != "bob-7" || res.Claims[1].DataResource != "urn:street" || res.Claims[1].Selector.Value != "/1" {
		t.Fatalf("claim1 wrong: %+v", res.Claims[1])
	}
}

func TestResolve_PathOpaqueFile_NoBody(t *testing.T) {
	// A single opaque file (no body sent): the owner comes from the path.
	r := mustParse(t, `{
	  "rules": [{
	    "match": {"service": "file-service"},
	    "consentRequired": true,
	    "matcher": {"type": "path", "pattern": "^/files/(?P<owner>[^/]+)/(?P<resource>.+)$"}
	  }]
	}`)

	res, err := r.Resolve(context.Background(), ResolveRequest{
		Resource: Resource{Service: "file-service", Path: "/files/alice-42/report.pdf", ContentType: "application/pdf"},
		Body:     &Body{Encoding: EncodingNone},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(res.Claims) != 1 {
		t.Fatalf("want 1 claim, got %d", len(res.Claims))
	}
	c := res.Claims[0]
	if c.OwnerID != "alice-42" || c.DataResource != "report.pdf" || c.Selector.Type != SelectorWhole {
		t.Fatalf("unexpected claim: %+v", c)
	}
}

func TestResolve_ResourceIsEntityID(t *testing.T) {
	// v1: dataResource = the id of the requested entity (from the body).
	r := mustParse(t, `{
	  "rules": [{
	    "match": {"service": "mp-data-service"},
	    "consentRequired": true,
	    "matcher": {"type": "json", "owner": "/dataOwner", "resourcePointer": "/id"}
	  }]
	}`)
	res, err := r.Resolve(context.Background(), ResolveRequest{
		Resource: Resource{Service: "mp-data-service", Path: "/ngsi-ld/v1/entities/urn:ngsi-ld:PersonalProfile:alice"},
		Body:     jsonBody(t, map[string]interface{}{"id": "urn:ngsi-ld:PersonalProfile:alice", "type": "PersonalProfile", "dataOwner": "alice-42"}),
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(res.Claims) != 1 {
		t.Fatalf("want 1 claim, got %d", len(res.Claims))
	}
	if res.Claims[0].OwnerID != "alice-42" || res.Claims[0].DataResource != "urn:ngsi-ld:PersonalProfile:alice" {
		t.Fatalf("unexpected claim: %+v", res.Claims[0])
	}
}

func TestResolve_OwnerOnly_JSON(t *testing.T) {
	// Current default: owner-level consent, no dataResource.
	r := mustParse(t, `{
	  "rules": [{
	    "match": {"service": "mp-data-service"},
	    "consentRequired": true,
	    "matcher": {"type": "json", "owner": "/dataOwner"}
	  }]
	}`)
	res, err := r.Resolve(context.Background(), ResolveRequest{
		Resource: Resource{Service: "mp-data-service", Path: "/e/alice"},
		Body:     jsonBody(t, map[string]interface{}{"id": "alice", "dataOwner": "alice-42"}),
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(res.Claims) != 1 || res.Claims[0].OwnerID != "alice-42" {
		t.Fatalf("unexpected claims: %+v", res.Claims)
	}
	if res.Claims[0].DataResource != "" {
		t.Fatalf("expected empty dataResource for owner-only, got %q", res.Claims[0].DataResource)
	}
	// dataResource must be omitted from the wire form when empty.
	raw, _ := json.Marshal(res.Claims[0])
	if strings.Contains(string(raw), "dataResource") {
		t.Fatalf("dataResource should be omitted, got %s", string(raw))
	}
}

func TestResolve_OwnerOnly_Path(t *testing.T) {
	r := mustParse(t, `{
	  "rules": [{
	    "match": {"service": "file-service"},
	    "consentRequired": true,
	    "matcher": {"type": "path", "pattern": "^/files/(?P<owner>[^/]+)/"}
	  }]
	}`)
	res, err := r.Resolve(context.Background(), ResolveRequest{
		Resource: Resource{Service: "file-service", Path: "/files/bob-7/scan.jpg"},
		Body:     &Body{Encoding: EncodingNone},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(res.Claims) != 1 || res.Claims[0].OwnerID != "bob-7" || res.Claims[0].DataResource != "" {
		t.Fatalf("unexpected claim: %+v", res.Claims)
	}
}

func TestResolve_BodylessMatchersIgnoreAnUndecodablePayload(t *testing.T) {
	// A `path` matcher needs no body, so a payload it never looks at must not
	// turn a resolvable request into a 422.
	cases := map[string]*Body{
		"illegal base64":      {Encoding: EncodingBase64, Content: json.RawMessage(`"!!!not-base64!!!"`)},
		"base64 not a string": {Encoding: EncodingBase64, Content: json.RawMessage(`{"nope":1}`)},
		"malformed json":      {Encoding: EncodingJSON, Content: json.RawMessage(`{"unclosed":`)},
		"unknown encoding":    {Encoding: "protobuf", Content: json.RawMessage(`"AAA"`)},
	}
	r := mustParse(t, `{
	  "rules": [{
	    "match": {"service": "file-service"},
	    "consentRequired": true,
	    "matcher": {"type": "path", "pattern": "^/files/(?P<owner>[^/]+)/(?P<resource>.+)$"}
	  }]
	}`)
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			res, err := r.Resolve(context.Background(), ResolveRequest{
				Resource: Resource{Service: "file-service", Path: "/files/alice-42/report.pdf"},
				Body:     body,
			})
			if err != nil {
				t.Fatalf("a body-less matcher must not fail on an undecodable payload: %v", err)
			}
			if len(res.Claims) != 1 || res.Claims[0].OwnerID != "alice-42" {
				t.Fatalf("unexpected claims: %+v", res.Claims)
			}
		})
	}
}

func TestResolve_UndecodableBodyStillFailsForJSONMatchers(t *testing.T) {
	// The laziness must not swallow the error where the matcher does need JSON.
	r := mustParse(t, `{
	  "rules": [{
	    "match": {"service": "svc"},
	    "consentRequired": true,
	    "matcher": {"type": "json", "owner": "/dataOwner"}
	  }]
	}`)
	_, err := r.Resolve(context.Background(), ResolveRequest{
		Resource: Resource{Service: "svc", Path: "/x"},
		Body:     &Body{Encoding: EncodingJSON, Content: json.RawMessage(`{"unclosed":`)},
	})
	if err == nil {
		t.Fatal("a json matcher must still fail on an undecodable body")
	}
}

func TestResolve_DefaultNoMatch_PassThrough(t *testing.T) {
	r := mustParse(t, `{"rules": [{"match": {"service": "other"}, "consentRequired": true, "matcher": {"type": "static", "owner": "x"}}]}`)
	res, err := r.Resolve(context.Background(), ResolveRequest{Resource: Resource{Service: "unknown", Path: "/x"}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.ConsentRequired || len(res.Claims) != 0 {
		t.Fatalf("expected passthrough default, got %+v", res)
	}
}

func TestResolve_DefaultConsentRequiredFailClosed(t *testing.T) {
	r := mustParse(t, `{"defaultConsentRequired": true, "rules": []}`)
	res, err := r.Resolve(context.Background(), ResolveRequest{Resource: Resource{Path: "/x"}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !res.ConsentRequired || len(res.Claims) != 0 {
		t.Fatalf("expected consentRequired with no claims (fail-closed), got %+v", res)
	}
}

func TestResolve_Errors(t *testing.T) {
	t.Run("json missing owner", func(t *testing.T) {
		r := mustParse(t, `{"rules": [{"match": {}, "consentRequired": true, "matcher": {"type": "json", "owner": "/dataOwner", "resource": "urn:x"}}]}`)
		_, err := r.Resolve(context.Background(), ResolveRequest{Body: jsonBody(t, map[string]interface{}{"id": "no-owner-here"})})
		if err == nil {
			t.Fatal("expected error when owner is absent")
		}
	})
	t.Run("path no match", func(t *testing.T) {
		r := mustParse(t, `{"rules": [{"match": {}, "consentRequired": true, "matcher": {"type": "path", "pattern": "^/files/(?P<owner>[^/]+)$", "resource": "urn:x"}}]}`)
		_, err := r.Resolve(context.Background(), ResolveRequest{Resource: Resource{Path: "/not-a-file"}})
		if err == nil {
			t.Fatal("expected error when path does not match")
		}
	})
	t.Run("json matcher without body", func(t *testing.T) {
		r := mustParse(t, `{"rules": [{"match": {}, "consentRequired": true, "matcher": {"type": "json", "owner": "/o", "resource": "urn:x"}}]}`)
		_, err := r.Resolve(context.Background(), ResolveRequest{Resource: Resource{Path: "/x"}})
		if err == nil {
			t.Fatal("expected error when json matcher has no body")
		}
	})
}

func TestParse_ShippedExampleConfigs(t *testing.T) {
	// The examples are the documentation of the config format; a change that
	// makes them unparseable must fail here rather than at a user's deployment.
	examples, err := filepath.Glob(filepath.Join("..", "..", "config", "*.json"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(examples) == 0 {
		t.Fatal("no example configs found")
	}
	for _, example := range examples {
		t.Run(filepath.Base(example), func(t *testing.T) {
			if _, err := Load(example); err != nil {
				t.Fatalf("Load(%s): %v", example, err)
			}
		})
	}
}

func TestLoad_MissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "does-not-exist.json")); err == nil {
		t.Fatal("expected an error for a missing config file")
	}
}

func TestParse_ConfigErrors(t *testing.T) {
	cases := map[string]string{
		"unknown matcher type":                     `{"rules": [{"match": {}, "matcher": {"type": "bogus"}}]}`,
		"missing matcher type":                     `{"rules": [{"match": {}, "matcher": {}}]}`,
		"path without owner grp":                   `{"rules": [{"match": {}, "matcher": {"type": "path", "pattern": "^/x$"}}]}`,
		"json without owner":                       `{"rules": [{"match": {}, "matcher": {"type": "json", "resource": "urn:x"}}]}`,
		"unknown top field":                        `{"nope": true, "rules": []}`,
		"contractService without url":              `{"contractService": {}, "rules": []}`,
		"contract matcher without contractService": `{"rules": [{"match": {}, "matcher": {"type": "contract", "owner": "/o"}}]}`,
		"contract matcher without owner":           `{"contractService": {"url": "http://facade:8080"}, "rules": [{"match": {}, "matcher": {"type": "contract"}}]}`,
		// consentRequired:false on a contract rule would fetch the governing
		// contract and then throw the claims away unchecked.
		"contract matcher with consentRequired false": `{"contractService": {"url": "http://facade:8080"}, "rules": [{"match": {}, "consentRequired": false, "matcher": {"type": "contract", "owner": "/o"}}]}`,
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(cfg)); err == nil {
				t.Fatalf("expected Parse error for %s", name)
			}
		})
	}
}
