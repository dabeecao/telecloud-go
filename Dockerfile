# ============================================================
# Stage 1: Build frontend assets + compile Go binary
# ============================================================
FROM golang:1.24-bookworm AS builder

WORKDIR /app

# Install curl
RUN apt-get update && apt-get install -y curl && rm -rf /var/lib/apt/lists/*

# Download dependencies first (cache layer)
COPY go.mod go.sum ./
RUN go mod download

# Download TailwindCSS binary (requires glibc, which is in bookworm)
RUN curl -sLO https://github.com/tailwindlabs/tailwindcss/releases/latest/download/tailwindcss-linux-x64 \
    && chmod +x tailwindcss-linux-x64 \
    && mv tailwindcss-linux-x64 tailwindcss

# Copy source code
COPY . .

# Build frontend (Tailwind + download JS/CSS libs)
RUN chmod +x build-frontend.sh && ./build-frontend.sh

# Build Go binary
RUN CGO_ENABLED=0 GOOS=linux go build \
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
