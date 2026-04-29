package api

import (
	"context"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
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

func SetupRouter(cfg *config.Config, contentFS fs.FS) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()
	r.SetTrustedProxies(nil)
	
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

	r.Use(setupCheckMiddleware())

	// Middleware for auth
	authMiddleware := func() gin.HandlerFunc {
		return func(c *gin.Context) {
			token, err := c.Cookie("session_token")
			if err != nil || token == "" {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
				return
			}
			
			var count int
			err = database.DB.Get(&count, "SELECT COUNT(*) FROM sessions WHERE token = ?", token)
			if err != nil || count == 0 {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
				return
			}

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
		_, err = database.DB.Exec("INSERT INTO sessions (token) VALUES (?)", sessionToken)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create session"})
			return
		}
		c.SetCookie("session_token", sessionToken, 3600*24*30, "/", "", false, true)

		c.JSON(http.StatusOK, gin.H{"status": "success"})
	})

	r.GET("/", func(c *gin.Context) {
		token, _ := c.Cookie("session_token")
		var count int
		if token != "" {
			database.DB.Get(&count, "SELECT COUNT(*) FROM sessions WHERE token = ?", token)
		}
		if token == "" || count == 0 {
			c.Redirect(http.StatusFound, "/login")
			return
		}
		
		setCSRFCookie(c)
		webdavEnabled := database.GetSetting("webdav_enabled") == "true"
		webdavUser := database.GetSetting("admin_username")
		uploadAPIEnabled := database.GetSetting("upload_api_enabled") == "true"
		uploadAPIKey := database.GetSetting("upload_api_key")

		c.HTML(http.StatusOK, "index.html", gin.H{
			"max_upload_size_mb": cfg.MaxUploadSizeMB,
			"webdav_enabled":     webdavEnabled,
			"webdav_user":        webdavUser,
			"upload_api_enabled": uploadAPIEnabled,
			"upload_api_key":     uploadAPIKey,
			"version":            cfg.Version,
		})
	})

	r.GET("/login", func(c *gin.Context) {
		token, _ := c.Cookie("session_token")
		var count int
		if token != "" {
			database.DB.Get(&count, "SELECT COUNT(*) FROM sessions WHERE token = ?", token)
		}
		if token != "" && count > 0 {
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

		if username == dbUser && bcrypt.CompareHashAndPassword([]byte(dbHash), []byte(password)) == nil {
			loginAttempts.Delete(ip) // Reset on success
			sessionToken := uuid.New().String()
			_, err = database.DB.Exec("INSERT INTO sessions (token) VALUES (?)", sessionToken)
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

		apiKey := database.GetSetting("upload_api_key")
		if apiKey == "" {
			c.JSON(http.StatusForbidden, gin.H{"error": "No API key configured"})
			return
		}
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" || authHeader != "Bearer "+apiKey {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or missing API key"})
			return
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
				tgclient.ProcessCompleteUpload(context.Background(), tempFilePath, filename, path, mimeType, taskID, cfg, overwrite)
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

	api := r.Group("/api")
	api.Use(authMiddleware())
	api.Use(csrfMiddleware())
	{
		api.POST("/settings/password", func(c *gin.Context) {
			oldPassword := c.PostForm("old_password")
			newPassword := c.PostForm("new_password")
			
			dbHash := database.GetSetting("admin_password_hash")
			if bcrypt.CompareHashAndPassword([]byte(dbHash), []byte(oldPassword)) != nil {
				c.JSON(http.StatusForbidden, gin.H{"error": "incorrect_old_password"})
				return
			}
			
			hashedPassword, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_hash_password"})
				return
			}
			
			database.SetSetting("admin_password_hash", string(hashedPassword))
			
			// Optional: invalidate current sessions or leave as is. We'll leave it to not log them out immediately.
			c.JSON(http.StatusOK, gin.H{"status": "success"})
		})
		
		api.POST("/settings/webdav", func(c *gin.Context) {
			enabled := c.PostForm("enabled")
			if enabled == "true" {
				database.SetSetting("webdav_enabled", "true")
			} else {
				database.SetSetting("webdav_enabled", "false")
			}
			c.JSON(http.StatusOK, gin.H{"status": "success"})
		})

		api.POST("/settings/upload-api", func(c *gin.Context) {
			enabled := c.PostForm("enabled")
			if enabled == "true" {
				database.SetSetting("upload_api_enabled", "true")
			} else {
				database.SetSetting("upload_api_enabled", "false")
			}
			c.JSON(http.StatusOK, gin.H{"status": "success"})
		})

		api.POST("/settings/upload-api/regenerate-key", func(c *gin.Context) {
			newKey := uuid.New().String()
			database.SetSetting("upload_api_key", newKey)
			c.JSON(http.StatusOK, gin.H{"status": "success", "api_key": newKey})
		})

		api.DELETE("/settings/upload-api/key", func(c *gin.Context) {
			database.SetSetting("upload_api_key", "")
			c.JSON(http.StatusOK, gin.H{"status": "success"})
		})

		api.GET("/progress/:task_id", func(c *gin.Context) {
			taskID := c.Param("task_id")
			c.JSON(http.StatusOK, tgclient.GetTask(taskID))
		})

		api.GET("/ws", func(c *gin.Context) {
			ws.HandleWebSocket(c.Writer, c.Request)
		})

		api.GET("/files", func(c *gin.Context) {
			path := c.Query("path")
			if path == "" {
				path = "/"
			}
			var files []database.File
			err := database.DB.Select(&files, "SELECT * FROM files WHERE path = ? ORDER BY is_folder DESC, id DESC", path)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}

			for i := range files {
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
			if path == "" {
				path = "/"
			}
			uniqueName := database.GetUniqueFilename(database.DB, path, name, true, 0)
			_, err := database.DB.Exec("INSERT INTO files (filename, path, is_folder) VALUES (?, ?, 1)", uniqueName, path)
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
			if path == "" {
				path = "/"
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
					tgclient.ProcessCompleteUpload(context.Background(), tempFilePath, filename, path, mimeType, taskID, cfg, false)
				}()

				c.JSON(http.StatusOK, gin.H{"status": "processing_telegram", "message": "Received all, pushing to Telegram"})
				return
			}

			serverPercent := int((float64(actualReceived) / float64(totalChunks)) * 100)
			tgclient.UpdateTask(taskID, "uploading_to_server", serverPercent, "")

			c.JSON(http.StatusOK, gin.H{"status": "chunk_received", "chunk": chunkIndex})
		})

		api.POST("/cancel_upload", func(c *gin.Context) {
			taskID := c.PostForm("task_id")
			filename := c.PostForm("filename")

			// 1. Cancel the telegram upload if it's currently syncing
			if taskID != "" {
				tgclient.CancelTask(taskID)
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
			token := uuid.New().String()
			database.DB.Exec("UPDATE files SET share_token = ? WHERE id = ?", token, id)
			c.JSON(http.StatusOK, gin.H{"share_token": token, "direct_token": utils.GenerateDirectToken(token)})
		})

		api.DELETE("/files/:id/share", func(c *gin.Context) {
			id, _ := strconv.Atoi(c.Param("id"))
			database.DB.Exec("UPDATE files SET share_token = NULL WHERE id = ?", id)
			c.JSON(http.StatusOK, gin.H{"status": "revoked"})
		})

		api.GET("/files/:id/thumb", func(c *gin.Context) {
			id, _ := strconv.Atoi(c.Param("id"))
			var item database.File
			if err := database.DB.Get(&item, "SELECT thumb_path FROM files WHERE id = ?", id); err != nil || item.ThumbPath == nil {
				c.AbortWithStatus(http.StatusNotFound)
				return
			}
			c.File(*item.ThumbPath)
		})

		api.GET("/files/:id/stream", func(c *gin.Context) {
			id, _ := strconv.Atoi(c.Param("id"))
			var item database.File
			if err := database.DB.Get(&item, "SELECT message_id, filename, mime_type, size FROM files WHERE id = ?", id); err != nil || item.MessageID == nil {
				c.AbortWithStatus(http.StatusNotFound)
				return
			}
			if item.MimeType != nil {
				c.Header("Content-Type", *item.MimeType)
			}
			tgclient.ServeTelegramFile(c.Request, c.Writer, *item.MessageID, item.Filename, item.Size, cfg)
		})
	}

	r.GET("/download/:id", authMiddleware(), func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		var item database.File
		if err := database.DB.Get(&item, "SELECT message_id, filename, mime_type, size FROM files WHERE id = ?", id); err != nil || item.MessageID == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
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
		if err := database.DB.Get(&item, "SELECT filename, size, created_at, thumb_path FROM files WHERE share_token = ?", token); err != nil {
			c.HTML(http.StatusNotFound, "error.html", gin.H{"error_message": "File not found or link has been revoked."})
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

	r.GET("/s/:token/stream", func(c *gin.Context) {
		token := c.Param("token")
		var item database.File
		if err := database.DB.Get(&item, "SELECT message_id, filename, size, mime_type FROM files WHERE share_token = ?", token); err != nil || item.MessageID == nil {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		
		if item.MimeType != nil {
			c.Header("Content-Type", *item.MimeType)
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
		// Set cookie để frontend share.html biết download đã bắt đầu và ẩn overlay
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

	return r
}
