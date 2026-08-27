# Multi-stage build for the consent OwnerResolver service.
# Stage 1: compile a static Go binary. Stage 2: minimal non-root runtime.

# --- Build stage ---
FROM golang:1.23-alpine AS builder

WORKDIR /build

# Cache dependency downloads (none beyond the stdlib, but keep the pattern).
COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o owner-resolver .

# --- Runtime stage ---
FROM alpine:3.19

RUN apk add --no-cache ca-certificates \
	&& addgroup -g 10100 app \
	&& adduser -u 10100 -G app -s /sbin/nologin -D app

WORKDIR /app
COPY --from=builder /build/owner-resolver /app/owner-resolver

# Non-root, read-only-friendly: the config is mounted at /etc/owner-resolver.
USER 10100:10100
ENV CONFIG_PATH=/etc/owner-resolver/config.json \
	LISTEN_ADDR=:8080

EXPOSE 8080
ENTRYPOINT ["/app/owner-resolver"]
