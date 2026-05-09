package api

import (
	"context"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"telecloud/database"
	"telecloud/tgclient"
	"telecloud/utils"
	"telecloud/ws"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func (h *Handler) handleGetIndex(c *gin.Context) {
	token, _ := c.Cookie("session_token")
	var sessionUsername string
	if token != "" {
		database.RODB.Get(&sessionUsername, "SELECT username FROM sessions WHERE token = ?", token)
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
		err := database.RODB.Get(&userStatus, "SELECT webdav_enabled, api_enabled FROM child_accounts WHERE username = ?", sessionUsername)
		if err == nil {
			webdavEnabled = (globalWebdavEnabled && userStatus.WebDAVEnabled == 1)
			uploadAPIEnabled = (globalUploadAPIEnabled && userStatus.APIEnabled == 1)
		}
		webdavUser = sessionUsername
	}

	var forcePasswordChange bool
	if !isAdmin {
		var fc int
		database.RODB.Get(&fc, "SELECT force_password_change FROM child_accounts WHERE username = ?", sessionUsername)
		forcePasswordChange = fc == 1
	}

	var userStorageUsed int64
	if isAdmin {
		database.RODB.Get(&userStorageUsed, "SELECT COALESCE(SUM(size), 0) FROM files WHERE is_folder = 0")
	} else {
		prefix := "/" + sessionUsername
		database.RODB.Get(&userStorageUsed, "SELECT COALESCE(SUM(size), 0) FROM files WHERE (path = ? OR path LIKE ?) AND is_folder = 0", prefix, prefix+"/%")
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
		err := database.RODB.Get(&childS3, "SELECT s3_enabled, s3_access_key, s3_secret_key FROM child_accounts WHERE username = ?", sessionUsername)
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

	currentRPID, currentOrigins := GetWebAuthnConfig()
	originsStr := strings.Join(currentOrigins, ",")

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
		"webauthn_rpid":         currentRPID,
		"webauthn_rporigin":     originsStr,
		"version":               h.cfg.Version,
		"is_admin":              isAdmin,
		"username":              sessionUsername,
		"storage_used":          userStorageUsed,
		"theme":                 database.GetUserSetting(sessionUsername, "theme"),
		"force_change":          forcePasswordChange,
	})
}

