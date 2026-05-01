package api

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"telecloud/database"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
)

var (
	webAuthn *webauthn.WebAuthn
	// In-memory store for pending WebAuthn sessions (registration/authentication)
	// Key: session ID (cookie), Value: *webauthn.SessionData
	webAuthnSessions sync.Map
)

func InitWebAuthn(rpid string, origins []string) {
	if rpid == "" {
		rpid = "localhost"
	}
	if len(origins) == 0 {
		origins = []string{"http://localhost:8091", "http://localhost:8080"}
	}

	var err error
	webAuthn, err = webauthn.New(&webauthn.Config{
		RPDisplayName: "TeleCloud",
		RPID:          rpid,
		RPOrigins:     origins,
	})
	if err != nil {
		panic(err)
	}
}

type WebAuthnUser struct {
	ID          []byte
	Username    string
	Credentials []webauthn.Credential
}

func (u *WebAuthnUser) WebAuthnID() []byte {
	return u.ID
}

func (u *WebAuthnUser) WebAuthnName() string {
	return u.Username
}

func (u *WebAuthnUser) WebAuthnDisplayName() string {
	return u.Username
}

func (u *WebAuthnUser) WebAuthnIcon() string {
	return ""
}

func (u *WebAuthnUser) WebAuthnCredentials() []webauthn.Credential {
	return u.Credentials
}

func getWebAuthnUser(username string) (*WebAuthnUser, error) {
	// Generate a stable ID based on username if not already stored
	// For simplicity, we use the username bytes as ID
	userID := []byte(username)

	var creds []webauthn.Credential
	rows, err := database.DB.Queryx("SELECT credential_id, public_key, attestation_type, aaguid, sign_count, transports, backup_eligible, backup_state FROM passkeys WHERE username = ?", username)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var c struct {
				CredentialID    []byte  `db:"credential_id"`
				PublicKey       []byte  `db:"public_key"`
				AttestationType string  `db:"attestation_type"`
				AAGUID          []byte  `db:"aaguid"`
				SignCount       uint32  `db:"sign_count"`
				Transports      *string `db:"transports"`
				BackupEligible  bool    `db:"backup_eligible"`
				BackupState     bool    `db:"backup_state"`
			}
			if err := rows.StructScan(&c); err == nil {
				transports := []protocol.AuthenticatorTransport{}
				if c.Transports != nil && *c.Transports != "" {
					parts := strings.Split(*c.Transports, ",")
					for _, p := range parts {
						transports = append(transports, protocol.AuthenticatorTransport(p))
					}
				}
				creds = append(creds, webauthn.Credential{
					ID:              c.CredentialID,
					PublicKey:       c.PublicKey,
					AttestationType: c.AttestationType,
					Authenticator: webauthn.Authenticator{
						AAGUID:    c.AAGUID,
						SignCount: c.SignCount,
					},
					Transport: transports,
					Flags: webauthn.CredentialFlags{
						BackupEligible: c.BackupEligible,
						BackupState:    c.BackupState,
					},
				})
			}
		}
	}

	return &WebAuthnUser{
		ID:          userID,
		Username:    username,
		Credentials: creds,
	}, nil
}

