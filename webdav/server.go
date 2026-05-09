package webdav

import (
	"context"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"telecloud/config"
	"telecloud/database"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/net/webdav"
)

// webdavAuthCache lưu kết quả bcrypt để tránh gọi lại mỗi request
type authCacheEntry struct {
	hash      string
	validated bool
	expiresAt time.Time
}

var (
	authCache   sync.Map // map[string]*authCacheEntry keyed by password
	authCacheTTL = 10 * time.Minute
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
		
		adminUser := database.GetSetting("admin_username")
		adminHash := database.GetSetting("admin_password_hash")

		var authed bool
		var isAdmin bool
		var dbHash string

		if user == adminUser {
			isAdmin = true
			dbHash = adminHash
		} else {
			isAdmin = false
			var userStatus struct {
				PasswordHash string `db:"password_hash"`
				Enabled      int    `db:"webdav_enabled"`
				ForceChange  int    `db:"force_password_change"`
			}
			err := database.RODB.Get(&userStatus, "SELECT password_hash, webdav_enabled, force_password_change FROM child_accounts WHERE username = ?", user)
			if err != nil {
				w.Header().Set("WWW-Authenticate", `Basic realm="TeleCloud WebDAV"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			if userStatus.Enabled == 0 {
				http.Error(w, "WebDAV is disabled for this account", http.StatusForbidden)
				return
			}
			if userStatus.ForceChange == 1 {
				http.Error(w, "Password change required. Please login via web interface first.", http.StatusForbidden)
				return
			}
			dbHash = userStatus.PasswordHash

		}

		// Kiểm tra cache trước khi gọi bcrypt (tốn ~100ms/lần)
		cacheKey := user + "|" + pass + "|" + dbHash
		if v, ok := authCache.Load(cacheKey); ok {
			entry := v.(*authCacheEntry)
			if time.Now().Before(entry.expiresAt) && entry.hash == dbHash {
				authed = entry.validated
			} else {
				authCache.Delete(cacheKey)
			}
		}

		if !authed {
			err := bcrypt.CompareHashAndPassword([]byte(dbHash), []byte(pass))
			if err == nil {
				authed = true
				// Chỉ cache kết quả auth thành công (không cache mật khẩu sai)
				authCache.Store(cacheKey, &authCacheEntry{
					hash:      dbHash,
					validated: true,
					expiresAt: time.Now().Add(authCacheTTL),
				})
			}
		}

		if !authed {
			w.Header().Set("WWW-Authenticate", `Basic realm="TeleCloud WebDAV"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Store user info in context
		ctx := context.WithValue(r.Context(), usernameKey, user)
		ctx = context.WithValue(ctx, isAdminKey, isAdmin)
		r = r.WithContext(ctx)

		// Standard WebDAV headers
		w.Header().Set("DAV", "1, 2")
		
		if r.Method == "OPTIONS" {
			w.Header().Set("Allow", "OPTIONS, GET, HEAD, POST, PUT, DELETE, TRACE, COPY, MOVE, MKCOL, PROPFIND, PROPPATCH, LOCK, UNLOCK")
			w.WriteHeader(http.StatusOK)
			return
		}

		// Handle macOS Finder specific garbage
		if strings.HasPrefix(r.URL.Path, "/webdav/._") || strings.HasPrefix(r.URL.Path, "/webdav/.DS_Store") {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// Intercept GET for thumbnails
		if r.Method == "GET" {
			q := r.URL.Query()
			// Synology: viewer=thumb, Alist: type=thumb, Nextcloud: x-thumbnail=1
			// Expand support for various thumbnail query params used by different apps
			if q.Get("viewer") == "thumb" || q.Get("type") == "thumb" || q.Get("x-thumbnail") == "1" || q.Get("thumbnail") == "1" || q.Has("preview") {
				name := strings.TrimPrefix(r.URL.Path, "/webdav")
				if thumbPath, err := fs.(*telecloudFS).GetThumbnailPath(r.Context(), name); err == nil {
					http.ServeFile(w, r, thumbPath)
					return
				}
			}
		}

		handler.ServeHTTP(w, r)
	})
}


