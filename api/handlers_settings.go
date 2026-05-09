package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"telecloud/database"
	"telecloud/utils"
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
		database.DB.Exec("UPDATE child_accounts SET password_hash = ?, force_password_change = 0 WHERE username = ?", string(hashedPassword), username)
	}

	token, _ := c.Cookie("session_token")
	if token == "" {
		sessionToken := uuid.New().String()
		_, err = database.DB.Exec("INSERT INTO sessions (token, username) VALUES (?, ?)", sessionToken, username)
		if err == nil {
			c.SetCookie("session_token", sessionToken, 3600*24*30, "/", "", isSecure(), true)
		}
	}

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
		database.RODB.Get(&fileCount, "SELECT COUNT(*) FROM files WHERE owner = ? AND is_folder = 0", owner)
		database.RODB.Get(&totalSize, "SELECT COALESCE(SUM(size), 0) FROM files WHERE owner = ? AND is_folder = 0", owner)
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
	folderQuery := "SELECT COUNT(*) FROM files WHERE path = '/' AND filename = ? COLLATE NOCASE AND is_folder = 1"
	if database.IsMySQL() {
		folderQuery = "SELECT COUNT(*) FROM files WHERE path = '/' AND LOWER(filename) = LOWER(?) AND is_folder = 1"
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
	if database.IsMySQL() {
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

	_, err = tx.Exec("INSERT INTO child_accounts (username, password_hash, force_password_change) VALUES (?, ?, 1)", username, string(hashedPassword))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create user record"})
		return
	}

	_, err = tx.Exec("INSERT INTO files (filename, path, is_folder, owner) VALUES (?, '/', 1, ?)", username, username)
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

	_, err = tx.Exec("UPDATE files SET filename = ?, owner = ? WHERE path = '/' AND filename = ? AND is_folder = 1 AND owner = ?", newFolderName, adminUsername, username, username)
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

	_, err = database.DB.Exec("UPDATE child_accounts SET password_hash = ?, force_password_change = 1 WHERE username = ?", string(hashedPassword), username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reset password"})
		return
	}

	database.DB.Exec("DELETE FROM sessions WHERE username = ?", username)

	c.JSON(http.StatusOK, gin.H{"status": "success", "temp_password": tempPassword})
}
