#!/bin/bash

set -e

APP_NAME="telecloud"

VERSION=$(sed -n 's/.*version *= *"\([^"]*\)".*/\1/p' main.go)

if [ -z "$VERSION" ]; then
  VERSION="dev"
fi

echo "===> Version: $VERSION"

# Read API_ID and API_HASH from .env file (ignored by git) if present
if [ -f .env ]; then
  [ -z "$DEFAULT_API_ID" ] && DEFAULT_API_ID=$(grep -E "^API_ID=" .env | cut -d= -f2)
  [ -z "$DEFAULT_API_HASH" ] && DEFAULT_API_HASH=$(grep -E "^API_HASH=" .env | cut -d= -f2)
fi

if [ -z "$DEFAULT_API_ID" ] && [ -t 0 ]; then
  read -p "Enter DEFAULT_API_ID to embed during build (or press Enter to skip): " input_api_id
  DEFAULT_API_ID=$input_api_id
fi

if [ -z "$DEFAULT_API_HASH" ] && [ -t 0 ]; then
  read -p "Enter DEFAULT_API_HASH to embed during build (or press Enter to skip): " input_api_hash
  DEFAULT_API_HASH=$input_api_hash
fi

echo "===> Cleaning old builds..."
rm -rf build
mkdir -p build

echo "===> Building & Compressing..."

for GOOS in linux darwin windows; do
  for GOARCH in amd64 arm64; do

    BIN_NAME="$APP_NAME"

    if [ "$GOOS" = "windows" ]; then
      BIN_NAME="${BIN_NAME}.exe"
    fi

    ZIP_NAME="${APP_NAME}-${VERSION}-${GOOS}-${GOARCH}.zip"

    LDFLAGS="-s -w -X main.version=$VERSION"
    if [ ! -z "$DEFAULT_API_ID" ]; then
      LDFLAGS="$LDFLAGS -X telecloud/config.DefaultAPIIDStr=$DEFAULT_API_ID"
    fi
    if [ ! -z "$DEFAULT_API_HASH" ]; then
      LDFLAGS="$LDFLAGS -X telecloud/config.DefaultAPIHash=$DEFAULT_API_HASH"
    fi

    CGO_ENABLED=0 GOOS=$GOOS GOARCH=$GOARCH \
    go build -ldflags="$LDFLAGS" \
    -o build/$BIN_NAME

    echo "Zipping $ZIP_NAME..."

    cd build
    zip -q "$ZIP_NAME" "$BIN_NAME"
    rm "$BIN_NAME"
    cd ..

  done
done

echo "===> Done!"
echo "Files now in ./build"