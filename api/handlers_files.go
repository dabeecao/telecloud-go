package api

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"telecloud/database"
	"telecloud/tgclient"
	"telecloud/utils"
	"telecloud/ws"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

var errUploadTaskForbidden = errors.New("upload task belongs to another user")

// processCompleteUpload is swapped out in tests.
var processCompleteUpload = tgclient.ProcessCompleteUpload

var chunkBufPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 64*1024)
		return &buf
	},
}

func (h *Handler) handleGetIndex(c *gin.Context) {
	token, _ := c.Cookie("session_token")
	sessionUsername := database.LookupSessionUser(token)
	if sessionUsername == "" {
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
		database.RODB.Get(&userStorageUsed, "SELECT COALESCE(SUM(size), 0) FROM files WHERE is_folder = FALSE AND message_id IS NOT NULL AND deleted_at IS NULL")
	} else {
		prefix := "/" + sessionUsername
		database.RODB.Get(&userStorageUsed, "SELECT COALESCE(SUM(size), 0) FROM files WHERE (path = ? OR path LIKE ?) AND is_folder = FALSE AND message_id IS NOT NULL AND deleted_at IS NULL", prefix, prefix+"/%")
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

	botStatuses := tgclient.GetBotStatuses(h.cfg)
	botStatusesJSON, _ := json.Marshal(botStatuses)
	botStatusesStr := string(botStatusesJSON)

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
		"log_group_id":          h.cfg.LogGroupID,
		"bot_tokens":            database.GetSetting("bot_tokens"),
		"bot_statuses":          botStatusesStr,
		"bot_admin_ids":         database.GetSetting("bot_admin_ids"),
		"telegram_user_id":      database.GetUserSetting(sessionUsername, "telegram_user_id"),
		"bot_pool_upload_folder": func() string {
			f := database.GetUserSetting(sessionUsername, "bot_pool_upload_folder")
			if f == "" {
				return "TelegramUpload"
			}
			return f
		}(),
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
	query := "SELECT * FROM files WHERE path = ? AND owner = ? AND deleted_at IS NULL AND (is_folder = TRUE OR message_id IS NOT NULL) ORDER BY is_folder DESC, id DESC"
	args := []interface{}{dbPath, username}
	if isAdmin {
		if dbPath == "/" {
			query = "SELECT * FROM files WHERE path = ? AND deleted_at IS NULL AND (is_folder = TRUE OR message_id IS NOT NULL) ORDER BY is_folder DESC, id DESC"
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
			query = "SELECT * FROM files WHERE path = ? AND owner = ? AND deleted_at IS NULL AND (is_folder = TRUE OR message_id IS NOT NULL) ORDER BY is_folder DESC, id DESC"
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
		decorateFileForResponse(&files[i], username, isAdmin)
	}
	var storageUsed int64
	if isAdmin {
		database.RODB.Get(&storageUsed, "SELECT COALESCE(SUM(size), 0) FROM files WHERE is_folder = FALSE AND message_id IS NOT NULL AND owner = ?", username)
	} else {
		prefix := "/" + username
		database.RODB.Get(&storageUsed, "SELECT COALESCE(SUM(size), 0) FROM files WHERE (path = ? OR path LIKE ?) AND is_folder = FALSE AND message_id IS NOT NULL", prefix, prefix+"/%")
	}

	c.JSON(http.StatusOK, gin.H{
		"files":        files,
		"storage_used": storageUsed,
	})
}

// decorateFileForResponse fills the display-only fields of a file row: the
// user-facing path, the direct token, thumbnail presence, and whether a share
// password is set. It is shared by every endpoint that returns file rows.
func decorateFileForResponse(f *database.File, username string, isAdmin bool) {
	f.Path = unmapPath(f.Path, username, isAdmin)
	if f.ShareToken != nil && !f.IsFolder {
		f.DirectToken = utils.GenerateDirectToken(*f.ShareToken)
	}
	if f.ThumbPath != nil {
		if _, err := os.Stat(*f.ThumbPath); err == nil {
			f.HasThumb = true
		}
	}
	if f.SharePassword != nil && *f.SharePassword != "" {
		f.HasSharePassword = true
	}
}

// handleGetFile returns one file row in the same shape as the listing, so a
// client can read its share_token without listing the whole folder.
func (h *Handler) handleGetFile(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_id"})
		return
	}
	username := c.GetString("username")
	isAdmin := c.GetBool("is_admin")

	var item database.File
	if err := database.RODB.Get(&item, "SELECT * FROM files WHERE id = ? AND owner = ? AND deleted_at IS NULL AND (is_folder = TRUE OR message_id IS NOT NULL)", id, username); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	decorateFileForResponse(&item, username, isAdmin)
	c.JSON(http.StatusOK, item)
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
	_, err := database.DB.Exec("INSERT INTO files (filename, path, is_folder, owner) VALUES (?, ?, TRUE, ?)", uniqueName, dbPath, username)
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
	if filename == "." || filename == ".." || filename == "/" || filename == string(filepath.Separator) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_filename"})
		return
	}
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

	chunkSize, err := strconv.ParseInt(c.PostForm("chunk_size"), 10, 64)
	if err != nil || chunkSize <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_chunk_size"})
		return
	}
	totalSize, err := strconv.ParseInt(c.PostForm("total_size"), 10, 64)
	if err != nil || totalSize < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_total_size"})
		return
	}
	// The chunk grid must describe the file exactly: every chunk but the last
	// is chunkSize bytes and the last one is non-empty. Otherwise offsets and
	// the Truncate size disagree and the assembled file is corrupt.
	expectedChunks := int64(1)
	if totalSize > 0 {
		expectedChunks = (totalSize + chunkSize - 1) / chunkSize
	}
	if expectedChunks != int64(totalChunks) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "chunk_layout_mismatch"})
		return
	}
	offset := int64(chunkIndex) * chunkSize
	expectedLen := chunkSize
	if remaining := totalSize - offset; remaining < expectedLen {
		expectedLen = remaining
	}
	if header.Size >= 0 && header.Size != expectedLen {
		c.JSON(http.StatusBadRequest, gin.H{"error": "chunk_size_mismatch"})
		return
	}

	tempDir := h.cfg.TempDir
	if err := os.MkdirAll(tempDir, os.ModePerm); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_open_temp_file"})
		return
	}
	tempFilePath := filepath.Join(tempDir, taskID+"_"+filename)
	rel, err := filepath.Rel(tempDir, tempFilePath)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_path"})
		return
	}

	// A chunk for a task that was already handed to Telegram or cancelled is a
	// retry whose response got lost, or a straggler after cancel. It must not
	// start a new task (fresh temp file, resume rows nobody cleans up).
	if v, ok := closedTasks.Load(taskID); ok {
		if ct := v.(*closedTask); ct.owner == username {
			if ct.cancelled {
				c.JSON(http.StatusConflict, gin.H{"error": "upload_cancelled"})
			} else {
				c.JSON(http.StatusOK, gin.H{"status": "processing_telegram", "message": "pushing_to_tg"})
			}
			return
		}
	}

	var state *chunkState
	for attempt := 0; ; attempt++ {
		val, _ := chunkTrackerSync.LoadOrStore(taskID, &chunkState{received: make(map[int]bool)})
		state = val.(*chunkState)

		// Initialise exactly once per in-memory task. Concurrent first chunks used
		// to race here: whoever lost LoadOrStore skipped the DB resync and wrote
		// into a file the winner was about to Truncate.
		state.initOnce.Do(func() {
			var task struct {
				Owner string `db:"owner"`
			}
			dbErr := database.RODB.Get(&task, "SELECT owner FROM upload_tasks WHERE id = ?", taskID)
			if dbErr == nil && task.Owner != username {
				// Record the real owner so the owner's own concurrent request
				// can tell it lost this race and initialise a fresh state.
				state.owner = task.Owner
				state.initErr = errUploadTaskForbidden
				return
			}
			tgclient.ResetTerminalTask(taskID, username)
			state.owner = username
			state.filename = filename
			state.totalSize = totalSize
			state.chunkSize = chunkSize
			state.totalChunks = totalChunks

			resumed := false
			if dbErr == nil {
				var dbChunks []int
				if err := database.RODB.Select(&dbChunks, "SELECT chunk_index FROM upload_chunks WHERE task_id = ?", taskID); err == nil {
					for _, idx := range dbChunks {
						if idx >= 0 && idx < totalChunks {
							state.received[idx] = true
						}
					}
				}
				if fi, err := os.Stat(tempFilePath); err == nil && fi.Size() == totalSize {
					resumed = true
					state.dbCreated = true
				} else {
					// Resume rows without a matching temp file (cleanup sweep,
					// crash, different layout) would mark chunks as received that
					// are no longer on disk.
					state.received = make(map[int]bool)
					purgeUploadTaskRows(taskID)
				}
			}
			if !resumed {
				f, err := os.OpenFile(tempFilePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
				if err != nil {
					state.initErr = err
					return
				}
				err = f.Truncate(totalSize)
				f.Close()
				if err != nil {
					os.Remove(tempFilePath)
					state.initErr = err
				}
			}
		})

		state.Lock()
		lostRace := state.initErr == errUploadTaskForbidden && state.owner == username
		state.Unlock()
		if !lostRace || attempt > 0 {
			break
		}
		chunkTrackerSync.CompareAndDelete(taskID, state)
	}

	state.Lock()
	initErr := state.initErr
	ownerOK := state.owner == username
	layoutOK := state.filename == filename && state.totalSize == totalSize && state.chunkSize == chunkSize && state.totalChunks == totalChunks
	started, cancelled := state.uploadStarted, state.cancelled
	state.Unlock()

	if initErr != nil {
		chunkTrackerSync.CompareAndDelete(taskID, state)
		if initErr == errUploadTaskForbidden {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		log.Printf("UPLOAD ERROR: Failed to prepare temp file %s: %v", tempFilePath, initErr)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_open_temp_file"})
		return
	}
	if !ownerOK {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	if !layoutOK {
		c.JSON(http.StatusConflict, gin.H{"error": "chunk_layout_mismatch"})
		return
	}
	if cancelled {
		c.JSON(http.StatusConflict, gin.H{"error": "upload_cancelled"})
		return
	}
	if started {
		c.JSON(http.StatusOK, gin.H{"status": "processing_telegram", "message": "pushing_to_tg"})
		return
	}

	out, err := os.OpenFile(tempFilePath, os.O_WRONLY, 0644)
	if errors.Is(err, os.ErrNotExist) {
		// Temp file swept (24h cleanup) or removed by a cancel while this task
		// was still in memory: its received set no longer matches the disk.
		state.Lock()
		drained := state.closeDBLocked()
		state.Unlock()
		chunkTrackerSync.CompareAndDelete(taskID, state)
		purgeAfter(drained, taskID)
		c.JSON(http.StatusConflict, gin.H{"error": "upload_state_lost"})
		return
	}
	if err != nil {
		log.Printf("UPLOAD ERROR: Failed to open temp file %s: %v", tempFilePath, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_open_temp_file"})
		return
	}
	bufPtr := chunkBufPool.Get().(*[]byte)
	// OffsetWriter keeps the write inside this chunk's slot; a body longer
	// than declared is rejected instead of overwriting the next chunk.
	written, err := io.CopyBuffer(io.NewOffsetWriter(out, offset), io.LimitReader(file, expectedLen+1), *bufPtr)
	chunkBufPool.Put(bufPtr)
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		log.Printf("UPLOAD ERROR: Failed to write chunk to %s: %v", tempFilePath, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_write_chunk"})
		return
	}
	if written != expectedLen {
		c.JSON(http.StatusBadRequest, gin.H{"error": "chunk_size_mismatch"})
		return
	}

	overwriteFlag := c.PostForm("overwrite") == "true"

	state.Lock()
	if state.cancelled {
		state.Unlock()
		c.JSON(http.StatusConflict, gin.H{"error": "upload_cancelled"})
		return
	}
	if state.uploadStarted {
		state.Unlock()
		c.JSON(http.StatusOK, gin.H{"status": "processing_telegram", "message": "pushing_to_tg"})
		return
	}
	if !state.dbCreated {
		state.dbCreated = true
		state.enqueueDBLocked(func() {
			database.DB.Exec(database.InsertIgnoreSQL("upload_tasks", "id, filename, owner, overwrite", "?, ?, ?, ?"), taskID, filename, username, overwriteFlag)
		})
	}
	if !state.received[chunkIndex] {
		state.received[chunkIndex] = true
		idx := chunkIndex
		state.enqueueDBLocked(func() {
			database.DB.Exec(database.InsertIgnoreSQL("upload_chunks", "task_id, chunk_index", "?, ?"), taskID, idx)
		})
	}

	actualReceived := len(state.received)
	uploadedBytes := int64(actualReceived) * chunkSize
	if uploadedBytes > totalSize {
		uploadedBytes = totalSize
	}
	serverPercent := actualReceived * 100 / totalChunks

	isDone := actualReceived == totalChunks
	var drained <-chan struct{}
	processPath := tempFilePath
	if isDone {
		state.uploadStarted = true
		drained = state.closeDBLocked()
		// Move the assembled file out of the task's slot before handing it
		// off. A new run of the same file (same deterministic task_id, e.g.
		// after a page reload) would otherwise O_TRUNC the very file
		// ProcessCompleteUpload is still streaming to Telegram. The new name
		// keeps the "<taskID>_" prefix (cancel/sweep) and the extension
		// (thumbnail detection).
		handoff := filepath.Join(tempDir, taskID+"_tg"+strings.ReplaceAll(uuid.NewString(), "-", "")[:8]+"_"+filename)
		if err := os.Rename(tempFilePath, handoff); err == nil {
			processPath = handoff
		} else {
			log.Printf("UPLOAD WARN: could not move %s for hand-off: %v", tempFilePath, err)
		}
		markTaskClosed(taskID, &closedTask{owner: username})
		chunkTrackerSync.CompareAndDelete(taskID, state)
	}
	state.Unlock()

	if !isDone {
		tgclient.UpdateTaskWithFile(taskID, "uploading_to_server", serverPercent, "pushing_to_server", filename, username, totalSize, uploadedBytes)
		c.JSON(http.StatusOK, gin.H{"status": "chunk_received", "chunk": chunkIndex})
		return
	}

	tgclient.UpdateTaskWithFile(taskID, "uploading_to_server", 100, "", filename, username, totalSize, totalSize)

	mimeType := header.Header.Get("Content-Type")
	// Browsers often send wrong or generic MIME types; fallback using extension.
	if mimeType == "" || mimeType == "application/octet-stream" {
		if ext := filepath.Ext(filename); ext != "" {
			if detected := mime.TypeByExtension(ext); detected != "" {
				mimeType = detected
			}
		}
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	// Synchronous so a new run of this task_id cannot have its fresh resume
	// rows deleted by this purge afterwards.
	purgeAfter(drained, taskID)

	go func() {
		defer os.Remove(processPath)
		processCompleteUpload(context.Background(), processPath, filename, dbPath, mimeType, taskID, h.cfg, overwriteFlag, username)
	}()

	c.JSON(http.StatusOK, gin.H{"status": "processing_telegram", "message": "pushing_to_tg"})
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

	dbPath := mapPath(uPath, username, isAdmin)
	if isAdmin && isChildAccountPath(dbPath) {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	if dbPath != "/" {
		var folder database.File
		err := database.RODB.Get(&folder, "SELECT is_folder FROM files WHERE path = ? AND filename = ? AND is_folder = TRUE AND owner = ?", filepath.Dir(dbPath), filepath.Base(dbPath), username)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "folder_not_found"})
			return
		}
	}

	taskID := c.PostForm("task_id")
	if taskID == "" {
		taskID = uuid.New().String()
	}

	// Intercept Telegram links
	if isTelegramLink(remoteURL) {
		if tgclient.Client == nil || !tgclient.IsAuthorized() {
			c.JSON(http.StatusBadRequest, gin.H{"error": "err_userbot_not_authorized"})
			return
		}
		go tgclient.ProcessTelegramLinkUpload(context.Background(), remoteURL, dbPath, taskID, h.cfg, overwrite, username)
		c.JSON(http.StatusOK, gin.H{
			"status":  "processing",
			"task_id": taskID,
		})
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

	// Intercept Telegram links
	if isTelegramLink(remoteURL) {
		if tgclient.Client == nil || !tgclient.IsAuthorized() {
			c.JSON(http.StatusBadRequest, gin.H{"error": "err_userbot_not_authorized"})
			return
		}

		username, channelID, msgID, err := tgclient.ParseTelegramLink(remoteURL)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "err_invalid_telegram_link"})
			return
		}

		api := tgclient.Client.API()
		peer, err := tgclient.ResolveTelegramPeer(c.Request.Context(), api, username, channelID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "err_resolve_peer_failed: " + err.Error()})
			return
		}

		msg, err := tgclient.FetchTelegramMessage(c.Request.Context(), api, peer, msgID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "err_fetch_message_failed: " + err.Error()})
			return
		}

		mediaInfo, err := tgclient.ExtractTelegramMediaInfo(msg)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "err_no_media_found: " + err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"content_type":   mediaInfo.MimeType,
			"content_length": mediaInfo.Size,
			"filename":       mediaInfo.Filename,
			"range_support":  true, // Fake range support to skip confirmation on frontend
		})
		return
	}

	if utils.IsPrivateIP(remoteURL) {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden_url"})
		return
	}

	// SafeHTTPClient re-resolves and rejects private/loopback IPs at dial
	// time, defeating DNS-rebinding tricks the upfront IsPrivateIP check
	// can't catch on its own.
	client := utils.SafeHTTPClient(10 * time.Second)

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

	rangeSupport := resp.Header.Get("Accept-Ranges") == "bytes" || resp.StatusCode == http.StatusPartialContent

	c.JSON(http.StatusOK, gin.H{
		"content_type":   contentType,
		"content_length": contentLength,
		"filename":       filename,
		"range_support":  rangeSupport,
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

		// Mark first so a chunk arriving from now on is rejected instead of
		// starting a new task after the in-memory state is looked up below.
		marker := &closedTask{owner: username, cancelled: true}
		markTaskClosed(taskID, marker)

		var state *chunkState
		if val, ok := chunkTrackerSync.Load(taskID); ok {
			state = val.(*chunkState)
			state.Lock()
			foreign := state.owner != "" && state.owner != username
			state.Unlock()
			if foreign {
				closedTasks.CompareAndDelete(taskID, marker)
				c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
				return
			}
		}

		if !tgclient.CancelTask(taskID, username) {
			closedTasks.CompareAndDelete(taskID, marker)
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}

		// Stop accepting chunks before the temp file goes away, so an in-flight
		// chunk cannot recreate the task's resume rows after this purge.
		var drained <-chan struct{}
		if state != nil {
			state.Lock()
			state.cancelled = true
			drained = state.closeDBLocked()
			state.Unlock()
			chunkTrackerSync.CompareAndDelete(taskID, state)
		}

		// Cleanup files/folders starting with taskID_, ytdlp_taskID_, or torrent_taskID_
		patterns := []string{
			filepath.Join(h.cfg.TempDir, taskID+"_*"),
			filepath.Join(h.cfg.TempDir, "ytdlp_"+taskID+"_*"),
			filepath.Join(h.cfg.TempDir, "torrent_"+taskID),
		}

		for _, pattern := range patterns {
			matches, err := filepath.Glob(pattern)
			if err == nil {
				for _, m := range matches {
					os.RemoveAll(m)
				}
			}
		}

		purgeAfter(drained, taskID)
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

	unlock := database.AcquireFileInsertLock(username, req.Destination)
	defer unlock()

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
				_, err = tx.Exec("INSERT INTO files (filename, path, is_folder, owner) VALUES (?, ?, TRUE, ?)", uniqueName, req.Destination, username)
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
					newChildID, err := database.InsertAndGetID(tx,
						"INSERT INTO files (message_id, filename, path, size, mime_type, is_folder, thumb_path, owner) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
						child.MessageID, child.Filename, newChildPath, child.Size, child.MimeType, child.IsFolder, child.ThumbPath, username)
					if err != nil {
						c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
						return
					}

					if !child.IsFolder {
						_, err = tx.Exec("INSERT INTO file_parts (file_id, part_index, message_id, size) SELECT ?, part_index, message_id, size FROM file_parts WHERE file_id = ?", newChildID, child.ID)
						if err != nil {
							c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
							return
						}
					}
				}
			} else {
				if item.MessageID == nil {
					continue
				}
				newFileID, err := database.InsertAndGetID(tx,
					"INSERT INTO files (message_id, filename, path, size, mime_type, is_folder, thumb_path, owner) VALUES (?, ?, ?, ?, ?, FALSE, ?, ?)",
					item.MessageID, uniqueName, req.Destination, item.Size, item.MimeType, item.ThumbPath, username)
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					return
				}
				_, err = tx.Exec("INSERT INTO file_parts (file_id, part_index, message_id, size) SELECT ?, part_index, message_id, size FROM file_parts WHERE file_id = ?", newFileID, item.ID)
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

	now := time.Now()
	if item.IsFolder {
		oldPrefix := item.Path + "/" + item.Filename
		if item.Path == "/" {
			oldPrefix = "/" + item.Filename
		}
		database.DB.Exec("UPDATE files SET deleted_at = ? WHERE (path = ? OR path LIKE ?) AND owner = ? AND deleted_at IS NULL", now, oldPrefix, oldPrefix+"/%", item.Owner)
	}
	database.DB.Exec("UPDATE files SET deleted_at = ? WHERE id = ?", now, id)

	c.JSON(http.StatusOK, gin.H{"status": "moved_to_trash"})
}

