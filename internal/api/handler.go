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

// Package api exposes the OwnerResolver over HTTP: POST /resolve, GET /health
// and GET /metrics.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"consent-owner-resolver/internal/resolver"
)

// defaultMaxBodyBytes caps an unconfigured request body at 5 MiB.
const defaultMaxBodyBytes = 5 << 20

// bearerPrefix is the Authorization scheme accepted for the shared secret.
const bearerPrefix = "Bearer "

// Routes served. They double as the `route` metric label, which is why the
// request path never appears in a metric: it carries owner identifiers.
const (
	routeResolve = "/resolve"
	routeHealth  = "/health"
	routeMetrics = "/metrics"
)

// prometheusContentType is the exposition format /metrics answers with.
const prometheusContentType = "text/plain; version=0.0.4; charset=utf-8"

// Options configures the HTTP binding.
type Options struct {
	// MaxBodyBytes caps the request body (0 = defaultMaxBodyBytes).
	MaxBodyBytes int64
	// AuthToken, when non-empty, is a shared secret every /resolve call must
	// present as `Authorization: Bearer <token>`. Empty leaves /resolve open,
	// which is only safe on a network where nothing but the plugin can reach it.
	AuthToken string
	// Debug logs the full request path and the full resolver error. Both can
	// carry owner identifiers, so it is off by default and is an explicit,
	// temporary operator choice - see redactPath and errorClass.
	Debug bool
}

// Handler serves the resolver HTTP API.
//
// The service is meant to run cluster-internal, reachable only by the
// consent-plugin: /resolve answers who owns a piece of data, so an open port is
// an owner-identifier oracle. Restrict it with a NetworkPolicy, and set
// Options.AuthToken on top where the plugin and resolver do not share a trust
// boundary.
type Handler struct {
	resolver     resolver.Resolver
	maxBodyBytes int64
	authToken    string
	debug        bool
	metrics      *metrics
}

// NewHandler builds an http.Handler routing /resolve and /health to the given
// resolver.
func NewHandler(r resolver.Resolver, opts Options) http.Handler {
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = defaultMaxBodyBytes
	}
	h := &Handler{
		resolver:     r,
		maxBodyBytes: opts.MaxBodyBytes,
		authToken:    opts.AuthToken,
		debug:        opts.Debug,
		metrics:      newMetrics(),
	}
	mux := http.NewServeMux()
	mux.Handle(routeResolve, h.instrument(routeResolve, h.resolve))
	// /health carries no data and must stay reachable for liveness probes, so it
	// is deliberately not authenticated.
	mux.Handle(routeHealth, h.instrument(routeHealth, h.health))
	// /metrics exposes route, status and error CLASS only - no path, no owner,
	// no error detail - so it is safe to scrape without the shared secret.
	mux.Handle(routeMetrics, h.instrument(routeMetrics, h.serveMetrics))
	return mux
}

// statusRecorder captures the status code so it can be counted; without it the
// metric could only ever record "a request happened".
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader implements http.ResponseWriter.
func (s *statusRecorder) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

// Write implements http.ResponseWriter, recording the implicit 200 that a Write
// without a preceding WriteHeader produces.
func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// instrument stamps the correlation id on the response, times the handler and
// records the outcome. The resolver sits on the synchronous path of every
// proxied request while fanning out to another service, so p99 latency and
// failure rate are exactly what an operator needs when the gateway starts
// timing out.
func (h *Handler) instrument(route string, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := requestID(r)
		w.Header().Set(RequestIDHeader, id)
		r = r.WithContext(withRequestID(r.Context(), id))

		recorder := &statusRecorder{ResponseWriter: w}
		start := time.Now()
		next(recorder, r)
		if recorder.status == 0 {
			recorder.status = http.StatusOK
		}
		h.metrics.observe(route, recorder.status, time.Since(start))
	})
}

// serveMetrics renders the collected metrics for a Prometheus scrape.
func (h *Handler) serveMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "only GET is allowed")
		return
	}
	w.Header().Set("Content-Type", prometheusContentType)
	if err := h.metrics.writeTo(w); err != nil {
		log.Printf("[owner-resolver] write metrics: %v", err)
	}
}

// authorized reports whether the request presents the configured shared secret.
// With no secret configured every request is authorized - the network is then
// the only control.
func (h *Handler) authorized(r *http.Request) bool {
	if h.authToken == "" {
		return true
	}
	header := r.Header.Get("Authorization")
	// The scheme is required: accepting a bare token would let a header that
	// merely happens to carry the secret authenticate.
	if !strings.HasPrefix(header, bearerPrefix) {
		return false
	}
	// Constant-time: a length-independent compare would leak the secret to a
	// caller that can time the response.
	return subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(header, bearerPrefix)), []byte(h.authToken)) == 1
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) resolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is allowed")
		return
	}
	if !h.authorized(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, h.maxBodyBytes)
	// Unknown fields are IGNORED, deliberately. The plugin and the resolver are
	// released from separate repositories on separate cycles, so the day the
	// plugin starts sending a new field, strict decoding would 400 every request
	// until this service is redeployed - a coupling the split repos exist to
	// avoid. (Config decoding stays strict: a config typo has no other way to be
	// noticed, and there is no independent release cycle to accommodate.)
	dec := json.NewDecoder(r.Body)
	var req resolver.ResolveRequest
	if err := dec.Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	result, err := h.resolver.Resolve(r.Context(), req)
	if err != nil {
		// A resolution error means "consent may be required but the owner could
		// not be determined". Surface it as 4xx so the plugin applies its fail
		// policy (deny by default); it never means "no consent needed".
		//
		// The plugin distinguishes the two 4xx cases, so they must not be
		// conflated: a payload the resolver cannot decode is the caller's bug
		// (400), an owner it cannot determine is not (422).
		h.metrics.observeFailure(errorClass(err))
		h.logResolveFailure(r.Context(), req, err)
		// The response says WHAT failed, never WHY in detail: an error like
		// `json matcher: no owner at "/dataOwner"` hands the caller this
		// deployment's pointer configuration. The detail is in the log.
		var badRequest *resolver.BadRequestError
		if errors.As(err, &badRequest) {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		writeError(w, http.StatusUnprocessableEntity, "cannot resolve owner")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// logResolveFailure records a failed resolve. At the default level neither the
// path nor the error detail is logged verbatim - both carry owner identifiers in
// the documented payload shapes.
func (h *Handler) logResolveFailure(ctx context.Context, req resolver.ResolveRequest, err error) {
	id := requestIDFrom(ctx)
	if h.debug {
		log.Printf("[owner-resolver] resolve failed: requestId=%q service=%q method=%q path=%q: %v",
			id, req.Resource.Service, req.Resource.Method, req.Resource.Path, err)
		return
	}
	log.Printf("[owner-resolver] resolve failed: requestId=%q service=%q method=%q path=%s: %s",
		id, req.Resource.Service, req.Resource.Method, redactPath(req.Resource.Path), errorClass(err))
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[owner-resolver] write response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
