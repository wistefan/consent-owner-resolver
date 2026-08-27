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
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"consent-owner-resolver/internal/resolver"
)

// Handler serves the resolver HTTP API.
type Handler struct {
	resolver     resolver.Resolver
	maxBodyBytes int64
}

// NewHandler builds an http.Handler routing /resolve and /health to the given
// resolver. maxBodyBytes caps the request body (0 = a 5 MiB default).
func NewHandler(r resolver.Resolver, maxBodyBytes int64) http.Handler {
	if maxBodyBytes <= 0 {
		maxBodyBytes = 5 << 20
	}
	h := &Handler{resolver: r, maxBodyBytes: maxBodyBytes}
	mux := http.NewServeMux()
	mux.HandleFunc("/resolve", h.resolve)
	mux.HandleFunc("/health", h.health)
	return mux
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) resolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is allowed")
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
