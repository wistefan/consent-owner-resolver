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

package contract

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// canonicalBase is the id space contracts/catalog ids are minted in (the
// authority facade); the local instance serves the same paths.
const canonicalBase = "http://consent-facade.trust-anchor.svc.cluster.local:8080"

func TestDataResources_FetchesLocallyButKeepsCanonicalIDs(t *testing.T) {
	var fetched []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched = append(fetched, r.URL.Path)
		switch r.URL.Path {
		case "/catalog/serviceofferings/default~agreement:1":
			_, _ = w.Write([]byte(`{"dataResources":["` + canonicalBase + `/catalog/dataresources/default~spec:1"]}`))
		case "/catalog/dataresources/default~spec:1":
			_, _ = w.Write([]byte(`{"@id":"` + canonicalBase + `/catalog/dataresources/default~spec:1","containsPII":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 0)
	resources, err := c.DataResources(context.Background(), Contract{
		ServiceOffering: canonicalBase + "/catalog/serviceofferings/default~agreement:1",
	})
	if err != nil {
		t.Fatalf("DataResources: %v", err)
	}
	if len(resources) != 1 || !resources[0].ContainsPII {
		t.Fatalf("unexpected resources: %+v", resources)
	}
	want := canonicalBase + "/catalog/dataresources/default~spec:1"
	if resources[0].ID != want {
		t.Fatalf("id must stay canonical (consents reference it), got %q", resources[0].ID)
	}
	if len(fetched) != 2 {
		t.Fatalf("expected 2 local fetches, got %v", fetched)
	}
}

func TestEncodeParticipant(t *testing.T) {
	// This is a WIRE CONTRACT with the consent-facade, not an implementation
	// detail: the expected strings are written out rather than computed, so a
	// change to the alphabet fails here instead of at integration time.
	cases := map[string]struct {
		selfDescriptionURL string
		want               string
	}{
		"http participant sd": {
			selfDescriptionURL: "http://facade/participants/urn:ngsi-ld:organization:prov",
			want:               "aHR0cDovL2ZhY2FkZS9wYXJ0aWNpcGFudHMvdXJuOm5nc2ktbGQ6b3JnYW5pemF0aW9uOnByb3Y",
		},
		"did consumer": {
			selfDescriptionURL: "did:web:fancy-marketplace.biz",
			want:               "ZGlkOndlYjpmYW5jeS1tYXJrZXRwbGFjZS5iaXo",
		},
		// `~` is what makes the standard alphabet reachable here: it is used
		// throughout this project's identifiers.
		"tilde in the identifier": {
			selfDescriptionURL: "http://facade/participants/default~urn:ngsi-ld:organization:prov",
			want:               "aHR0cDovL2ZhY2FkZS9wYXJ0aWNpcGFudHMvZGVmYXVsdH51cm46bmdzaS1sZDpvcmdhbml6YXRpb246cHJvdg",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := encodeParticipant(tc.selfDescriptionURL)
			if got != tc.want {
				t.Fatalf("encodeParticipant(%q) = %q, want %q", tc.selfDescriptionURL, got, tc.want)
			}
			// Round-trip: whatever the facade decodes it with must yield the
			// original URL.
			decoded, err := participantEncoding.DecodeString(got)
			if err != nil {
				t.Fatalf("decode %q: %v", got, err)
			}
			if string(decoded) != tc.selfDescriptionURL {
				t.Fatalf("round-trip gave %q, want %q", decoded, tc.selfDescriptionURL)
			}
			// The whole point of the alphabet: nothing needs escaping, so a
			// proxy cannot normalize the path segment out from under us.
			if url.PathEscape(got) != got {
				t.Fatalf("encoded participant %q needs path escaping", got)
			}
		})
	}
}

func TestSignedContracts_UsesTheEncodedParticipantsInThePath(t *testing.T) {
	const providerSD = "http://facade/participants/default~urn:ngsi-ld:organization:prov"
	const consumerSD = "did:web:fancy-marketplace.biz"
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// EscapedPath is what actually goes on the wire; RequestURI would hide a
		// percent-encoding problem behind Go's own decoding.
		gotPath = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{"verified":true,"contracts":[]}`))
	}))
	defer srv.Close()

	if _, err := NewClient(srv.URL, 0).SignedContracts(context.Background(), providerSD, consumerSD); err != nil {
		t.Fatalf("SignedContracts: %v", err)
	}
	want := "/verify/" + encodeParticipant(providerSD) + "/" + encodeParticipant(consumerSD)
	if gotPath != want {
		t.Fatalf("facade saw path %q, want %q", gotPath, want)
	}
	if strings.Contains(gotPath, "%") {
		t.Fatalf("the path must carry no percent-encoding a proxy could normalize: %q", gotPath)
	}
}

