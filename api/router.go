package api

import (
	"context"
	"fmt"
	"html/template"
	"io"
	"io/fs"
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
	"telecloud/tgclient"
	"telecloud/utils"
	"telecloud/webdav"
	"telecloud/ws"
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
)


const csrfCookieName = "csrf_token"
const csrfHeaderName = "X-CSRF-Token"

// generateCSRFToken creates a new random CSRF token
func generateCSRFToken() string {
	return uuid.New().String()
}

// setCSRFCookie sets the CSRF cookie on a response.
// HttpOnly=false so JavaScript can read it to include in request headers.
func setCSRFCookie(c *gin.Context) string {
	token, err := c.Cookie(csrfCookieName)
	if err != nil || token == "" {
		token = generateCSRFToken()
	}
	c.SetCookie(csrfCookieName, token, 3600*24*7, "/", "", false, false)
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
	cleanPath := path.Clean(dbPath)
	if cleanPath == "/" {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(cleanPath, "/"), "/")
	rootFolder := parts[0]

	var exists int
	database.DB.Get(&exists, "SELECT COUNT(*) FROM child_accounts WHERE username = ?", rootFolder)
	return exists > 0
}



// securityHeadersMiddleware adds standard security headers to prevent common web attacks.
func securityHeadersMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "SAMEORIGIN")
		c.Header("X-XSS-Protection", "1; mode=block")
		// Basic Content Security Policy
		c.Header("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline' 'unsafe-eval' https://cdnjs.cloudflare.com https://static.cloudflareinsights.com; style-src 'self' 'unsafe-inline' https://cdnjs.cloudflare.com https://fonts.googleapis.com; font-src 'self' https://cdnjs.cloudflare.com https://fonts.gstatic.com; img-src 'self' data:; connect-src 'self' https://api.github.com https://cloudflareinsights.com; media-src 'self' blob:;")
		c.Next()
	}
}

