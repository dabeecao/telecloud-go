#!/bin/bash

set -e

APP_NAME="telecloud" 

echo "===> Cleaning old builds..."
rm -rf build
mkdir -p build

echo "===> Building & Compressing..."

for GOOS in linux darwin windows; do
  for GOARCH in amd64 arm64; do

    OUTPUT_NAME="${APP_NAME}-${GOOS}-${GOARCH}"
    if [ "$GOOS" = "windows" ]; then
      OUTPUT_NAME="${OUTPUT_NAME}.exe"
    fi

    echo "Building $OUTPUT_NAME..."

    CGO_ENABLED=0 GOOS=$GOOS GOARCH=$GOARCH \
    go build -ldflags="-s -w" -o build/$OUTPUT_NAME

    echo "Zipping $OUTPUT_NAME..."

    cd build
    zip -q "${OUTPUT_NAME}.zip" "$OUTPUT_NAME"
    rm "$OUTPUT_NAME"
    cd ..

  done
done

echo "===> Done!"
echo "File now in ./build"