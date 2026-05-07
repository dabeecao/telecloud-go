package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"telecloud/tgclient"
	"time"

	"github.com/gin-gonic/gin"
)

func (h *Handler) handleGetYTDLPStatus(c *gin.Context) {
	ytdlpEnabled := h.cfg.YTDLPPath != "disabled" && h.cfg.YTDLPPath != "disable"
	ffmpegEnabled := h.cfg.FFMPEGPath != "disabled" && h.cfg.FFMPEGPath != "disable"
	enabled := ytdlpEnabled && ffmpegEnabled
	c.JSON(http.StatusOK, gin.H{"enabled": enabled})
}

func (h *Handler) handleGetYTDLPCookiesStatus(c *gin.Context) {
	username := c.GetString("username")
	cookieFile := filepath.Join(h.cfg.CookiesDir, fmt.Sprintf("user_%s.txt", username))
	_, err := os.Stat(cookieFile)
	c.JSON(http.StatusOK, gin.H{"has_cookie": err == nil})
}

func (h *Handler) handlePostYTDLPCookies(c *gin.Context) {
	file, err := c.FormFile("cookie_file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file_required"})
		return
	}

	if file.Size > 2*1024*1024 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file_too_large"})
		return
	}

	src, err := file.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "open_failed"})
		return
	}
	defer src.Close()

	head := make([]byte, 100)
	n, _ := src.Read(head)
	headStr := string(head[:n])

	isNetscape := strings.Contains(headStr, "# Netscape HTTP Cookie File") || strings.Contains(headStr, "# HTTP Cookie File")
	isJSON := strings.HasPrefix(strings.TrimSpace(headStr), "[")

	if !isNetscape && !isJSON {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_cookie_format"})
		return
	}

	username := c.GetString("username")
	os.MkdirAll(h.cfg.CookiesDir, 0755)
	cookieFile := filepath.Join(h.cfg.CookiesDir, fmt.Sprintf("user_%s.txt", username))

	if err := c.SaveUploadedFile(file, cookieFile); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func (h *Handler) handleDeleteYTDLPCookies(c *gin.Context) {
	username := c.GetString("username")
	cookieFile := filepath.Join(h.cfg.CookiesDir, fmt.Sprintf("user_%s.txt", username))
	os.Remove(cookieFile)
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func (h *Handler) handleGetProxyImage(c *gin.Context) {
	targetURL := c.Query("url")
	if targetURL == "" {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if !tgclient.IsValidURL(targetURL) {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	client := &http.Client{
		Timeout: 15 * time.Second,
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), "GET", targetURL, nil)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/119.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Sec-Fetch-Dest", "image")
	req.Header.Set("Sec-Fetch-Mode", "no-cors")
	req.Header.Set("Sec-Fetch-Site", "cross-site")

	if u, err := url.Parse(targetURL); err == nil {
		req.Header.Set("Referer", u.Scheme+"://"+u.Host+"/")
	}

	resp, err := client.Do(req)
	if err != nil {
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.AbortWithStatus(resp.StatusCode)
		return
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType != "" && !strings.HasPrefix(contentType, "image/") && contentType != "application/octet-stream" {
		if strings.Contains(contentType, "text/html") || strings.Contains(contentType, "application/json") {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
	}

	if contentType == "" {
		contentType = "image/jpeg"
	}

	c.Header("Content-Type", contentType)
	c.Header("Cache-Control", "public, max-age=86400")
	c.Header("Cross-Origin-Resource-Policy", "cross-origin")
	io.Copy(c.Writer, resp.Body)
}

func (h *Handler) handlePostYTDLPFormats(c *gin.Context) {
	url := c.PostForm("url")
	if url == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url_required"})
		return
	}
	if !tgclient.IsValidURL(url) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_url_format"})
		return
	}
	username := c.GetString("username")
	info, err := tgclient.GetYTDLPFormats(url, h.cfg, username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, info)
}

func (h *Handler) handlePostYTDLPDownload(c *gin.Context) {
	url := c.PostForm("url")
	formatID := c.PostForm("format_id")
	downloadType := c.PostForm("download_type")
	path := c.PostForm("path")

	if url == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url_required"})
		return
	}
	if !tgclient.IsValidURL(url) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_url_format"})
		return
	}

	if path == "" {
		path = "/"
	}

	username := c.GetString("username")
	isAdmin := c.GetBool("is_admin")
	dbPath := mapPath(path, username, isAdmin)

	if isAdmin && isChildAccountPath(dbPath) {
		c.JSON(http.StatusForbidden, gin.H{"error": "admin_forbidden_child_path"})
		return
	}

	taskID := fmt.Sprintf("ytdlp_%d", time.Now().UnixNano())
	go tgclient.ProcessYTDLPUpload(context.Background(), url, formatID, dbPath, taskID, downloadType, h.cfg, username)

	c.JSON(http.StatusOK, gin.H{"status": "started", "task_id": taskID})
}
