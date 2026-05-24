package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"telecloud/database"
	"telecloud/tgclient"

	"github.com/gin-gonic/gin"
)

func (h *Handler) handleGetBackupStatus(c *gin.Context) {
	if !c.GetBool("is_admin") {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	info := tgclient.GetBackupInfo()
	c.JSON(http.StatusOK, info)
}

func (h *Handler) handlePostBackup(c *gin.Context) {
	if !c.GetBool("is_admin") {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	go tgclient.PerformBackup(context.Background(), h.cfg)

	c.JSON(http.StatusOK, gin.H{"status": "started"})
}

func (h *Handler) handlePostBackupToggle(c *gin.Context) {
	if !c.GetBool("is_admin") {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	enabled := c.PostForm("enabled")
	if enabled == "true" {
		database.SetSetting("backup_enabled", "true")
	} else {
		database.SetSetting("backup_enabled", "false")
	}
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func (h *Handler) handlePostRestore(c *gin.Context) {
	if !c.GetBool("is_admin") {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	file, err := c.FormFile("backup_file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing backup_file"})
		return
	}

	// Create temp path to save the uploaded file
	tempFile := filepath.Join(h.cfg.TempDir, "uploaded_backup_"+filepath.Base(file.Filename))
	if err := c.SaveUploadedFile(file, tempFile); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save uploaded file"})
		return
	}
	defer os.Remove(tempFile)

	// Call PerformRestore
	if err := tgclient.PerformRestore(h.cfg, tempFile); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Successful restore! Return status and trigger restart
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "database_restored"})

	// Restart app to load the new database
	go h.restartApp()
}
