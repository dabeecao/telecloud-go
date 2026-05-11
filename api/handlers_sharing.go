package api

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"telecloud/database"
	"telecloud/tgclient"
	"telecloud/utils"

	"github.com/gin-gonic/gin"
)

func (h *Handler) handleGetSharedFile(c *gin.Context) {
	token := c.Param("token")
	var item database.File
	if err := database.RODB.Get(&item, "SELECT id, filename, size, created_at, thumb_path, is_folder, path FROM files WHERE share_token = ?", token); err != nil {
		c.HTML(http.StatusNotFound, "error.html", gin.H{
			"error_message": "File not found or link has been revoked.",
			"version":       h.cfg.Version,
		})
		return
	}

	if item.IsFolder {
		c.HTML(http.StatusOK, "share_folder.html", gin.H{
			"filename":   item.Filename,
			"created_at": item.CreatedAt.Format("2006-01-02 15:04:05"),
			"token":      token,
			"version":    h.cfg.Version,
		})
		return
	}

	hasThumb := false
	if item.ThumbPath != nil {
		if _, err := os.Stat(*item.ThumbPath); err == nil {
			hasThumb = true
		}
	}

	c.HTML(http.StatusOK, "share.html", gin.H{
		"filename":       item.Filename,
		"size":           item.Size,
		"formatted_size": formatBytes(item.Size),
		"created_at":     item.CreatedAt.Format("2006-01-02 15:04:05"),
		"token":          token,
		"has_thumb":      hasThumb,
		"version":        h.cfg.Version,
	})
}

