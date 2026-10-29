package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"telecloud/database"
	"telecloud/tgclient"
	"telecloud/uploadsession"

	"github.com/gin-gonic/gin"
)

// Resumable upload sessions: see package uploadsession and docs/API.md.
//
//	GET    /api/uploads/config                  effective client settings
//	POST   /api/uploads/probe                   discard up to 8 MiB; lets a client measure its upload speed
//	GET    /api/uploads                         own unfinished sessions
//	POST   /api/uploads                         create a session
//	GET    /api/uploads/:id                     status (resume point)
//	PUT    /api/uploads/:id/chunks/:index       upload one chunk (raw body)
//	POST   /api/uploads/:id/finalize            verify and publish (idempotent)
//	DELETE /api/uploads/:id                     cancel and discard temporary data

func (h *Handler) uploadsEnabled(c *gin.Context) bool {
	if h.uploads == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "chunked_upload_unavailable"})
		return false
	}
	return true
}

func writeUploadErr(c *gin.Context, err error) {
	var ue *uploadsession.Error
	if errors.As(err, &ue) {
		if ue.RetryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(ue.RetryAfter))
		}
		c.JSON(ue.Status, gin.H{"error": ue.Code, "message": ue.Message})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
}

func (h *Handler) handleGetUploadsConfig(c *gin.Context) {
	if !h.uploadsEnabled(c) {
		return
	}
	c.JSON(http.StatusOK, h.uploads.ClientConfig())
}

// maxProbeBytes bounds one speed probe; probeSlots bounds concurrent probes so
// the endpoint cannot be used to hold many connections open.
const maxProbeBytes = 8 << 20

var probeSlots = make(chan struct{}, 4)

// handlePostUploadProbe reads and discards the body so a client can time an
// upload before choosing a chunk size. It stores nothing.
func (h *Handler) handlePostUploadProbe(c *gin.Context) {
	if !h.uploadsEnabled(c) {
		return
	}
	select {
	case probeSlots <- struct{}{}:
		defer func() { <-probeSlots }()
	default:
		c.Header("Retry-After", "2")
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "server_busy"})
		return
	}
	if c.Request.ContentLength > maxProbeBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "probe_too_large"})
		return
	}
	n, err := io.Copy(io.Discard, http.MaxBytesReader(c.Writer, c.Request.Body, maxProbeBytes))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "probe_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"bytes": n})
}

func (h *Handler) handleListUploads(c *gin.Context) {
	if !h.uploadsEnabled(c) {
		return
	}
	list, err := h.uploads.List(c.GetString("username"))
	if err != nil {
		writeUploadErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"sessions": list})
}

type createUploadRequest struct {
	Filename  string `json:"filename"`
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	MimeType  string `json:"mime_type"`
	Overwrite bool   `json:"overwrite"`
	ChunkSize int64  `json:"chunk_size"`
	SHA256    string `json:"sha256"`
}

func (h *Handler) handleCreateUpload(c *gin.Context) {
	if !h.uploadsEnabled(c) {
		return
	}
	var req createUploadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json"})
		return
	}
	filename := filepath.Base(strings.TrimSpace(req.Filename))
	if filename == "" || filename == "." || filename == ".." || filename == string(filepath.Separator) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_filename"})
		return
	}
	username := c.GetString("username")
	isAdmin := c.GetBool("is_admin")
	dbPath := mapPath(req.Path, username, isAdmin)

	// Same destination rules as the single-request upload.
	if isAdmin && isChildAccountPath(dbPath) {
		c.JSON(http.StatusForbidden, gin.H{"error": "admin_forbidden_child_path"})
		return
	}
	if isAdmin && dbPath == "/" {
		var count int
		database.RODB.Get(&count, "SELECT COUNT(*) FROM child_accounts WHERE username = ?", filename)
		if count > 0 {
			c.JSON(http.StatusForbidden, gin.H{"error": "filename_collides_child"})
			return
		}
	}

	v, err := h.uploads.Create(uploadsession.CreateParams{
		Owner: username, Filename: filename, Path: dbPath, MimeType: req.MimeType,
		Overwrite: req.Overwrite, TotalSize: req.Size, ChunkSize: req.ChunkSize, SHA256: strings.ToLower(req.SHA256),
	})
	if err != nil {
		writeUploadErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, v)
}

func (h *Handler) handleGetUpload(c *gin.Context) {
	if !h.uploadsEnabled(c) {
		return
	}
	v, err := h.uploads.Status(c.GetString("username"), c.Param("id"))
	if err != nil {
		writeUploadErr(c, err)
		return
	}
	c.JSON(http.StatusOK, v)
}

// maxChunkBody is the absolute ceiling for one request body: the hard chunk
// limit plus slack. The manager enforces the exact per-chunk length.
const maxChunkBody = 51 << 20

func (h *Handler) handlePutUploadChunk(c *gin.Context) {
	if !h.uploadsEnabled(c) {
		return
	}
	index, err := strconv.ParseInt(c.Param("index"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_chunk_index"})
		return
	}
	body := http.MaxBytesReader(c.Writer, c.Request.Body, maxChunkBody)
	res, err := h.uploads.ReceiveChunk(c.GetString("username"), c.Param("id"), index, body,
		c.Request.ContentLength, strings.ToLower(c.GetHeader("X-Chunk-SHA256")))
	if err != nil {
		writeUploadErr(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

func (h *Handler) handleFinalizeUpload(c *gin.Context) {
	if !h.uploadsEnabled(c) {
		return
	}
	var req struct {
		SHA256 string `json:"sha256"`
	}
	// An empty body is fine when the checksum was supplied at creation.
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json"})
			return
		}
	}
	v, err := h.uploads.Finalize(c.GetString("username"), c.Param("id"), strings.ToLower(req.SHA256))
	if err != nil {
		writeUploadErr(c, err)
		return
	}
	if v.State == uploadsession.StateFinalized {
		c.JSON(http.StatusOK, v)
		return
	}
	c.JSON(http.StatusAccepted, v) // still storing the last parts; poll GET /api/uploads/:id
}

func (h *Handler) handleCancelUploadSession(c *gin.Context) {
	if !h.uploadsEnabled(c) {
		return
	}
	v, err := h.uploads.Cancel(c.GetString("username"), c.Param("id"))
	if err != nil {
		writeUploadErr(c, err)
		return
	}
	c.JSON(http.StatusOK, v)
}

// regenerateThumbAsync builds a thumbnail for a freshly published upload.
func (h *Handler) regenerateThumbAsync(fileID int64, _ string, mime string) {
	if !strings.HasPrefix(mime, "image/") && !strings.HasPrefix(mime, "video/") && !strings.HasPrefix(mime, "audio/") {
		return
	}
	go func() {
		if _, err := tgclient.RegenerateFileThumbnail(context.Background(), fileID, h.cfg); err != nil {
			return
		}
	}()
}