func TestLocalize_RebuildsOrFails(t *testing.T) {
	// The input comes from the facade's response, i.e. from outside this
	// process: a URL that cannot be rewritten must be refused, never fetched
	// as-is.
	c := NewClient("http://consent-facade.provider.svc.cluster.local:8080", 0)
	cases := map[string]struct {
		in      string
		want    string
		wantErr bool
	}{
		"canonical id is rewritten to the local base": {
			in:   canonicalBase + "/catalog/dataresources/default~spec:1",
			want: "http://consent-facade.provider.svc.cluster.local:8080/catalog/dataresources/default~spec:1",
		},
		"query is preserved": {
			in:   canonicalBase + "/catalog/dataresources?id=1",
			want: "http://consent-facade.provider.svc.cluster.local:8080/catalog/dataresources?id=1",
		},
		"host with no path is refused": {in: "http://some-internal-host", wantErr: true},
		"host with a bare slash":       {in: "http://some-internal-host/", wantErr: true},
		"unparseable url is refused":   {in: "http://[::1", wantErr: true},
		"empty string is refused":      {in: "", wantErr: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := c.localize(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error for %q, got %q", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("localize(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("localize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDataResources_RefusesAnUnlocalizableReference(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"dataResources":["http://attacker-controlled-host"]}`))
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, 0).DataResources(context.Background(), Contract{
		ServiceOffering: canonicalBase + "/catalog/serviceofferings/default~agreement:1",
	})
	if err == nil {
		t.Fatal("a data resource reference with no path must not be fetched as-is")
	}
}

func TestSignedContracts_OnlySignedContractsGovern(t *testing.T) {
	// A terminated or pending agreement must never gate personal data as though
	// it were in force; a contract with no status at all fails closed too.
	cases := map[string]struct {
		status    string
		wantCount int
	}{
		"signed":            {status: `"status":"signed",`, wantCount: 1},
		"signed uppercase":  {status: `"status":"SIGNED",`, wantCount: 1},
		"terminated":        {status: `"status":"terminated",`, wantCount: 0},
		"revoked":           {status: `"status":"revoked",`, wantCount: 0},
		"pending":           {status: `"status":"pending",`, wantCount: 0},
		"status is missing": {status: ``, wantCount: 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"verified":true,"contracts":[{"_id":"default~agreement:1",` + tc.status + `"dataProvider":"p"}]}`))
			}))
			defer srv.Close()

			contracts, err := NewClient(srv.URL, 0).SignedContracts(context.Background(), "http://facade/participants/p", "did:web:consumer")
			if err != nil {
				t.Fatalf("SignedContracts: %v", err)
			}
			if len(contracts) != tc.wantCount {
				t.Fatalf("status %q: want %d contracts, got %d", tc.status, tc.wantCount, len(contracts))
			}
		})
	}
}

func TestGetJSON_RejectsAnOversizedResponse(t *testing.T) {
	// Without a cap the resolver would buffer whatever the facade sends, on the
	// synchronous path of every proxied request.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"verified":true,"contracts":[{"_id":"`))
		chunk := bytes.Repeat([]byte("a"), 1<<20)
		for written := 0; written <= maxResponseBytes; written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, 0).SignedContracts(context.Background(), "http://facade/participants/p", "did:web:consumer")
	if err == nil {
		t.Fatal("an oversized facade response must be rejected")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("want a size-limit error, got %v", err)
	}
}

func TestGetJSON_DoesNotReadTheBodyOfAnErrorResponse(t *testing.T) {
	// A 500 with a large body must not be read in full just to be discarded.
	var served int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		n, _ := w.Write(bytes.Repeat([]byte("x"), 8<<20))
		served += n
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, 0).SignedContracts(context.Background(), "http://facade/participants/p", "did:web:consumer")
	if err == nil {
		t.Fatal("a non-200 facade response must be an error")
	}
	if !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("want a status error, got %v", err)
	}
}

func TestSignedContracts_UnverifiedYieldsNone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"verified":false,"contracts":[]}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, 0)
	contracts, err := c.SignedContracts(context.Background(), "http://facade/participants/p", "did:web:consumer")
	if err != nil {
		t.Fatalf("SignedContracts: %v", err)
	}
	if len(contracts) != 0 {
		t.Fatalf("unverified pair must yield no contracts, got %+v", contracts)
	}
}
