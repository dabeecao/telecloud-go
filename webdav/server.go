package webdav

import (
	"log"
	"net/http"
	"strings"

	"telecloud/config"
	"telecloud/database"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/net/webdav"
)

func NewHandler(cfg *config.Config) http.Handler {
	fs := NewTelecloudFS(cfg)
	ls := webdav.NewMemLS()

	handler := &webdav.Handler{
		Prefix:     "/webdav",
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

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if database.GetSetting("webdav_enabled") != "true" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		user, pass, ok := r.BasicAuth()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="TeleCloud WebDAV"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		
		dbUser := database.GetSetting("admin_username")
		dbHash := database.GetSetting("admin_password_hash")
		
		if user != dbUser || bcrypt.CompareHashAndPassword([]byte(dbHash), []byte(pass)) != nil {
			w.Header().Set("WWW-Authenticate", `Basic realm="TeleCloud WebDAV"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		if r.Method == "OPTIONS" {
			w.Header().Set("Allow", "OPTIONS, GET, HEAD, POST, PUT, DELETE, TRACE, COPY, MOVE, MKCOL, PROPFIND, PROPPATCH, LOCK, UNLOCK")
			w.Header().Set("DAV", "1, 2")
		}

		// Handle macOS Finder specific garbage
		if strings.HasPrefix(r.URL.Path, "/webdav/._") || strings.HasPrefix(r.URL.Path, "/webdav/.DS_Store") {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		handler.ServeHTTP(w, r)
	})
}
