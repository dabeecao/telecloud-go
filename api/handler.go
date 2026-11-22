package api

import (
	"io/fs"
	"telecloud/config"
	"telecloud/uploadsession"
)

type Handler struct {
	cfg        *config.Config
	contentFS  fs.FS
	startTG    func(cfg *config.Config)
	restartApp func()
	uploads    *uploadsession.Manager
}

func NewHandler(cfg *config.Config, contentFS fs.FS, startTG func(cfg *config.Config), restartApp func()) *Handler {
	h := &Handler{
		cfg:        cfg,
		contentFS:  contentFS,
		startTG:    startTG,
		restartApp: restartApp,
		uploads:    uploadsession.New(cfg, uploadsession.TelegramBridge{Cfg: cfg}),
	}
	h.uploads.OnPublished = h.regenerateThumbAsync
	h.uploads.Resume()
	h.uploads.StartCleanup()
	return h
}
