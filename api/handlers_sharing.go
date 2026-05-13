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
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

func (h *Handler) checkShareAuth(c *gin.Context, item database.File) bool {
	if item.SharePassword == nil || *item.SharePassword == "" {
		return true
	}
	token := c.Param("token")
	authCookie, err := c.Cookie("share_auth_" + token)
	if err == nil && authCookie == *item.SharePassword {
		return true
	}
	return false
}

func (h *Handler) handleVerifySharePassword(c *gin.Context) {
	token := c.Param("token")
	password := c.PostForm("password")

	var item database.File
	if err := database.RODB.Get(&item, "SELECT share_password FROM files WHERE share_token = ? AND deleted_at IS NULL", token); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}

	if item.SharePassword == nil || *item.SharePassword == "" {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(*item.SharePassword), []byte(password)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "incorrect_password"})
		return
	}

	// Set cookie with the hash as value for verification in other handlers
	c.SetCookie("share_auth_"+token, *item.SharePassword, 3600*24, "/", "", false, true)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) handleGetSharedFile(c *gin.Context) {
	token := c.Param("token")
	var item database.File
	if err := database.RODB.Get(&item, "SELECT id, filename, size, created_at, thumb_path, is_folder, path, share_password FROM files WHERE share_token = ? AND deleted_at IS NULL AND (is_folder = 1 OR message_id IS NOT NULL)", token); err != nil {
		c.HTML(http.StatusNotFound, "error.html", gin.H{
			"error_message": "File not found or link has been revoked.",
			"version":       h.cfg.Version,
		})
		return
	}

	if !h.checkShareAuth(c, item) {
		c.HTML(http.StatusOK, "share_login.html", gin.H{
			"filename": item.Filename,
			"token":    token,
			"version":  h.cfg.Version,
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
	if err := database.RODB.Get(&item, "SELECT filename, path, is_folder, share_password FROM files WHERE share_token = ? AND deleted_at IS NULL", token); err != nil || !item.IsFolder {
		c.JSON(http.StatusNotFound, gin.H{"error": "Folder not found"})
		return
	}

	if !h.checkShareAuth(c, item) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "password_required"})
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
	err := database.RODB.Select(&files, "SELECT id, filename, path, size, created_at, is_folder, mime_type, thumb_path FROM files WHERE path = ? AND deleted_at IS NULL AND (is_folder = 1 OR message_id IS NOT NULL) ORDER BY is_folder DESC, id DESC", targetPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var totalSize int64
	database.RODB.Get(&totalSize, "SELECT COALESCE(SUM(size), 0) FROM files WHERE (path = ? OR path LIKE ?) AND is_folder = 0 AND message_id IS NOT NULL AND deleted_at IS NULL", targetPath, targetPath+"/%")

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
	if err := database.RODB.Get(&item, "SELECT * FROM files WHERE share_token = ? AND deleted_at IS NULL", token); err != nil || item.IsFolder {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if !h.checkShareAuth(c, item) {
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

	if err := tgclient.ServeTelegramFile(c.Request, c.Writer, item, h.cfg); err != nil {
		fmt.Printf("[SharedStream] Error serving file %s: %v\n", token, err)
	}

}

func (h *Handler) handleDownloadSharedFile(c *gin.Context) {
	token := c.Param("token")
	var item database.File
	if err := database.RODB.Get(&item, "SELECT * FROM files WHERE share_token = ? AND deleted_at IS NULL", token); err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if !h.checkShareAuth(c, item) {
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

func (h *Handler) handleGetSharedThumb(c *gin.Context) {
	token := c.Param("token")
	var item database.File
	if err := database.RODB.Get(&item, "SELECT thumb_path, share_password FROM files WHERE share_token = ? AND deleted_at IS NULL", token); err != nil || item.ThumbPath == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if !h.checkShareAuth(c, item) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	c.File(*item.ThumbPath)
}

func (h *Handler) handleStreamSharedFileInFolder(c *gin.Context) {
	token := c.Param("token")
	id, _ := strconv.Atoi(c.Param("id"))

	var folder database.File
	if err := database.RODB.Get(&folder, "SELECT filename, path, is_folder, share_password FROM files WHERE share_token = ? AND deleted_at IS NULL", token); err != nil || !folder.IsFolder {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if !h.checkShareAuth(c, folder) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	basePrefix := folder.Path + "/" + folder.Filename
	if folder.Path == "/" {
		basePrefix = "/" + folder.Filename
	}

	var item database.File
	if err := database.RODB.Get(&item, "SELECT * FROM files WHERE id = ? AND deleted_at IS NULL", id); err != nil {
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
	if err := database.RODB.Get(&folder, "SELECT filename, path, is_folder, share_password FROM files WHERE share_token = ? AND deleted_at IS NULL", token); err != nil || !folder.IsFolder {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if !h.checkShareAuth(c, folder) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	basePrefix := folder.Path + "/" + folder.Filename
	if folder.Path == "/" {
		basePrefix = "/" + folder.Filename
	}

	var item database.File
	if err := database.RODB.Get(&item, "SELECT * FROM files WHERE id = ? AND deleted_at IS NULL", id); err != nil {
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
	if err := database.RODB.Get(&folder, "SELECT filename, path, is_folder, share_password FROM files WHERE share_token = ? AND deleted_at IS NULL", token); err != nil || !folder.IsFolder {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if !h.checkShareAuth(c, folder) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	basePrefix := folder.Path + "/" + folder.Filename
	if folder.Path == "/" {
		basePrefix = "/" + folder.Filename
	}

	var item database.File
	if err := database.RODB.Get(&item, "SELECT thumb_path, path FROM files WHERE id = ? AND deleted_at IS NULL", id); err != nil || item.ThumbPath == nil {
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
	if err := database.RODB.Get(&item, "SELECT * FROM files WHERE share_token = ? AND deleted_at IS NULL", *shareToken); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
		return
	}

	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, item.Filename))
	if item.MimeType != nil {
		c.Header("Content-Type", *item.MimeType)
	}

	tgclient.ServeTelegramFile(c.Request, c.Writer, item, h.cfg)
}

func (h *Handler) handleShareFile(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_id"})
		return
	}
	password := c.PostForm("password")
	var item database.File
	username := c.GetString("username")
	if err := database.RODB.Get(&item, "SELECT path, is_folder FROM files WHERE id = ? AND owner = ?", id, username); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	var hashedPass *string
	if password != "" {
		h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err == nil {
			s := string(h)
			hashedPass = &s
		}
	}

	token := uuid.New().String()
	database.DB.Exec("UPDATE files SET share_token = ?, share_password = ? WHERE id = ?", token, hashedPass, id)

	resp := gin.H{"share_token": token}
	if !item.IsFolder {
		resp["direct_token"] = utils.GenerateDirectToken(token)
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) handleRevokeShare(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_id"})
		return
	}
	var item database.File
	username := c.GetString("username")
	if err := database.RODB.Get(&item, "SELECT path FROM files WHERE id = ? AND owner = ?", id, username); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	database.DB.Exec("UPDATE files SET share_token = NULL, share_password = NULL WHERE id = ?", id)
	c.JSON(http.StatusOK, gin.H{"status": "revoked"})
}
