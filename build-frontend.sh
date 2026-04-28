#!/bin/bash
set -e

echo "Building Tailwind CSS..."
if [ ! -f "./tailwindcss" ]; then
    echo "Error: tailwindcss binary not found!"
    exit 1
fi

./tailwindcss -i ./static/css/input.css -o ./static/css/tailwind.css --minify

echo "Downloading frontend libraries..."
mkdir -p static/js static/css

# Helper function for downloading with retry/error check
download_lib() {
    local url=$1
    local out=$2
    echo "Downloading $out..."
    curl -sSL "$url" -o "$out"
}

download_lib "https://cdn.jsdelivr.net/npm/alpinejs@3.x.x/dist/cdn.min.js" "static/js/alpine.min.js"
download_lib "https://cdn.jsdelivr.net/npm/@alpinejs/collapse@3.x.x/dist/cdn.min.js" "static/js/alpine-collapse.min.js"
download_lib "https://cdn.plyr.io/3.7.8/plyr.css" "static/css/plyr.css"
download_lib "https://cdn.plyr.io/3.7.8/plyr.polyfilled.js" "static/js/plyr.polyfilled.js"

echo "Frontend build complete!"