func (h *Handler) handleGetFiles(c *gin.Context) {
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
			parts := strings.Split(strings.TrimPrefix(dbPath, "/"), "/")
			rootFolder := parts[0]
			var isChild int
			database.RODB.Get(&isChild, "SELECT COUNT(*) FROM child_accounts WHERE username = ?", rootFolder)

			effectiveOwner := username
			if isChild > 0 {
				effectiveOwner = rootFolder
			}
			args = []interface{}{dbPath, effectiveOwner}
		}
	}
	err := database.RODB.Select(&files, query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if isAdmin && dbPath == "/" {
		var activeUsers []string
		database.RODB.Select(&activeUsers, "SELECT username FROM child_accounts")
		userMap := make(map[string]bool)
		for _, u := range activeUsers {
			userMap[u] = true
		}
		var filtered []database.File
		for _, f := range files {
			if f.IsFolder && userMap[f.Filename] {
				continue
			}
			filtered = append(filtered, f)
		}
		files = filtered
	}

	for i := range files {
		files[i].Path = unmapPath(files[i].Path, username, isAdmin)
		if files[i].ShareToken != nil && !files[i].IsFolder {
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
		database.RODB.Get(&storageUsed, "SELECT COALESCE(SUM(size), 0) FROM files WHERE is_folder = 0")
	} else {
		prefix := "/" + username
		database.RODB.Get(&storageUsed, "SELECT COALESCE(SUM(size), 0) FROM files WHERE (path = ? OR path LIKE ?) AND is_folder = 0", prefix, prefix+"/%")
	}

	c.JSON(http.StatusOK, gin.H{
		"files":        files,
		"storage_used": storageUsed,
	})
}

func (h *Handler) handlePostFolders(c *gin.Context) {
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
		database.RODB.Get(&count, "SELECT COUNT(*) FROM child_accounts WHERE username = ?", name)
		if count > 0 {
			c.JSON(http.StatusForbidden, gin.H{"error": "folder_collides_child"})
			return
		}
	}

	uniqueName := database.GetUniqueFilename(database.RODB, dbPath, name, true, 0, username)
	_, err := database.DB.Exec("INSERT INTO files (filename, path, is_folder, owner) VALUES (?, ?, 1, ?)", uniqueName, dbPath, username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func (h *Handler) handlePostUpload(c *gin.Context) {
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
		database.RODB.Get(&count, "SELECT COUNT(*) FROM child_accounts WHERE username = ?", filename)
		if count > 0 {
			c.JSON(http.StatusForbidden, gin.H{"error": "filename_collides_child"})
			return
		}
	}

	taskID := c.PostForm("task_id")
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

	tempDir := h.cfg.TempDir
	os.MkdirAll(tempDir, os.ModePerm)
	safeFilename := filepath.Base(filename)
	tempFilePath := filepath.Join(tempDir, taskID+"_"+safeFilename)

	rel, err := filepath.Rel(tempDir, tempFilePath)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_path"})
		return
	}

	const chunkSize = 10 * 1024 * 1024
	offset := int64(chunkIndex) * int64(chunkSize)

	val, _ := chunkTrackerSync.LoadOrStore(taskID, &chunkState{
		received: make(map[int]bool),
	})
	state := val.(*chunkState)

	chunkData, err := io.ReadAll(file)
	if err != nil {
		log.Printf("UPLOAD ERROR: Failed to read chunk: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_read_chunk"})
		return
	}

	overwriteFlag := c.PostForm("overwrite") == "true"
	database.DB.Exec(database.InsertIgnoreSQL("upload_tasks", "id, filename, owner, overwrite", "?, ?, ?, ?"), taskID, safeFilename, username, overwriteFlag)

	out, err := os.OpenFile(tempFilePath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("UPLOAD ERROR: Failed to open temp file %s: %v", tempFilePath, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_open_temp_file"})
		return
	}
	_, err = out.WriteAt(chunkData, offset)
	out.Close()
	if err != nil {
		log.Printf("UPLOAD ERROR: Failed to write chunk to %s: %v", tempFilePath, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_write_chunk"})
		return
	}

	_, err = database.DB.Exec(database.InsertIgnoreSQL("upload_chunks", "task_id, chunk_index", "?, ?"), taskID, chunkIndex)
	if err != nil {
		log.Printf("UPLOAD ERROR: Failed to record chunk in DB: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_record_chunk"})
		return
	}

	state.Lock()
	state.received[chunkIndex] = true

	var actualReceived int
	database.RODB.Get(&actualReceived, "SELECT COUNT(*) FROM upload_chunks WHERE task_id = ?", taskID)

	// uploadStarted guards against double-trigger: two goroutines can both see
	// COUNT == totalChunks if their INSERTs land before either grabs the lock.
	// Only the first goroutine to set uploadStarted=true will trigger the upload.
	isDone := actualReceived == totalChunks && !state.uploadStarted
	if isDone {
		state.uploadStarted = true
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

		// Capture overwriteFlag from this request directly.
		// Do NOT re-read from upload_tasks: that row was deleted above,
		// so the old code always read false (silent bug).
		ov := overwriteFlag
		go func() {
			defer os.Remove(tempFilePath)
			tgclient.ProcessCompleteUpload(context.Background(), tempFilePath, filename, dbPath, mimeType, taskID, h.cfg, ov, username)
		}()

		c.JSON(http.StatusOK, gin.H{"status": "processing_telegram", "message": "pushing_to_tg"})
		return
	}

	serverPercent := int((float64(actualReceived) / float64(totalChunks)) * 100)
	tgclient.UpdateTask(taskID, "uploading_to_server", serverPercent, "", username)

	c.JSON(http.StatusOK, gin.H{"status": "chunk_received", "chunk": chunkIndex})
}

