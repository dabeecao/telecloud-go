package uploadsession

import (
	"database/sql"
	"errors"
	"time"

	"telecloud/database"
)

// Session states.
const (
	StateOpen       = "open"
	StateFinalizing = "finalizing"
	StateFinalized  = "finalized"
	StateFailed     = "failed"
	StateCancelled  = "cancelled"
)

// Storage part states.
const (
	partPending   = "pending"   // waiting for chunks
	partReady     = "ready"     // all chunks staged, waiting for Telegram
	partUploading = "uploading" // being sent to Telegram
	partUploaded  = "uploaded"  // stored in Telegram (message_id set)
	partFailed    = "failed"    // upload attempts exhausted; retried later
)

type Session struct {
	ID             string  `db:"id"`
	Owner          string  `db:"owner"`
	Filename       string  `db:"filename"`
	Path           string  `db:"path"`
	MimeType       *string `db:"mime_type"`
	Overwrite      int     `db:"overwrite"`
	TotalSize      int64   `db:"total_size"`
	ChunkSize      int64   `db:"chunk_size"`
	TotalChunks    int64   `db:"total_chunks"`
	PartSize       int64   `db:"part_size"`
	TotalParts     int64   `db:"total_parts"`
	State          string  `db:"state"`
	ExpectedSHA256 *string `db:"expected_sha256"`
	HashState      []byte  `db:"hash_state"`
	HashedParts    int64   `db:"hashed_parts"`
	FileID         *int64  `db:"file_id"`
	FinalName      *string `db:"final_name"`
	FailureReason  *string `db:"failure_reason"`
	RetryCount     int64   `db:"retry_count"`
	CreatedAt      int64   `db:"created_at"`
	UpdatedAt      int64   `db:"updated_at"`
	LastChunkAt    int64   `db:"last_chunk_at"`
}

type Part struct {
	SessionID string  `db:"session_id"`
	Index     int64   `db:"part_index"`
	Offset    int64   `db:"offset_bytes"`
	Size      int64   `db:"size"`
	State     string  `db:"state"`
	MessageID *int64  `db:"message_id"`
	Attempts  int64   `db:"attempts"`
	LastError *string `db:"last_error"`
	UpdatedAt int64   `db:"updated_at"`
}

type Chunk struct {
	Index  int64  `db:"chunk_index"`
	Offset int64  `db:"offset_bytes"`
	Size   int64  `db:"size"`
	SHA256 string `db:"sha256"`
}

func now() int64 { return time.Now().Unix() }

func loadSession(id string) (*Session, error) {
	var s Session
	err := database.RODB.Get(&s, "SELECT * FROM upload_sessions WHERE id = ?", id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func loadParts(id string) ([]Part, error) {
	var parts []Part
	err := database.RODB.Select(&parts, "SELECT * FROM upload_session_parts WHERE session_id = ? ORDER BY part_index", id)
	return parts, err
}

func loadPart(id string, idx int64) (*Part, error) {
	var p Part
	if err := database.RODB.Get(&p, "SELECT * FROM upload_session_parts WHERE session_id = ? AND part_index = ?", id, idx); err != nil {
		return nil, err
	}
	return &p, nil
}

func loadChunk(id string, idx int64) (*Chunk, error) {
	var c Chunk
	err := database.RODB.Get(&c, "SELECT chunk_index, offset_bytes, size, sha256 FROM upload_session_chunks WHERE session_id = ? AND chunk_index = ?", id, idx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &c, err
}

func countChunks(id string) (n int64, bytes int64, err error) {
	var row struct {
		N int64         `db:"n"`
		B sql.NullInt64 `db:"b"`
	}
	err = database.RODB.Get(&row, "SELECT COUNT(*) AS n, SUM(size) AS b FROM upload_session_chunks WHERE session_id = ?", id)
	return row.N, row.B.Int64, err
}

func chunkIndexes(id string) ([]int64, error) {
	var idx []int64
	err := database.RODB.Select(&idx, "SELECT chunk_index FROM upload_session_chunks WHERE session_id = ? ORDER BY chunk_index", id)
	return idx, err
}

// setState moves a session to a new state and reports whether it changed.
// Transitions are guarded so a terminal state is never overwritten by a racing
// goroutine: a finalized session stays finalized, and only live sessions can
// be failed or cancelled.
func setState(id, state, reason string) (bool, error) {
	var r interface{}
	if reason != "" {
		r = reason
	}
	guard := "state IN ('open','finalizing')"
	if state == StateCancelled {
		guard = "state IN ('open','finalizing','failed')"
	}
	res, err := database.DB.Exec("UPDATE upload_sessions SET state = ?, failure_reason = ?, updated_at = ? WHERE id = ? AND "+guard, state, r, now(), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func touch(id string) {
	database.DB.Exec("UPDATE upload_sessions SET updated_at = ? WHERE id = ?", now(), id)
}

// messageReferenced reports whether a Telegram message backs a published file.
// It is the last line of defence ensuring temporary-upload cleanup can never
// delete committed file data.
func messageReferenced(msgID int64) bool {
	var n int
	if err := database.RODB.Get(&n, "SELECT (SELECT COUNT(*) FROM files WHERE message_id = ?) + (SELECT COUNT(*) FROM file_parts WHERE message_id = ?)", msgID, msgID); err != nil {
		return true // fail safe: when unsure, keep the data
	}
	return n > 0
}
