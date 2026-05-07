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
	"telecloud/database"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/tg"
)

var (
	Client  *telegram.Client
	BotPool []BotInstance

	tgCtx    context.Context
	tgCancel context.CancelFunc

	SkipBotPool bool

	botCounter uint32
	botPeers   sync.Map // Map of bot index to resolved peer
	botPoolMu  sync.RWMutex

	mainAuthorized int32
	mainRunning    int32
	systemReady    int32

	Dispatcher = tg.NewUpdateDispatcher()
	tgMu       sync.Mutex
)

func IsAuthorized() bool {
	return atomic.LoadInt32(&mainAuthorized) == 1
}

func IsRunning() bool {
	return atomic.LoadInt32(&mainRunning) == 1
}

func IsSystemReady() bool {
	return atomic.LoadInt32(&systemReady) == 1
}

func GetAuthStatus(ctx context.Context) (*auth.Status, error) {
	tgMu.Lock()
	c := Client
	tgMu.Unlock()
	if c == nil {
		return nil, fmt.Errorf("client not initialized")
	}
	return c.Auth().Status(ctx)
}

type BotInstance struct {
	Client  *telegram.Client
	Token   string
	Deleted bool // Mark as deleted if initialization fails
}

func GetAPI() *tg.Client {
	botPoolMu.RLock()
	defer botPoolMu.RUnlock()

	var activeIndices []int
	for i := range BotPool {
		if !BotPool[i].Deleted {
			activeIndices = append(activeIndices, i)
		}
	}

	total := uint32(len(activeIndices) + 1)
	idx := atomic.AddUint32(&botCounter, 1) % total
	if idx == 0 {
		return Client.API()
	}
	return BotPool[activeIndices[idx-1]].Client.API()
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

type DBSessionStorage struct {
	SessionID string
}

func (s *DBSessionStorage) LoadSession(ctx context.Context) ([]byte, error) {
	data, err := database.GetTGSession(s.SessionID)
	if err != nil {
		return nil, session.ErrNotFound
	}
	return data, nil
}

func (s *DBSessionStorage) StoreSession(ctx context.Context, data []byte) error {
	return database.SetTGSession(s.SessionID, data)
}


func StopClient() {
	tgMu.Lock()
	defer tgMu.Unlock()
	stopClientUnlocked()
}

func stopClientUnlocked() {
	if tgCancel != nil {
		log.Println("Stopping existing Telegram client...")
		tgCancel()
		tgCancel = nil
	}
}

func InitClient(cfg *config.Config, runAuthFlow bool) error {
	tgMu.Lock()
	defer tgMu.Unlock()

	stopClientUnlocked() // Stop previous client if running
	tgCtx, tgCancel = context.WithCancel(context.Background())
	Dispatcher = tg.NewUpdateDispatcher()

	BotPool = nil // Clear existing bot pool

	options := telegram.Options{
		SessionStorage: &DBSessionStorage{
			SessionID: "main",
		},
		Device: telegram.DeviceConfig{
			DeviceModel:   "TeleCloud Server",
			SystemVersion: "Linux",
			AppVersion:    cfg.Version,
		},
		UpdateHandler: Dispatcher,
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
		log.Println("Info: Multi-bot disabled - Bots cannot access your private 'Saved Messages' (LOG_GROUP_ID=me).")
	}

	for _, token := range cfg.BotTokens {
		token = strings.TrimSpace(token)
		if token == "" || isPrivateMe {
			continue
		}
		// Create bot-specific options with database storage
		botOptions := options
		botOptions.SessionStorage = &DBSessionStorage{SessionID: token}
		botClient := telegram.NewClient(cfg.APIID, cfg.APIHash, botOptions)
		BotPool = append(BotPool, BotInstance{Client: botClient, Token: token})
	}

	if runAuthFlow {
		err := Client.Run(tgCtx, func(ctx context.Context) error {
			flow := auth.NewFlow(
				termAuth{},
				auth.SendCodeOptions{},
			)
			if err := Client.Auth().IfNecessary(ctx, flow); err != nil {
				return fmt.Errorf("auth error: %w", err)
			}
			fmt.Println("Successfully authenticated! Session saved to database.")
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
	atomic.StoreInt32(&mainRunning, 1)
	defer atomic.StoreInt32(&mainRunning, 0)

	// 1. Initialize main Telegram session FIRST
	log.Println("Initializing main Telegram session...")
	mainStarted := make(chan struct{})
	go func() {
		err := Client.Run(tgCtx, func(ctx context.Context) error {
			status, err := Client.Auth().Status(ctx)
			if err != nil {
				return err
			}
			if !status.Authorized {
				atomic.StoreInt32(&mainAuthorized, 0)
				return fmt.Errorf("AUTH_REQUIRED: not authorized, please login first")
			}

			atomic.StoreInt32(&mainAuthorized, 1)

			// Detect Telegram account status
			api := Client.API()
			fullUser, _ := api.UsersGetFullUser(ctx, &tg.InputUserSelf{})
			if fullUser != nil {
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
			}

			// 2. Start Bot Pool ONLY after main client is up
			if !SkipBotPool {
				for i := range BotPool {
					ready := make(chan bool, 1)
					go func(idx int, r chan bool) {
						b := &BotPool[idx]
						log.Printf("Initializing Bot #%d session...", idx+1)

						// Try login
						err := b.Client.Run(ctx, func(ctx context.Context) error {
							_, err := b.Client.Auth().Bot(ctx, b.Token)
							if err != nil {
								return err
							}
							r <- true // Success
							<-ctx.Done() // Keep the connection alive
							return nil
						})

						if err != nil && err != context.Canceled {
							if strings.Contains(err.Error(), "AUTH_KEY_UNREGISTERED") {
								log.Printf("Bot #%d: session invalid, clearing and retrying...", idx+1)
								database.DB.Exec("DELETE FROM tg_sessions WHERE session_id = ?", b.Token)
							}
							log.Printf("Warning: Bot #%d encountered an error and will be disabled: %v", idx+1, err)
							b.Deleted = true
							select {
							case r <- false:
							default:
							}
						}
					}(i, ready)

					// Wait for this bot to be ready or fail before moving to next
					select {
					case ok := <-ready:
						if !ok {
							log.Printf("Bot #%d failed to start.", i+1)
						}
					case <-time.After(15 * time.Second):
						log.Printf("Warning: Bot #%d authorization timed out", i+1)
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			} else {
				log.Println("Bot Pool startup skipped (SkipBotPool is true).")
			}

			atomic.StoreInt32(&systemReady, 1)
			close(mainStarted)
			return cb(ctx)
		})
		if err != nil && err != context.Canceled {
			errCh <- err
		}
	}()

	// Wait for main client to be ready or fail
	select {
	case <-mainStarted:
		log.Println("Telegram system (Main + Pool) initialized.")
	case err := <-errCh:
		atomic.StoreInt32(&mainAuthorized, 0)
		return err
	case <-ctx.Done():
		atomic.StoreInt32(&mainAuthorized, 0)
		return ctx.Err()
	}

	return <-errCh
}

func VerifyLogGroup(ctx context.Context, cfg *config.Config) error {
	if cfg.LogGroupID == "" {
		return fmt.Errorf("LOG_GROUP_ID is not set in .env")
	}

	log.Println("Verifying Log Group connectivity and bot pool status...")

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

	// Verify all bots in the pool ONLY if they have been initialized and SkipBotPool is false
	if !SkipBotPool {
		botPoolMu.Lock()
		var wgBots sync.WaitGroup
		activeBotsChan := make(chan BotInstance, len(BotPool))

		for i := range BotPool {
			if BotPool[i].Deleted {
				continue
			}
			wgBots.Add(1)
			go func(idx int) {
				defer wgBots.Done()
				bot := BotPool[idx]
				api := bot.Client.API()
				// Try to resolve group for this bot
				botPeer, err := resolveLogGroup(ctx, api, cfg.LogGroupID)
				if err != nil {
					errMsg := err.Error()
					if strings.Contains(errMsg, "PEER_ID_INVALID") || strings.Contains(errMsg, "CHANNEL_INVALID") {
						errMsg = "Bot is not a member of the Log Group or ID is invalid"
					}
					log.Printf("Warning: Bot #%d: resolution failed: %v. Removing from pool.", idx+1, errMsg)
					return
				}

				// Try to send a message
				botSender := message.NewSender(api)
				_, err = botSender.To(botPeer).Text(ctx, fmt.Sprintf("🤖 Bot #%d (%s) is online and reporting for duty!", idx+1, bot.Token[:8]+"..."))
				if err != nil {
					log.Printf("Warning: Bot #%d: connectivity check failed: %v. Removing from pool.", idx+1, err)
					return
				}
				activeBotsChan <- bot
			}(i)
		}

		wgBots.Wait()
		close(activeBotsChan)

		var activeBots []BotInstance
		for bot := range activeBotsChan {
			activeBots = append(activeBots, bot)
		}
		BotPool = activeBots
		botPoolMu.Unlock()
		log.Printf("Log Group connectivity verified. Active Bots: %d", len(activeBots))
	} else {
		log.Println("Log Group connectivity verified (Main Client only, skipping bot pool check during setup).")
	}
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
