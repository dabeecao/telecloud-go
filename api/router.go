package api

import (
	"context"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"telecloud/config"
	"telecloud/database"
	"telecloud/s3"
	"telecloud/tgclient"
	"telecloud/utils"
	"telecloud/webdav"
	"telecloud/ws"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"rsc.io/qr"
)

type chunkState struct {
	sync.Mutex
	received map[int]bool
}

var (
	chunkTrackerSync sync.Map // map[string]*chunkState
)

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

type PasteRequest struct {
	Action      string `json:"action"`
	ItemIDs     []int  `json:"item_ids"`
	Destination string `json:"destination"`
}

var loginAttempts sync.Map

type loginAttempt struct {
	count int
	last  time.Time
}

func mapPath(userPath, username string, isAdmin bool) string {
	cleanPath := path.Clean("/" + userPath)
	if isAdmin {
		return cleanPath
	}
	if cleanPath == "/" {
		return "/" + username
	}
	return "/" + username + cleanPath
}

func unmapPath(dbPath, username string, isAdmin bool) string {
	if isAdmin {
		return dbPath
	}
	prefix := "/" + username
	if dbPath == prefix {
		return "/"
	}
	if strings.HasPrefix(dbPath, prefix+"/") {
		return strings.TrimPrefix(dbPath, prefix)
	}
	return dbPath
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func verifyItemAccess(c *gin.Context, path string) bool {
	isAdmin := c.GetBool("is_admin")
	if isAdmin {
		return true
	}
	username := c.GetString("username")
	prefix := "/" + username
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func isChildAccountPath(dbPath string) bool {
	return isChildAccountPathQuery(database.DB, dbPath)
}

func isChildAccountPathQuery(q database.Queryer, dbPath string) bool {
	cleanPath := path.Clean(dbPath)
	if cleanPath == "/" {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(cleanPath, "/"), "/")
	rootFolder := parts[0]

	var exists int
	q.Get(&exists, "SELECT COUNT(*) FROM child_accounts WHERE username = ?", rootFolder)
	return exists > 0
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
		c.Header("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline' 'unsafe-eval' https://cdnjs.cloudflare.com https://static.cloudflareinsights.com; style-src 'self' 'unsafe-inline' https://cdnjs.cloudflare.com https://fonts.googleapis.com; font-src 'self' https://cdnjs.cloudflare.com https://fonts.gstatic.com; img-src 'self' data: *; connect-src 'self' https://api.github.com https://cloudflareinsights.com https://cdn.plyr.io; media-src 'self' blob: * https://cdn.plyr.io;")
		c.Next()
	}
}

func SetupRouter(cfg *config.Config, contentFS fs.FS, startTG func(cfg *config.Config), restartApp func()) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()
	// Trust private network IPs and loopback for reverse proxies (e.g. Cloudflare Tunnel, Nginx, Docker)
	r.SetTrustedProxies([]string{"127.0.0.0/8", "::1", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"})

	templ := template.Must(template.New("").ParseFS(contentFS, "templates/*"))
	r.SetHTMLTemplate(templ)

	staticFS, err := fs.Sub(contentFS, "static")
	if err == nil {
		r.StaticFS("/static", http.FS(staticFS))
	}

	// Middleware for checking if setup is needed
	setupCheckMiddleware := func() gin.HandlerFunc {
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

	r.Use(securityHeadersMiddleware())
	r.Use(setupCheckMiddleware())

	// Middleware for auth
	authMiddleware := func() gin.HandlerFunc {
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

	// WebDAV Route
	h := gin.WrapH(webdav.NewHandler(cfg))

	// S3 Route
	s3h := gin.WrapH(s3.NewHandler(cfg))

	methods := []string{
		"GET", "POST", "PUT", "PATCH", "HEAD", "OPTIONS", "DELETE", "CONNECT", "TRACE",
		"PROPFIND", "PROPPATCH", "MKCOL", "COPY", "MOVE", "LOCK", "UNLOCK",
	}
	for _, method := range methods {
		r.Handle(method, "/webdav", h)
		r.Handle(method, "/webdav/*path", h)
		r.Handle(method, "/s3", s3h)
		r.Handle(method, "/s3/*path", s3h)
	}

	r.GET("/setup", func(c *gin.Context) {
		adminUser := database.GetSetting("admin_username")
		setCSRFCookie(c)
		c.HTML(http.StatusOK, "setup.html", gin.H{
			"version":      cfg.Version,
			"api_id":       cfg.APIID,
			"api_hash":     cfg.APIHash,
			"log_group_id": cfg.LogGroupID,
			"admin_exists": adminUser != "",
		})
	})

	r.POST("/setup", csrfMiddleware(), func(c *gin.Context) {
		adminUser := database.GetSetting("admin_username")
		if adminUser != "" {
			c.JSON(http.StatusForbidden, gin.H{"error": "already setup"})
			return
		}

		username := c.PostForm("username")
		password := c.PostForm("password")

		if username == "" || password == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "username and password required"})
			return
		}

		// Security: Validate username format to prevent XSS and path issues
		validUsername := regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
		if !validUsername.MatchString(username) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_USERNAME_FORMAT"})
			return
		}

		hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to hash password"})
			return
		}

		database.SetSetting("admin_username", username)
		database.SetSetting("admin_password_hash", string(hashedPassword))
		database.SetSetting("webdav_enabled", "false")

		// Create session
		sessionToken := uuid.New().String()
		_, err = database.DB.Exec("INSERT INTO sessions (token, username) VALUES (?, ?)", sessionToken, username)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create session"})
			return
		}
		c.SetCookie("session_token", sessionToken, 3600*24*30, "/", "", isSecure(), true)

		c.JSON(http.StatusOK, gin.H{"status": "success"})
	})

	r.POST("/api/setup/config", csrfMiddleware(), func(c *gin.Context) {
		apiID, _ := strconv.Atoi(c.PostForm("api_id"))
		apiHash := c.PostForm("api_hash")
		siteURL := strings.TrimRight(c.PostForm("site_url"), "/")

		if apiID == 0 || apiHash == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "API_ID and API_HASH required"})
			return
		}

		database.SetSetting("api_id", strconv.Itoa(apiID))
		database.SetSetting("api_hash", apiHash)
		database.SetSetting("site_url", siteURL)

		// Auto-configure WebAuthn based on Site URL
		if u, err := url.Parse(siteURL); err == nil {
			database.SetSetting("webauthn_rpid", u.Hostname())
			database.SetSetting("webauthn_rporigin", siteURL)
		}

		cfg.APIID = apiID
		cfg.APIHash = apiHash

		c.JSON(http.StatusOK, gin.H{"status": "success"})
	})

	r.POST("/api/setup/tg/phone", csrfMiddleware(), func(c *gin.Context) {
		phone := c.PostForm("phone")

		// Always restart for a fresh phone login attempt to avoid conflicts with QR
		tgclient.StartWebAuth(cfg)
		wa := tgclient.GetActiveWebAuth()

		if wa != nil {
			wa.SubmitPhone(phone)
		}
		c.JSON(http.StatusOK, gin.H{"status": "success"})
	})

	r.POST("/api/setup/tg/qr", csrfMiddleware(), func(c *gin.Context) {
		// Always restart for a fresh QR login attempt
		tgclient.StartQRAuth(cfg)
		c.JSON(http.StatusOK, gin.H{"status": "success"})
	})

	r.GET("/api/setup/tg/qr/image", func(c *gin.Context) {
		wa := tgclient.GetActiveWebAuth()
		if wa == nil || wa.GetQRURL() == "" {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}

		code, err := qr.Encode(wa.GetQRURL(), qr.M)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "QR generation failed"})
			return
		}

		c.Header("Content-Type", "image/png")
		c.Header("Cache-Control", "no-cache")
		c.Writer.Write(code.PNG())
	})

	r.POST("/api/setup/tg/code", csrfMiddleware(), func(c *gin.Context) {
		code := c.PostForm("code")
		wa := tgclient.GetActiveWebAuth()
		if wa != nil {
			wa.SubmitCode(code)
			c.JSON(http.StatusOK, gin.H{"status": "success"})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "SESSION_EXPIRED"})
		}
	})

	r.POST("/api/setup/tg/password", csrfMiddleware(), func(c *gin.Context) {
		password := c.PostForm("password")
		wa := tgclient.GetActiveWebAuth()
		if wa != nil {
			wa.SubmitPassword(password)
			c.JSON(http.StatusOK, gin.H{"status": "success"})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "SESSION_EXPIRED"})
		}
	})

	r.POST("/api/setup/tg/cancel", csrfMiddleware(), func(c *gin.Context) {
		wa := tgclient.GetActiveWebAuth()
		if wa != nil {
			wa.Cancel(fmt.Errorf("USER_CANCELLED"))
		}
		c.JSON(http.StatusOK, gin.H{"status": "success"})
	})

	r.POST("/api/setup/tg/test-log-group", csrfMiddleware(), func(c *gin.Context) {
		logGroupID := c.PostForm("log_group_id")
		if logGroupID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "log_group_id required"})
			return
		}

		database.SetSetting("log_group_id", logGroupID)
		cfg.LogGroupID = logGroupID

		// During setup, we only want to test the main client, so skip bots to avoid FLOOD_WAIT
		tgclient.SkipBotPool = true
		
		// Attempt synchronous verification
		if err := tgclient.VerifyLogGroup(c.Request.Context(), cfg); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		// If verification succeeded, start the background services
		startTG(cfg)

		c.JSON(http.StatusOK, gin.H{"status": "success"})
	})

	r.POST("/api/setup/restart", csrfMiddleware(), func(c *gin.Context) {
		adminUser := database.GetSetting("admin_username")
		if adminUser == "" {
			// If no admin, this shouldn't be called yet but let's be safe
			c.JSON(http.StatusForbidden, gin.H{"error": "setup not finished"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"status": "restarting"})
		go restartApp()
	})

	r.GET("/api/system/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"authorized": tgclient.IsAuthorized(),
			"ready":      tgclient.IsSystemReady(),
			"running":    tgclient.IsRunning(),
		})
	})

	r.GET("/api/setup/tg/status", func(c *gin.Context) {
		wa := tgclient.GetActiveWebAuth()
		
		// Prioritize returning the last error if we are not authorized and an error exists
		if !tgclient.IsAuthorized() && tgclient.LastAuthError != nil {
			errStr := tgclient.LastAuthError.Error()
			authState := "none"
			if wa != nil {
				authState = wa.GetState()
			}
			c.JSON(http.StatusOK, gin.H{"authorized": false, "authState": authState, "error": errStr})
			return
		}

		if tgclient.Client == nil && wa == nil {
			c.JSON(http.StatusOK, gin.H{"authorized": false, "authState": "none"})
			return
		}

		authState := "none"
		if wa != nil {
			authState = wa.GetState()
			transErr := wa.GetTransientErr()
			if transErr != "" {
				wa.SetTransientErr("")
			}
			
			// If web auth is active, we know it's not authorized yet and we don't want to block on Status()
			c.JSON(http.StatusOK, gin.H{
				"authorized":      authState == "success",
				"authState":       authState,
				"qr_url":          wa.GetQRURL(),
				"transient_error": transErr,
			})
			return
		}

		// Check status using a quick timeout to prevent hanging if client is stuck
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		
		status, err := tgclient.GetAuthStatus(ctx)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"authorized": false, "error": err.Error(), "authState": authState})
			return
		}

		c.JSON(http.StatusOK, gin.H{"authorized": status.Authorized, "authState": authState})
	})

	r.GET("/reset-admin", func(c *gin.Context) {
		token := c.Query("token")
		dbToken := database.GetSetting("admin_reset_token")
		expiryStr := database.GetSetting("admin_reset_expiry")

		if token == "" || token != dbToken {
			c.String(http.StatusForbidden, "Invalid token")
			return
		}

		expiry, _ := strconv.ParseInt(expiryStr, 10, 64)
		if time.Now().Unix() > expiry {
			c.String(http.StatusForbidden, "Token expired")
			return
		}

		setCSRFCookie(c)
		c.HTML(http.StatusOK, "reset-admin.html", gin.H{
			"version": cfg.Version,
		})
	})

	r.POST("/reset-admin", csrfMiddleware(), func(c *gin.Context) {
		token := c.PostForm("token")
		password := c.PostForm("password")

		dbToken := database.GetSetting("admin_reset_token")
		expiryStr := database.GetSetting("admin_reset_expiry")

		if token == "" || token != dbToken {
			c.JSON(http.StatusForbidden, gin.H{"error": "invalid_token"})
			return
		}

		expiry, _ := strconv.ParseInt(expiryStr, 10, 64)
		if time.Now().Unix() > expiry {
			c.JSON(http.StatusForbidden, gin.H{"error": "token_expired"})
			return
		}

		hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_hash_password"})
			return
		}

		database.SetSetting("admin_password_hash", string(hashedPassword))
		database.DeleteSetting("admin_reset_token")
		database.DeleteSetting("admin_reset_expiry")

		// Clear admin sessions
		adminUser := database.GetSetting("admin_username")
		if adminUser != "" {
			database.DB.Exec("DELETE FROM sessions WHERE username = ?", adminUser)
		}

		c.JSON(http.StatusOK, gin.H{"status": "success"})
	})

	r.GET("/", func(c *gin.Context) {

		token, _ := c.Cookie("session_token")
		var sessionUsername string
		if token != "" {
			database.DB.Get(&sessionUsername, "SELECT username FROM sessions WHERE token = ?", token)
		}
		if token == "" || sessionUsername == "" {
			c.Redirect(http.StatusFound, "/login")
			return
		}

		setCSRFCookie(c)
		webdavEnabled := database.GetSetting("webdav_enabled") == "true"
		webdavUser := database.GetSetting("admin_username")
		uploadAPIEnabled := database.GetSetting("upload_api_enabled") == "true"
		uploadAPIKey := database.GetSetting("upload_api_key")
		isAdmin := sessionUsername == webdavUser

		globalWebdavEnabled := database.GetSetting("webdav_enabled") == "true"
		globalUploadAPIEnabled := database.GetSetting("upload_api_enabled") == "true"

		if !isAdmin {
			var userStatus struct {
				WebDAVEnabled int `db:"webdav_enabled"`
				APIEnabled    int `db:"api_enabled"`
			}
			err := database.DB.Get(&userStatus, "SELECT webdav_enabled, api_enabled FROM child_accounts WHERE username = ?", sessionUsername)
			if err == nil {
				webdavEnabled = (globalWebdavEnabled && userStatus.WebDAVEnabled == 1)
				uploadAPIEnabled = (globalUploadAPIEnabled && userStatus.APIEnabled == 1)
			}
			webdavUser = sessionUsername
		}

		var userStorageUsed int64
		if isAdmin {
			database.DB.Get(&userStorageUsed, "SELECT COALESCE(SUM(size), 0) FROM files WHERE is_folder = 0")
		} else {
			prefix := "/" + sessionUsername
			database.DB.Get(&userStorageUsed, "SELECT COALESCE(SUM(size), 0) FROM files WHERE (path = ? OR path LIKE ?) AND is_folder = 0", prefix, prefix+"/%")
		}

		// S3 for the current session user
		var s3Enabled bool
		var s3AccessKey string
		var s3SecretKey string

		if isAdmin {
			s3Enabled = database.GetSetting("s3_enabled") == "true"
			s3AccessKey = database.GetSetting("s3_access_key")
			s3SecretKey = database.GetSetting("s3_secret_key")
		} else {
			var childS3 struct {
				Enabled   int     `db:"s3_enabled"`
				AccessKey *string `db:"s3_access_key"`
				SecretKey *string `db:"s3_secret_key"`
			}
			err := database.DB.Get(&childS3, "SELECT s3_enabled, s3_access_key, s3_secret_key FROM child_accounts WHERE username = ?", sessionUsername)
			if err == nil {
				s3Enabled = childS3.Enabled == 1 && database.GetSetting("s3_enabled") == "true"
				if childS3.AccessKey != nil {
					s3AccessKey = *childS3.AccessKey
				}
				if childS3.SecretKey != nil {
					s3SecretKey = *childS3.SecretKey
				}
			}
		}

		c.HTML(http.StatusOK, "index.html", gin.H{
			"webdav_enabled":        webdavEnabled,
			"global_webdav_enabled": globalWebdavEnabled,
			"webdav_user":           webdavUser,
			"s3_enabled":            s3Enabled,
			"global_s3_enabled":     database.GetSetting("s3_enabled") == "true",
			"s3_access_key":         s3AccessKey,
			"s3_secret_key":         s3SecretKey,
			"upload_api_enabled":    uploadAPIEnabled,
			"global_api_enabled":    globalUploadAPIEnabled,
			"upload_api_key":        uploadAPIKey,
			"webauthn_rpid":         database.GetSetting("webauthn_rpid"),
			"webauthn_rporigin":     database.GetSetting("webauthn_rporigin"),
			"version":               cfg.Version,
			"is_admin":              isAdmin,
			"username":              sessionUsername,
			"storage_used":          userStorageUsed,
			"theme":                 database.GetUserSetting(sessionUsername, "theme"),
		})
	})

	r.GET("/login", func(c *gin.Context) {
		token, _ := c.Cookie("session_token")
		var sessionUsername string
		if token != "" {
			database.DB.Get(&sessionUsername, "SELECT username FROM sessions WHERE token = ?", token)
		}
		if token != "" && sessionUsername != "" {
			c.Redirect(http.StatusFound, "/")
			return
		}
		setCSRFCookie(c)
		c.HTML(http.StatusOK, "login.html", gin.H{
			"version": cfg.Version,
		})
	})

	r.POST("/login", func(c *gin.Context) {
		ip := c.ClientIP()
		val, _ := loginAttempts.Load(ip)
		var att loginAttempt
		if val != nil {
			att = val.(loginAttempt)
			if att.count >= 5 && time.Since(att.last) < 15*time.Minute {
				c.JSON(http.StatusTooManyRequests, gin.H{"error": "too_many_requests"})
				return
			}
		}

		username := c.PostForm("username")
		password := c.PostForm("password")

		dbUser := database.GetSetting("admin_username")
		dbHash := database.GetSetting("admin_password_hash")

		var authSuccess bool
		var forceChange bool
		if username == dbUser && bcrypt.CompareHashAndPassword([]byte(dbHash), []byte(password)) == nil {
			authSuccess = true
		} else {
			var child struct {
				Hash        string `db:"password_hash"`
				ForceChange int    `db:"force_password_change"`
			}
			err := database.DB.Get(&child, "SELECT password_hash, force_password_change FROM child_accounts WHERE username = ?", username)
			if err == nil && bcrypt.CompareHashAndPassword([]byte(child.Hash), []byte(password)) == nil {
				authSuccess = true
				forceChange = child.ForceChange == 1
			}
		}

		if authSuccess {
			if forceChange {
				c.JSON(http.StatusOK, gin.H{"status": "force_password_change", "username": username})
				return
			}
			loginAttempts.Delete(ip) // Reset on success
			sessionToken := uuid.New().String()
			_, err := database.DB.Exec("INSERT INTO sessions (token, username) VALUES (?, ?)", sessionToken, username)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create session"})
				return
			}
			c.SetCookie("session_token", sessionToken, 3600*24*30, "/", "", isSecure(), true)
			c.JSON(http.StatusOK, gin.H{"status": "success"})
			return
		}

		// On failure
		att.count++
		att.last = time.Now()
		loginAttempts.Store(ip, att)

		// Artificial delay to thwart fast scripts
		time.Sleep(1 * time.Second)

		if att.count >= 5 {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "ip_blocked"})
		} else {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		}
	})

	r.POST("/logout", csrfMiddleware(), func(c *gin.Context) {
		token, _ := c.Cookie("session_token")
		if token != "" {
			database.DB.Exec("DELETE FROM sessions WHERE token = ?", token)
		}
		c.SetCookie("session_token", "", -1, "/", "", isSecure(), true)
		c.SetCookie(csrfCookieName, "", -1, "/", "", isSecure(), false)
		c.JSON(http.StatusOK, gin.H{"status": "success"})
	})

	// --- Public Upload API endpoint (Bearer token, synchronous) ---
	r.POST("/api/upload-api/upload", func(c *gin.Context) {
		if database.GetSetting("upload_api_enabled") != "true" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Upload API is disabled"})
			return
		}

		authHeader := c.GetHeader("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or missing Authorization header"})
			return
		}
		token := strings.TrimPrefix(authHeader, "Bearer ")

		var username string
		var isAdmin bool

		adminKey := database.GetSetting("upload_api_key")
		if token == adminKey && adminKey != "" {
			isAdmin = true
		} else {
			var userStatus struct {
				Username    string `db:"username"`
				Enabled     int    `db:"api_enabled"`
				ForceChange int    `db:"force_password_change"`
			}
			err := database.DB.Get(&userStatus, "SELECT username, api_enabled, force_password_change FROM child_accounts WHERE api_key = ?", token)
			if err != nil || userStatus.Username == "" {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid API key"})
				return
			}
			if userStatus.Enabled == 0 {
				c.JSON(http.StatusForbidden, gin.H{"error": "API is disabled for this account"})
				return
			}
			if userStatus.ForceChange == 1 {
				c.JSON(http.StatusForbidden, gin.H{"error": "Password change required via web interface before using API"})
				return
			}

			username = userStatus.Username
			isAdmin = false
		}

		file, header, err := c.Request.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "No file provided"})
			return
		}
		defer file.Close()

		filename := filepath.Base(header.Filename)
		path := c.PostForm("path")
		if path == "" {
			path = "/"
		}

		dbPath := mapPath(path, username, isAdmin)

		if isAdmin && isChildAccountPath(dbPath) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Admin cannot upload to child account directory"})
			return
		}
		shareMode := c.PostForm("share") // "public" → auto share link

		// Save to temp file
		taskID := uuid.New().String()
		os.MkdirAll(cfg.TempDir, os.ModePerm)
		tempFilePath := filepath.Join(cfg.TempDir, taskID+"_"+filename)

		out, err := os.OpenFile(tempFilePath, os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save file"})
			return
		}
		_, err = io.Copy(out, file)
		out.Close()
		if err != nil {
			os.Remove(tempFilePath)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to write file"})
			return
		}

		// Multi-part upload allows any size, splitting will happen in ProcessCompleteUpload

		mimeType := header.Header.Get("Content-Type")
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}

		async := c.PostForm("async") == "true"
		overwrite := c.PostForm("overwrite") == "true"

		// If async is requested AND they don't need a public share link immediately,
		// we can process this in the background.
		if async && shareMode != "public" {
			go func() {
				tgclient.ProcessCompleteUpload(context.Background(), tempFilePath, filename, dbPath, mimeType, taskID, cfg, overwrite, username)
				os.Remove(tempFilePath)
			}()

			c.JSON(http.StatusOK, gin.H{
				"status":   "processing",
				"task_id":  taskID,
				"filename": filename,
				"path":     path,
			})
			return
		}

		// Synchronous upload — block until Telegram upload + DB insert done
		defer os.Remove(tempFilePath)
		fileID, finalName, err := tgclient.ProcessCompleteUploadSync(c.Request.Context(), tempFilePath, filename, dbPath, mimeType, cfg, overwrite, username)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Upload failed: " + err.Error()})
			return
		}

		resp := gin.H{
			"status":   "done",
			"filename": finalName,
			"path":     path,
			"file_id":  fileID,
		}

		// If share=public, create share token and return links
		if shareMode == "public" {
			shareToken := uuid.New().String()
			directToken := utils.GenerateDirectToken(shareToken)
			database.DB.Exec("UPDATE files SET share_token = ? WHERE id = ?", shareToken, fileID)

			scheme := "http"
			if c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https" {
				scheme = "https"
			}
			origin := scheme + "://" + c.Request.Host

			resp["share_token"] = shareToken
			resp["share_link"] = origin + "/s/" + shareToken
			resp["direct_link"] = origin + "/dl/" + directToken
		}

		c.JSON(http.StatusOK, resp)
	})

	r.GET("/api/passkey/login/begin", LoginPasskeyBegin)
	r.POST("/api/passkey/login/finish", LoginPasskeyFinish)

	api := r.Group("/api")
	api.Use(authMiddleware())
	api.Use(csrfMiddleware())
	{
		api.GET("/passkey/register/begin", RegisterPasskeyBegin)
		api.POST("/passkey/register/finish", RegisterPasskeyFinish)
		api.GET("/passkeys", ListPasskeys)
		api.DELETE("/passkeys/:id", DeletePasskey)
		api.POST("/passkeys/:id/rename", RenamePasskey)
		api.POST("/settings/password", func(c *gin.Context) {
			oldPassword := c.PostForm("old_password")
			newPassword := c.PostForm("new_password")

			username := c.GetString("username")
			isAdmin := c.GetBool("is_admin")

			var dbHash string
			var forceChange int
			if isAdmin {
				dbHash = database.GetSetting("admin_password_hash")
			} else {
				err := database.DB.Get(&dbHash, "SELECT password_hash FROM child_accounts WHERE username = ?", username)
				if err != nil {
					c.JSON(http.StatusForbidden, gin.H{"error": "user_not_found"})
					return
				}
				database.DB.Get(&forceChange, "SELECT force_password_change FROM child_accounts WHERE username = ?", username)
			}

			if oldPassword != "" || forceChange == 0 {
				if bcrypt.CompareHashAndPassword([]byte(dbHash), []byte(oldPassword)) != nil {
					c.JSON(http.StatusForbidden, gin.H{"error": "incorrect_old_password"})
					return
				}
			}

			hashedPassword, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_hash_password"})
				return
			}

			if isAdmin {
				database.SetSetting("admin_password_hash", string(hashedPassword))
			} else {
				database.DB.Exec("UPDATE child_accounts SET password_hash = ?, force_password_change = 0 WHERE username = ?", string(hashedPassword), username)
			}

			// If user doesn't have a session yet (e.g. just performed a forced password change via Basic Auth),
			// create one now so they are logged in immediately.
			token, _ := c.Cookie("session_token")
			if token == "" {
				sessionToken := uuid.New().String()
				_, err = database.DB.Exec("INSERT INTO sessions (token, username) VALUES (?, ?)", sessionToken, username)
				if err == nil {
					c.SetCookie("session_token", sessionToken, 3600*24*30, "/", "", isSecure(), true)
				}
			}

			c.JSON(http.StatusOK, gin.H{"status": "success"})
		})

		api.POST("/settings/webdav", func(c *gin.Context) {
			if !c.GetBool("is_admin") {
				c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
				return
			}
			enabled := c.PostForm("enabled")
			if enabled == "true" {
				database.SetSetting("webdav_enabled", "true")
			} else {
				database.SetSetting("webdav_enabled", "false")
			}
			c.JSON(http.StatusOK, gin.H{"status": "success"})
		})

		api.POST("/settings/upload-api", func(c *gin.Context) {
			if !c.GetBool("is_admin") {
				c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
				return
			}
			enabled := c.PostForm("enabled")
			if enabled == "true" {
				database.SetSetting("upload_api_enabled", "true")
			} else {
				database.SetSetting("upload_api_enabled", "false")
			}
			c.JSON(http.StatusOK, gin.H{"status": "success"})
		})

		api.POST("/settings/upload-api/regenerate-key", func(c *gin.Context) {
			if !c.GetBool("is_admin") {
				c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
				return
			}
			newKey := uuid.New().String()
			database.SetSetting("upload_api_key", newKey)
			c.JSON(http.StatusOK, gin.H{"status": "success", "api_key": newKey})
		})

		api.DELETE("/settings/upload-api/key", func(c *gin.Context) {
			if !c.GetBool("is_admin") {
				c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
				return
			}
			database.SetSetting("upload_api_key", "")
			c.JSON(http.StatusOK, gin.H{"status": "success"})
		})

		api.POST("/settings/s3", func(c *gin.Context) {
			if !c.GetBool("is_admin") {
				c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
				return
			}
			enabled := c.PostForm("enabled")
			if enabled == "true" {
				database.SetSetting("s3_enabled", "true")
			} else {
				database.SetSetting("s3_enabled", "false")
			}
			c.JSON(http.StatusOK, gin.H{"status": "success"})
		})

		api.POST("/settings/s3/credentials", func(c *gin.Context) {
			accessKey := c.PostForm("access_key")
			secretKey := c.PostForm("secret_key")

			if c.GetBool("is_admin") {
				database.SetSetting("s3_access_key", accessKey)
				database.SetSetting("s3_secret_key", secretKey)
			} else {
				username := c.GetString("username")
				_, err := database.DB.Exec("UPDATE child_accounts SET s3_access_key = ?, s3_secret_key = ? WHERE username = ?", accessKey, secretKey, username)
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					return
				}
			}
			c.JSON(http.StatusOK, gin.H{"status": "success"})
		})

		api.POST("/settings/child-s3", func(c *gin.Context) {
			if c.GetBool("is_admin") {
				c.JSON(http.StatusForbidden, gin.H{"error": "Admins should use global S3 toggle"})
				return
			}
			username := c.GetString("username")
			enabled := c.PostForm("enabled") == "true"

			// Check if global S3 is enabled
			if enabled && database.GetSetting("s3_enabled") != "true" {
				c.JSON(http.StatusForbidden, gin.H{"error": "ADMIN_DISABLED"})
				return
			}

			val := 0
			if enabled {
				val = 1
			}
			_, err := database.DB.Exec("UPDATE child_accounts SET s3_enabled = ? WHERE username = ?", val, username)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"status": "success"})
		})

		api.GET("/settings/user", func(c *gin.Context) {
			username := c.GetString("username")
			theme := database.GetUserSetting(username, "theme")
			c.JSON(http.StatusOK, gin.H{
				"theme": theme,
			})
		})

		api.POST("/settings/user/theme", func(c *gin.Context) {
			username := c.GetString("username")
			theme := c.PostForm("theme")
			err := database.SetUserSetting(username, "theme", theme)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save theme"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"status": "success"})
		})

		api.GET("/settings/child-api-key", func(c *gin.Context) {
			if c.GetBool("is_admin") {
				c.JSON(http.StatusForbidden, gin.H{"error": "Admins should use global API key"})
				return
			}
			username := c.GetString("username")
			var apiKey *string
			err := database.DB.Get(&apiKey, "SELECT api_key FROM child_accounts WHERE username = ?", username)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"api_key": apiKey})
		})

		api.POST("/settings/child-api-key", func(c *gin.Context) {
			if c.GetBool("is_admin") {
				c.JSON(http.StatusForbidden, gin.H{"error": "Admins should use global API key"})
				return
			}
			username := c.GetString("username")
			newKey := utils.GenerateRandomString(32)
			_, err := database.DB.Exec("UPDATE child_accounts SET api_key = ? WHERE username = ?", newKey, username)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"api_key": newKey})
		})

		api.DELETE("/settings/child-api-key", func(c *gin.Context) {
			if c.GetBool("is_admin") {
				c.JSON(http.StatusForbidden, gin.H{"error": "Admins should use global API key"})
				return
			}
			username := c.GetString("username")
			_, err := database.DB.Exec("UPDATE child_accounts SET api_key = NULL WHERE username = ?", username)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"status": "success"})
		})

		api.POST("/settings/child-webdav", func(c *gin.Context) {
			if c.GetBool("is_admin") {
				c.JSON(http.StatusForbidden, gin.H{"error": "Admins should use global WebDAV toggle"})
				return
			}
			username := c.GetString("username")
			enabled := c.PostForm("enabled") == "true"

			// Check if global WebDAV is enabled
			if enabled && database.GetSetting("webdav_enabled") != "true" {
				c.JSON(http.StatusForbidden, gin.H{"error": "ADMIN_DISABLED"})
				return
			}

			val := 0
			if enabled {
				val = 1
			}
			_, err := database.DB.Exec("UPDATE child_accounts SET webdav_enabled = ? WHERE username = ?", val, username)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"status": "success"})
		})

		api.POST("/settings/child-api", func(c *gin.Context) {
			if c.GetBool("is_admin") {
				c.JSON(http.StatusForbidden, gin.H{"error": "Admins should use global API toggle"})
				return
			}
			username := c.GetString("username")
			enabled := c.PostForm("enabled") == "true"

			// Check if global API is enabled
			if enabled && database.GetSetting("upload_api_enabled") != "true" {
				c.JSON(http.StatusForbidden, gin.H{"error": "ADMIN_DISABLED"})
				return
			}

			val := 0
			if enabled {
				val = 1
			}
			_, err := database.DB.Exec("UPDATE child_accounts SET api_enabled = ? WHERE username = ?", val, username)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"status": "success"})
		})

		api.POST("/settings/webauthn", func(c *gin.Context) {
			if !c.GetBool("is_admin") {
				c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
				return
			}
			rpid := c.PostForm("rpid")
			origins := c.PostForm("origins")

			database.SetSetting("webauthn_rpid", rpid)
			database.SetSetting("webauthn_rporigin", origins)

			// Re-initialize WebAuthn
			originList := []string{}
			if origins != "" {
				originList = strings.Split(origins, ",")
			}
			InitWebAuthn(rpid, originList)

			c.JSON(http.StatusOK, gin.H{"status": "success"})
		})

		api.GET("/users", func(c *gin.Context) {
			if !c.GetBool("is_admin") {
				c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
				return
			}
			var users []database.User
			err := database.DB.Select(&users, "SELECT id, username, created_at FROM child_accounts ORDER BY id DESC")
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}

			// Calculate stats for each user
			for i := range users {
				var fileCount int
				var totalSize int64
				prefix := "/" + users[i].Username
				// Count files and folders inside their directory
				database.DB.Get(&fileCount, "SELECT COUNT(*) FROM files WHERE (path = ? OR path LIKE ?) AND is_folder = 0", prefix, prefix+"/%")
				database.DB.Get(&totalSize, "SELECT COALESCE(SUM(size), 0) FROM files WHERE (path = ? OR path LIKE ?) AND is_folder = 0", prefix, prefix+"/%")
				users[i].FileCount = fileCount
				users[i].TotalSize = totalSize
			}

			c.JSON(http.StatusOK, gin.H{"users": users})
		})

		api.POST("/users", func(c *gin.Context) {
			if !c.GetBool("is_admin") {
				c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
				return
			}
			username := c.PostForm("username")
			password := c.PostForm("password")
			if username == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "username required"})
				return
			}
			if password == "" {
				password = "abc123"
			}

			// Validate username (no spaces, no special chars, only a-z, 0-9, ., _, -)
			validUsername := regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
			if !validUsername.MatchString(username) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_username_format", "message": "Username can only contain alphanumeric characters, dots, underscores and hyphens"})
				return
			}

			adminUsername := database.GetSetting("admin_username")
			if strings.EqualFold(username, adminUsername) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "username cannot be the same as admin username"})
				return
			}

			hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to hash password"})
				return
			}

			tx, err := database.DB.Beginx()
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start transaction"})
				return
			}
			defer tx.Rollback()

			// Check if folder exists in root (case-insensitive)
			var folderCount int
			folderQuery := "SELECT COUNT(*) FROM files WHERE path = '/' AND filename = ? COLLATE NOCASE AND is_folder = 1"
			if database.IsMySQL() {
				folderQuery = "SELECT COUNT(*) FROM files WHERE path = '/' AND LOWER(filename) = LOWER(?) AND is_folder = 1"
			}
			err = tx.Get(&folderCount, folderQuery, username)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
				return
			}
			if folderCount > 0 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "folder_exists", "message": "A folder with this name already exists in root directory"})
				return
			}

			// Check if username already exists in child_accounts (case-insensitive)
			var userExists int
			userQuery := "SELECT COUNT(*) FROM child_accounts WHERE username = ? COLLATE NOCASE"
			if database.IsMySQL() {
				userQuery = "SELECT COUNT(*) FROM child_accounts WHERE LOWER(username) = LOWER(?)"
			}
			err = tx.Get(&userExists, userQuery, username)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
				return
			}
			if userExists > 0 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "username_exists", "message": "Username already exists"})
				return
			}

			_, err = tx.Exec("INSERT INTO child_accounts (username, password_hash, force_password_change) VALUES (?, ?, 1)", username, string(hashedPassword))
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create user record"})
				return
			}

			// Create the user folder
			_, err = tx.Exec("INSERT INTO files (filename, path, is_folder, owner) VALUES (?, '/', 1, ?)", username, username)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create folder"})
				return
			}

			if err := tx.Commit(); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit transaction"})
				return
			}

			c.JSON(http.StatusOK, gin.H{"status": "success"})
		})

		api.DELETE("/users/:username", func(c *gin.Context) {
			if !c.GetBool("is_admin") {
				c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
				return
			}
			username := c.Param("username")

			// Start transaction to ensure atomicity
			tx, err := database.DB.Beginx()
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			defer tx.Rollback()

			// Rename folder to avoid collisions and mark as deleted for Admin inheritance
			timestamp := time.Now().Format("20060102_150405")
			newFolderName := fmt.Sprintf("deleted_%s_%s", username, timestamp)

			adminUsername := c.GetString("username")

			// 1. Update the root folder record of the user and change owner to Admin
			_, err = tx.Exec("UPDATE files SET filename = ?, owner = ? WHERE path = '/' AND filename = ? AND is_folder = 1 AND owner = ?", newFolderName, adminUsername, username, username)
			if err != nil {
				tx.Rollback()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to rename user folder"})
				return
			}

			// 2. Update paths and owners of all files and subfolders within that folder
			oldPrefix := "/" + username
			newPrefix := "/" + newFolderName

			// Update files/folders directly inside the user folder
			_, err = tx.Exec("UPDATE files SET path = ?, owner = ? WHERE path = ? AND owner = ?", newPrefix, adminUsername, oldPrefix, username)
			if err != nil {
				tx.Rollback()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update direct file paths"})
				return
			}

			// Update files/folders in subfolders (recursive)
			_, err = tx.Exec("UPDATE files SET path = "+database.ConcatPathSQL()+", owner = ? WHERE path LIKE ? AND owner = ?", newPrefix, len(oldPrefix)+1, adminUsername, oldPrefix+"/%", username)
			if err != nil {
				tx.Rollback()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update nested file paths"})
				return
			}

			// 3. Delete child account record
			_, err = tx.Exec("DELETE FROM child_accounts WHERE username = ?", username)
			if err != nil {
				tx.Rollback()
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}

			// 4. Revoke all sessions for this user
			tx.Exec("DELETE FROM sessions WHERE username = ?", username)

			// 5. Delete all passkeys for this user
			tx.Exec("DELETE FROM passkeys WHERE username = ?", username)

			if err := tx.Commit(); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit transaction"})
				return
			}

			c.JSON(http.StatusOK, gin.H{"status": "success"})
		})

		api.POST("/users/:username/reset-pass", func(c *gin.Context) {
			if !c.GetBool("is_admin") {
				c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
				return
			}
			username := c.Param("username")

			hashedPassword, err := bcrypt.GenerateFromPassword([]byte("abc123"), bcrypt.DefaultCost)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to hash password"})
				return
			}

			_, err = database.DB.Exec("UPDATE child_accounts SET password_hash = ?, force_password_change = 1 WHERE username = ?", string(hashedPassword), username)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reset password"})
				return
			}

			// Revoke all sessions for this user
			database.DB.Exec("DELETE FROM sessions WHERE username = ?", username)

			c.JSON(http.StatusOK, gin.H{"status": "success"})
		})

		api.GET("/progress/:task_id", func(c *gin.Context) {
			taskID := c.Param("task_id")
			c.JSON(http.StatusOK, tgclient.GetTask(taskID))
		})

		api.GET("/upload/check/:task_id", func(c *gin.Context) {
			taskID := c.Param("task_id")
			username := c.GetString("username")

			// Check if task exists and belongs to user
			var task struct {
				ID string `db:"id"`
			}
			err := database.DB.Get(&task, "SELECT id FROM upload_tasks WHERE id = ? AND owner = ?", taskID, username)
			if err != nil {
				c.JSON(http.StatusOK, gin.H{"chunks": []int{}})
				return
			}

			var chunks []int
			database.DB.Select(&chunks, "SELECT chunk_index FROM upload_chunks WHERE task_id = ? ORDER BY chunk_index ASC", taskID)
			c.JSON(http.StatusOK, gin.H{"chunks": chunks})
		})

		api.POST("/upload/check-exists", func(c *gin.Context) {
			path := c.PostForm("path")
			filenamesStr := c.PostForm("filenames")
			if filenamesStr == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "filenames_required"})
				return
			}
			filenames := strings.Split(filenamesStr, "|")
			username := c.GetString("username")
			isAdmin := c.GetBool("is_admin")
			dbPath := mapPath(path, username, isAdmin)

			existing := make([]string, 0)
			for _, fn := range filenames {
				var count int
				err := database.DB.Get(&count, "SELECT COUNT(*) FROM files WHERE path = ? AND filename = ? AND is_folder = 0 AND owner = ?", dbPath, fn, username)
				if err == nil && count > 0 {
					existing = append(existing, fn)
				}
			}
			c.JSON(http.StatusOK, gin.H{"existing": existing})
		})

		api.GET("/ws", func(c *gin.Context) {
			username := c.GetString("username")
			ws.HandleWebSocket(c.Writer, c.Request, username)
		})

		api.GET("/files", func(c *gin.Context) {
			path := c.Query("path")
			username := c.GetString("username")
			isAdmin := c.GetBool("is_admin")
			dbPath := mapPath(path, username, isAdmin)

			if isAdmin && isChildAccountPath(dbPath) {
				c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
				return
			}

			var files []database.File
			query := "SELECT * FROM files WHERE path = ? AND owner = ? ORDER BY is_folder DESC, id DESC"
			args := []interface{}{dbPath, username}
			if isAdmin {
				if dbPath == "/" {
					query = "SELECT * FROM files WHERE path = ? ORDER BY is_folder DESC, id DESC"
					args = []interface{}{dbPath}
				} else {
					// Detect owner from path to avoid seeing duplicates from other owners in this path
					parts := strings.Split(strings.TrimPrefix(dbPath, "/"), "/")
					rootFolder := parts[0]
					var isChild int
					database.DB.Get(&isChild, "SELECT COUNT(*) FROM child_accounts WHERE username = ?", rootFolder)

					effectiveOwner := username // Default to admin themselves
					if isChild > 0 {
						effectiveOwner = rootFolder
					}
					args = []interface{}{dbPath, effectiveOwner}
				}
			}
			err := database.DB.Select(&files, query, args...)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}

			if isAdmin && dbPath == "/" {
				var activeUsers []string
				database.DB.Select(&activeUsers, "SELECT username FROM child_accounts")
				userMap := make(map[string]bool)
				for _, u := range activeUsers {
					userMap[u] = true
				}
				var filtered []database.File
				for _, f := range files {
					// Admin isolation: hide folders that match active child account usernames
					if f.IsFolder && userMap[f.Filename] {
						continue
					}
					filtered = append(filtered, f)
				}
				files = filtered
			}

			for i := range files {
				files[i].Path = unmapPath(files[i].Path, username, isAdmin)
				if files[i].ShareToken != nil {
					files[i].DirectToken = utils.GenerateDirectToken(*files[i].ShareToken)
				}
				if files[i].ThumbPath != nil {
					if _, err := os.Stat(*files[i].ThumbPath); err == nil {
						files[i].HasThumb = true
					}
				}
			}
			var storageUsed int64
			if isAdmin {
				database.DB.Get(&storageUsed, "SELECT COALESCE(SUM(size), 0) FROM files WHERE is_folder = 0")
			} else {
				prefix := "/" + username
				database.DB.Get(&storageUsed, "SELECT COALESCE(SUM(size), 0) FROM files WHERE (path = ? OR path LIKE ?) AND is_folder = 0", prefix, prefix+"/%")
			}

			c.JSON(http.StatusOK, gin.H{
				"files":        files,
				"storage_used": storageUsed,
			})
		})

		api.POST("/folders", func(c *gin.Context) {
			name := c.PostForm("name")
			path := c.PostForm("path")
			username := c.GetString("username")
			isAdmin := c.GetBool("is_admin")
			dbPath := mapPath(path, username, isAdmin)

			if isAdmin && isChildAccountPath(dbPath) {
				c.JSON(http.StatusForbidden, gin.H{"error": "admin_forbidden_child_path"})
				return
			}

			if isAdmin && dbPath == "/" {
				var count int
				database.DB.Get(&count, "SELECT COUNT(*) FROM child_accounts WHERE username = ?", name)
				if count > 0 {
					c.JSON(http.StatusForbidden, gin.H{"error": "folder_collides_child"})
					return
				}
			}

			uniqueName := database.GetUniqueFilename(database.DB, dbPath, name, true, 0, username)
			_, err := database.DB.Exec("INSERT INTO files (filename, path, is_folder, owner) VALUES (?, ?, 1, ?)", uniqueName, dbPath, username)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"status": "success"})
		})

		api.POST("/upload", func(c *gin.Context) {
			file, header, err := c.Request.FormFile("file")
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "No file"})
				return
			}
			defer file.Close()

			filename := filepath.Base(c.PostForm("filename"))
			path := c.PostForm("path")
			username := c.GetString("username")
			isAdmin := c.GetBool("is_admin")
			dbPath := mapPath(path, username, isAdmin)

			if isAdmin && isChildAccountPath(dbPath) {
				c.JSON(http.StatusForbidden, gin.H{"error": "admin_forbidden_child_path"})
				return
			}

			if isAdmin && dbPath == "/" {
				var count int
				database.DB.Get(&count, "SELECT COUNT(*) FROM child_accounts WHERE username = ?", filename)
				if count > 0 {
					c.JSON(http.StatusForbidden, gin.H{"error": "filename_collides_child"})
					return
				}
			}

			taskID := c.PostForm("task_id")

			// [SECURITY] Basic validation for taskID to prevent path traversal characters
			if taskID == "" || strings.Contains(taskID, "..") || strings.ContainsAny(taskID, "/\\") {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_task_id"})
				return
			}

			chunkIndex, err := strconv.Atoi(c.PostForm("chunk_index"))
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid chunk_index"})
				return
			}
			totalChunks, err := strconv.Atoi(c.PostForm("total_chunks"))
			if err != nil || totalChunks <= 0 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid total_chunks"})
				return
			}

			if chunkIndex < 0 || chunkIndex >= totalChunks {
				c.JSON(http.StatusBadRequest, gin.H{"error": "chunk_index out of range"})
				return
			}

			tempDir := cfg.TempDir
			os.MkdirAll(tempDir, os.ModePerm)
			// [SECURITY] Use filepath.Base to strip any path traversal from filename
			safeFilename := filepath.Base(filename)
			tempFilePath := filepath.Join(tempDir, taskID+"_"+safeFilename)

			// Boundary check: ensure resolved path is still inside TempDir
			rel, err := filepath.Rel(tempDir, tempFilePath)
			if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_path"})
				return
			}

			// Constant chunk size from frontend is 10MB
			const chunkSize = 10 * 1024 * 1024
			offset := int64(chunkIndex) * int64(chunkSize)

			// Track received chunks; IO happens outside the lock
			val, _ := chunkTrackerSync.LoadOrStore(taskID, &chunkState{
				received: make(map[int]bool),
			})
			state := val.(*chunkState)

			// Read chunk bytes into memory first (outside any lock)
			chunkData, err := io.ReadAll(file)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_read_chunk"})
				return
			}

			// Store metadata in DB if it's the first chunk
			// Read overwrite flag (only used when inserting new task)
			overwriteFlag := c.PostForm("overwrite") == "true"
			database.DB.Exec(database.InsertIgnoreSQL("upload_tasks", "id, filename, owner, overwrite", "?, ?, ?, ?"), taskID, safeFilename, username, overwriteFlag)

			// WriteAt is safe for concurrent goroutines writing non-overlapping offsets
			out, err := os.OpenFile(tempFilePath, os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_open_temp_file"})
				return
			}
			_, err = out.WriteAt(chunkData, offset)
			out.Close()
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_write_chunk"})
				return
			}

			// Record chunk in DB
			_, err = database.DB.Exec(database.InsertIgnoreSQL("upload_chunks", "task_id, chunk_index", "?, ?"), taskID, chunkIndex)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_record_chunk"})
				return
			}

			// Update in-memory tracker (for active progress)
			state.Lock()
			// If memory tracker is empty but DB has chunks (after restart), we should sync them
			// but for simplicity, we just trust the memory tracker for the current session
			// and use DB for "Resume" check API.
			state.received[chunkIndex] = true

			// Count total chunks from DB to be accurate
			var actualReceived int
			database.DB.Get(&actualReceived, "SELECT COUNT(*) FROM upload_chunks WHERE task_id = ?", taskID)

			isDone := actualReceived == totalChunks
			if isDone {
				chunkTrackerSync.Delete(taskID)
				database.DB.Exec("DELETE FROM upload_chunks WHERE task_id = ?", taskID)
				database.DB.Exec("DELETE FROM upload_tasks WHERE id = ?", taskID)
			}
			state.Unlock()

			if isDone {
				tgclient.UpdateTask(taskID, "uploading_to_server", 100, "", username)

				mimeType := header.Header.Get("Content-Type")
				if mimeType == "" {
					mimeType = "application/octet-stream"
				}

				go func() {
					defer os.Remove(tempFilePath)
					// Retrieve overwrite flag for this task
					var ov bool
					database.DB.Get(&ov, "SELECT overwrite FROM upload_tasks WHERE id = ?", taskID)
					tgclient.ProcessCompleteUpload(context.Background(), tempFilePath, filename, dbPath, mimeType, taskID, cfg, ov, username)
				}()

				c.JSON(http.StatusOK, gin.H{"status": "processing_telegram", "message": "pushing_to_tg"})
				return
			}

			serverPercent := int((float64(actualReceived) / float64(totalChunks)) * 100)
			tgclient.UpdateTask(taskID, "uploading_to_server", serverPercent, "", username)

			c.JSON(http.StatusOK, gin.H{"status": "chunk_received", "chunk": chunkIndex})
		})

		api.POST("/remote-upload", func(c *gin.Context) {
			remoteURL := c.PostForm("url")
			uPath := c.PostForm("path")
			overwrite := c.PostForm("overwrite") == "true"
			username := c.GetString("username")
			isAdmin := c.GetBool("is_admin")

			if remoteURL == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "url_required"})
				return
			}

			// Validate URL format
			u, err := url.ParseRequestURI(remoteURL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_url"})
				return
			}

			dbPath := mapPath(uPath, username, isAdmin)
			if isAdmin && isChildAccountPath(dbPath) {
				c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
				return
			}

			// Check if destination path exists and is a folder
			if dbPath != "/" {
				var folder database.File
				err = database.DB.Get(&folder, "SELECT is_folder FROM files WHERE path = ? AND filename = ? AND is_folder = 1 AND owner = ?", filepath.Dir(dbPath), filepath.Base(dbPath), username)
				if err != nil {
					c.JSON(http.StatusNotFound, gin.H{"error": "folder_not_found"})
					return
				}
			}

			taskID := c.PostForm("task_id")
			if taskID == "" {
				taskID = uuid.New().String()
			}
			go tgclient.ProcessRemoteUpload(context.Background(), remoteURL, dbPath, taskID, cfg, overwrite, username)

			c.JSON(http.StatusOK, gin.H{
				"status":  "processing",
				"task_id": taskID,
			})
		})

		api.GET("/tasks", func(c *gin.Context) {
			username := c.GetString("username")
			c.JSON(http.StatusOK, gin.H{
				"tasks": tgclient.GetActiveTasks(username),
			})
		})

		api.POST("/cancel_upload", func(c *gin.Context) {
			taskID := c.PostForm("task_id")
			filename := c.PostForm("filename")
			username := c.GetString("username")

			// 1. Cancel the telegram upload (only if owner matches)
			if taskID != "" {
				if !tgclient.CancelTask(taskID, username) {
					c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
					return
				}
			}

			// 2. Delete the temporary file if it's partially uploaded
			if taskID != "" && filename != "" {
				// [SECURITY FIX CRIT-01] Validate taskID is a UUID and sanitize filename
				// to prevent path traversal attacks (e.g. filename="../../database.db")
				// [SECURITY FIX] Basic validation for taskID
				if taskID == "" || strings.Contains(taskID, "..") || strings.ContainsAny(taskID, "/\\") {
					c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_task_id"})
					return
				}
				safeFilename := filepath.Base(filename) // Strip any path components
				tempFilePath := filepath.Join(cfg.TempDir, taskID+"_"+safeFilename)
				// Boundary check: ensure resolved path is still inside TempDir
				rel, err := filepath.Rel(cfg.TempDir, tempFilePath)
				if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
					c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_path"})
					return
				}
				os.Remove(tempFilePath)
				chunkTrackerSync.Delete(taskID)
				database.DB.Exec("DELETE FROM upload_chunks WHERE task_id = ?", taskID)
				database.DB.Exec("DELETE FROM upload_tasks WHERE id = ?", taskID)
			}

			c.JSON(http.StatusOK, gin.H{"status": "cancelled"})
		})

		api.POST("/actions/paste", func(c *gin.Context) {
			var req PasteRequest
			if err := c.ShouldBindJSON(&req); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}

			username := c.GetString("username")
			isAdmin := c.GetBool("is_admin")
			req.Destination = mapPath(req.Destination, username, isAdmin)

			if isAdmin && isChildAccountPath(req.Destination) {
				c.JSON(http.StatusForbidden, gin.H{"error": "Admin cannot paste to child account directory"})
				return
			}

			tx, err := database.DB.Beginx()
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start transaction"})
				return
			}
			defer tx.Rollback()

			for _, id := range req.ItemIDs {
				var item database.File
				err := tx.Get(&item, "SELECT * FROM files WHERE id = ?", id)
				if err != nil {
					continue
				}

				if !verifyItemAccess(c, item.Path) {
					continue
				}

				if isAdmin && isChildAccountPathQuery(tx, item.Path) {
					continue
				}

				if item.IsFolder {
					oldPrefix := item.Path + "/" + item.Filename
					if item.Path == "/" {
						oldPrefix = "/" + item.Filename
					}
					// Prevent moving a folder into itself or its own subfolder
					if req.Action == "move" && (req.Destination == oldPrefix || strings.HasPrefix(req.Destination, oldPrefix+"/")) {
						continue
					}
				}

				// If moving to the same destination, it's a no-op
				if req.Action == "move" && req.Destination == item.Path {
					continue
				}

				// Use item.ID as excludeID for move to allow no-op (same name in same/diff folder)
				// Actually for copy, excludeID is 0.
				var excludeID int
				if req.Action == "move" {
					excludeID = item.ID
				}
				username := c.GetString("username")
				uniqueName := database.GetUniqueFilename(tx, req.Destination, item.Filename, item.IsFolder, excludeID, username)

				switch req.Action {
				case "move":
					if item.IsFolder {
						oldPrefix := item.Path + "/" + item.Filename
						if item.Path == "/" {
							oldPrefix = "/" + item.Filename
						}
						newPrefix := req.Destination + "/" + uniqueName
						if req.Destination == "/" {
							newPrefix = "/" + uniqueName
						}

						_, err = tx.Exec("UPDATE files SET path = ?, filename = ? WHERE id = ?", req.Destination, uniqueName, id)
						if err != nil {
							c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
							return
						}
						_, err = tx.Exec("UPDATE files SET path = "+database.ConcatPathSQL()+" WHERE path = ? OR path LIKE ?", newPrefix, len(oldPrefix)+1, oldPrefix, oldPrefix+"/%")
						if err != nil {
							c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
							return
						}
					} else {
						_, err = tx.Exec("UPDATE files SET path = ?, filename = ? WHERE id = ?", req.Destination, uniqueName, id)
						if err != nil {
							c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
							return
						}
					}
				case "copy":
					if item.IsFolder {
						_, err = tx.Exec("INSERT INTO files (filename, path, is_folder, owner) VALUES (?, ?, 1, ?)", uniqueName, req.Destination, username)
						if err != nil {
							c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
							return
						}

						oldPrefix := item.Path + "/" + item.Filename
						if item.Path == "/" {
							oldPrefix = "/" + item.Filename
						}
						newPrefix := req.Destination + "/" + uniqueName
						if req.Destination == "/" {
							newPrefix = "/" + uniqueName
						}

						var children []database.File
						err = tx.Select(&children, "SELECT * FROM files WHERE (path = ? OR path LIKE ?) AND owner = ?", oldPrefix, oldPrefix+"/%", item.Owner)
						if err != nil {
							c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
							return
						}

						for _, child := range children {
							newChildPath := newPrefix + child.Path[len(oldPrefix):]
							res, err := tx.Exec("INSERT INTO files (message_id, filename, path, size, mime_type, is_folder, thumb_path, owner) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
								child.MessageID, child.Filename, newChildPath, child.Size, child.MimeType, child.IsFolder, child.ThumbPath, username)
							if err != nil {
								c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
								return
							}

							if !child.IsFolder {
								newChildID, err := res.LastInsertId()
								if err == nil {
									_, err = tx.Exec("INSERT INTO file_parts (file_id, part_index, message_id, size) SELECT ?, part_index, message_id, size FROM file_parts WHERE file_id = ?", newChildID, child.ID)
									if err != nil {
										c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
										return
									}
								}
							}
						}
					} else {
						// Only copy files that have a valid Telegram message reference
						if item.MessageID == nil {
							continue
						}
						res, err := tx.Exec("INSERT INTO files (message_id, filename, path, size, mime_type, is_folder, thumb_path, owner) VALUES (?, ?, ?, ?, ?, 0, ?, ?)", item.MessageID, uniqueName, req.Destination, item.Size, item.MimeType, item.ThumbPath, username)
						if err != nil {
							c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
							return
						}
						newFileID, err := res.LastInsertId()
						if err == nil {
							_, err = tx.Exec("INSERT INTO file_parts (file_id, part_index, message_id, size) SELECT ?, part_index, message_id, size FROM file_parts WHERE file_id = ?", newFileID, item.ID)
							if err != nil {
								c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
								return
							}
						}
					}
				}
			}

			if err := tx.Commit(); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit transaction"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"status": "success"})
		})

		api.DELETE("/files/:id", func(c *gin.Context) {
			id, _ := strconv.Atoi(c.Param("id"))

			var item database.File
			if err := database.DB.Get(&item, "SELECT * FROM files WHERE id = ?", id); err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}

			isAdmin := c.GetBool("is_admin")
			if !verifyItemAccess(c, item.Path) || (isAdmin && isChildAccountPath(item.Path)) {
				c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
				return
			}

			if item.IsFolder {
				oldPrefix := item.Path + "/" + item.Filename
				if item.Path == "/" {
					oldPrefix = "/" + item.Filename
				}
				var children []database.File
				database.DB.Select(&children, "SELECT * FROM files WHERE (path = ? OR path LIKE ?) AND owner = ?", oldPrefix, oldPrefix+"/%", item.Owner)

				var msgIDsToDelete []int
				for _, child := range children {
					// Collect parts
					var partMsgIDs []int
					database.DB.Select(&partMsgIDs, "SELECT message_id FROM file_parts WHERE file_id = ?", child.ID)
					for _, pm := range partMsgIDs {
						var count int
						database.DB.Get(&count, "SELECT COUNT(*) FROM file_parts WHERE message_id = ?", pm)
						if count <= 1 {
							msgIDsToDelete = append(msgIDsToDelete, pm)
						}
					}

					if child.MessageID != nil {
						var count int
						database.DB.Get(&count, "SELECT COUNT(*) FROM files WHERE message_id = ?", *child.MessageID)
						if count <= 1 {
							// Check if not already added from parts
							found := false
							for _, m := range msgIDsToDelete {
								if m == *child.MessageID {
									found = true
									break
								}
							}
							if !found {
								msgIDsToDelete = append(msgIDsToDelete, *child.MessageID)
							}
						}
					}
					if child.ThumbPath != nil {
						var count int
						database.DB.Get(&count, "SELECT COUNT(*) FROM files WHERE thumb_path = ?", *child.ThumbPath)
						if count <= 1 {
							os.Remove(*child.ThumbPath)
						}
					}
				}

				// Delete DB rows first (source of truth), then Telegram messages
				database.DB.Exec("DELETE FROM files WHERE (path = ? OR path LIKE ?) AND owner = ?", oldPrefix, oldPrefix+"/%", item.Owner)
				database.DB.Exec("DELETE FROM files WHERE id = ?", id)
				if len(msgIDsToDelete) > 0 {
					tgclient.DeleteMessages(context.Background(), cfg, msgIDsToDelete)
				}
			} else {
				var msgIDsToDelete []int

				// Collect parts
				var partMsgIDs []int
				database.DB.Select(&partMsgIDs, "SELECT message_id FROM file_parts WHERE file_id = ?", item.ID)
				for _, pm := range partMsgIDs {
					var count int
					database.DB.Get(&count, "SELECT COUNT(*) FROM file_parts WHERE message_id = ?", pm)
					if count <= 1 {
						msgIDsToDelete = append(msgIDsToDelete, pm)
					}
				}

				if item.MessageID != nil {
					var count int
					database.DB.Get(&count, "SELECT COUNT(*) FROM files WHERE message_id = ?", *item.MessageID)
					if count <= 1 {
						// Check if not already added from parts
						found := false
						for _, m := range msgIDsToDelete {
							if m == *item.MessageID {
								found = true
								break
							}
						}
						if !found {
							msgIDsToDelete = append(msgIDsToDelete, *item.MessageID)
						}
					}
				}
				if item.ThumbPath != nil {
					var count int
					database.DB.Get(&count, "SELECT COUNT(*) FROM files WHERE thumb_path = ?", *item.ThumbPath)
					if count <= 1 {
						os.Remove(*item.ThumbPath)
					}
				}
				// Delete DB row first, then Telegram message
				database.DB.Exec("DELETE FROM files WHERE id = ?", id)
				if len(msgIDsToDelete) > 0 {
					tgclient.DeleteMessages(context.Background(), cfg, msgIDsToDelete)
				}
			}

			c.JSON(http.StatusOK, gin.H{"status": "deleted"})
		})

		api.PUT("/files/:id/rename", func(c *gin.Context) {
			id, _ := strconv.Atoi(c.Param("id"))
			newName := c.PostForm("new_name")

			tx, err := database.DB.Beginx()
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start transaction"})
				return
			}
			defer tx.Rollback()

			var item database.File
			err = tx.Get(&item, "SELECT filename, path, is_folder FROM files WHERE id = ?", id)
			if err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}

			isAdmin := c.GetBool("is_admin")
			if !verifyItemAccess(c, item.Path) || (isAdmin && isChildAccountPathQuery(tx, item.Path)) {
				c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
				return
			}

			if !item.IsFolder {
				oldExt := filepath.Ext(item.Filename)
				newExt := filepath.Ext(newName)
				if oldExt != "" && newExt == "" {
					newName += oldExt
				}
			}

			username := c.GetString("username")
			uniqueName := database.GetUniqueFilename(tx, item.Path, newName, item.IsFolder, id, username)

			if item.IsFolder {
				basePath := item.Path
				oldPrefix := basePath + "/" + item.Filename
				if basePath == "/" {
					oldPrefix = "/" + item.Filename
				}
				newPrefix := basePath + "/" + uniqueName
				if basePath == "/" {
					newPrefix = "/" + uniqueName
				}
				_, err = tx.Exec("UPDATE files SET path = "+database.ConcatPathSQL()+" WHERE path = ? OR path LIKE ?", newPrefix, len(oldPrefix)+1, oldPrefix, oldPrefix+"/%")
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					return
				}
			}

			_, err = tx.Exec("UPDATE files SET filename = ? WHERE id = ?", uniqueName, id)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}

			// Update MIME type based on new extension
			if !item.IsFolder {
				newMime := mime.TypeByExtension(filepath.Ext(uniqueName))
				if newMime != "" {
					tx.Exec("UPDATE files SET mime_type = ? WHERE id = ?", newMime, id)
				}
			}

			if err := tx.Commit(); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit transaction"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"status": "renamed", "new_name": uniqueName})
		})

		api.POST("/files/:id/share", func(c *gin.Context) {
			id, _ := strconv.Atoi(c.Param("id"))
			var item database.File
			if err := database.DB.Get(&item, "SELECT path FROM files WHERE id = ?", id); err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
			isAdmin := c.GetBool("is_admin")
			if !verifyItemAccess(c, item.Path) || (isAdmin && isChildAccountPath(item.Path)) {
				c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
				return
			}
			token := uuid.New().String()
			database.DB.Exec("UPDATE files SET share_token = ? WHERE id = ?", token, id)
			c.JSON(http.StatusOK, gin.H{"share_token": token, "direct_token": utils.GenerateDirectToken(token)})
		})

		api.DELETE("/files/:id/share", func(c *gin.Context) {
			id, _ := strconv.Atoi(c.Param("id"))
			var item database.File
			if err := database.DB.Get(&item, "SELECT path FROM files WHERE id = ?", id); err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
			isAdminDel := c.GetBool("is_admin")
			if !verifyItemAccess(c, item.Path) || (isAdminDel && isChildAccountPath(item.Path)) {
				c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
				return
			}
			database.DB.Exec("UPDATE files SET share_token = NULL WHERE id = ?", id)
			c.JSON(http.StatusOK, gin.H{"status": "revoked"})
		})

		api.GET("/files/:id/thumb", func(c *gin.Context) {
			id, _ := strconv.Atoi(c.Param("id"))
			var item database.File
			if err := database.DB.Get(&item, "SELECT path, thumb_path FROM files WHERE id = ?", id); err != nil || item.ThumbPath == nil {
				c.AbortWithStatus(http.StatusNotFound)
				return
			}
			isAdmin := c.GetBool("is_admin")
			if !verifyItemAccess(c, item.Path) || (isAdmin && isChildAccountPath(item.Path)) {
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
			c.File(*item.ThumbPath)
		})

		api.GET("/files/:id/stream", func(c *gin.Context) {
			id, _ := strconv.Atoi(c.Param("id"))
			var item database.File
			if err := database.DB.Get(&item, "SELECT * FROM files WHERE id = ?", id); err != nil || item.MessageID == nil {
				c.AbortWithStatus(http.StatusNotFound)
				return
			}
			isAdminStream := c.GetBool("is_admin")
			if !verifyItemAccess(c, item.Path) || (isAdminStream && isChildAccountPath(item.Path)) {
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
			if item.MimeType != nil {
				mime := *item.MimeType
				if strings.HasSuffix(strings.ToLower(item.Filename), ".mkv") {
					mime = "video/webm"
				}
				c.Header("Content-Type", mime)
			}
			tgclient.ServeTelegramFile(c.Request, c.Writer, item, cfg)
		})

		api.GET("/ytdlp/status", func(c *gin.Context) {
			ytdlpEnabled := cfg.YTDLPPath != "disabled" && cfg.YTDLPPath != "disable"
			ffmpegEnabled := cfg.FFMPEGPath != "disabled" && cfg.FFMPEGPath != "disable"
			enabled := ytdlpEnabled && ffmpegEnabled
			c.JSON(http.StatusOK, gin.H{"enabled": enabled})
		})

		api.GET("/ytdlp/cookies/status", func(c *gin.Context) {
			username := c.GetString("username")
			cookieFile := filepath.Join(cfg.CookiesDir, fmt.Sprintf("user_%s.txt", username))
			_, err := os.Stat(cookieFile)
			c.JSON(http.StatusOK, gin.H{"has_cookie": err == nil})
		})

		api.POST("/ytdlp/cookies", func(c *gin.Context) {
			file, err := c.FormFile("cookie_file")
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "file_required"})
				return
			}

			// Validation: max size 2MB
			if file.Size > 2*1024*1024 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "file_too_large"})
				return
			}

			src, err := file.Open()
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "open_failed"})
				return
			}
			defer src.Close()

			// Read first 100 bytes to check header
			head := make([]byte, 100)
			n, _ := src.Read(head)
			headStr := string(head[:n])

			isNetscape := strings.Contains(headStr, "# Netscape HTTP Cookie File") || strings.Contains(headStr, "# HTTP Cookie File")
			isJSON := strings.HasPrefix(strings.TrimSpace(headStr), "[")

			if !isNetscape && !isJSON {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_cookie_format"})
				return
			}

			username := c.GetString("username")
			os.MkdirAll(cfg.CookiesDir, 0755)
			cookieFile := filepath.Join(cfg.CookiesDir, fmt.Sprintf("user_%s.txt", username))

			if err := c.SaveUploadedFile(file, cookieFile); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "save_failed"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"status": "success"})
		})

		api.DELETE("/ytdlp/cookies", func(c *gin.Context) {
			username := c.GetString("username")
			cookieFile := filepath.Join(cfg.CookiesDir, fmt.Sprintf("user_%s.txt", username))
			os.Remove(cookieFile)
			c.JSON(http.StatusOK, gin.H{"status": "success"})
		})

		api.GET("/proxy/image", func(c *gin.Context) {
			targetURL := c.Query("url")
			if targetURL == "" {
				c.AbortWithStatus(http.StatusBadRequest)
				return
			}
			if !tgclient.IsValidURL(targetURL) {
				c.AbortWithStatus(http.StatusBadRequest)
				return
			}

			client := &http.Client{
				Timeout: 15 * time.Second,
			}

			req, err := http.NewRequestWithContext(c.Request.Context(), "GET", targetURL, nil)
			if err != nil {
				c.AbortWithStatus(http.StatusInternalServerError)
				return
			}

			// Add headers to look more like a real browser request
			req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/119.0.0.0 Safari/537.36")
			req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
			req.Header.Set("Accept-Language", "en-US,en;q=0.9")
			req.Header.Set("Cache-Control", "no-cache")
			req.Header.Set("Pragma", "no-cache")
			req.Header.Set("Sec-Fetch-Dest", "image")
			req.Header.Set("Sec-Fetch-Mode", "no-cors")
			req.Header.Set("Sec-Fetch-Site", "cross-site")

			// Try to derive a sensible referer from the target URL
			if u, err := url.Parse(targetURL); err == nil {
				req.Header.Set("Referer", u.Scheme+"://"+u.Host+"/")
			}

			resp, err := client.Do(req)
			if err != nil {
				c.AbortWithStatus(http.StatusBadGateway)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				c.AbortWithStatus(resp.StatusCode)
				return
			}

			contentType := resp.Header.Get("Content-Type")
			// Relaxed check: Allow if it looks like an image or is a generic stream (often used as fallback)
			if contentType != "" && !strings.HasPrefix(contentType, "image/") && contentType != "application/octet-stream" {
				// We still proxy it if it's not explicitly an non-image type like html/json
				if strings.Contains(contentType, "text/html") || strings.Contains(contentType, "application/json") {
					c.AbortWithStatus(http.StatusForbidden)
					return
				}
			}

			if contentType == "" {
				contentType = "image/jpeg" // Fallback
			}

			c.Header("Content-Type", contentType)
			c.Header("Cache-Control", "public, max-age=86400")
			c.Header("Cross-Origin-Resource-Policy", "cross-origin")
			io.Copy(c.Writer, resp.Body)
		})

		api.POST("/ytdlp/formats", func(c *gin.Context) {
			url := c.PostForm("url")
			if url == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "url_required"})
				return
			}
			if !tgclient.IsValidURL(url) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_url_format"})
				return
			}
			username := c.GetString("username")
			info, err := tgclient.GetYTDLPFormats(url, cfg, username)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, info)
		})

		api.POST("/ytdlp/download", func(c *gin.Context) {
			url := c.PostForm("url")
			formatID := c.PostForm("format_id")
			downloadType := c.PostForm("download_type")
			path := c.PostForm("path")

			if url == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "url_required"})
				return
			}
			if !tgclient.IsValidURL(url) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_url_format"})
				return
			}

			if path == "" {
				path = "/"
			}

			username := c.GetString("username")
			isAdmin := c.GetBool("is_admin")
			dbPath := mapPath(path, username, isAdmin)

			if isAdmin && isChildAccountPath(dbPath) {
				c.JSON(http.StatusForbidden, gin.H{"error": "admin_forbidden_child_path"})
				return
			}

			taskID := fmt.Sprintf("ytdlp_%d", time.Now().UnixNano())
			go tgclient.ProcessYTDLPUpload(context.Background(), url, formatID, dbPath, taskID, downloadType, cfg, username)

			c.JSON(http.StatusOK, gin.H{"status": "started", "task_id": taskID})
		})
	}

	r.GET("/download/:id", authMiddleware(), func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		var item database.File
		if err := database.DB.Get(&item, "SELECT * FROM files WHERE id = ?", id); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
			return
		}

		isAdmin := c.GetBool("is_admin")
		if !verifyItemAccess(c, item.Path) || (isAdmin && isChildAccountPath(item.Path)) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Forbidden"})
			return
		}

		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, item.Filename))
		if item.MimeType != nil {
			c.Header("Content-Type", *item.MimeType)
		}
		c.SetCookie("dl_started", "1", 15, "/", "", false, false)

		if err := tgclient.ServeTelegramFile(c.Request, c.Writer, item, cfg); err != nil {
			// Handle error
			fmt.Println("Stream error:", err)
		}
	})

	r.GET("/dl/:token", func(c *gin.Context) {
		directToken := c.Param("token")
		shareToken := utils.VerifyDirectToken(directToken)
		if shareToken == nil {
			c.JSON(http.StatusForbidden, gin.H{"error": "Invalid token"})
			return
		}

		var item database.File
		if err := database.DB.Get(&item, "SELECT * FROM files WHERE share_token = ?", *shareToken); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
			return
		}

		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, item.Filename))
		if item.MimeType != nil {
			c.Header("Content-Type", *item.MimeType)
		}

		if err := tgclient.ServeTelegramFile(c.Request, c.Writer, item, cfg); err != nil {
			fmt.Println("Stream error:", err)
		}
	})

	r.GET("/s/:token", func(c *gin.Context) {
		token := c.Param("token")
		var item database.File
		if err := database.DB.Get(&item, "SELECT filename, size, created_at, thumb_path, is_folder, path FROM files WHERE share_token = ?", token); err != nil {
			c.HTML(http.StatusNotFound, "error.html", gin.H{
				"error_message": "File not found or link has been revoked.",
				"version":       cfg.Version,
			})
			return
		}

		if item.IsFolder {
			c.HTML(http.StatusOK, "share_folder.html", gin.H{
				"filename":   item.Filename,
				"created_at": item.CreatedAt.Format("2006-01-02 15:04:05"),
				"token":      token,
				"version":    cfg.Version,
			})
			return
		}

		hasThumb := false
		if item.ThumbPath != nil {
			if _, err := os.Stat(*item.ThumbPath); err == nil {
				hasThumb = true
			}
		}

		c.HTML(http.StatusOK, "share.html", gin.H{
			"filename":       item.Filename,
			"size":           item.Size,
			"formatted_size": formatBytes(item.Size),
			"created_at":     item.CreatedAt.Format("2006-01-02 15:04:05"),
			"token":          token,
			"has_thumb":      hasThumb,
			"version":        cfg.Version,
		})

	})

	r.GET("/s/:token/api/files", func(c *gin.Context) {
		token := c.Param("token")
		var item database.File
		if err := database.DB.Get(&item, "SELECT filename, path, is_folder FROM files WHERE share_token = ?", token); err != nil || !item.IsFolder {
			c.JSON(http.StatusNotFound, gin.H{"error": "Folder not found"})
			return
		}

		reqPath := c.Query("path")
		if reqPath == "" {
			reqPath = "/"
		}

		basePrefix := item.Path + "/" + item.Filename
		if item.Path == "/" {
			basePrefix = "/" + item.Filename
		}

		targetPath := basePrefix
		if reqPath != "/" {
			if !strings.HasPrefix(reqPath, "/") {
				reqPath = "/" + reqPath
			}
			targetPath = basePrefix + reqPath
		}

		var files []database.File
		// For shared folders, we trust the share token to identify the base folder
		err := database.DB.Select(&files, "SELECT id, filename, path, size, created_at, is_folder, mime_type, thumb_path FROM files WHERE path = ? ORDER BY is_folder DESC, id DESC", targetPath)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		var totalSize int64
		database.DB.Get(&totalSize, "SELECT COALESCE(SUM(size), 0) FROM files WHERE (path = ? OR path LIKE ?) AND is_folder = 0", targetPath, targetPath+"/%")

		for i := range files {
			if files[i].ThumbPath != nil {
				if _, err := os.Stat(*files[i].ThumbPath); err == nil {
					files[i].HasThumb = true
				}
			}
		}
		c.JSON(http.StatusOK, gin.H{"files": files, "total_size": totalSize})
	})

	r.GET("/s/:token/stream", func(c *gin.Context) {
		token := c.Param("token")
		var item database.File
		if err := database.DB.Get(&item, "SELECT * FROM files WHERE share_token = ?", token); err != nil || item.MessageID == nil {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}

		if item.MimeType != nil {
			mime := *item.MimeType
			if strings.HasSuffix(strings.ToLower(item.Filename), ".mkv") {
				mime = "video/webm"
			}
			c.Header("Content-Type", mime)
		}

		tgclient.ServeTelegramFile(c.Request, c.Writer, item, cfg)
	})

	r.POST("/s/:token/dl", func(c *gin.Context) {
		token := c.Param("token")
		var item database.File
		if err := database.DB.Get(&item, "SELECT * FROM files WHERE share_token = ?", token); err != nil {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}

		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, item.Filename))
		if item.MimeType != nil {
			c.Header("Content-Type", *item.MimeType)
		}
		c.SetCookie("dl_started", "1", 15, "/", "", false, false)

		tgclient.ServeTelegramFile(c.Request, c.Writer, item, cfg)
	})

	r.GET("/s/:token/thumb", func(c *gin.Context) {
		token := c.Param("token")
		var item database.File
		if err := database.DB.Get(&item, "SELECT thumb_path FROM files WHERE share_token = ?", token); err != nil || item.ThumbPath == nil {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		c.File(*item.ThumbPath)
	})

	// APIs for files inside a shared folder
	r.GET("/s/:token/file/:id/stream", func(c *gin.Context) {
		token := c.Param("token")
		id, _ := strconv.Atoi(c.Param("id"))

		var folder database.File
		if err := database.DB.Get(&folder, "SELECT filename, path, is_folder FROM files WHERE share_token = ?", token); err != nil || !folder.IsFolder {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}

		basePrefix := folder.Path + "/" + folder.Filename
		if folder.Path == "/" {
			basePrefix = "/" + folder.Filename
		}

		var item database.File
		if err := database.DB.Get(&item, "SELECT * FROM files WHERE id = ?", id); err != nil {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}

		if item.Path != basePrefix && !strings.HasPrefix(item.Path, basePrefix+"/") {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}

		if item.MimeType != nil {
			mime := *item.MimeType
			if strings.HasSuffix(strings.ToLower(item.Filename), ".mkv") {
				mime = "video/webm"
			}
			c.Header("Content-Type", mime)
		}

		tgclient.ServeTelegramFile(c.Request, c.Writer, item, cfg)
	})

	r.GET("/s/:token/file/:id/dl", func(c *gin.Context) {
		token := c.Param("token")
		id, _ := strconv.Atoi(c.Param("id"))

		var folder database.File
		if err := database.DB.Get(&folder, "SELECT filename, path, is_folder FROM files WHERE share_token = ?", token); err != nil || !folder.IsFolder {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}

		basePrefix := folder.Path + "/" + folder.Filename
		if folder.Path == "/" {
			basePrefix = "/" + folder.Filename
		}

		var item database.File
		if err := database.DB.Get(&item, "SELECT * FROM files WHERE id = ?", id); err != nil {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}

		if item.Path != basePrefix && !strings.HasPrefix(item.Path, basePrefix+"/") {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}

		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, item.Filename))
		if item.MimeType != nil {
			c.Header("Content-Type", *item.MimeType)
		}
		c.SetCookie("dl_started", "1", 15, "/", "", false, false)

		tgclient.ServeTelegramFile(c.Request, c.Writer, item, cfg)
	})

	r.GET("/s/:token/file/:id/thumb", func(c *gin.Context) {
		token := c.Param("token")
		id, _ := strconv.Atoi(c.Param("id"))

		var folder database.File
		if err := database.DB.Get(&folder, "SELECT filename, path, is_folder FROM files WHERE share_token = ?", token); err != nil || !folder.IsFolder {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}

		basePrefix := folder.Path + "/" + folder.Filename
		if folder.Path == "/" {
			basePrefix = "/" + folder.Filename
		}

		var item database.File
		if err := database.DB.Get(&item, "SELECT thumb_path, path FROM files WHERE id = ?", id); err != nil || item.ThumbPath == nil {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}

		if item.Path != basePrefix && !strings.HasPrefix(item.Path, basePrefix+"/") {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}

		c.File(*item.ThumbPath)
	})

	return r
}
