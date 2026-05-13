package tgclient

import (
	"context"
	"database/sql"
	"fmt"

	"crypto/tls"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"sync"
	"time"

	"telecloud/config"
	"telecloud/database"
	"telecloud/utils"
	"telecloud/ws"

	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/html"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
)

var (
	UploadTasks = make(map[string]*UploadStatus)
	TaskCancels = make(map[string]context.CancelFunc)
	taskMutex   sync.Mutex

	// Limit concurrent uploads to Telegram to prevent floodwait
	uploadSemaphore         chan struct{}
	globalDownloadSemaphore chan struct{}
	
	// Stagger mechanism to prevent bursts
	lastUploadStart time.Time
	startMu         sync.Mutex
)

func staggerUpload(ctx context.Context) {
	startMu.Lock()
	defer startMu.Unlock()
	
	elapsed := time.Since(lastUploadStart)
	if elapsed < 500*time.Millisecond {
		wait := 500*time.Millisecond - elapsed
		select {
		case <-time.After(wait):
		case <-ctx.Done():
		}
	}
	lastUploadStart = time.Now()
}

func InitUploader(cfg *config.Config) {
	uploadCount := 4
	if botCount := GetBotCount(); botCount > 0 {
		uploadCount = 4 * (botCount + 1)
	}
	uploadSemaphore = make(chan struct{}, uploadCount)
	
	// Set concurrent download limit to 2 for a more comfortable experience
	globalDownloadSemaphore = make(chan struct{}, 2)
}

type UploadStatus struct {
	Status        string  `json:"status"`
	Percent       int     `json:"percent"`
	Phase         string  `json:"phase,omitempty"`
	Progress      float64 `json:"progress"`
	UploadedBytes int64   `json:"uploaded"`
	Size          int64   `json:"total"`
	Speed         int64   `json:"speed,omitempty"`
	ETA           int     `json:"eta,omitempty"`
	Filename      string  `json:"filename,omitempty"`
	Owner         string  `json:"owner,omitempty"`
	FileID        int64   `json:"file_id,omitempty"`
	Message       string  `json:"message,omitempty"`

	// Legacy support for web UI
	OldSize          int64 `json:"size,omitempty"`
	OldUploadedBytes int64 `json:"uploaded_bytes,omitempty"`

	startTime time.Time
}

func UpdateTask(taskID string, status string, percent int, msg string, owner string) {
	UpdateTaskWithSpeed(taskID, status, percent, msg, "", owner, 0, 0, 0)
}

func UpdateTaskWithSize(taskID string, status string, percent int, msg string, size int64, uploaded int64, owner string) {
	UpdateTaskWithSpeed(taskID, status, percent, msg, "", owner, size, uploaded, 0)
}

func UpdateTaskWithSpeed(taskID string, status string, percent int, msg string, filename string, owner string, size int64, uploaded int64, speed int64) {
	UpdateTaskWithFile(taskID, status, percent, msg, filename, owner, size, uploaded, speed)
}

func UpdateTaskWithFileID(taskID string, status string, percent int, msg string, fileID int64, filename string, owner string) {
	taskMutex.Lock()
	defer taskMutex.Unlock()
	if existing, ok := UploadTasks[taskID]; ok {
		existing.Status = status
		existing.Percent = percent
		existing.Message = msg
		existing.FileID = fileID
		if filename != "" {
			existing.Filename = filename
		}
	} else {
		UploadTasks[taskID] = &UploadStatus{
			Status:   status,
			Percent:  percent,
			Message:  msg,
			FileID:   fileID,
			Filename: filename,
			Owner:    owner,
		}
	}

	// Notify frontend about the update
	if s, ok := UploadTasks[taskID]; ok {
		ws.BroadcastTaskUpdate(s.Owner, taskID, s.Status, s.Percent, s.Message, s.Filename, s.Size, s.UploadedBytes, s.Speed, s.ETA)
	}

	// Auto-cleanup: remove task from memory once terminal
	if status == "done" || status == "error" || status == "cancelled" {
		go func() {
			time.Sleep(1 * time.Hour)
			taskMutex.Lock()
			delete(UploadTasks, taskID)
			taskMutex.Unlock()
		}()
	}
}

