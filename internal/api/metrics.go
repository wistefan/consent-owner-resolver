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
	"io"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Metric names, in the Prometheus text exposition format. The service has no
// dependencies beyond the standard library, and adding a client library for
// three metrics would not be worth the supply-chain surface - the format is a
// few lines of text.
const (
	metricRequests = "owner_resolver_requests_total"
	metricDuration = "owner_resolver_request_duration_seconds"
	metricFailures = "owner_resolver_resolve_failures_total"
)

// durationBuckets are the histogram bounds in seconds. They straddle what
// matters operationally: a resolve with no facade call is sub-millisecond, one
// that queries the consent-facade costs a round-trip, and the facade client's
// own timeout is 3s — so the tail is where the buckets are dense.
var durationBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// requestKey labels one entry of the request counter. Only the ROUTE is used,
// never the request path: the path carries owner identifiers (see redactPath),
// and a per-path metric would be both a leak and unbounded cardinality.
type requestKey struct {
	route  string
	status int
}

// metrics collects the three numbers that matter for a service on the
// synchronous path of every proxied request: how many requests, how they ended,
// and how long they took. Safe for concurrent use.
type metrics struct {
	mu sync.Mutex
	// requests counts responses by route and status code.
	requests map[requestKey]uint64
	// failures counts resolve errors by error class (see errorClass) - which
	// component failed, never the detail.
	failures map[string]uint64
	// bucketCounts holds, per route, the count in each durationBuckets slot.
	bucketCounts map[string][]uint64
	durationSum  map[string]float64
	requestCount map[string]uint64
}

func newMetrics() *metrics {
	return &metrics{
		requests:     map[requestKey]uint64{},
		failures:     map[string]uint64{},
		bucketCounts: map[string][]uint64{},
		durationSum:  map[string]float64{},
		requestCount: map[string]uint64{},
	}
}

// observe records one completed request.
func (m *metrics) observe(route string, status int, elapsed time.Duration) {
	seconds := elapsed.Seconds()

	m.mu.Lock()
	defer m.mu.Unlock()

	m.requests[requestKey{route: route, status: status}]++
	m.requestCount[route]++
	m.durationSum[route] += seconds

	counts, ok := m.bucketCounts[route]
	if !ok {
		counts = make([]uint64, len(durationBuckets))
		m.bucketCounts[route] = counts
	}
	for i, bound := range durationBuckets {
		if seconds <= bound {
			counts[i]++
		}
	}
}

// observeFailure records one resolve error, by class.
func (m *metrics) observeFailure(class string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures[class]++
}

// metricsWriter latches the first write error so the exposition can be emitted
// as a flat sequence of prints rather than an error check per line.
type metricsWriter struct {
	w   io.Writer
	err error
}

// printf writes one formatted line unless a previous write already failed.
func (mw *metricsWriter) printf(format string, args ...interface{}) {
	if mw.err != nil {
		return
	}
	_, mw.err = fmt.Fprintf(mw.w, format, args...)
}

// writeTo renders the collected metrics in the Prometheus text exposition
// format, returning the first write error. Output is sorted, so scrapes and
// tests see a stable ordering.
func (m *metrics) writeTo(w io.Writer) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	mw := &metricsWriter{w: w}

	mw.printf("# HELP %s Responses served, by route and status code.\n", metricRequests)
	mw.printf("# TYPE %s counter\n", metricRequests)
	for _, key := range sortedRequestKeys(m.requests) {
		mw.printf("%s{route=%q,status=%q} %d\n", metricRequests, key.route, strconv.Itoa(key.status), m.requests[key])
	}

	mw.printf("# HELP %s Request duration in seconds, by route.\n", metricDuration)
	mw.printf("# TYPE %s histogram\n", metricDuration)
	for _, route := range sortedKeys(m.bucketCounts) {
		counts := m.bucketCounts[route]
		for i, bound := range durationBuckets {
			mw.printf("%s_bucket{route=%q,le=%q} %d\n", metricDuration, route, strconv.FormatFloat(bound, 'g', -1, 64), counts[i])
		}
		mw.printf("%s_bucket{route=%q,le=\"+Inf\"} %d\n", metricDuration, route, m.requestCount[route])
		mw.printf("%s_sum{route=%q} %s\n", metricDuration, route, strconv.FormatFloat(m.durationSum[route], 'g', -1, 64))
		mw.printf("%s_count{route=%q} %d\n", metricDuration, route, m.requestCount[route])
	}

	mw.printf("# HELP %s Resolve failures, by error class.\n", metricFailures)
	mw.printf("# TYPE %s counter\n", metricFailures)
	for _, class := range sortedKeys(m.failures) {
		mw.printf("%s{class=%q} %d\n", metricFailures, class, m.failures[class])
	}
	return mw.err
}

// sortedKeys returns the map's keys in a stable order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sortedRequestKeys orders request counter keys by route, then status.
func sortedRequestKeys(m map[requestKey]uint64) []requestKey {
	keys := make([]requestKey, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].route != keys[j].route {
			return keys[i].route < keys[j].route
		}
		return keys[i].status < keys[j].status
	})
	return keys
}