func (h *Handler) handleGetTrashFiles(c *gin.Context) {
	username := c.GetString("username")
	var allFiles []database.File
	err := database.RODB.Select(&allFiles, "SELECT * FROM files WHERE owner = ? AND deleted_at IS NOT NULL ORDER BY deleted_at DESC", username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Filter to only show top-level deleted items (items whose parent folder is NOT deleted)
	deletedFolders := make(map[string]bool)
	for _, f := range allFiles {
		if f.IsFolder {
			fullPath := f.Path + "/" + f.Filename
			if f.Path == "/" {
				fullPath = "/" + f.Filename
			}
			deletedFolders[fullPath] = true
		}
	}

	var topLevelFiles []database.File
	for _, f := range allFiles {
		isSub := false
		if f.Path != "/" {
			parts := strings.Split(strings.TrimPrefix(f.Path, "/"), "/")
			curr := ""
			for _, part := range parts {
				if part == "" {
					continue
				}
				if curr == "" {
					curr = "/" + part
				} else {
					curr = curr + "/" + part
				}
				if deletedFolders[curr] {
					fFullPath := f.Path + "/" + f.Filename
					if f.Path == "/" {
						fFullPath = "/" + f.Filename
					}
					if fFullPath != curr {
						isSub = true
						break
					}
				}
			}
		}
		if !isSub {
			topLevelFiles = append(topLevelFiles, f)
		}
	}

	for i := range topLevelFiles {
		if topLevelFiles[i].ThumbPath != nil {
			if _, err := os.Stat(*topLevelFiles[i].ThumbPath); err == nil {
				topLevelFiles[i].HasThumb = true
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{"files": topLevelFiles})
}

func (h *Handler) handleRestoreFile(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_id"})
		return
	}

	username := c.GetString("username")
	tx, err := database.DB.Beginx()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db_error"})
		return
	}
	defer tx.Rollback()

	var item database.File
	if err := tx.Get(&item, "SELECT * FROM files WHERE id = ? AND owner = ?", id, username); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	if item.DeletedAt == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "item_not_in_trash"})
		return
	}

	// 1. Ensure parent folders exist
	if item.Path != "/" {
		if err := database.EnsureFoldersExistTx(tx, item.Path, item.Owner); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_recreate_parents"})
			return
		}
	}

	// 2. Handle name collision
	newFilename := database.GetUniqueFilename(tx, item.Path, item.Filename, item.IsFolder, item.ID, item.Owner)

	// 3. Restore item and children (if folder)
	if item.IsFolder {
		oldPrefix := item.Path + "/" + item.Filename
		if item.Path == "/" {
			oldPrefix = "/" + item.Filename
		}

		// Restore children that were deleted at the exact same time as the parent
		// This prevents restoring items that were deleted individually before the folder was deleted.
		if _, err := tx.Exec("UPDATE files SET deleted_at = NULL WHERE (path = ? OR path LIKE ?) AND owner = ? AND deleted_at = ?", oldPrefix, oldPrefix+"/%", item.Owner, item.DeletedAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "restore_children_failed"})
			return
		}
	}

	if _, err := tx.Exec("UPDATE files SET deleted_at = NULL, filename = ? WHERE id = ?", newFilename, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "restore_failed"})
		return
	}

	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "commit_failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "restored", "new_filename": newFilename})
}