// Keep this for compatibility but update internally
func UpdateTaskWithFile(taskID string, status string, percent int, msg string, filename string, owner string, size int64, uploaded int64, manualSpeed ...int64) {
	taskMutex.Lock()
	defer taskMutex.Unlock()

	var finalSpeed int64
	if len(manualSpeed) > 0 {
		finalSpeed = manualSpeed[0]
	}

	var finalFilename string
	var finalOwner string
	if existing, ok := UploadTasks[taskID]; ok {
		// Prevent terminal statuses (done, cancelled) from being overwritten
		// by late-arriving updates. We allow 'error' to be overwritten so that
		// manual retries from the UI can still show progress correctly.
		if (existing.Status == "done" || existing.Status == "cancelled") &&
			(status != "done" && status != "error" && status != "cancelled") {
			return
		}
		finalFilename = filename
		if filename == "" {
			finalFilename = existing.Filename
		}
		finalOwner = owner
		if owner == "" {
			finalOwner = existing.Owner
		}
	} else {
		finalFilename = filename
		if filename == "" {
			finalFilename = "File"
		}
		finalOwner = owner
	}

	var fs int64
	var fu int64
	var st time.Time

	if existing, ok := UploadTasks[taskID]; ok {
		fs = size
		if size <= 0 {
			fs = existing.Size
		}
		fu = uploaded
		if uploaded <= 0 {
			fu = existing.UploadedBytes
		}
		st = existing.startTime
	} else {
		fs = size
		fu = uploaded
		st = time.Now()
	}

	if st.IsZero() {
		st = time.Now()
	}

	var speed int64
	var eta int
	if finalSpeed > 0 {
		speed = finalSpeed
	} else {
		duration := time.Since(st).Seconds()
		if duration > 1 && fu > 0 {
			speed = int64(float64(fu) / duration)
		}
	}

	if speed > 0 && fs > fu {
		eta = int(float64(fs-fu) / float64(speed))
	}

	phase := status
	switch status {
	case "telegram":
		phase = "telegram_upload"
	case "downloading":
		phase = "remote_download"
	case "uploading_to_server":
		phase = "server_upload"
	}

	progress := float64(percent)
	if fs > 0 {
		progress = (float64(fu) / float64(fs)) * 100
	}

	statusObj := &UploadStatus{
		Status:           status,
		Phase:            phase,
		Percent:          percent,
		Progress:         progress,
		Message:          msg,
		Filename:         finalFilename,
		Owner:            finalOwner,
		Size:             fs,
		UploadedBytes:    fu,
		OldSize:          fs,
		OldUploadedBytes: fu,
		Speed:            speed,
		ETA:              eta,
		startTime:        st,
	}

	if existing, ok := UploadTasks[taskID]; ok {
		statusObj.FileID = existing.FileID
		if filename == "" {
			statusObj.Filename = existing.Filename
		}
		if fs == 0 {
			statusObj.Size = existing.Size
			statusObj.OldSize = existing.OldSize
		}
		if fu == 0 {
			statusObj.UploadedBytes = existing.UploadedBytes
			statusObj.OldUploadedBytes = existing.OldUploadedBytes
			statusObj.Progress = existing.Progress
		}
	}

	UploadTasks[taskID] = statusObj
	ws.BroadcastTaskUpdate(finalOwner, taskID, status, percent, msg, statusObj.Filename, fs, fu, speed, eta)

	// Auto-cleanup: remove task from memory once terminal
	if status == "done" || status == "error" || status == "cancelled" {
		go func() {
			time.Sleep(1 * time.Hour)
			taskMutex.Lock()
			delete(UploadTasks, taskID)
			taskMutex.Unlock()
		}()
	}
}

func GetTask(taskID string) *UploadStatus {
	taskMutex.Lock()
	defer taskMutex.Unlock()
	if t, ok := UploadTasks[taskID]; ok {
		return t
	}
	return nil
}

func CancelTask(taskID string, username string) bool {
	taskMutex.Lock()

	// Verify owner from memory
	status, ok := UploadTasks[taskID]
	if ok && status.Owner != username {
		taskMutex.Unlock()
		return false
	}

	// If not in memory, verify owner from database (for chunked uploads still in progress)
	if !ok {
		var dbOwner string
		err := database.RODB.Get(&dbOwner, "SELECT owner FROM upload_tasks WHERE id = ?", taskID)
		if err == nil && dbOwner != username {
			taskMutex.Unlock()
			return false
		}
	}

	if cancel, ok := TaskCancels[taskID]; ok {
		cancel()
		delete(TaskCancels, taskID)
	}
	taskMutex.Unlock()

	// Call UpdateTask in a separate goroutine to avoid deadlock
	go UpdateTask(taskID, "cancelled", 0, "", username)
	return true
}

type uploadProgress struct {
	taskID       string
	totalSize    int64
	previousSize int64
	owner        string
}

func (p uploadProgress) Chunk(ctx context.Context, state uploader.ProgressState) error {
	currentUploaded := p.previousSize + state.Uploaded
	percent := 0
	if p.totalSize > 0 {
		percent = int(float64(currentUploaded) / float64(p.totalSize) * 100)
	}
	UpdateTaskWithSize(p.taskID, "telegram", percent, "", p.totalSize, currentUploaded, p.owner)
	return nil
}

type maxSizeReader struct {
	r       io.Reader
	maxSize int64
	read    int64
}

func (m *maxSizeReader) Read(p []byte) (n int, err error) {
	n, err = m.r.Read(p)
	m.read += int64(n)
	if m.maxSize > 0 && m.read > m.maxSize {
		return n, fmt.Errorf("file_too_large")
	}
	return n, err
}

