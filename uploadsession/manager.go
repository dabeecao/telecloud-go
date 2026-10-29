// Package uploadsession implements resumable, checksummed, chunked uploads.
//
// A client splits a file into HTTP transport chunks (default 32 MiB, never more
// than 50 MiB) so no request exceeds a proxy's body limit, e.g. Cloudflare's
// 100 MB. The server streams each chunk to a staging file, records it
// durably, and groups chunks into Telegram storage parts (the largest multiple
// of the chunk size that fits the account limit). A part is sent to Telegram as
// soon as all of its chunks arrived, then its staging file is deleted, so local
// disk use is bounded by a small window of parts rather than by the file size.
// Only when every part is stored and the whole-file SHA-256 matches is the file
// published, in a single transaction.
package uploadsession

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"

	"telecloud/config"
	"telecloud/database"
)

// maxTotalChunks bounds per-session metadata (rows). At the 50 MiB hard cap
// this is ~47 TiB; the practical limit is Telegram, storage and time.
const maxTotalChunks = 1_000_000

// maxOpenSessionsPerUser bounds unfinished sessions (and so staging disk) per user.
const maxOpenSessionsPerUser = 32

var (
	idRe     = regexp.MustCompile(`^[a-f0-9]{32}$`)
	sha256Re = regexp.MustCompile(`^[a-f0-9]{64}$`)
	bufPool  = sync.Pool{New: func() any { b := make([]byte, 256<<10); return &b }}
)

// ValidID reports whether id has the shape of a session ID.
func ValidID(id string) bool { return idRe.MatchString(id) }

// Manager owns all upload sessions of the process.
type Manager struct {
	cfg    *config.Config
	bridge Bridge
	dir    string

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup // pipeline goroutines, drained by Close
	closed bool           // guarded by mu

	inflight chan struct{} // process-wide bound on concurrent chunk requests
	partSem  chan struct{} // bound on concurrent Telegram part uploads

	// OnPublished, when set, is called after a file was published (used to
	// generate thumbnails). It must not block.
	OnPublished func(fileID int64, owner, mime string)

	mu sync.Mutex
	rt map[string]*runtime
}

// runtime is the in-memory companion of one session.
type runtime struct {
	id     string
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex // guards the fields below and staging-file release
	active   int        // in-flight chunk requests
	busy     map[int64]bool
	running  map[int64]bool // parts currently uploading
	pumping  bool
	dirty    bool
	retryAt  map[int64]time.Time
	lastBeat time.Time
}

// New creates a Manager. Call Resume once Telegram is connected to continue
// work interrupted by a restart.
func New(cfg *config.Config, bridge Bridge) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	cu := cfg.ChunkedUpload
	return &Manager{
		cfg: cfg, bridge: bridge, dir: cu.Dir,
		ctx: ctx, cancel: cancel,
		inflight: make(chan struct{}, cu.MaxInflight),
		partSem:  make(chan struct{}, 2),
		rt:       map[string]*runtime{},
	}
}

// Close stops background work. Persisted state is kept.
// Close stops all background work and waits for it to finish. Staged data and
// session rows are left intact so a later Resume continues the uploads.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.cancel()
	m.wg.Wait()
}

// spawn runs fn in a tracked goroutine; it is a no-op once the manager is closed.
func (m *Manager) spawn(fn func()) bool {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return false
	}
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		fn()
	}()
	return true
}

func (m *Manager) enabled() bool { return m.cfg.ChunkedUpload.Enabled }

func (m *Manager) runtime(id string) *runtime {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rt[id]
	if r == nil {
		ctx, cancel := context.WithCancel(m.ctx)
		r = &runtime{id: id, ctx: ctx, cancel: cancel, busy: map[int64]bool{}, running: map[int64]bool{}, retryAt: map[int64]time.Time{}}
		m.rt[id] = r
	}
	return r
}

func (m *Manager) dropRuntime(id string) {
	m.mu.Lock()
	if r := m.rt[id]; r != nil {
		r.cancel()
		delete(m.rt, id)
	}
	m.mu.Unlock()
}

func (m *Manager) stagingDir(id string) string { return filepath.Join(m.dir, id) }
func (m *Manager) partPath(id string, idx int64) string {
	return filepath.Join(m.stagingDir(id), fmt.Sprintf("%d.part", idx))
}

// CreateParams describes a new upload session. Path is the already-mapped
// database path of the destination folder.
type CreateParams struct {
	Owner     string
	Filename  string
	Path      string
	MimeType  string
	Overwrite bool
	TotalSize int64
	ChunkSize int64 // 0 = server default
	SHA256    string
}

