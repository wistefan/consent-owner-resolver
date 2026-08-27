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
	"bytes"
	"consent-owner-resolver/internal/contract"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"time"
)

// Matcher type identifiers used in configuration.
const (
	matcherPath     = "path"
	matcherJSON     = "json"
	matcherStatic   = "static"
	matcherContract = "contract"
)

// defaultScheme is used when the config sets no scheme; the consent-manager
// resolves owners by their per-participant identifier.
const defaultScheme = "identifier"

// rawConfig is the on-disk (JSON) configuration.
type rawConfig struct {
	// DefaultConsentRequired is returned when no rule matches. Default false
	// (data not governed by this resolver passes through). Set true to fail
	// closed on unknown data.
	DefaultConsentRequired bool `json:"defaultConsentRequired"`
	// DefaultScheme interprets owner ids: identifier | email | did.
	DefaultScheme string `json:"defaultScheme"`
	// ContractService points at a consent-facade used by `contract` matchers to
	// find the governing contract and its catalog resources.
	ContractService *rawContractService `json:"contractService"`
	// Rules are evaluated top-down; the first whose Match matches wins.
	Rules []rawRule `json:"rules"`
}

// rawContractService configures the consent-facade lookup.
type rawContractService struct {
	// URL is the facade base url, e.g. http://consent-facade:8080.
	URL string `json:"url"`
	// ProviderSelfDescription is this provider's participant SD url, one side of
	// every contract lookup.
	ProviderSelfDescription string `json:"providerSelfDescription"`
	// TimeoutMs bounds each facade call (default contract.DefaultTimeoutMs).
	TimeoutMs int `json:"timeoutMs"`
	// ResourceCacheTtlMs is how long a contract's catalog data resources are
	// reused across requests (default DefaultResourceCacheTTLMs). Negative
	// disables the cache.
	ResourceCacheTtlMs int `json:"resourceCacheTtlMs"`
}

type rawRule struct {
	Name            string     `json:"name"`
	Match           rawMatch   `json:"match"`
	ConsentRequired bool       `json:"consentRequired"`
	Matcher         rawMatcher `json:"matcher"`
}

// rawMatch selects which requests a rule applies to.
type rawMatch struct {
	// Service, when set, must equal ResolveRequest.Resource.Service.
	Service string `json:"service"`
	// PathPattern, when set, is a regexp that must match Resource.Path.
	PathPattern string `json:"pathPattern"`
}

// rawMatcher is the union of all matcher configs (only the fields for the chosen
// Type are used).
type rawMatcher struct {
	Type string `json:"type"`
	// path matcher
	Pattern string `json:"pattern"`
	// json matcher
	Items           string `json:"items"`
	ItemsIsArray    bool   `json:"itemsIsArray"`
	Owner           string `json:"owner"`
	ResourcePointer string `json:"resourcePointer"`
	// contract matcher
	URIPointer string `json:"uriPointer"`
	// shared
	Resource    string `json:"resource"`
	Participant string `json:"participant"`
	Scheme      string `json:"scheme"`
}

// compiledRule is a rule ready for evaluation.
type compiledRule struct {
	name            string
	service         string
	pathRe          *regexp.Regexp
	consentRequired bool
	scheme          string
	matcher         Matcher
}

func (r compiledRule) matches(req ResolveRequest) bool {
	if r.service != "" && r.service != req.Resource.Service {
		return false
	}
	if r.pathRe != nil && !r.pathRe.MatchString(req.Resource.Path) {
		return false
	}
	return true
}

// ConfigResolver is a Resolver backed by an ordered list of rules.
type ConfigResolver struct {
	defaultConsentRequired bool
	defaultScheme          string
	rules                  []compiledRule
}

// Load reads and compiles a resolver configuration from a JSON file.
func Load(path string) (*ConfigResolver, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	return Parse(data)
}

// Parse compiles a resolver configuration from JSON bytes.
func Parse(data []byte) (*ConfigResolver, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var raw rawConfig
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	cr := &ConfigResolver{
		defaultConsentRequired: raw.DefaultConsentRequired,
		defaultScheme:          raw.DefaultScheme,
	}
	if cr.defaultScheme == "" {
		cr.defaultScheme = defaultScheme
	}

	var contractClient contractLookup
	providerSD := ""
	if raw.ContractService != nil {
		if raw.ContractService.URL == "" {
			return nil, fmt.Errorf("contractService.url is required when contractService is set")
		}
		contractClient = newCachingLookup(
			contract.NewClient(raw.ContractService.URL, raw.ContractService.TimeoutMs),
			resourceCacheTTL(raw.ContractService.ResourceCacheTtlMs),
		)
		providerSD = raw.ContractService.ProviderSelfDescription
	}

	for i, rr := range raw.Rules {
		label := rr.Name
		if label == "" {
			label = fmt.Sprintf("#%d", i)
		}
		var pathRe *regexp.Regexp
		if rr.Match.PathPattern != "" {
			re, err := regexp.Compile(rr.Match.PathPattern)
			if err != nil {
				return nil, fmt.Errorf("rule %s: invalid match.pathPattern: %w", label, err)
			}
			pathRe = re
		}
		matcher, err := buildMatcher(rr.Matcher, contractClient, providerSD)
		if err != nil {
			return nil, fmt.Errorf("rule %s: %w", label, err)
		}
		scheme := rr.Matcher.Scheme
		if scheme == "" {
			scheme = cr.defaultScheme
		}
		cr.rules = append(cr.rules, compiledRule{
			name:            label,
			service:         rr.Match.Service,
			pathRe:          pathRe,
			consentRequired: rr.ConsentRequired,
			scheme:          scheme,
			matcher:         matcher,
		})
	}
	return cr, nil
}

// resourceCacheTTL maps the configured value onto a duration: unset (0) means
// the default, a negative value disables the cache.
func resourceCacheTTL(ms int) time.Duration {
	if ms == 0 {
		ms = DefaultResourceCacheTTLMs
	}
	return time.Duration(ms) * time.Millisecond
}

func buildMatcher(m rawMatcher, contractClient contractLookup, providerSD string) (Matcher, error) {
	switch m.Type {
	case matcherPath:
		return newPathMatcher(m)
	case matcherJSON:
		return newJSONMatcher(m)
	case matcherStatic:
		return newStaticMatcher(m)
	case matcherContract:
		return newContractMatcher(m, contractClient, providerSD)
	case "":
		return nil, fmt.Errorf("matcher.type is required (one of %q, %q, %q, %q)", matcherPath, matcherJSON, matcherStatic, matcherContract)
	default:
		return nil, fmt.Errorf("unknown matcher.type %q", m.Type)
	}
}

// Resolve implements Resolver: it decodes the body once, finds the first
// matching rule, and delegates claim extraction to that rule's matcher. When no
// rule matches it returns the configured default.
func (r *ConfigResolver) Resolve(ctx context.Context, req ResolveRequest) (ResolveResult, error) {
	decoded, err := decodeJSONBody(req.Body)
	if err != nil {
		return ResolveResult{}, err
	}
	for _, rule := range r.rules {
		if !rule.matches(req) {
			continue
		}
		claims, err := rule.matcher.Claims(ctx, req, decoded)
		if err != nil {
			return ResolveResult{}, err
		}
		return ResolveResult{
			ConsentRequired: rule.consentRequired,
			Scheme:          rule.scheme,
			Claims:          claims,
		}, nil
	}
	return ResolveResult{
		ConsentRequired: r.defaultConsentRequired,
		Scheme:          r.defaultScheme,
	}, nil
}