func ProcessCompleteUpload(ctx context.Context, filePath, filename, path, mimeType, taskID string, cfg *config.Config, overwrite bool, owner string) {
	ctx, cancel := context.WithCancel(ctx)
	taskMutex.Lock()
	TaskCancels[taskID] = cancel
	taskMutex.Unlock()

	defer func() {
		taskMutex.Lock()
		delete(TaskCancels, taskID)
		taskMutex.Unlock()
		cancel()
	}()

	stat, err := os.Stat(filePath)
	var fileSize int64
	if err == nil {
		fileSize = stat.Size()
	}

	database.EnsureFoldersExist(path, owner)

	UpdateTaskWithFile(taskID, "telegram", 0, "waiting_slot", filename, owner, fileSize, 0)

	// Wait for a slot in the upload queue
	select {
	case uploadSemaphore <- struct{}{}:
		staggerUpload(ctx)
		defer func() { <-uploadSemaphore }()
	case <-ctx.Done():
		UpdateTaskWithFile(taskID, "error", 0, "upload_cancelled_waiting", filename, owner, fileSize, 0)
		return
	}

	UpdateTaskWithFile(taskID, "telegram", 0, "", filename, owner, fileSize, 0)

	// Handle overwriting: identify and remove old record before inserting new one to avoid UNIQUE constraint conflict
	var existingID int
	var existingMsgID *int
	var existingThumb *string
	var msgIDsToDelete []int
	if overwrite {
		database.RODB.QueryRow("SELECT id, message_id, thumb_path FROM files WHERE path = ? AND filename = ? AND is_folder = 0 AND owner = ?", path, filename, owner).Scan(&existingID, &existingMsgID, &existingThumb)
		if existingID > 0 {
			// Identify messages to delete from Telegram BEFORE deleting the DB record
			msgIDsToDelete, _ = database.GetOrphanedMessages([]int{existingID})
			database.DB.Exec("DELETE FROM files WHERE id = ?", existingID)
		}
	}

	uniqueFilename := filename
	if !overwrite || existingID == 0 {
		uniqueFilename = database.GetUniqueFilename(database.RODB, path, filename, false, 0, owner)
	}

	// Create the main file record first so we have an ID for parts
	var fileID int64
	var dbErr error
	for i := 0; i < 5; i++ {
		var res sql.Result
		res, dbErr = database.DB.Exec(
			"INSERT INTO files (filename, path, size, mime_type, is_folder, owner) VALUES (?, ?, ?, ?, 0, ?)",
			uniqueFilename, path, fileSize, mimeType, owner,
		)
		if dbErr == nil {
			fileID, err = res.LastInsertId()
			if err != nil {
				dbErr = err
				continue
			}
			break
		}
		// If it still fails, fallback to unique name
		uniqueFilename = database.GetUniqueFilename(database.RODB, path, filename, false, 0, owner)
		time.Sleep(100 * time.Millisecond)
	}

	if dbErr != nil {
		UpdateTask(taskID, "error", 0, "err_db_error: "+dbErr.Error(), "")
		return
	}

	success := false
	var uploadedMsgIDs []int
	defer func() {
		if !success {
			if len(uploadedMsgIDs) > 0 {
				go DeleteMessages(context.Background(), cfg, uploadedMsgIDs)
			}
			database.DB.Exec("DELETE FROM files WHERE id = ?", fileID)
		}
	}()

	numParts := int((fileSize + cfg.MaxPartSize - 1) / cfg.MaxPartSize)
	if numParts == 0 {
		numParts = 1
	}

	f, err := os.Open(filePath)
	if err != nil {
		UpdateTask(taskID, "error", 0, "err_open_file: "+err.Error(), "")
		return
	}
	defer f.Close()

	var firstMsgID int
	for i := 0; i < numParts; i++ {
		start := int64(i) * cfg.MaxPartSize
		end := start + cfg.MaxPartSize
		if end > fileSize {
			end = fileSize
		}
		partSize := end - start

		sectionReader := io.NewSectionReader(f, start, partSize)

		partFilename := uniqueFilename
		if numParts > 1 {
			partFilename = fmt.Sprintf("%s.part%d", uniqueFilename, i+1)
		}

		UpdateTask(taskID, "telegram", int(float64(i)/float64(numParts)*100), fmt.Sprintf("uploading_part_%d_of_%d", i+1, numParts), "")

		var msgID int
		var uploadErr error
		for attempt := 1; attempt <= 15; attempt++ {
			if ctx.Err() != nil {
				uploadErr = ctx.Err()
				break
			}
			// Fresh API client per attempt to rotate through the bot pool on failure.
			currentApi := GetAPI()
			
			if attempt > 1 {
				UpdateTask(taskID, "telegram", int(float64(i)/float64(numParts)*100), fmt.Sprintf("retrying_part_%d_attempt_%d", i+1, attempt), "")
				
				// Wait with increasing backoff, but return early if context is canceled
				select {
				case <-ctx.Done():
					uploadErr = ctx.Err()
					break
				case <-time.After(time.Duration(attempt*2) * time.Second):
				}
				
				if ctx.Err() != nil {
					break
				}
				if _, err := sectionReader.Seek(0, io.SeekStart); err != nil {
					log.Printf("[Upload] Failed to seek section reader: %v", err)
				}
			}
			// Fresh uploader per attempt: avoids reusing a session that
			// Telegram may have already invalidated after a partial upload.
			freshUp := uploader.NewUploader(currentApi).
				WithPartSize(uploader.MaximumPartSize).
				WithProgress(uploadProgress{taskID: taskID, totalSize: fileSize, previousSize: start, owner: owner}).
				WithThreads(cfg.UploadThreads)
			msgID, uploadErr = uploadFilePart(ctx, currentApi, freshUp, sectionReader, partFilename, mimeType, uniqueFilename, cfg, partSize)
			if uploadErr == nil {
				break
			}
			log.Printf("[Upload] Task %s part %d attempt %d failed: %v", taskID, i+1, attempt, uploadErr)
		}

		if uploadErr != nil {
			if ctx.Err() != nil {
				return // Intentionally canceled by user, don't overwrite "cancelled" state
			}
			UpdateTask(taskID, "error", 0, "upload_part_failed: "+uploadErr.Error(), "")
			return
		}
		uploadedMsgIDs = append(uploadedMsgIDs, msgID)

		if i == 0 {
			firstMsgID = msgID
		}

		// Insert part record
		_, err = database.DB.Exec(
			"INSERT INTO file_parts (file_id, message_id, part_index, size) VALUES (?, ?, ?, ?)",
			fileID, msgID, i, partSize,
		)
		if err != nil {
			UpdateTask(taskID, "error", 0, "err_db_part_insert: "+err.Error(), "")
			return
		}
	}

	// Update main file record with the first message ID for backward compatibility/previews
	database.DB.Exec("UPDATE files SET message_id = ? WHERE id = ?", firstMsgID, fileID)

	// Overwrite cleanup (Telegram messages and old thumbnail)
	if overwrite && existingID > 0 {
		if len(msgIDsToDelete) > 0 {
			go DeleteMessages(context.Background(), cfg, msgIDsToDelete)
		}

		if existingThumb != nil {
			var count int
			database.RODB.Get(&count, "SELECT COUNT(*) FROM files WHERE thumb_path = ?", *existingThumb)
			if count == 0 {
				os.Remove(*existingThumb)
			}
		}
	}

	// Generate thumbnail from temp file (still exists at this point) and update DB
	localThumb := utils.CreateLocalThumbnail(filePath, mimeType, cfg.FFMPEGPath)
	if localThumb != nil {
		database.DB.Exec("UPDATE files SET thumb_path = ? WHERE message_id = ? AND path = ? AND filename = ? AND owner = ?", *localThumb, firstMsgID, path, uniqueFilename, owner)
	}

	// Signal done to user after thumbnail is ready
	UpdateTaskWithFileID(taskID, "done", 100, "", fileID, uniqueFilename, owner)
	success = true

	// Cooldown before releasing the semaphore slot
	select {
	case <-time.After(1000 * time.Millisecond):
	case <-ctx.Done():
	}
}

