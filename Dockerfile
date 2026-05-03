# ============================================================
# Stage 1: Build frontend assets + compile Go binary
# ============================================================
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS builder

ARG TARGETARCH
ARG BUILDPLATFORM
WORKDIR /app

# Install curl and Node.js for frontend minification
RUN apt-get update && apt-get install -y curl nodejs npm && rm -rf /var/lib/apt/lists/*

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
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

# Copy the compiled binary (assets are embedded via go:embed)
COPY --from=builder /app/telecloud /app/telecloud

# Copy the data directory with correct ownership
COPY --from=builder --chown=nonroot:nonroot /app/data /app/data

EXPOSE 8091

ENTRYPOINT ["/app/telecloud"]
