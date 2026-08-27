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
	"testing"
	"time"

	"consent-owner-resolver/internal/contract"
)

// fakeClock lets the TTL be advanced without sleeping.
type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time          { return f.t }
func (f *fakeClock) advance(d time.Duration) { f.t = f.t.Add(d) }

// size reports how many entries the cache currently holds.
func (c *cachingLookup) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

func cachedLookup(t *testing.T, inner contractLookup, ttl time.Duration) (*cachingLookup, *fakeClock) {
	t.Helper()
	clk := &fakeClock{t: time.Unix(0, 0)}
	c, ok := newCachingLookup(inner, ttl).(*cachingLookup)
	if !ok {
		t.Fatalf("newCachingLookup did not return a cache for ttl %v", ttl)
	}
	c.now = clk.now
	return c, clk
}

func TestCachingLookup_ReusesResourcesWithinTTL(t *testing.T) {
	stub := &stubLookup{resources: piiResources()}
	c, clk := cachedLookup(t, stub, time.Minute)
	ct := contractWithTarget(testAssetURI)

	for i := 0; i < 3; i++ {
		if _, err := c.DataResources(context.Background(), ct); err != nil {
			t.Fatalf("DataResources: %v", err)
		}
	}
	if stub.resourceCalls != 1 {
		t.Fatalf("want 1 facade call within the TTL, got %d", stub.resourceCalls)
	}

	clk.advance(2 * time.Minute)
	if _, err := c.DataResources(context.Background(), ct); err != nil {
		t.Fatalf("DataResources after expiry: %v", err)
	}
	if stub.resourceCalls != 2 {
		t.Fatalf("want a refetch after the TTL expired, got %d calls", stub.resourceCalls)
	}
}

func TestCachingLookup_SweepsExpiredEntriesOnWrite(t *testing.T) {
	// `load` treats an expired entry as a miss but leaves it in place, so
	// without a sweep the map only ever grows.
	stub := &stubLookup{resources: piiResources()}
	c, clk := cachedLookup(t, stub, time.Minute)

	for i := 0; i < sweepThreshold; i++ {
		ct := contractWithTarget(fmt.Sprintf("urn:ngsi-ld:PersonalProfile:%d", i))
		if _, err := c.DataResources(context.Background(), ct); err != nil {
			t.Fatalf("DataResources: %v", err)
		}
	}
	if got := c.size(); got != sweepThreshold {
		t.Fatalf("want %d cached entries, got %d", sweepThreshold, got)
	}

	// Everything above is now stale; the next write must clear it out.
	clk.advance(2 * time.Minute)
	fresh := contractWithTarget("urn:ngsi-ld:PersonalProfile:fresh")
	if _, err := c.DataResources(context.Background(), fresh); err != nil {
		t.Fatalf("DataResources: %v", err)
	}
	if got := c.size(); got != 1 {
		t.Fatalf("the sweep must leave only the fresh entry, got %d", got)
	}

	// The survivor must still be the live one, not a stale leftover.
	before := stub.resourceCalls
	if _, err := c.DataResources(context.Background(), fresh); err != nil {
		t.Fatalf("DataResources: %v", err)
	}
	if stub.resourceCalls != before {
		t.Fatal("the entry kept by the sweep must still be a cache hit")
	}
}

func TestCachingLookup_DoesNotCacheErrorsOrUnkeyedContracts(t *testing.T) {
	cases := map[string]struct {
		stub      *stubLookup
		contract  contract.Contract
		wantCalls int
	}{
		// A failing facade must never be remembered as "this contract has no
		// resources" - that would fail OPEN for as long as the TTL lasts.
		"errors are not cached": {
			stub:      &stubLookup{resourcesErr: errors.New("facade down")},
			contract:  contractWithTarget(testAssetURI),
			wantCalls: 2,
		},
		// Without an id two different contracts would share one entry.
		"unkeyed contracts pass through": {
			stub:      &stubLookup{resources: piiResources()},
			contract:  contract.Contract{ServiceOffering: "http://facade/catalog/serviceofferings/x"},
			wantCalls: 2,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c, _ := cachedLookup(t, tc.stub, time.Minute)
			for i := 0; i < 2; i++ {
				_, _ = c.DataResources(context.Background(), tc.contract)
			}
			if tc.stub.resourceCalls != tc.wantCalls {
				t.Fatalf("want %d facade calls, got %d", tc.wantCalls, tc.stub.resourceCalls)
			}
		})
	}
}

func TestResourceCacheTTL(t *testing.T) {
	cases := map[string]struct {
		configured int
		want       time.Duration
	}{
		"unset uses the default": {configured: 0, want: DefaultResourceCacheTTLMs * time.Millisecond},
		"explicit value":         {configured: 1500, want: 1500 * time.Millisecond},
		"negative disables":      {configured: -1, want: -1 * time.Millisecond},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := resourceCacheTTL(tc.configured); got != tc.want {
				t.Fatalf("resourceCacheTTL(%d) = %v, want %v", tc.configured, got, tc.want)
			}
		})
	}
	// A disabled cache must hand back the undecorated lookup.
	stub := &stubLookup{}
	if got := newCachingLookup(stub, resourceCacheTTL(-1)); got != contractLookup(stub) {
		t.Fatalf("a negative ttl must disable the cache, got %T", got)
	}
}
