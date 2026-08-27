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
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"consent-owner-resolver/internal/api"
	"consent-owner-resolver/internal/resolver"
)

const (
	envConfigPath   = "CONFIG_PATH"
	envListenAddr   = "LISTEN_ADDR"
	envMaxBodyBytes = "MAX_BODY_BYTES"

	defaultConfigPath = "/etc/owner-resolver/config.json"
	defaultListenAddr = ":8080"
)

func main() {
	configPath := getenv(envConfigPath, defaultConfigPath)
	listenAddr := getenv(envListenAddr, defaultListenAddr)
	maxBodyBytes := getenvInt(envMaxBodyBytes, 0)

	res, err := resolver.Load(configPath)
	if err != nil {
		log.Fatalf("[owner-resolver] load config: %v", err)
	}

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           api.NewHandler(res, maxBodyBytes),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("[owner-resolver] listening on %s (config: %s)", listenAddr, configPath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[owner-resolver] server error: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("[owner-resolver] shutdown: %v", err)
	}
	log.Print("[owner-resolver] stopped")
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
		log.Printf("[owner-resolver] invalid %s=%q, using default", key, v)
	}
	return fallback
}