func (h *Handler) handlePostRemoteUpload(c *gin.Context) {
	remoteURL := c.PostForm("url")
	uPath := c.PostForm("path")
	overwrite := c.PostForm("overwrite") == "true"
	username := c.GetString("username")
	isAdmin := c.GetBool("is_admin")

	if remoteURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url_required"})
		return
	}

	u, err := url.ParseRequestURI(remoteURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_url"})
		return
	}

	if utils.IsPrivateIP(remoteURL) {
		c.JSON(http.StatusForbidden, gin.H{"error": "err_forbidden_url"})
		return
	}

	dbPath := mapPath(uPath, username, isAdmin)
	if isAdmin && isChildAccountPath(dbPath) {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	if dbPath != "/" {
		var folder database.File
		err = database.RODB.Get(&folder, "SELECT is_folder FROM files WHERE path = ? AND filename = ? AND is_folder = 1 AND owner = ?", filepath.Dir(dbPath), filepath.Base(dbPath), username)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "folder_not_found"})
			return
		}
	}

	taskID := c.PostForm("task_id")
	if taskID == "" {
		taskID = uuid.New().String()
	}
	go tgclient.ProcessRemoteUpload(context.Background(), remoteURL, dbPath, taskID, h.cfg, overwrite, username)

	c.JSON(http.StatusOK, gin.H{
		"status":  "processing",
		"task_id": taskID,
	})
}

func (h *Handler) handlePostRemoteUploadCheck(c *gin.Context) {
	remoteURL := c.PostForm("url")
	if remoteURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url_required"})
		return
	}

	if utils.IsPrivateIP(remoteURL) {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden_url"})
		return
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			if utils.IsPrivateIP(req.URL.String()) {
				return fmt.Errorf("forbidden_url")
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), "HEAD", remoteURL, nil)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_url"})
		return
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/119.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		// Try GET if HEAD fails (some servers block HEAD)
		req, _ = http.NewRequestWithContext(c.Request.Context(), "GET", remoteURL, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/119.0.0.0 Safari/537.36")
		req.Header.Set("Range", "bytes=0-0")
		resp, err = client.Do(req)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_connect"})
			return
		}
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")
	contentLength, _ := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)

	// Try to get filename from Content-Disposition
	filename := ""
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		_, params, err := mime.ParseMediaType(cd)
		if err == nil {
			filename = params["filename"]
		}
	}

	// Fallback to URL path
	if filename == "" {
		if u, err := url.Parse(remoteURL); err == nil {
			filename = filepath.Base(u.Path)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"content_type":   contentType,
		"content_length": contentLength,
		"filename":       filename,
	})
}

func (h *Handler) handleGetTasks(c *gin.Context) {
	username := c.GetString("username")
	c.JSON(http.StatusOK, gin.H{
		"tasks": tgclient.GetActiveTasks(username),
	})
}

func (h *Handler) handleCancelUpload(c *gin.Context) {
	taskID := c.PostForm("task_id")
	username := c.GetString("username")

	if taskID != "" {
		// Validate taskID format before any operation to prevent path traversal
		if strings.Contains(taskID, "..") || strings.ContainsAny(taskID, "/\\") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_task_id"})
			return
		}

		if !tgclient.CancelTask(taskID, username) {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}

		// Cleanup files starting with taskID_ or ytdlp_taskID_
		patterns := []string{
			filepath.Join(h.cfg.TempDir, taskID+"_*"),
			filepath.Join(h.cfg.TempDir, "ytdlp_"+taskID+"_*"),
		}

		for _, pattern := range patterns {
			matches, err := filepath.Glob(pattern)
			if err == nil {
				for _, m := range matches {
					os.Remove(m)
				}
			}
		}

		chunkTrackerSync.Delete(taskID)
		database.DB.Exec("DELETE FROM upload_chunks WHERE task_id = ?", taskID)
		database.DB.Exec("DELETE FROM upload_tasks WHERE id = ?", taskID)
	}

	c.JSON(http.StatusOK, gin.H{"status": "cancelled"})
}

