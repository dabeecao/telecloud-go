package tgclient

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

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

	resolvedPeer   tg.InputPeerClass
	resolvedPeerID string
	resolvedPeerMu sync.RWMutex
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

func resolveLogGroup(ctx context.Context, api *tg.Client, logGroupIDStr string) (tg.InputPeerClass, error) {
	resolvedPeerMu.RLock()
	if resolvedPeerID == logGroupIDStr && resolvedPeer != nil {
		p := resolvedPeer
		resolvedPeerMu.RUnlock()
		return p, nil
	}
	resolvedPeerMu.RUnlock()

	resolvedPeerMu.Lock()
	defer resolvedPeerMu.Unlock()

	// Double check
	if resolvedPeerID == logGroupIDStr && resolvedPeer != nil {
		return resolvedPeer, nil
	}

	var peer tg.InputPeerClass
	var err error

	if logGroupIDStr == "me" || logGroupIDStr == "self" {
		peer = &tg.InputPeerSelf{}
	} else {
		logGroupID, errParse := strconv.ParseInt(logGroupIDStr, 10, 64)
		if errParse != nil {
			return nil, fmt.Errorf("invalid LOG_GROUP_ID: %v", errParse)
		}

		if logGroupID < 0 {
			strID := strconv.FormatInt(logGroupID, 10)
			if strings.HasPrefix(strID, "-100") {
				channelID, _ := strconv.ParseInt(strID[4:], 10, 64)
				dialogs, errDlg := api.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
					OffsetPeer: &tg.InputPeerEmpty{},
					Limit:      100,
				})
				if errDlg == nil {
					switch d := dialogs.(type) {
					case *tg.MessagesDialogs:
						for _, chat := range d.Chats {
							if c, ok := chat.(*tg.Channel); ok && c.ID == channelID {
								peer = &tg.InputPeerChannel{
									ChannelID:  c.ID,
									AccessHash: c.AccessHash,
								}
								break
							}
						}
					case *tg.MessagesDialogsSlice:
						for _, chat := range d.Chats {
							if c, ok := chat.(*tg.Channel); ok && c.ID == channelID {
								peer = &tg.InputPeerChannel{
									ChannelID:  c.ID,
									AccessHash: c.AccessHash,
								}
								break
							}
						}
					}
				} else {
					err = errDlg
				}
			} else {
				peer = &tg.InputPeerChat{ChatID: -logGroupID}
			}
		} else {
			peer = &tg.InputPeerUser{UserID: logGroupID}
		}
	}

	if err != nil {
		return nil, err
	}
	if peer == nil {
		return nil, fmt.Errorf("could not resolve peer for ID %s", logGroupIDStr)
	}

	resolvedPeer = peer
	resolvedPeerID = logGroupIDStr
	return peer, nil
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

	UpdateTask(taskID, "telegram", 0, "")

	api := Client.API()
	up := uploader.NewUploader(api).
		WithPartSize(uploader.MaximumPartSize).
		WithProgress(uploadProgress{taskID: taskID}).
		WithThreads(3)

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
	caption := fmt.Sprintf("Path: %s\nFilename: %s", path, filename)

	docBuilder := message.UploadedDocument(file, html.String(nil, caption)).Filename(filename).MIME(mimeType)
	
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

	fileInfo, err := os.Stat(filePath)
	var size int64 = 0
	if err == nil {
		size = fileInfo.Size()
	}

	localThumb := utils.CreateLocalThumbnail(filePath, mimeType, cfg.FFMPEGPath)

	// Save to DB
	_, err = database.DB.Exec(
		"INSERT INTO files (message_id, filename, path, size, mime_type, is_folder, thumb_path) VALUES (?, ?, ?, ?, ?, 0, ?)",
		msgID, filename, path, size, mimeType, localThumb,
	)
	if err != nil {
		UpdateTask(taskID, "error", 0, "DB Error: "+err.Error())
		return
	}

	UpdateTask(taskID, "done", 100, "")
}

// ProcessCompleteUploadSync is the synchronous version for the Upload API.
// It blocks until the Telegram upload and DB insert are complete, then returns
// the newly created file ID (for attaching a share token) or an error.
func ProcessCompleteUploadSync(ctx context.Context, filePath, filename, path, mimeType string, cfg *config.Config) (fileID int64, err error) {
	api := Client.API()
	up := uploader.NewUploader(api).
		WithPartSize(uploader.MaximumPartSize).
		WithThreads(3)

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
