package tgclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"telecloud/config"

	"github.com/gotd/td/tg"
)

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

	chunkSize := int64(512 * 1024)
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

func ServeTelegramFile(c *http.Request, w http.ResponseWriter, msgID int, filename string, size int64, cfg *config.Config) error {
	ctx := c.Context()
	reader, err := GetTelegramFileReader(ctx, msgID, size, cfg)
	if err != nil {
		return err
	}

	http.ServeContent(w, c, filename, time.Time{}, reader)
	return nil
}

func GetTelegramFileReader(ctx context.Context, msgID int, size int64, cfg *config.Config) (io.ReadSeeker, error) {
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
	
	reader := &tgFileReader{
		ctx:  ctx,
		api:  api,
		loc:  loc,
		size: size,
	}

	return reader, nil
}
