package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"telecloud/config"
	"telecloud/database"
	"telecloud/uploadsession"

	"github.com/gin-gonic/gin"
)

type memBridge struct {
	mu   sync.Mutex
	n    int
	data map[int][]byte
}

func (b *memBridge) UploadPart(ctx context.Context, f io.ReaderAt, size int64, name, caption string, maxAttempts int, onRetry func(uploadsession.Retry)) (int, error) {
	buf := make([]byte, size)
	f.ReadAt(buf, 0)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.n++
	b.data[1000+b.n] = buf
	return 1000 + b.n, nil
}
func (b *memBridge) DeleteMessages(ctx context.Context, ids []int) error { return nil }

func shaHex(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func TestUploadSessionHTTPFlow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	if err := database.InitDB("sqlite", t.TempDir()+"/t.db", ""); err != nil {
		t.Fatal(err)
	}
	defer database.RWDB.Close()
	defer database.RODB.Close()

	const mib = 1 << 20
	cfg := &config.Config{MaxPartSize: 1900 * mib, ChunkedUpload: config.ChunkedUploadConfig{
		Enabled: true, Threshold: 80 * mib, ChunkSize: mib, MaxParallel: 4, MaxInflight: 8,
		SessionTTL: time.Hour, StagedParts: 2, PartSize: 2 * mib, MaxRetries: 2, Dir: t.TempDir(),
	}}
	br := &memBridge{data: map[int][]byte{}}
	h := &Handler{cfg: cfg, uploads: uploadsession.New(cfg, br)}
	defer h.uploads.Close()

	r := gin.New()
	user := "alice"
	g := r.Group("/api", func(c *gin.Context) { c.Set("username", user); c.Set("is_admin", false) })
	g.GET("/uploads/config", h.handleGetUploadsConfig)
	g.POST("/uploads/probe", h.handlePostUploadProbe)
	g.POST("/uploads", h.handleCreateUpload)
	g.GET("/uploads/:id", h.handleGetUpload)
	g.PUT("/uploads/:id/chunks/:index", h.handlePutUploadChunk)
	g.POST("/uploads/:id/finalize", h.handleFinalizeUpload)
	g.DELETE("/uploads/:id", h.handleCancelUploadSession)

	do := func(method, path string, body []byte, hdr map[string]string) (*httptest.ResponseRecorder, map[string]any) {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w, out
	}

	w, conf := do("GET", "/api/uploads/config", nil, nil)
	if w.Code != 200 || conf["max_chunk_size_bytes"].(float64) != 50*mib || conf["chunk_size_bytes"].(float64) != mib {
		t.Fatalf("config: %d %v", w.Code, conf)
	}

	if conf["min_chunk_size_bytes"].(float64) != 1*mib || conf["target_chunk_seconds"].(float64) != uploadsession.TargetChunkSeconds {
		t.Fatalf("auto-size guidance missing from config: %v", conf)
	}

	// Speed probe: the body is read and discarded, nothing is stored; oversized
	// probes are refused.
	w, pr := do("POST", "/api/uploads/probe", make([]byte, 2*mib), nil)
	if w.Code != 200 || pr["bytes"].(float64) != 2*mib {
		t.Fatalf("probe: %d %v", w.Code, pr)
	}
	if w, _ := do("POST", "/api/uploads/probe", make([]byte, 9*mib), nil); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized probe must be refused, got %d", w.Code)
	}

	data := make([]byte, 3*mib+5)
	rand.Read(data)
	create, _ := json.Marshal(map[string]any{"filename": "../../etc/x.bin", "path": "/", "size": len(data), "chunk_size": mib})
	w, v := do("POST", "/api/uploads", create, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %v", w.Code, v)
	}
	id := v["id"].(string)
	if v["filename"] != "x.bin" {
		t.Fatalf("filename not sanitised: %v", v["filename"])
	}

	chunk := func(i int) []byte {
		e := (i + 1) * mib
		if e > len(data) {
			e = len(data)
		}
		return data[i*mib : e]
	}
	send := func(i int, hdr map[string]string) (*httptest.ResponseRecorder, map[string]any) {
		return do("PUT", "/api/uploads/"+id+"/chunks/"+strconv.Itoa(i), chunk(i), hdr)
	}
	ok := func(i int) map[string]string { return map[string]string{"X-Chunk-SHA256": shaHex(chunk(i))} }

	if w, _ := send(0, nil); w.Code != 400 {
		t.Fatalf("missing checksum header must be rejected, got %d", w.Code)
	}
	if w, _ := send(0, map[string]string{"X-Chunk-SHA256": shaHex([]byte("x"))}); w.Code != 422 {
		t.Fatalf("corrupt chunk: %d", w.Code)
	}
	if w, _ := send(0, ok(0)); w.Code != 200 {
		t.Fatalf("chunk 0: %d", w.Code)
	}
	if w, r := send(0, ok(0)); w.Code != 200 || r["duplicate"] != true {
		t.Fatalf("duplicate chunk: %d %v", w.Code, r)
	}
	// Resume point: status lists what the server already has.
	w, st := do("GET", "/api/uploads/"+id, nil, nil)
	if w.Code != 200 || st["confirmed_chunks"].(float64) != 1 || st["bytes_received"].(float64) != mib {
		t.Fatalf("status: %v", st)
	}
	for i := 1; i < 4; i++ {
		for tries := 0; ; tries++ {
			w, _ := send(i, ok(i))
			if w.Code == 200 {
				break
			}
			if w.Code != 429 || w.Header().Get("Retry-After") == "" || tries > 200 {
				t.Fatalf("chunk %d: %d %s", i, w.Code, w.Body.String())
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	// Another user is blind to the session.
	user = "mallory"
	if w, _ := do("GET", "/api/uploads/"+id, nil, nil); w.Code != 404 {
		t.Fatalf("cross-user access: %d", w.Code)
	}
	user = "alice"

	fin, _ := json.Marshal(map[string]string{"sha256": shaHex(data)})
	var last map[string]any
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		w, last = do("POST", "/api/uploads/"+id+"/finalize", fin, nil)
		if w.Code == 200 {
			break
		}
		if w.Code != 202 {
			t.Fatalf("finalize: %d %v", w.Code, last)
		}
		time.Sleep(30 * time.Millisecond)
	}
	if last["state"] != "finalized" || last["file_id"] == nil {
		t.Fatalf("not finalized: %v", last)
	}
	// Repeated finalize returns the same file.
	if _, again := do("POST", "/api/uploads/"+id+"/finalize", fin, nil); again["file_id"] != last["file_id"] {
		t.Fatalf("finalize not idempotent: %v vs %v", again["file_id"], last["file_id"])
	}
	if w, _ := do("DELETE", "/api/uploads/"+id, nil, nil); w.Code != 409 {
		t.Fatalf("cancelling a committed upload must be refused, got %d", w.Code)
	}
}

// While Telegram is not ready the upload API must answer with a retryable 503,
// never an HTML page or redirect a client could mistake for an acknowledgement.
func TestUploadAPINotReadyIsExplicit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	if err := database.InitDB("sqlite", t.TempDir()+"/t.db", ""); err != nil {
		t.Fatal(err)
	}
	defer database.RWDB.Close()
	defer database.RODB.Close()
	database.SetSetting("admin_username", "admin")

	r := gin.New()
	r.Use(setupCheckMiddleware())
	r.PUT("/api/uploads/:id/chunks/:index", func(c *gin.Context) { c.String(200, "should not run") })

	req := httptest.NewRequest("PUT", "/api/uploads/abc/chunks/7", bytes.NewReader([]byte("x")))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Fatalf("want 503 + Retry-After, got %d %q", w.Code, w.Header().Get("Retry-After"))
	}
	var body map[string]any
	if json.Unmarshal(w.Body.Bytes(), &body) != nil || body["error"] != "telegram_not_ready" {
		t.Fatalf("want JSON error, got %q", w.Body.String())
	}
}