func ProcessRemoteUpload(ctx context.Context, url, path, taskID string, cfg *config.Config, overwrite bool, owner string) {
	filename := filepath.Base(url)
	if idx := strings.Index(filename, "?"); idx != -1 {
		filename = filename[:idx]
	}

	ctx, cancel := context.WithCancel(ctx)
	taskMutex.Lock()
	TaskCancels[taskID] = cancel
	taskMutex.Unlock()

	defer func() {
		taskMutex.Lock()
		delete(TaskCancels, taskID)
		taskMutex.Unlock()
		cancel()
	}()

	// SSRF Protection (Check before waiting in queue)
	if utils.IsPrivateIP(url) {
		UpdateTaskWithFile(taskID, "error", 0, "err_forbidden_url", filename, owner, 0, 0)
		return
	}

	UpdateTaskWithFile(taskID, "waiting_slot", 0, "waiting_slot", filename, owner, 0, 0)

	// 1. Wait for a slot in the global download queue (HTTP download limit)
	select {
	case globalDownloadSemaphore <- struct{}{}:
		defer func() { <-globalDownloadSemaphore }()
	case <-ctx.Done():
		UpdateTaskWithFile(taskID, "error", 0, "upload_cancelled_waiting", "", owner, 0, 0)
		return
	}

	database.EnsureFoldersExist(path, owner)
	UpdateTaskWithFile(taskID, "downloading", 0, "initiating_request", filename, owner, 0, 0)

	// 2. Get the file stream
	defaultDialer := &net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	fallbackResolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return defaultDialer.DialContext(ctx, "udp", "1.1.1.1:53")
		},
	}
	fallbackDialer := &net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
		Resolver:  fallbackResolver,
	}

	client := &http.Client{
		Timeout: 0, // No timeout for overall download, context handles cancellation
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				conn, err := defaultDialer.DialContext(ctx, network, addr)
				if err != nil {
					// Fallback to Cloudflare DNS if system resolver fails (very common on Termux)
					return fallbackDialer.DialContext(ctx, network, addr)
				}
				return conn, nil
			},
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
			ExpectContinueTimeout: 1 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		},
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		UpdateTaskWithFile(taskID, "error", 0, "request_creation_failed: "+err.Error(), "", owner, 0, 0)
		return
	}

	// Add User-Agent to avoid being blocked by some servers
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		UpdateTaskWithFile(taskID, "error", 0, "connection_failed: "+err.Error(), "", owner, 0, 0)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg := "err_remote_failed"
		if resp.StatusCode == http.StatusNotFound {
			msg = "err_remote_not_found"
		} else if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
			msg = "err_remote_forbidden"
		} else if resp.StatusCode >= 500 {
			msg = "err_remote_server_error"
		}
		UpdateTask(taskID, "error", 0, msg, "")
		return
	}

	size := resp.ContentLength
	// Multi-part remote upload allows any size

	// Determine filename from final URL after redirects
	filename = filepath.Base(resp.Request.URL.Path)
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			if f, ok := params["filename"]; ok {
				filename = f
			}
		}
	}
	// Clean filename
	filename = filepath.Base(filename)
	if filename == "" || filename == "." || filename == "/" {
		filename = "downloaded_file"
	}

	mimeType := resp.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	rangeSupport := resp.Header.Get("Accept-Ranges") == "bytes"
	rangeNote := ""
	if !rangeSupport {
		rangeNote = " [No Resume Support]"
	}

	// Guess extension if missing
	if filepath.Ext(filename) == "" && mimeType != "application/octet-stream" {
		exts, _ := mime.ExtensionsByType(mimeType)
		if len(exts) > 0 {
			// exts[0] includes the dot, e.g., ".jpg"
			filename += exts[0]
		}
	}

	UpdateTaskWithFile(taskID, "telegram", 0, "waiting_slot"+rangeNote, filename, owner, size, 0)

	// Wait for a slot in the upload queue
	select {
	case uploadSemaphore <- struct{}{}:
		staggerUpload(ctx)
		defer func() { <-uploadSemaphore }()
	case <-ctx.Done():
		UpdateTaskWithFile(taskID, "error", 0, "upload_cancelled_waiting", filename, owner, size, 0)
		return
	}

	UpdateTaskWithFile(taskID, "telegram", 0, rangeNote, filename, owner, size, 0)

	// Handle overwriting: identify and remove old record before inserting new one to avoid UNIQUE constraint conflict
	var existingID int
	var existingMsgID *int
	var existingThumb *string
	var msgIDsToDelete []int
	if overwrite {
		database.RODB.QueryRow("SELECT id, message_id, thumb_path FROM files WHERE path = ? AND filename = ? AND is_folder = 0 AND owner = ?", path, filename, owner).Scan(&existingID, &existingMsgID, &existingThumb)
		if existingID > 0 {
			// Identify messages to delete from Telegram BEFORE deleting the DB record
			msgIDsToDelete, _ = database.GetOrphanedMessages([]int{existingID})
			database.DB.Exec("DELETE FROM files WHERE id = ?", existingID)
		}
	}

	uniqueFilename := filename
	if !overwrite || existingID == 0 {
		uniqueFilename = database.GetUniqueFilename(database.RODB, path, filename, false, 0, owner)
	}


	// Create main record first
	var fileID int64
	var dbErr error
	for i := 0; i < 5; i++ {
		var res sql.Result
		res, dbErr = database.DB.Exec(
			"INSERT INTO files (filename, path, size, mime_type, is_folder, owner) VALUES (?, ?, ?, ?, 0, ?)",
			uniqueFilename, path, size, mimeType, owner,
		)
		if dbErr == nil {
			fileID, err = res.LastInsertId()
			if err != nil {
				dbErr = err
				continue
			}
			break
		}
		uniqueFilename = database.GetUniqueFilename(database.RODB, path, filename, false, 0, owner)
		time.Sleep(100 * time.Millisecond)
	}

	if dbErr != nil {
		UpdateTask(taskID, "error", 0, "err_db_error: "+dbErr.Error(), "")
		return
	}

	success := false
	var uploadedMsgIDs []int
	defer func() {
		if !success {
			if len(uploadedMsgIDs) > 0 {
				go DeleteMessages(context.Background(), cfg, uploadedMsgIDs)
			}
			database.DB.Exec("DELETE FROM files WHERE id = ?", fileID)
		}
	}()

	// Allow unlimited file size for remote uploads since we split it
	var bodyReader io.ReadCloser = resp.Body
	defer func() {
		if bodyReader != nil {
			bodyReader.Close()
		}
	}()

	partIndex := 0
	totalUploaded := int64(0)
	var firstMsgID int
	var lastPartSize int64

	for {
		partFilename := uniqueFilename
		if size > cfg.MaxPartSize || size == -1 {
			partFilename = fmt.Sprintf("%s.part%d", uniqueFilename, partIndex+1)
		}

		var msgID int
		var uploadErr error
		
		// Determine max attempts: reduce if no range support and deep into the file
		maxAttempts := 3
		if !rangeSupport && totalUploaded > 0 {
			maxAttempts = 2 // Only 1 retry if we have to discard data manually
		}
		
		// Retry loop for each part
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			if ctx.Err() != nil {
				return // Intentionally canceled
			}

			// If this is a retry, we need to re-open the body and skip to the current offset
			if attempt > 1 {
				if bodyReader != nil {
					bodyReader.Close()
				}
				
				// Re-connect to source with Range header
				newReq, _ := http.NewRequestWithContext(ctx, "GET", resp.Request.URL.String(), nil)
				newReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
				newReq.Header.Set("Range", fmt.Sprintf("bytes=%d-", totalUploaded))
				
				newResp, err := client.Do(newReq)
				if err != nil {
					uploadErr = err
					time.Sleep(time.Duration(attempt) * time.Second)
					continue
				}
				
				if newResp.StatusCode == http.StatusOK || newResp.StatusCode == http.StatusPartialContent {
					bodyReader = newResp.Body
					if newResp.StatusCode == http.StatusOK && totalUploaded > 0 {
						// Source doesn't support Range, must discard prefix manually
						io.CopyN(io.Discard, bodyReader, totalUploaded)
					}
				} else {
					newResp.Body.Close()
					uploadErr = fmt.Errorf("remote server status %d on retry", newResp.StatusCode)
					time.Sleep(time.Duration(attempt) * time.Second)
					continue
				}
			}

			// Wrap current stream part
			pr := &utils.CountingReader{R: io.LimitReader(bodyReader, cfg.MaxPartSize)}
			currentApi := GetAPI()
			up := uploader.NewUploader(currentApi).
				WithPartSize(uploader.MaximumPartSize).
				WithProgress(uploadProgress{taskID: taskID, totalSize: size, previousSize: totalUploaded, owner: owner}).
				WithThreads(cfg.UploadThreads)

			msgID, uploadErr = uploadFilePart(ctx, currentApi, up, pr, partFilename, mimeType, uniqueFilename, cfg, -1)
			
			if uploadErr == nil {
				// Successfully uploaded this part
				lastPartSize = pr.N
				totalUploaded += lastPartSize
				uploadedMsgIDs = append(uploadedMsgIDs, msgID)
				if partIndex == 0 {
					firstMsgID = msgID
				}

				// Insert part record
				_, err = database.DB.Exec(
					"INSERT INTO file_parts (file_id, message_id, part_index, size) VALUES (?, ?, ?, ?)",
					fileID, msgID, partIndex, lastPartSize,
				)
				if err != nil {
					UpdateTask(taskID, "error", 0, "err_db_part_insert: "+err.Error(), "")
					return
				}
				break // Success, break retry loop
			}

			log.Printf("[RemoteUpload] Part %d attempt %d failed: %v", partIndex+1, attempt, uploadErr)
			time.Sleep(time.Duration(attempt) * time.Second)
		}

		if uploadErr != nil {
			UpdateTask(taskID, "error", 0, "upload_part_failed: "+uploadErr.Error(), "")
			return
		}

		partIndex++

		// Check if we finished
		if size > 0 && totalUploaded >= size {
			break
		}
		if size <= 0 && lastPartSize < cfg.MaxPartSize {
			break
		}
	}

	// Update main file record
	database.DB.Exec("UPDATE files SET message_id = ?, size = ? WHERE id = ?", firstMsgID, totalUploaded, fileID)

	// Overwrite cleanup (Telegram messages and old thumbnail)
	if overwrite && existingID > 0 {
		if len(msgIDsToDelete) > 0 {
			DeleteMessages(context.Background(), cfg, msgIDsToDelete)
		}

		if existingThumb != nil {
			var count int
			database.RODB.Get(&count, "SELECT COUNT(*) FROM files WHERE thumb_path = ?", *existingThumb)
			if count == 0 {
				os.Remove(*existingThumb)
			}
		}
	}

	// Note: Remote uploads usually don't have a local file for thumbnail generation 
	// unless we download it first. Since this is a streaming upload, we skip local thumb for now.
	// But we signal done so the UI refreshes.
	UpdateTaskWithFileID(taskID, "done", 100, "", fileID, uniqueFilename, owner)
	success = true
}


