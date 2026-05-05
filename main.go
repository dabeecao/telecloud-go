// Copyright (C) 2026 @dabeecao
//
// This file is part of TeleCloud project, lead developer: @dabeecao
// For support, please visit the group: https://t.me/+p-d0qfGRbX4wNzJl
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
	"bufio"
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"telecloud/api"
	"telecloud/config"
	"telecloud/database"
	"telecloud/tgclient"
	"telecloud/utils"
	"telecloud/ws"
)

//go:embed web/templates
//go:embed web/static/css/*.min.css web/static/css/tailwind.css web/static/css/plyr.css web/static/css/prism.css
//go:embed web/static/js/*.min.js web/static/js/plyr.polyfilled.js web/static/js/prism.js
//go:embed web/static/themes/*.min.css
//go:embed web/static/fonts web/static/webfonts
//go:embed web/static/favicon.ico
//go:embed web/static/locales/*.min.json
var contentFS embed.FS

var (
	version = "v3.2.0-beta2"
	commit  = "none"
	date    = "unknown"
)

func main() {
	// Fix environment variables for Termux/Android to ensure FFmpeg/YT-DLP work correctly
	fixTermuxEnvironment()

	authFlag := flag.Bool("auth", false, "Run the terminal authentication flow for a Userbot session")
	versionFlag := flag.Bool("version", false, "Show version information")
	resetPassFlag := flag.Bool("resetpass", false, "Reset admin username and password")
	flag.Parse()

	if *versionFlag {
		log.Printf("TeleCloud %s (commit: %s, date: %s)\n", version, commit, date)
		waitExitOnWindows()
		return
	}

	fmt.Printf("\n")
	fmt.Printf("  ╔╦╗┌─┐┬  ┌─┐╔═╗┬  ┌─┐┬ ┬┌┬┐\n")
	fmt.Printf("   ║ ├┤ │  ├┤ ║  │  │ ││ │ ││\n")
	fmt.Printf("   ╩ └─┘┴─┘└─┘╚═╝┴─┘└─┘└─┘─┴┘\n")
	fmt.Printf("   TeleCloud %s - Lead by @dabeecao\n\n", version)
	log.Println("TeleCloud is starting, please wait...")

	cfg, err := config.Load()
	if err != nil {
		fatalf("%v", err)
	}
	cfg.Version = version

	if cfg.DatabaseDriver == "sqlite" || cfg.DatabaseDriver == "" {
		// Ensure the directory for the SQLite database exists.
		dbDir := filepath.Dir(cfg.DatabasePath)
		if err := os.MkdirAll(dbDir, 0755); err != nil {
			cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("Warning: Could not create database directory: %v", err))
		}
	}

	if err := database.InitDB(cfg.DatabaseDriver, cfg.DatabasePath, cfg.DatabaseDSN); err != nil {
		fatalf("%v", err)
	}

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
		waitExitOnWindows()
		return
	}

	if err := os.MkdirAll(cfg.TempDir, 0755); err != nil {
		cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("Warning: Could not create TempDir: %v", err))
	} else {
		// Startup cleanup: remove only old files in temp dir from previous sessions
		// to allow resumable uploads after server restart.
		now := time.Now()
		files, _ := os.ReadDir(cfg.TempDir)
		for _, f := range files {
			if !f.IsDir() {
				info, err := f.Info()
				if err == nil && now.Sub(info.ModTime()) > 24*time.Hour {
					os.Remove(filepath.Join(cfg.TempDir, f.Name()))
				}
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
		fatalf("Telegram client init error: %v", err)
	}
	tgclient.InitUploader(cfg)

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
		fatalf("Failed to create sub FS for web: %v", err)
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
			if err := tgclient.VerifyLogGroup(ctx, cfg); err != nil {
				return fmt.Errorf("Log Group verification failed: %v", err)
			}
			printStartupBox(cfg)
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
	waitExitOnWindows()
	os.Exit(exitCode)
}

func waitExitOnWindows() {
	if runtime.GOOS != "windows" {
		return
	}

	// Check if output is redirected (e.g. to a log file)
	// If it's not a character device, we're likely in a script or background task.
	if stats, _ := os.Stdout.Stat(); (stats.Mode() & os.ModeCharDevice) == 0 {
		return
	}

	fmt.Println("\n[!] Press Enter to exit...")
	bufio.NewReader(os.Stdin).ReadBytes('\n')
}

func fatalf(format string, v ...interface{}) {
	log.Printf(format, v...)
	waitExitOnWindows()
	os.Exit(1)
}

func printStartupBox(cfg *config.Config) {
	fmt.Println("  [System Configuration]")
	fmt.Printf("  %-15s : %s\n", "Port", cfg.Port)
	dbDisplay := cfg.DatabasePath
	if cfg.DatabaseDriver == "mysql" {
		dsn := cfg.DatabaseDSN
		if strings.Contains(dsn, ":") && strings.Contains(dsn, "@") {
			parts := strings.SplitN(dsn, "@", 2)
			if len(parts) == 2 {
				userPass := parts[0]
				if strings.Contains(userPass, ":") {
					up := strings.SplitN(userPass, ":", 2)
					dsn = up[0] + ":****@" + parts[1]
				}
			}
		}
		dbDisplay = "MySQL (" + dsn + ")"
	} else {
		dbDisplay = "SQLite (" + cfg.DatabasePath + ")"
	}
	fmt.Printf("  %-15s : %s\n", "Database", dbDisplay)
	fmt.Printf("  %-15s : %s\n", "Upload Threads", fmt.Sprintf("%d", cfg.UploadThreads))
	fmt.Printf("  %-15s : %d\n", "Active Bots", tgclient.GetBotCount())
	fmt.Printf("  %-15s : %s (Premium: %v)\n", "Max Part Size", utils.FormatBytes(cfg.MaxPartSize), cfg.IsPremium)

	fmt.Println("\n  [Features Status]")

	// FFmpeg status
	ffmpegEnabled := cfg.FFMPEGPath != "disabled" && cfg.FFMPEGPath != "disable"
	ffmpegStatus := "DISABLED"
	if ffmpegEnabled {
		ffmpegStatus = "ENABLED (" + cfg.FFMPEGPath + ")"
	}
	fmt.Printf("  %-15s : %s\n", "FFmpeg", ffmpegStatus)

	// yt-dlp status
	ytdlpEnabled := cfg.YTDLPPath != "disabled" && cfg.YTDLPPath != "disable"
	if !ffmpegEnabled {
		ytdlpEnabled = false
	}
	ytdlpStatus := "DISABLED"
	if ytdlpEnabled {
		ytdlpStatus = "ENABLED (" + cfg.YTDLPPath + ")"
	}
	fmt.Printf("  %-15s : %s\n", "yt-dlp", ytdlpStatus)

	// WebAuthn status
	rpid := database.GetSetting("webauthn_rpid")
	if rpid == "" {
		rpid = cfg.WebAuthnRPID
	}
	fmt.Printf("  %-15s : %s (ID: %s)\n", "Passkeys", "READY", rpid)

	// Proxy status
	proxyStatus := "DISABLED"
	if cfg.ProxyURL != "" {
		proxyStatus = "ENABLED"
	}
	fmt.Printf("  %-15s : %s\n", "Proxy", proxyStatus)
	fmt.Printf("\n")

	// Print delayed warnings
	for _, w := range cfg.Warnings {
		log.Println(w)
	}
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
					// Extract taskId from filename (taskId_filename)
					filename := filepath.Base(path)
					if idx := strings.Index(filename, "_"); idx != -1 {
						taskId := filename[:idx]
						database.DB.Exec("DELETE FROM upload_chunks WHERE task_id = ?", taskId)
						database.DB.Exec("DELETE FROM upload_tasks WHERE id = ?", taskId)
					}
				}
				return nil
			})
		}
	}()
}