func RegisterPasskeyBegin(c *gin.Context) {
	username := c.GetString("username")
	user, err := getWebAuthnUser(username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get user"})
		return
	}

	options, sessionData, err := webAuthn.BeginRegistration(user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	sessionID := uuid.New().String()
	webAuthnSessions.Store(sessionID, sessionData)
	c.SetCookie("webauthn_session", sessionID, 300, "/", "", false, true)

	c.JSON(http.StatusOK, options)
}

func RegisterPasskeyFinish(c *gin.Context) {
	username := c.GetString("username")
	user, err := getWebAuthnUser(username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get user"})
		return
	}

	sessionID, err := c.Cookie("webauthn_session")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing webauthn session"})
		return
	}

	val, ok := webAuthnSessions.Load(sessionID)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid webauthn session"})
		return
	}
	sessionData := val.(*webauthn.SessionData)
	webAuthnSessions.Delete(sessionID)

	credential, err := webAuthn.FinishRegistration(user, *sessionData, c.Request)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	transports := ""
	for i, t := range credential.Transport {
		if i > 0 {
			transports += ","
		}
		transports += string(t)
	}

	name := c.Query("name")
	if name == "" {
		name = "Passkey"
	}

	_, err = database.DB.Exec("INSERT INTO passkeys (username, credential_id, public_key, attestation_type, aaguid, sign_count, transports, backup_eligible, backup_state, name) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		username, credential.ID, credential.PublicKey, credential.AttestationType, credential.Authenticator.AAGUID, credential.Authenticator.SignCount, transports, credential.Flags.BackupEligible, credential.Flags.BackupState, name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save passkey"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func LoginPasskeyBegin(c *gin.Context) {
	username := c.Query("username")
	if username == "" {
		// Discoverable credentials (resident keys)
		options, sessionData, err := webAuthn.BeginDiscoverableLogin()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		sessionID := uuid.New().String()
		webAuthnSessions.Store(sessionID, sessionData)
		c.SetCookie("webauthn_session", sessionID, 300, "/", "", false, true)
		c.JSON(http.StatusOK, options)
		return
	}

	user, err := getWebAuthnUser(username)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	options, sessionData, err := webAuthn.BeginLogin(user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	sessionID := uuid.New().String()
	webAuthnSessions.Store(sessionID, sessionData)
	c.SetCookie("webauthn_session", sessionID, 300, "/", "", false, true)

	c.JSON(http.StatusOK, options)
}

func LoginPasskeyFinish(c *gin.Context) {
	sessionID, err := c.Cookie("webauthn_session")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing webauthn session"})
		return
	}

	val, ok := webAuthnSessions.Load(sessionID)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid webauthn session"})
		return
	}
	sessionData := val.(*webauthn.SessionData)
	webAuthnSessions.Delete(sessionID)

	var credential *webauthn.Credential
	var username string

	if len(sessionData.UserID) == 0 {
		handler := func(rawID, userHandle []byte) (webauthn.User, error) {
			var uName string
			err := database.DB.Get(&uName, "SELECT username FROM passkeys WHERE credential_id = ?", rawID)
			if err != nil {
				return nil, fmt.Errorf("user not found for this passkey")
			}
			
			adminUser := database.GetSetting("admin_username")
			if uName != adminUser {
				var count int
				database.DB.Get(&count, "SELECT COUNT(*) FROM child_accounts WHERE username = ?", uName)
				if count == 0 {
					return nil, fmt.Errorf("user no longer exists")
				}
			}

			userObj, err := getWebAuthnUser(uName)
			if err != nil {
				return nil, err
			}
			username = uName
			return userObj, nil
		}
		credential, err = webAuthn.FinishDiscoverableLogin(handler, *sessionData, c.Request)
	} else {
		username = string(sessionData.UserID)
		adminUser := database.GetSetting("admin_username")
		if username != adminUser {
			var count int
			database.DB.Get(&count, "SELECT COUNT(*) FROM child_accounts WHERE username = ?", username)
			if count == 0 {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "user no longer exists"})
				return
			}
		}

		userObj, err := getWebAuthnUser(username)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get user"})
			return
		}
		credential, err = webAuthn.FinishLogin(userObj, *sessionData, c.Request)
	}

	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	// Update sign count and backup state
	database.DB.Exec("UPDATE passkeys SET sign_count = ?, backup_state = ? WHERE credential_id = ?", 
		credential.Authenticator.SignCount, credential.Flags.BackupState, credential.ID)

	// Create session
	sessionToken := uuid.New().String()
	_, err = database.DB.Exec("INSERT INTO sessions (token, username) VALUES (?, ?)", sessionToken, username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create session"})
		return
	}
	c.SetCookie("session_token", sessionToken, 3600*24*30, "/", "", false, true)

	var forceChange int
	if username != database.GetSetting("admin_username") {
		database.DB.Get(&forceChange, "SELECT force_password_change FROM child_accounts WHERE username = ?", username)
	}

	if forceChange == 1 {
		c.JSON(http.StatusOK, gin.H{"status": "force_password_change", "username": username})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func ListPasskeys(c *gin.Context) {
	username := c.GetString("username")
	var pks = make([]struct {
		ID        int       `db:"id" json:"id"`
		Name      *string   `db:"name" json:"name"`
		CreatedAt time.Time `db:"created_at" json:"created_at"`
	}, 0)
	err := database.DB.Select(&pks, "SELECT id, name, created_at FROM passkeys WHERE username = ? ORDER BY created_at DESC", username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list passkeys"})
		return
	}
	c.JSON(http.StatusOK, pks)
}

func DeletePasskey(c *gin.Context) {
	username := c.GetString("username")
	id := c.Param("id")
	_, err := database.DB.Exec("DELETE FROM passkeys WHERE id = ? AND username = ?", id, username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete passkey"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func RenamePasskey(c *gin.Context) {
	username := c.GetString("username")
	id := c.Param("id")
	name := c.PostForm("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	_, err := database.DB.Exec("UPDATE passkeys SET name = ? WHERE id = ? AND username = ?", name, id, username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to rename passkey"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}
