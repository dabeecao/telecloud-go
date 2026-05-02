// Copyright (C) 2026 @dabeecao
//
// This file is part of TeleCloud project, lead developer: @dabeecao
// For support, please visit the TTJB support group: https://t.me/thuthuatjb_sp
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//

package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"path/filepath"
	"telecloud/api"
	"telecloud/config"
	"telecloud/database"
	"telecloud/tgclient"
	"telecloud/utils"
	"telecloud/ws"
)

//go:embed web/templates web/static/css/*.min.css web/static/css/tailwind.css web/static/css/plyr.css web/static/css/prism.css web/static/js/*.min.js web/static/js/plyr.polyfilled.js web/static/js/prism.js web/static/fonts web/static/webfonts web/static/favicon.ico web/static/locales/*.json
var contentFS embed.FS

var (
	version = "v3.0.0"
	commit  = "none"
	date    = "unknown"
)

func main() {
	authFlag := flag.Bool("auth", false, "Run the terminal authentication flow for a Userbot session")
	versionFlag := flag.Bool("version", false, "Show version information")
	resetPassFlag := flag.Bool("resetpass", false, "Reset admin username and password")
	flag.Parse()

	if *versionFlag {
		log.Printf("TeleCloud %s (commit: %s, date: %s)\n", version, commit, date)
		return
	}

	cfg := config.Load()
	cfg.Version = version

	// Ensure the directory for the database exists
	dbDir := filepath.Dir(cfg.DatabasePath)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		log.Printf("Warning: Could not create database directory: %v\n", err)
	}

	database.InitDB(cfg.DatabasePath)

	if *resetPassFlag {
		token := uuid.New().String()
		expiry := time.Now().Add(15 * time.Minute).Unix()
		database.SetSetting("admin_reset_token", token)
		database.SetSetting("admin_reset_expiry", fmt.Sprintf("%d", expiry))

		log.Println("================================================================")
		log.Println("ADMIN PASSWORD RESET INITIATED")
		log.Printf("Please visit the following URL to reset your admin password:\n")
		log.Printf("http://<your-domain-or-ip>/reset-admin?token=%s\n", token)
		log.Println("This link will expire in 15 minutes.")
		log.Println("================================================================")
		return
	}

	if err := os.MkdirAll(cfg.TempDir, 0755); err != nil {
		log.Printf("Warning: Could not create TempDir: %v\n", err)
	} else {
		// Startup cleanup: remove all files in temp dir from previous sessions
		files, _ := os.ReadDir(cfg.TempDir)
		for _, f := range files {
			if !f.IsDir() {
				os.Remove(filepath.Join(cfg.TempDir, f.Name()))
			}
		}
	}
	cryptoSecret := database.GetSetting("crypto_secret")
	if cryptoSecret == "" {
		cryptoSecret = uuid.New().String()
		database.SetSetting("crypto_secret", cryptoSecret)
	}
	utils.InitCrypto(cryptoSecret)
	utils.InitMedia(cfg.ThumbsDir)

	rpid := database.GetSetting("webauthn_rpid")
	if rpid == "" {
		rpid = cfg.WebAuthnRPID
	}
	rporigin := database.GetSetting("webauthn_rporigin")
	origins := []string{}
	if rporigin != "" {
		origins = strings.Split(rporigin, ",")
	} else if cfg.WebAuthnRPOrigin != "" {
		origins = strings.Split(cfg.WebAuthnRPOrigin, ",")
	}
	api.InitWebAuthn(rpid, origins)

	startCleanupTask(cfg)

	if err := tgclient.InitClient(cfg, *authFlag); err != nil {
		log.Fatalf("Telegram client init error: %v", err)
	}

	// cancelCtx is used to signal the Telegram client to stop
	appCtx, cancelApp := context.WithCancel(context.Background())
	defer cancelApp()

	// Catch OS signals for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Initialise the WebSocket hub with the app context so it shuts down gracefully
	ws.InitHub(appCtx)

	// Sub-folder 'web' from the embedded FS to keep paths clean
	webFS, err := fs.Sub(contentFS, "web")
	if err != nil {
		log.Fatalf("Failed to create sub FS for web: %v", err)
	}

	router := api.SetupRouter(cfg, webFS)

	httpServer := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: router,
	}

	// Run Telegram client in the background; it will block until appCtx is cancelled
	tgErrCh := make(chan error, 1)
	go func() {
		tgErrCh <- tgclient.Run(appCtx, cfg, func(ctx context.Context) error {
			log.Println("Starting TeleCloud on port " + cfg.Port + "...")

			// Start HTTP server in its own goroutine so Telegram keeps running alongside
			go func() {
				if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					log.Printf("HTTP server error: %v", err)
				}
			}()

			// Block until the app context is cancelled (signal received)
			<-ctx.Done()
			return nil
		})
	}()

	// Wait for shutdown signal or Telegram client to exit
	var exitCode int
	select {
	case sig := <-sigCh:
		log.Printf("Received signal: %v — initiating graceful shutdown...", sig)
	case err := <-tgErrCh:
		if err != nil {
			log.Printf("Telegram client exited with error: %v", err)
			exitCode = 1
		}
	}

	// Step 1: Gracefully shut down HTTP server (wait up to 15s for in-flight requests)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()

	log.Println("Shutting down HTTP server...")
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP server forced to shut down: %v", err)
	} else {
		log.Println("HTTP server stopped cleanly.")
	}

	// Step 2: Cancel app context → signals Telegram client goroutine to exit
	cancelApp()

	// Step 3: Wait for Telegram client to finish (with timeout)
	select {
	case <-tgErrCh:
		log.Println("Telegram client stopped.")
	case <-time.After(10 * time.Second):
		log.Println("Telegram client did not stop in time; forcing exit.")
	}

	// Step 4: Close database connection safely
	if err := database.DB.Close(); err != nil {
		log.Printf("Error closing database: %v", err)
	} else {
		log.Println("Database closed cleanly.")
	}

	log.Println("TeleCloud shut down successfully.")
	os.Exit(exitCode)
}

func startCleanupTask(cfg *config.Config) {
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		for range ticker.C {
			now := time.Now()
			filepath.WalkDir(cfg.TempDir, func(path string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				info, err := d.Info()
				if err != nil {
					return nil
				}
				if now.Sub(info.ModTime()) > 24*time.Hour {
					os.Remove(path)
				}
				return nil
			})
		}
	}()
}
