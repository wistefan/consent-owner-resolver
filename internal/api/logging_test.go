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
	"bytes"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRedactPath(t *testing.T) {
	const alicePath = "/ngsi-ld/v1/entities/urn:ngsi-ld:PersonalProfile:alice"

	alice, again := redactPath(alicePath), redactPath(alicePath)
	if strings.Contains(alice, "alice") {
		t.Fatalf("the owner must not survive redaction, got %q", alice)
	}
	if alice != again {
		t.Fatal("the digest must be stable so the same path correlates across log lines")
	}
	if bob := redactPath("/ngsi-ld/v1/entities/urn:ngsi-ld:PersonalProfile:bob"); alice == bob {
		t.Fatal("different paths must digest differently")
	}
	if got := redactPath(""); got != pathDigestPrefix+"empty" {
		t.Fatalf("empty path: got %q", got)
	}
}

func TestErrorClass(t *testing.T) {
	cases := map[string]struct {
		err  error
		want string
	}{
		"drops the pointer config": {err: errors.New(`json matcher: no owner at "/dataOwner"`), want: "json matcher"},
		"drops the requested uri":  {err: errors.New(`contract matcher: no contract rule targets "urn:ngsi-ld:PersonalProfile:alice"`), want: "contract matcher"},
		"message without a detail": {err: errors.New("boom"), want: "boom"},
		"nil error":                {err: nil, want: ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := errorClass(tc.err); got != tc.want {
				t.Fatalf("errorClass = %q, want %q", got, tc.want)
			}
		})
	}
}

// captureLog redirects the standard logger for the duration of a test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	flags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(nil)
		log.SetFlags(flags)
	})
	return &buf
}

func TestHandler_DoesNotLeakOwnersInLogsOrResponses(t *testing.T) {
	// The path IS the subject id in the documented NGSI-LD shape, and the error
	// quotes this deployment's pointer configuration. Neither may reach the
	// default log or the response body.
	const body = `{"resource":{"service":"svc","method":"GET","path":"/ngsi-ld/v1/entities/urn:ngsi-ld:PersonalProfile:alice"},"body":{"encoding":"json","content":{"id":"x"}}}`

	for _, debug := range []bool{false, true} {
		name := "default level"
		if debug {
			name = "debug level"
		}
		t.Run(name, func(t *testing.T) {
			buf := captureLog(t)
			h := newTestHandlerWithOptions(t, Options{Debug: debug})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/resolve", strings.NewReader(body)))

			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("want 422, got %d", rec.Code)
			}
			// The response body is generic at BOTH levels: debug is a logging
			// choice, never a disclosure one.
			if strings.Contains(rec.Body.String(), "alice") || strings.Contains(rec.Body.String(), "dataOwner") {
				t.Fatalf("response leaked request data or config: %s", rec.Body.String())
			}

			logged := buf.String()
			if !strings.Contains(logged, `service="svc"`) {
				t.Fatalf("the service must stay loggable for correlation: %s", logged)
			}
			if debug {
				if !strings.Contains(logged, "alice") || !strings.Contains(logged, "dataOwner") {
					t.Fatalf("debug must log the full path and error: %s", logged)
				}
				return
			}
			if strings.Contains(logged, "alice") {
				t.Fatalf("default level leaked the owner id: %s", logged)
			}
			if strings.Contains(logged, "dataOwner") {
				t.Fatalf("default level leaked the pointer config: %s", logged)
			}
			if !strings.Contains(logged, pathDigestPrefix) {
				t.Fatalf("default level must log a path digest: %s", logged)
			}
		})
	}
}
