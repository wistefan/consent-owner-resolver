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

// Command owner-resolver runs the OwnerResolver HTTP service used by the
// consent-plugin to determine, from the data alone (never the requestor),
// whether a payload needs a consent check and who its data owner is.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"consent-owner-resolver/internal/api"
	"consent-owner-resolver/internal/resolver"
)

// Environment variables the service reads.
const (
	envConfigPath   = "CONFIG_PATH"
	envListenAddr   = "LISTEN_ADDR"
	envMaxBodyBytes = "MAX_BODY_BYTES"
	envAuthToken    = "AUTH_TOKEN"
	envLogLevel     = "LOG_LEVEL"
)

const (
	defaultConfigPath = "/etc/owner-resolver/config.json"
	defaultListenAddr = ":8080"

	// logLevelDebug turns on verbatim path and error logging. Both can carry
	// owner identifiers, so it is opt-in and meant to be temporary.
	logLevelDebug = "debug"
)

// Server timeouts. The resolver sits on the synchronous path of every proxied
// request, so a slow client must not be able to hold a connection open.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 15 * time.Second
	writeTimeout      = 15 * time.Second
	idleTimeout       = 60 * time.Second
	shutdownTimeout   = 5 * time.Second
)

// settings is the process configuration, read from the environment.
type settings struct {
	configPath   string
	listenAddr   string
	maxBodyBytes int64
	authToken    string
	debug        bool
}

// settingsFromEnv reads the configuration, falling back to the defaults.
func settingsFromEnv() settings {
	return settings{
		configPath:   getenv(envConfigPath, defaultConfigPath),
		listenAddr:   getenv(envListenAddr, defaultListenAddr),
		maxBodyBytes: getenvInt(envMaxBodyBytes, 0),
		authToken:    os.Getenv(envAuthToken),
		debug:        strings.EqualFold(os.Getenv(envLogLevel), logLevelDebug),
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	s := settingsFromEnv()
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", s.listenAddr)
	if err != nil {
		log.Fatalf("[owner-resolver] listen on %s: %v", s.listenAddr, err)
	}

	if err := run(ctx, s, listener); err != nil {
		log.Fatalf("[owner-resolver] %v", err)
	}
	log.Print("[owner-resolver] stopped")
}

// run loads the configuration, serves on the given listener, and shuts down
// gracefully when ctx is cancelled.
//
// The listener is passed in rather than opened here so a test can serve on an
// ephemeral port and still know which one it got.
func run(ctx context.Context, s settings, listener net.Listener) error {
	res, err := resolver.Load(s.configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if s.authToken == "" {
		log.Printf("[owner-resolver] %s is not set: /resolve is unauthenticated and must be reachable only by the consent-plugin (restrict it with a NetworkPolicy)", envAuthToken)
	}

	srv := newServer(s, res)
	serveErr := make(chan error, 1)
	go func() {
		log.Printf("[owner-resolver] listening on %s (config: %s)", listener.Addr(), s.configPath)
		err := srv.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	// A fresh context: ctx is already cancelled, and shutdown needs its own
	// budget to drain in-flight requests.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	//nolint:contextcheck // deliberate: ctx is already cancelled, and draining
	// in-flight requests needs its own budget.
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return <-serveErr
}

// newServer builds the HTTP server for the given settings and resolver.
func newServer(s settings, res resolver.Resolver) *http.Server {
	return &http.Server{
		Handler: api.NewHandler(res, api.Options{
			MaxBodyBytes: s.maxBodyBytes,
			AuthToken:    s.authToken,
			Debug:        s.debug,
		}),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
}

// getenv reads an environment variable, falling back when it is unset or empty.
func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// getenvInt reads an integer environment variable. An unparseable value is
// logged and the fallback used, so a typo degrades to the default rather than
// preventing the service from starting.
func getenvInt(key string, fallback int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
		// #nosec G706 -- %q escapes control characters, so an env value cannot
		// forge a log line.
		log.Printf("[owner-resolver] invalid %s=%q, using default", key, v)
	}
	return fallback
}
