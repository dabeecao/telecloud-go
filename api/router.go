package api

import (
	"html/template"
	"io/fs"
	"net/http"
	"telecloud/config"
	"telecloud/s3"
	"telecloud/webdav"

	"github.com/gin-gonic/gin"
)

func SetupRouter(cfg *config.Config, contentFS fs.FS, startTG func(cfg *config.Config), restartApp func()) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()
	r.SetTrustedProxies([]string{"127.0.0.0/8", "::1", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"})

	templ := template.Must(template.New("").ParseFS(contentFS, "templates/*"))
	r.SetHTMLTemplate(templ)

	staticFS, err := fs.Sub(contentFS, "static")
	if err == nil {
		r.StaticFS("/static", http.FS(staticFS))
	}

	h := NewHandler(cfg, contentFS, startTG, restartApp)

	r.Use(securityHeadersMiddleware())
	r.Use(setupCheckMiddleware())

	// WebDAV Route
	webdavH := gin.WrapH(webdav.NewHandler(cfg))
	// S3 Route
	s3h := gin.WrapH(s3.NewHandler(cfg))

	methods := []string{
		"GET", "POST", "PUT", "PATCH", "HEAD", "OPTIONS", "DELETE", "CONNECT", "TRACE",
		"PROPFIND", "PROPPATCH", "MKCOL", "COPY", "MOVE", "LOCK", "UNLOCK",
	}
	for _, method := range methods {
		r.Handle(method, "/webdav", webdavH)
		r.Handle(method, "/webdav/*path", webdavH)
		r.Handle(method, "/s3", s3h)
		r.Handle(method, "/s3/*path", s3h)
	}

	// Public routes
	r.GET("/setup", h.handleGetSetup)
	r.POST("/setup", csrfMiddleware(), h.handlePostSetup)
	r.GET("/login", h.handleGetLogin)
	r.POST("/login", h.handlePostLogin)
	r.POST("/logout", csrfMiddleware(), h.handleLogout)
	r.GET("/reset-admin", h.handleGetResetAdmin)
	r.POST("/reset-admin", csrfMiddleware(), h.handlePostResetAdmin)

	// Public Setup APIs
	r.POST("/api/setup/config", csrfMiddleware(), h.handleSetupConfig)
	r.POST("/api/setup/tg/phone", csrfMiddleware(), h.handleSetupTGPhone)
	r.POST("/api/setup/tg/qr", csrfMiddleware(), h.handleSetupTGQR)
	r.GET("/api/setup/tg/qr/image", h.handleSetupTGQRImage)
	r.POST("/api/setup/tg/code", csrfMiddleware(), h.handleSetupTGCode)
	r.POST("/api/setup/tg/password", csrfMiddleware(), h.handleSetupTGPassword)
	r.POST("/api/setup/tg/cancel", csrfMiddleware(), h.handleSetupTGCancel)
	r.POST("/api/setup/tg/test-log-group", csrfMiddleware(), h.handleSetupTGTestLogGroup)
	r.POST("/api/setup/restart", csrfMiddleware(), h.handleSetupRestart)
	r.GET("/api/system/status", h.handleSystemStatus)
	r.GET("/api/setup/tg/status", h.handleSetupTGStatus)

	// Public Upload API
	r.POST("/api/upload-api/upload", h.handlePublicUploadAPI)
	r.POST("/api/upload-api/remote", h.handlePublicRemoteUploadAPI)
	r.GET("/api/upload-api/tasks/:task_id", h.handleGetPublicTaskStatusAPI)
	r.DELETE("/api/upload-api/tasks/:task_id", h.handleDeletePublicTaskAPI)
	r.POST("/api/upload-api/share", h.handlePublicShareAPI)

	// Passkey Login (Public)
	r.GET("/api/passkey/login/begin", LoginPasskeyBegin)
	r.POST("/api/passkey/login/finish", LoginPasskeyFinish)

	// Sharing Routes (Public)
	r.GET("/s/:token", h.handleGetSharedFile)
	r.GET("/s/:token/api/files", h.handleGetSharedFolderFiles)
	r.GET("/s/:token/stream", h.handleStreamSharedFile)
	r.GET("/s/:token/stream/:filename", h.handleStreamSharedFile)
	r.POST("/s/:token/dl", h.handleDownloadSharedFile)
	r.GET("/s/:token/thumb", h.handleGetSharedThumb)
	r.GET("/s/:token/file/:id/stream", h.handleStreamSharedFileInFolder)
	r.GET("/s/:token/file/:id/stream/:filename", h.handleStreamSharedFileInFolder)
	r.GET("/s/:token/file/:id/dl", h.handleDownloadSharedFileInFolder)
	r.GET("/s/:token/file/:id/thumb", h.handleGetSharedFileThumbInFolder)
	r.GET("/dl/:token", h.handleGetDirectDownload)

	// Protected API group
	api := r.Group("/api")
	api.Use(authMiddleware())
	api.Use(csrfMiddleware())
	{
		// Passkey Management
		api.GET("/passkey/register/begin", RegisterPasskeyBegin)
		api.POST("/passkey/register/finish", RegisterPasskeyFinish)
		api.GET("/passkeys", ListPasskeys)
		api.DELETE("/passkeys/:id", DeletePasskey)
		api.POST("/passkeys/:id/rename", RenamePasskey)

		// Settings
		api.POST("/settings/password", h.handlePostPassword)
		api.POST("/settings/webdav", h.handlePostWebDAV)
		api.POST("/settings/upload-api", h.handlePostUploadAPI)
		api.POST("/settings/upload-api/regenerate-key", h.handleRegenerateAPIKey)
		api.DELETE("/settings/upload-api/key", h.handleDeleteAPIKey)
		api.POST("/settings/s3", h.handlePostS3)
		api.POST("/settings/s3/credentials", h.handlePostS3Credentials)
		api.POST("/settings/child-s3", h.handlePostChildS3)
		api.GET("/settings/user", h.handleGetUserSettings)
		api.POST("/settings/user/theme", h.handlePostUserTheme)
		api.GET("/settings/child-api-key", h.handleGetChildAPIKey)
		api.POST("/settings/child-api-key", h.handlePostChildAPIKey)
		api.DELETE("/settings/child-api-key", h.handleDeleteChildAPIKey)
		api.POST("/settings/child-webdav", h.handlePostChildWebDAV)
		api.POST("/settings/child-api", h.handlePostChildAPI)
		api.POST("/settings/webauthn", h.handlePostWebAuthn)

		// Users
		api.GET("/users", h.handleGetUsers)
		api.POST("/users", h.handlePostUser)
		api.DELETE("/users/:username", h.handleDeleteUser)
		api.POST("/users/:username/reset-pass", h.handlePostUserResetPass)

		// Files & Folders
		api.GET("/files", h.handleGetFiles)
		api.POST("/folders", h.handlePostFolders)
		api.POST("/upload", h.handlePostUpload)
		api.POST("/remote-upload", h.handlePostRemoteUpload)
		api.POST("/remote-upload/check", h.handlePostRemoteUploadCheck)
		api.GET("/tasks", h.handleGetTasks)
		api.POST("/cancel_upload", h.handleCancelUpload)
		api.POST("/actions/paste", h.handlePostPaste)
		api.DELETE("/files/:id", h.handleDeleteFile)
		api.PUT("/files/:id/rename", h.handleRenameFile)
		api.POST("/files/:id/share", h.handleShareFile)
		api.DELETE("/files/:id/share", h.handleRevokeShare)
		api.GET("/files/:id/thumb", h.handleGetThumb)
		api.GET("/files/:id/stream", h.handleStreamFile)
		api.GET("/files/:id/stream/:filename", h.handleStreamFile)
		api.GET("/progress/:task_id", h.handleGetProgress)
		api.GET("/upload/check/:task_id", h.handleGetUploadCheck)
		api.POST("/upload/check-exists", h.handlePostCheckExists)

		// YT-DLP
		api.GET("/ytdlp/status", h.handleGetYTDLPStatus)
		api.GET("/ytdlp/cookies/status", h.handleGetYTDLPCookiesStatus)
		api.POST("/ytdlp/cookies", h.handlePostYTDLPCookies)
		api.DELETE("/ytdlp/cookies", h.handleDeleteYTDLPCookies)
		api.GET("/proxy/image", h.handleGetProxyImage)
		api.POST("/ytdlp/formats", h.handlePostYTDLPFormats)
		api.POST("/ytdlp/download", h.handlePostYTDLPDownload)

		// WebSocket
		api.GET("/ws", h.handleWebSocket)
	}

	// Main Page
	r.GET("/", h.handleGetIndex)

	// Private Download
	r.GET("/download/:id", authMiddleware(), h.handleDownloadFile)

	return r
}

type PasteRequest struct {
	Action      string `json:"action"`
	ItemIDs     []int  `json:"item_ids"`
	Destination string `json:"destination"`
}
