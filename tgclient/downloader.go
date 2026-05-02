package tgclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
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
	expiresAt time.Time
}

type tgFileReader struct {
	ctx         context.Context
	api         *tg.Client
	loc         tg.InputFileLocationClass
	size        int64
	offset      int64
	chunkOffset int64
	chunkData   []byte
}

func (r *tgFileReader) Read(p []byte) (int, error) {
	if r.offset >= r.size {
		return 0, io.EOF
	}

	chunkSize := int64(1024 * 1024) // 1MB chunks — max supported by Telegram UploadGetFile
	chunkStart := (r.offset / chunkSize) * chunkSize

	if r.chunkData == nil || r.chunkOffset != chunkStart {
		req := &tg.UploadGetFileRequest{
			Precise:  true,
			Location: r.loc,
			Offset:   chunkStart,
			Limit:    int(chunkSize),
		}

		res, err := r.api.UploadGetFile(r.ctx, req)
		if err != nil {
			return 0, err
		}

		switch result := res.(type) {
		case *tg.UploadFile:
			r.chunkData = result.Bytes
			r.chunkOffset = chunkStart
			if len(r.chunkData) == 0 {
				return 0, io.EOF
			}
		case *tg.UploadFileCDNRedirect:
			return 0, fmt.Errorf("CDN redirect not supported")
		default:
			return 0, fmt.Errorf("unexpected type %T", res)
		}
	}

	inChunkOffset := r.offset - r.chunkOffset
	if inChunkOffset >= int64(len(r.chunkData)) {
		return 0, io.EOF
	}

	n := copy(p, r.chunkData[inChunkOffset:])
	r.offset += int64(n)
	return n, nil
}

func (r *tgFileReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		r.offset = offset
	case io.SeekCurrent:
		r.offset += offset
	case io.SeekEnd:
		r.offset = r.size + offset
	}
	if r.offset < 0 {
		r.offset = 0
	}
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

	// Serve inline so browsers play it directly instead of downloading
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, file.Filename))

	http.ServeContent(w, c, file.Filename, time.Time{}, reader)
	return nil
}

func GetTelegramFileReader(ctx context.Context, file database.File, cfg *config.Config) (io.ReadSeeker, error) {
	// Check if this file has multiple parts
	parts, err := database.GetFileParts(file.ID)
	if err == nil && len(parts) > 1 {
		return &multiPartReader{
			ctx:   ctx,
			parts: parts,
			size:  file.Size,
			cfg:   cfg,
		}, nil
	}

	// Single part (or legacy file)
	if file.MessageID == nil {
		return nil, fmt.Errorf("file has no message ID")
	}
	return getSinglePartReader(ctx, *file.MessageID, file.Size, cfg)
}

var getSinglePartReader = func(ctx context.Context, msgID int, size int64, cfg *config.Config) (io.ReadSeeker, error) {
	// Check cache first
	cacheMutex.RLock()
	cached, ok := locationCache[msgID]
	cacheMutex.RUnlock()

	if ok && time.Now().Before(cached.expiresAt) {
		return &tgFileReader{
			ctx:  ctx,
			api:  Client.API(),
			loc:  cached.loc,
			size: size,
		}, nil
	}

	api := Client.API()
	peer, err := resolveLogGroup(ctx, api, cfg.LogGroupID)
	if err != nil {
		return nil, err
	}

	var msgs tg.MessageClassArray

	if channel, ok := peer.(*tg.InputPeerChannel); ok {
		res, err := api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: &tg.InputChannel{
				ChannelID:  channel.ChannelID,
				AccessHash: channel.AccessHash,
			},
			ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}},
		})
		if err != nil {
			return nil, err
		}
		if m, ok := res.(*tg.MessagesChannelMessages); ok {
			msgs = m.Messages
		}
	} else {
		res, err := api.MessagesGetMessages(ctx, []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}})
		if err != nil {
			return nil, err
		}
		if m, ok := res.(*tg.MessagesMessages); ok {
			msgs = m.Messages
		} else if m, ok := res.(*tg.MessagesMessagesSlice); ok {
			msgs = m.Messages
		}
	}

	if len(msgs) == 0 {
		return nil, fmt.Errorf("message not found")
	}

	msg, ok := msgs[0].(*tg.Message)
	if !ok || msg.Media == nil {
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

	loc := doc.AsInputDocumentFileLocation()
	
	// Cache the location for 1 hour
	cacheMutex.Lock()
	locationCache[msgID] = &cachedLocation{
		loc:       loc,
		expiresAt: time.Now().Add(1 * time.Hour),
	}
	cacheMutex.Unlock()

	reader := &tgFileReader{
		ctx:  ctx,
		api:  api,
		loc:  loc,
		size: size,
	}

	return reader, nil
}

type multiPartReader struct {
	ctx    context.Context
	parts  []database.FilePart
	size   int64
	offset int64
	cfg    *config.Config

	currentReader io.ReadSeeker
	currentIndex  int
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
		r.currentReader = nil // Force re-acquisition and seek of the correct part
	}
	return r.offset, nil
}