func SetupRouter(cfg *config.Config, contentFS fs.FS) *gin.Engine {
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
			adminUser := database.GetSetting("admin_username")
			if adminUser == "" && !strings.HasPrefix(c.Request.URL.Path, "/setup") && !strings.HasPrefix(c.Request.URL.Path, "/static") {
				c.Redirect(http.StatusFound, "/setup")
				c.Abort()
				return
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


	// WebDAV Route (handler will check if enabled internally)
	h := gin.WrapH(webdav.NewHandler(cfg))
	methods := []string{
		"GET", "POST", "PUT", "PATCH", "HEAD", "OPTIONS", "DELETE", "CONNECT", "TRACE",
		"PROPFIND", "PROPPATCH", "MKCOL", "COPY", "MOVE", "LOCK", "UNLOCK",
	}
	for _, method := range methods {
		r.Handle(method, "/webdav", h)
		r.Handle(method, "/webdav/*path", h)
	}

	r.GET("/setup", func(c *gin.Context) {
		adminUser := database.GetSetting("admin_username")
		if adminUser != "" {
			c.Redirect(http.StatusFound, "/")
			return
		}
		setCSRFCookie(c)
		c.HTML(http.StatusOK, "setup.html", gin.H{
			"version": cfg.Version,
		})
	})

	r.POST("/setup", func(c *gin.Context) {
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
		c.SetCookie("session_token", sessionToken, 3600*24*30, "/", "", false, true)

		c.JSON(http.StatusOK, gin.H{"status": "success"})
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

		c.HTML(http.StatusOK, "index.html", gin.H{
			"max_upload_size_mb":     cfg.MaxUploadSizeMB,
			"webdav_enabled":         webdavEnabled,
			"global_webdav_enabled":  globalWebdavEnabled,
			"webdav_user":            webdavUser,
			"upload_api_enabled":     uploadAPIEnabled,
			"global_api_enabled":     globalUploadAPIEnabled,
			"upload_api_key":         uploadAPIKey,
			"webauthn_rpid":         database.GetSetting("webauthn_rpid"),
			"webauthn_rporigin":     database.GetSetting("webauthn_rporigin"),
			"version":                cfg.Version,
			"is_admin":               isAdmin,
			"username":               sessionUsername,
			"storage_used":           userStorageUsed,
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
			c.SetCookie("session_token", sessionToken, 3600*24*30, "/", "", false, true)
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
		c.SetCookie("session_token", "", -1, "/", "", false, true)
		c.SetCookie(csrfCookieName, "", -1, "/", "", false, false)
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

		// Limit request body to prevent OOM / disk-exhaustion attacks.
		// Allow max upload size + 32 KB overhead for multipart form fields.
		maxBytes := int64(cfg.MaxUploadSizeMB) * 1024 * 1024
		if maxBytes <= 0 {
			maxBytes = 4000 * 1024 * 1024 // fallback: 4 GB (premium Telegram limit)
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes+32*1024)

		file, header, err := c.Request.FormFile("file")
		if err != nil {
			if err.Error() == "http: request body too large" {
				c.JSON(http.StatusRequestEntityTooLarge, gin.H{
					"error": fmt.Sprintf("File too large. Maximum allowed size is %d MB", cfg.MaxUploadSizeMB),
				})
				return
			}
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

		// Validate kích thước thực tế sau khi ghi xong (không tin vào header.Size do client cung cấp)
		if cfg.MaxUploadSizeMB > 0 {
			fi, statErr := os.Stat(tempFilePath)
			if statErr == nil && fi.Size() > int64(cfg.MaxUploadSizeMB)*1024*1024 {
				os.Remove(tempFilePath)
				c.JSON(http.StatusRequestEntityTooLarge, gin.H{
					"error": fmt.Sprintf("File too large. Maximum allowed size is %d MB", cfg.MaxUploadSizeMB),
				})
				return
			}
		}

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
				tgclient.ProcessCompleteUpload(context.Background(), tempFilePath, filename, path, mimeType, taskID, cfg, overwrite, username)
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
		fileID, finalName, err := tgclient.ProcessCompleteUploadSync(c.Request.Context(), tempFilePath, filename, path, mimeType, cfg, overwrite)
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
					c.SetCookie("session_token", sessionToken, 3600*24*30, "/", "", false, true)
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
			err = tx.Get(&folderCount, "SELECT COUNT(*) FROM files WHERE path = '/' AND filename = ? COLLATE NOCASE AND is_folder = 1", username)
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
			err = tx.Get(&userExists, "SELECT COUNT(*) FROM child_accounts WHERE username = ? COLLATE NOCASE", username)
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
			_, err = tx.Exec("INSERT INTO files (filename, path, is_folder) VALUES (?, '/', 1)", username)
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
			
			// 1. Update the root folder record of the user
			_, err = tx.Exec("UPDATE files SET filename = ? WHERE path = '/' AND filename = ? AND is_folder = 1", newFolderName, username)
			if err != nil {
				tx.Rollback()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to rename user folder"})
				return
			}

			// 2. Update paths of all files and subfolders within that folder
			oldPrefix := "/" + username
			newPrefix := "/" + newFolderName
			
			// Update files/folders directly inside the user folder
			_, err = tx.Exec("UPDATE files SET path = ? WHERE path = ?", newPrefix, oldPrefix)
			if err != nil {
				tx.Rollback()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update direct file paths"})
				return
			}
			
			// Update files/folders in subfolders (recursive)
			_, err = tx.Exec("UPDATE files SET path = ? || substr(path, ?) WHERE path LIKE ?", newPrefix, len(oldPrefix)+1, oldPrefix+"/%")
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
			err := database.DB.Select(&files, "SELECT * FROM files WHERE path = ? ORDER BY is_folder DESC, id DESC", dbPath)
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
			c.JSON(http.StatusOK, gin.H{"files": files})
		})

		api.POST("/folders", func(c *gin.Context) {
			name := c.PostForm("name")
			path := c.PostForm("path")
			username := c.GetString("username")
			isAdmin := c.GetBool("is_admin")
			dbPath := mapPath(path, username, isAdmin)
			
			if isAdmin && isChildAccountPath(dbPath) {
				c.JSON(http.StatusForbidden, gin.H{"error": "Admin cannot create folders in child account directory"})
				return
			}

			if isAdmin && dbPath == "/" {
				var count int
				database.DB.Get(&count, "SELECT COUNT(*) FROM child_accounts WHERE username = ?", name)
				if count > 0 {
					c.JSON(http.StatusForbidden, gin.H{"error": "Folder name collides with a child account username"})
					return
				}
			}

			uniqueName := database.GetUniqueFilename(database.DB, dbPath, name, true, 0)
			_, err := database.DB.Exec("INSERT INTO files (filename, path, is_folder) VALUES (?, ?, 1)", uniqueName, dbPath)
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
				c.JSON(http.StatusForbidden, gin.H{"error": "Admin cannot upload to child account directory"})
				return
			}

			if isAdmin && dbPath == "/" {
				var count int
				database.DB.Get(&count, "SELECT COUNT(*) FROM child_accounts WHERE username = ?", filename)
				if count > 0 {
					c.JSON(http.StatusForbidden, gin.H{"error": "Filename collides with a child account username"})
					return
				}
			}

			taskID := c.PostForm("task_id")
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
			tempFilePath := filepath.Join(tempDir, taskID+"_"+filename)

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

			// Lock only to update the received-chunks map
			state.Lock()
			if state.received[chunkIndex] {
				// Chunk đã được nhận rồi (do retry gửi lại) – idempotent, bỏ qua
				state.Unlock()
				c.JSON(http.StatusOK, gin.H{"status": "chunk_already_received", "chunk": chunkIndex})
				return
			}
			state.received[chunkIndex] = true
			actualReceived := len(state.received)
			isDone := actualReceived == totalChunks
			if isDone {
				chunkTrackerSync.Delete(taskID)
			}
			state.Unlock()

			if isDone {
				tgclient.UpdateTask(taskID, "uploading_to_server", 100, "")
				
				mimeType := header.Header.Get("Content-Type")
				if mimeType == "" {
					mimeType = "application/octet-stream"
				}

				go func() {
					defer os.Remove(tempFilePath)
					tgclient.ProcessCompleteUpload(context.Background(), tempFilePath, filename, dbPath, mimeType, taskID, cfg, false, username)
				}()

				c.JSON(http.StatusOK, gin.H{"status": "processing_telegram", "message": "Received all, pushing to Telegram"})
				return
			}

			serverPercent := int((float64(actualReceived) / float64(totalChunks)) * 100)
			tgclient.UpdateTask(taskID, "uploading_to_server", serverPercent, "")

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
				err = database.DB.Get(&folder, "SELECT is_folder FROM files WHERE path = ? AND filename = ? AND is_folder = 1", filepath.Dir(dbPath), filepath.Base(dbPath))
				if err != nil {
					c.JSON(http.StatusNotFound, gin.H{"error": "folder_not_found"})
					return
				}
			}

			taskID := uuid.New().String()
			go tgclient.ProcessRemoteUpload(context.Background(), remoteURL, dbPath, taskID, cfg, overwrite, cfg.MaxUploadSizeMB, username)

			c.JSON(http.StatusOK, gin.H{
				"status": "processing",
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
				tempFilePath := filepath.Join(cfg.TempDir, taskID+"_"+filename)
				os.Remove(tempFilePath)
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

				if isAdmin && isChildAccountPath(item.Path) {
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
				uniqueName := database.GetUniqueFilename(tx, req.Destination, item.Filename, item.IsFolder, excludeID)

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
						_, err = tx.Exec("UPDATE files SET path = ? || SUBSTR(path, ?) WHERE path = ? OR path LIKE ?", newPrefix, len(oldPrefix)+1, oldPrefix, oldPrefix+"/%")
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
						_, err = tx.Exec("INSERT INTO files (filename, path, is_folder) VALUES (?, ?, 1)", uniqueName, req.Destination)
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
						
						_, err = tx.Exec(`INSERT INTO files (message_id, filename, path, size, mime_type, is_folder, thumb_path, share_token)
                            SELECT message_id, filename, ? || SUBSTR(path, ?), size, mime_type, is_folder, thumb_path, NULL
                            FROM files WHERE path = ? OR path LIKE ?`, newPrefix, len(oldPrefix)+1, oldPrefix, oldPrefix+"/%")
						if err != nil {
							c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
							return
						}
					} else {
						// Only copy files that have a valid Telegram message reference
						if item.MessageID == nil {
							continue
						}
						_, err = tx.Exec("INSERT INTO files (message_id, filename, path, size, mime_type, is_folder, thumb_path) VALUES (?, ?, ?, ?, ?, 0, ?)", item.MessageID, uniqueName, req.Destination, item.Size, item.MimeType, item.ThumbPath)
						if err != nil {
							c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
							return
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
				database.DB.Select(&children, "SELECT message_id, thumb_path FROM files WHERE (path = ? OR path LIKE ?) AND message_id IS NOT NULL", oldPrefix, oldPrefix+"/%")

				var msgIDsToDelete []int
				for _, child := range children {
					if child.MessageID != nil {
						var count int
						database.DB.Get(&count, "SELECT COUNT(*) FROM files WHERE message_id = ?", *child.MessageID)
						if count <= 1 {
							msgIDsToDelete = append(msgIDsToDelete, *child.MessageID)
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
				database.DB.Exec("DELETE FROM files WHERE path = ? OR path LIKE ?", oldPrefix, oldPrefix+"/%")
				database.DB.Exec("DELETE FROM files WHERE id = ?", id)
				if len(msgIDsToDelete) > 0 {
					tgclient.DeleteMessages(context.Background(), cfg, msgIDsToDelete)
				}
			} else {
				var msgIDsToDelete []int
				if item.MessageID != nil {
					var count int
					database.DB.Get(&count, "SELECT COUNT(*) FROM files WHERE message_id = ?", *item.MessageID)
					if count <= 1 {
						msgIDsToDelete = append(msgIDsToDelete, *item.MessageID)
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
			if !verifyItemAccess(c, item.Path) || (isAdmin && isChildAccountPath(item.Path)) {
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

			uniqueName := database.GetUniqueFilename(tx, item.Path, newName, item.IsFolder, id)

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
				_, err = tx.Exec("UPDATE files SET path = ? || SUBSTR(path, ?) WHERE path = ? OR path LIKE ?", newPrefix, len(oldPrefix)+1, oldPrefix, oldPrefix+"/%")
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
			if err := database.DB.Get(&item, "SELECT path, message_id, filename, mime_type, size FROM files WHERE id = ?", id); err != nil || item.MessageID == nil {
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
			tgclient.ServeTelegramFile(c.Request, c.Writer, *item.MessageID, item.Filename, item.Size, cfg)
		})
	}

	r.GET("/download/:id", authMiddleware(), func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		var item database.File
		if err := database.DB.Get(&item, "SELECT path, message_id, filename, mime_type, size FROM files WHERE id = ?", id); err != nil || item.MessageID == nil {
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

		if err := tgclient.ServeTelegramFile(c.Request, c.Writer, *item.MessageID, item.Filename, item.Size, cfg); err != nil {
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
		if err := database.DB.Get(&item, "SELECT message_id, filename, mime_type, size FROM files WHERE share_token = ?", *shareToken); err != nil || item.MessageID == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
			return
		}

		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, item.Filename))
		if item.MimeType != nil {
			c.Header("Content-Type", *item.MimeType)
		}

		if err := tgclient.ServeTelegramFile(c.Request, c.Writer, *item.MessageID, item.Filename, item.Size, cfg); err != nil {
			fmt.Println("Stream error:", err)
		}
	})

	r.GET("/s/:token", func(c *gin.Context) {
		token := c.Param("token")
		var item database.File
		if err := database.DB.Get(&item, "SELECT filename, size, created_at, thumb_path, is_folder, path FROM files WHERE share_token = ?", token); err != nil {
			c.HTML(http.StatusNotFound, "error.html", gin.H{"error_message": "File not found or link has been revoked."})
			return
		}
		
		if item.IsFolder {
			c.HTML(http.StatusOK, "share_folder.html", gin.H{
				"filename": item.Filename,
				"created_at": item.CreatedAt.Format("2006-01-02 15:04:05"),
				"token": token,
				"version": cfg.Version,
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
			"filename": item.Filename,
			"size": item.Size,
			"created_at": item.CreatedAt.Format("2006-01-02 15:04:05"),
			"token": token,
			"has_thumb": hasThumb,
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
		err := database.DB.Select(&files, "SELECT id, filename, path, size, created_at, is_folder, mime_type, thumb_path FROM files WHERE path = ? ORDER BY is_folder DESC, id DESC", targetPath)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		for i := range files {
			if files[i].ThumbPath != nil {
				if _, err := os.Stat(*files[i].ThumbPath); err == nil {
					files[i].HasThumb = true
				}
			}
		}
		c.JSON(http.StatusOK, gin.H{"files": files})
	})

	r.GET("/s/:token/stream", func(c *gin.Context) {
		token := c.Param("token")
		var item database.File
		if err := database.DB.Get(&item, "SELECT message_id, filename, size, mime_type FROM files WHERE share_token = ?", token); err != nil || item.MessageID == nil {
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
		
		tgclient.ServeTelegramFile(c.Request, c.Writer, *item.MessageID, item.Filename, item.Size, cfg)
	})

	r.POST("/s/:token/dl", func(c *gin.Context) {
		token := c.Param("token")
		var item database.File
		if err := database.DB.Get(&item, "SELECT message_id, filename, size, mime_type FROM files WHERE share_token = ?", token); err != nil || item.MessageID == nil {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}

		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, item.Filename))
		if item.MimeType != nil {
			c.Header("Content-Type", *item.MimeType)
		}
		c.SetCookie("dl_started", "1", 15, "/", "", false, false)

		tgclient.ServeTelegramFile(c.Request, c.Writer, *item.MessageID, item.Filename, item.Size, cfg)
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
		if err := database.DB.Get(&item, "SELECT message_id, filename, size, mime_type, path FROM files WHERE id = ?", id); err != nil || item.MessageID == nil {
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
		
		tgclient.ServeTelegramFile(c.Request, c.Writer, *item.MessageID, item.Filename, item.Size, cfg)
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
		if err := database.DB.Get(&item, "SELECT message_id, filename, size, mime_type, path FROM files WHERE id = ?", id); err != nil || item.MessageID == nil {
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

		tgclient.ServeTelegramFile(c.Request, c.Writer, *item.MessageID, item.Filename, item.Size, cfg)
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
