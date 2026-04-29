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

echo "Building Prism.js locally..."
download_lib "https://cdnjs.cloudflare.com/ajax/libs/prism/1.29.0/themes/prism-tomorrow.min.css" "static/css/prism.css"
download_lib "https://cdnjs.cloudflare.com/ajax/libs/prism/1.29.0/prism.min.js" "static/js/prism.js"
for lang in json javascript python go bash yaml sql; do
  echo "Adding Prism language: $lang..."
  echo "" >> static/js/prism.js
  curl -sSL "https://cdnjs.cloudflare.com/ajax/libs/prism/1.29.0/components/prism-$lang.min.js" >> static/js/prism.js
done

echo "Minifying JS and CSS files..."
npx -y esbuild static/js/common.js --minify --outfile=static/js/common.min.js
npx -y esbuild static/js/script.js --minify --outfile=static/js/script.min.js
npx -y esbuild static/js/prism.js --minify --outfile=static/js/prism.min.js

npx -y esbuild static/css/style.css --bundle --minify --external:/static/* --outfile=static/css/style.min.css
npx -y esbuild static/css/nunito.css --minify --outfile=static/css/nunito.min.css
npx -y esbuild static/css/prism.css --minify --outfile=static/css/prism.min.css
npx -y esbuild static/css/plyr.css --minify --outfile=static/css/plyr.min.css

echo "Frontend build complete!"
