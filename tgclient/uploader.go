package tgclient

import (
	"context"
	"fmt"
	"os"
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
	uploadSemaphore = make(chan struct{}, 3)
)

type UploadStatus struct {
	Status  string `json:"status"`
	Percent int    `json:"percent"`
	Message string `json:"message,omitempty"`
}

func UpdateTask(taskID string, status string, percent int, msg string) {
	taskMutex.Lock()
	defer taskMutex.Unlock()
	UploadTasks[taskID] = &UploadStatus{
		Status:  status,
		Percent: percent,
		Message: msg,
	}
	ws.BroadcastTaskUpdate(taskID, status, percent, msg)
}

func GetTask(taskID string) *UploadStatus {
	taskMutex.Lock()
	defer taskMutex.Unlock()
	if t, ok := UploadTasks[taskID]; ok {
		return t
	}
	return &UploadStatus{Status: "pending", Percent: 0}
}

func CancelTask(taskID string) {
	taskMutex.Lock()
	defer taskMutex.Unlock()
	if cancel, ok := TaskCancels[taskID]; ok {
		cancel()
		delete(TaskCancels, taskID)
	}
	if task, ok := UploadTasks[taskID]; ok {
		task.Status = "error"
		task.Message = "Upload cancelled"
	}
}


type uploadProgress struct {
	taskID string
}

func (p uploadProgress) Chunk(ctx context.Context, state uploader.ProgressState) error {
	if state.Total > 0 {
		percent := int(float64(state.Uploaded) / float64(state.Total) * 100)
		UpdateTask(p.taskID, "telegram", percent, "")
	}
	return nil
}

func ProcessCompleteUpload(ctx context.Context, filePath, filename, path, mimeType, taskID string, cfg *config.Config) {
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

	UpdateTask(taskID, "telegram", 0, "waiting_slot")

	// Wait for a slot in the upload queue
	select {
	case uploadSemaphore <- struct{}{}:
		defer func() { <-uploadSemaphore }()
	case <-ctx.Done():
		UpdateTask(taskID, "error", 0, "upload_cancelled_waiting")
		return
	}

	UpdateTask(taskID, "telegram", 0, "")

	// Recalculate unique filename inside the telegram processing block to reduce race window
	uniqueFilename := database.GetUniqueFilename(path, filename, false)

	api := Client.API()
	up := uploader.NewUploader(api).
		WithPartSize(uploader.MaximumPartSize).
		WithProgress(uploadProgress{taskID: taskID}).
		WithThreads(4) // Increased from 2 to speed up larger file uploads

	file, err := up.FromPath(ctx, filePath)
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

	// Create caption
	caption := fmt.Sprintf("Path: %s\nFilename: %s", path, uniqueFilename)

	docBuilder := message.UploadedDocument(file, html.String(nil, caption)).Filename(uniqueFilename).MIME(mimeType)
	
	msgBuilder := sender.To(peer)

	res, err := msgBuilder.Media(ctx, docBuilder)
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

	localThumb := utils.CreateLocalThumbnail(filePath, mimeType, cfg.FFMPEGPath)

	// Save to DB
	_, err = database.DB.Exec(
		"INSERT INTO files (message_id, filename, path, size, mime_type, is_folder, thumb_path) VALUES (?, ?, ?, ?, ?, 0, ?)",
		msgID, uniqueFilename, path, size, mimeType, localThumb,
	)
	if err != nil {
		UpdateTask(taskID, "error", 0, "err_db_error")
		return
	}

	UpdateTask(taskID, "done", 100, "")

	// Add a small cool-down delay before releasing the semaphore slot
	// to prevent hitting rate limits when uploading many small files in sequence.
	select {
	case <-time.After(1000 * time.Millisecond):
	case <-ctx.Done():
	}
}

// ProcessCompleteUploadSync is the synchronous version for the Upload API.
// It blocks until the Telegram upload and DB insert are complete, then returns
// the newly created file ID (for attaching a share token) or an error.
func ProcessCompleteUploadSync(ctx context.Context, filePath, filename, path, mimeType string, cfg *config.Config) (fileID int64, err error) {
	// Wait for a slot in the upload queue
	select {
	case uploadSemaphore <- struct{}{}:
		defer func() { <-uploadSemaphore }()
	case <-ctx.Done():
		return 0, fmt.Errorf("upload cancelled while waiting for queue")
	}

	api := Client.API()
	up := uploader.NewUploader(api).
		WithPartSize(uploader.MaximumPartSize).
		WithThreads(4) // Increased from 2 to speed up larger file uploads

	file, err := up.FromPath(ctx, filePath)
	if err != nil {
		return 0, fmt.Errorf("upload to telegram: %w", err)
	}

	sender := message.NewSender(api)
	peer, err := resolveLogGroup(ctx, api, cfg.LogGroupID)
	if err != nil {
		return 0, fmt.Errorf("resolve peer: %w", err)
	}

	caption := fmt.Sprintf("Path: %s\nFilename: %s", path, filename)
	docBuilder := message.UploadedDocument(file, html.String(nil, caption)).Filename(filename).MIME(mimeType)

	res, err := sender.To(peer).Media(ctx, docBuilder)
	if err != nil {
		return 0, fmt.Errorf("send media: %w", err)
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
		return 0, fmt.Errorf("err_tg_msgid")
	}

	fileInfo, _ := os.Stat(filePath)
	var size int64
	if fileInfo != nil {
		size = fileInfo.Size()
	}

	localThumb := utils.CreateLocalThumbnail(filePath, mimeType, cfg.FFMPEGPath)

	var newID int64
	err = database.DB.QueryRow(
		"INSERT INTO files (message_id, filename, path, size, mime_type, is_folder, thumb_path) VALUES (?, ?, ?, ?, ?, 0, ?) RETURNING id",
		msgID, filename, path, size, mimeType, localThumb,
	).Scan(&newID)
	if err != nil {
		return 0, fmt.Errorf("db insert: %w", err)
	}

	// Add a small cool-down delay before releasing the semaphore slot
	select {
	case <-time.After(1000 * time.Millisecond):
	case <-ctx.Done():
	}

	return newID, nil
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
