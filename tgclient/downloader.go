package tgclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"telecloud/config"
	"telecloud/database"

	"github.com/gotd/td/tg"
)

var (
	locationCache = make(map[int]*cachedLocation)
	cacheMutex    sync.RWMutex
)

func init() {
	// Dọn dẹp location cache expired mỗi 30 phút
	go func() {
		ticker := time.NewTicker(30 * time.Minute)
		for range ticker.C {
			now := time.Now()
			cacheMutex.Lock()
			for k, v := range locationCache {
				if now.After(v.expiresAt) {
					delete(locationCache, k)
				}
			}
			cacheMutex.Unlock()
		}
	}()
}

type cachedLocation struct {
	loc       *tg.InputDocumentFileLocation
	api       *tg.Client // Store the API client that resolved this location
	expiresAt time.Time
}

type tgFileReader struct {
	ctx         context.Context
	cancel      context.CancelFunc
	api         *tg.Client
	loc         tg.InputFileLocationClass
	size        int64
	offset      int64
	chunkOffset int64
	chunkData   []byte
}

func (r *tgFileReader) Close() error {
	if r.cancel != nil {
		r.cancel()
	}
	return nil
}

func (r *tgFileReader) Read(p []byte) (int, error) {
	if r.offset >= r.size {
		return 0, io.EOF
	}

	// 1MB chunks — max supported by Telegram UploadGetFile
	const chunkSize = int64(1024 * 1024)

	// If we have no data or the current offset is outside our cached chunk
	if r.chunkData == nil || r.offset < r.chunkOffset || r.offset >= r.chunkOffset+int64(len(r.chunkData)) {
		// Align chunkStart to 1MB to take advantage of sequential reading
		chunkStart := (r.offset / chunkSize) * chunkSize
		
		req := &tg.UploadGetFileRequest{
			Precise:  true,
			Location: r.loc,
			Offset:   chunkStart,
			Limit:    int(chunkSize),
		}

		// Retry up to 3 times for transient Telegram errors
		var res tg.UploadFileClass
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			res, err = r.api.UploadGetFile(r.ctx, req)
			if err == nil {
				break
			}
			if r.ctx.Err() != nil {
				return 0, r.ctx.Err()
			}
			errStr := err.Error()
			if strings.Contains(errStr, "FLOOD_WAIT") || strings.Contains(errStr, "TIMEOUT") || strings.Contains(errStr, "RPC_CALL_FAIL") {
				waitDuration := time.Duration(attempt+1) * 2 * time.Second
				if strings.Contains(errStr, "FLOOD_WAIT_") {
					parts := strings.Split(errStr, "FLOOD_WAIT_")
					if len(parts) > 1 {
						if secs, e := fmt.Sscanf(parts[1], "%d", new(int)); e == nil && secs > 0 {
							waitDuration = time.Duration(secs) * time.Second
						}
					}
				}
				select {
				case <-time.After(waitDuration):
					continue
				case <-r.ctx.Done():
					return 0, r.ctx.Err()
				}
			}
			select {
			case <-time.After(time.Duration(attempt+1) * time.Second):
			case <-r.ctx.Done():
				return 0, r.ctx.Err()
			}
		}
		if err != nil {
			return 0, err
		}

		switch result := res.(type) {
		case *tg.UploadFile:
			r.chunkData = result.Bytes
			r.chunkOffset = chunkStart
			if len(r.chunkData) == 0 {
				// If Telegram returns 0 bytes but we haven't reached r.size, it's an error or unexpected EOF
				if r.offset < r.size {
					return 0, fmt.Errorf("unexpected end of file from telegram at offset %d (expected %d)", r.offset, r.size)
				}
				return 0, io.EOF
			}
		case *tg.UploadFileCDNRedirect:
			return 0, fmt.Errorf("CDN redirect not supported")
		default:
			return 0, fmt.Errorf("unexpected type %T", res)
		}
	}

	inChunkOffset := r.offset - r.chunkOffset
	if inChunkOffset < 0 || inChunkOffset >= int64(len(r.chunkData)) {
		// This should not happen with the check above, but for safety:
		r.chunkData = nil
		return r.Read(p)
	}

	n := copy(p, r.chunkData[inChunkOffset:])
	r.offset += int64(n)
	return n, nil
}

