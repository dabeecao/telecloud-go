package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"telecloud/config"
	"telecloud/database"

	"github.com/gin-gonic/gin"
)

func newLegacyUploadRouter(t *testing.T) (*gin.Engine, *config.Config) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	// SQLite by default; set TC_TEST_DRIVER/TC_TEST_DSN for MySQL or PostgreSQL.
	driver, dsn := testDriverEnv()
	if driver == "sqlite" {
		if err := database.InitDB("sqlite", t.TempDir()+"/t.db", ""); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := database.InitDB(driver, "", dsn); err != nil {
			t.Fatalf("InitDB(%s): %v", driver, err)
		}
		// Shared server: start from empty resume tables.
		database.DB.Exec("DELETE FROM upload_chunks")
		database.DB.Exec("DELETE FROM upload_tasks")
	}
	t.Cleanup(func() {
		// Drain each task's async DB queue before the next test swaps the
		// global DB handles out from under it.
		chunkTrackerSync.Range(func(k, v any) bool {
			s := v.(*chunkState)
			s.Lock()
			drained := s.closeDBLocked()
			s.Unlock()
			if drained != nil {
				<-drained
			}
			chunkTrackerSync.Delete(k)
			return true
		})
		closedTasks.Range(func(k, _ any) bool { closedTasks.Delete(k); return true })
		database.RWDB.Close()
		database.RODB.Close()
	})
	cfg := &config.Config{TempDir: t.TempDir(), MaxPartSize: 1 << 30}
	h := &Handler{cfg: cfg}
	r := gin.New()
	g := r.Group("/api", func(c *gin.Context) {
		c.Set("username", c.GetHeader("X-User"))
		c.Set("is_admin", false)
	})
	g.POST("/upload", h.handlePostUpload)
	g.GET("/upload/check/:task_id", h.handleGetUploadCheck)
	return r, cfg
}

type legacyChunk struct {
	user, taskID, filename string
	index, total           int
	totalSize, chunkSize   int64
	body                   []byte
}

func postLegacyChunk(r *gin.Engine, ch legacyChunk) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fields := map[string]string{
		"filename":     ch.filename,
		"path":         "/",
		"task_id":      ch.taskID,
		"chunk_index":  strconv.Itoa(ch.index),
		"total_chunks": strconv.Itoa(ch.total),
		"total_size":   strconv.FormatInt(ch.totalSize, 10),
		"chunk_size":   strconv.FormatInt(ch.chunkSize, 10),
	}
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	fw, _ := mw.CreateFormFile("file", "blob")
	fw.Write(ch.body)
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-User", ch.user)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestLegacyUploadConcurrentFirstChunks sends the chunks of a fresh task at
// once. Before initialisation was serialised, a chunk that lost the
// LoadOrStore race could be written before the winner truncated the file, and
// the temp file came out with zeroed regions. The last chunk is withheld so
// the task does not hand off to Telegram (which would delete the temp file).
func TestLegacyUploadConcurrentFirstChunks(t *testing.T) {
	r, cfg := newLegacyUploadRouter(t)
	const chunkSize = 1024
	data := make([]byte, chunkSize*7+100)
	rand.Read(data)
	total := (len(data) + chunkSize - 1) / chunkSize
	sent := total - 1

	var wg sync.WaitGroup
	codes := make([]int, sent)
	for i := sent - 1; i >= 0; i-- {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			end := min((i+1)*chunkSize, len(data))
			w := postLegacyChunk(r, legacyChunk{"alice", "task_a", "f.bin", i, total, int64(len(data)), chunkSize, data[i*chunkSize : end]})
			codes[i] = w.Code
		}(i)
	}
	wg.Wait()
	for i, c := range codes {
		if c != http.StatusOK {
			t.Fatalf("chunk %d: status %d", i, c)
		}
	}
	got, err := os.ReadFile(filepath.Join(cfg.TempDir, "task_a_f.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(data) {
		t.Fatalf("temp file size = %d, want %d", len(got), len(data))
	}
	if !bytes.Equal(got[:sent*chunkSize], data[:sent*chunkSize]) {
		t.Fatal("temp file does not match the uploaded chunks")
	}
}

