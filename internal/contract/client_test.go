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
	"context"
	"net/http"
	"net/http/httptest"
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
