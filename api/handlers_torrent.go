package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"telecloud/tgclient"
	"telecloud/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// handleGetTorrentStatus reports whether torrent support is enabled.
func (h *Handler) handleGetTorrentStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"enabled": h.cfg.TorrentEnabled})
}

// handlePostTorrentAdd starts a torrent download task.
// Body (form): input=<magnet|.torrent-url>, path=<dest-path>
func (h *Handler) handlePostTorrentAdd(c *gin.Context) {
	if !h.cfg.TorrentEnabled {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "torrent_disabled"})
		return
	}

	input := c.PostForm("input")
	if input == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "input_required"})
		return
	}

	if !tgclient.IsValidTorrentInput(input) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "torrent_invalid_input"})
		return
	}

	// SSRF check — only for HTTP .torrent URLs; magnet links have no IP to check.
	if tgclient.IsValidURL(input) {
		if utils.IsPrivateIP(input) {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden_url"})
			return
		}

		// Check if URL returns a torrent file
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Head(input)
		if err != nil || resp.StatusCode != http.StatusOK {
			// Try GET if HEAD fails
			resp, err = client.Get(input)
			if err != nil || resp.StatusCode != http.StatusOK {
				c.JSON(http.StatusBadRequest, gin.H{"error": "torrent_url_unreachable"})
				return
			}
			resp.Body.Close()
		}
		contentType := resp.Header.Get("Content-Type")
		if contentType != "application/x-bittorrent" && !utils.HasTorrentExtension(input) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "not_a_torrent_file"})
			return
		}
	}

	destPath := c.PostForm("path")
	if destPath == "" {
		destPath = "/"
	}

	username := c.GetString("username")
	isAdmin := c.GetBool("is_admin")
	dbPath := mapPath(destPath, username, isAdmin)

	if isAdmin && isChildAccountPath(dbPath) {
		c.JSON(http.StatusForbidden, gin.H{"error": "admin_forbidden_child_path"})
		return
	}

	taskID := c.PostForm("task_id")
	if taskID == "" {
		taskID = fmt.Sprintf("torrent_%d", time.Now().UnixNano())
	}

	go tgclient.ProcessTorrentUpload(context.Background(), input, dbPath, taskID, h.cfg, username)

	c.JSON(http.StatusOK, gin.H{"status": "started", "task_id": taskID})
}

// handlePostTorrentUpload handles uploading a .torrent file.
func (h *Handler) handlePostTorrentUpload(c *gin.Context) {
	if !h.cfg.TorrentEnabled {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "torrent_disabled"})
		return
	}

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no_file_uploaded"})
		return
	}
	defer file.Close()

	if !utils.HasTorrentExtension(header.Filename) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_file_extension"})
		return
	}

	tempFile := filepath.Join(h.cfg.TempDir, "torrent_"+uuid.New().String()+".torrent")
	out, err := os.Create(tempFile)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_save_torrent"})
		return
	}
	defer out.Close()

	_, err = io.Copy(out, file)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_save_torrent"})
		return
	}

	destPath := c.PostForm("path")
	if destPath == "" {
		destPath = "/"
	}
	username := c.GetString("username")
	isAdmin := c.GetBool("is_admin")
	dbPath := mapPath(destPath, username, isAdmin)

	taskID := fmt.Sprintf("torrent_file_%d", time.Now().UnixNano())
	go func() {
		defer os.Remove(tempFile)
		tgclient.ProcessTorrentUpload(context.Background(), tempFile, dbPath, taskID, h.cfg, username)
	}()

	c.JSON(http.StatusOK, gin.H{"status": "started", "task_id": taskID})
}
