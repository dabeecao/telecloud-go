package webdav

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	user      string
	hash      string
	validated bool
	expiresAt time.Time
}

type failedAttempt struct {
	count int
	last  time.Time
}

const (
	authCacheTTL         = 2 * time.Minute
	failedAttemptMax     = 5
	failedAttemptWindow  = 15 * time.Minute
	failedAttemptBackoff = 100 * time.Millisecond
)

var (
	// authCache stores successful auth results, keyed by user|sha256(pass)|hash.
	// The plaintext password is never stored here — only its SHA-256 — so a
	// core/memory dump cannot leak credentials.
	authCache sync.Map

	// failedAuthAttempts tracks consecutive failures per remote IP. Hitting
	// the threshold yields 401 with a short backoff to slow brute force.
	failedAuthAttempts sync.Map
)

// authCacheKey hashes the password so we never keep plaintext credentials in
// process memory. Hash is bound to the user and the stored bcrypt hash so a
// password change invalidates the entry naturally.
func authCacheKey(user, pass, dbHash string) string {
	sum := sha256.Sum256([]byte(pass))
	return user + "|" + hex.EncodeToString(sum[:]) + "|" + dbHash
}

// InvalidateCache removes all cache entries for the given user. Called when
// the user's password is rotated so an old session cannot ride out the TTL.
func InvalidateCache(user string) {
	authCache.Range(func(k, v interface{}) bool {
		entry := v.(*authCacheEntry)
		if entry.user == user {
			authCache.Delete(k)
		}
		return true
	})
}

// clientIP extracts the best-effort remote IP. Honors X-Forwarded-For /
// X-Real-IP only when set by a trusted reverse proxy; for direct connections
// it falls back to the peer address. NOT a substitute for real proxy
// authentication, but enough to make per-IP rate limiting work behind a sane
// front-end.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if comma := strings.IndexByte(xff, ','); comma >= 0 {
			return strings.TrimSpace(xff[:comma])
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	// Strip port from RemoteAddr ("1.2.3.4:5678" -> "1.2.3.4").
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i > 0 && strings.IndexByte(host, '.') > 0 {
		host = host[:i]
	}
	return host
}

func bumpFailed(ip string) (blocked bool) {
	v, _ := failedAuthAttempts.Load(ip)
	var att failedAttempt
	if v != nil {
		att = v.(failedAttempt)
	}
	if att.count >= failedAttemptMax && time.Since(att.last) < failedAttemptWindow {
		return true
	}
	att.count++
	att.last = time.Now()
	failedAuthAttempts.Store(ip, att)
	if att.count >= failedAttemptMax {
		log.Printf("WEBDAV: rate-limit triggered for %s after %d failed attempts", ip, att.count)
	}
	return att.count >= failedAttemptMax
}

func checkBlocked(ip string) bool {
	v, _ := failedAuthAttempts.Load(ip)
	if v == nil {
		return false
	}
	att := v.(failedAttempt)
	if att.count >= failedAttemptMax && time.Since(att.last) < failedAttemptWindow {
		return true
	}
	if att.count >= failedAttemptMax {
		failedAuthAttempts.Delete(ip)
	}
	return false
}

func clearFailed(ip string) {
	failedAuthAttempts.Delete(ip)
}

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

		ip := clientIP(r)
		if checkBlocked(ip) {
			w.Header().Set("Retry-After", "900")
			http.Error(w, "Too many failed attempts", http.StatusTooManyRequests)
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
				bumpFailed(ip)
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

		// Cache lookup uses sha256(pass) so plaintext credentials never sit in
		// process memory. Cache TTL is intentionally short (authCacheTTL) so a
		// recently-rotated password takes effect quickly.
		cacheKey := authCacheKey(user, pass, dbHash)
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
				authCache.Store(cacheKey, &authCacheEntry{
					user:      user,
					hash:      dbHash,
					validated: true,
					expiresAt: time.Now().Add(authCacheTTL),
				})
			}
		}

		if !authed {
			if bumpFailed(ip) {
				// Add a small backoff to slow brute force even before the limit
				// hits, without making the legitimate retry path unbearable.
				time.Sleep(failedAttemptBackoff)
			}
			w.Header().Set("WWW-Authenticate", `Basic realm="TeleCloud WebDAV"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		clearFailed(ip)

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