func (h *Handler) handlePostPaste(c *gin.Context) {
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
		err := tx.Get(&item, "SELECT * FROM files WHERE id = ? AND owner = ?", id, username)
		if err != nil {
			continue
		}

		if item.IsFolder {
			oldPrefix := item.Path + "/" + item.Filename
			if item.Path == "/" {
				oldPrefix = "/" + item.Filename
			}
			if req.Action == "move" && (req.Destination == oldPrefix || strings.HasPrefix(req.Destination, oldPrefix+"/")) {
				continue
			}
		}

		if req.Action == "move" && req.Destination == item.Path {
			continue
		}

		var excludeID int
		if req.Action == "move" {
			excludeID = item.ID
		}
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
				_, err = tx.Exec("UPDATE files SET path = "+database.ConcatPathSQL()+" WHERE (path = ? OR path LIKE ?) AND owner = ?", newPrefix, len(oldPrefix)+1, oldPrefix, oldPrefix+"/%", item.Owner)
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
}

func (h *Handler) handleDeleteFile(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_id"})
		return
	}

	var item database.File
	username := c.GetString("username")
	if err := database.RODB.Get(&item, "SELECT * FROM files WHERE id = ? AND owner = ?", id, username); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	var fileIDs []int
	if item.IsFolder {
		oldPrefix := item.Path + "/" + item.Filename
		if item.Path == "/" {
			oldPrefix = "/" + item.Filename
		}
		database.RODB.Select(&fileIDs, "SELECT id FROM files WHERE (path = ? OR path LIKE ?) AND owner = ?", oldPrefix, oldPrefix+"/%", item.Owner)
	}
	fileIDs = append(fileIDs, id)

	// Identify messages to delete from Telegram before removing files from DB
	msgIDsToDelete, _ := database.GetOrphanedMessages(fileIDs)

	// Delete thumbnails — single query: only get thumb paths exclusively owned by files being deleted
	if len(fileIDs) > 0 {
		placeholders := make([]string, len(fileIDs))
		args := make([]interface{}, len(fileIDs))
		for i, id := range fileIDs {
			placeholders[i] = "?"
			args[i] = id
		}
		var thumbsToDelete []string
		database.RODB.Select(&thumbsToDelete, fmt.Sprintf(
			`SELECT thumb_path FROM files WHERE id IN (%s) AND thumb_path IS NOT NULL
			 AND (SELECT COUNT(*) FROM files f2 WHERE f2.thumb_path = files.thumb_path) = 1`,
			strings.Join(placeholders, ","),
		), args...)
		for _, tp := range thumbsToDelete {
			os.Remove(tp)
		}
	}

	// Delete from DB
	if item.IsFolder {
		oldPrefix := item.Path + "/" + item.Filename
		if item.Path == "/" {
			oldPrefix = "/" + item.Filename
		}
		database.DB.Exec("DELETE FROM files WHERE (path = ? OR path LIKE ?) AND owner = ?", oldPrefix, oldPrefix+"/%", item.Owner)
	}
	database.DB.Exec("DELETE FROM files WHERE id = ?", id)

	// Delete from Telegram
	if len(msgIDsToDelete) > 0 {
		tgclient.DeleteMessages(context.Background(), h.cfg, msgIDsToDelete)
	}

	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

