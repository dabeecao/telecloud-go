package tgclient

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"strings"

	"golang.org/x/net/proxy"
	"golang.org/x/term"

	"telecloud/config"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/tg"
)

var Client *telegram.Client

type termAuth struct{}

func (termAuth) Phone(ctx context.Context) (string, error) {
	fmt.Print("Enter phone number (e.g. +1234567890): ")
	phone, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(phone), nil
}

func (termAuth) Password(ctx context.Context) (string, error) {
	fmt.Print("Enter 2FA password: ")
	bytePassword, err := term.ReadPassword(int(os.Stdin.Fd()))
	if err != nil {
		return "", err
	}
	fmt.Println()
	return strings.TrimSpace(string(bytePassword)), nil
}

func (termAuth) AcceptTermsOfService(ctx context.Context, tos tg.HelpTermsOfService) error {
	return nil
}

func (termAuth) SignUp(ctx context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, fmt.Errorf("signup not supported")
}

func (termAuth) Code(ctx context.Context, sentCode *tg.AuthSentCode) (string, error) {
	fmt.Print("Enter code: ")
	code, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(code), nil
}

func InitClient(cfg *config.Config, runAuthFlow bool) error {
	sessionDir := cfg.SessionFile

	options := telegram.Options{
		SessionStorage: &session.FileStorage{
			Path: sessionDir,
		},
		Device: telegram.DeviceConfig{
			DeviceModel:   "TeleCloud Server",
			SystemVersion: "Linux",
			AppVersion:    cfg.Version,
		},
	}

	if cfg.ProxyURL != "" {
		u, err := url.Parse(cfg.ProxyURL)
		if err != nil {
			return fmt.Errorf("invalid PROXY_URL: %v", err)
		}

		dialer, err := proxy.FromURL(u, proxy.Direct)
		if err != nil {
			return fmt.Errorf("failed to create proxy dialer: %v", err)
		}

		options.Resolver = dcs.Plain(dcs.PlainOptions{
			Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if d, ok := dialer.(proxy.ContextDialer); ok {
					return d.DialContext(ctx, network, addr)
				}
				return dialer.Dial(network, addr)
			},
		})
		log.Printf("Using proxy: %s", cfg.ProxyURL)
	}

	// Userbot mode
	Client = telegram.NewClient(cfg.APIID, cfg.APIHash, options)

	if runAuthFlow {
		err := Client.Run(context.Background(), func(ctx context.Context) error {
			flow := auth.NewFlow(
				termAuth{},
				auth.SendCodeOptions{},
			)
			if err := Client.Auth().IfNecessary(ctx, flow); err != nil {
				return fmt.Errorf("auth error: %w", err)
			}
			fmt.Println("Successfully authenticated! Session saved to", sessionDir)
			return nil
		})
		if err != nil {
			return err
		}
		os.Exit(0)
	}

	return nil
}

func Run(ctx context.Context, cfg *config.Config, cb func(ctx context.Context) error) error {
	return Client.Run(ctx, func(ctx context.Context) error {
		status, err := Client.Auth().Status(ctx)
		if err != nil {
			return err
		}
		if !status.Authorized {
			return fmt.Errorf("not authorized, please run with -auth flag first to login")
		}

		// Detect MaxUploadSizeMB if not set
		if cfg.MaxUploadSizeMB <= 0 {
			api := Client.API()
			fullUser, err := api.UsersGetFullUser(ctx, &tg.InputUserSelf{})
			if err == nil {
				isPremium := false
				for _, u := range fullUser.Users {
					if user, ok := u.(*tg.User); ok {
						isPremium = user.Premium
						break
					}
				}
				if isPremium {
					cfg.MaxUploadSizeMB = 4000
				} else {
					cfg.MaxUploadSizeMB = 2000
				}
				log.Printf("Detected Telegram account status: Premium=%v. Automatically setting MaxUploadSizeMB to %d", isPremium, cfg.MaxUploadSizeMB)
			} else {
				cfg.MaxUploadSizeMB = 2000 // Fallback
				log.Printf("Could not detect Telegram account status: %v. Using default 2000 MB", err)
			}
		}

		return cb(ctx)
	})
}
