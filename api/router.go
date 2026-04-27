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

		// Enforce max upload size
		if cfg.MaxUploadSizeMB > 0 && header.Size > int64(cfg.MaxUploadSizeMB)*1024*1024 {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": fmt.Sprintf("File too large. Maximum allowed size is %d MB", cfg.MaxUploadSizeMB),
			})
			return
		}

		filename := header.Filename
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
		defer os.Remove(tempFilePath)

		mimeType := header.Header.Get("Content-Type")
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}

		uniqueName := database.GetUniqueFilename(path, filename, false)

		// Synchronous upload — block until Telegram upload + DB insert done
		fileID, err := tgclient.ProcessCompleteUploadSync(c.Request.Context(), tempFilePath, uniqueName, path, mimeType, cfg)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Upload failed: " + err.Error()})
			return
		}

		resp := gin.H{
			"status":   "done",
			"filename": uniqueName,
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
				c.JSON(http.StatusForbidden, gin.H{"error": "incorrect old password"})
				return
			}
			
			hashedPassword, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to hash password"})
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
			uniqueName := database.GetUniqueFilename(path, name, true)
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

			filename := c.PostForm("filename")
			path := c.PostForm("path")
			if path == "" {
				path = "/"
			}
			taskID := c.PostForm("task_id")
			chunkIndex, _ := strconv.Atoi(c.PostForm("chunk_index"))
			totalChunks, _ := strconv.Atoi(c.PostForm("total_chunks"))

			tempDir := cfg.TempDir
			os.MkdirAll(tempDir, os.ModePerm)
			tempFilePath := filepath.Join(tempDir, taskID+"_"+filename)

			out, err := os.OpenFile(tempFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			_, err = io.Copy(out, file)
			out.Close()

			if totalChunks == 0 {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "totalChunks is 0"})
				return
			}
			serverPercent := int((float64(chunkIndex+1) / float64(totalChunks)) * 100)
			tgclient.UpdateTask(taskID, "uploading_to_server", serverPercent, "")

			if chunkIndex == totalChunks-1 {
				mimeType := header.Header.Get("Content-Type")
				if mimeType == "" {
					mimeType = "application/octet-stream"
				}

				go func() {
					uniqueName := database.GetUniqueFilename(path, filename, false)
					tgclient.ProcessCompleteUpload(context.Background(), tempFilePath, uniqueName, path, mimeType, taskID, cfg)
					os.Remove(tempFilePath)
				}()

				c.JSON(http.StatusOK, gin.H{"status": "processing_telegram", "message": "Received all, pushing to Telegram"})
				return
			}

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

			tx, _ := database.DB.Beginx()
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
					if req.Destination == oldPrefix || strings.HasPrefix(req.Destination, oldPrefix+"/") {
						continue
					}
				}

				if req.Action == "move" {
					tx.Exec("UPDATE files SET path = ? WHERE id = ?", req.Destination, id)
					if item.IsFolder {
						oldPrefix := item.Path + "/" + item.Filename
						if item.Path == "/" {
							oldPrefix = "/" + item.Filename
						}
						newPrefix := req.Destination + "/" + item.Filename
						if req.Destination == "/" {
							newPrefix = "/" + item.Filename
						}
						tx.Exec("UPDATE files SET path = ? || SUBSTR(path, ?) WHERE path = ? OR path LIKE ?", newPrefix, len(oldPrefix)+1, oldPrefix, oldPrefix+"/%")
					}
				} else if req.Action == "copy" {
					if item.IsFolder {
						tx.Exec("INSERT INTO files (filename, path, is_folder) VALUES (?, ?, 1)", item.Filename, req.Destination)
						oldPrefix := item.Path + "/" + item.Filename
						if item.Path == "/" {
							oldPrefix = "/" + item.Filename
						}
						newPrefix := req.Destination + "/" + item.Filename
						if req.Destination == "/" {
							newPrefix = "/" + item.Filename
						}
						tx.Exec(`INSERT INTO files (message_id, filename, path, size, mime_type, is_folder, thumb_path, share_token)
                            SELECT message_id, filename, ? || SUBSTR(path, ?), size, mime_type, is_folder, thumb_path, NULL
                            FROM files WHERE path = ? OR path LIKE ?`, newPrefix, len(oldPrefix)+1, oldPrefix, oldPrefix+"/%")
					} else {
						uniqueName := database.GetUniqueFilename(req.Destination, item.Filename, false)
						tx.Exec("INSERT INTO files (message_id, filename, path, size, mime_type, is_folder, thumb_path) VALUES (?, ?, ?, ?, ?, 0, ?)", item.MessageID, uniqueName, req.Destination, item.Size, item.MimeType, item.ThumbPath)
					}
				}
			}
			tx.Commit()
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
						if count <= 1 { // Only this one exists (or 0 if something went wrong)
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
				
				database.DB.Exec("DELETE FROM files WHERE path = ? OR path LIKE ?", oldPrefix, oldPrefix+"/%")
				database.DB.Exec("DELETE FROM files WHERE id = ?", id)
				
				if len(msgIDsToDelete) > 0 {
					tgclient.DeleteMessages(context.Background(), cfg, msgIDsToDelete)
				}
			} else {
				if item.MessageID != nil {
					var count int
					database.DB.Get(&count, "SELECT COUNT(*) FROM files WHERE message_id = ?", *item.MessageID)
					if count <= 1 {
						tgclient.DeleteMessages(context.Background(), cfg, []int{*item.MessageID})
					}
				}
				if item.ThumbPath != nil {
					var count int
					database.DB.Get(&count, "SELECT COUNT(*) FROM files WHERE thumb_path = ?", *item.ThumbPath)
					if count <= 1 {
						os.Remove(*item.ThumbPath)
					}
				}
				database.DB.Exec("DELETE FROM files WHERE id = ?", id)
			}
			
			c.JSON(http.StatusOK, gin.H{"status": "deleted"})
		})

		api.PUT("/files/:id/rename", func(c *gin.Context) {
			id, _ := strconv.Atoi(c.Param("id"))
			newName := c.PostForm("new_name")

			var item database.File
			database.DB.Get(&item, "SELECT filename, path, is_folder FROM files WHERE id = ?", id)

			if !item.IsFolder {
				oldExt := filepath.Ext(item.Filename)
				newExt := filepath.Ext(newName)
				if oldExt != "" && newExt == "" {
					newName += oldExt
				}
			} else {
				basePath := item.Path
				oldPrefix := basePath + "/" + item.Filename
				if basePath == "/" {
					oldPrefix = "/" + item.Filename
				}
				newPrefix := basePath + "/" + newName
				if basePath == "/" {
					newPrefix = "/" + newName
				}
				database.DB.Exec("UPDATE files SET path = ? || SUBSTR(path, ?) WHERE path = ? OR path LIKE ?", newPrefix, len(oldPrefix)+1, oldPrefix, oldPrefix+"/%")
			}

			uniqueName := database.GetUniqueFilename(item.Path, newName, item.IsFolder)
			database.DB.Exec("UPDATE files SET filename = ? WHERE id = ?", uniqueName, id)
			c.JSON(http.StatusOK, gin.H{"status": "renamed"})
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