func (h *Handler) handleRenameFile(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_id"})
		return
	}
	newName := c.PostForm("new_name")

	tx, err := database.DB.Beginx()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start transaction"})
		return
	}
	defer tx.Rollback()

	var item database.File
	username := c.GetString("username")
	err = tx.Get(&item, "SELECT filename, path, is_folder, owner FROM files WHERE id = ? AND owner = ?", id, username)
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
		_, err = tx.Exec("UPDATE files SET path = "+database.ConcatPathSQL()+" WHERE (path = ? OR path LIKE ?) AND owner = ?", newPrefix, len(oldPrefix)+1, oldPrefix, oldPrefix+"/%", item.Owner)
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
}

func (h *Handler) handleShareFile(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_id"})
		return
	}
	var item database.File
	username := c.GetString("username")
	if err := database.RODB.Get(&item, "SELECT path, is_folder FROM files WHERE id = ? AND owner = ?", id, username); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	token := uuid.New().String()
	database.DB.Exec("UPDATE files SET share_token = ? WHERE id = ?", token, id)
	
	resp := gin.H{"share_token": token}
	if !item.IsFolder {
		resp["direct_token"] = utils.GenerateDirectToken(token)
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) handleRevokeShare(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_id"})
		return
	}
	var item database.File
	username := c.GetString("username")
	if err := database.RODB.Get(&item, "SELECT path FROM files WHERE id = ? AND owner = ?", id, username); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	database.DB.Exec("UPDATE files SET share_token = NULL WHERE id = ?", id)
	c.JSON(http.StatusOK, gin.H{"status": "revoked"})
}

func (h *Handler) handleGetThumb(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	var item database.File
	username := c.GetString("username")
	if err := database.RODB.Get(&item, "SELECT path, thumb_path FROM files WHERE id = ? AND owner = ?", id, username); err != nil || item.ThumbPath == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.File(*item.ThumbPath)
}

func (h *Handler) handleStreamFile(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	var item database.File
	username := c.GetString("username")
	if err := database.RODB.Get(&item, "SELECT * FROM files WHERE id = ? AND owner = ?", id, username); err != nil || item.IsFolder {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if err := tgclient.ServeTelegramFile(c.Request, c.Writer, item, h.cfg); err != nil {
		fmt.Printf("[Stream] Error serving file %d: %v\n", id, err)
	}
}

func (h *Handler) handleDownloadFile(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_id"})
		return
	}
	var item database.File
	username := c.GetString("username")
	if err := database.RODB.Get(&item, "SELECT * FROM files WHERE id = ? AND owner = ?", id, username); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
		return
	}

	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, item.Filename))
	if item.MimeType != nil {
		c.Header("Content-Type", *item.MimeType)
	}
	c.SetCookie("dl_started", "1", 15, "/", "", false, false)

	if err := tgclient.ServeTelegramFile(c.Request, c.Writer, item, h.cfg); err != nil {
		fmt.Println("Stream error:", err)
	}
}

func (h *Handler) handleGetProgress(c *gin.Context) {
	taskID := c.Param("task_id")
	c.JSON(http.StatusOK, tgclient.GetTask(taskID))
}

func (h *Handler) handleGetUploadCheck(c *gin.Context) {
	taskID := c.Param("task_id")
	username := c.GetString("username")

	var task struct {
		ID string `db:"id"`
	}
	err := database.RODB.Get(&task, "SELECT id FROM upload_tasks WHERE id = ? AND owner = ?", taskID, username)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"chunks": []int{}})
		return
	}

	var chunks []int
	database.RODB.Select(&chunks, "SELECT chunk_index FROM upload_chunks WHERE task_id = ? ORDER BY chunk_index ASC", taskID)
	c.JSON(http.StatusOK, gin.H{"chunks": chunks})
}

func (h *Handler) handlePostCheckExists(c *gin.Context) {
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
		err := database.RODB.Get(&count, "SELECT COUNT(*) FROM files WHERE path = ? AND filename = ? AND is_folder = 0 AND owner = ?", dbPath, fn, username)
		if err == nil && count > 0 {
			existing = append(existing, fn)
		}
	}
	c.JSON(http.StatusOK, gin.H{"existing": existing})
}

