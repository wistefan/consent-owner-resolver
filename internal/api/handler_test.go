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

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"consent-owner-resolver/internal/resolver"
)

func newTestHandler(t *testing.T) http.Handler {
	return newTestHandlerWithOptions(t, Options{})
}

func newTestHandlerWithOptions(t *testing.T, opts Options) http.Handler {
	t.Helper()
	r, err := resolver.Parse([]byte(`{
	  "rules": [{
	    "match": {"service": "svc"},
	    "consentRequired": true,
	    "matcher": {"type": "json", "owner": "/dataOwner", "resource": "urn:x"}
	  }]
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return NewHandler(r, opts)
}

func TestHandler_SharedSecret(t *testing.T) {
	const token = "s3cr3t"
	body := `{"resource":{"service":"svc","path":"/e/1"},"body":{"encoding":"json","content":{"dataOwner":"alice-42"}}}`
	cases := map[string]struct {
		authorization string
		wantStatus    int
	}{
		"correct secret":   {authorization: "Bearer " + token, wantStatus: http.StatusOK},
		"wrong secret":     {authorization: "Bearer nope", wantStatus: http.StatusUnauthorized},
		"no header at all": {authorization: "", wantStatus: http.StatusUnauthorized},
		"missing scheme":   {authorization: token, wantStatus: http.StatusUnauthorized},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newTestHandlerWithOptions(t, Options{AuthToken: token})
			req := httptest.NewRequest(http.MethodPost, "/resolve", strings.NewReader(body))
			if tc.authorization != "" {
				req.Header.Set("Authorization", tc.authorization)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("want %d, got %d (%s)", tc.wantStatus, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandler_HealthStaysUnauthenticated(t *testing.T) {
	// Liveness probes carry no credentials, and /health exposes no data.
	h := newTestHandlerWithOptions(t, Options{AuthToken: "s3cr3t"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestHandler_ResolveOK(t *testing.T) {
	h := newTestHandler(t)
	body := `{"resource":{"service":"svc","path":"/e/1"},"body":{"encoding":"json","content":{"dataOwner":"alice-42"}}}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/resolve", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"alice-42"`) || !strings.Contains(rec.Body.String(), `"consentRequired":true`) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestHandler_BadJSON(t *testing.T) {
	h := newTestHandler(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/resolve", strings.NewReader(`{bad`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestHandler_UnresolvableIsClientError(t *testing.T) {
	h := newTestHandler(t)
	// matches the rule but has no owner -> resolver errors -> 422 (plugin fails closed)
	body := `{"resource":{"service":"svc","path":"/e/1"},"body":{"encoding":"json","content":{"id":"x"}}}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/resolve", strings.NewReader(body)))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestHandler_StatusCodesMatchTheDocumentedContract(t *testing.T) {
	// The plugin keys its fail policy off these codes, so the split matters:
	// 400 = the caller's payload is broken, 422 = the owner is unresolvable.
	cases := map[string]struct {
		body       string
		wantStatus int
	}{
		"valid json, no owner in it": {body: `{"resource":{"service":"svc","path":"/e/1"},"body":{"encoding":"json","content":"not-an-object"}}`, wantStatus: http.StatusUnprocessableEntity},
		"illegal base64 body":        {body: `{"resource":{"service":"svc","path":"/e/1"},"body":{"encoding":"base64","content":"!!!not-base64!!!"}}`, wantStatus: http.StatusBadRequest},
		"unknown body encoding":      {body: `{"resource":{"service":"svc","path":"/e/1"},"body":{"encoding":"protobuf","content":"AAA"}}`, wantStatus: http.StatusBadRequest},
		// Opaque base64 bytes that are not JSON are not a decode failure: the
		// body is simply unusable to a json matcher.
		"opaque base64 bytes":  {body: `{"resource":{"service":"svc","path":"/e/1"},"body":{"encoding":"base64","content":"eyJ1bmNsb3NlZCI6"}}`, wantStatus: http.StatusUnprocessableEntity},
		"owner not resolvable": {body: `{"resource":{"service":"svc","path":"/e/1"},"body":{"encoding":"json","content":{"id":"x"}}}`, wantStatus: http.StatusUnprocessableEntity},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newTestHandler(t)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/resolve", strings.NewReader(tc.body)))
			if rec.Code != tc.wantStatus {
				t.Fatalf("want %d, got %d (%s)", tc.wantStatus, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	h := newTestHandler(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/resolve", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", rec.Code)
	}
}

func TestHandler_Health(t *testing.T) {
	h := newTestHandler(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ok") {
		t.Fatalf("health failed: %d %s", rec.Code, rec.Body.String())
	}
}
