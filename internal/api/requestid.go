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
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
)

// requestIDContextKey is the private key the correlation id is stored under; a
// distinct type keeps it from colliding with any other package's context value.
type requestIDContextKey struct{}

// withRequestID returns a context carrying the correlation id.
func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDContextKey{}, id)
}

// requestIDFrom reads the correlation id from a context, or "" when absent.
func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDContextKey{}).(string)
	return id
}

// RequestIDHeader carries the correlation id between the plugin and the
// resolver. The plugin sends one; when it does not, the resolver mints it, and
// either way it is echoed on the response so both sides log the same value.
const RequestIDHeader = "X-Request-Id"

// requestIDBytes is the length of a generated id before hex encoding. 8 bytes is
// ample for correlating within a trace window and keeps log lines short.
const requestIDBytes = 8

// maxRequestIDLength bounds an id accepted from the caller, so a caller cannot
// push arbitrary-length data into the logs through the header.
const maxRequestIDLength = 64

// requestID returns the caller's correlation id, or a freshly generated one.
//
// An inbound id is used only if it is short and printable: it ends up in log
// lines, and a header is caller-controlled input.
func requestID(r *http.Request) string {
	if given := r.Header.Get(RequestIDHeader); isUsableRequestID(given) {
		return given
	}
	buf := make([]byte, requestIDBytes)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand does not fail in practice, and a correlation id is not
		// worth failing a request over - the log line is simply uncorrelated.
		return "unavailable"
	}
	return hex.EncodeToString(buf)
}

// isUsableRequestID reports whether a caller-supplied id is safe to log: within
// the length bound and free of control characters that could forge a log line.
func isUsableRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLength {
		return false
	}
	return strings.IndexFunc(id, func(r rune) bool { return r < ' ' || r == 0x7f }) < 0
}
