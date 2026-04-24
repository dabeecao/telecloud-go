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
	"log"
	"os"

	"telecloud/api"
	"telecloud/config"
	"telecloud/database"
	"telecloud/tgclient"
	"telecloud/utils"
)

//go:embed templates/* static/css/* static/js/* static/favicon.ico
var contentFS embed.FS

var (
	version = "v1.2.0"
	commit  = "none"
	date    = "unknown"
)

func main() {
	authFlag := flag.Bool("auth", false, "Run the terminal authentication flow for a Userbot session")
	versionFlag := flag.Bool("version", false, "Show version information")
	flag.Parse()

	if *versionFlag {
		log.Printf("TeleCloud %s (commit: %s, date: %s)\n", version, commit, date)
		return
	}

	cfg := config.Load()
	cfg.Version = version
	database.InitDB(cfg.DatabasePath)
	if err := os.MkdirAll(cfg.TempDir, 0755); err != nil {
		log.Printf("Warning: Could not create TempDir: %v\n", err)
	}
	utils.InitCrypto(cfg.AdminPassword)
	utils.InitMedia(cfg.ThumbsDir)

	if err := tgclient.InitClient(cfg, *authFlag); err != nil {
		log.Fatalf("Telegram client init error: %v", err)
	}

	router := api.SetupRouter(cfg, contentFS)

	ctx := context.Background()

	log.Println("Starting Telecloud on port " + cfg.Port + "...")

	// Start telegram client run loop in the background and block on router.Run()
	err := tgclient.Run(ctx, cfg, func(ctx context.Context) error {
		return router.Run(":" + cfg.Port)
	})

	if err != nil {
		log.Fatalf("Run error: %v", err)
	}
}
