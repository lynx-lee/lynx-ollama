FROM golang:1.25-alpine AS builder

WORKDIR /build

# Install dependencies
RUN apk add --no-cache git

# Copy go module files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Extract version from main.go as fallback; allow override via build arg
ARG VERSION=""
RUN if [ -z "$VERSION" ]; then \
      VERSION=$(grep -m1 'var Version' cmd/server/main.go | sed 's/.*"\(.*\)".*/\1/'); \
    fi && \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X main.Version=${VERSION}" -o ollama-console ./cmd/server/

# ── Runtime ──────────────────────────────────────────────────────
FROM alpine:3

RUN apk add --no-cache \
    bash \
    curl \
    docker-cli \
    docker-cli-compose \
    tzdata

WORKDIR /app

COPY --from=builder /build/ollama-console .

EXPOSE 8080

ENTRYPOINT ["/app/ollama-console"]
