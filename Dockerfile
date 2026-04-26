# ============================================================
# Stage 1: Build frontend assets + compile Go binary
# ============================================================
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Install curl for downloading tailwindcss
RUN apk add --no-cache curl

# Download dependencies first (cache layer)
COPY go.mod go.sum ./
RUN go mod download

# Download TailwindCSS binary
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

# ============================================================
# Stage 2: Minimal runtime image
# ============================================================
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

# Copy the compiled binary (assets are embedded via go:embed)
COPY --from=builder /app/telecloud /app/telecloud

# Create data directories (will be overridden by volume mounts)
# The binary handles creating these at runtime

EXPOSE 8091

ENTRYPOINT ["/app/telecloud"]
