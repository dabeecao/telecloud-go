# ============================================================
# Stage 1: Build frontend assets + compile Go binary
# ============================================================
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS builder

ARG TARGETARCH
ARG BUILDPLATFORM
WORKDIR /app

# Install curl, unzip, and Bun for frontend
RUN apt-get update && apt-get install -y ca-certificates curl unzip && \
    curl -fsSL https://bun.sh/install | bash && \
    mv /root/.bun/bin/bun /usr/local/bin/bun && \
    rm -rf /var/lib/apt/lists/*

# Download dependencies first (cache layer)
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Fetch frontend submodule if building from a git clone
RUN if [ -d .git ]; then git submodule update --init --recursive; else echo "Not a git repository, skipping submodule init"; fi

# Build frontend (Tailwind + bundle assets via bun)
RUN cd web && sed -i 's/\r$//' build-frontend.sh && bash build-frontend.sh

# Build Go binary for TARGET architecture
ARG VERSION=dev
ARG DEFAULT_API_ID="0"
ARG DEFAULT_API_HASH=""
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build \
    -p 2 \
    -ldflags="-s -w -X main.version=${VERSION} -X telecloud/config.DefaultAPIIDStr=${DEFAULT_API_ID} -X telecloud/config.DefaultAPIHash=${DEFAULT_API_HASH}" \
    -o telecloud .

# Create data directory and set permissions for the nonroot user (UID 65532)
RUN mkdir -p /app/data && chown 65532:65532 /app/data

# ============================================================
# Stage 2: Minimal runtime image
# ============================================================
# Pin alpine major.minor for reproducible builds. Bump deliberately.
FROM alpine:3.21

WORKDIR /app

# Create a non-root user
RUN addgroup -g 65532 nonroot && adduser -u 65532 -G nonroot -D nonroot

# Install required packages: ca-certificates, tzdata, ffmpeg, python3, aria2.
# yt-dlp is downloaded from upstream and SHA-256 verified against the
# checksum file published alongside the same release tag.
RUN apk add --no-cache ca-certificates tzdata ffmpeg python3 aria2 wget \
    && set -eux \
    && wget -qO /tmp/yt-dlp        https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp \
    && wget -qO /tmp/yt-dlp.sha256 https://github.com/yt-dlp/yt-dlp/releases/latest/download/SHA2-256SUMS \
    && (cd /tmp && grep -E '  yt-dlp$' yt-dlp.sha256 | sha256sum -c -) \
    && install -m 0755 /tmp/yt-dlp /usr/local/bin/yt-dlp \
    && rm -f /tmp/yt-dlp /tmp/yt-dlp.sha256

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