func (h *Handler) handleGetSharedFolderFiles(c *gin.Context) {
	token := c.Param("token")
	var item database.File
	if err := database.RODB.Get(&item, "SELECT filename, path, is_folder FROM files WHERE share_token = ?", token); err != nil || !item.IsFolder {
		c.JSON(http.StatusNotFound, gin.H{"error": "Folder not found"})
		return
	}

	reqPath := c.Query("path")
	if reqPath == "" {
		reqPath = "/"
	}

	basePrefix := item.Path + "/" + item.Filename
	if item.Path == "/" {
		basePrefix = "/" + item.Filename
	}

	targetPath := basePrefix
	if reqPath != "/" {
		if !strings.HasPrefix(reqPath, "/") {
			reqPath = "/" + reqPath
		}
		targetPath = basePrefix + reqPath
	}

	var files []database.File
	err := database.RODB.Select(&files, "SELECT id, filename, path, size, created_at, is_folder, mime_type, thumb_path FROM files WHERE path = ? ORDER BY is_folder DESC, id DESC", targetPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var totalSize int64
	database.RODB.Get(&totalSize, "SELECT COALESCE(SUM(size), 0) FROM files WHERE (path = ? OR path LIKE ?) AND is_folder = 0", targetPath, targetPath+"/%")

	for i := range files {
		if files[i].ThumbPath != nil {
			if _, err := os.Stat(*files[i].ThumbPath); err == nil {
				files[i].HasThumb = true
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"files": files, "total_size": totalSize})
}

func (h *Handler) handleStreamSharedFile(c *gin.Context) {
	token := c.Param("token")
	var item database.File
	if err := database.RODB.Get(&item, "SELECT * FROM files WHERE share_token = ?", token); err != nil || item.IsFolder {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if item.MimeType != nil {
		mime := *item.MimeType
		if strings.HasSuffix(strings.ToLower(item.Filename), ".mkv") {
			mime = "video/webm"
		}
		c.Header("Content-Type", mime)
	}

	if err := tgclient.ServeTelegramFile(c.Request, c.Writer, item, h.cfg); err != nil {
		fmt.Printf("[SharedStream] Error serving file %s: %v\n", token, err)
	}

}

func (h *Handler) handleDownloadSharedFile(c *gin.Context) {
	token := c.Param("token")
	var item database.File
	if err := database.RODB.Get(&item, "SELECT * FROM files WHERE share_token = ?", token); err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, item.Filename))
	if item.MimeType != nil {
		c.Header("Content-Type", *item.MimeType)
	}
	c.SetCookie("dl_started", "1", 15, "/", "", false, false)

	tgclient.ServeTelegramFile(c.Request, c.Writer, item, h.cfg)
}

func (h *Handler) handleGetSharedThumb(c *gin.Context) {
	token := c.Param("token")
	var item database.File
	if err := database.RODB.Get(&item, "SELECT thumb_path FROM files WHERE share_token = ?", token); err != nil || item.ThumbPath == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.File(*item.ThumbPath)
}

func (h *Handler) handleStreamSharedFileInFolder(c *gin.Context) {
	token := c.Param("token")
	id, _ := strconv.Atoi(c.Param("id"))

	var folder database.File
	if err := database.RODB.Get(&folder, "SELECT filename, path, is_folder FROM files WHERE share_token = ?", token); err != nil || !folder.IsFolder {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	basePrefix := folder.Path + "/" + folder.Filename
	if folder.Path == "/" {
		basePrefix = "/" + folder.Filename
	}

	var item database.File
	if err := database.RODB.Get(&item, "SELECT * FROM files WHERE id = ?", id); err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if item.Path != basePrefix && !strings.HasPrefix(item.Path, basePrefix+"/") {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	if item.MimeType != nil {
		mime := *item.MimeType
		if strings.HasSuffix(strings.ToLower(item.Filename), ".mkv") {
			mime = "video/webm"
		}
		c.Header("Content-Type", mime)
	}

	tgclient.ServeTelegramFile(c.Request, c.Writer, item, h.cfg)
}

func (h *Handler) handleDownloadSharedFileInFolder(c *gin.Context) {
	token := c.Param("token")
	id, _ := strconv.Atoi(c.Param("id"))

	var folder database.File
	if err := database.RODB.Get(&folder, "SELECT filename, path, is_folder FROM files WHERE share_token = ?", token); err != nil || !folder.IsFolder {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	basePrefix := folder.Path + "/" + folder.Filename
	if folder.Path == "/" {
		basePrefix = "/" + folder.Filename
	}

	var item database.File
	if err := database.RODB.Get(&item, "SELECT * FROM files WHERE id = ?", id); err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if item.Path != basePrefix && !strings.HasPrefix(item.Path, basePrefix+"/") {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, item.Filename))
	if item.MimeType != nil {
		c.Header("Content-Type", *item.MimeType)
	}
	c.SetCookie("dl_started", "1", 15, "/", "", false, false)

	tgclient.ServeTelegramFile(c.Request, c.Writer, item, h.cfg)
}

func (h *Handler) handleGetSharedFileThumbInFolder(c *gin.Context) {
	token := c.Param("token")
	id, _ := strconv.Atoi(c.Param("id"))

	var folder database.File
	if err := database.RODB.Get(&folder, "SELECT filename, path, is_folder FROM files WHERE share_token = ?", token); err != nil || !folder.IsFolder {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	basePrefix := folder.Path + "/" + folder.Filename
	if folder.Path == "/" {
		basePrefix = "/" + folder.Filename
	}

	var item database.File
	if err := database.RODB.Get(&item, "SELECT thumb_path, path FROM files WHERE id = ?", id); err != nil || item.ThumbPath == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if item.Path != basePrefix && !strings.HasPrefix(item.Path, basePrefix+"/") {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	c.File(*item.ThumbPath)
}

func (h *Handler) handleGetDirectDownload(c *gin.Context) {
	directToken := c.Param("token")
	shareToken := utils.VerifyDirectToken(directToken)
	if shareToken == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Invalid token"})
		return
	}

	var item database.File
	if err := database.RODB.Get(&item, "SELECT * FROM files WHERE share_token = ?", *shareToken); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
		return
	}

	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, item.Filename))
	if item.MimeType != nil {
		c.Header("Content-Type", *item.MimeType)
	}

	tgclient.ServeTelegramFile(c.Request, c.Writer, item, h.cfg)
}
