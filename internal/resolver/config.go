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
	"slices"
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
const defaultScheme = SchemeIdentifier

// validSchemes is the closed set a configured scheme must belong to. A
// misspelled scheme would otherwise ship to the plugin and change which lookup
// the consent check performs, with nothing anywhere to notice it.
var validSchemes = []string{SchemeIdentifier, SchemeEmail, SchemeDID}

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

// isCatchAll reports whether the rule matches every request. Rules are
// first-match, so a catch-all is legitimate as the LAST rule (an explicit
// fallback) and makes every rule after it dead.
func (r compiledRule) isCatchAll() bool {
	return r.service == "" && r.pathRe == nil
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
	// #nosec G304 -- the path is operator-supplied (CONFIG_PATH), never request
	// input, so there is no untrusted value to traverse with.
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
	if err := validateScheme("defaultScheme", cr.defaultScheme); err != nil {
		return nil, err
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
		// A contract rule answering "no consent needed" is a contradiction: the
		// matcher would query the facade for the governing contract and the
		// plugin would then discard the claims unchecked. Reject it rather than
		// letting a typo silently ungate personal data.
		if rr.Matcher.Type == matcherContract && !rr.ConsentRequired {
			return nil, fmt.Errorf("rule %s: a %q matcher requires consentRequired:true (containsPII selects which resource a claim names, it does not decide whether consent is checked)", label, matcherContract)
		}
		matcher, err := buildMatcher(rr.Matcher, contractClient, providerSD)
		if err != nil {
			return nil, fmt.Errorf("rule %s: %w", label, err)
		}
		scheme := rr.Matcher.Scheme
		if scheme == "" {
			scheme = cr.defaultScheme
		}
		if err := validateScheme(fmt.Sprintf("rule %s: matcher.scheme", label), scheme); err != nil {
			return nil, err
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
	if err := rejectDeadRules(cr.rules); err != nil {
		return nil, err
	}
	return cr, nil
}

// rejectDeadRules fails a config in which a rule can never be reached.
//
// Rules are first-match and an empty `match` matches everything, so a catch-all
// anywhere but last silently disables every rule below it - and for a component
// that gates personal data, a rule the operator believes is active but is not is
// the worst kind of quiet. As the last rule a catch-all is a deliberate
// fallback and stays allowed.
func rejectDeadRules(rules []compiledRule) error {
	for i, rule := range rules {
		if rule.isCatchAll() && i < len(rules)-1 {
			return fmt.Errorf("rule %s has an empty match, so it matches every request and rule %s (and any after it) can never be reached; move it last or give it a match",
				rule.name, rules[i+1].name)
		}
	}
	return nil
}

// validateScheme rejects a scheme outside validSchemes, naming where it came
// from so the operator does not have to guess which rule is at fault.
func validateScheme(field, scheme string) error {
	if slices.Contains(validSchemes, scheme) {
		return nil
	}
	return fmt.Errorf("%s: unknown scheme %q (expected one of %v)", field, scheme, validSchemes)
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

// Resolve implements Resolver: it finds the first matching rule and delegates
// claim extraction to that rule's matcher. When no rule matches it returns the
// configured default.
//
// The body is decoded lazily and at most once (see lazyPayload): a rule whose
// matcher never reads it - `path`, `static` - must still answer when the payload
// is undecodable, and no rule should pay for a decode it does not use.
func (r *ConfigResolver) Resolve(ctx context.Context, req ResolveRequest) (ResolveResult, error) {
	body := newLazyPayload(req.Body)
	for _, rule := range r.rules {
		if !rule.matches(req) {
			continue
		}
		claims, err := rule.matcher.Claims(ctx, req, body)
		if err != nil {
			return ResolveResult{}, err
		}
		return ResolveResult{
			ConsentRequired: rule.consentRequired,
			Scheme:          rule.scheme,
			Claims:          noClaimsIsEmptyList(claims),
		}, nil
	}
	return ResolveResult{
		ConsentRequired: r.defaultConsentRequired,
		Scheme:          r.defaultScheme,
		Claims:          noClaimsIsEmptyList(nil),
	}, nil
}

// noClaimsIsEmptyList turns a nil claim slice into an empty one so the response
// carries `"claims": []` and never `"claims": null`.
//
// The consumer is a Lua/OpenResty plugin: cjson decodes null to cjson.null (a
// lightuserdata), so `#resp.claims` and `ipairs(resp.claims)` throw instead of
// iterating zero times.
func noClaimsIsEmptyList(claims []Claim) []Claim {
	if claims == nil {
		return []Claim{}
	}
	return claims
}