func (h *Handler) handleWebSocket(c *gin.Context) {
	username := c.GetString("username")
	ws.HandleWebSocket(c.Writer, c.Request, username)
}

func (h *Handler) authenticatePublicAPI(c *gin.Context) (string, bool, error) {
	if database.GetSetting("upload_api_enabled") != "true" {
		return "", false, fmt.Errorf("Upload API is disabled")
	}

	authHeader := c.GetHeader("Authorization")
	if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
		return "", false, fmt.Errorf("Invalid or missing Authorization header")
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")

	adminKey := database.GetSetting("upload_api_key")
	if token == adminKey && adminKey != "" {
		return database.GetSetting("admin_username"), true, nil
	}

	var userStatus struct {
		Username    string `db:"username"`
		Enabled     int    `db:"api_enabled"`
		ForceChange int    `db:"force_password_change"`
	}
	err := database.RODB.Get(&userStatus, "SELECT username, api_enabled, force_password_change FROM child_accounts WHERE api_key = ?", token)
	if err != nil || userStatus.Username == "" {
		return "", false, fmt.Errorf("Invalid API key")
	}
	if userStatus.Enabled == 0 {
		return "", false, fmt.Errorf("API is disabled for this account")
	}
	if userStatus.ForceChange == 1 {
		return "", false, fmt.Errorf("Password change required via web interface before using API")
	}

	return userStatus.Username, false, nil
}

