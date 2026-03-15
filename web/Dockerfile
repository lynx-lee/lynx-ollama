FROM golang:1.23-alpine AS builder

ARG VERSION=dev

WORKDIR /build

# Install dependencies
RUN apk add --no-cache git

# Copy go module files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build binary with version info
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X main.Version=${VERSION}" -o ollama-web ./cmd/server/

# ── Runtime ──────────────────────────────────────────────────────
FROM alpine:3.20

RUN apk add --no-cache \
    bash \
    curl \
    docker-cli \
    docker-cli-compose \
    tzdata

WORKDIR /app

COPY --from=builder /build/ollama-web .

EXPOSE 8080

ENTRYPOINT ["/app/ollama-web"]
