package api

import (
	"net/http"
	"strings"
	"sync"
	"telecloud/database"
	"telecloud/tgclient"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

type chunkState struct {
	sync.Mutex
	received map[int]bool
}

var (
	chunkTrackerSync sync.Map // map[string]*chunkState
	loginAttempts    sync.Map
)

type loginAttempt struct {
	count int
	last  time.Time
}

const csrfCookieName = "csrf_token"
const csrfHeaderName = "X-CSRF-Token"

// generateCSRFToken creates a new random CSRF token
func generateCSRFToken() string {
	return uuid.New().String()
}

// isSecure checks if the application is running on HTTPS based on SITE_URL.
func isSecure() bool {
	siteURL := database.GetSetting("site_url")
	return strings.HasPrefix(siteURL, "https://")
}

// setCSRFCookie sets the CSRF cookie on a response.
// HttpOnly=false so JavaScript can read it to include in request headers.
func setCSRFCookie(c *gin.Context) string {
	token, err := c.Cookie(csrfCookieName)
	if err != nil || token == "" {
		token = generateCSRFToken()
	}
	c.SetCookie(csrfCookieName, token, 3600*24*7, "/", "", isSecure(), false)
	return token
}

// csrfMiddleware validates the X-CSRF-Token header against the csrf_token cookie.
// Applies to all state-changing methods: POST, PUT, PATCH, DELETE.
func csrfMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		method := c.Request.Method
		if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
			c.Next()
			return
		}

		cookieToken, err := c.Cookie(csrfCookieName)
		if err != nil || cookieToken == "" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "csrf token missing"})
			return
		}

		headerToken := c.GetHeader(csrfHeaderName)
		if headerToken == "" || headerToken != cookieToken {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "csrf token invalid"})
			return
		}

		c.Next()
	}
}

// securityHeadersMiddleware adds standard security headers to prevent common web attacks.
func securityHeadersMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "SAMEORIGIN")
		c.Header("X-XSS-Protection", "1; mode=block")
		c.Header("Cross-Origin-Resource-Policy", "cross-origin")
		c.Header("Cross-Origin-Opener-Policy", "same-origin-allow-popups")
		// Basic Content Security Policy
		c.Header("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline' 'unsafe-eval' https://cdnjs.cloudflare.com https://static.cloudflareinsights.com; style-src 'self' 'unsafe-inline' https://cdnjs.cloudflare.com https://fonts.googleapis.com; font-src 'self' https://cdnjs.cloudflare.com https://fonts.gstatic.com; img-src 'self' data: *; connect-src 'self' https://api.github.com https://cloudflareinsights.com https://cdn.plyr.io; media-src 'self' blob: * https://cdn.plyr.io; object-src 'self';")
		c.Next()
	}
}

// setupCheckMiddleware checks if the system needs initial configuration.
func setupCheckMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		if strings.HasPrefix(path, "/static") || strings.HasPrefix(path, "/login") || strings.HasPrefix(path, "/reset-admin") || path == "/api/system/status" {
			c.Next()
			return
		}

		adminUser := database.GetSetting("admin_username")
		isSetupEndpoint := strings.HasPrefix(path, "/api/setup") || strings.HasPrefix(path, "/setup")

		if isSetupEndpoint && adminUser != "" {
			token, _ := c.Cookie("session_token")
			var sessionUsername string
			if token != "" {
				database.DB.Get(&sessionUsername, "SELECT username FROM sessions WHERE token = ?", token)
			}

			// Only admin can access setup once it's configured
			if sessionUsername != adminUser {
				if strings.HasPrefix(path, "/api/") {
					c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "unauthorized"})
				} else {
					c.Redirect(http.StatusFound, "/login")
				}
				return
			}

			// Optimization: If admin is logged in and system is already ready,
			// redirect to dashboard instead of showing setup wizard again
			if path == "/setup" && tgclient.IsSystemReady() {
				c.Redirect(http.StatusFound, "/")
				c.Abort()
				return
			}

			c.Next()
			return
		}

		if isSetupEndpoint {
			c.Next()
			return
		}

		if adminUser == "" {
			c.Redirect(http.StatusFound, "/setup")
			c.Abort()
			return
		}

		// If admin exists but Telegram system is not ready, handle accordingly
		if !tgclient.IsSystemReady() {
			// If the system is currently initializing, show a loading message instead of redirecting to setup
			if tgclient.IsRunning() {
				c.Data(http.StatusServiceUnavailable, "text/html; charset=utf-8", []byte(`
						<!DOCTYPE html><html><head><meta http-equiv="refresh" content="3"><title>Starting up...</title>
						<style>body{font-family:system-ui,-apple-system,sans-serif;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;background:#f8fafc;color:#334155;text-align:center;} h2{margin-bottom:8px;} p{color:#64748b;}</style>
						</head><body><div><h2>TeleCloud is starting up</h2><p>Please wait a few seconds...</p></div></body></html>
					`))
				c.Abort()
				return
			}

			token, _ := c.Cookie("session_token")
			var sessionUsername string
			if token != "" {
				database.DB.Get(&sessionUsername, "SELECT username FROM sessions WHERE token = ?", token)
			}

			if sessionUsername == "" {
				c.Redirect(http.StatusFound, "/login")
				c.Abort()
				return
			}

			if sessionUsername != adminUser {
				c.String(http.StatusForbidden, "System is in maintenance mode. Only admin can access.")
				c.Abort()
				return
			}

			// If admin is logged in, redirect to setup to fix Telegram
			if path != "/setup" {
				c.Redirect(http.StatusFound, "/setup")
				c.Abort()
				return
			}
		}

		c.Next()
	}
}

// authMiddleware handles user authentication and session management.
func authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		var sessionUsername string
		var isAdmin bool
		var forceChange bool

		token, err := c.Cookie("session_token")
		if err == nil && token != "" {
			err = database.DB.Get(&sessionUsername, "SELECT username FROM sessions WHERE token = ?", token)
			if err == nil {
				adminUser := database.GetSetting("admin_username")
				isAdmin = sessionUsername == adminUser

				if !isAdmin {
					database.DB.Get(&forceChange, "SELECT force_password_change FROM child_accounts WHERE username = ?", sessionUsername)
				}
			}
		}

		// Fallback to Basic Auth
		if sessionUsername == "" {
			user, password, hasAuth := c.Request.BasicAuth()
			if hasAuth {
				adminUser := database.GetSetting("admin_username")
				adminPassHash := database.GetSetting("admin_password_hash")
				if user == adminUser && bcrypt.CompareHashAndPassword([]byte(adminPassHash), []byte(password)) == nil {
					sessionUsername = user
					isAdmin = true
				} else {
					var child struct {
						Hash        string `db:"password_hash"`
						ForceChange int    `db:"force_password_change"`
					}
					err := database.DB.Get(&child, "SELECT password_hash, force_password_change FROM child_accounts WHERE username = ?", user)
					if err == nil && bcrypt.CompareHashAndPassword([]byte(child.Hash), []byte(password)) == nil {
						sessionUsername = user
						isAdmin = false
						forceChange = child.ForceChange == 1
					}
				}
			}
		}

		if sessionUsername == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}

		// If password change is forced, only allow the password change API
		if forceChange && c.Request.URL.Path != "/api/settings/password" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "force_password_change", "username": sessionUsername})
			return
		}

		c.Set("username", sessionUsername)
		c.Set("is_admin", isAdmin)
		c.Next()
	}
}
