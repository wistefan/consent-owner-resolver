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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMetrics_ExpositionFormat(t *testing.T) {
	m := newMetrics()
	m.observe(routeResolve, http.StatusOK, 2*time.Millisecond)
	m.observe(routeResolve, http.StatusOK, 40*time.Millisecond)
	m.observe(routeResolve, http.StatusUnprocessableEntity, time.Millisecond)
	m.observeFailure("json matcher")
	m.observeFailure("json matcher")
	m.observeFailure("contract matcher")

	var out strings.Builder
	if err := m.writeTo(&out); err != nil {
		t.Fatalf("writeTo: %v", err)
	}
	got := out.String()

	for _, want := range []string{
		`owner_resolver_requests_total{route="/resolve",status="200"} 2`,
		`owner_resolver_requests_total{route="/resolve",status="422"} 1`,
		// 3 requests, one of which is above 5ms.
		`owner_resolver_request_duration_seconds_bucket{route="/resolve",le="0.005"} 2`,
		`owner_resolver_request_duration_seconds_bucket{route="/resolve",le="+Inf"} 3`,
		`owner_resolver_request_duration_seconds_count{route="/resolve"} 3`,
		`owner_resolver_resolve_failures_total{class="contract matcher"} 1`,
		`owner_resolver_resolve_failures_total{class="json matcher"} 2`,
		"# TYPE owner_resolver_request_duration_seconds histogram",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

func TestMetrics_ConcurrentObservationsAreCounted(t *testing.T) {
	const goroutines, perGoroutine = 8, 50
	m := newMetrics()
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				m.observe(routeResolve, http.StatusOK, time.Millisecond)
				m.observeFailure("json matcher")
			}
		}()
	}
	wg.Wait()

	var out strings.Builder
	if err := m.writeTo(&out); err != nil {
		t.Fatalf("writeTo: %v", err)
	}
	want := goroutines * perGoroutine
	for _, line := range []string{
		`owner_resolver_requests_total{route="/resolve",status="200"} 400`,
		`owner_resolver_resolve_failures_total{class="json matcher"} 400`,
	} {
		if !strings.Contains(out.String(), line) {
			t.Fatalf("want %d observations (%q), got:\n%s", want, line, out.String())
		}
	}
}

func TestHandler_MetricsEndpoint(t *testing.T) {
	h := newTestHandler(t)
	// One success and one failure, so both counters have something in them.
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/resolve",
		strings.NewReader(`{"resource":{"service":"svc","path":"/e/1"},"body":{"encoding":"json","content":{"dataOwner":"alice-42"}}}`)))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/resolve",
		strings.NewReader(`{"resource":{"service":"svc","path":"/ngsi-ld/v1/entities/urn:ngsi-ld:PersonalProfile:alice"},"body":{"encoding":"json","content":{"id":"x"}}}`)))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != prometheusContentType {
		t.Fatalf("unexpected content type %q", ct)
	}

	body := rec.Body.String()
	for _, want := range []string{
		`owner_resolver_requests_total{route="/resolve",status="200"} 1`,
		`owner_resolver_requests_total{route="/resolve",status="422"} 1`,
		`owner_resolver_resolve_failures_total{class="json matcher"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in:\n%s", want, body)
		}
	}
	// Metrics are scrapeable without the shared secret, so they must not carry
	// anything a scrape endpoint should not: no request path, no owner.
	if strings.Contains(body, "alice") || strings.Contains(body, "PersonalProfile") {
		t.Fatalf("metrics leaked request data:\n%s", body)
	}
	if strings.Contains(body, "dataOwner") {
		t.Fatalf("metrics leaked the pointer configuration:\n%s", body)
	}
}

func TestMetrics_FailureClassSetIsClosed(t *testing.T) {
	// /metrics is served WITHOUT the shared secret on the stated grounds that
	// its labels carry no request data. A caller must not be able to mint label
	// values through an error message.
	const attempts = 25
	h := newTestHandler(t)
	for i := 0; i < attempts; i++ {
		body := fmt.Sprintf(`{"resource":{"service":"svc","path":"/e/1"},"body":{"encoding":"attacker-%d","content":"x"}}`, i)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/resolve", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400 for an unknown encoding, got %d", rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	if strings.Contains(body, "attacker-") {
		t.Fatalf("caller-supplied text reached the metrics output:\n%s", body)
	}
	if got := strings.Count(body, metricFailures+"{"); got != 1 {
		t.Fatalf("%d requests must produce ONE failure series, got %d:\n%s", attempts, got, body)
	}
	if want := fmt.Sprintf(`%s{class="decode body"} %d`, metricFailures, attempts); !strings.Contains(body, want) {
		t.Fatalf("missing %q in:\n%s", want, body)
	}
}

func TestMetrics_FailureClassesAreCapped(t *testing.T) {
	// Structural backstop: even if a future error message reintroduces
	// caller-influenced text, the map cannot grow without bound.
	m := newMetrics()
	const extra = 10
	for i := 0; i < maxFailureClasses+extra; i++ {
		m.observeFailure(fmt.Sprintf("class-%d", i))
	}

	var out strings.Builder
	if err := m.writeTo(&out); err != nil {
		t.Fatalf("writeTo: %v", err)
	}
	if got := strings.Count(out.String(), metricFailures+"{"); got != maxFailureClasses+1 {
		t.Fatalf("want %d series (the cap plus the overflow bucket), got %d", maxFailureClasses+1, got)
	}
	if want := fmt.Sprintf(`%s{class=%q} %d`, metricFailures, failureClassOverflow, extra); !strings.Contains(out.String(), want) {
		t.Fatalf("overflowed failures must still be counted; missing %q", want)
	}
}

func TestHandler_MetricsRejectsNonGet(t *testing.T) {
	h := newTestHandler(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/metrics", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", rec.Code)
	}
}

func TestRequestID(t *testing.T) {
	cases := map[string]struct {
		given      string
		wantEchoed bool
	}{
		"a usable id is reused":        {given: "plugin-req-42", wantEchoed: true},
		"no header mints one":          {given: "", wantEchoed: false},
		"an overlong id is rejected":   {given: strings.Repeat("x", maxRequestIDLength+1), wantEchoed: false},
		"a control character is not":   {given: "abc\ndef", wantEchoed: false},
		"a tab is a control character": {given: "abc\tdef", wantEchoed: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newTestHandler(t)
			req := httptest.NewRequest(http.MethodGet, "/health", nil)
			if tc.given != "" {
				req.Header.Set(RequestIDHeader, tc.given)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			echoed := rec.Header().Get(RequestIDHeader)
			if echoed == "" {
				t.Fatal("every response must carry a correlation id")
			}
			if tc.wantEchoed && echoed != tc.given {
				t.Fatalf("caller's id must be reused: got %q, want %q", echoed, tc.given)
			}
			if !tc.wantEchoed && echoed == tc.given {
				t.Fatalf("an unusable id must not be echoed back: %q", echoed)
			}
		})
	}
}

func TestHandler_LogsTheRequestID(t *testing.T) {
	const id = "plugin-req-42"
	buf := captureLog(t)
	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/resolve",
		strings.NewReader(`{"resource":{"service":"svc","path":"/e/1"},"body":{"encoding":"json","content":{"id":"x"}}}`))
	req.Header.Set(RequestIDHeader, id)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !strings.Contains(buf.String(), id) {
		t.Fatalf("the failure log must carry the correlation id: %s", buf.String())
	}
}
