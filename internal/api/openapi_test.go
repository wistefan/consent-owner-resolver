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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// specPath is the OpenAPI document this package's responses are checked against.
const specPath = "../../api/openapi.json"

// schema is the subset of JSON Schema the drift check needs.
type schema struct {
	Type       string            `json:"type"`
	Required   []string          `json:"required"`
	Properties map[string]schema `json:"properties"`
	Enum       []string          `json:"enum"`
	Ref        string            `json:"$ref"`
	Items      *schema           `json:"items"`
}

// openAPI is the subset of the document the drift check needs.
type openAPI struct {
	OpenAPI    string                    `json:"openapi"`
	Paths      map[string]map[string]any `json:"paths"`
	Components struct {
		Schemas map[string]schema `json:"schemas"`
	} `json:"components"`
}

func loadSpec(t *testing.T) openAPI {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(specPath))
	if err != nil {
		t.Fatalf("read %s: %v", specPath, err)
	}
	var spec openAPI
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse %s: %v", specPath, err)
	}
	return spec
}

// resolveRef follows a local `$ref` to the schema it names.
func resolveRef(t *testing.T, spec openAPI, s schema) schema {
	t.Helper()
	if s.Ref == "" {
		return s
	}
	const prefix = "#/components/schemas/"
	if !strings.HasPrefix(s.Ref, prefix) {
		t.Fatalf("unsupported $ref %q", s.Ref)
	}
	name := strings.TrimPrefix(s.Ref, prefix)
	target, ok := spec.Components.Schemas[name]
	if !ok {
		t.Fatalf("$ref %q names a schema that does not exist", s.Ref)
	}
	return target
}

// assertMatchesSchema checks a decoded JSON value against a spec schema: no
// undocumented property, every required property present, every enum honoured.
//
// This is the drift check, not a general validator - the request and response
// shapes are the integration contract with a separately-released Lua plugin, and
// the cheapest defence against drift is a test that reads the spec.
func assertMatchesSchema(t *testing.T, spec openAPI, s schema, value any, path string) {
	t.Helper()
	s = resolveRef(t, spec, s)

	switch actual := value.(type) {
	case map[string]any:
		for name := range actual {
			if _, documented := s.Properties[name]; !documented {
				t.Errorf("%s: property %q is not in the OpenAPI spec", path, name)
			}
		}
		for _, name := range s.Required {
			if _, present := actual[name]; !present {
				t.Errorf("%s: required property %q is missing from the response", path, name)
			}
		}
		for name, sub := range s.Properties {
			if v, present := actual[name]; present {
				assertMatchesSchema(t, spec, sub, v, path+"."+name)
			}
		}
	case []any:
		if s.Type != "array" {
			t.Errorf("%s: got an array, spec says %q", path, s.Type)
			return
		}
		for _, item := range actual {
			assertMatchesSchema(t, spec, *s.Items, item, path+"[]")
		}
	case string:
		if len(s.Enum) > 0 && !contains(s.Enum, actual) {
			t.Errorf("%s: value %q is not one of the documented values %v", path, actual, s.Enum)
		}
	}
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func TestOpenAPI_DocumentsEveryRoute(t *testing.T) {
	spec := loadSpec(t)
	if spec.OpenAPI == "" {
		t.Fatal("the spec declares no openapi version")
	}
	documented := make([]string, 0, len(spec.Paths))
	for path := range spec.Paths {
		documented = append(documented, path)
	}
	sort.Strings(documented)

	want := []string{routeHealth, routeMetrics, routeResolve}
	sort.Strings(want)
	if strings.Join(documented, ",") != strings.Join(want, ",") {
		t.Fatalf("spec documents %v, the handler serves %v", documented, want)
	}
}

func TestOpenAPI_ResponsesMatchTheSpec(t *testing.T) {
	// Every documented response shape, driven through the real handler. A field
	// added to ResolveResult without a spec entry fails here.
	spec := loadSpec(t)
	h := newTestHandler(t)

	cases := map[string]struct {
		request    *http.Request
		schemaRef  string
		wantStatus int
	}{
		"resolved payload": {
			request:    httptest.NewRequest(http.MethodPost, "/resolve", strings.NewReader(`{"resource":{"service":"svc","method":"GET","path":"/e/1","contentType":"application/json"},"body":{"encoding":"json","content":{"dataOwner":"alice-42"}}}`)),
			schemaRef:  "ResolveResult",
			wantStatus: http.StatusOK,
		},
		"no rule matched": {
			request:    httptest.NewRequest(http.MethodPost, "/resolve", strings.NewReader(`{"resource":{"service":"unknown","path":"/x"}}`)),
			schemaRef:  "ResolveResult",
			wantStatus: http.StatusOK,
		},
		"owner unresolvable": {
			request:    httptest.NewRequest(http.MethodPost, "/resolve", strings.NewReader(`{"resource":{"service":"svc","path":"/e/1"},"body":{"encoding":"json","content":{"id":"x"}}}`)),
			schemaRef:  "Error",
			wantStatus: http.StatusUnprocessableEntity,
		},
		"undecodable payload": {
			request:    httptest.NewRequest(http.MethodPost, "/resolve", strings.NewReader(`{"resource":{"service":"svc","path":"/e/1"},"body":{"encoding":"base64","content":"!!!"}}`)),
			schemaRef:  "Error",
			wantStatus: http.StatusBadRequest,
		},
		"method not allowed": {
			request:    httptest.NewRequest(http.MethodGet, "/resolve", nil),
			schemaRef:  "Error",
			wantStatus: http.StatusMethodNotAllowed,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, tc.request)
			if rec.Code != tc.wantStatus {
				t.Fatalf("want %d, got %d (%s)", tc.wantStatus, rec.Code, rec.Body.String())
			}
			assertDocumentedResponse(t, spec, tc.schemaRef, rec.Body.Bytes())
			assertStatusIsDocumented(t, spec, "/resolve", rec.Code)
		})
	}
}

