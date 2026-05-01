package tgclient

import (
	"context"
	"database/sql"
	"fmt"

	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"crypto/tls"
	"io"

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
	uploadSemaphore       = make(chan struct{}, 3)
	remoteUploadSemaphore = make(chan struct{}, 3)
)



type UploadStatus struct {
	Status   string `json:"status"`
	Percent  int    `json:"percent"`
	Message  string `json:"message,omitempty"`
	Filename string `json:"filename,omitempty"`
	Owner    string `json:"owner,omitempty"`
	Size     int64  `json:"size,omitempty"`
}

func UpdateTask(taskID string, status string, percent int, msg string) {
	UpdateTaskWithFile(taskID, status, percent, msg, "", "", 0)
}

func UpdateTaskWithSize(taskID string, status string, percent int, msg string, size int64) {
	UpdateTaskWithFile(taskID, status, percent, msg, "", "", size)
}

func UpdateTaskWithFile(taskID string, status string, percent int, msg string, filename string, owner string, size int64) {
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

	UploadTasks[taskID] = &UploadStatus{
		Status:   status,
		Percent:  percent,
		Message:  msg,
		Filename: finalFilename,
		Owner:    finalOwner,
		Size:     finalSize,
	}
	ws.BroadcastTaskUpdate(finalOwner, taskID, status, percent, msg, finalSize)

	// Auto-cleanup: remove task from memory after 5 minutes once terminal
	if status == "done" || status == "error" {
		go func() {
			time.Sleep(5 * time.Minute)
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
	
	// Verify owner
	if status, ok := UploadTasks[taskID]; !ok || status.Owner != username {
		taskMutex.Unlock()
		return false
	}

	if cancel, ok := TaskCancels[taskID]; ok {
		cancel()
		delete(TaskCancels, taskID)
	}
	taskMutex.Unlock()

	// Gọi UpdateTask trong goroutine riêng để tránh deadlock (UpdateTask cũng lock taskMutex)
	go UpdateTask(taskID, "error", 0, "upload_cancelled_waiting")
	return true
}


type uploadProgress struct {
	taskID    string
	totalSize int64
}

func (p uploadProgress) Chunk(ctx context.Context, state uploader.ProgressState) error {
	total := state.Total
	if total <= 0 {
		total = p.totalSize
	}
	if total > 0 {
		percent := int(float64(state.Uploaded) / float64(total) * 100)
		UpdateTask(p.taskID, "telegram", percent, "")
	}
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

	UpdateTaskWithFile(taskID, "telegram", 0, "waiting_slot", filename, owner, fileSize)

	// Wait for a slot in the upload queue
	select {
	case uploadSemaphore <- struct{}{}:
		defer func() { <-uploadSemaphore }()
	case <-ctx.Done():
		UpdateTaskWithFile(taskID, "error", 0, "upload_cancelled_waiting", filename, owner, fileSize)
		return
	}

	UpdateTaskWithFile(taskID, "telegram", 0, "", filename, owner, fileSize)

	// Handle overwriting: single query instead of two
	var existingID int
	var existingMsgID *int
	var existingThumb *string
	if overwrite {
		database.DB.QueryRow("SELECT id, message_id, thumb_path FROM files WHERE path = ? AND filename = ? AND is_folder = 0", path, filename).Scan(&existingID, &existingMsgID, &existingThumb)
	}

	uniqueFilename := filename
	if !overwrite || existingID == 0 {
		uniqueFilename = database.GetUniqueFilename(database.DB, path, filename, false, 0)
	}

	api := Client.API()
	up := uploader.NewUploader(api).
		WithPartSize(uploader.MaximumPartSize).
		WithProgress(uploadProgress{taskID: taskID, totalSize: stat.Size()}).
		WithThreads(cfg.UploadThreads)

	var file tg.InputFileClass

	for attempt := 1; attempt <= 3; attempt++ {
		file, err = up.FromPath(ctx, filePath)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			break // Bị huỷ do user cancel
		}
		UpdateTask(taskID, "telegram", 0, fmt.Sprintf("upload_failed_retrying_attempt_%d", attempt))
		select {
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
		}
	}

	if err != nil {
		UpdateTask(taskID, "error", 0, err.Error())
		return
	}

	sender := message.NewSender(api)
	peer, err := resolveLogGroup(ctx, api, cfg.LogGroupID)
	if err != nil {
		UpdateTask(taskID, "error", 0, "Resolve peer error: "+err.Error())
		return
	}

	caption := fmt.Sprintf("Path: %s\nFilename: %s", path, uniqueFilename)
	docBuilder := message.UploadedDocument(file, html.String(nil, caption)).Filename(uniqueFilename).MIME(mimeType)

	res, err := sender.To(peer).Media(ctx, docBuilder)
	if err != nil {
		UpdateTask(taskID, "error", 0, err.Error())
		return
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
		UpdateTask(taskID, "error", 0, "err_tg_msgid")
		return
	}

	fileInfo, err := os.Stat(filePath)
	var size int64 = 0
	if err == nil {
		size = fileInfo.Size()
	}

	// Insert to DB first (thumbnail generated async below)
	for i := 0; i < 5; i++ {
		_, err = database.DB.Exec(
			"INSERT INTO files (message_id, filename, path, size, mime_type, is_folder, thumb_path) VALUES (?, ?, ?, ?, ?, 0, NULL)",
			msgID, uniqueFilename, path, size, mimeType,
		)
		if err == nil {
			break
		}
		uniqueFilename = database.GetUniqueFilename(database.DB, path, filename, false, 0)
		time.Sleep(100 * time.Millisecond)
	}

	if err != nil {
		UpdateTask(taskID, "error", 0, "err_db_error: "+err.Error())
		return
	}

	// Overwrite cleanup
	if overwrite && existingID > 0 {
		database.DB.Exec("DELETE FROM files WHERE id = ?", existingID)
		if existingMsgID != nil {
			var count int
			database.DB.Get(&count, "SELECT COUNT(*) FROM files WHERE message_id = ?", *existingMsgID)
			if count == 0 {
				DeleteMessages(context.Background(), cfg, []int{*existingMsgID})
			}
		}
		if existingThumb != nil {
			var count int
			database.DB.Get(&count, "SELECT COUNT(*) FROM files WHERE thumb_path = ?", *existingThumb)
			if count == 0 {
				os.Remove(*existingThumb)
			}
		}
	}

	// Signal done to user immediately, then generate thumbnail during cooldown
	UpdateTask(taskID, "done", 100, "")

	// Cooldown before releasing the semaphore slot
	select {
	case <-time.After(1000 * time.Millisecond):
	case <-ctx.Done():
	}

	// Generate thumbnail from temp file (still exists at this point) and update DB
	localThumb := utils.CreateLocalThumbnail(filePath, mimeType, cfg.FFMPEGPath)
	if localThumb != nil {
		database.DB.Exec("UPDATE files SET thumb_path = ? WHERE message_id = ? AND path = ? AND filename = ?", *localThumb, msgID, path, uniqueFilename)
	}
}



func ProcessRemoteUpload(ctx context.Context, url, path, taskID string, cfg *config.Config, overwrite bool, maxSizeMB int, owner string) {
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

	UpdateTaskWithFile(taskID, "downloading", 0, "initiating_request", filename, owner, 0)

	// SSRF Protection
	if isPrivateIP(url) {
		UpdateTask(taskID, "error", 0, "err_forbidden_url")
		return
	}

	// 1. Wait for a slot in the remote upload queue (HTTP download limit)
	select {
	case remoteUploadSemaphore <- struct{}{}:
		defer func() { <-remoteUploadSemaphore }()
	case <-ctx.Done():
		UpdateTaskWithFile(taskID, "error", 0, "upload_cancelled_waiting", filename, owner, 0)
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
		UpdateTask(taskID, "error", 0, "request_creation_failed: "+err.Error())
		return
	}

	// Add User-Agent to avoid being blocked by some servers
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		UpdateTask(taskID, "error", 0, "connection_failed: "+err.Error())
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
		UpdateTask(taskID, "error", 0, msg)
		return
	}

	size := resp.ContentLength
	if maxSizeMB > 0 && size > int64(maxSizeMB)*1024*1024 {
		UpdateTask(taskID, "error", 0, "file_too_large")
		return
	}

	// Determine filename
	filename = filepath.Base(url)
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

	UpdateTaskWithFile(taskID, "telegram", 0, "waiting_slot", filename, owner, size)

	// Wait for a slot in the upload queue
	select {
	case uploadSemaphore <- struct{}{}:
		defer func() { <-uploadSemaphore }()
	case <-ctx.Done():
		UpdateTaskWithFile(taskID, "error", 0, "upload_cancelled_waiting", filename, owner, size)
		return
	}

	UpdateTaskWithFile(taskID, "telegram", 0, "", filename, owner, size)

	// Handle overwriting
	var existingID int
	var existingMsgID *int
	var existingThumb *string
	if overwrite {
		database.DB.QueryRow("SELECT id, message_id, thumb_path FROM files WHERE path = ? AND filename = ? AND is_folder = 0", path, filename).Scan(&existingID, &existingMsgID, &existingThumb)
	}

	uniqueFilename := filename
	if !overwrite || existingID == 0 {
		uniqueFilename = database.GetUniqueFilename(database.DB, path, filename, false, 0)
	}

	api := Client.API()
	up := uploader.NewUploader(api).
		WithPartSize(uploader.MaximumPartSize).
		WithProgress(uploadProgress{taskID: taskID, totalSize: size}).
		WithThreads(cfg.UploadThreads)

	// Enforce max size for streams with unknown ContentLength using maxSizeReader
	var limit int64 = 0
	if maxSizeMB > 0 {
		limit = int64(maxSizeMB) * 1024 * 1024
	}
	bodyReader := &maxSizeReader{
		r:       resp.Body,
		maxSize: limit,
	}

	// Stream from Reader to Telegram
	// Note: FromReader will read the stream. If it fails, we can't easily retry without restarting the HTTP request.
	file, err := up.FromReader(ctx, uniqueFilename, bodyReader)
	if err != nil {
		if ctx.Err() != nil {
			UpdateTask(taskID, "error", 0, "upload_cancelled")
		} else {
			UpdateTask(taskID, "error", 0, "upload_failed: "+err.Error())
		}
		return
	}

	sender := message.NewSender(api)
	peer, err := resolveLogGroup(ctx, api, cfg.LogGroupID)
	if err != nil {
		UpdateTask(taskID, "error", 0, "Resolve peer error: "+err.Error())
		return
	}

	caption := fmt.Sprintf("Path: %s\nFilename: %s\nSource: %s", path, uniqueFilename, url)
	docBuilder := message.UploadedDocument(file, html.String(nil, caption)).Filename(uniqueFilename).MIME(mimeType)

	res, err := sender.To(peer).Media(ctx, docBuilder)
	if err != nil {
		UpdateTask(taskID, "error", 0, err.Error())
		return
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
		UpdateTask(taskID, "error", 0, "err_tg_msgid")
		return
	}

	// Insert to DB
	for i := 0; i < 5; i++ {
		_, err = database.DB.Exec(
			"INSERT INTO files (message_id, filename, path, size, mime_type, is_folder, thumb_path) VALUES (?, ?, ?, ?, ?, 0, NULL)",
			msgID, uniqueFilename, path, size, mimeType,
		)
		if err == nil {
			break
		}
		uniqueFilename = database.GetUniqueFilename(database.DB, path, filename, false, 0)
		time.Sleep(100 * time.Millisecond)
	}

	if err != nil {
		UpdateTask(taskID, "error", 0, "err_db_error: "+err.Error())
		return
	}

	// Overwrite cleanup
	if overwrite && existingID > 0 {
		database.DB.Exec("DELETE FROM files WHERE id = ?", existingID)
		if existingMsgID != nil {
			var count int
			database.DB.Get(&count, "SELECT COUNT(*) FROM files WHERE message_id = ?", *existingMsgID)
			if count == 0 {
				DeleteMessages(context.Background(), cfg, []int{*existingMsgID})
			}
		}
		if existingThumb != nil {
			var count int
			database.DB.Get(&count, "SELECT COUNT(*) FROM files WHERE thumb_path = ?", *existingThumb)
			if count == 0 {
				os.Remove(*existingThumb)
			}
		}
	}

	UpdateTask(taskID, "done", 100, "")
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
func ProcessCompleteUploadSync(ctx context.Context, filePath, filename, path, mimeType string, cfg *config.Config, overwrite bool) (fileID int64, finalName string, err error) {
	// Wait for a slot in the upload queue
	select {
	case uploadSemaphore <- struct{}{}:
		defer func() { <-uploadSemaphore }()
	case <-ctx.Done():
		return 0, "", fmt.Errorf("upload cancelled while waiting for queue")
	}

	api := Client.API()
	up := uploader.NewUploader(api).
		WithPartSize(uploader.MaximumPartSize).
		WithThreads(cfg.UploadThreads)

	var file tg.InputFileClass

	for attempt := 1; attempt <= 3; attempt++ {
		file, err = up.FromPath(ctx, filePath)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			break
		}
		select {
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
		}
	}

	if err != nil {
		return 0, "", fmt.Errorf("upload to telegram: %w", err)
	}

	sender := message.NewSender(api)
	peer, err := resolveLogGroup(ctx, api, cfg.LogGroupID)
	if err != nil {
		return 0, "", fmt.Errorf("resolve peer: %w", err)
	}

	// Handle overwriting: single query instead of two
	var existingID int
	var existingMsgID *int
	var existingThumb *string
	if overwrite {
		database.DB.QueryRow("SELECT id, message_id, thumb_path FROM files WHERE path = ? AND filename = ? AND is_folder = 0", path, filename).Scan(&existingID, &existingMsgID, &existingThumb)
	}

	uniqueFilename := filename
	if !overwrite || existingID == 0 {
		uniqueFilename = database.GetUniqueFilename(database.DB, path, filename, false, 0)
	}
	caption := fmt.Sprintf("Path: %s\nFilename: %s", path, uniqueFilename)
	docBuilder := message.UploadedDocument(file, html.String(nil, caption)).Filename(uniqueFilename).MIME(mimeType)

	res, err := sender.To(peer).Media(ctx, docBuilder)
	if err != nil {
		return 0, "", fmt.Errorf("send media: %w", err)
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
		return 0, "", fmt.Errorf("err_tg_msgid")
	}

	fileInfo, _ := os.Stat(filePath)
	var size int64
	if fileInfo != nil {
		size = fileInfo.Size()
	}

	// Thumbnail skipped for the sync API path (no temp file retention after return)
	var newID int64
	var dbErr error
	for i := 0; i < 5; i++ {
		var res sql.Result
		res, dbErr = database.DB.Exec(
			"INSERT INTO files (message_id, filename, path, size, mime_type, is_folder, thumb_path) VALUES (?, ?, ?, ?, ?, 0, NULL)",
			msgID, uniqueFilename, path, size, mimeType,
		)
		if dbErr == nil {
			newID, _ = res.LastInsertId()
			break
		}
		uniqueFilename = database.GetUniqueFilename(database.DB, path, filename, false, 0)
		time.Sleep(100 * time.Millisecond)
	}
	if dbErr != nil {
		return 0, "", fmt.Errorf("db insert: %w", dbErr)
	}

	// Clean up old file if overwriting
	if overwrite && existingID > 0 {
		database.DB.Exec("DELETE FROM files WHERE id = ?", existingID)
		if existingMsgID != nil {
			var count int
			database.DB.Get(&count, "SELECT COUNT(*) FROM files WHERE message_id = ?", *existingMsgID)
			if count == 0 {
				DeleteMessages(context.Background(), cfg, []int{*existingMsgID})
			}
		}
		if existingThumb != nil {
			var count int
			database.DB.Get(&count, "SELECT COUNT(*) FROM files WHERE thumb_path = ?", *existingThumb)
			if count == 0 {
				os.Remove(*existingThumb)
			}
		}
	}

	return newID, uniqueFilename, nil
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
