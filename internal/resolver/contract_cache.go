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
	"sync"
	"time"

	"consent-owner-resolver/internal/contract"
)

// sweepThreshold is the entry count at which a write purges expired entries.
//
// `load` treats an expired entry as a miss but cannot delete it (it holds the
// read path and would turn every lookup into a write), so without this nothing
// ever shrinks the map. Keys are contract ids from the facade, so the map is
// bounded by the contracts the provider has actually served - fine in practice,
// unbounded in principle. Sweeping on write, and only past a threshold, keeps
// the common case O(1).
const sweepThreshold = 128

// DefaultResourceCacheTTLMs is how long a contract's catalog data resources are
// reused across requests when the config does not say otherwise. Catalog
// self-descriptions change on the timescale of contract negotiation, not of API
// traffic, so a short TTL removes almost all facade load while keeping the
// resolver responsive to a re-published catalog.
const DefaultResourceCacheTTLMs = 30000

// clock is the time source, replaceable in tests.
type clock func() time.Time

// cachingLookup decorates a contractLookup with a short-TTL, per-contract cache
// of the catalog data resources.
//
// Resolving a contract's resources costs 1 + len(offering.dataResources) HTTP
// GETs against the facade, and it happens on the synchronous path of every
// proxied API call. Contracts themselves are NOT cached: they carry the
// signature state the resolver must see promptly (see the status filter in
// contractMatcher), so only the far more static catalog side is memoized.
type cachingLookup struct {
	contractLookup
	ttl   time.Duration
	now   clock
	mu    sync.Mutex
	items map[string]cacheEntry
}

type cacheEntry struct {
	resources []contract.DataResource
	expires   time.Time
}

// newCachingLookup wraps inner so DataResources results are reused for ttl. A
// ttl of zero or less disables caching and returns inner unchanged.
func newCachingLookup(inner contractLookup, ttl time.Duration) contractLookup {
	if inner == nil || ttl <= 0 {
		return inner
	}
	return &cachingLookup{
		contractLookup: inner,
		ttl:            ttl,
		now:            time.Now,
		items:          map[string]cacheEntry{},
	}
}

// DataResources returns the cached resources of c when the entry is still fresh,
// otherwise delegates and caches the result. Errors are never cached: a failing
// facade must not be remembered as "this contract has no resources".
func (c *cachingLookup) DataResources(ctx context.Context, ct contract.Contract) ([]contract.DataResource, error) {
	// A contract with no id cannot be keyed safely - two different contracts
	// would share one entry. Pass those straight through.
	if ct.ID == "" {
		return c.contractLookup.DataResources(ctx, ct)
	}
	if cached, ok := c.load(ct.ID); ok {
		return cached, nil
	}
	resources, err := c.contractLookup.DataResources(ctx, ct)
	if err != nil {
		return nil, err
	}
	c.store(ct.ID, resources)
	return resources, nil
}

func (c *cachingLookup) load(id string) ([]contract.DataResource, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[id]
	if !ok || c.now().After(entry.expires) {
		return nil, false
	}
	return entry.resources, true
}

func (c *cachingLookup) store(id string, resources []contract.DataResource) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	if len(c.items) >= sweepThreshold {
		c.sweepExpired(now)
	}
	c.items[id] = cacheEntry{resources: resources, expires: now.Add(c.ttl)}
}

// sweepExpired drops every entry that is past its TTL. The caller holds c.mu.
func (c *cachingLookup) sweepExpired(now time.Time) {
	for id, entry := range c.items {
		if now.After(entry.expires) {
			delete(c.items, id)
		}
	}
}
