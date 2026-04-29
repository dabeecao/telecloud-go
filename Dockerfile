# ============================================================
# Stage 1: Build frontend assets + compile Go binary
# ============================================================
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS builder

ARG TARGETARCH
ARG BUILDPLATFORM
WORKDIR /app

# Install curl
RUN apt-get update && apt-get install -y curl && rm -rf /var/lib/apt/lists/*

# Download dependencies first (cache layer)
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Download TailwindCSS binary based on BUILD architecture (to run on the host's native CPU during build)
# BUILDPLATFORM is like "linux/amd64" or "linux/arm64" — we parse the arch part
RUN BUILDARCH=$(echo "${BUILDPLATFORM}" | cut -d'/' -f2) && \
    case "$BUILDARCH" in \
    amd64) TAILWIND_ARCH="x64" ;; \
    arm64) TAILWIND_ARCH="arm64" ;; \
    *) TAILWIND_ARCH="x64" ;; \
    esac && \
    echo "Downloading TailwindCSS for build architecture: $TAILWIND_ARCH (from BUILDPLATFORM=${BUILDPLATFORM})" && \
    curl -sSLfO "https://github.com/tailwindlabs/tailwindcss/releases/latest/download/tailwindcss-linux-${TAILWIND_ARCH}" \
    && chmod +x "tailwindcss-linux-${TAILWIND_ARCH}" \
    && mv -f "tailwindcss-linux-${TAILWIND_ARCH}" tailwindcss

# Build frontend (Tailwind + download JS/CSS libs)
RUN sed -i 's/\r$//' build-frontend.sh && bash build-frontend.sh

# Build Go binary for TARGET architecture
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build \
    -p 2 \
    -ldflags="-s -w" \
    -o telecloud .

# Create data directory and set permissions for the nonroot user (UID 65532)
RUN mkdir -p /app/data && chown 65532:65532 /app/data

# ============================================================
# Stage 2: Minimal runtime image
# ============================================================
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

# Copy the compiled binary (assets are embedded via go:embed)
COPY --from=builder /app/telecloud /app/telecloud

# Copy the data directory with correct ownership
COPY --from=builder --chown=nonroot:nonroot /app/data /app/data

EXPOSE 8091

ENTRYPOINT ["/app/telecloud"]
