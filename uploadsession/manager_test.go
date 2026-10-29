package uploadsession

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"telecloud/config"
	"telecloud/database"
)

const mib = 1 << 20

// fakeBridge stands in for Telegram: it keeps every stored part in memory.
type fakeBridge struct {
	mu      sync.Mutex
	next    int
	stored  map[int][]byte
	deleted []int
	gate    chan struct{} // when set, UploadPart blocks until it is closed
	failN   int           // fail this many calls first
	calls   int
}

func newFakeBridge() *fakeBridge { return &fakeBridge{stored: map[int][]byte{}, next: 100} }

func (b *fakeBridge) UploadPart(ctx context.Context, f io.ReaderAt, size int64, name, caption string, maxAttempts int, onRetry func(Retry)) (int, error) {
	if b.gate != nil {
		select {
		case <-b.gate:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	b.mu.Lock()
	b.calls++
	if b.failN > 0 {
		b.failN--
		b.mu.Unlock()
		return 0, errors.New("simulated telegram failure")
	}
	b.mu.Unlock()
	data := make([]byte, size)
	if _, err := f.ReadAt(data, 0); err != nil && err != io.EOF {
		return 0, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next++
	b.stored[b.next] = data
	return b.next, nil
}

func (b *fakeBridge) DeleteMessages(ctx context.Context, ids []int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deleted = append(b.deleted, ids...)
	for _, id := range ids {
		delete(b.stored, id)
	}
	return nil
}

func (b *fakeBridge) deletedIDs() []int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]int(nil), b.deleted...)
}

func testCfg(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		MaxPartSize: 1900 * mib,
		ChunkedUpload: config.ChunkedUploadConfig{
			Enabled: true, Threshold: 80 * mib, ChunkSize: mib, MaxParallel: 4, MaxInflight: 8,
			SessionTTL: time.Hour, StagedParts: 2, PartSize: 2 * mib, MaxRetries: 2, Dir: t.TempDir(),
		},
	}
}

func initDB(t *testing.T) {
	t.Helper()
	if err := database.InitDB("sqlite", t.TempDir()+"/t.db", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		database.RWDB.Close()
		database.RODB.Close()
	})
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func randBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func put(m *Manager, id string, idx int, data []byte, chunk int) (*ChunkResult, error) {
	start := idx * chunk
	end := start + chunk
	if end > len(data) {
		end = len(data)
	}
	part := data[start:end]
	return m.ReceiveChunk("alice", id, int64(idx), bytes.NewReader(part), int64(len(part)), sum(part))
}

// putRetry sends a chunk, backing off on 429 like a well-behaved client.
func putRetry(t *testing.T, m *Manager, id string, idx int, data []byte, chunk int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := put(m, id, idx, data, chunk)
		if err == nil {
			return
		}
		var ue *Error
		if errors.As(err, &ue) && ue.Status == http.StatusTooManyRequests && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		t.Fatalf("chunk %d: %v", idx, err)
	}
}

