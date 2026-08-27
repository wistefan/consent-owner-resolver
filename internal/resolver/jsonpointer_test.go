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
	"testing"
)

func TestGetJSONPointer(t *testing.T) {
	doc := map[string]interface{}{
		"a":     map[string]interface{}{"b": "v"},
		"list":  []interface{}{"x", map[string]interface{}{"o": "owner1"}},
		"a/b":   "slash",
		"tilde": "t",
	}
	cases := []struct {
		ptr  string
		want interface{}
		ok   bool
	}{
		{"", doc, true},
		{"/a/b", "v", true},
		{"/list/0", "x", true},
		{"/list/1/o", "owner1", true},
		{"/list/9", nil, false},
		{"/missing", nil, false},
		{"/a~1b", "slash", true}, // "a/b" key via ~1 escaping
		{"no-slash", nil, false},
	}
	for _, c := range cases {
		got, ok := getJSONPointer(doc, c.ptr)
		if ok != c.ok {
			t.Fatalf("ptr %q ok=%v want %v", c.ptr, ok, c.ok)
		}
		if ok && c.ptr != "" && got != c.want {
			t.Fatalf("ptr %q got %v want %v", c.ptr, got, c.want)
		}
	}
}

func TestJoinPointer(t *testing.T) {
	if got := joinPointer("", "0"); got != "/0" {
		t.Fatalf(`joinPointer("","0")=%q`, got)
	}
	if got := joinPointer("/items", "3"); got != "/items/3" {
		t.Fatalf("joinPointer got %q", got)
	}
	if got := joinPointer("", "a/b"); got != "/a~1b" {
		t.Fatalf("joinPointer escape got %q", got)
	}
}

func TestDecodeJSONBody(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		v, err := decodeJSONBody(nil)
		if err != nil || v != nil {
			t.Fatalf("nil body: %v %v", v, err)
		}
	})
	t.Run("none", func(t *testing.T) {
		v, err := decodeJSONBody(&Body{Encoding: EncodingNone})
		if err != nil || v != nil {
			t.Fatalf("none: %v %v", v, err)
		}
	})
	t.Run("json", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]string{"k": "val"})
		v, err := decodeJSONBody(&Body{Encoding: EncodingJSON, Content: raw})
		if err != nil {
			t.Fatalf("json: %v", err)
		}
		m, ok := v.(map[string]interface{})
		if !ok || m["k"] != "val" {
			t.Fatalf("json decode wrong: %v", v)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		if _, err := decodeJSONBody(&Body{Encoding: EncodingJSON, Content: json.RawMessage(`{bad`)}); err == nil {
			t.Fatal("expected error on bad json")
		}
	})
	t.Run("base64 of json", func(t *testing.T) {
		inner, _ := json.Marshal(map[string]string{"k": "b64"})
		enc, _ := json.Marshal(base64.StdEncoding.EncodeToString(inner))
		v, err := decodeJSONBody(&Body{Encoding: EncodingBase64, Content: enc})
		if err != nil {
			t.Fatalf("base64 json: %v", err)
		}
		if m, ok := v.(map[string]interface{}); !ok || m["k"] != "b64" {
			t.Fatalf("base64 json decode wrong: %v", v)
		}
	})
	t.Run("base64 of opaque bytes -> nil", func(t *testing.T) {
		enc, _ := json.Marshal(base64.StdEncoding.EncodeToString([]byte{0x00, 0x01, 0xff}))
		v, err := decodeJSONBody(&Body{Encoding: EncodingBase64, Content: enc})
		if err != nil || v != nil {
			t.Fatalf("opaque base64 should decode to nil, got %v %v", v, err)
		}
	})
	t.Run("unknown encoding", func(t *testing.T) {
		if _, err := decodeJSONBody(&Body{Encoding: "xml"}); err == nil {
			t.Fatal("expected error on unknown encoding")
		}
	})
}
