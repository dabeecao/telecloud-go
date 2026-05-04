package s3

import (
	"net/http"
	"strings"
	"telecloud/config"
	"telecloud/database"

	"github.com/johannesboyne/gofakes3"
)

func NewHandler(cfg *config.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if database.GetSetting("s3_enabled") != "true" {
			http.Error(w, "S3 API is disabled", http.StatusForbidden)
			return
		}

		// Pre-authentication to identify the user
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			authHeader = r.URL.Query().Get("X-Amz-Algorithm")
		}

		var accessKey string
		if authHeader != "" {
			if strings.HasPrefix(authHeader, "AWS4-HMAC-SHA256") {
				parts := strings.Split(authHeader, " ")
				for _, part := range parts {
					if strings.HasPrefix(part, "Credential=") {
						cred := strings.TrimPrefix(part, "Credential=")
						accessKey = strings.Split(cred, "/")[0]
						break
					}
				}
			} else if strings.HasPrefix(authHeader, "AWS ") {
				parts := strings.Split(authHeader, " ")
				if len(parts) > 1 {
					accessKey = strings.Split(parts[1], ":")[0]
				}
			} else {
				accessKey = r.URL.Query().Get("X-Amz-Algorithm")
				if accessKey != "" {
					cred := r.URL.Query().Get("X-Amz-Credential")
					if cred != "" {
						accessKey = strings.Split(cred, "/")[0]
					}
				}
			}
		}

		var username string
		var isAdmin bool

		if accessKey != "" {
			dbAccessKey := database.GetSetting("s3_access_key")
			if accessKey == dbAccessKey && dbAccessKey != "" {
				username = database.GetSetting("admin_username")
				if username == "" { username = "admin" }
				isAdmin = true
			} else {
				var child struct {
					Username string `db:"username"`
					Enabled  int    `db:"s3_enabled"`
				}
				err := database.DB.Get(&child, "SELECT username, s3_enabled FROM child_accounts WHERE s3_access_key = ?", accessKey)
				if err == nil && child.Username != "" {
					if child.Enabled == 0 {
						http.Error(w, "S3 API disabled", http.StatusForbidden)
						return
					}
					username = child.Username
					isAdmin = false
				}
			}
		}

		if username == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Normalize path for "Fixed Bucket" mode.
		// We want to force all requests to be treated as if they are against the "telecloud" bucket.
		path := strings.TrimPrefix(r.URL.Path, "/s3")
		path = strings.TrimPrefix(path, "/")
		
		if path != "" {
			parts := strings.Split(path, "/")
			if len(parts) > 0 {
				// If the first part is a known bucket name or just any bucket name, 
				// we strip it to treat the rest as the object key.
				// For Telecloud, we only ever "really" have one bucket called "telecloud".
				if parts[0] == "telecloud" || parts[0] == username || parts[0] == "admin" {
					path = strings.Join(parts[1:], "/")
				}
			}
		}
		
		// Reconstruct the path to always be /telecloud/<key>
		r.URL.Path = "/" + "telecloud/" + strings.TrimPrefix(path, "/")

		backend := NewBackend(cfg, username, isAdmin)
		faker := gofakes3.New(backend)
		faker.Server().ServeHTTP(w, r)
	})
}
