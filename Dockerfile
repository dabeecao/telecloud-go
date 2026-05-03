# ============================================================
# Stage 1: Build frontend assets + compile Go binary
# ============================================================
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS builder

ARG TARGETARCH
ARG BUILDPLATFORM
WORKDIR /app

# Install curl and Node.js 20+ for frontend (tailwindcss oxide requires node >= 20)
RUN apt-get update && apt-get install -y curl && \
    curl -fsSL https://deb.nodesource.com/setup_20.x | bash - && \
    apt-get install -y nodejs && \
    rm -rf /var/lib/apt/lists/*

# Download dependencies first (cache layer)
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .



# Build frontend (Tailwind + download JS/CSS libs)
RUN cd web && sed -i 's/\r$//' build-frontend.sh && bash build-frontend.sh

# Build Go binary for TARGET architecture
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build \
    -p 2 \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o telecloud .

# Create data directory and set permissions for the nonroot user (UID 65532)
RUN mkdir -p /app/data && chown 65532:65532 /app/data

# ============================================================
# Stage 2: Minimal runtime image
# ============================================================
FROM alpine:latest

WORKDIR /app

# Create a non-root user
RUN addgroup -g 65532 nonroot && adduser -u 65532 -G nonroot -D nonroot

# Install required packages: ca-certificates, tzdata, ffmpeg, python3
RUN apk add --no-cache ca-certificates tzdata ffmpeg python3 \
    && wget -qO /usr/local/bin/yt-dlp https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp \
    && chmod a+rx /usr/local/bin/yt-dlp

# Copy the compiled binary (assets are embedded via go:embed)
COPY --from=builder /app/telecloud /app/telecloud

# Copy the data directory with correct ownership
COPY --from=builder --chown=nonroot:nonroot /app/data /app/data

USER nonroot:nonroot

EXPOSE 8091

ENTRYPOINT ["/app/telecloud"]
