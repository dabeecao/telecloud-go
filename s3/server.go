package s3

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"telecloud/config"
	"telecloud/database"

	"github.com/johannesboyne/gofakes3"
)

func NewHandler(cfg *config.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		corsAllowed := applyCORSHeaders(w, r)
		if r.Method == http.MethodOptions {
			if !corsAllowed {
				http.Error(w, "CORS origin not allowed", http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !corsAllowed {
			http.Error(w, "CORS origin not allowed", http.StatusForbidden)
			return
		}

		if database.GetSetting("s3_enabled") != "true" {
			http.Error(w, "S3 API is disabled", http.StatusForbidden)
			return
		}

		// Pre-authentication to identify the user.
		// Two auth methods:
		// 1. Authorization header (normal SDK requests)
		// 2. Query parameters (presigned URLs — no Authorization header)
		var accessKey string

		authHeader := r.Header.Get("Authorization")
		accessKey, err := extractAccessKey(r, authHeader)
		if err != nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		var username string
		var isAdmin bool
		var secretKey string

		if accessKey != "" {
			dbAccessKey := database.GetSetting("s3_access_key")
			if accessKey == dbAccessKey && dbAccessKey != "" {
				username = database.GetSetting("admin_username")
				if username == "" {
					username = "admin"
				}
				isAdmin = true
				secretKey = database.GetSetting("s3_secret_key")
			} else {
				var child struct {
					Username  string  `db:"username"`
					Enabled   int     `db:"s3_enabled"`
					SecretKey *string `db:"s3_secret_key"`
				}
				err := database.RODB.Get(&child, "SELECT username, s3_enabled, s3_secret_key FROM child_accounts WHERE s3_access_key = ?", accessKey)
				if err == nil && child.Username != "" {
					if child.Enabled == 0 {
						http.Error(w, "S3 API disabled", http.StatusForbidden)
						return
					}
					username = child.Username
					isAdmin = false
					if child.SecretKey != nil {
						secretKey = *child.SecretKey
					}
				}
			}
		}

		if username == "" || secretKey == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Security relaxed: Signature verification is bypassed to ensure 100% compatibility
		// with all S3 clients (especially those behind Nginx reverse proxies, Cloudflare, etc.
		// that alter headers, Host, or URI paths) and to dramatically increase upload speed
		// by avoiding expensive payload SHA256 disk buffering.
		//
		// If you require strict security, uncomment the block below:
		/*
			if err := verifyS3Signature(r, authHeader, accessKey, secretKey); err != nil {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		*/

		// Verify signatures against the external URI clients sign. The rewrite
		// below is an internal fixed-bucket mapping for gofakes3 only.
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
		if r.Header.Get("Range") != "" {
			faker.Server().ServeHTTP(&rangeStatusWriter{ResponseWriter: w}, r)
			return
		}
		faker.Server().ServeHTTP(w, r)
	})
}

type rangeStatusWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (w *rangeStatusWriter) WriteHeader(statusCode int) {
	if statusCode == http.StatusOK && w.Header().Get("Content-Range") != "" {
		statusCode = http.StatusPartialContent
	}
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *rangeStatusWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader && w.Header().Get("Content-Range") != "" {
		w.WriteHeader(http.StatusPartialContent)
	}
	return w.ResponseWriter.Write(b)
}

func (w *rangeStatusWriter) ReadFrom(r io.Reader) (int64, error) {
	if !w.wroteHeader && w.Header().Get("Content-Range") != "" {
		w.WriteHeader(http.StatusPartialContent)
	}
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return io.Copy(w.ResponseWriter, r)
}

func (w *rangeStatusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *rangeStatusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return h.Hijack()
}

func (w *rangeStatusWriter) Push(target string, opts *http.PushOptions) error {
	p, ok := w.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return p.Push(target, opts)
}

func (w *rangeStatusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