func (r *tgFileReader) Seek(offset int64, whence int) (int64, error) {
	var newOffset int64
	switch whence {
	case io.SeekStart:
		newOffset = offset
	case io.SeekCurrent:
		newOffset = r.offset + offset
	case io.SeekEnd:
		newOffset = r.size + offset
	}
	if newOffset < 0 {
		newOffset = 0
	}
	if newOffset > r.size {
		newOffset = r.size
	}
	r.offset = newOffset
	return r.offset, nil
}

func ServeTelegramFile(c *http.Request, w http.ResponseWriter, file database.File, cfg *config.Config) error {
	ctx := c.Context()

	reader, err := GetTelegramFileReader(ctx, file, cfg)
	if err != nil {
		return err
	}

	// Allow browser/player to seek and cache the stream
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "private, max-age=3600")

	// Set Content-Type if not already set
	if w.Header().Get("Content-Type") == "" && file.MimeType != nil {
		mime := *file.MimeType
		// Fallback for common types if stored as octet-stream
		lowerName := strings.ToLower(file.Filename)
		if mime == "application/octet-stream" {
			if strings.HasSuffix(lowerName, ".pdf") {
				mime = "application/pdf"
			} else if strings.HasSuffix(lowerName, ".epub") {
				mime = "application/epub+zip"
			}
		}
		// Special handling for MKV to ensure browser compatibility
		if strings.HasSuffix(lowerName, ".mkv") {
			mime = "video/webm"
		}
		w.Header().Set("Content-Type", mime)
	}

	// Set Content-Disposition only if not already set (e.g., by router for attachment)
	if w.Header().Get("Content-Disposition") == "" {
		// Use proper RFC 6266 encoding for filename to support non-ASCII characters and quotes
		// filename*=UTF-8''... is the standard for modern browsers
		encodedName := url.PathEscape(file.Filename)
		// We still provide the quoted filename for legacy browsers
		safeName := strings.ReplaceAll(file.Filename, `"`, `\"`)
		w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"; filename*=UTF-8''%s`, safeName, encodedName))
	}

	defer reader.Close()
	http.ServeContent(w, c, file.Filename, file.CreatedAt, reader)
	return nil
}

func GetTelegramFileReader(ctx context.Context, file database.File, cfg *config.Config) (io.ReadSeekCloser, error) {
	// Check if this file has multiple parts
	parts, err := database.GetFileParts(file.ID)
	if err == nil && len(parts) > 1 {
		ctx, cancel := context.WithCancel(ctx)
		return &multiPartReader{
			ctx:    ctx,
			cancel: cancel,
			parts:  parts,
			size:   file.Size,
			cfg:    cfg,
		}, nil
	}

	// Single part (or legacy file)
	if file.MessageID == nil {
		return nil, fmt.Errorf("file has no message ID")
	}
	return getSinglePartReader(ctx, *file.MessageID, file.Size, cfg)
}