func (h *Handler) handleEmptyTrash(c *gin.Context) {
	username := c.GetString("username")

	// Identify all items in trash for this user
	var items []database.File
	if err := database.RODB.Select(&items, "SELECT id, is_folder, thumb_path FROM files WHERE owner = ? AND deleted_at IS NOT NULL", username); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db_error"})
		return
	}

	if len(items) == 0 {
		c.JSON(http.StatusOK, gin.H{"status": "trash_already_empty"})
		return
	}

	var fileIDs []int
	for _, item := range items {
		fileIDs = append(fileIDs, item.ID)
	}

	// Identify messages to delete from Telegram
	msgIDsToDelete, _ := database.GetOrphanedMessages(fileIDs)

	// Delete thumbnails
	for _, item := range items {
		if item.ThumbPath != nil && *item.ThumbPath != "" {
			// Check if other files use the same thumb (unlikely for trash items, but safe)
			var count int
			database.RODB.Get(&count, "SELECT COUNT(*) FROM files WHERE thumb_path = ? AND id NOT IN ("+strings.Trim(strings.Join(strings.Fields(fmt.Sprint(fileIDs)), ","), "[]")+")", *item.ThumbPath)
			if count == 0 {
				os.Remove(*item.ThumbPath)
			}
		}
	}

	// Delete Telegram messages in background
	if len(msgIDsToDelete) > 0 {
		go tgclient.DeleteMessages(context.Background(), h.cfg, msgIDsToDelete)
	}

	// Delete from DB
	query, args, err := sqlx.In("DELETE FROM files WHERE id IN (?)", fileIDs)
	if err == nil {
		query = database.DB.Rebind(query)
		database.DB.Exec(query, args...)
	}

	c.JSON(http.StatusOK, gin.H{"status": "trash_emptied"})
}