func (h *Handler) handlePublicUploadAPI(c *gin.Context) {
	username, isAdmin, err := h.authenticatePublicAPI(c)
	if err != nil {
		if strings.Contains(err.Error(), "Authorization") || strings.Contains(err.Error(), "key") {
			c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		} else if strings.Contains(err.Error(), "disabled") || strings.Contains(err.Error(), "required") {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
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

	dbPath := mapPath(path, username, isAdmin)

	if isAdmin && isChildAccountPath(dbPath) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Admin cannot upload to child account directory"})
		return
	}
	shareMode := c.PostForm("share")

	taskID := uuid.New().String()
	os.MkdirAll(h.cfg.TempDir, os.ModePerm)
	tempFilePath := filepath.Join(h.cfg.TempDir, taskID+"_"+filename)

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

	mimeType := header.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	async := c.PostForm("async") == "true"
	overwrite := c.PostForm("overwrite") == "true"

	if async {
		origin := getRequestOrigin(c)
		
		var fileSize int64
		if stat, err := os.Stat(tempFilePath); err == nil {
			fileSize = stat.Size()
		}
		tgclient.UpdateTaskWithFile(taskID, "processing", 0, "uploading_to_telegram", filename, username, fileSize, 0)
		
		go func() {
			defer os.Remove(tempFilePath)
			fileID, finalName, err := tgclient.ProcessCompleteUploadSync(context.Background(), tempFilePath, filename, dbPath, mimeType, taskID, h.cfg, overwrite, username)
			if err == nil {
				if shareMode == "public" || shareMode == "folder" {
					h.publicShareItem(origin, fileID, shareMode, dbPath, username, make(gin.H))
				}
				tgclient.UpdateTaskWithFileID(taskID, "done", 100, "", fileID, finalName, username)
			} else {
				tgclient.UpdateTask(taskID, "error", 0, "upload_failed: "+err.Error(), username)
			}
		}()

		c.JSON(http.StatusOK, gin.H{
			"status":   "processing",
			"task_id":  taskID,
			"filename": filename,
			"path":     path,
		})
		return
	}

	defer os.Remove(tempFilePath)
	fileID, finalName, err := tgclient.ProcessCompleteUploadSync(c.Request.Context(), tempFilePath, filename, dbPath, mimeType, taskID, h.cfg, overwrite, username)
	if err != nil {
		tgclient.UpdateTask(taskID, "error", 0, "upload_failed: "+err.Error(), username)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Upload failed: " + err.Error()})
		return
	}

	resp := gin.H{
		"status":   "done",
		"filename": finalName,
		"path":     path,
		"file_id":  fileID,
	}

	if shareMode == "public" || shareMode == "folder" {
		h.publicShareItem(getRequestOrigin(c), fileID, shareMode, dbPath, username, resp)
	}

	tgclient.UpdateTaskWithFileID(taskID, "done", 100, "", fileID, finalName, username)
	c.JSON(http.StatusOK, resp)
}

type PublicRemoteUploadRequest struct {
	URL       string      `json:"url"`
	Path      string      `json:"path"`
	Share     interface{} `json:"share"`
	Overwrite bool        `json:"overwrite"`
	Async     bool        `json:"async"`
}

func (h *Handler) handlePublicRemoteUploadAPI(c *gin.Context) {
	username, isAdmin, err := h.authenticatePublicAPI(c)
	if err != nil {
		if strings.Contains(err.Error(), "Authorization") || strings.Contains(err.Error(), "key") {
			c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		} else if strings.Contains(err.Error(), "disabled") || strings.Contains(err.Error(), "required") {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}

	var req PublicRemoteUploadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	if req.URL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url is required"})
		return
	}

	parsedURL, parseErr := url.ParseRequestURI(req.URL)
	if parseErr != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_url"})
		return
	}

	if utils.IsPrivateIP(req.URL) {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden_url"})
		return
	}

	dbPath := mapPath(req.Path, username, isAdmin)
	if isAdmin && isChildAccountPath(dbPath) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Admin cannot upload to child account directory"})
		return
	}

	taskID := uuid.New().String()
	shareMode := ""
	if s, ok := req.Share.(string); ok {
		shareMode = s
	} else if b, ok := req.Share.(bool); ok && b {
		shareMode = "public"
	}

	if req.Async {
		origin := getRequestOrigin(c)
		
		// Initialize task in memory
		filename := filepath.Base(req.URL)
		if idx := strings.Index(filename, "?"); idx != -1 {
			filename = filename[:idx]
		}
		tgclient.UpdateTaskWithFile(taskID, "processing", 0, "remote_download_queued", filename, username, 0, 0)

		go func() {
			fileID, finalName, err := tgclient.ProcessRemoteUploadSync(context.Background(), req.URL, dbPath, taskID, h.cfg, req.Overwrite, username)
			if err == nil {
				if shareMode == "public" || shareMode == "folder" {
					h.publicShareItem(origin, fileID, shareMode, dbPath, username, make(gin.H))
				}
				tgclient.UpdateTaskWithFileID(taskID, "done", 100, "", fileID, finalName, username)
			} else {
				tgclient.UpdateTask(taskID, "error", 0, "upload_failed: "+err.Error(), username)
			}
		}()
		c.JSON(http.StatusOK, gin.H{
			"status":  "processing",
			"task_id": taskID,
		})
		return
	}

	fileID, finalName, err := tgclient.ProcessRemoteUploadSync(c.Request.Context(), req.URL, dbPath, taskID, h.cfg, req.Overwrite, username)
	if err != nil {
		tgclient.UpdateTask(taskID, "error", 0, "upload_failed: "+err.Error(), username)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Remote upload failed: " + err.Error()})
		return
	}

	resp := gin.H{
		"status":   "done",
		"filename": finalName,
		"path":     req.Path,
		"file_id":  fileID,
	}

	if shareMode == "public" || shareMode == "folder" {
		h.publicShareItem(getRequestOrigin(c), fileID, shareMode, dbPath, username, resp)
	}

	tgclient.UpdateTaskWithFileID(taskID, "done", 100, "", fileID, finalName, username)
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) handleGetPublicTaskStatusAPI(c *gin.Context) {
	username, _, err := h.authenticatePublicAPI(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	taskID := c.Param("task_id")
	task := tgclient.GetTask(taskID)
	if task == nil || task.Owner != username {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}

	// Enrich if done
	if task.Status == "done" && task.FileID != 0 {
		var file database.File
		err := database.RODB.Get(&file, "SELECT * FROM files WHERE id = ?", task.FileID)
		if err == nil {
			resp := gin.H{
				"status":   "done",
				"file_id":  task.FileID,
				"filename": file.Filename,
				"path":     file.Path,
			}

			if file.ShareToken != nil {
				origin := getRequestOrigin(c)
				resp["share_token"] = *file.ShareToken
				resp["share_link"] = origin + "/s/" + *file.ShareToken
				if !file.IsFolder {
					resp["direct_link"] = origin + "/dl/" + utils.GenerateDirectToken(*file.ShareToken)
				}
			}
			c.JSON(http.StatusOK, resp)
			return
		}
	}

	c.JSON(http.StatusOK, task)
}

func (h *Handler) handleDeletePublicTaskAPI(c *gin.Context) {
	username, _, err := h.authenticatePublicAPI(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	taskID := c.Param("task_id")
	if tgclient.CancelTask(taskID, username) {
		c.JSON(http.StatusOK, gin.H{"status": "cancelled"})
	} else {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found or already completed"})
	}
}

func (h *Handler) publicShareItem(origin string, fileID int64, shareMode, dbPath, username string, resp gin.H) {
	targetID := fileID
	actualShareMode := shareMode

	if shareMode == "folder" {
		userHome := "/" + username
		// Prevent sharing system root or user home folder automatically
		if dbPath != "/" && dbPath != userHome && dbPath != "." && dbPath != "" {
			var parent struct {
				ID int64 `db:"id"`
			}
			parentPath := filepath.Dir(dbPath)
			parentName := filepath.Base(dbPath)
			err := database.RODB.Get(&parent, "SELECT id FROM files WHERE path = ? AND filename = ? AND is_folder = 1 AND owner = ?", parentPath, parentName, username)
			if err == nil {
				targetID = parent.ID
			} else {
				actualShareMode = "public"
			}
		} else {
			actualShareMode = "public"
		}
	}

	shareToken := uuid.New().String()
	_, err := database.DB.Exec("UPDATE files SET share_token = ? WHERE id = ?", shareToken, targetID)
	if err != nil {
		fmt.Printf("[PublicAPI] Failed to update share token: %v\n", err)
		return
	}

	resp["share_token"] = shareToken
	resp["share_link"] = origin + "/s/" + shareToken
	if actualShareMode != "folder" {
		resp["direct_link"] = origin + "/dl/" + utils.GenerateDirectToken(shareToken)
	}
}

type PublicShareRequest struct {
	Path string `json:"path"`
}

func (h *Handler) handlePublicShareAPI(c *gin.Context) {
	username, isAdmin, err := h.authenticatePublicAPI(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	var req PublicShareRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	if req.Path == "" || req.Path == "/" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid path"})
		return
	}

	fullPath := mapPath(req.Path, username, isAdmin)
	dbPath := filepath.Dir(fullPath)
	filename := filepath.Base(fullPath)

	var item struct {
		ID       int64 `db:"id"`
		IsFolder bool  `db:"is_folder"`
	}
	err = database.RODB.Get(&item, "SELECT id, is_folder FROM files WHERE path = ? AND filename = ? AND owner = ?", dbPath, filename, username)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Item not found"})
		return
	}

	shareMode := "public"
	if item.IsFolder {
		shareMode = "folder"
	}

	resp := gin.H{"status": "done"}
	h.publicShareItem(getRequestOrigin(c), item.ID, shareMode, dbPath, username, resp)
	c.JSON(http.StatusOK, resp)
}
