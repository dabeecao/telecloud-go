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

func restartApp() {
	log.Println("Restarting TeleCloud...")
	executable, err := os.Executable()
	if err != nil {
		log.Printf("Error getting executable path: %v. Exiting instead.", err)
		os.Exit(0)
	}

	if runtime.GOOS == "windows" {
		log.Println("Self-restart not supported on Windows. Please restart manually.")
		os.Exit(0)
	}

	// Small delay to allow HTTP response to be sent
	time.Sleep(500 * time.Millisecond)

	err = syscall.Exec(executable, os.Args, os.Environ())
	if err != nil {
		log.Printf("Failed to restart app: %v. Exiting instead.", err)
		os.Exit(0)
	}
}

var (
	version = "v3.2.3"
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
	fmt.Printf("  TeleCloud %s - Powered by @dabeecao\n\n", version)
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
	cfg.LoadFromDB(database.GetSetting)

	if *resetPassFlag {
		token := uuid.New().String()
		expiry := time.Now().Add(15 * time.Minute).Unix()
		database.SetSetting("admin_reset_token", token)
		database.SetSetting("admin_reset_expiry", fmt.Sprintf("%d", expiry))

		siteURL := database.GetSetting("site_url")
		if siteURL == "" {
			siteURL = "http://<your-domain-or-ip>"
		}

		log.Println("================================================================")
		log.Println("ADMIN PASSWORD RESET INITIATED")
		log.Printf("Please visit the following URL to reset your admin password:\n")
		log.Printf("%s/reset-admin?token=%s\n", siteURL, token)
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

	// Initialize WebAuthn (logic moved to api.InitWebAuthn for consistency)
	api.InitWebAuthn("", nil)

	startCleanupTask(cfg)

	// cancelCtx is used to signal the Telegram client to stop
	appCtx, cancelApp := context.WithCancel(context.Background())
	defer cancelApp()

	// Catch OS signals for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	// Initialise the WebSocket hub with the app context so it shuts down gracefully
	ws.InitHub(appCtx)

	// Sub-folder 'web' from the embedded FS to keep paths clean
	webFS, err := fs.Sub(contentFS, "web")
	if err != nil {
		fatalf("Failed to create sub FS for web: %v", err)
	}

	tgErrCh := make(chan error, 1)

	startTG := func(newCfg *config.Config) {
		if err := tgclient.InitClient(newCfg, *authFlag); err != nil {
			log.Printf("Telegram client init error: %v", err)
			return
		}
		tgclient.InitUploader(newCfg)
		go func() {
			tgErrCh <- tgclient.Run(appCtx, newCfg, func(ctx context.Context) error {
				if err := tgclient.VerifyLogGroup(ctx, newCfg); err != nil {
					log.Printf("Warning: Log Group verification failed: %v", err)
				}
				printStartupBox(newCfg)
				log.Println("Starting TeleCloud on port " + newCfg.Port + "...")
				<-ctx.Done()
				return nil
			})
		}()
	}

	router := api.SetupRouter(cfg, webFS, startTG, restartApp)

	httpServer := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: router,
	}

	adminUser := database.GetSetting("admin_username")
	if cfg.APIID == 0 || cfg.APIHash == "" || adminUser == "" {
		setupURL := fmt.Sprintf("http://YOUR_IP_OR_DOMAIN:%s/setup", cfg.Port)
		log.Printf("Setup is incomplete. Starting in Setup Mode. Please visit: %s", setupURL)
		log.Println("Starting TeleCloud on port " + cfg.Port + "...")
	} else {
		startTG(cfg)
	}

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
			if p, err := os.FindProcess(os.Getpid()); err == nil {
				p.Signal(syscall.SIGTERM)
			}
		}
	}()

	// Wait for shutdown signal or Telegram client to exit
	var exitCode int
Loop:
	for {
		select {
		case sig := <-sigCh:
			log.Printf("Received signal: %v — initiating graceful shutdown...", sig)
			break Loop
		case err := <-tgErrCh:
			if err != nil {
				if strings.Contains(err.Error(), "AUTH_REQUIRED") {
					log.Printf("Telegram session not authorized. App will remain in Maintenance Mode.")
					log.Println("Starting TeleCloud on port " + cfg.Port + "...")
					continue
				}
				log.Printf("Telegram client exited with error: %v", err)
				adminUser := database.GetSetting("admin_username")
				if adminUser == "" {
					log.Println("Setup is incomplete. Keeping HTTP server alive for Web Setup.")
					continue
				}
				exitCode = 1
				break Loop
			}
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
	tgclient.StopClient()

	// Step 3: Wait for Telegram client to finish (with timeout)
	select {
	case <-tgErrCh:
		log.Println("Telegram client stopped.")
	case <-time.After(10 * time.Second):
		log.Println("Telegram client did not stop in time; forcing exit.")
	}

	// Step 4: Close database connection safely
	if err := database.CloseDB(); err != nil {
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
	// Prepare data
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

	ffmpegEnabled := cfg.FFMPEGPath != "disabled" && cfg.FFMPEGPath != "disable"
	ffmpegStatus := "Disabled"
	if ffmpegEnabled {
		ffmpegStatus = "Enabled"
	}

	ytdlpEnabled := cfg.YTDLPPath != "disabled" && cfg.YTDLPPath != "disable"
	if !ffmpegEnabled {
		ytdlpEnabled = false
	}
	ytdlpStatus := "Disabled"
	if ytdlpEnabled {
		ytdlpStatus = "Enabled"
	}

	proxyStatus := "Disabled"
	if cfg.ProxyURL != "" {
		proxyStatus = "Enabled"
	}

	// Print table
	fmt.Println("  ┌──────────────────────────────────────────────────────────────────┐")
	fmt.Println("  │                      SYSTEM CONFIGURATION                        │")
	fmt.Println("  ├────────────────────────────┬─────────────────────────────────────┤")
	fmt.Printf("  │ %-26s │ %-35s │\n", "Service Port", cfg.Port)
	fmt.Printf("  │ %-26s │ %-35s │\n", "Database", truncateString(dbDisplay, 35))
	fmt.Printf("  │ %-26s │ %-35s │\n", "Upload Threads", fmt.Sprintf("%d", cfg.UploadThreads))
	fmt.Printf("  │ %-26s │ %-35s │\n", "Active Bot Pool", fmt.Sprintf("%d bots", tgclient.GetBotCount()))
	fmt.Printf("  │ %-26s │ %-35s │\n", "Max Part Size", utils.FormatBytes(cfg.MaxPartSize))
	fmt.Printf("  │ %-26s │ %-35s │\n", "Premium Status", fmt.Sprintf("%v", cfg.IsPremium))
	fmt.Println("  ├────────────────────────────┼─────────────────────────────────────┤")
	fmt.Printf("  │ %-26s │ %-35s │\n", "FFmpeg Support", ffmpegStatus)
	fmt.Printf("  │ %-26s │ %-35s │\n", "YouTube-DLP Support", ytdlpStatus)
	fmt.Printf("  │ %-26s │ %-35s │\n", "Proxy Connection", proxyStatus)
	fmt.Println("  └────────────────────────────┴─────────────────────────────────────┘")
	fmt.Println()

	// Print delayed warnings
	for _, w := range cfg.Warnings {
		log.Println("Warning: " + w)
	}
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
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
