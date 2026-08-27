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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// getJSONPointer resolves an RFC6901 JSON Pointer against a decoded JSON value
// (map[string]interface{} / []interface{} / scalars). An empty pointer returns
// the whole document. It returns the located value and whether it was found.
func getJSONPointer(doc interface{}, pointer string) (interface{}, bool) {
	if pointer == "" {
		return doc, true
	}
	if pointer[0] != '/' {
		return nil, false
	}
	cur := doc
	for _, tok := range strings.Split(pointer[1:], "/") {
		tok = strings.ReplaceAll(tok, "~1", "/")
		tok = strings.ReplaceAll(tok, "~0", "~")
		switch node := cur.(type) {
		case map[string]interface{}:
			v, ok := node[tok]
			if !ok {
				return nil, false
			}
			cur = v
		case []interface{}:
			idx, err := strconv.Atoi(tok)
			if err != nil || idx < 0 || idx >= len(node) {
				return nil, false
			}
			cur = node[idx]
		default:
			return nil, false
		}
	}
	return cur, true
}

// joinPointer appends one (unescaped) reference token to a base pointer,
// escaping per RFC6901 (~ -> ~0, / -> ~1).
func joinPointer(base, token string) string {
	token = strings.ReplaceAll(token, "~", "~0")
	token = strings.ReplaceAll(token, "/", "~1")
	return base + "/" + token
}

// pointerString reads a string value at an RFC6901 pointer within item; it
// returns ("", false) when absent or not a string.
func pointerString(item interface{}, pointer string) (string, bool) {
	v, ok := getJSONPointer(item, pointer)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// Payload gives a matcher access to the request body as decoded JSON. It is
// decoded on FIRST USE, not up front: a matcher that needs no body (path,
// static) must not be broken by a payload it never looks at.
type Payload interface {
	// JSON returns the decoded body, or nil when there is no usable JSON payload
	// (EncodingNone, or opaque base64 that is not JSON).
	JSON() (interface{}, error)
}

// lazyPayload decodes b at most once, on the first JSON call. Requests are
// handled by a single goroutine, so no locking is needed.
type lazyPayload struct {
	body    *Body
	decoded interface{}
	err     error
	done    bool
}

func newLazyPayload(b *Body) *lazyPayload { return &lazyPayload{body: b} }

// JSON implements Payload.
func (p *lazyPayload) JSON() (interface{}, error) {
	if !p.done {
		p.decoded, p.err = decodeJSONBody(p.body)
		p.done = true
	}
	return p.decoded, p.err
}

// BadRequestError marks an error caused by the CALLER's payload - a body that
// cannot be decoded at all - as opposed to "consent may be required but the
// owner could not be resolved". The two mean different things to the plugin,
// which keys its fail policy off the status code, so the HTTP binding maps this
// one to 400 and everything else to 422.
type BadRequestError struct{ Err error }

// Error implements error.
func (e *BadRequestError) Error() string { return e.Err.Error() }

// Unwrap gives errors.Is/errors.As access to the cause.
func (e *BadRequestError) Unwrap() error { return e.Err }

// badRequestf builds a BadRequestError with a formatted message.
func badRequestf(format string, args ...interface{}) error {
	return &BadRequestError{Err: fmt.Errorf(format, args...)}
}

// decodeJSONBody turns an optional request Body into a decoded JSON value, or
// nil when there is no usable JSON payload (EncodingNone, or opaque base64 that
// is not JSON). A JSON body that fails to parse is a client error.
func decodeJSONBody(b *Body) (interface{}, error) {
	if b == nil {
		return nil, nil
	}
	switch b.Encoding {
	case "", EncodingNone:
		return nil, nil
	case EncodingJSON:
		if len(b.Content) == 0 {
			return nil, nil
		}
		var v interface{}
		if err := json.Unmarshal(b.Content, &v); err != nil {
			return nil, badRequestf("decode json body: %w", err)
		}
		return v, nil
	case EncodingBase64:
		var s string
		if err := json.Unmarshal(b.Content, &s); err != nil {
			return nil, badRequestf("base64 body must be a JSON string: %w", err)
		}
		raw, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, badRequestf("decode base64 body: %w", err)
		}
		var v interface{}
		if err := json.Unmarshal(raw, &v); err != nil {
			// Opaque (non-JSON) bytes: no structured view, and NOT a client
			// error - base64 is the encoding for payloads that need not be JSON
			// at all. Path/static matchers still work from Resource; json
			// matchers report that they need JSON.
			return nil, nil //nolint:nilerr // deliberate: opaque bytes are a valid payload
		}
		return v, nil
	default:
		// The class prefix matters: errorClass keys the failure metric on the
		// text before the first ": ", and this is the one message that
		// interpolates a caller-supplied value.
		return nil, badRequestf("decode body: unknown encoding %q", b.Encoding)
	}
}
