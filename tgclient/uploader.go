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
	"net/url"
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
	uploadSemaphore       chan struct{}
	remoteUploadSemaphore chan struct{}
)

func InitUploader(cfg *config.Config) {
	count := 3
	if botCount := GetBotCount(); botCount > 0 {
		count = 3 * (botCount + 1)
	}
	uploadSemaphore = make(chan struct{}, count)
	remoteUploadSemaphore = make(chan struct{}, count)
}

type UploadStatus struct {
	Status        string `json:"status"`
	Percent       int    `json:"percent"`
	Message       string `json:"message,omitempty"`
	Filename      string `json:"filename,omitempty"`
	Owner         string `json:"owner,omitempty"`
	Size          int64  `json:"size,omitempty"`
	UploadedBytes int64  `json:"uploaded_bytes,omitempty"`
}

func UpdateTask(taskID string, status string, percent int, msg string, owner string) {
	UpdateTaskWithFile(taskID, status, percent, msg, "", owner, 0, 0)
}

func UpdateTaskWithSize(taskID string, status string, percent int, msg string, size int64, uploaded int64, owner string) {
	UpdateTaskWithFile(taskID, status, percent, msg, "", owner, size, uploaded)
}

func UpdateTaskWithFile(taskID string, status string, percent int, msg string, filename string, owner string, size int64, uploaded int64) {
	taskMutex.Lock()
	defer taskMutex.Unlock()

	var finalFilename string
	var finalOwner string
	if existing, ok := UploadTasks[taskID]; ok {
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

	var finalSize int64
	if size > 0 {
		finalSize = size
	} else if existing, ok := UploadTasks[taskID]; ok {
		finalSize = existing.Size
	}

	var finalUploaded int64
	if uploaded > 0 {
		finalUploaded = uploaded
	} else if existing, ok := UploadTasks[taskID]; ok {
		finalUploaded = existing.UploadedBytes
	}

	UploadTasks[taskID] = &UploadStatus{
		Status:        status,
		Percent:       percent,
		Message:       msg,
		Filename:      finalFilename,
		Owner:        finalOwner,
		Size:          finalSize,
		UploadedBytes: finalUploaded,
	}
	ws.BroadcastTaskUpdate(finalOwner, taskID, status, percent, msg, finalSize, finalUploaded)

	// Auto-cleanup: remove task from memory once terminal
	if status == "done" || status == "error" {
		go func() {
			if status == "done" {
				time.Sleep(5 * time.Second)
			} else {
				time.Sleep(5 * time.Minute)
			}
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
	return &UploadStatus{Status: "pending", Percent: 0}
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
		err := database.DB.Get(&dbOwner, "SELECT owner FROM upload_tasks WHERE id = ?", taskID)
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
	go UpdateTask(taskID, "error", 0, "upload_cancelled_waiting", username)
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
		defer func() { <-uploadSemaphore }()
	case <-ctx.Done():
		UpdateTaskWithFile(taskID, "error", 0, "upload_cancelled_waiting", filename, owner, fileSize, 0)
		return
	}

	UpdateTaskWithFile(taskID, "telegram", 0, "", filename, owner, fileSize, 0)

	// Handle overwriting: single query instead of two
	var existingID int
	var existingMsgID *int
	var existingThumb *string
	if overwrite {
		database.DB.QueryRow("SELECT id, message_id, thumb_path FROM files WHERE path = ? AND filename = ? AND is_folder = 0 AND owner = ?", path, filename, owner).Scan(&existingID, &existingMsgID, &existingThumb)
	}

	uniqueFilename := filename
	if !overwrite || existingID == 0 {
		uniqueFilename = database.GetUniqueFilename(database.DB, path, filename, false, 0, owner)
	}

	api := GetAPI()

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
			fileID, _ = res.LastInsertId()
			break
		}
		uniqueFilename = database.GetUniqueFilename(database.DB, path, filename, false, 0, owner)
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

	// Prepare for upload
	up := uploader.NewUploader(api).
		WithPartSize(uploader.MaximumPartSize).
		WithProgress(uploadProgress{taskID: taskID, totalSize: fileSize, previousSize: 0, owner: owner}).
		WithThreads(cfg.UploadThreads)

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

		// Update progress state for current part
		up = up.WithProgress(uploadProgress{taskID: taskID, totalSize: fileSize, previousSize: start, owner: owner})

		var msgID int
		var uploadErr error
		for attempt := 1; attempt <= 3; attempt++ {
			if attempt > 1 {
				UpdateTask(taskID, "telegram", int(float64(i)/float64(numParts)*100), fmt.Sprintf("retrying_part_%d_attempt_%d", i+1, attempt), "")
				time.Sleep(5 * time.Second)
			}
			msgID, uploadErr = uploadFilePart(ctx, api, up, sectionReader, partFilename, mimeType, uniqueFilename, cfg, partSize)
			if uploadErr == nil {
				break
			}
			log.Printf("[Upload] Task %s part %d attempt %d failed: %v", taskID, i+1, attempt, uploadErr)
			
			// Reset section reader for retry
			_, _ = sectionReader.Seek(0, io.SeekStart)
		}

		if uploadErr != nil {
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

	// Overwrite cleanup
	if overwrite && existingID > 0 {
		// Delete parts of the old file
		var oldParts []int
		database.DB.Select(&oldParts, "SELECT message_id FROM file_parts WHERE file_id = ?", existingID)

		database.DB.Exec("DELETE FROM files WHERE id = ?", existingID)
		// file_parts should be deleted by CASCADE, but we still need to delete Telegram messages

		if len(oldParts) > 0 {
			DeleteMessages(context.Background(), cfg, oldParts)
		} else if existingMsgID != nil {
			DeleteMessages(context.Background(), cfg, []int{*existingMsgID})
		}

		if existingThumb != nil {
			var count int
			database.DB.Get(&count, "SELECT COUNT(*) FROM files WHERE thumb_path = ?", *existingThumb)
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
	UpdateTaskWithSize(taskID, "done", 100, "", fileSize, fileSize, owner)
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

	UpdateTaskWithFile(taskID, "downloading", 0, "initiating_request", filename, owner, 0, 0)

	// SSRF Protection
	if isPrivateIP(url) {
		UpdateTaskWithFile(taskID, "error", 0, "err_forbidden_url", "", owner, 0, 0)
		return
	}

	database.EnsureFoldersExist(path, owner)

	// 1. Wait for a slot in the remote upload queue (HTTP download limit)
	select {
	case remoteUploadSemaphore <- struct{}{}:
		defer func() { <-remoteUploadSemaphore }()
	case <-ctx.Done():
		UpdateTaskWithFile(taskID, "error", 0, "upload_cancelled_waiting", "", owner, 0, 0)
		return
	}

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

	// Guess extension if missing
	if filepath.Ext(filename) == "" && mimeType != "application/octet-stream" {
		exts, _ := mime.ExtensionsByType(mimeType)
		if len(exts) > 0 {
			// exts[0] includes the dot, e.g., ".jpg"
			filename += exts[0]
		}
	}

	UpdateTaskWithFile(taskID, "telegram", 0, "waiting_slot", filename, owner, size, 0)

	// Wait for a slot in the upload queue
	select {
	case uploadSemaphore <- struct{}{}:
		defer func() { <-uploadSemaphore }()
	case <-ctx.Done():
		UpdateTaskWithFile(taskID, "error", 0, "upload_cancelled_waiting", filename, owner, size, 0)
		return
	}

	UpdateTaskWithFile(taskID, "telegram", 0, "", filename, owner, size, 0)

	// Handle overwriting
	var existingID int
	var existingMsgID *int
	var existingThumb *string
	if overwrite {
		database.DB.QueryRow("SELECT id, message_id, thumb_path FROM files WHERE path = ? AND filename = ? AND is_folder = 0 AND owner = ?", path, filename, owner).Scan(&existingID, &existingMsgID, &existingThumb)
	}

	uniqueFilename := filename
	if !overwrite || existingID == 0 {
		uniqueFilename = database.GetUniqueFilename(database.DB, path, filename, false, 0, owner)
	}

	api := GetAPI()

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
			fileID, _ = res.LastInsertId()
			break
		}
		uniqueFilename = database.GetUniqueFilename(database.DB, path, filename, false, 0, owner)
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
	bodyReader := resp.Body
	
	up := uploader.NewUploader(api).
		WithPartSize(uploader.MaximumPartSize).
		WithProgress(uploadProgress{taskID: taskID, totalSize: size, previousSize: 0, owner: owner}).
		WithThreads(cfg.UploadThreads)

	partIndex := 0
	totalUploaded := int64(0)
	var firstMsgID int

	for {
		// Use a counting reader to know the exact size of this part
		pr := &utils.CountingReader{R: io.LimitReader(bodyReader, cfg.MaxPartSize)}
		
		// For remote streaming, we might not know the exact part size if it's the last part and size is unknown
		expectedPartSize := int64(0)
		if size > 0 {
			expectedPartSize = cfg.MaxPartSize
			remaining := size - totalUploaded
			if remaining < expectedPartSize {
				expectedPartSize = remaining
			}
		}

		partFilename := uniqueFilename
		if size > cfg.MaxPartSize || size == -1 {
			partFilename = fmt.Sprintf("%s.part%d", uniqueFilename, partIndex+1)
		}

		UpdateTask(taskID, "telegram", 0, fmt.Sprintf("uploading_part_%d", partIndex+1), "")

		up = up.WithProgress(uploadProgress{taskID: taskID, totalSize: size, previousSize: totalUploaded, owner: owner})

		var msgID int
		var uploadErr error
		// Note: For remote streaming uploads, we can't easily retry the same part 
		// because the bodyReader is consumed. We would need to restart the whole download.
		// For now, we try once. If it's a critical error, we fail.
		msgID, uploadErr = uploadFilePart(ctx, api, up, pr, partFilename, mimeType, uniqueFilename, cfg, expectedPartSize)
		if uploadErr != nil {
			UpdateTask(taskID, "error", 0, "upload_part_failed: "+uploadErr.Error(), "")
			return
		}
		uploadedMsgIDs = append(uploadedMsgIDs, msgID)

		if partIndex == 0 {
			firstMsgID = msgID
		}

		partSize := pr.N
		totalUploaded += partSize

		// Insert part record
		_, err = database.DB.Exec(
			"INSERT INTO file_parts (file_id, message_id, part_index, size) VALUES (?, ?, ?, ?)",
			fileID, msgID, partIndex, partSize,
		)
		if err != nil {
			UpdateTask(taskID, "error", 0, "err_db_part_insert: "+err.Error(), "")
			return
		}

		partIndex++

		// Check if we finished
		if size > 0 && totalUploaded >= size {
			break
		}
		// If size unknown, check if we read less than the limit (means EOF reached)
		if size <= 0 && partSize < cfg.MaxPartSize {
			break
		}
	}

	// Update main file record
	database.DB.Exec("UPDATE files SET message_id = ?, size = ? WHERE id = ?", firstMsgID, totalUploaded, fileID)

	// Overwrite cleanup
	if overwrite && existingID > 0 {
		var oldParts []int
		database.DB.Select(&oldParts, "SELECT message_id FROM file_parts WHERE file_id = ?", existingID)

		database.DB.Exec("DELETE FROM files WHERE id = ?", existingID)

		if len(oldParts) > 0 {
			DeleteMessages(context.Background(), cfg, oldParts)
		} else if existingMsgID != nil {
			DeleteMessages(context.Background(), cfg, []int{*existingMsgID})
		}

		if existingThumb != nil {
			var count int
			database.DB.Get(&count, "SELECT COUNT(*) FROM files WHERE thumb_path = ?", *existingThumb)
			if count == 0 {
				os.Remove(*existingThumb)
			}
		}
	}

	// Note: Remote uploads usually don't have a local file for thumbnail generation 
	// unless we download it first. Since this is a streaming upload, we skip local thumb for now.
	// But we signal done so the UI refreshes.
	UpdateTask(taskID, "done", 100, "", "")
	success = true
}

func isPrivateIP(urlStr string) bool {
	u, err := url.Parse(urlStr)
	if err != nil {
		return true
	}
	hostname := u.Hostname()
	if hostname == "localhost" || hostname == "127.0.0.1" || hostname == "::1" {
		return true
	}

	// Check if the hostname is directly an IP address
	if ip := net.ParseIP(hostname); ip != nil {
		return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate()
	}

	ips, err := net.LookupIP(hostname)
	if err != nil {
		// On Termux/Android, DNS lookup might fail due to strict network configurations
		// or missing /etc/resolv.conf. If we can't look it up, we allow it to proceed.
		// If it's truly an invalid domain, the HTTP client will fail to connect anyway.
		return false
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() {
			return true
		}
	}
	return false
}

// ProcessCompleteUploadSync is the synchronous version for the Upload API.
func ProcessCompleteUploadSync(ctx context.Context, filePath, filename, path, mimeType string, cfg *config.Config, overwrite bool, owner string) (fileID int64, finalName string, err error) {
	database.EnsureFoldersExist(path, owner)

	// Wait for a slot in the upload queue
	select {
	case uploadSemaphore <- struct{}{}:
		defer func() { <-uploadSemaphore }()
	case <-ctx.Done():
		return 0, "", fmt.Errorf("upload cancelled while waiting for queue")
	}

	// Handle overwriting: single query instead of two
	var existingID int
	var existingMsgID *int
	var existingThumb *string
	if overwrite {
		database.DB.QueryRow("SELECT id, message_id, thumb_path FROM files WHERE path = ? AND filename = ? AND is_folder = 0 AND owner = ?", path, filename, owner).Scan(&existingID, &existingMsgID, &existingThumb)
	}

	uniqueFilename := filename
	if !overwrite || existingID == 0 {
		uniqueFilename = database.GetUniqueFilename(database.DB, path, filename, false, 0, owner)
	}

	fileInfo, _ := os.Stat(filePath)
	var fileSize int64
	if fileInfo != nil {
		fileSize = fileInfo.Size()
	}

	api := GetAPI()

	// Create main record
	var dbErr error
	for i := 0; i < 5; i++ {
		var res sql.Result
		res, dbErr = database.DB.Exec(
			"INSERT INTO files (filename, path, size, mime_type, is_folder, owner) VALUES (?, ?, ?, ?, 0, ?)",
			uniqueFilename, path, fileSize, mimeType, owner,
		)
		if dbErr == nil {
			fileID, _ = res.LastInsertId()
			break
		}
		uniqueFilename = database.GetUniqueFilename(database.DB, path, filename, false, 0, owner)
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

	up := uploader.NewUploader(api).
		WithPartSize(uploader.MaximumPartSize).
		WithThreads(cfg.UploadThreads)

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
		for attempt := 1; attempt <= 3; attempt++ {
			if attempt > 1 {
				time.Sleep(5 * time.Second)
			}
			msgID, uploadErr = uploadFilePart(ctx, api, up, sectionReader, partFilename, mimeType, uniqueFilename, cfg, partSize)
			if uploadErr == nil {
				break
			}
			log.Printf("[UploadSync] Part %d attempt %d failed: %v", i+1, attempt, uploadErr)
			_, _ = sectionReader.Seek(0, io.SeekStart)
		}

		if uploadErr != nil {
			return 0, "", fmt.Errorf("upload part %d (3 attempts): %w", i+1, uploadErr)
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

	// Clean up old file if overwriting
	if overwrite && existingID > 0 {
		var oldParts []int
		database.DB.Select(&oldParts, "SELECT message_id FROM file_parts WHERE file_id = ?", existingID)

		database.DB.Exec("DELETE FROM files WHERE id = ?", existingID)

		if len(oldParts) > 0 {
			DeleteMessages(context.Background(), cfg, oldParts)
		} else if existingMsgID != nil {
			DeleteMessages(context.Background(), cfg, []int{*existingMsgID})
		}

		if existingThumb != nil {
			var count int
			database.DB.Get(&count, "SELECT COUNT(*) FROM files WHERE thumb_path = ?", *existingThumb)
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
