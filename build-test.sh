#!/bin/bash

set -e

APP_NAME="telecloud"

VERSION=$(sed -n 's/.*version *= *"\([^"]*\)".*/\1/p' main.go)

if [ -z "$VERSION" ]; then
  VERSION="dev"
fi

echo "===> Version: $VERSION"

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

    echo "Building $ZIP_NAME..."

    CGO_ENABLED=0 GOOS=$GOOS GOARCH=$GOARCH \
    go build -ldflags="-s -w -X main.version=$VERSION" \
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