// ProcessCompleteUploadSync is the synchronous version for the Upload API.
func ProcessCompleteUploadSync(ctx context.Context, filePath, filename, path, mimeType, taskID string, cfg *config.Config, overwrite bool, owner string) (fileID int64, finalName string, err error) {
	if taskID != "" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		taskMutex.Lock()
		TaskCancels[taskID] = cancel
		taskMutex.Unlock()
		defer func() {
			taskMutex.Lock()
			delete(TaskCancels, taskID)
			taskMutex.Unlock()
			cancel()
		}()
	}

	database.EnsureFoldersExist(path, owner)

	// Wait for a slot in the upload queue
	select {
	case uploadSemaphore <- struct{}{}:
		staggerUpload(ctx)
		defer func() { <-uploadSemaphore }()
	case <-ctx.Done():
		return 0, "", fmt.Errorf("upload cancelled while waiting for queue")
	}

	// Handle overwriting: identify and remove old record before inserting new one to avoid UNIQUE constraint conflict
	var existingID int
	var existingMsgID *int
	var existingThumb *string
	var msgIDsToDelete []int
	if overwrite {
		database.RODB.QueryRow("SELECT id, message_id, thumb_path FROM files WHERE path = ? AND filename = ? AND is_folder = 0 AND owner = ?", path, filename, owner).Scan(&existingID, &existingMsgID, &existingThumb)
		if existingID > 0 {
			// Identify messages to delete from Telegram BEFORE deleting the DB record
			msgIDsToDelete, _ = database.GetOrphanedMessages([]int{existingID})
			database.DB.Exec("DELETE FROM files WHERE id = ?", existingID)
		}
	}

	uniqueFilename := filename
	if !overwrite || existingID == 0 {
		uniqueFilename = database.GetUniqueFilename(database.RODB, path, filename, false, 0, owner)
	}

	fileInfo, _ := os.Stat(filePath)
	var fileSize int64
	if fileInfo != nil {
		fileSize = fileInfo.Size()
	}


	// Create main record
	var dbErr error
	for i := 0; i < 5; i++ {
		var res sql.Result
		res, dbErr = database.DB.Exec(
			"INSERT INTO files (filename, path, size, mime_type, is_folder, owner) VALUES (?, ?, ?, ?, 0, ?)",
			uniqueFilename, path, fileSize, mimeType, owner,
		)
		if dbErr == nil {
			fileID, err = res.LastInsertId()
			if err != nil {
				dbErr = err
				continue
			}
			break
		}
		uniqueFilename = database.GetUniqueFilename(database.RODB, path, filename, false, 0, owner)
		time.Sleep(100 * time.Millisecond)
	}
	if dbErr != nil {
		return 0, "", fmt.Errorf("db insert: %w", dbErr)
	}

	success := false
	var uploadedMsgIDs []int
	defer func() {
		if !success {
			if len(uploadedMsgIDs) > 0 {
				go DeleteMessages(context.Background(), cfg, uploadedMsgIDs)
			}
			database.DB.Exec("DELETE FROM files WHERE id = ?", fileID)
		}
	}()

	numParts := int((fileSize + cfg.MaxPartSize - 1) / cfg.MaxPartSize)
	if numParts == 0 {
		numParts = 1
	}

	f, err := os.Open(filePath)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()

	var firstMsgID int
	for i := 0; i < numParts; i++ {
		start := int64(i) * cfg.MaxPartSize
		end := start + cfg.MaxPartSize
		if end > fileSize {
			end = fileSize
		}
		partSize := end - start

		sectionReader := io.NewSectionReader(f, start, partSize)

		partFilename := uniqueFilename
		if numParts > 1 {
			partFilename = fmt.Sprintf("%s.part%d", uniqueFilename, i+1)
		}

		var msgID int
		var uploadErr error
		for attempt := 1; attempt <= 15; attempt++ {
			if ctx.Err() != nil {
				uploadErr = ctx.Err()
				break
			}
			currentApi := GetAPI()
			if attempt > 1 {
				select {
				case <-ctx.Done():
					uploadErr = ctx.Err()
					break
				case <-time.After(time.Duration(attempt*2) * time.Second):
				}
				if ctx.Err() != nil {
					break
				}
				_, _ = sectionReader.Seek(0, io.SeekStart)
			}
			// Fresh uploader per attempt to avoid stale session state.
			freshUp := uploader.NewUploader(currentApi).
				WithPartSize(uploader.MaximumPartSize).
				WithThreads(cfg.UploadThreads)
			msgID, uploadErr = uploadFilePart(ctx, currentApi, freshUp, sectionReader, partFilename, mimeType, uniqueFilename, cfg, partSize)
			if uploadErr == nil {
				break
			}
			log.Printf("[UploadSync] Part %d attempt %d failed: %v", i+1, attempt, uploadErr)
		}

		if uploadErr != nil {
			return 0, "", fmt.Errorf("upload part %d (15 attempts): %w", i+1, uploadErr)
		}
		uploadedMsgIDs = append(uploadedMsgIDs, msgID)

		if i == 0 {
			firstMsgID = msgID
		}

		_, err = database.DB.Exec(
			"INSERT INTO file_parts (file_id, message_id, part_index, size) VALUES (?, ?, ?, ?)",
			fileID, msgID, i, partSize,
		)
		if err != nil {
			return 0, "", fmt.Errorf("db part insert %d: %w", i+1, err)
		}
	}

	// Update main file record
	database.DB.Exec("UPDATE files SET message_id = ? WHERE id = ?", firstMsgID, fileID)

	// Clean up old file if overwriting (Telegram messages and old thumbnail)
	if overwrite && existingID > 0 {
		if len(msgIDsToDelete) > 0 {
			DeleteMessages(context.Background(), cfg, msgIDsToDelete)
		}

		if existingThumb != nil {
			var count int
			database.RODB.Get(&count, "SELECT COUNT(*) FROM files WHERE thumb_path = ?", *existingThumb)
			if count == 0 {
				os.Remove(*existingThumb)
			}
		}
	}
	success = true
	
	// Generate thumbnail and update DB
	localThumb := utils.CreateLocalThumbnail(filePath, mimeType, cfg.FFMPEGPath)
	if localThumb != nil {
		database.DB.Exec("UPDATE files SET thumb_path = ? WHERE id = ?", *localThumb, fileID)
	}

	return fileID, uniqueFilename, nil
}

