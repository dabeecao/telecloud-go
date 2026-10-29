package uploadsession

import (
	"context"
	"io"
	"time"

	"telecloud/config"
	"telecloud/tgclient"
)

// Retry is reported by a Bridge for every failed attempt of a part upload.
type Retry struct {
	Attempt   int
	Wait      time.Duration
	FloodWait bool
	Err       error
}

// Bridge is the Telegram storage layer as seen by upload sessions. It is an
// interface so the pipeline can be exercised without a Telegram connection.
type Bridge interface {
	// UploadPart stores size bytes of f as one Telegram storage part and returns
	// the ID of the message holding it.
	UploadPart(ctx context.Context, f io.ReaderAt, size int64, name, caption string, maxAttempts int, onRetry func(Retry)) (int, error)
	// DeleteMessages removes stored parts that were never published.
	DeleteMessages(ctx context.Context, ids []int) error
}

// TelegramBridge adapts tgclient to the Bridge interface.
type TelegramBridge struct{ Cfg *config.Config }

func (b TelegramBridge) UploadPart(ctx context.Context, f io.ReaderAt, size int64, name, caption string, maxAttempts int, onRetry func(Retry)) (int, error) {
	return tgclient.UploadStagedPart(ctx, f, size, name, caption, b.Cfg, maxAttempts, func(ri tgclient.RetryInfo) {
		if onRetry != nil {
			onRetry(Retry{Attempt: ri.Attempt, Wait: ri.Wait, FloodWait: ri.FloodWait, Err: ri.Err})
		}
	})
}

func (b TelegramBridge) DeleteMessages(ctx context.Context, ids []int) error {
	return tgclient.DeleteMessages(ctx, b.Cfg, ids)
}
