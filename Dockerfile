# Multi-stage build for the consent OwnerResolver service.
# Stage 1: compile a static Go binary. Stage 2: minimal non-root runtime.

# --- Build stage ---
# Pinned to the exact toolchain go.mod requires, so the image-built binary and
# the CI-built binary are produced by the same compiler. Bump both together.
FROM golang:1.27.1-alpine AS builder

WORKDIR /build

# Cache dependency downloads (none beyond the stdlib, but keep the pattern).
COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o owner-resolver .

# --- Runtime stage ---
# distroless/static: no shell, no package manager, nothing to patch - the binary
# is CGO_ENABLED=0, so it needs nothing but ca-certificates, which the image
# already carries. `nonroot` runs as uid 65532.
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=builder /build/owner-resolver /app/owner-resolver

# Non-root, read-only-friendly: the config is mounted at /etc/owner-resolver.
USER 65532:65532
ENV CONFIG_PATH=/etc/owner-resolver/config.json \
	LISTEN_ADDR=:8080

EXPOSE 8080
ENTRYPOINT ["/app/owner-resolver"]