func DeleteMessages(ctx context.Context, cfg *config.Config, msgIDs []int) error {
	if len(msgIDs) == 0 {
		return nil
	}
	api := Client.API()
	peer, err := resolveLogGroup(ctx, api, cfg.LogGroupID)
	if err != nil {
		return err
	}

	if channel, ok := peer.(*tg.InputPeerChannel); ok {
		_, err = api.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: channel.ChannelID, AccessHash: channel.AccessHash},
			ID:      msgIDs,
		})
		return err
	}

	_, err = api.MessagesDeleteMessages(ctx, &tg.MessagesDeleteMessagesRequest{
		Revoke: true,
		ID:     msgIDs,
	})
	return err
}
func GetActiveTasks(username string) map[string]*UploadStatus {
	taskMutex.Lock()
	defer taskMutex.Unlock()

	tasks := make(map[string]*UploadStatus)
	for id, status := range UploadTasks {
		if status.Owner == username {
			tasks[id] = status
		}
	}
	return tasks
}

func uploadFilePart(ctx context.Context, api *tg.Client, up *uploader.Uploader, r io.Reader, filename, mimeType, caption string, cfg *config.Config, size int64) (int, error) {
	u := uploader.NewUpload(filename, r, size)
	file, err := up.Upload(ctx, u)
	if err != nil {
		return 0, err
	}

	sender := message.NewSender(api)
	peer, err := resolveLogGroup(ctx, api, cfg.LogGroupID)
	if err != nil {
		return 0, err
	}

	displayInfo := caption
	if displayInfo == "" {
		displayInfo = filename
	}

	finalCaption := "<b>📄 File:</b> " + displayInfo + "\n\n<b>🚀 Powered by TeleCloud Go</b>\n<i>Unlimited Cloud Storage via Telegram</i>\n\n🔗 <a href=\"https://github.com/dabeecao/telecloud-go\">GitHub Repository</a>"

	docBuilder := message.UploadedDocument(file, html.String(nil, finalCaption)).
		Filename(filename).
		MIME(mimeType)

	res, err := sender.To(peer).Media(ctx, docBuilder)
	if err != nil {
		return 0, err
	}

	var msgID int
	if updReq, ok := res.(*tg.Updates); ok {
		for _, u := range updReq.Updates {
			if m, ok := u.(*tg.UpdateNewMessage); ok {
				if msg, ok := m.Message.(*tg.Message); ok {
					msgID = msg.ID
					break
				}
			} else if m, ok := u.(*tg.UpdateNewChannelMessage); ok {
				if msg, ok := m.Message.(*tg.Message); ok {
					msgID = msg.ID
					break
				}
			}
		}
	}
	if msgID <= 0 {
		return 0, fmt.Errorf("could not get message ID")
	}
	return msgID, nil
}