func fixTermuxEnvironment() {
	// Only proceed if we are on Android or TERMUX_VERSION is set
	if runtime.GOOS != "android" && os.Getenv("TERMUX_VERSION") == "" {
		return
	}

	prefix := os.Getenv("PREFIX")
	if prefix == "" && runtime.GOOS == "android" {
		prefix = "/data/data/com.termux/files/usr"
	}

	if prefix != "" {
		binDir := filepath.Join(prefix, "bin")
		currentPath := os.Getenv("PATH")
		if !strings.Contains(currentPath, binDir) {
			os.Setenv("PATH", binDir+string(os.PathListSeparator)+currentPath)
		}

		libDir := filepath.Join(prefix, "lib")
		currentLD := os.Getenv("LD_LIBRARY_PATH")
		if !strings.Contains(currentLD, libDir) {
			newLD := libDir
			if currentLD != "" {
				newLD = libDir + string(os.PathListSeparator) + currentLD
			}
			os.Setenv("LD_LIBRARY_PATH", newLD)
		}

		if os.Getenv("TMPDIR") == "" {
			tmpDir := filepath.Join(prefix, "tmp")
			os.MkdirAll(tmpDir, 0755)
			os.Setenv("TMPDIR", tmpDir)
		}

		preload := filepath.Join(prefix, "lib", "libtermux-exec.so")
		if _, err := os.Stat(preload); err == nil {
			currentPreload := os.Getenv("LD_PRELOAD")
			if !strings.Contains(currentPreload, "libtermux-exec.so") {
				if currentPreload != "" {
					os.Setenv("LD_PRELOAD", preload+" "+currentPreload)
				} else {
					os.Setenv("LD_PRELOAD", preload)
				}
			}
		}
	}
}
