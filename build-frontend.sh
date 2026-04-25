#!/bin/bash
echo "Building Tailwind CSS..."
./tailwindcss -i ./static/css/input.css -o ./static/css/tailwind.css --minify

echo "Downloading frontend libraries..."
mkdir -p static/js static/css
curl -sSL https://cdn.jsdelivr.net/npm/alpinejs@3.x.x/dist/cdn.min.js -o static/js/alpine.min.js
curl -sSL https://cdn.jsdelivr.net/npm/@alpinejs/collapse@3.x.x/dist/cdn.min.js -o static/js/alpine-collapse.min.js
curl -sSL https://cdn.plyr.io/3.7.8/plyr.css -o static/css/plyr.css
curl -sSL https://cdn.plyr.io/3.7.8/plyr.polyfilled.js -o static/js/plyr.polyfilled.js

echo "Frontend build complete!"
