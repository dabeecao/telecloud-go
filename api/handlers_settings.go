package api

import (
	"fmt"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"telecloud/database"
	"telecloud/tgclient"
	"telecloud/utils"
	"telecloud/webdav"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

func (h *Handler) handlePostPassword(c *gin.Context) {
	oldPassword := c.PostForm("old_password")
	newPassword := c.PostForm("new_password")
	username := c.GetString("username")
	isAdmin := c.GetBool("is_admin")

	var dbHash string
	var forceChange int
	if isAdmin {
		dbHash = database.GetSetting("admin_password_hash")
	} else {
		err := database.RODB.Get(&dbHash, "SELECT password_hash FROM child_accounts WHERE username = ?", username)
		if err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": "user_not_found"})
			return
		}
		database.RODB.Get(&forceChange, "SELECT force_password_change FROM child_accounts WHERE username = ?", username)
	}

	// Only verify old password when NOT in force-change mode.
	// When forceChange==1, admin has already reset the password so we skip verification.
	if forceChange == 0 {
		if bcrypt.CompareHashAndPassword([]byte(dbHash), []byte(oldPassword)) != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": "incorrect_old_password"})
			return
		}
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_hash_password"})
		return
	}

	if isAdmin {
		database.SetSetting("admin_password_hash", string(hashedPassword))
	} else {
		database.DB.Exec("UPDATE child_accounts SET password_hash = ?, force_password_change = FALSE WHERE username = ?", string(hashedPassword), username)
	}

	currentToken, _ := c.Cookie("session_token")
	if currentToken == "" {
		sessionToken, err := database.CreateSession(username)
		if err == nil {
			c.SetCookie("session_token", sessionToken, int(database.SessionTTL.Seconds()), "/", "", isSecure(), true)
			currentToken = sessionToken
		}
	}

	// Force every OTHER device for this user to re-authenticate. The session
	// the user is changing the password from is preserved so they don't
	// log themselves out.
	_ = database.DeleteOtherSessions(username, currentToken)
	// Clear any cached WebDAV bcrypt result tied to the old password.
	webdav.InvalidateCache(username)
	database.LogAuditFromCtx(c, username, database.AuditActionPasswordChange, "", database.AuditStatusOK)

	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func (h *Handler) handlePostWebDAV(c *gin.Context) {
	if !c.GetBool("is_admin") {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	enabled := c.PostForm("enabled")
	if enabled == "true" {
		database.SetSetting("webdav_enabled", "true")
	} else {
		database.SetSetting("webdav_enabled", "false")
	}
	database.LogAuditFromCtx(c, c.GetString("username"), database.AuditActionWebDAVToggle, "enabled="+enabled, database.AuditStatusOK)
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func (h *Handler) handlePostUploadAPI(c *gin.Context) {
	if !c.GetBool("is_admin") {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	enabled := c.PostForm("enabled")
	if enabled == "true" {
		database.SetSetting("upload_api_enabled", "true")
	} else {
		database.SetSetting("upload_api_enabled", "false")
	}
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func (h *Handler) handleRegenerateAPIKey(c *gin.Context) {
	if !c.GetBool("is_admin") {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	newKey := uuid.New().String()
	database.SetSetting("upload_api_key", newKey)
	c.JSON(http.StatusOK, gin.H{"status": "success", "api_key": newKey})
}

func (h *Handler) handleDeleteAPIKey(c *gin.Context) {
	if !c.GetBool("is_admin") {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	database.SetSetting("upload_api_key", "")
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func (h *Handler) handlePostS3(c *gin.Context) {
	if !c.GetBool("is_admin") {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	enabled := c.PostForm("enabled")
	if enabled == "true" {
		database.SetSetting("s3_enabled", "true")
	} else {
		database.SetSetting("s3_enabled", "false")
	}
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func (h *Handler) handlePostS3Credentials(c *gin.Context) {
	accessKey := c.PostForm("access_key")
	secretKey := c.PostForm("secret_key")

	if c.GetBool("is_admin") {
		database.SetSetting("s3_access_key", accessKey)
		database.SetSetting("s3_secret_key", secretKey)
	} else {
		username := c.GetString("username")
		_, err := database.DB.Exec("UPDATE child_accounts SET s3_access_key = ?, s3_secret_key = ? WHERE username = ?", accessKey, secretKey, username)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func (h *Handler) handlePostChildS3(c *gin.Context) {
	if c.GetBool("is_admin") {
		c.JSON(http.StatusForbidden, gin.H{"error": "Admins should use global S3 toggle"})
		return
	}
	username := c.GetString("username")
	enabled := c.PostForm("enabled") == "true"

	if enabled && database.GetSetting("s3_enabled") != "true" {
		c.JSON(http.StatusForbidden, gin.H{"error": "ADMIN_DISABLED"})
		return
	}

	val := 0
	if enabled {
		val = 1
	}
	_, err := database.DB.Exec("UPDATE child_accounts SET s3_enabled = ? WHERE username = ?", val, username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func (h *Handler) handleGetUserSettings(c *gin.Context) {
	username := c.GetString("username")
	theme := database.GetUserSetting(username, "theme")
	c.JSON(http.StatusOK, gin.H{
		"theme": theme,
	})
}

func (h *Handler) handlePostUserTheme(c *gin.Context) {
	username := c.GetString("username")
	theme := c.PostForm("theme")
	err := database.SetUserSetting(username, "theme", theme)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save theme"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func (h *Handler) handleGetBotUserSettings(c *gin.Context) {
	username := c.GetString("username")
	telegramUserID := database.GetUserSetting(username, "telegram_user_id")
	botPoolUploadFolder := database.GetUserSetting(username, "bot_pool_upload_folder")
	if botPoolUploadFolder == "" {
		botPoolUploadFolder = "TelegramUpload"
	}
	c.JSON(http.StatusOK, gin.H{
		"telegram_user_id":       telegramUserID,
		"bot_pool_upload_folder": botPoolUploadFolder,
	})
}

func (h *Handler) handlePostBotUserSettings(c *gin.Context) {
	username := c.GetString("username")
	var req struct {
		TelegramUserID      string `json:"telegram_user_id"`
		BotPoolUploadFolder string `json:"bot_pool_upload_folder"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	req.TelegramUserID = strings.TrimSpace(req.TelegramUserID)
	req.BotPoolUploadFolder = strings.TrimSpace(req.BotPoolUploadFolder)

	// Validate telegram_user_id is numeric string if not empty
	if req.TelegramUserID != "" {
		if _, err := strconv.ParseInt(req.TelegramUserID, 10, 64); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "err_invalid_telegram_user_id"})
			return
		}

		// Verify uniqueness across all accounts where key = 'telegram_user_id' and username != current_username
		var existingUsername string
		query := "SELECT username FROM user_settings WHERE `key` = 'telegram_user_id' AND value = ? AND username != ?"
		if database.IsPostgres() {
			query = "SELECT username FROM user_settings WHERE \"key\" = 'telegram_user_id' AND value = ? AND username != ?"
		} else if !database.IsMySQL() {
			query = "SELECT username FROM user_settings WHERE key = 'telegram_user_id' AND value = ? AND username != ?"
		}
		err := database.RODB.Get(&existingUsername, query, req.TelegramUserID, username)
		if err == nil && existingUsername != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "err_telegram_user_id_exists"})
			return
		}
	}

	if req.BotPoolUploadFolder == "" {
		req.BotPoolUploadFolder = "TelegramUpload"
	}

	// Clean up upload folder name: no leading/trailing slashes, no dot-dots
	folder := path.Clean(req.BotPoolUploadFolder)
	if folder == "." || folder == "/" || strings.HasPrefix(folder, "..") {
		folder = "TelegramUpload"
	}

	// If current user is the admin, ensure the folder doesn't conflict with any child account paths
	isAdmin := c.GetBool("is_admin")
	if isAdmin {
		if isChildAccountPath("/" + folder) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "err_folder_collides_child"})
			return
		}
	}

	err := database.SetUserSetting(username, "telegram_user_id", req.TelegramUserID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save telegram user id"})
		return
	}

	err = database.SetUserSetting(username, "bot_pool_upload_folder", folder)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save bot pool upload folder"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func (h *Handler) handleGetChildAPIKey(c *gin.Context) {
	if c.GetBool("is_admin") {
		c.JSON(http.StatusForbidden, gin.H{"error": "Admins should use global API key"})
		return
	}
	username := c.GetString("username")
	var apiKey *string
	err := database.RODB.Get(&apiKey, "SELECT api_key FROM child_accounts WHERE username = ?", username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"api_key": apiKey})
}

func (h *Handler) handlePostChildAPIKey(c *gin.Context) {
	if c.GetBool("is_admin") {
		c.JSON(http.StatusForbidden, gin.H{"error": "Admins should use global API key"})
		return
	}
	username := c.GetString("username")
	newKey := utils.GenerateRandomString(32)
	_, err := database.DB.Exec("UPDATE child_accounts SET api_key = ? WHERE username = ?", newKey, username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"api_key": newKey})
}

func (h *Handler) handleDeleteChildAPIKey(c *gin.Context) {
	if c.GetBool("is_admin") {
		c.JSON(http.StatusForbidden, gin.H{"error": "Admins should use global API key"})
		return
	}
	username := c.GetString("username")
	_, err := database.DB.Exec("UPDATE child_accounts SET api_key = NULL WHERE username = ?", username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func (h *Handler) handlePostChildWebDAV(c *gin.Context) {
	if c.GetBool("is_admin") {
		c.JSON(http.StatusForbidden, gin.H{"error": "Admins should use global WebDAV toggle"})
		return
	}
	username := c.GetString("username")
	enabled := c.PostForm("enabled") == "true"

	if enabled && database.GetSetting("webdav_enabled") != "true" {
		c.JSON(http.StatusForbidden, gin.H{"error": "ADMIN_DISABLED"})
		return
	}

	val := 0
	if enabled {
		val = 1
	}
	_, err := database.DB.Exec("UPDATE child_accounts SET webdav_enabled = ? WHERE username = ?", val, username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func (h *Handler) handlePostChildAPI(c *gin.Context) {
	if c.GetBool("is_admin") {
		c.JSON(http.StatusForbidden, gin.H{"error": "Admins should use global API toggle"})
		return
	}
	username := c.GetString("username")
	enabled := c.PostForm("enabled") == "true"

	if enabled && database.GetSetting("upload_api_enabled") != "true" {
		c.JSON(http.StatusForbidden, gin.H{"error": "ADMIN_DISABLED"})
		return
	}

	val := 0
	if enabled {
		val = 1
	}
	_, err := database.DB.Exec("UPDATE child_accounts SET api_enabled = ? WHERE username = ?", val, username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func (h *Handler) handlePostWebAuthn(c *gin.Context) {
	if !c.GetBool("is_admin") {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	rpid := c.PostForm("rpid")
	origins := c.PostForm("origins")

	database.SetSetting("webauthn_rpid", rpid)
	database.SetSetting("webauthn_rporigin", origins)

	originList := []string{}
	if origins != "" {
		originList = strings.Split(origins, ",")
	}

	InitWebAuthn(rpid, originList)

	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func (h *Handler) handleGetUsers(c *gin.Context) {
	if !c.GetBool("is_admin") {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	var users []database.User
	err := database.RODB.Select(&users, "SELECT id, username, created_at FROM child_accounts ORDER BY id DESC")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	for i := range users {
		var fileCount int
		var totalSize int64
		owner := users[i].Username
		database.RODB.Get(&fileCount, "SELECT COUNT(*) FROM files WHERE owner = ? AND is_folder = FALSE", owner)
		database.RODB.Get(&totalSize, "SELECT COALESCE(SUM(size), 0) FROM files WHERE owner = ? AND is_folder = FALSE", owner)
		users[i].FileCount = fileCount
		users[i].TotalSize = totalSize
	}

	c.JSON(http.StatusOK, gin.H{"users": users})
}

func (h *Handler) handlePostUser(c *gin.Context) {
	if !c.GetBool("is_admin") {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	username := c.PostForm("username")
	password := c.PostForm("password")
	if username == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "username required"})
		return
	}
	if password == "" {
		password = utils.GenerateRandomString(16)
	}

	validUsername := regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
	if !validUsername.MatchString(username) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_username_format", "message": "Username can only contain alphanumeric characters, dots, underscores and hyphens"})
		return
	}

	adminUsername := database.GetSetting("admin_username")
	if strings.EqualFold(username, adminUsername) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "username cannot be the same as admin username"})
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to hash password"})
		return
	}

	tx, err := database.DB.Beginx()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start transaction"})
		return
	}
	defer tx.Rollback()

	var folderCount int
	folderQuery := "SELECT COUNT(*) FROM files WHERE path = '/' AND filename = ? COLLATE NOCASE AND is_folder = TRUE"
	if database.IsMySQL() || database.IsPostgres() {
		folderQuery = "SELECT COUNT(*) FROM files WHERE path = '/' AND LOWER(filename) = LOWER(?) AND is_folder = TRUE"
	}
	err = tx.Get(&folderCount, folderQuery, username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	if folderCount > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "folder_exists", "message": "A folder with this name already exists in root directory"})
		return
	}

	var userExists int
	userQuery := "SELECT COUNT(*) FROM child_accounts WHERE username = ? COLLATE NOCASE"
	if database.IsMySQL() || database.IsPostgres() {
		userQuery = "SELECT COUNT(*) FROM child_accounts WHERE LOWER(username) = LOWER(?)"
	}
	err = tx.Get(&userExists, userQuery, username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	if userExists > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "username_exists", "message": "Username already exists"})
		return
	}

	_, err = tx.Exec("INSERT INTO child_accounts (username, password_hash, force_password_change) VALUES (?, ?, TRUE)", username, string(hashedPassword))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create user record"})
		return
	}

	_, err = tx.Exec("INSERT INTO files (filename, path, is_folder, owner) VALUES (?, '/', TRUE, ?)", username, username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create folder"})
		return
	}

	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit transaction"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "temp_password": password})
}

func (h *Handler) handleDeleteUser(c *gin.Context) {
	if !c.GetBool("is_admin") {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	username := c.Param("username")

	tx, err := database.DB.Beginx()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer tx.Rollback()

	timestamp := time.Now().Format("20060102_150405")
	newFolderName := fmt.Sprintf("deleted_%s_%s", username, timestamp)
	adminUsername := c.GetString("username")

	_, err = tx.Exec("UPDATE files SET filename = ?, owner = ? WHERE path = '/' AND filename = ? AND is_folder = TRUE AND owner = ?", newFolderName, adminUsername, username, username)
	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to rename user folder"})
		return
	}

	oldPrefix := "/" + username
	newPrefix := "/" + newFolderName

	_, err = tx.Exec("UPDATE files SET path = ?, owner = ? WHERE path = ? AND owner = ?", newPrefix, adminUsername, oldPrefix, username)
	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update direct file paths"})
		return
	}

	_, err = tx.Exec("UPDATE files SET path = "+database.ConcatPathSQL()+", owner = ? WHERE path LIKE ? AND owner = ?", newPrefix, len(oldPrefix)+1, adminUsername, oldPrefix+"/%", username)
	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update nested file paths"})
		return
	}

	_, err = tx.Exec("DELETE FROM child_accounts WHERE username = ?", username)
	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if _, err = tx.Exec("DELETE FROM sessions WHERE username = ?", username); err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete user sessions"})
		return
	}
	if _, err = tx.Exec("DELETE FROM passkeys WHERE username = ?", username); err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete user passkeys"})
		return
	}

	if _, err = tx.Exec("DELETE FROM user_settings WHERE username = ?", username); err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete user settings"})
		return
	}

	if _, err = tx.Exec("DELETE FROM upload_chunks WHERE task_id IN (SELECT id FROM upload_tasks WHERE owner = ?)", username); err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete user upload chunks"})
		return
	}

	if _, err = tx.Exec("DELETE FROM upload_tasks WHERE owner = ?", username); err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete user upload tasks"})
		return
	}

	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit transaction"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func (h *Handler) handlePostUserResetPass(c *gin.Context) {
	if !c.GetBool("is_admin") {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	username := c.Param("username")

	tempPassword := utils.GenerateRandomString(16)
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(tempPassword), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to hash password"})
		return
	}

	_, err = database.DB.Exec("UPDATE child_accounts SET password_hash = ?, force_password_change = TRUE WHERE username = ?", string(hashedPassword), username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reset password"})
		return
	}

	database.DB.Exec("DELETE FROM sessions WHERE username = ?", username)

	c.JSON(http.StatusOK, gin.H{"status": "success", "temp_password": tempPassword})
}

func (h *Handler) handlePostBotPool(c *gin.Context) {
	if !c.GetBool("is_admin") {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	var req struct {
		Tokens       []string `json:"tokens"`
		AdminIDs     string   `json:"admin_ids"`
		UploadFolder string   `json:"upload_folder"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	type BotVerifyResult struct {
		Token  string `json:"token"`
		Status string `json:"status"` // "success" or "error"
		Error  string `json:"error,omitempty"`
	}

	results := make([]BotVerifyResult, len(req.Tokens))

	// Create a map of existing active/healthy tokens to skip re-verification
	existingTokens := make(map[string]bool)
	tgclient.BotPoolMu.RLock()
	for _, bot := range tgclient.BotPool {
		if !bot.Deleted {
			existingTokens[bot.Token] = true
		}
	}
	tgclient.BotPoolMu.RUnlock()

	verifiedCount := 0
	for i, token := range req.Tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			results[i] = BotVerifyResult{Token: token, Status: "error", Error: "Empty token"}
			continue
		}

		// Skip verification if token is already active and verified
		if existingTokens[token] {
			results[i] = BotVerifyResult{Token: token, Status: "success"}
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
	database.SetSetting("bot_admin_ids", req.AdminIDs)
	database.SetSetting("bot_pool_upload_folder", req.UploadFolder)

	// Dynamic update in-memory
	tgclient.UpdateBotPool(h.cfg, validTokens)

	c.JSON(http.StatusOK, gin.H{
		"results": results,
	})
}

func (h *Handler) handlePostRestart(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "restarting"})
	go h.restartApp()
}