// Create opens a new upload session.
func (m *Manager) Create(p CreateParams) (*View, error) {
	if !m.enabled() {
		return nil, errDisabled
	}
	cu := m.cfg.ChunkedUpload
	if p.TotalSize <= 0 {
		return nil, newErr(http.StatusBadRequest, "invalid_size", "total_size must be > 0")
	}
	chunk := p.ChunkSize
	if chunk == 0 {
		chunk = cu.ChunkSize
	}
	if chunk < config.MinChunkedUploadChunkSize || chunk > config.MaxChunkedUploadChunkSize {
		return nil, newErr(http.StatusBadRequest, "invalid_chunk_size",
			fmt.Sprintf("chunk_size must be between %d and %d bytes", config.MinChunkedUploadChunkSize, config.MaxChunkedUploadChunkSize))
	}
	if p.SHA256 != "" && !sha256Re.MatchString(p.SHA256) {
		return nil, errBadChecksum
	}
	// Idempotent create: the same owner, file, destination, size and
	// whole-file checksum maps to the open session already holding the
	// confirmed chunks, so a client that lost its session ID simply resumes.
	// The requested chunk size is deliberately not part of the match: clients
	// that auto-size measure a different speed on every run, and must then use
	// the chunk size of the returned session.
	if p.SHA256 != "" {
		ow := 0
		if p.Overwrite {
			ow = 1
		}
		var existing string
		err := database.RODB.Get(&existing, "SELECT id FROM upload_sessions WHERE owner = ? AND filename = ? AND path = ? AND total_size = ? AND overwrite = ? AND expected_sha256 = ? AND state = ? ORDER BY created_at LIMIT 1",
			p.Owner, p.Filename, p.Path, p.TotalSize, ow, p.SHA256, StateOpen)
		if err == nil {
			return m.view(existing, p.Owner)
		}
	}
	if p.TotalSize > maxTotalChunks*chunk {
		return nil, newErr(http.StatusRequestEntityTooLarge, "file_too_large", "file would need too many chunks; raise chunk_size")
	}
	var open int
	if err := database.RODB.Get(&open, "SELECT COUNT(*) FROM upload_sessions WHERE owner = ? AND state IN (?, ?)", p.Owner, StateOpen, StateFinalizing); err != nil {
		return nil, err
	}
	if open >= maxOpenSessionsPerUser {
		return nil, &Error{Status: http.StatusTooManyRequests, Code: "too_many_sessions", Message: "finish or cancel an existing upload first", RetryAfter: 30}
	}
	totalChunks := (p.TotalSize + chunk - 1) / chunk
	if totalChunks > maxTotalChunks {
		return nil, newErr(http.StatusRequestEntityTooLarge, "file_too_large", "file would need too many chunks; raise chunk_size")
	}
	partSize := m.cfg.EffectivePartSize(chunk)
	if partSize < chunk {
		partSize = chunk
	}
	totalParts := (p.TotalSize + partSize - 1) / partSize

	var idb [16]byte
	if _, err := rand.Read(idb[:]); err != nil {
		return nil, err
	}
	id := hex.EncodeToString(idb[:])
	ts := now()

	tx, err := database.DB.Beginx()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var mimeArg, sum interface{}
	// Same fallback as the single-request upload: browsers/CLIs often send a
	// generic type, so derive it from the extension.
	if p.MimeType == "" || p.MimeType == "application/octet-stream" {
		if t := mime.TypeByExtension(filepath.Ext(p.Filename)); t != "" {
			p.MimeType = t
		}
	}
	if p.MimeType != "" {
		mimeArg = p.MimeType
	}
	if p.SHA256 != "" {
		sum = p.SHA256
	}
	ow := 0
	if p.Overwrite {
		ow = 1
	}
	if _, err := tx.Exec(`INSERT INTO upload_sessions
		(id, owner, filename, path, mime_type, overwrite, total_size, chunk_size, total_chunks, part_size, total_parts, state, expected_sha256, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, p.Owner, p.Filename, p.Path, mimeArg, ow, p.TotalSize, chunk, totalChunks, partSize, totalParts, StateOpen, sum, ts, ts); err != nil {
		return nil, err
	}
	for i := int64(0); i < totalParts; i++ {
		off := i * partSize
		size := partSize
		if off+size > p.TotalSize {
			size = p.TotalSize - off
		}
		if _, err := tx.Exec(`INSERT INTO upload_session_parts (session_id, part_index, offset_bytes, size, state, updated_at) VALUES (?,?,?,?,?,?)`,
			id, i, off, size, partPending, ts); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	log.Printf("[Upload] session=%.8s created size=%d chunks=%d parts=%d", id, p.TotalSize, totalChunks, totalParts)
	return m.view(id, p.Owner)
}

// ChunkResult is the outcome of ReceiveChunk.
type ChunkResult struct {
	Index     int64 `json:"index"`
	Size      int64 `json:"size"`
	Duplicate bool  `json:"duplicate"`
}

// ReceiveChunk streams one chunk from body into the session's staging area.
// contentLength may be -1 when unknown. The chunk is confirmed (and visible in
// the status) only after its length and SHA-256 were verified and it has been
// flushed to disk. Re-sending a confirmed chunk with identical content is a
// no-op; different content for the same index is rejected.
func (m *Manager) ReceiveChunk(owner, id string, index int64, body io.Reader, contentLength int64, sum string) (*ChunkResult, error) {
	if !m.enabled() {
		return nil, errDisabled
	}
	if !sha256Re.MatchString(sum) {
		return nil, errBadChecksum
	}
	s, err := m.owned(id, owner)
	if err != nil {
		return nil, err
	}
	if index < 0 || index >= s.TotalChunks {
		return nil, newErr(http.StatusBadRequest, "invalid_chunk_index", fmt.Sprintf("index must be in [0,%d)", s.TotalChunks))
	}
	offset := index * s.ChunkSize
	expected := s.ChunkSize
	if offset+expected > s.TotalSize {
		expected = s.TotalSize - offset
	}
	if contentLength >= 0 && contentLength != expected {
		return nil, newErr(http.StatusBadRequest, "chunk_length_mismatch", fmt.Sprintf("chunk %d must be exactly %d bytes", index, expected))
	}

	// Idempotent duplicate handling comes first: a retry of an acknowledged
	// chunk must succeed even if the session has since moved on.
	if c, err := loadChunk(id, index); err != nil {
		return nil, err
	} else if c != nil {
		return m.dupResult(s, c, sum)
	}
	if s.State != StateOpen {
		return nil, errNotOpen
	}

	cu := m.cfg.ChunkedUpload
	rt := m.runtime(id)
	select {
	case m.inflight <- struct{}{}:
		defer func() { <-m.inflight }()
	default:
		return nil, busyErr("server_busy")
	}
	rt.mu.Lock()
	if rt.active >= cu.MaxParallel {
		rt.mu.Unlock()
		return nil, busyErr("too_many_parallel_chunks")
	}
	if rt.busy[index] {
		rt.mu.Unlock()
		return nil, &Error{Status: http.StatusConflict, Code: "chunk_in_progress", RetryAfter: 1}
	}
	rt.active++
	rt.busy[index] = true
	rt.mu.Unlock()
	defer func() {
		rt.mu.Lock()
		rt.active--
		delete(rt.busy, index)
		rt.mu.Unlock()
	}()

	// Re-check under the per-chunk lock: another request may have confirmed
	// this chunk while we were waiting. A confirmed chunk's bytes are immutable
	// (they may already be hashed or sent to Telegram), so never write them.
	if c, err := loadChunk(id, index); err != nil {
		return nil, err
	} else if c != nil {
		return m.dupResult(s, c, sum)
	}

	partIdx := offset / s.PartSize
	if err := m.checkWindow(s, partIdx); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(m.stagingDir(id), 0o700); err != nil {
		return nil, storageErr(err)
	}
	f, err := os.OpenFile(m.partPath(id, partIdx), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, storageErr(err)
	}
	defer f.Close()

	h := sha256.New()
	bp := bufPool.Get().(*[]byte)
	w := io.NewOffsetWriter(f, offset-partIdx*s.PartSize)
	n, err := io.CopyBuffer(w, io.TeeReader(io.LimitReader(body, expected), h), *bp)
	bufPool.Put(bp)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, newErr(http.StatusRequestEntityTooLarge, "chunk_too_large", "")
		}
		if isNoSpace(err) {
			return nil, storageErr(err)
		}
		return nil, newErr(http.StatusBadRequest, "chunk_read_failed", "upload interrupted; retry the chunk")
	}
	if n != expected {
		return nil, newErr(http.StatusBadRequest, "chunk_length_mismatch", fmt.Sprintf("chunk %d must be exactly %d bytes", index, expected))
	}
	// Exactly `expected` bytes were written; anything beyond is an error. The
	// excess byte is never written to the staging file.
	var probe [1]byte
	if k, _ := body.Read(probe[:]); k > 0 {
		return nil, newErr(http.StatusBadRequest, "chunk_length_mismatch", fmt.Sprintf("chunk %d must be exactly %d bytes", index, expected))
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sum {
		return nil, newErr(http.StatusUnprocessableEntity, "chunk_checksum_mismatch", "body does not match the supplied SHA-256")
	}
	if err := f.Sync(); err != nil {
		return nil, storageErr(err)
	}

	res, err := database.DB.Exec(database.InsertIgnoreSQL("upload_session_chunks", "session_id, chunk_index, offset_bytes, size, sha256, created_at", "?,?,?,?,?,?"),
		id, index, offset, expected, sum, now())
	if err != nil {
		return nil, err
	}
	if af, _ := res.RowsAffected(); af == 0 {
		// Lost a race with an identical concurrent request.
		c, err := loadChunk(id, index)
		if err != nil || c == nil {
			return nil, errors.New("chunk vanished")
		}
		return m.dupResult(s, c, sum)
	}
	database.DB.Exec("UPDATE upload_sessions SET updated_at = ?, last_chunk_at = ? WHERE id = ?", now(), now(), id)

	if err := m.maybeReadyPart(s, partIdx); err != nil {
		return nil, err
	}
	return &ChunkResult{Index: index, Size: expected}, nil
}

// dupResult answers a request for an already-confirmed chunk. It also repairs
// a part whose promotion was lost to a transient error after the last chunk
// of that part was recorded.
func (m *Manager) dupResult(s *Session, c *Chunk, sum string) (*ChunkResult, error) {
	if s.State == StateOpen {
		m.maybeReadyPart(s, c.Index*s.ChunkSize/s.PartSize)
	}
	return m.duplicate(c, sum)
}

func (m *Manager) duplicate(c *Chunk, sum string) (*ChunkResult, error) {
	if c.SHA256 != sum {
		return nil, newErr(http.StatusConflict, "chunk_conflict", "chunk already received with different content")
	}
	return &ChunkResult{Index: c.Index, Size: c.Size, Duplicate: true}, nil
}

// checkWindow applies backpressure: only a few Telegram parts may be staged on
// disk at once. A chunk beyond the window is deferred (429) until earlier
// parts have been stored in Telegram and released.
func (m *Manager) checkWindow(s *Session, partIdx int64) error {
	var firstOpen struct {
		V *int64 `db:"v"`
	}
	if err := database.RODB.Get(&firstOpen, "SELECT MIN(part_index) AS v FROM upload_session_parts WHERE session_id = ? AND state <> ?", s.ID, partUploaded); err != nil {
		return err
	}
	lowest := s.TotalParts
	if firstOpen.V != nil {
		lowest = *firstOpen.V
	}
	if s.HashedParts < lowest {
		lowest = s.HashedParts
	}
	if partIdx >= lowest+int64(m.cfg.ChunkedUpload.StagedParts) {
		return busyErr("staging_window_full")
	}
	return nil
}

// maybeReadyPart promotes a part to "ready" once all its chunks are confirmed.
func (m *Manager) maybeReadyPart(s *Session, partIdx int64) error {
	first := partIdx * s.PartSize / s.ChunkSize
	last := (partIdx+1)*s.PartSize/s.ChunkSize - 1
	if last >= s.TotalChunks {
		last = s.TotalChunks - 1
	}
	var n int64
	if err := database.RODB.Get(&n, "SELECT COUNT(*) FROM upload_session_chunks WHERE session_id = ? AND chunk_index BETWEEN ? AND ?", s.ID, first, last); err != nil {
		return err
	}
	if n != last-first+1 {
		return nil
	}
	res, err := database.DB.Exec("UPDATE upload_session_parts SET state = ?, updated_at = ? WHERE session_id = ? AND part_index = ? AND state = ?",
		partReady, now(), s.ID, partIdx, partPending)
	if err != nil {
		return err
	}
	if af, _ := res.RowsAffected(); af > 0 {
		m.kick(s.ID)
	}
	return nil
}

func (m *Manager) owned(id, owner string) (*Session, error) {
	if !ValidID(id) {
		return nil, errNotFound
	}
	s, err := loadSession(id)
	if err != nil {
		return nil, err
	}
	if s.Owner != owner {
		return nil, errNotFound
	}
	return s, nil
}

// Finalize requests publication of the file. It is idempotent: repeated calls
// return the current state, and a finalized session keeps returning its file.
// Finalization continues in the background (the last Telegram part may still
// be uploading), so clients poll Status until the state is "finalized".
func (m *Manager) Finalize(owner, id, sum string) (*View, error) {
	s, err := m.owned(id, owner)
	if err != nil {
		return nil, err
	}
	if sum != "" && !sha256Re.MatchString(sum) {
		return nil, errBadChecksum
	}
	switch s.State {
	case StateFinalized:
		return m.view(id, owner)
	case StateFailed, StateCancelled:
		return nil, newErr(http.StatusConflict, "session_"+s.State, deref(s.FailureReason))
	}
	if sum != "" && s.ExpectedSHA256 != nil && *s.ExpectedSHA256 != sum {
		return nil, newErr(http.StatusConflict, "checksum_conflict", "a different whole-file checksum was already registered")
	}
	if sum == "" && s.ExpectedSHA256 != nil {
		sum = *s.ExpectedSHA256
	}
	if sum == "" {
		return nil, newErr(http.StatusBadRequest, "sha256_required", "supply the whole-file SHA-256 at creation or finalization")
	}
	n, bytes, err := countChunks(id)
	if err != nil {
		return nil, err
	}
	if n != s.TotalChunks || bytes != s.TotalSize {
		missing, _ := missingChunks(id, s.TotalChunks, 20)
		e := newErr(http.StatusConflict, "incomplete_upload", fmt.Sprintf("%d of %d chunks confirmed; first missing: %v", n, s.TotalChunks, missing))
		return nil, e
	}
	// Repair any part whose promotion was lost to a transient error.
	for i := int64(0); i < s.TotalParts; i++ {
		m.maybeReadyPart(s, i)
	}
	if s.State == StateOpen {
		if _, err := database.DB.Exec("UPDATE upload_sessions SET state = ?, expected_sha256 = ?, updated_at = ? WHERE id = ? AND state = ?",
			StateFinalizing, sum, now(), id, StateOpen); err != nil {
			return nil, err
		}
	}
	m.kick(id)
	return m.view(id, owner)
}

// Cancel aborts a session and discards its temporary data (staging files and
// Telegram parts that were never published). A finalized session cannot be
// cancelled: its data is committed. Cancelling twice is harmless.
func (m *Manager) Cancel(owner, id string) (*View, error) {
	s, err := m.owned(id, owner)
	if err != nil {
		return nil, err
	}
	if s.State == StateFinalized {
		return nil, newErr(http.StatusConflict, "session_finalized", "committed uploads cannot be cancelled; delete the file instead")
	}
	if s.State != StateCancelled {
		changed, err := setState(id, StateCancelled, "cancelled by client")
		if err != nil {
			return nil, err
		}
		if !changed {
			// Lost a race with publication: the upload is committed.
			if cur, err := loadSession(id); err == nil && cur.State == StateFinalized {
				return nil, newErr(http.StatusConflict, "session_finalized", "committed uploads cannot be cancelled; delete the file instead")
			}
		} else {
			m.discard(id)
		}
	}
	return m.view(id, owner)
}

// discard removes the temporary data of a non-finalized session.
func (m *Manager) discard(id string) {
	m.dropRuntime(id)
	os.RemoveAll(m.stagingDir(id))
	parts, _ := loadParts(id)
	var ids []int
	for _, p := range parts {
		if p.MessageID != nil && !messageReferenced(*p.MessageID) {
			ids = append(ids, int(*p.MessageID))
		}
	}
	database.DB.Exec("DELETE FROM upload_session_chunks WHERE session_id = ?", id)
	database.DB.Exec("DELETE FROM upload_session_parts WHERE session_id = ?", id)
	if len(ids) > 0 {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			if err := m.bridge.DeleteMessages(ctx, ids); err != nil {
				log.Printf("[Upload] session=%.8s cleanup of %d stored parts failed: %v", id, len(ids), err)
			}
		}()
	}
}

func missingChunks(id string, total int64, limit int) ([]int64, error) {
	have, err := chunkIndexes(id)
	if err != nil {
		return nil, err
	}
	var out []int64
	next := int64(0)
	for _, h := range have {
		for ; next < h && len(out) < limit; next++ {
			out = append(out, next)
		}
		next = h + 1
	}
	for ; next < total && len(out) < limit; next++ {
		out = append(out, next)
	}
	return out, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func isNoSpace(err error) bool { return errors.Is(err, syscall.ENOSPC) }

func storageErr(err error) error {
	if isNoSpace(err) {
		return newErr(http.StatusInsufficientStorage, "insufficient_storage", "server staging disk is full")
	}
	log.Printf("[Upload] staging error: %v", err)
	return newErr(http.StatusInternalServerError, "staging_error", "")
}
