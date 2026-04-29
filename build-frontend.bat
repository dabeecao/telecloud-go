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

echo Building Prism.js locally...
curl -sSL https://cdnjs.cloudflare.com/ajax/libs/prism/1.29.0/themes/prism-tomorrow.min.css -o static/css/prism.css
curl -sSL https://cdnjs.cloudflare.com/ajax/libs/prism/1.29.0/prism.min.js -o static/js/prism.js
for %%l in (json javascript python go bash yaml sql) do (
  echo Adding Prism language: %%l
  echo. >> static/js/prism.js
  curl -sSL https://cdnjs.cloudflare.com/ajax/libs/prism/1.29.0/components/prism-%%l.min.js >> static/js/prism.js
)

echo Minifying JS and CSS files...
call npx -y esbuild static/js/common.js --minify --outfile=static/js/common.min.js
call npx -y esbuild static/js/script.js --minify --outfile=static/js/script.min.js
call npx -y esbuild static/js/prism.js --minify --outfile=static/js/prism.min.js

call npx -y esbuild static/css/style.css --bundle --minify --external:/static/* --outfile=static/css/style.min.css
call npx -y esbuild static/css/nunito.css --minify --outfile=static/css/nunito.min.css
call npx -y esbuild static/css/prism.css --minify --outfile=static/css/prism.min.css
call npx -y esbuild static/css/plyr.css --minify --outfile=static/css/plyr.min.css

echo Frontend build complete!
