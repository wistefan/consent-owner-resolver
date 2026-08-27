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
	"fmt"
	"strconv"
)

// buildClaim turns one data item into a claim. `selector` is the RFC6901 pointer
// at which the item was found, which becomes the claim's selector value.
type buildClaim func(item interface{}, selector string) (Claim, error)

// claimsPerItem walks the data items of a decoded body and builds one claim per
// item, tracking the JSON pointer of each.
//
// It is the shared shape of every collection-aware matcher: resolve the `items`
// pointer, treat the value as a single item or as an array of them, and give
// each element its own selector. `matcher` names the caller in error messages,
// which are the resolver's only diagnostic once a payload turns out not to have
// the configured shape.
//
// Zero claims is possible and legitimate: an empty array means there is no
// subject in this payload. See the Matcher contract.
func claimsPerItem(decoded interface{}, items string, itemsIsArray bool, matcher string, build buildClaim) ([]Claim, error) {
	root, ok := getJSONPointer(decoded, items)
	if !ok {
		return nil, fmt.Errorf("%s: no data at items pointer %q", matcher, items)
	}
	if !itemsIsArray {
		claim, err := build(root, items)
		if err != nil {
			return nil, err
		}
		return []Claim{claim}, nil
	}
	arr, ok := root.([]interface{})
	if !ok {
		return nil, fmt.Errorf("%s: value at %q is not an array", matcher, items)
	}
	claims := make([]Claim, 0, len(arr))
	for i, item := range arr {
		claim, err := build(item, joinPointer(items, strconv.Itoa(i)))
		if err != nil {
			return nil, err
		}
		claims = append(claims, claim)
	}
	return claims, nil
}
