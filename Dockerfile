# ============================================================
# Stage 1: Build frontend assets + compile Go binary
# ============================================================
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS builder

ARG TARGETARCH
ARG BUILDPLATFORM
WORKDIR /app

# Install curl and Node.js 20+ for frontend (tailwindcss oxide requires node >= 20)
RUN apt-get update && apt-get install -y ca-certificates curl gnupg && \
    mkdir -p /etc/apt/keyrings && \
    curl -fsSL https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key | gpg --dearmor -o /etc/apt/keyrings/nodesource.gpg && \
    echo "deb [signed-by=/etc/apt/keyrings/nodesource.gpg] https://deb.nodesource.com/node_20.x nodistro main" > /etc/apt/sources.list.d/nodesource.list && \
    apt-get update && apt-get install -y nodejs && \
    rm -rf /var/lib/apt/lists/*

# Download dependencies first (cache layer)
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Fetch frontend submodule (web/ is a git submodule, COPY doesn't include it)
RUN git submodule update --init --recursive

# Build frontend (Tailwind + bundle assets via npm)
RUN cd web && sed -i 's/\r$//' build-frontend.sh && bash build-frontend.sh 1

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

# Install required packages: ca-certificates, tzdata, ffmpeg, python3, aria2
RUN apk add --no-cache ca-certificates tzdata ffmpeg python3 aria2 \
    && wget -qO /usr/local/bin/yt-dlp https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp \
    && chmod a+rx /usr/local/bin/yt-dlp

# Set default environment variables for external tools
ENV TORRENT_PATH=/usr/bin/aria2c
ENV YTDLP_PATH=/usr/local/bin/yt-dlp
ENV FFMPEG_PATH=/usr/bin/ffmpeg

# Copy the compiled binary (assets are embedded via go:embed)
COPY --from=builder /app/telecloud /app/telecloud

# Copy the data directory with correct ownership
COPY --from=builder --chown=nonroot:nonroot /app/data /app/data

USER nonroot:nonroot

EXPOSE 8091

ENTRYPOINT ["/app/telecloud"]
