package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"telecloud/config"
	"telecloud/database"
	"telecloud/tgclient"
	"telecloud/utils"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"rsc.io/qr"
)

func (h *Handler) handleGetSetup(c *gin.Context) {
	adminUser := database.GetSetting("admin_username")
	setCSRFCookie(c)

	dbAPIID := database.GetSetting("api_id")
	dbAPIHash := database.GetSetting("api_hash")

	apiIDShow := ""
	apiHashShow := ""
	if dbAPIID != "" {
		apiIDShow = dbAPIID
		apiHashShow = dbAPIHash
	}

	c.HTML(http.StatusOK, "setup.html", gin.H{
		"version":         h.cfg.Version,
		"api_id":          apiIDShow,
		"api_hash":        apiHashShow,
		"has_default_api": config.DefaultAPIIDStr != "" && config.DefaultAPIIDStr != "0",
		"log_group_id":    h.cfg.LogGroupID,
		"admin_exists":    adminUser != "",
	})
}

func (h *Handler) handlePostSetup(c *gin.Context) {
	adminUser := database.GetSetting("admin_username")
	if adminUser != "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "already setup"})
		return
	}

	// Same per-IP rate limit as /login to slow down anyone who slips past the
	// setup-token gate (e.g. local actors).
	ip := c.ClientIP()
	if v, _ := loginAttempts.Load(ip); v != nil {
		att := v.(loginAttempt)
		if att.count >= 5 && time.Since(att.last) < 15*time.Minute {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "too_many_requests"})
			return
		}
	}

	username := c.PostForm("username")
	password := c.PostForm("password")

	if username == "" || password == "" {
		bumpAttempt(ip)
		c.JSON(http.StatusBadRequest, gin.H{"error": "username and password required"})
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to hash password"})
		return
	}

	database.SetSetting("admin_username", username)
	database.SetSetting("admin_password_hash", string(hashedPassword))
	database.SetSetting("webdav_enabled", "false")

	// Create session
	sessionToken, err := database.CreateSession(username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create session"})
		return
	}
	c.SetCookie("session_token", sessionToken, int(database.SessionTTL.Seconds()), "/", "", isSecure(), true)

	database.LogAuditFromCtx(c, username, database.AuditActionSetupComplete, "", database.AuditStatusOK)
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func (h *Handler) handleSetupConfig(c *gin.Context) {
	restoreDefault := c.PostForm("restore_default") == "true"
	siteURL := strings.TrimRight(c.PostForm("site_url"), "/")

	if restoreDefault {
		database.DeleteSetting("api_id")
		database.DeleteSetting("api_hash")
		database.SetSetting("site_url", siteURL)

		h.cfg.APIID = 0
		h.cfg.APIHash = ""
		h.cfg.LoadFromDB(database.GetSetting)

		database.LogAuditFromCtx(c, database.GetSetting("admin_username"), database.AuditActionSetupConfig, "api_credentials_default", database.AuditStatusOK)
		c.JSON(http.StatusOK, gin.H{"status": "success"})
		return
	}

	apiID, _ := strconv.Atoi(c.PostForm("api_id"))
	apiHash := c.PostForm("api_hash")

	if apiID == 0 || apiHash == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "API_ID and API_HASH required"})
		return
	}

	database.SetSetting("api_id", strconv.Itoa(apiID))
	database.SetSetting("api_hash", apiHash)
	database.SetSetting("site_url", siteURL)

	h.cfg.APIID = apiID
	h.cfg.APIHash = apiHash

	database.LogAuditFromCtx(c, database.GetSetting("admin_username"), database.AuditActionSetupConfig, "api_credentials", database.AuditStatusOK)
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func (h *Handler) handleSetupTGPhone(c *gin.Context) {
	phone := c.PostForm("phone")
	tgclient.StartWebAuth(h.cfg)
	wa := tgclient.GetActiveWebAuth()
	if wa != nil {
		wa.SubmitPhone(phone)
	}
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func (h *Handler) handleSetupTGQR(c *gin.Context) {
	tgclient.StartQRAuth(h.cfg)
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func (h *Handler) handleSetupTGQRImage(c *gin.Context) {
	wa := tgclient.GetActiveWebAuth()
	if wa == nil || wa.GetQRURL() == "" {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	code, err := qr.Encode(wa.GetQRURL(), qr.M)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "QR generation failed"})
		return
	}

	c.Header("Content-Type", "image/png")
	c.Header("Cache-Control", "no-cache")
	c.Writer.Write(code.PNG())
}

