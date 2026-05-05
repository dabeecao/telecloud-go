package tgclient

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/proxy"
	"golang.org/x/term"

	"telecloud/config"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/tg"
)

var (
	Client *telegram.Client

	BotPool        []BotInstance
	botCounter     uint32
	botPeers       sync.Map // Map of bot index to resolved peer
	botPoolMu      sync.RWMutex
)

type BotInstance struct {
	Client  *telegram.Client
	Token   string
	Deleted bool // Mark as deleted if initialization fails
}

func GetAPI() *tg.Client {
	botPoolMu.RLock()
	defer botPoolMu.RUnlock()

	total := uint32(len(BotPool) + 1)
	idx := atomic.AddUint32(&botCounter, 1) % total
	if idx == 0 {
		return Client.API()
	}
	return BotPool[idx-1].Client.API()
}

func GetBotCount() int {
	botPoolMu.RLock()
	defer botPoolMu.RUnlock()
	return len(BotPool)
}

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

	// Initialize bots if provided
	isPrivateMe := cfg.LogGroupID == "me" || cfg.LogGroupID == "self"
	if isPrivateMe && len(cfg.BotTokens) > 0 {
		log.Println("ℹ️ Multi-bot disabled: Bots cannot access your private 'Saved Messages' (LOG_GROUP_ID=me).")
	}

	for _, token := range cfg.BotTokens {
		token = strings.TrimSpace(token)
		if token == "" || isPrivateMe {
			continue
		}
		// Create bot-specific options to avoid session file conflicts
		botOptions := options
		botOptions.SessionStorage = nil // Bots use tokens and don't need persistent session files
		botClient := telegram.NewClient(cfg.APIID, cfg.APIHash, botOptions)
		BotPool = append(BotPool, BotInstance{Client: botClient, Token: token})
	}

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
	errCh := make(chan error, len(BotPool)+1)
	var wg sync.WaitGroup

	for i := range BotPool {
		wg.Add(1)
		ready := make(chan struct{})
		go func(idx int, r chan struct{}) {
			defer wg.Done()
			b := &BotPool[idx]
			err := b.Client.Run(ctx, func(ctx context.Context) error {
				_, err := b.Client.Auth().Bot(ctx, b.Token)
				if err != nil {
					return err
				}
				close(r) // Signal that this bot is authorized and ready
				<-ctx.Done()
				return nil
			})
			if err != nil && err != context.Canceled {
				// Close channel to avoid hang if not already closed
				select {
				case <-r:
				default:
					close(r)
				}
				log.Printf("⚠️ Bot #%d encountered an error and will be disabled: %v", idx+1, err)
				b.Deleted = true
				// We DO NOT send this error to errCh because we want the app to keep running
			}
		}(i, ready)

		// Wait for this bot to be ready before starting the next one or proceeding
		select {
		case <-ready:
			// Bot is ready, continue
		case <-time.After(15 * time.Second):
			log.Printf("⚠️ Bot #%d: authorization timed out", i+1)
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := Client.Run(ctx, func(ctx context.Context) error {
			status, err := Client.Auth().Status(ctx)
			if err != nil {
				return err
			}
			if !status.Authorized {
				return fmt.Errorf("not authorized, please run with -auth flag first to login")
			}

			// Always detect Telegram account status to set part size
			{
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
					cfg.IsPremium = isPremium
					if isPremium && len(BotPool) == 0 {
						cfg.MaxPartSize = 3900 * 1024 * 1024
					} else {
						cfg.MaxPartSize = 1900 * 1024 * 1024
					}
				} else {
					cfg.IsPremium = false
					cfg.MaxPartSize = 1900 * 1024 * 1024 // Fallback
				}
			}

			return cb(ctx)
		})
		if err != nil && err != context.Canceled {
			errCh <- err
		}
	}()

	// Wait for first error or context cancellation
	go func() {
		wg.Wait()
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func VerifyLogGroup(ctx context.Context, cfg *config.Config) error {
	if cfg.LogGroupID == "" {
		return fmt.Errorf("LOG_GROUP_ID is not set in .env")
	}

	mainApi := Client.API()
	peer, err := resolveLogGroup(ctx, mainApi, cfg.LogGroupID)
	if err != nil {
		return fmt.Errorf("could not resolve log group: %w", err)
	}

	sender := message.NewSender(mainApi)
	_, err = sender.To(peer).Text(ctx, "🚀 TeleCloud is starting...\nConnectivity check: OK")
	if err != nil {
		return fmt.Errorf("could not send test message to log group: %w", err)
	}

	// Verify all bots in the pool
	botPoolMu.Lock()
	var activeBots []BotInstance
	for i, bot := range BotPool {
		if bot.Deleted {
			continue
		}
		api := bot.Client.API()
		// Try to resolve group for this bot
		botPeer, err := resolveLogGroup(ctx, api, cfg.LogGroupID)
		if err != nil {
			log.Printf("⚠️ Bot #%d: resolution failed: %v. Removing from pool.", i+1, err)
			continue
		}

		// Try to send a message
		botSender := message.NewSender(api)
		_, err = botSender.To(botPeer).Text(ctx, fmt.Sprintf("🤖 Bot #%d (%s) is online and reporting for duty!", i+1, bot.Token[:8]+"..."))
		if err != nil {
			log.Printf("⚠️ Bot #%d: connectivity check failed: %v. Removing from pool.", i+1, err)
			continue
		}
		activeBots = append(activeBots, bot)
	}
	BotPool = activeBots
	botPoolMu.Unlock()

	log.Printf("Log Group connectivity verified. Active Bots: %d", len(activeBots))
	return nil
}

func resolveLogGroup(ctx context.Context, api *tg.Client, logGroupIDStr string) (tg.InputPeerClass, error) {
	cacheKey := fmt.Sprintf("%p_%s", api, logGroupIDStr)
	if val, ok := botPeers.Load(cacheKey); ok {
		return val.(tg.InputPeerClass), nil
	}

	var peer tg.InputPeerClass
	var err error

	if logGroupIDStr == "me" || logGroupIDStr == "self" {
		peer = &tg.InputPeerSelf{}
	} else {
		logGroupID, errParse := strconv.ParseInt(logGroupIDStr, 10, 64)
		if errParse != nil {
			return nil, fmt.Errorf("invalid LOG_GROUP_ID: %v", errParse)
		}

		if logGroupID < 0 {
			strID := strconv.FormatInt(logGroupID, 10)
			if strings.HasPrefix(strID, "-100") {
				channelID, _ := strconv.ParseInt(strID[4:], 10, 64)
				// Use ChannelsGetChannels which is bot-friendly
				res, errChannels := api.ChannelsGetChannels(ctx, []tg.InputChannelClass{
					&tg.InputChannel{ChannelID: channelID},
				})
				if errChannels == nil {
					chats := res.GetChats()
					if len(chats) > 0 {
						if c, ok := chats[0].(*tg.Channel); ok {
							peer = &tg.InputPeerChannel{
								ChannelID:  c.ID,
								AccessHash: c.AccessHash,
							}
						}
					}
				} else {
					err = errChannels
				}
			} else {
				// Regular chat
				chatID := -logGroupID
				res, errChats := api.MessagesGetChats(ctx, []int64{chatID})
				if errChats == nil {
					chats := res.GetChats()
					if len(chats) > 0 {
						peer = &tg.InputPeerChat{ChatID: chatID}
					}
				} else {
					err = errChats
				}
			}
		} else {
			peer = &tg.InputPeerUser{UserID: logGroupID}
		}
	}

	if err != nil {
		return nil, err
	}
	if peer == nil {
		return nil, fmt.Errorf("could not resolve peer for ID %s", logGroupIDStr)
	}

	botPeers.Store(cacheKey, peer)
	return peer, nil
}