func (h *Handler) handlePermanentDeleteFile(c *gin.Context) {
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

	// Delete thumbnails
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
	if item.ShareToken != nil {
		database.DB.Exec("DELETE FROM share_sessions WHERE share_token = ?", *item.ShareToken)
	}
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

	c.JSON(http.StatusOK, gin.H{"status": "permanently_deleted"})
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

	if val, ok := chunkTrackerSync.Load(taskID); ok {
		state := val.(*chunkState)
		state.Lock()
		chunks := make([]int, 0, len(state.received))
		if state.owner == username && !state.uploadStarted && !state.cancelled {
			for chIdx := range state.received {
				chunks = append(chunks, chIdx)
			}
		}
		state.Unlock()
		sort.Ints(chunks)
		c.JSON(http.StatusOK, gin.H{"chunks": chunks})
		return
	}

	// The client is starting a new run of a finished/cancelled task.
	if v, ok := closedTasks.Load(taskID); ok && v.(*closedTask).owner == username {
		closedTasks.CompareAndDelete(taskID, v)
		c.JSON(http.StatusOK, gin.H{"chunks": []int{}})
		return
	}

	var task struct {
		ID       string `db:"id"`
		Filename string `db:"filename"`
	}
	err := database.RODB.Get(&task, "SELECT id, filename FROM upload_tasks WHERE id = ? AND owner = ?", taskID, username)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"chunks": []int{}})
		return
	}
	// Chunks are only resumable while their temp file is still on disk.
	if strings.Contains(taskID, "..") || strings.ContainsAny(taskID, "/\\") {
		c.JSON(http.StatusOK, gin.H{"chunks": []int{}})
		return
	}
	if _, err := os.Stat(filepath.Join(h.cfg.TempDir, taskID+"_"+filepath.Base(task.Filename))); err != nil {
		purgeUploadTaskRows(taskID)
		c.JSON(http.StatusOK, gin.H{"chunks": []int{}})
		return
	}

	chunks := []int{}
	database.RODB.Select(&chunks, "SELECT chunk_index FROM upload_chunks WHERE task_id = ? ORDER BY chunk_index ASC", taskID)
	c.JSON(http.StatusOK, gin.H{"chunks": chunks})
}