var getSinglePartReader = func(ctx context.Context, msgID int, size int64, cfg *config.Config) (io.ReadSeekCloser, error) {
	ctx, cancel := context.WithCancel(ctx)

	// Check cache first
	cacheMutex.RLock()
	cached, ok := locationCache[msgID]
	cacheMutex.RUnlock()

	if ok && time.Now().Before(cached.expiresAt) {
		return &tgFileReader{
			ctx:    ctx,
			cancel: cancel,
			api:    cached.api, // Reuse the same API client that resolved this location
			loc:    cached.loc,
			size:   size,
		}, nil
	}

	// Helper function to resolve media from a specific API client
	resolve := func(targetApi *tg.Client) (*tg.InputDocumentFileLocation, error) {
		peer, err := resolveLogGroup(ctx, targetApi, cfg.LogGroupID)
		if err != nil {
			return nil, err
		}

		var msgs tg.MessageClassArray
		if channel, ok := peer.(*tg.InputPeerChannel); ok {
			res, err := targetApi.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
				Channel: &tg.InputChannel{
					ChannelID:  channel.ChannelID,
					AccessHash: channel.AccessHash,
				},
				ID: []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}},
			})
			if err != nil {
				return nil, err
			}
			switch m := res.(type) {
			case *tg.MessagesMessages:
				msgs = m.Messages
			case *tg.MessagesMessagesSlice:
				msgs = m.Messages
			case *tg.MessagesChannelMessages:
				msgs = m.Messages
			}
		} else {
			res, err := targetApi.MessagesGetMessages(ctx, []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}})
			if err != nil {
				return nil, err
			}
			switch m := res.(type) {
			case *tg.MessagesMessages:
				msgs = m.Messages
			case *tg.MessagesMessagesSlice:
				msgs = m.Messages
			case *tg.MessagesChannelMessages:
				msgs = m.Messages
			}
		}

		if len(msgs) == 0 {
			return nil, fmt.Errorf("message not found")
		}

		msg, ok := msgs[0].(*tg.Message)
		if !ok || msg.Media == nil {
			// This often happens if the bot is not an admin in a group and privacy mode is on,
			// or if the message ID is invalid for this session.
			return nil, fmt.Errorf("message has no media")
		}

		docMedia, ok := msg.Media.(*tg.MessageMediaDocument)
		if !ok {
			return nil, fmt.Errorf("media is not a document")
		}

		doc, ok := docMedia.Document.(*tg.Document)
		if !ok {
			return nil, fmt.Errorf("document is empty")
		}

		return doc.AsInputDocumentFileLocation(), nil
	}

	api := GetAPI()
	loc, err := resolve(api)

	// Fallback to main client if the selected bot failed to find the message/media
	if err != nil && api != Client.API() {
		// Only retry for specific "not found" or "no media" errors which usually indicate permission issues in bot pool
		errStr := err.Error()
		if strings.Contains(errStr, "not found") || strings.Contains(errStr, "no media") {
			mainApi := Client.API()
			if locRetry, errRetry := resolve(mainApi); errRetry == nil {
				api = mainApi
				loc = locRetry
				err = nil
			}
		}
	}

	if err != nil {
		cancel()
		return nil, err
	}
	
	// Cache the location AND the API client for 1 hour
	cacheMutex.Lock()
	locationCache[msgID] = &cachedLocation{
		loc:       loc,
		api:       api,
		expiresAt: time.Now().Add(1 * time.Hour),
	}
	cacheMutex.Unlock()

	reader := &tgFileReader{
		ctx:    ctx,
		cancel: cancel,
		api:    api,
		loc:    loc,
		size:   size,
	}

	return reader, nil
}

type multiPartReader struct {
	ctx    context.Context
	cancel context.CancelFunc
	parts  []database.FilePart
	size   int64
	offset int64
	cfg    *config.Config

	currentReader io.ReadSeekCloser
	currentIndex  int
}

func (r *multiPartReader) Close() error {
	if r.currentReader != nil {
		r.currentReader.Close()
	}
	if r.cancel != nil {
		r.cancel()
	}
	return nil
}

func (r *multiPartReader) Read(p []byte) (int, error) {
	if r.offset >= r.size {
		return 0, io.EOF
	}

	for {
		if r.currentReader == nil {
			// Find which part contains the current offset
			var partStart int64
			found := false
			for i, p := range r.parts {
				if r.offset < partStart+p.Size {
					r.currentIndex = i
					reader, err := getSinglePartReader(r.ctx, p.MessageID, p.Size, r.cfg)
					if err != nil {
						return 0, err
					}
					// Seek to the relative offset within this part
					_, err = reader.Seek(r.offset-partStart, io.SeekStart)
					if err != nil {
						return 0, err
					}
					r.currentReader = reader
					found = true
					break
				}
				partStart += p.Size
			}
			if !found {
				return 0, io.EOF
			}
		}

		n, err := r.currentReader.Read(p)
		if n > 0 {
			r.offset += int64(n)
			return n, nil
		}
		if err == io.EOF {
			r.currentReader.Close()
			r.currentReader = nil
			r.currentIndex++
			if r.currentIndex >= len(r.parts) {
				return 0, io.EOF
			}
			continue
		}
		return n, err
	}
}

func (r *multiPartReader) Seek(offset int64, whence int) (int64, error) {
	var newOffset int64
	switch whence {
	case io.SeekStart:
		newOffset = offset
	case io.SeekCurrent:
		newOffset = r.offset + offset
	case io.SeekEnd:
		newOffset = r.size + offset
	}

	if newOffset < 0 {
		newOffset = 0
	}
	if newOffset > r.size {
		newOffset = r.size
	}

	if newOffset != r.offset {
		r.offset = newOffset
		if r.currentReader != nil {
			r.currentReader.Close()
			r.currentReader = nil
		}
	}
	return r.offset, nil
}
