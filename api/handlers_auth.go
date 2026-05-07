package api

import (
	"net/http"
	"strconv"
	"telecloud/database"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

func (h *Handler) handleGetLogin(c *gin.Context) {
	token, _ := c.Cookie("session_token")
	var sessionUsername string
	if token != "" {
		database.DB.Get(&sessionUsername, "SELECT username FROM sessions WHERE token = ?", token)
	}
	if token != "" && sessionUsername != "" {
		c.Redirect(http.StatusFound, "/")
		return
	}
	setCSRFCookie(c)
	c.HTML(http.StatusOK, "login.html", gin.H{
		"version": h.cfg.Version,
	})
}

func (h *Handler) handlePostLogin(c *gin.Context) {
	ip := c.ClientIP()
	val, _ := loginAttempts.Load(ip)
	var att loginAttempt
	if val != nil {
		att = val.(loginAttempt)
		if att.count >= 5 && time.Since(att.last) < 15*time.Minute {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "too_many_requests"})
			return
		}
	}

	username := c.PostForm("username")
	password := c.PostForm("password")

	dbUser := database.GetSetting("admin_username")
	dbHash := database.GetSetting("admin_password_hash")

	var authSuccess bool
	var forceChange bool
	if username == dbUser && bcrypt.CompareHashAndPassword([]byte(dbHash), []byte(password)) == nil {
		authSuccess = true
	} else {
		var child struct {
			Hash        string `db:"password_hash"`
			ForceChange int    `db:"force_password_change"`
		}
		err := database.DB.Get(&child, "SELECT password_hash, force_password_change FROM child_accounts WHERE username = ?", username)
		if err == nil && bcrypt.CompareHashAndPassword([]byte(child.Hash), []byte(password)) == nil {
			authSuccess = true
			forceChange = child.ForceChange == 1
		}
	}

	if authSuccess {
		if forceChange {
			c.JSON(http.StatusOK, gin.H{"status": "force_password_change", "username": username})
			return
		}
		loginAttempts.Delete(ip) // Reset on success
		sessionToken := uuid.New().String()
		_, err := database.DB.Exec("INSERT INTO sessions (token, username) VALUES (?, ?)", sessionToken, username)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create session"})
			return
		}
		c.SetCookie("session_token", sessionToken, 3600*24*30, "/", "", isSecure(), true)
		c.JSON(http.StatusOK, gin.H{"status": "success"})
		return
	}

	// On failure
	att.count++
	att.last = time.Now()
	loginAttempts.Store(ip, att)

	// Artificial delay to thwart fast scripts
	time.Sleep(1 * time.Second)

	if att.count >= 5 {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "ip_blocked"})
	} else {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
	}
}

func (h *Handler) handleLogout(c *gin.Context) {
	token, _ := c.Cookie("session_token")
	if token != "" {
		database.DB.Exec("DELETE FROM sessions WHERE token = ?", token)
	}
	c.SetCookie("session_token", "", -1, "/", "", isSecure(), true)
	c.SetCookie(csrfCookieName, "", -1, "/", "", isSecure(), false)
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func (h *Handler) handleGetResetAdmin(c *gin.Context) {
	token := c.Query("token")
	dbToken := database.GetSetting("admin_reset_token")
	expiryStr := database.GetSetting("admin_reset_expiry")

	if token == "" || token != dbToken {
		c.String(http.StatusForbidden, "Invalid token")
		return
	}

	expiry, _ := strconv.ParseInt(expiryStr, 10, 64)
	if time.Now().Unix() > expiry {
		c.String(http.StatusForbidden, "Token expired")
		return
	}

	setCSRFCookie(c)
	c.HTML(http.StatusOK, "reset-admin.html", gin.H{
		"version": h.cfg.Version,
	})
}

func (h *Handler) handlePostResetAdmin(c *gin.Context) {
	token := c.PostForm("token")
	password := c.PostForm("password")

	dbToken := database.GetSetting("admin_reset_token")
	expiryStr := database.GetSetting("admin_reset_expiry")

	if token == "" || token != dbToken {
		c.JSON(http.StatusForbidden, gin.H{"error": "invalid_token"})
		return
	}

	expiry, _ := strconv.ParseInt(expiryStr, 10, 64)
	if time.Now().Unix() > expiry {
		c.JSON(http.StatusForbidden, gin.H{"error": "token_expired"})
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_hash_password"})
		return
	}

	database.SetSetting("admin_password_hash", string(hashedPassword))
	database.DeleteSetting("admin_reset_token")
	database.DeleteSetting("admin_reset_expiry")

	// Clear admin sessions
	adminUser := database.GetSetting("admin_username")
	if adminUser != "" {
		database.DB.Exec("DELETE FROM sessions WHERE username = ?", adminUser)
	}

	c.JSON(http.StatusOK, gin.H{"status": "success"})
}