func (h *Handler) handlePostCheckExists(c *gin.Context) {
	path := c.PostForm("path")
	filenamesStr := c.PostForm("filenames")
	if filenamesStr == "" {
		c.JSON(http.StatusOK, gin.H{"existing": []string{}})
		return
	}
	filenames := strings.Split(filenamesStr, "|")
	username := c.GetString("username")
	isAdmin := c.GetBool("is_admin")
	dbPath := mapPath(path, username, isAdmin)

	existing := make([]string, 0)
	for _, fn := range filenames {
		var count int
		// Mirror the visibility rule used by every listing: a row only counts as
		// an existing file once it references stored Telegram content. Without
		// these filters an in-flight upload (row inserted, no message_id yet) or
		// an item sitting in the trash would pop the collision dialog for a file
		// the user cannot open.
		err := database.RODB.Get(&count, "SELECT COUNT(*) FROM files WHERE path = ? AND filename = ? AND is_folder = FALSE AND owner = ? AND message_id IS NOT NULL AND deleted_at IS NULL", dbPath, fn, username)
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
	// Browsers often send wrong or generic MIME types; fallback using extension.
	if mimeType == "" || mimeType == "application/octet-stream" {
		if ext := filepath.Ext(filename); ext != "" {
			if detected := mime.TypeByExtension(ext); detected != "" {
				mimeType = detected
			}
		}
	}
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
	fileID, finalName, err := tgclient.ProcessCompleteUploadSync(context.WithoutCancel(c.Request.Context()), tempFilePath, filename, dbPath, mimeType, taskID, h.cfg, overwrite, username)
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

	fileID, finalName, err := tgclient.ProcessRemoteUploadSync(context.WithoutCancel(c.Request.Context()), req.URL, dbPath, taskID, h.cfg, req.Overwrite, username)
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
			err := database.RODB.Get(&parent, "SELECT id FROM files WHERE path = ? AND filename = ? AND is_folder = TRUE AND owner = ?", parentPath, parentName, username)
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
	_, err := database.DB.Exec("UPDATE files SET share_token = ?, share_password = NULL, share_views = 0, share_downloads = 0 WHERE id = ?", shareToken, targetID)
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

func (h *Handler) handleDownloadFolder(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_id"})
		return
	}
	var folder database.File
	username := c.GetString("username")
	if err := database.RODB.Get(&folder, "SELECT * FROM files WHERE id = ? AND owner = ? AND deleted_at IS NULL", id, username); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Folder not found"})
		return
	}
	if !folder.IsFolder {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Not a folder"})
		return
	}

	zipName := folder.Filename + ".zip"
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, zipName))
	c.Header("Content-Type", "application/zip")
	c.Header("X-Accel-Buffering", "no")
	c.SetCookie("dl_started", "1", 15, "/", "", false, false)

	folderPrefix := folder.Path + "/" + folder.Filename
	if folder.Path == "/" {
		folderPrefix = "/" + folder.Filename
	}

	var allItems []database.File
	err = database.RODB.Select(&allItems,
		"SELECT * FROM files WHERE (path = ? OR path LIKE ?) AND owner = ? AND deleted_at IS NULL AND (is_folder = TRUE OR message_id IS NOT NULL)",
		folderPrefix, folderPrefix+"/%", username)
	if err != nil {
		return
	}

	zw := zip.NewWriter(c.Writer)
	defer zw.Close()

	getZipPath := func(item database.File) string {
		itemFullPath := item.Path + "/" + item.Filename
		if item.Path == "/" {
			itemFullPath = "/" + item.Filename
		}
		parentPrefix := folder.Path
		if parentPrefix == "/" {
			return strings.TrimPrefix(itemFullPath, "/")
		}
		return strings.TrimPrefix(strings.TrimPrefix(itemFullPath, parentPrefix), "/")
	}

	for _, item := range allItems {
		zipPath := getZipPath(item)
		if item.IsFolder {
			if !strings.HasSuffix(zipPath, "/") {
				zipPath += "/"
			}
			_, _ = zw.Create(zipPath)
		} else {
			// Skip Deflate for already-compressed formats (video, images, audio,
			// archives): re-compressing wastes CPU and throttles stream throughput.
			method := zip.Deflate
			if isPrecompressed(item.Filename) {
				method = zip.Store
			}
			header := &zip.FileHeader{
				Name:   zipPath,
				Method: method,
			}
			header.Modified = item.CreatedAt

			writer, err := zw.CreateHeader(header)
			if err != nil {
				log.Printf("[FolderZip] Failed to create zip header for %q: %v", zipPath, err)
				continue
			}

			reader, err := tgclient.GetTelegramFileReader(c.Request.Context(), item, h.cfg)
			if err != nil {
				log.Printf("[FolderZip] Failed to open reader for %q (id=%d): %v", zipPath, item.ID, err)
				continue
			}

			_, copyErr := io.Copy(writer, reader)
			reader.Close()
			if copyErr != nil {
				// Mid-file failure corrupts the archive; abort instead of silently
				// producing a broken/truncated ZIP.
				log.Printf("[FolderZip] Stream error for %q (id=%d), aborting download: %v", zipPath, item.ID, copyErr)
				return
			}

			if flusher, ok := c.Writer.(http.Flusher); ok {
				flusher.Flush()
			}
		}
	}
}

