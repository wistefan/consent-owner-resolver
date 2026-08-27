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

package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testConfig = `{
  "rules": [{
    "name": "profiles",
    "match": {"service": "svc"},
    "consentRequired": true,
    "matcher": {"type": "json", "owner": "/dataOwner"}
  }]
}`

// writeConfig puts a config file in a temp dir and returns its path.
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestGetenv(t *testing.T) {
	cases := map[string]struct {
		value    string
		set      bool
		fallback string
		want     string
	}{
		"set":            {value: "/etc/custom.json", set: true, fallback: "/default.json", want: "/etc/custom.json"},
		"unset":          {set: false, fallback: "/default.json", want: "/default.json"},
		"set but empty":  {value: "", set: true, fallback: "/default.json", want: "/default.json"},
		"empty fallback": {set: false, fallback: "", want: ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			const key = "OWNER_RESOLVER_TEST_STRING"
			if tc.set {
				t.Setenv(key, tc.value)
			}
			if got := getenv(key, tc.fallback); got != tc.want {
				t.Fatalf("getenv = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGetenvInt(t *testing.T) {
	cases := map[string]struct {
		value string
		set   bool
		want  int64
	}{
		"parses a number": {value: "1048576", set: true, want: 1048576},
		"negative":        {value: "-1", set: true, want: -1},
		// A typo must degrade to the default rather than stop the service
		// starting.
		"unparseable falls back": {value: "5MiB", set: true, want: 42},
		"unset falls back":       {set: false, want: 42},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			const key = "OWNER_RESOLVER_TEST_INT"
			if tc.set {
				t.Setenv(key, tc.value)
			}
			if got := getenvInt(key, 42); got != tc.want {
				t.Fatalf("getenvInt = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSettingsFromEnv(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		s := settingsFromEnv()
		if s.configPath != defaultConfigPath || s.listenAddr != defaultListenAddr {
			t.Fatalf("unexpected defaults: %+v", s)
		}
		if s.maxBodyBytes != 0 || s.authToken != "" || s.debug {
			t.Fatalf("unexpected defaults: %+v", s)
		}
	})
	t.Run("from the environment", func(t *testing.T) {
		t.Setenv(envConfigPath, "/etc/x.json")
		t.Setenv(envListenAddr, "127.0.0.1:9999")
		t.Setenv(envMaxBodyBytes, "1024")
		t.Setenv(envAuthToken, "s3cr3t")
		t.Setenv(envLogLevel, "DEBUG") // case-insensitive
		s := settingsFromEnv()
		want := settings{configPath: "/etc/x.json", listenAddr: "127.0.0.1:9999", maxBodyBytes: 1024, authToken: "s3cr3t", debug: true}
		if s != want {
			t.Fatalf("settingsFromEnv = %+v, want %+v", s, want)
		}
	})
	t.Run("a non-debug log level leaves debug off", func(t *testing.T) {
		t.Setenv(envLogLevel, "info")
		if settingsFromEnv().debug {
			t.Fatal("only LOG_LEVEL=debug enables debug logging")
		}
	})
}

func TestRun_ServesAndShutsDownOnContextCancel(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- run(ctx, settings{configPath: writeConfig(t, testConfig)}, listener)
	}()

	base := "http://" + listener.Addr().String()
	resp, err := waitForHealth(t, base)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if !strings.Contains(resp, "ok") {
		t.Fatalf("unexpected health response: %s", resp)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v, want a clean shutdown", err)
		}
	case <-time.After(shutdownTimeout + time.Second):
		t.Fatal("run did not return after the context was cancelled")
	}
}

func TestRun_FailsOnAnUnloadableConfig(t *testing.T) {
	cases := map[string]string{
		"missing file": filepath.Join(t.TempDir(), "nope.json"),
		"invalid json": writeConfig(t, `{"rules": [`),
		"invalid rule": writeConfig(t, `{"rules": [{"match": {}, "matcher": {"type": "nope"}}]}`),
	}
	for name, configPath := range cases {
		t.Run(name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			defer func() { _ = listener.Close() }()

			if err := run(context.Background(), settings{configPath: configPath}, listener); err == nil {
				t.Fatal("run must fail rather than serve with no usable configuration")
			}
		})
	}
}

// waitForHealth polls /health until the server is up, so the test does not race
// the goroutine that starts it.
func waitForHealth(t *testing.T, base string) (string, error) {
	t.Helper()
	var lastErr error
	for i := 0; i < 50; i++ {
		resp, err := http.Get(base + "/health") //nolint:noctx // short-lived test poll
		if err != nil {
			lastErr = err
			time.Sleep(10 * time.Millisecond)
			continue
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return "", err
		}
		return string(body), nil
	}
	return "", lastErr
}
