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

// Package api exposes the OwnerResolver over HTTP: POST /resolve and GET /health.
package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"consent-owner-resolver/internal/resolver"
)

// defaultMaxBodyBytes caps an unconfigured request body at 5 MiB.
const defaultMaxBodyBytes = 5 << 20

// bearerPrefix is the Authorization scheme accepted for the shared secret.
const bearerPrefix = "Bearer "

// Options configures the HTTP binding.
type Options struct {
	// MaxBodyBytes caps the request body (0 = defaultMaxBodyBytes).
	MaxBodyBytes int64
	// AuthToken, when non-empty, is a shared secret every /resolve call must
	// present as `Authorization: Bearer <token>`. Empty leaves /resolve open,
	// which is only safe on a network where nothing but the plugin can reach it.
	AuthToken string
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
}

// NewHandler builds an http.Handler routing /resolve and /health to the given
// resolver.
func NewHandler(r resolver.Resolver, opts Options) http.Handler {
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = defaultMaxBodyBytes
	}
	h := &Handler{resolver: r, maxBodyBytes: opts.MaxBodyBytes, authToken: opts.AuthToken}
	mux := http.NewServeMux()
	mux.HandleFunc("/resolve", h.resolve)
	// /health carries no data and must stay reachable for liveness probes, so it
	// is deliberately not authenticated.
	mux.HandleFunc("/health", h.health)
	return mux
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
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
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
		log.Printf("[owner-resolver] resolve failed for %s %s: %v", req.Resource.Method, req.Resource.Path, err)
		writeError(w, http.StatusUnprocessableEntity, "cannot resolve owner: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
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
