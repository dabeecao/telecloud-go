@echo off
echo Building Tailwind CSS...
tailwindcss.exe -i static/css/input.css -o static/css/tailwind.css --minify

echo Downloading frontend libraries...
if not exist "static\js" mkdir "static\js"
if not exist "static\css" mkdir "static\css"
curl -sSL https://cdn.jsdelivr.net/npm/alpinejs@3.x.x/dist/cdn.min.js -o static/js/alpine.min.js
curl -sSL https://cdn.jsdelivr.net/npm/@alpinejs/collapse@3.x.x/dist/cdn.min.js -o static/js/alpine-collapse.min.js
curl -sSL https://cdn.plyr.io/3.7.8/plyr.css -o static/css/plyr.css
curl -sSL https://cdn.plyr.io/3.7.8/plyr.polyfilled.js -o static/js/plyr.polyfilled.js

echo Frontend build complete!