func TestLegacyUploadRejectsForeignTaskAndBadChunks(t *testing.T) {
	r, _ := newLegacyUploadRouter(t)
	const chunkSize = 1024
	totalSize := int64(chunkSize*2 + 10)

	if w := postLegacyChunk(r, legacyChunk{"alice", "task_b", "f.bin", 0, 3, totalSize, chunkSize, make([]byte, chunkSize)}); w.Code != http.StatusOK {
		t.Fatalf("first chunk: %d %s", w.Code, w.Body.String())
	}
	// Another user must neither write into nor read the progress of alice's task.
	if w := postLegacyChunk(r, legacyChunk{"mallory", "task_b", "f.bin", 1, 3, totalSize, chunkSize, make([]byte, chunkSize)}); w.Code != http.StatusForbidden {
		t.Fatalf("foreign chunk: got %d, want 403", w.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/upload/check/task_b", nil)
	req.Header.Set("X-User", "mallory")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Body.String() != `{"chunks":[]}` {
		t.Fatalf("foreign check leaked chunks: %s", w.Body.String())
	}

	// A body longer than its slot would overwrite the next chunk.
	if w := postLegacyChunk(r, legacyChunk{"alice", "task_b", "f.bin", 1, 3, totalSize, chunkSize, make([]byte, chunkSize+5)}); w.Code != http.StatusBadRequest {
		t.Fatalf("oversized chunk: got %d, want 400", w.Code)
	}
	// total_chunks that does not match total_size/chunk_size.
	if w := postLegacyChunk(r, legacyChunk{"alice", "task_c", "f.bin", 0, 5, totalSize, chunkSize, make([]byte, chunkSize)}); w.Code != http.StatusBadRequest {
		t.Fatalf("layout mismatch: got %d, want 400", w.Code)
	}
}

// stubTelegram replaces the Telegram hand-off and reports the file it was
// given (with its content read at hand-off time) on the returned channel.
func stubTelegram(t *testing.T, block <-chan struct{}) <-chan []byte {
	t.Helper()
	got := make(chan []byte, 4)
	prev := processCompleteUpload
	processCompleteUpload = func(_ context.Context, filePath, _, _, _, _ string, _ *config.Config, _ bool, _ string) {
		if block != nil {
			<-block
		}
		b, _ := os.ReadFile(filePath)
		got <- b
	}
	t.Cleanup(func() { processCompleteUpload = prev })
	return got
}

func uploadCheck(r *gin.Engine, user, taskID string) string {
	req := httptest.NewRequest(http.MethodGet, "/api/upload/check/"+taskID, nil)
	req.Header.Set("X-User", user)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Body.String()
}

func countResumeRows(t *testing.T, taskID string) int {
	t.Helper()
	var a, b int
	database.RODB.Get(&a, "SELECT COUNT(*) FROM upload_tasks WHERE id = ?", taskID)
	database.RODB.Get(&b, "SELECT COUNT(*) FROM upload_chunks WHERE task_id = ?", taskID)
	return a + b
}

// A retried final chunk (response lost) must not start a second task, and a
// new run of the same file while Telegram is still reading the first one must
// not truncate the file being uploaded.
func TestLegacyUploadFinalChunkRetryAndRerun(t *testing.T) {
	r, _ := newLegacyUploadRouter(t)
	release := make(chan struct{})
	handed := stubTelegram(t, release)

	const cs = 1024
	data := make([]byte, cs*2+7)
	rand.Read(data)
	n := 3
	send := func(i int) *httptest.ResponseRecorder {
		end := min((i+1)*cs, len(data))
		return postLegacyChunk(r, legacyChunk{"alice", "task_r", "f.bin", i, n, int64(len(data)), cs, data[i*cs : end]})
	}
	for i := 0; i < n; i++ {
		if w := send(i); w.Code != http.StatusOK {
			t.Fatalf("chunk %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	if w := send(n - 1); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "processing_telegram") {
		t.Fatalf("retried final chunk: %d %s", w.Code, w.Body.String())
	}
	if c := countResumeRows(t, "task_r"); c != 0 {
		t.Fatalf("%d resume rows left after hand-off", c)
	}

	// New run of the same file while the first hand-off is still pending.
	if body := uploadCheck(r, "alice", "task_r"); body != `{"chunks":[]}` {
		t.Fatalf("check for new run: %s", body)
	}
	if w := send(0); w.Code != http.StatusOK {
		t.Fatalf("new run chunk 0: %d %s", w.Code, w.Body.String())
	}
	close(release)
	select {
	case b := <-handed:
		if !bytes.Equal(b, data) {
			t.Fatal("file handed to Telegram was modified by the new run")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no hand-off")
	}
	select {
	case <-handed:
		t.Fatal("final-chunk retry triggered a second hand-off")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestLegacyUploadChunkAfterCancel(t *testing.T) {
	r, cfg := newLegacyUploadRouter(t)
	r.POST("/api/cancel_upload", func(c *gin.Context) {
		c.Set("username", "alice")
		(&Handler{cfg: cfg}).handleCancelUpload(c)
	})
	const cs = 1024
	chunk := func(i int) legacyChunk {
		return legacyChunk{"alice", "task_x", "f.bin", i, 3, cs * 3, cs, make([]byte, cs)}
	}
	if w := postLegacyChunk(r, chunk(0)); w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	form := url.Values{"task_id": {"task_x"}}
	req := httptest.NewRequest(http.MethodPost, "/api/cancel_upload", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	if w := postLegacyChunk(r, chunk(1)); w.Code != http.StatusConflict {
		t.Fatalf("chunk after cancel: got %d, want 409", w.Code)
	}
	if c := countResumeRows(t, "task_x"); c != 0 {
		t.Fatalf("%d resume rows left after cancel", c)
	}
	if m, _ := filepath.Glob(filepath.Join(cfg.TempDir, "task_x_*")); len(m) != 0 {
		t.Fatalf("temp files left after cancel: %v", m)
	}
}