func create(t *testing.T, m *Manager, data []byte) *View {
	t.Helper()
	v, err := m.Create(CreateParams{Owner: "alice", Filename: "big.bin", Path: "/", MimeType: "application/octet-stream", TotalSize: int64(len(data)), ChunkSize: mib})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func wantCode(t *testing.T, err error, status int, code string) {
	t.Helper()
	var ue *Error
	if !errors.As(err, &ue) || ue.Status != status || ue.Code != code {
		t.Fatalf("want %d %s, got %v", status, code, err)
	}
}

// reassemble returns the stored bytes of a published file, following file_parts.
func reassemble(t *testing.T, b *fakeBridge, fileID int64) []byte {
	t.Helper()
	var out []byte
	var parts []database.FilePart
	database.RODB.Select(&parts, "SELECT * FROM file_parts WHERE file_id = ? ORDER BY part_index", fileID)
	if len(parts) == 0 {
		var mid int
		database.RODB.Get(&mid, "SELECT message_id FROM files WHERE id = ?", fileID)
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.stored[mid]
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, p := range parts {
		out = append(out, b.stored[p.MessageID]...)
	}
	return out
}

func TestUploadOutOfOrderAndPublish(t *testing.T) {
	initDB(t)
	cfg, br := testCfg(t), newFakeBridge()
	m := New(cfg, br)
	defer m.Close()

	data := randBytes(5*mib + 12345) // 6 chunks, 2 MiB parts -> 3 parts
	v := create(t, m, data)
	if v.TotalChunks != 6 || v.TotalParts != 3 || v.PartSize != 2*mib {
		t.Fatalf("unexpected geometry: %+v", v)
	}

	// Nothing is published before finalization. Chunks beyond the staging window
	// are deferred with 429 and must succeed when retried, as a real client does.
	for _, i := range []int{1, 0, 3, 2, 5, 4} {
		putRetry(t, m, v.ID, i, data, mib)
	}
	var n int
	database.RODB.Get(&n, "SELECT COUNT(*) FROM files WHERE filename = 'big.bin'")
	if n != 0 {
		t.Fatal("file visible before finalize")
	}

	if _, err := m.Finalize("alice", v.ID, sum(data)); err != nil {
		t.Fatal(err)
	}
	var fin *View
	waitFor(t, "finalized", func() bool {
		fin, _ = m.Status("alice", v.ID)
		return fin != nil && fin.State == StateFinalized
	})
	if fin.FileID == nil {
		t.Fatal("no file id")
	}
	if got := reassemble(t, br, *fin.FileID); !bytes.Equal(got, data) {
		t.Fatal("reconstructed file differs from source")
	}

	// Idempotent finalize: same file, no duplicate row.
	again, err := m.Finalize("alice", v.ID, sum(data))
	if err != nil || again.FileID == nil || *again.FileID != *fin.FileID {
		t.Fatalf("finalize not idempotent: %v %+v", err, again)
	}
	database.RODB.Get(&n, "SELECT COUNT(*) FROM files WHERE filename = 'big.bin'")
	if n != 1 {
		t.Fatalf("expected exactly one file row, got %d", n)
	}

	// Staging disk is released once everything is stored.
	if entries, _ := os.ReadDir(m.stagingDir(v.ID)); len(entries) != 0 {
		t.Fatalf("staging files left behind: %d", len(entries))
	}
}

func TestDuplicateConflictCorruptAndLength(t *testing.T) {
	initDB(t)
	m := New(testCfg(t), newFakeBridge())
	defer m.Close()
	data := randBytes(3 * mib)
	v := create(t, m, data)

	if r, err := put(m, v.ID, 0, data, mib); err != nil || r.Duplicate {
		t.Fatalf("first send: %v %+v", err, r)
	}
	if r, err := put(m, v.ID, 0, data, mib); err != nil || !r.Duplicate {
		t.Fatalf("identical duplicate must be a no-op: %v %+v", err, r)
	}
	// Same index, different content.
	other := randBytes(mib)
	_, err := m.ReceiveChunk("alice", v.ID, 0, bytes.NewReader(other), mib, sum(other))
	wantCode(t, err, http.StatusConflict, "chunk_conflict")

	// Corrupted body: checksum does not match, chunk stays unconfirmed.
	bad := append([]byte(nil), data[mib:2*mib]...)
	bad[10] ^= 0xff
	_, err = m.ReceiveChunk("alice", v.ID, 1, bytes.NewReader(bad), mib, sum(data[mib:2*mib]))
	wantCode(t, err, http.StatusUnprocessableEntity, "chunk_checksum_mismatch")
	// Truncated body and wrong declared length.
	_, err = m.ReceiveChunk("alice", v.ID, 1, bytes.NewReader(data[mib:mib+100]), -1, sum(data[mib:mib+100]))
	wantCode(t, err, http.StatusBadRequest, "chunk_length_mismatch")
	_, err = m.ReceiveChunk("alice", v.ID, 1, bytes.NewReader(data[mib:2*mib]), mib-1, sum(data[mib:2*mib]))
	wantCode(t, err, http.StatusBadRequest, "chunk_length_mismatch")
	// Oversized body.
	_, err = m.ReceiveChunk("alice", v.ID, 1, bytes.NewReader(append(data[mib:2*mib:2*mib], 1)), -1, sum(data[mib:2*mib]))
	wantCode(t, err, http.StatusBadRequest, "chunk_length_mismatch")
	// Out of range and malformed checksum.
	_, err = put(m, v.ID, 3, data, mib)
	wantCode(t, err, http.StatusBadRequest, "invalid_chunk_index")
	_, err = m.ReceiveChunk("alice", v.ID, 1, bytes.NewReader(nil), 0, "xyz")
	wantCode(t, err, http.StatusBadRequest, "invalid_checksum")

	st, _ := m.Status("alice", v.ID)
	if st.ConfirmedChunks != 1 || len(st.ConfirmedRanges) != 1 || st.ConfirmedRanges[0] != [2]int64{0, 0} {
		t.Fatalf("only chunk 0 should be confirmed: %+v", st)
	}
	// The corrupt attempt left garbage in the staging file; a correct retry must win.
	if _, err := put(m, v.ID, 1, data, mib); err != nil {
		t.Fatal(err)
	}

	// Another user cannot see or touch the session.
	_, err = m.Status("mallory", v.ID)
	wantCode(t, err, http.StatusNotFound, "session_not_found")
	_, err = m.ReceiveChunk("mallory", v.ID, 2, bytes.NewReader(nil), 0, sum(nil))
	wantCode(t, err, http.StatusNotFound, "session_not_found")
}

func TestFinalizeRejectsIncompleteAndMissingChecksum(t *testing.T) {
	initDB(t)
	m := New(testCfg(t), newFakeBridge())
	defer m.Close()
	data := randBytes(3 * mib)
	v := create(t, m, data)
	put(m, v.ID, 0, data, mib)

	_, err := m.Finalize("alice", v.ID, sum(data))
	wantCode(t, err, http.StatusConflict, "incomplete_upload")
	put(m, v.ID, 1, data, mib)
	put(m, v.ID, 2, data, mib)
	_, err = m.Finalize("alice", v.ID, "")
	wantCode(t, err, http.StatusBadRequest, "sha256_required")
}

func TestWholeFileChecksumMismatchPublishesNothing(t *testing.T) {
	initDB(t)
	br := newFakeBridge()
	m := New(testCfg(t), br)
	defer m.Close()
	data := randBytes(3 * mib)
	v := create(t, m, data)
	for i := 0; i < 3; i++ {
		if _, err := put(m, v.ID, i, data, mib); err != nil {
			t.Fatal(err)
		}
	}
	wrong := sum([]byte("not the file"))
	if _, err := m.Finalize("alice", v.ID, wrong); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "failed", func() bool { s, _ := m.Status("alice", v.ID); return s.State == StateFailed })
	var n int
	database.RODB.Get(&n, "SELECT COUNT(*) FROM files")
	if n != 0 {
		t.Fatal("a file with a bad checksum was published")
	}
	waitFor(t, "stored parts deleted", func() bool { return len(br.deletedIDs()) > 0 })
}

func TestRestartKeepsConfirmedWork(t *testing.T) {
	initDB(t)
	cfg, br := testCfg(t), newFakeBridge()
	m1 := New(cfg, br)
	data := randBytes(4*mib + 7) // 5 chunks, 3 parts
	v := create(t, m1, data)
	for _, i := range []int{0, 1, 2} {
		if _, err := put(m1, v.ID, i, data, mib); err != nil {
			t.Fatal(err)
		}
	}
	m1.Close() // "process restart": in-memory state is gone, DB + staging remain

	m2 := New(cfg, br)
	defer m2.Close()
	m2.Resume()
	st, err := m2.Status("alice", v.ID)
	if err != nil || st.ConfirmedChunks != 3 || st.State != StateOpen {
		t.Fatalf("progress lost across restart: %v %+v", err, st)
	}
	// A client resumes by sending only what is missing.
	for _, i := range []int{3, 4} {
		if _, err := put(m2, v.ID, i, data, mib); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m2.Finalize("alice", v.ID, sum(data)); err != nil {
		t.Fatal(err)
	}
	var fin *View
	waitFor(t, "finalized", func() bool { fin, _ = m2.Status("alice", v.ID); return fin.State == StateFinalized })
	if !bytes.Equal(reassemble(t, br, *fin.FileID), data) {
		t.Fatal("file differs after restart")
	}
}

func TestRestartDuringFinalizingResumes(t *testing.T) {
	initDB(t)
	cfg, br := testCfg(t), newFakeBridge()
	br.gate = make(chan struct{}) // Telegram "hangs"
	m1 := New(cfg, br)
	data := randBytes(2 * mib)
	v := create(t, m1, data)
	put(m1, v.ID, 0, data, mib)
	put(m1, v.ID, 1, data, mib)
	if _, err := m1.Finalize("alice", v.ID, sum(data)); err != nil {
		t.Fatal(err)
	}
	m1.Close()

	br2 := newFakeBridge()
	m2 := New(cfg, br2)
	defer m2.Close()
	m2.Resume()
	var fin *View
	waitFor(t, "finalized after restart", func() bool { fin, _ = m2.Status("alice", v.ID); return fin.State == StateFinalized })
	if !bytes.Equal(reassemble(t, br2, *fin.FileID), data) {
		t.Fatal("file differs")
	}
}

func TestTelegramFailureIsRetried(t *testing.T) {
	initDB(t)
	br := newFakeBridge()
	br.failN = 1
	cfg := testCfg(t)
	m := New(cfg, br)
	defer m.Close()
	data := randBytes(mib)
	v := create(t, m, data)
	put(m, v.ID, 0, data, mib)
	// First round fails; the pipeline schedules another round (30s in
	// production). Shorten it by re-readying the part as the timer would.
	waitFor(t, "first failure", func() bool { p, _ := loadPart(v.ID, 0); return p != nil && p.State == partFailed })
	database.DB.Exec("UPDATE upload_session_parts SET state = ? WHERE session_id = ?", partReady, v.ID)
	rt := m.runtime(v.ID)
	rt.mu.Lock()
	rt.retryAt = map[int64]time.Time{}
	rt.mu.Unlock()
	m.kick(v.ID)
	if _, err := m.Finalize("alice", v.ID, sum(data)); err != nil {
		t.Fatal(err)
	}
	var fin *View
	waitFor(t, "finalized", func() bool { fin, _ = m.Status("alice", v.ID); return fin.State == StateFinalized })
}

func TestBackpressureWindowAndParallelLimit(t *testing.T) {
	initDB(t)
	cfg := testCfg(t)
	cfg.ChunkedUpload.StagedParts = 1
	cfg.ChunkedUpload.MaxParallel = 1
	br := newFakeBridge()
	br.gate = make(chan struct{})
	m := New(cfg, br)
	defer m.Close()
	data := randBytes(6 * mib) // parts of 2 chunks
	v := create(t, m, data)

	// Part 1 is outside the 1-part staging window while part 0 is incomplete.
	_, err := put(m, v.ID, 2, data, mib)
	wantCode(t, err, http.StatusTooManyRequests, "staging_window_full")

	// Per-session parallelism: hold one chunk request open, a second is refused.
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		_, err := m.ReceiveChunk("alice", v.ID, 0, pr, mib, sum(data[:mib]))
		done <- err
	}()
	waitFor(t, "slow request in flight", func() bool {
		rt := m.runtime(v.ID)
		rt.mu.Lock()
		defer rt.mu.Unlock()
		return rt.active == 1
	})
	_, err = put(m, v.ID, 1, data, mib)
	wantCode(t, err, http.StatusTooManyRequests, "too_many_parallel_chunks")
	// Same chunk twice concurrently is not allowed either.
	pw.Write(data[:mib])
	pw.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	close(br.gate)
}

func TestCancelAndCleanupNeverTouchCommittedData(t *testing.T) {
	initDB(t)
	cfg, br := testCfg(t), newFakeBridge()
	m := New(cfg, br)
	defer m.Close()

	// A finished upload...
	data := randBytes(2 * mib)
	done := create(t, m, data)
	put(m, done.ID, 0, data, mib)
	put(m, done.ID, 1, data, mib)
	m.Finalize("alice", done.ID, sum(data))
	var fin *View
	waitFor(t, "finalized", func() bool { fin, _ = m.Status("alice", done.ID); return fin.State == StateFinalized })

	// ...cannot be cancelled.
	_, err := m.Cancel("alice", done.ID)
	wantCode(t, err, http.StatusConflict, "session_finalized")

	// An abandoned upload with one stored part.
	data2 := randBytes(4 * mib)
	ab := create(t, m, data2)
	put(m, ab.ID, 0, data2, mib)
	put(m, ab.ID, 1, data2, mib)
	waitFor(t, "part stored", func() bool { v, _ := m.Status("alice", ab.ID); return v.PartsStored == 1 })
	put(m, ab.ID, 2, data2, mib)

	// Expire everything, then run cleanup.
	database.DB.Exec("UPDATE upload_sessions SET updated_at = 1")
	m.Cleanup()

	var files int
	database.RODB.Get(&files, "SELECT COUNT(*) FROM files WHERE id = ?", *fin.FileID)
	if files != 1 {
		t.Fatal("cleanup removed a published file")
	}
	if !bytes.Equal(reassemble(t, br, *fin.FileID), data) {
		t.Fatal("cleanup removed committed Telegram data")
	}
	waitFor(t, "abandoned parts deleted", func() bool { return len(br.deletedIDs()) == 1 })
	if _, err := os.Stat(m.stagingDir(ab.ID)); !os.IsNotExist(err) {
		t.Fatal("staging dir of expired session not removed")
	}
	var rows int
	database.RODB.Get(&rows, "SELECT COUNT(*) FROM upload_sessions")
	if rows != 0 {
		t.Fatalf("expired sessions not removed: %d", rows)
	}

	// Cancel is idempotent and discards temporary data.
	v3 := create(t, m, data2)
	put(m, v3.ID, 0, data2, mib)
	if _, err := m.Cancel("alice", v3.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Cancel("alice", v3.ID); err != nil {
		t.Fatalf("second cancel: %v", err)
	}
	_, err = put(m, v3.ID, 1, data2, mib)
	wantCode(t, err, http.StatusConflict, "session_not_open")
}

func TestCreateValidation(t *testing.T) {
	initDB(t)
	m := New(testCfg(t), newFakeBridge())
	defer m.Close()
	for name, p := range map[string]CreateParams{
		"zero size":       {Owner: "a", Filename: "x", Path: "/", TotalSize: 0},
		"chunk too big":   {Owner: "a", Filename: "x", Path: "/", TotalSize: 10, ChunkSize: 51 * mib},
		"chunk too small": {Owner: "a", Filename: "x", Path: "/", TotalSize: 10, ChunkSize: 100},
		"bad sha":         {Owner: "a", Filename: "x", Path: "/", TotalSize: 10, SHA256: "nope"},
	} {
		if _, err := m.Create(p); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	// 50 MiB is the hard upper bound and must be accepted.
	if _, err := m.Create(CreateParams{Owner: "a", Filename: "x", Path: "/", TotalSize: 200 * mib, ChunkSize: 50 * mib}); err != nil {
		t.Fatalf("50 MiB chunk rejected: %v", err)
	}
	cfg := testCfg(t)
	cfg.ChunkedUpload.Enabled = false
	_, err := New(cfg, newFakeBridge()).Create(CreateParams{Owner: "a", Filename: "x", Path: "/", TotalSize: 10})
	wantCode(t, err, http.StatusServiceUnavailable, "chunked_upload_disabled")
}

func TestLargeLogicalFileGeometry(t *testing.T) {
	initDB(t)
	cfg := testCfg(t)
	cfg.ChunkedUpload.PartSize = 0 // use the account limit (1900 MiB)
	m := New(cfg, newFakeBridge())
	defer m.Close()
	// 1 TiB logical file at the default 32 MiB chunk size: metadata only, no data.
	v, err := m.Create(CreateParams{Owner: "a", Filename: "huge.bin", Path: "/", TotalSize: 1 << 40, ChunkSize: 32 * mib})
	if err != nil {
		t.Fatal(err)
	}
	if v.TotalChunks != 32768 {
		t.Fatalf("chunks: %d", v.TotalChunks)
	}
	// Largest multiple of 32 MiB within 1900 MiB is 59*32 = 1888 MiB: chunks never straddle parts.
	if v.PartSize != 1888*mib || v.PartSize%(32*mib) != 0 {
		t.Fatalf("part size %d", v.PartSize)
	}
	if v.TotalParts != (1<<40+v.PartSize-1)/v.PartSize {
		t.Fatalf("parts %d", v.TotalParts)
	}
}

// gaugeBridge records the peak number of concurrent part uploads.
type gaugeBridge struct {
	*fakeBridge
	cur, peak int32
	mu2       sync.Mutex
}

func (g *gaugeBridge) UploadPart(ctx context.Context, f io.ReaderAt, size int64, name, caption string, maxAttempts int, onRetry func(Retry)) (int, error) {
	g.mu2.Lock()
	g.cur++
	if g.cur > g.peak {
		g.peak = g.cur
	}
	g.mu2.Unlock()
	time.Sleep(60 * time.Millisecond) // hold the "Telegram slot"
	defer func() { g.mu2.Lock(); g.cur--; g.mu2.Unlock() }()
	return g.fakeBridge.UploadPart(ctx, f, size, name, caption, maxAttempts, onRetry)
}

// Many concurrent uploads must respect the configured bounds: at most
// MaxInflight chunk requests are served at once (the rest get 429), and at
// most partSem parts are handed to Telegram concurrently.
func TestConcurrentSessionsRespectLimits(t *testing.T) {
	initDB(t)
	cfg := testCfg(t)
	cfg.ChunkedUpload.MaxInflight = 3
	cfg.ChunkedUpload.MaxParallel = 2
	gb := &gaugeBridge{fakeBridge: newFakeBridge()}
	m := New(cfg, gb)
	defer m.Close()

	// Phase 1: hold 12 chunk requests open across 6 sessions; at most 3 may run.
	data := randBytes(4 * mib)
	var ids []string
	for i := 0; i < 6; i++ {
		ids = append(ids, create(t, m, data).ID)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted, refused := 0, 0
	release := make(chan struct{})
	for _, id := range ids {
		for c := 0; c < 2; c++ {
			wg.Add(1)
			go func(id string, c int) {
				defer wg.Done()
				part := data[c*mib : (c+1)*mib]
				pr, pw := io.Pipe()
				go func() { <-release; pw.Write(part); pw.Close() }()
				_, err := m.ReceiveChunk("alice", id, int64(c), pr, mib, sum(part))
				mu.Lock()
				defer mu.Unlock()
				var ue *Error
				if err == nil {
					accepted++
				} else if errors.As(err, &ue) && ue.Status == http.StatusTooManyRequests {
					refused++
				} else {
					t.Errorf("unexpected: %v", err)
				}
			}(id, c)
		}
	}
	waitFor(t, "inflight saturated", func() bool { return len(m.inflight) == cap(m.inflight) })
	time.Sleep(100 * time.Millisecond)
	if n := len(m.inflight); n > 3 {
		t.Fatalf("inflight %d exceeds limit 3", n)
	}
	close(release)
	wg.Wait()
	if refused == 0 || accepted == 0 {
		t.Fatalf("expected both accepted and refused requests, got %d/%d", accepted, refused)
	}

	// Phase 2: finish all sessions; Telegram part uploads stay within the bound.
	for _, id := range ids {
		for c := 0; c < 4; c++ {
			putRetry(t, m, id, c, data, mib)
		}
		if _, err := m.Finalize("alice", id, sum(data)); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range ids {
		id := id
		waitFor(t, "finalized", func() bool { v, _ := m.Status("alice", id); return v.State == StateFinalized })
	}
	if gb.peak > int32(cap(m.partSem)) || gb.peak == 0 {
		t.Fatalf("peak concurrent Telegram part uploads = %d, limit %d", gb.peak, cap(m.partSem))
	}
	t.Logf("peak concurrent chunk requests <= 3, peak concurrent Telegram part uploads = %d (limit %d)", gb.peak, cap(m.partSem))
}

// An over-long body (e.g. chunked transfer encoding, so no Content-Length
// pre-check) must not leave an extra byte in the staging file: on the last
// chunk of a part it would make the part the wrong size and fail the session.
func TestOverlongBodyLeavesNoResidue(t *testing.T) {
	initDB(t)
	cfg, br := testCfg(t), newFakeBridge()
	m := New(cfg, br)
	defer m.Close()

	data := randBytes(4 * mib) // 2 parts of 2 chunks
	v := create(t, m, data)
	for i := 0; i < 4; i++ {
		good := data[i*mib : (i+1)*mib]
		long := append(append([]byte(nil), good...), 0xAA)
		_, err := m.ReceiveChunk("alice", v.ID, int64(i), bytes.NewReader(long), -1, sum(good))
		wantCode(t, err, http.StatusBadRequest, "chunk_length_mismatch")
		putRetry(t, m, v.ID, i, data, mib)
	}
	if _, err := m.Finalize("alice", v.ID, sum(data)); err != nil {
		t.Fatal(err)
	}
	var fin *View
	waitFor(t, "finalized", func() bool {
		fin, _ = m.Status("alice", v.ID)
		return fin != nil && (fin.State == StateFinalized || fin.State == StateFailed)
	})
	if fin.State != StateFinalized {
		t.Fatalf("session ended %s: %s", fin.State, fin.FailureReason)
	}
	if got := reassemble(t, br, *fin.FileID); !bytes.Equal(got, data) {
		t.Fatal("reconstructed file differs from source")
	}
}

// A terminal state must never be overwritten by a racing transition.
func TestStateTransitionsAreGuarded(t *testing.T) {
	initDB(t)
	m := New(testCfg(t), newFakeBridge())
	defer m.Close()
	v := create(t, m, randBytes(mib))
	if ok, err := setState(v.ID, StateFinalized, ""); err != nil || !ok {
		t.Fatalf("open->finalized: %v %v", ok, err)
	}
	for _, st := range []string{StateFailed, StateCancelled, StateOpen} {
		if ok, _ := setState(v.ID, st, "x"); ok {
			t.Fatalf("finalized session was moved to %s", st)
		}
	}
}

// Creating the same upload twice (e.g. after the client lost its session ID)
// must return the open session that already holds the confirmed chunks.
func TestCreateIsIdempotentForIdenticalUpload(t *testing.T) {
	initDB(t)
	m := New(testCfg(t), newFakeBridge())
	defer m.Close()
	data := randBytes(3 * mib)
	mk := func(path string) *View {
		v, err := m.Create(CreateParams{Owner: "alice", Filename: "big.bin", Path: path, TotalSize: int64(len(data)), ChunkSize: mib, SHA256: sum(data)})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	a := mk("/")
	if _, err := put(m, a.ID, 0, data, mib); err != nil {
		t.Fatal(err)
	}
	// A different requested chunk size (auto-sizing measures a new speed on
	// every run) must still resume the same session, which keeps its own size.
	b, err := m.Create(CreateParams{Owner: "alice", Filename: "big.bin", Path: "/", TotalSize: int64(len(data)), ChunkSize: 2 * mib, SHA256: sum(data)})
	if err != nil {
		t.Fatal(err)
	}
	if b.ChunkSize != mib {
		t.Fatalf("resumed session must keep its chunk size, got %d", b.ChunkSize)
	}
	if b.ID != a.ID || b.ConfirmedChunks != 1 {
		t.Fatalf("expected the same session with 1 confirmed chunk, got %s (%d)", b.ID, b.ConfirmedChunks)
	}
	if c := mk("/other"); c.ID == a.ID {
		t.Fatal("a different destination must get its own session")
	}
}