// isPrecompressed reports whether a filename refers to a format that is already
// compressed, so ZIP Deflate would only waste CPU with no size gain.
func isPrecompressed(filename string) bool {
	switch strings.ToLower(filepath.Ext(filename)) {
	case // video
		".mp4", ".mkv", ".webm", ".avi", ".mov", ".wmv", ".flv", ".m4v", ".ts", ".mpg", ".mpeg", ".3gp",
		// audio
		".mp3", ".m4a", ".aac", ".ogg", ".opus", ".flac", ".wma",
		// images
		".jpg", ".jpeg", ".png", ".gif", ".webp", ".heic", ".heif", ".avif",
		// archives & compressed containers
		".zip", ".rar", ".7z", ".gz", ".bz2", ".xz", ".zst", ".cbz", ".cbr", ".epub", ".apk", ".jar", ".docx", ".xlsx", ".pptx":
		return true
	}
	return false
}

func (h *Handler) handleTempStreamFile(c *gin.Context) {
	token := c.Param("token")
	val, ok := tgclient.TempStreamTokens.Load(token)
	if !ok {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	info, ok := val.(tgclient.TempStreamInfo)
	if !ok {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	var item database.File
	if err := database.RODB.Get(&item, "SELECT * FROM files WHERE id = ?", info.FileID); err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if item.Owner != info.Username {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	if err := tgclient.ServeTelegramFile(c.Request, c.Writer, item, h.cfg); err != nil {
		log.Printf("[TempStream] ServeTelegramFile error: %v", err)
	}
}

func (h *Handler) handleRegenerateThumb(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	username := c.GetString("username")
	var item database.File
	if err := database.RODB.Get(&item, "SELECT id, is_folder FROM files WHERE id = ? AND owner = ?", id, username); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		return
	}

	if item.IsFolder {
		c.JSON(http.StatusBadRequest, gin.H{"error": "folders do not support covers"})
		return
	}

	newPath, err := tgclient.RegenerateFileThumbnail(c.Request.Context(), int64(id), h.cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	hasThumb := false
	if newPath != nil {
		if _, errStat := os.Stat(*newPath); errStat == nil {
			hasThumb = true
		}
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok", "has_thumb": hasThumb})
}

func isTelegramLink(link string) bool {
	link = strings.ToLower(link)
	if strings.HasPrefix(link, "tg://") {
		return true
	}
	if strings.Contains(link, "://") {
		parts := strings.SplitN(link, "://", 2)
		link = parts[1]
	}
	parts := strings.Split(link, "/")
	if len(parts) > 0 {
		host := parts[0]
		return host == "t.me" || host == "telegram.me" || host == "telegram.dog"
	}
	return false
}