func TestOpenAPI_HealthResponseMatchesTheSpec(t *testing.T) {
	spec := loadSpec(t)
	h := newTestHandler(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	var decoded any
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	healthSchema := responseSchema(t, spec, routeHealth, http.MethodGet, http.StatusOK)
	assertMatchesSchema(t, spec, healthSchema, decoded, "health")
}

// assertDocumentedResponse checks a response body against a named component
// schema.
func assertDocumentedResponse(t *testing.T, spec openAPI, schemaName string, body []byte) {
	t.Helper()
	s, ok := spec.Components.Schemas[schemaName]
	if !ok {
		t.Fatalf("the spec has no schema %q", schemaName)
	}
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	assertMatchesSchema(t, spec, s, decoded, schemaName)
}

// assertStatusIsDocumented fails when the handler produced a status the spec
// does not list for that path.
func assertStatusIsDocumented(t *testing.T, spec openAPI, path string, status int) {
	t.Helper()
	for _, operation := range spec.Paths[path] {
		op, ok := operation.(map[string]any)
		if !ok {
			continue
		}
		responses, ok := op["responses"].(map[string]any)
		if !ok {
			continue
		}
		if _, documented := responses[strconv.Itoa(status)]; documented {
			return
		}
	}
	t.Errorf("%s answers %d, which the spec does not document", path, status)
}

// responseSchema digs out the JSON schema documented for one path/method/status.
func responseSchema(t *testing.T, spec openAPI, path, method string, status int) schema {
	t.Helper()
	operation, ok := spec.Paths[path][strings.ToLower(method)].(map[string]any)
	if !ok {
		t.Fatalf("the spec has no %s %s", method, path)
	}
	raw, err := json.Marshal(operation)
	if err != nil {
		t.Fatalf("re-marshal operation: %v", err)
	}
	var typed struct {
		Responses map[string]struct {
			Content map[string]struct {
				Schema schema `json:"schema"`
			} `json:"content"`
		} `json:"responses"`
	}
	if err := json.Unmarshal(raw, &typed); err != nil {
		t.Fatalf("decode operation: %v", err)
	}
	response, ok := typed.Responses[strconv.Itoa(status)]
	if !ok {
		t.Fatalf("the spec documents no %d for %s %s", status, method, path)
	}
	for _, content := range response.Content {
		return content.Schema
	}
	t.Fatalf("the spec documents no content for %d on %s %s", status, method, path)
	return schema{}
}