func (h *Handler) handleSetupTGCode(c *gin.Context) {
	code := c.PostForm("code")
	wa := tgclient.GetActiveWebAuth()
	if wa != nil {
		wa.SubmitCode(code)
		c.JSON(http.StatusOK, gin.H{"status": "success"})
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": "SESSION_EXPIRED"})
	}
}

func (h *Handler) handleSetupTGPassword(c *gin.Context) {
	password := c.PostForm("password")
	wa := tgclient.GetActiveWebAuth()
	if wa != nil {
		wa.SubmitPassword(password)
		c.JSON(http.StatusOK, gin.H{"status": "success"})
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": "SESSION_EXPIRED"})
	}
}

func (h *Handler) handleSetupTGCancel(c *gin.Context) {
	wa := tgclient.GetActiveWebAuth()
	if wa != nil {
		wa.Cancel(fmt.Errorf("USER_CANCELLED"))
	}
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func (h *Handler) handleSetupTGTestLogGroup(c *gin.Context) {
	logGroupID := c.PostForm("log_group_id")
	if logGroupID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "log_group_id required"})
		return
	}

	database.SetSetting("log_group_id", logGroupID)
	h.cfg.LogGroupID = logGroupID

	tgclient.SkipBotPool = true
	if err := tgclient.VerifyLogGroup(c.Request.Context(), h.cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	h.startTG(h.cfg)
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func (h *Handler) handleSetupRestart(c *gin.Context) {
	adminUser := database.GetSetting("admin_username")
	if adminUser == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "setup not finished"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "restarting"})
	go h.restartApp()
}

func (h *Handler) handleSystemStatus(c *gin.Context) {
	total, free, _ := utils.GetDiskSpace(".")
	c.JSON(http.StatusOK, gin.H{
		"authorized":    tgclient.IsAuthorized(),
		"ready":         tgclient.IsSystemReady(),
		"running":       tgclient.IsRunning(),
		"storage_total": total,
		"storage_free":  free,
	})
}

func (h *Handler) handleSetupTGStatus(c *gin.Context) {
	wa := tgclient.GetActiveWebAuth()
	if !tgclient.IsAuthorized() && tgclient.LastAuthError != nil {
		errStr := tgclient.LastAuthError.Error()
		authState := "none"
		if wa != nil {
			authState = wa.GetState()
		}
		c.JSON(http.StatusOK, gin.H{"authorized": false, "authState": authState, "error": errStr})
		return
	}

	if tgclient.Client == nil && wa == nil {
		c.JSON(http.StatusOK, gin.H{"authorized": false, "authState": "none"})
		return
	}

	authState := "none"
	if wa != nil {
		authState = wa.GetState()
		transErr := wa.GetTransientErr()
		if transErr != "" {
			wa.SetTransientErr("")
		}
		c.JSON(http.StatusOK, gin.H{
			"authorized":      authState == "success",
			"authState":       authState,
			"qr_url":          wa.GetQRURL(),
			"transient_error": transErr,
		})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	status, err := tgclient.GetAuthStatus(ctx)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"authorized": false, "error": err.Error(), "authState": authState})
		return
	}
	c.JSON(http.StatusOK, gin.H{"authorized": status.Authorized, "authState": authState})
}

type VerifyBotsRequest struct {
	Tokens []string `json:"tokens"`
}

type BotVerifyResult struct {
	Token  string `json:"token"`
	Status string `json:"status"` // "success" or "error"
	Error  string `json:"error,omitempty"`
}

func (h *Handler) handleSetupTGVerifyBots(c *gin.Context) {
	adminUser := database.GetSetting("admin_username")
	if adminUser != "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "already setup"})
		return
	}

	var req VerifyBotsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	results := make([]BotVerifyResult, len(req.Tokens))

	verifiedCount := 0
	for i, token := range req.Tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			results[i] = BotVerifyResult{Token: token, Status: "error", Error: "Empty token"}
			continue
		}

		// If this is not the first token we actually verify, sleep to avoid flooding
		if verifiedCount > 0 {
			time.Sleep(1200 * time.Millisecond)
		}
		verifiedCount++

		err := tgclient.VerifyBotToken(c.Request.Context(), h.cfg, token)
		if err != nil {
			results[i] = BotVerifyResult{
				Token:  token,
				Status: "error",
				Error:  err.Error(),
			}
		} else {
			results[i] = BotVerifyResult{
				Token:  token,
				Status: "success",
			}
		}
	}

	var validTokens []string
	for _, res := range results {
		if res.Status == "success" {
			validTokens = append(validTokens, res.Token)
		}
	}

	tokensStr := strings.Join(validTokens, ",")
	database.SetSetting("bot_tokens", tokensStr)

	h.cfg.BotTokens = validTokens

	c.JSON(http.StatusOK, gin.H{
		"results": results,
	})
}
