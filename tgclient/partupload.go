package tgclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"time"

	"telecloud/config"

	"github.com/gotd/td/telegram/uploader"
)

// Backoff bounds for retrying a failed storage-part upload.
const (
	partRetryBase = 2 * time.Second
	partRetryMax  = 60 * time.Second
)

// ErrTelegramNotReady is returned while the Telegram client is not connected.
var ErrTelegramNotReady = errors.New("telegram client not ready")

// RetryInfo describes one failed attempt of UploadStagedPart.
type RetryInfo struct {
	Attempt   int
	Wait      time.Duration
	FloodWait bool
	Err       error
}

// retryDelay returns how long to wait after a failed attempt: the exact
// flood-wait duration demanded by Telegram (plus a small jitter), otherwise
// exponential backoff with jitter, capped at partRetryMax.
func retryDelay(attempt int, err error) (time.Duration, bool) {
	if secs, ok := ParseFloodWait(err); ok {
		return time.Duration(secs)*time.Second + time.Duration(rand.Int63n(int64(3*time.Second))), true
	}
	d := partRetryBase << uint(attempt-1)
	if d <= 0 || d > partRetryMax {
		d = partRetryMax
	}
	// Equal jitter: wait between 50% and 100% of the computed delay.
	return d/2 + time.Duration(rand.Int63n(int64(d/2)+1)), false
}

// UploadStagedPart uploads size bytes of f as one Telegram storage part and
// returns the message ID that holds it. It is the bridge between the resumable
// upload sessions (which stage HTTP chunks on disk) and the Telegram storage
// layer: callers need not know about slots, flood waits or bot pools.
//
// It takes a slot in the global upload semaphore for every attempt, so session
// uploads share the same Telegram rate limits as regular uploads, and releases
// it while sleeping out a backoff or flood-wait.
func UploadStagedPart(ctx context.Context, f io.ReaderAt, size int64, partName, caption string, cfg *config.Config, maxAttempts int, onRetry func(RetryInfo)) (int, error) {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		msgID, err := uploadStagedPartOnce(ctx, f, size, partName, caption, cfg)
		if err == nil {
			return msgID, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if attempt == maxAttempts {
			break
		}
		wait, flood := retryDelay(attempt, err)
		if onRetry != nil {
			onRetry(RetryInfo{Attempt: attempt, Wait: wait, FloodWait: flood, Err: err})
		}
		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return 0, ctx.Err()
		}
	}
	return 0, fmt.Errorf("telegram part upload failed after %d attempts: %w", maxAttempts, lastErr)
}

func uploadStagedPartOnce(ctx context.Context, f io.ReaderAt, size int64, partName, caption string, cfg *config.Config) (int, error) {
	// IsSystemReady is only true once the client is connected, the log group
	// was verified and the bot pool is settled. Uploading earlier (a resumed
	// session can be ready the moment the process starts) races the startup
	// checks.
	if Client == nil || !IsSystemReady() {
		return 0, ErrTelegramNotReady
	}
	api := GetAPI()
	if api == nil {
		return 0, ErrTelegramNotReady
	}

	if sem := uploadSemaphore; sem != nil {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	staggerUpload(ctx, size)

	up := uploader.NewUploader(api).
		WithPartSize(uploader.MaximumPartSize).
		WithThreads(adaptiveThreads(size, cfg))
	id, err := uploadFilePart(ctx, api, up, io.NewSectionReader(f, 0, size), partName, caption, cfg, size)
	if err != nil && isPeerError(err) {
		// A cached peer that Telegram rejects would otherwise be reused by
		// every later attempt; drop it so the next attempt resolves it again.
		forgetLogGroupPeer(api, cfg.LogGroupID)
	}
	return id, err
}

func isPeerError(err error) bool {
	m := err.Error()
	return strings.Contains(m, "PEER_ID_INVALID") || strings.Contains(m, "CHANNEL_INVALID")
}
