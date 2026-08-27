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
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// pathDigestLength is how many hex characters of the path digest are logged.
// 8 hex characters (32 bits) is enough to correlate the same path across log
// lines without being reversible in practice for a high-cardinality path space.
const pathDigestLength = 8

// pathDigestPrefix labels the digest so nobody mistakes it for a real path.
const pathDigestPrefix = "sha256:"

// errorClassSeparator splits a resolver error into its "which component
// failed" prefix and the detail that may quote request data.
const errorClassSeparator = ": "

// errorClassUnspecified is the class of an error whose message carries no
// component prefix.
//
// It matters that this is a FIXED string rather than the message itself: the
// class becomes a metric label on the unauthenticated /metrics endpoint, so
// returning an unprefixed message would put caller-influenced text into the
// scrape and let a caller mint unbounded label values. Falling back to a
// constant makes the label set closed by construction - the next error message
// added without a prefix cannot reopen that.
const errorClassUnspecified = "unspecified"

// redactPath turns a request path into a stable, non-reversible digest.
//
// In the documented NGSI-LD shape the path IS the subject id
// (`/ngsi-ld/v1/entities/urn:ngsi-ld:PersonalProfile:alice`), and for the `path`
// matcher the owner is literally in it. Logging it verbatim would copy owner
// identifiers into every log sink the cluster ships to; the digest keeps the
// one property operators actually need - the same path logs as the same value.
func redactPath(path string) string {
	if path == "" {
		return pathDigestPrefix + "empty"
	}
	sum := sha256.Sum256([]byte(path))
	return pathDigestPrefix + hex.EncodeToString(sum[:])[:pathDigestLength]
}

// errorClass reduces a resolver error to the part that says WHICH component
// failed - "json matcher", "contract matcher", "decode body" - dropping the
// detail after it, which quotes pointers, URIs and other request data.
//
// It is what gets logged at the default level and what labels the failure
// metric; the full error is available at debug level, and never in a response
// body or a metric. An unprefixed message yields errorClassUnspecified rather
// than the message, which is what keeps the label set closed.
func errorClass(err error) string {
	if err == nil {
		return ""
	}
	head, _, found := strings.Cut(err.Error(), errorClassSeparator)
	if !found || head == "" {
		return errorClassUnspecified
	}
	return head
}