func ProcessRemoteUploadSync(ctx context.Context, url, path, taskID string, cfg *config.Config, overwrite bool, owner string) (int64, string, error) {
	if taskID != "" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		taskMutex.Lock()
		TaskCancels[taskID] = cancel
		taskMutex.Unlock()
		defer func() {
			taskMutex.Lock()
			delete(TaskCancels, taskID)
			taskMutex.Unlock()
			cancel()
		}()
	}

	filename := filepath.Base(url)
	if idx := strings.Index(filename, "?"); idx != -1 {
		filename = filename[:idx]
	}

	UpdateTaskWithFile(taskID, "waiting_slot", 0, "waiting_slot", filename, owner, 0, 0)

	// 1. Wait for a slot in the remote upload queue (HTTP download limit)
	select {
	case globalDownloadSemaphore <- struct{}{}:
		defer func() { <-globalDownloadSemaphore }()
	case <-ctx.Done():
		return 0, "", fmt.Errorf("cancelled while waiting for remote slot")
	}

	client := &http.Client{
		Timeout: 0,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("remote server returned status %d", resp.StatusCode)
	}

	size := resp.ContentLength
	filename = filepath.Base(resp.Request.URL.Path)
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			if f, ok := params["filename"]; ok {
				filename = f
			}
		}
	}
	filename = filepath.Base(filename)
	if filename == "" || filename == "." || filename == "/" {
		filename = "downloaded_file"
	}

	mimeType := resp.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	rangeSupport := resp.Header.Get("Accept-Ranges") == "bytes"
	rangeNote := ""
	if !rangeSupport {
		rangeNote = " [No Resume Support]"
	}

	if filepath.Ext(filename) == "" && mimeType != "application/octet-stream" {
		exts, _ := mime.ExtensionsByType(mimeType)
		if len(exts) > 0 {
			filename += exts[0]
		}
	}

	database.EnsureFoldersExist(path, owner)
	if taskID != "" {
		UpdateTask(taskID, "telegram", 0, "waiting_slot"+rangeNote, "")
	}

	// Wait for a slot in the upload queue
	select {
	case uploadSemaphore <- struct{}{}:
		staggerUpload(ctx)
		defer func() { <-uploadSemaphore }()
	case <-ctx.Done():
		return 0, "", fmt.Errorf("cancelled while waiting for upload slot")
	}

	// Handle overwriting: identify and remove old record before inserting new one to avoid UNIQUE constraint conflict
	var existingID int
	var existingMsgID *int
	var existingThumb *string
	var msgIDsToDelete []int
	if overwrite {
		database.RODB.QueryRow("SELECT id, message_id, thumb_path FROM files WHERE path = ? AND filename = ? AND is_folder = 0 AND owner = ?", path, filename, owner).Scan(&existingID, &existingMsgID, &existingThumb)
		if existingID > 0 {
			// Identify messages to delete from Telegram BEFORE deleting the DB record
			msgIDsToDelete, _ = database.GetOrphanedMessages([]int{existingID})
			database.DB.Exec("DELETE FROM files WHERE id = ?", existingID)
		}
	}

	uniqueFilename := filename
	if !overwrite || existingID == 0 {
		uniqueFilename = database.GetUniqueFilename(database.RODB, path, filename, false, 0, owner)
	}


	// Create main record
	var fileID int64
	var dbErr error
	for i := 0; i < 5; i++ {
		var res sql.Result
		res, dbErr = database.DB.Exec(
			"INSERT INTO files (filename, path, size, mime_type, is_folder, owner) VALUES (?, ?, ?, ?, 0, ?)",
			uniqueFilename, path, size, mimeType, owner,
		)
		if dbErr == nil {
			fileID, _ = res.LastInsertId()
			break
		}
		uniqueFilename = database.GetUniqueFilename(database.RODB, path, filename, false, 0, owner)
		time.Sleep(100 * time.Millisecond)
	}

	if dbErr != nil {
		return 0, "", dbErr
	}

	success := false
	var uploadedMsgIDs []int
	defer func() {
		if !success {
			if len(uploadedMsgIDs) > 0 {
				go DeleteMessages(context.Background(), cfg, uploadedMsgIDs)
			}
			database.DB.Exec("DELETE FROM files WHERE id = ?", fileID)
		}
	}()

	var bodyReader io.ReadCloser = resp.Body
	defer func() {
		if bodyReader != nil {
			bodyReader.Close()
		}
	}()

	partIndex := 0
	totalUploaded := int64(0)
	var firstMsgID int
	var lastPartSize int64

	for {
		partFilename := uniqueFilename
		if size > cfg.MaxPartSize || size == -1 {
			partFilename = fmt.Sprintf("%s.part%d", uniqueFilename, partIndex+1)
		}

		UpdateTask(taskID, "telegram", 0, fmt.Sprintf("uploading_part_%d", partIndex+1), "")

		var msgID int
		var uploadErr error
		
		// Determine max attempts: reduce if no range support and deep into the file
		maxAttempts := 3
		if !rangeSupport && totalUploaded > 0 {
			maxAttempts = 2
		}
		
		// Retry loop for each part
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			if ctx.Err() != nil {
				return 0, "", ctx.Err()
			}

			if attempt > 1 {
				if bodyReader != nil {
					bodyReader.Close()
				}
				
				// Re-connect to source with Range header
				newReq, _ := http.NewRequestWithContext(ctx, "GET", resp.Request.URL.String(), nil)
				newReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
				newReq.Header.Set("Range", fmt.Sprintf("bytes=%d-", totalUploaded))
				
				newResp, err := client.Do(newReq)
				if err != nil {
					uploadErr = err
					time.Sleep(time.Duration(attempt) * time.Second)
					continue
				}
				
				if newResp.StatusCode == http.StatusOK || newResp.StatusCode == http.StatusPartialContent {
					bodyReader = newResp.Body
					if newResp.StatusCode == http.StatusOK && totalUploaded > 0 {
						// Source doesn't support Range, must discard prefix manually
						io.CopyN(io.Discard, bodyReader, totalUploaded)
					}
				} else {
					newResp.Body.Close()
					uploadErr = fmt.Errorf("remote server status %d on retry", newResp.StatusCode)
					time.Sleep(time.Duration(attempt) * time.Second)
					continue
				}
			}

			// Wrap current stream part
			pr := &utils.CountingReader{R: io.LimitReader(bodyReader, cfg.MaxPartSize)}
			currentApi := GetAPI()
			up := uploader.NewUploader(currentApi).
				WithPartSize(uploader.MaximumPartSize).
				WithProgress(uploadProgress{taskID: taskID, totalSize: size, previousSize: totalUploaded, owner: owner}).
				WithThreads(cfg.UploadThreads)

			msgID, uploadErr = uploadFilePart(ctx, currentApi, up, pr, partFilename, mimeType, uniqueFilename, cfg, -1)
			
			if uploadErr == nil {
				// Successfully uploaded this part
				lastPartSize = pr.N
				totalUploaded += lastPartSize
				uploadedMsgIDs = append(uploadedMsgIDs, msgID)
				if partIndex == 0 {
					firstMsgID = msgID
				}

				// Insert part record
				_, err = database.DB.Exec(
					"INSERT INTO file_parts (file_id, message_id, part_index, size) VALUES (?, ?, ?, ?)",
					fileID, msgID, partIndex, lastPartSize,
				)
				if err != nil {
					return 0, "", fmt.Errorf("err_db_part_insert: %w", err)
				}
				break // Success, break retry loop
			}

			log.Printf("[RemoteUploadSync] Part %d attempt %d failed: %v", partIndex+1, attempt, uploadErr)
			time.Sleep(time.Duration(attempt) * time.Second)
		}

		if uploadErr != nil {
			return 0, "", fmt.Errorf("upload_part_failed: %w", uploadErr)
		}

		partIndex++

		// Check if we finished
		if size > 0 && totalUploaded >= size {
			break
		}
		if size <= 0 && lastPartSize < cfg.MaxPartSize {
			break
		}
	}

	database.DB.Exec("UPDATE files SET message_id = ?, size = ? WHERE id = ?", firstMsgID, totalUploaded, fileID)

	// Overwrite cleanup (Telegram messages)
	if overwrite && existingID > 0 {
		if len(msgIDsToDelete) > 0 {
			DeleteMessages(context.Background(), cfg, msgIDsToDelete)
		}
	}

	success = true
	return fileID, uniqueFilename, nil
}
