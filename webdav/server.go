package webdav

import (
	"log"
	"net/http"
	"strings"

	"telecloud/config"

	"golang.org/x/net/webdav"
)

func StartServer(cfg *config.Config) {
	fs := NewTelecloudFS(cfg)
	ls := webdav.NewMemLS()

	handler := &webdav.Handler{
		FileSystem: fs,
		LockSystem: ls,
		Logger: func(r *http.Request, err error) {
			if err != nil {
				log.Printf("WEBDAV [%s]: %s, ERROR: %s\n", r.Method, r.URL.Path, err)
			} else {
				log.Printf("WEBDAV [%s]: %s\n", r.Method, r.URL.Path)
			}
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != cfg.WebdavUser || pass != cfg.WebdavPassword {
			w.Header().Set("WWW-Authenticate", `Basic realm="TeleCloud WebDAV"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		if r.Method == "OPTIONS" {
			w.Header().Set("Allow", "OPTIONS, GET, HEAD, POST, PUT, DELETE, TRACE, COPY, MOVE, MKCOL, PROPFIND, PROPPATCH, LOCK, UNLOCK")
			w.Header().Set("DAV", "1, 2")
		}

		// Handle macOS Finder specific garbage
		if strings.HasPrefix(r.URL.Path, "/._") || strings.HasPrefix(r.URL.Path, "/.DS_Store") {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		handler.ServeHTTP(w, r)
	})

	log.Printf("Starting WebDAV server on port %s...", cfg.WebdavPort)
	if err := http.ListenAndServe(":"+cfg.WebdavPort, mux); err != nil {
		log.Fatalf("WebDAV server failed: %v", err)
	}
}
