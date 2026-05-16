package api

import (
	"context"
	"net/http"
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
