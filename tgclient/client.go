package tgclient

import (
	"bufio"
	"context"
	crypto_rand "crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"mime"
	"net"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/proxy"
	"golang.org/x/term"

	"telecloud/config"
	"telecloud/database"
	"telecloud/utils"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/html"
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
	BotPoolMu  sync.RWMutex

	mainAuthorized int32
	mainRunning    int32
	systemReady    int32

	Dispatcher = tg.NewUpdateDispatcher()
	tgMu       sync.Mutex

	// botFileWorkers is a bounded channel used as a semaphore to limit concurrent
	// bot file-processing goroutines. This prevents Telegram flood-wait errors
	// when a user sends a large album or many files at once.
	// Reduced from 16 to 3 to prevent overwhelming Telegram/log group rate limits.
	botFileWorkers = make(chan struct{}, 3)

	// botMutexes is a thread-safe map storing a mutex per botToken to serialize
	// processing of messages sequentially on a per-bot basis.
	botMutexes sync.Map

	AppConfig *config.Config
	UserbotCooldownUntil time.Time
	initializationDone int32
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

func IsInitializationDone() bool {
	return atomic.LoadInt32(&initializationDone) == 1
}

func SetInitializationDone(done bool) {
	if done {
		atomic.StoreInt32(&initializationDone, 1)
	} else {
		atomic.StoreInt32(&initializationDone, 0)
	}
}

func SetSystemReady(ready bool) {
	if ready {
		atomic.StoreInt32(&systemReady, 1)
	} else {
		atomic.StoreInt32(&systemReady, 0)
	}
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
	Client        *telegram.Client
	Token         string
	Deleted       bool // Mark as deleted if initialization fails
	CooldownUntil time.Time
	Ctx           context.Context
	Cancel        context.CancelFunc // To stop this bot instance dynamically
}

func GetAPI() *tg.Client {
	BotPoolMu.RLock()
	defer BotPoolMu.RUnlock()

	var activeIndices []int
	now := time.Now()
	for i := range BotPool {
		if !BotPool[i].Deleted && now.After(BotPool[i].CooldownUntil) {
			activeIndices = append(activeIndices, i)
		}
	}

	userbotActive := now.After(UserbotCooldownUntil)

	// If everything is rate-limited, fallback to Client.API()
	if len(activeIndices) == 0 && !userbotActive {
		return Client.API()
	}

	var total uint32
	if userbotActive {
		total = uint32(len(activeIndices) + 1)
	} else {
		total = uint32(len(activeIndices))
	}

	idx := atomic.AddUint32(&botCounter, 1) % total
	if userbotActive {
		if idx == 0 {
			return Client.API()
		}
		return BotPool[activeIndices[idx-1]].Client.API()
	}
	return BotPool[activeIndices[idx]].Client.API()
}

func MarkBotCooldown(api *tg.Client, seconds int) {
	BotPoolMu.Lock()
	defer BotPoolMu.Unlock()

	now := time.Now()
	cooldownTime := now.Add(time.Duration(seconds) * time.Second)

	if api == Client.API() {
		if UserbotCooldownUntil.Before(cooldownTime) {
			UserbotCooldownUntil = cooldownTime
			log.Printf("[BotPool] Main Userbot is rate-limited. Cooling down for %d seconds...", seconds)
		}
		return
	}

	for i := range BotPool {
		if BotPool[i].Client.API() == api {
			if BotPool[i].CooldownUntil.Before(cooldownTime) {
				BotPool[i].CooldownUntil = cooldownTime
				log.Printf("[BotPool] Bot #%d (%s) is rate-limited. Cooling down for %d seconds...", i+1, BotPool[i].Token[:8]+"...", seconds)
			}
			break
		}
	}
}

func ParseFloodWait(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	errStr := err.Error()
	if idx := strings.Index(errStr, "FLOOD_WAIT_"); idx != -1 {
		sub := errStr[idx+len("FLOOD_WAIT_"):]
		var digits []rune
		for _, r := range sub {
			if r >= '0' && r <= '9' {
				digits = append(digits, r)
			} else {
				break
			}
		}
		if len(digits) > 0 {
			if secs, err := strconv.Atoi(string(digits)); err == nil {
				return secs, true
			}
		}
		return 10, true
	}
	return 0, false
}

func GetBotCount() int {
	BotPoolMu.RLock()
	defer BotPoolMu.RUnlock()
	return len(BotPool)
}

func GetBotStatuses(cfg *config.Config) map[string]string {
	BotPoolMu.RLock()
	defer BotPoolMu.RUnlock()

	statuses := make(map[string]string)
	for _, tok := range cfg.BotTokens {
		statuses[tok] = "error:offline"
	}

	for _, bot := range BotPool {
		if !bot.Deleted {
			statuses[bot.Token] = "success"
		} else {
			statuses[bot.Token] = "error:failed"
		}
	}

	return statuses
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
	if err != nil || len(data) == 0 {
		return nil, session.ErrNotFound
	}
	// Decrypt blobs that were stored with EncryptAEAD.
	plain, err := utils.DecryptAEAD(data)
	if err != nil {
		// If decryption fails, the data could be a legacy plaintext JSON session
		// from pre-encryption. We verify if it is valid JSON.
		// If it's NOT valid JSON, it's either corrupted or encrypted with a different key,
		// so we must treat it as not found so gotd can cleanly recreate the session.
		if json.Valid(data) {
			log.Printf("[Session] Loaded legacy plaintext session for %s", s.SessionID)
			return data, nil
		}
		log.Printf("[Session] Decryption failed and data is not valid JSON for %s. Treating as ErrNotFound.", s.SessionID)
		return nil, session.ErrNotFound
	}
	return plain, nil
}

func (s *DBSessionStorage) StoreSession(ctx context.Context, data []byte) error {
	enc, err := utils.EncryptAEAD(data)
	if err != nil {
		return err
	}
	return database.SetTGSession(s.SessionID, enc)
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
	atomic.StoreInt32(&systemReady, 0)
	atomic.StoreInt32(&initializationDone, 0)
}

func InitClient(cfg *config.Config, runAuthFlow bool) error {
	tgMu.Lock()
	defer tgMu.Unlock()

	AppConfig = cfg

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
		botOptions := options
		botOptions.SessionStorage = &DBSessionStorage{SessionID: token}
		botDispatcher := tg.NewUpdateDispatcher()
		registerBotUpdateHandlers(&botDispatcher, token)
		botOptions.UpdateHandler = botDispatcher
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
				if len(BotPool) > 0 {
					cfg.MaxPartSize = 500 * 1024 * 1024
				} else if isPremium {
					cfg.MaxPartSize = 3900 * 1024 * 1024
				} else {
					cfg.MaxPartSize = 1900 * 1024 * 1024
				}
			}

			// 2. Start Bot Pool ONLY after main client is up
			if !SkipBotPool {
				for i := range BotPool {
					ready := make(chan bool, 1)
					botCtx, botCancel := context.WithCancel(ctx)
					BotPool[i].Cancel = botCancel
					BotPool[i].Ctx = botCtx
					go func(idx int, r chan bool, bCtx context.Context) {
						b := &BotPool[idx]
						log.Printf("Initializing Bot #%d session...", idx+1)

						// Try login
						err := b.Client.Run(bCtx, func(ctx context.Context) error {
							_, err := b.Client.Auth().Bot(ctx, b.Token)
							if err != nil {
								return err
							}
							r <- true    // Success
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
					}(i, ready, botCtx)

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
		errCh <- err
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

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func VerifyLogGroup(ctx context.Context, cfg *config.Config) error {
	if cfg.LogGroupID == "" {
		return fmt.Errorf("LOG_GROUP_ID is not configured")
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

	// Verify all bots in the pool ONLY if SkipBotPool is false
	if !SkipBotPool {
		BotPoolMu.Lock()
		var activeBots []BotInstance
		for i, bot := range BotPool {
			if bot.Deleted {
				continue
			}

			api := bot.Client.API()
			// Try to resolve group for this bot
			botPeer, err := resolveLogGroup(ctx, api, cfg.LogGroupID)
			if err != nil {
				errMsg := err.Error()
				if strings.Contains(errMsg, "PEER_ID_INVALID") || strings.Contains(errMsg, "CHANNEL_INVALID") {
					errMsg = "Bot is not a member of the Log Group or ID is invalid"
				}
				log.Printf("Warning: Bot #%d: resolution failed: %v. Removing from pool.", i+1, errMsg)
				continue
			}

			// Try to send a message
			botSender := message.NewSender(api)
			_, err = botSender.To(botPeer).Text(ctx, fmt.Sprintf("🤖 Bot #%d (%s) is online and reporting for duty!", i+1, bot.Token[:8]+"..."))
			if err != nil {
				log.Printf("Warning: Bot #%d: connectivity check failed: %v. Removing from pool.", i+1, err)
				continue
			}

			activeBots = append(activeBots, bot)
			// Small delay between bots to be safe and avoid flooding
			time.Sleep(100 * time.Millisecond)
		}

		BotPool = activeBots
		BotPoolMu.Unlock()
		log.Printf("Log Group connectivity verified. Active Bots: %d", len(activeBots))
	} else {
		log.Println("Log Group connectivity verified (Main Client only, skipping bot pool check during setup).")
	}
	return nil
}

// forgetLogGroupPeer drops the cached log-group peer of api so it is resolved
// again on the next use.
func forgetLogGroupPeer(api *tg.Client, logGroupIDStr string) {
	botPeers.Delete(fmt.Sprintf("%p_%s", api, logGroupIDStr))
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

func VerifyBotToken(ctx context.Context, cfg *config.Config, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("empty bot token")
	}

	options := telegram.Options{
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
	}

	botClient := telegram.NewClient(cfg.APIID, cfg.APIHash, options)

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	var loginErr error
	runErr := botClient.Run(ctx, func(ctx context.Context) error {
		_, err := botClient.Auth().Bot(ctx, token)
		if err != nil {
			loginErr = err
			return err
		}

		api := botClient.API()
		peer, err := resolveLogGroup(ctx, api, cfg.LogGroupID)
		if err != nil {
			loginErr = err
			return err
		}

		sender := message.NewSender(api)
		_, err = sender.To(peer).Text(ctx, fmt.Sprintf("🤖 Bot (%s) setup verification: OK", token[:8]+"..."))
		if err != nil {
			loginErr = err
			return err
		}

		return nil
	})

	if loginErr != nil {
		return loginErr
	}
	if runErr != nil && runErr != context.Canceled {
		return runErr
	}
	return nil
}

func UpdateBotPool(cfg *config.Config, newTokens []string) {
	BotPoolMu.Lock()
	defer BotPoolMu.Unlock()

	AppConfig = cfg

	log.Println("Updating Bot Pool dynamically...")

	// 1. Stop all existing bots
	for _, bot := range BotPool {
		if bot.Cancel != nil {
			bot.Cancel()
		}
	}
	BotPool = nil

	// 2. Initialize new bots
	cfg.BotTokens = newTokens
	isPrivateMe := cfg.LogGroupID == "me" || cfg.LogGroupID == "self"
	if isPrivateMe {
		log.Println("[BotPool] Multi-bot disabled — Bots cannot access Saved Messages.")
		return
	}

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
		u, _ := url.Parse(cfg.ProxyURL)
		dialer, _ := proxy.FromURL(u, proxy.Direct)
		options.Resolver = dcs.Plain(dcs.PlainOptions{
			Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if d, ok := dialer.(proxy.ContextDialer); ok {
					return d.DialContext(ctx, network, addr)
				}
				return dialer.Dial(network, addr)
			},
		})
	}

	for _, token := range cfg.BotTokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		botOptions := options
		botOptions.SessionStorage = &DBSessionStorage{SessionID: token}
		botDispatcher := tg.NewUpdateDispatcher()
		registerBotUpdateHandlers(&botDispatcher, token)
		botOptions.UpdateHandler = botDispatcher
		botClient := telegram.NewClient(cfg.APIID, cfg.APIHash, botOptions)

		botCtx, botCancel := context.WithCancel(tgCtx)
		BotPool = append(BotPool, BotInstance{
			Client: botClient,
			Token:  token,
			Ctx:    botCtx,
			Cancel: botCancel,
		})
	}

	// 3. Start bots if system is running
	if atomic.LoadInt32(&mainRunning) == 1 && !SkipBotPool {
		for i := range BotPool {
			go func(idx int) {
				b := &BotPool[idx]
				log.Printf("Initializing dynamic Bot #%d session...", idx+1)
				err := b.Client.Run(b.Ctx, func(ctx context.Context) error {
					_, err := b.Client.Auth().Bot(ctx, b.Token)
					if err != nil {
						return err
					}
					<-ctx.Done()
					return nil
				})
				if err != nil && err != context.Canceled {
					if strings.Contains(err.Error(), "AUTH_KEY_UNREGISTERED") {
						database.DB.Exec("DELETE FROM tg_sessions WHERE session_id = ?", b.Token)
					}
					log.Printf("Warning: Dynamic Bot #%d encountered error: %v", idx+1, err)
					b.Deleted = true
				}
			}(i)
		}
	}
}

func registerBotUpdateHandlers(dispatcher *tg.UpdateDispatcher, botToken string) {
	// Only OnNewMessage: bot-only sessions only receive private DM updates.
	// Registering OnNewChannelMessage is unnecessary and wastes cycles.
	dispatcher.OnNewMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewMessage) error {
		// Capture values before the goroutine — ctx will be released by the
		// dispatcher once this handler returns, so we use Background() for
		// the actual (potentially long-running) work.
		msg := u.Message
		entities := e
		go func() {
			// 1. Serialize message processing sequentially on a per-bot basis.
			val, _ := botMutexes.LoadOrStore(botToken, &sync.Mutex{})
			mu := val.(*sync.Mutex)
			mu.Lock()
			defer mu.Unlock()

			// 2. Check bot rate-limit cooldown state. If rate-limited, wait out the
			// cooldown period *outside* of the global semaphore slot to keep it free for other bots.
			BotPoolMu.RLock()
			var cooldown time.Duration
			for _, bot := range BotPool {
				if bot.Token == botToken {
					if time.Now().Before(bot.CooldownUntil) {
						cooldown = time.Until(bot.CooldownUntil)
					}
					break
				}
			}
			BotPoolMu.RUnlock()

			if cooldown > 0 {
				log.Printf("[BotPool] Bot %s... is rate-limited. Waiting %v before processing next message.", botToken[:8], cooldown)
				time.Sleep(cooldown)
			}

			// 3. Acquire a global worker slot.
			botFileWorkers <- struct{}{}        // acquire slot
			defer func() { <-botFileWorkers }() // release slot
			workCtx := context.Background()
			if err := handleBotNewMessage(workCtx, entities, msg, botToken); err != nil {
				log.Printf("[BotPool] handleBotNewMessage error: %v", err)
			}
		}()
		return nil
	})
}

func cryptoRandInt64() (int64, error) {
	var b [8]byte
	_, err := crypto_rand.Read(b[:])
	if err != nil {
		return 0, err
	}
	return int64(binary.BigEndian.Uint64(b[:])), nil
}

// botMediaInfo holds normalized info extracted from any media type.
type botMediaInfo struct {
	doc      *tg.Document // non-nil for document/file
	photo    *tg.Photo    // non-nil for compressed photo
	size     int64
	mimeType string
	filename string
}

// extractBotMedia normalises MessageMediaDocument and MessageMediaPhoto into
// a single botMediaInfo so the rest of the handler is media-type agnostic.
func extractBotMedia(msg *tg.Message) (*botMediaInfo, bool) {
	switch m := msg.Media.(type) {
	case *tg.MessageMediaDocument:
		doc, ok := m.Document.(*tg.Document)
		if !ok {
			return nil, false
		}
		// Clear thumbnails to prevent errors when forwarding/sending media
		// because the sub-bot does not have access/permissions to resolve or fetch them.
		doc.Thumbs = nil
		doc.VideoThumbs = nil

		info := &botMediaInfo{
			doc:      doc,
			size:     doc.Size,
			mimeType: doc.MimeType,
			filename: "file",
		}
		if info.mimeType == "" {
			info.mimeType = "application/octet-stream"
		}
		for _, attr := range doc.Attributes {
			if fAttr, ok := attr.(*tg.DocumentAttributeFilename); ok {
				info.filename = fAttr.FileName
				break
			}
		}
		return info, true

	case *tg.MessageMediaPhoto:
		photo, ok := m.Photo.(*tg.Photo)
		if !ok {
			return nil, false
		}
		// Pick the largest photo size available.
		var bestSize int64
		for _, sz := range photo.Sizes {
			if ps, ok := sz.(*tg.PhotoSize); ok {
				if int64(ps.Size) > bestSize {
					bestSize = int64(ps.Size)
				}
			}
		}
		if bestSize == 0 {
			// PhotoSizeProgressive or unknown — use a small estimate
			bestSize = 1
		}
		ts := time.Unix(int64(photo.Date), 0).UTC().Format("20060102_150405")
		return &botMediaInfo{
			photo:    photo,
			size:     bestSize,
			mimeType: "image/jpeg",
			filename: "photo_" + ts + ".jpg",
		}, true
	}
	return nil, false
}

func handleBotNewMessage(ctx context.Context, e tg.Entities, msgClass tg.MessageClass, botToken string) error {
	msg, ok := msgClass.(*tg.Message)
	if !ok {
		return nil
	}

	// Only process private chats (direct messages user-to-bot). Ignore basic groups, channels, supergroups.
	if _, ok := msg.PeerID.(*tg.PeerUser); !ok {
		return nil
	}

	mediaInfo, ok := extractBotMedia(msg)
	if !ok {
		return nil
	}

	var senderUserID int64
	if fromUser, ok := msg.FromID.(*tg.PeerUser); ok {
		senderUserID = fromUser.UserID
	} else if peerUser, ok := msg.PeerID.(*tg.PeerUser); ok {
		senderUserID = peerUser.UserID
	}

	if senderUserID == 0 {
		return nil
	}

	var ownerUsername string
	query := "SELECT username FROM user_settings WHERE `key` = 'telegram_user_id' AND value = ?"
	if database.IsPostgres() {
		query = "SELECT username FROM user_settings WHERE \"key\" = 'telegram_user_id' AND value = ?"
	} else if !database.IsMySQL() {
		query = "SELECT username FROM user_settings WHERE key = 'telegram_user_id' AND value = ?"
	}
	err := database.RODB.Get(&ownerUsername, query, strconv.FormatInt(senderUserID, 10))
	if err != nil || ownerUsername == "" {
		return nil
	}

	var fromPeer tg.InputPeerClass
	if peerUser, ok := msg.PeerID.(*tg.PeerUser); ok {
		if u, ok := e.Users[peerUser.UserID]; ok {
			fromPeer = &tg.InputPeerUser{UserID: u.ID, AccessHash: u.AccessHash}
		}
	} else if peerChat, ok := msg.PeerID.(*tg.PeerChat); ok {
		fromPeer = &tg.InputPeerChat{ChatID: peerChat.ChatID}
	} else if peerChannel, ok := msg.PeerID.(*tg.PeerChannel); ok {
		if c, ok := e.Channels[peerChannel.ChannelID]; ok {
			fromPeer = &tg.InputPeerChannel{ChannelID: c.ID, AccessHash: c.AccessHash}
		}
	}

	if fromPeer == nil {
		return nil
	}

	BotPoolMu.RLock()
	var botClient *tg.Client
	for _, bot := range BotPool {
		if bot.Token == botToken {
			botClient = bot.Client.API()
			break
		}
	}
	BotPoolMu.RUnlock()

	if botClient == nil {
		return fmt.Errorf("bot instance not found for token")
	}

	logGroupID := ""
	if AppConfig != nil {
		logGroupID = AppConfig.LogGroupID
	}
	if logGroupID == "" {
		logGroupID = database.GetSetting("log_group_id")
	}

	toPeer, err := resolveLogGroup(ctx, botClient, logGroupID)
	if err != nil {
		log.Printf("[BotPool] Failed to resolve log group for forwarding: %v", err)
		return nil
	}

	docSize := mediaInfo.size
	docMimeType := mediaInfo.mimeType
	docFilename := mediaInfo.filename

	// Smart file format auto-detection for voice recordings, audio, video messages, or missing extensions.
	// mime.ExtensionsByType returns extensions sorted alphabetically, which gives wrong results for many
	// common types (e.g. video/mp4 → ".mpv" or ".m4v" instead of ".mp4"). Use a curated table first.
	if docFilename == "file" || !strings.Contains(docFilename, ".") {
		// Preferred extension table — takes priority over mime.ExtensionsByType.
		preferredExt := map[string]string{
			"video/mp4":                    ".mp4",
			"video/webm":                   ".webm",
			"video/x-matroska":             ".mkv",
			"video/quicktime":              ".mov",
			"video/x-msvideo":              ".avi",
			"video/mpeg":                   ".mpeg",
			"video/3gpp":                   ".3gp",
			"audio/mpeg":                   ".mp3",
			"audio/mp4":                    ".m4a",
			"audio/ogg":                    ".ogg",
			"audio/webm":                   ".weba",
			"audio/wav":                    ".wav",
			"audio/flac":                   ".flac",
			"audio/x-flac":                 ".flac",
			"audio/aac":                    ".aac",
			"audio/opus":                   ".opus",
			"image/jpeg":                   ".jpg",
			"image/png":                    ".png",
			"image/gif":                    ".gif",
			"image/webp":                   ".webp",
			"image/heic":                   ".heic",
			"image/heif":                   ".heif",
			"application/pdf":              ".pdf",
			"application/zip":              ".zip",
			"application/x-tar":            ".tar",
			"application/gzip":             ".gz",
			"application/x-7z-compressed":  ".7z",
			"application/x-rar-compressed": ".rar",
			"text/plain":                   ".txt",
		}
		if ext, ok := preferredExt[docMimeType]; ok {
			docFilename = docFilename + ext
		} else {
			// Fallback: try mime package, but skip the first result if there are multiple
			// (alphabetically first is often wrong, e.g. .m4v before .mp4).
			exts, _ := mime.ExtensionsByType(docMimeType)
			if len(exts) > 0 {
				docFilename = docFilename + exts[0]
			} else if strings.HasPrefix(docMimeType, "image/") {
				docFilename = docFilename + ".jpg"
			} else if strings.HasPrefix(docMimeType, "video/") {
				docFilename = docFilename + ".mp4"
			} else if strings.HasPrefix(docMimeType, "audio/") {
				docFilename = docFilename + ".mp3"
			}
		}
	}

	// Standardized consistent HTML caption for forwarded files, stripping user's original caption
	finalCaption := "<b>📄 File:</b> " + docFilename + "\n\n<b>🚀 Powered by TeleCloud Go</b>\n<i>Unlimited Cloud Storage via Telegram</i>\n\n🔗 <a href=\"https://github.com/dabeecao/telecloud-go\">GitHub Repository</a>"

	sender := message.NewSender(botClient)
	var updates tg.UpdatesClass
	// Retry up to 3 times to handle transient FLOOD_WAIT from Telegram.
	// Without retry, files sent during a burst are silently lost.
	const maxRetries = 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(attempt*attempt) * 5 * time.Second
			log.Printf("[BotPool] Retrying forward to log group (attempt %d/%d) after %v", attempt+1, maxRetries, backoff)
			time.Sleep(backoff)
		}
		if mediaInfo.doc != nil {
			updates, err = sender.To(toPeer).Silent().Document(ctx, mediaInfo.doc, html.String(nil, finalCaption))
		} else {
			updates, err = sender.To(toPeer).Silent().Photo(ctx, mediaInfo.photo, html.String(nil, finalCaption))
		}
		if err == nil {
			break
		}
		// If Telegram returns FLOOD_WAIT, respect the wait duration instead of a fixed backoff.
		if secs, ok := ParseFloodWait(err); ok {
			MarkBotCooldown(botClient, secs)
			waitDur := time.Duration(secs+1) * time.Second
			log.Printf("[BotPool] FLOOD_WAIT %ds while forwarding file — waiting before retry", secs)
			time.Sleep(waitDur)
		}
	}
	if err != nil {
		log.Printf("[BotPool] Failed to send media to log group after %d attempts: %v", maxRetries, err)
		return nil
	}

	var newMessageID int
	switch u := updates.(type) {
	case *tg.Updates:
		for _, upd := range u.Updates {
			if newMessage, ok := upd.(*tg.UpdateNewMessage); ok {
				newMessageID = newMessage.Message.GetID()
			} else if newChannelMessage, ok := upd.(*tg.UpdateNewChannelMessage); ok {
				newMessageID = newChannelMessage.Message.GetID()
			}
		}
	case *tg.UpdatesCombined:
		for _, upd := range u.Updates {
			if newMessage, ok := upd.(*tg.UpdateNewMessage); ok {
				newMessageID = newMessage.Message.GetID()
			} else if newChannelMessage, ok := upd.(*tg.UpdateNewChannelMessage); ok {
				newMessageID = newChannelMessage.Message.GetID()
			}
		}
	case *tg.UpdateShortSentMessage:
		newMessageID = u.ID
	}

	if newMessageID == 0 {
		log.Printf("[BotPool] Could not extract sent message ID")
		return nil
	}

	folderName := ""
	folderQuery := "SELECT value FROM user_settings WHERE username = ? AND `key` = 'bot_pool_upload_folder'"
	if database.IsPostgres() {
		folderQuery = "SELECT value FROM user_settings WHERE username = ? AND \"key\" = 'bot_pool_upload_folder'"
	} else if !database.IsMySQL() {
		folderQuery = "SELECT value FROM user_settings WHERE username = ? AND key = 'bot_pool_upload_folder'"
	}
	_ = database.RODB.Get(&folderName, folderQuery, ownerUsername)
	// Sanitize: mirror the same rules applied in handlePostBotUserSettings.
	// Prevents path traversal if the DB row was ever set by an older version.
	if cleaned := path.Clean(folderName); cleaned == "" || cleaned == "." || cleaned == "/" || strings.HasPrefix(cleaned, "..") {
		folderName = "TelegramUpload"
	} else {
		folderName = strings.Trim(cleaned, "/")
	}
	if folderName == "" {
		folderName = "TelegramUpload"
	}

	adminUsername := database.GetSetting("admin_username")
	if adminUsername == "" {
		adminUsername = "admin"
	}

	var folderPath string
	if ownerUsername == adminUsername {
		folderPath = "/" + folderName
	} else {
		folderPath = "/" + ownerUsername + "/" + folderName
	}

	err = database.EnsureFoldersExist(folderPath, ownerUsername)
	if err != nil {
		log.Printf("[BotPool] Failed to ensure folders exist for %s: %v", ownerUsername, err)
		return nil
	}

	unlockInsert := database.AcquireFileInsertLock(ownerUsername, folderPath)
	uniqueFilename := database.GetUniqueFilename(database.RODB, folderPath, docFilename, false, 0, ownerUsername)

	tx, err := database.DB.Beginx()
	if err != nil {
		unlockInsert()
		log.Printf("[BotPool] Failed to start DB transaction: %v", err)
		return nil
	}
	defer tx.Rollback()

	fileID, err := database.InsertAndGetID(tx,
		"INSERT INTO files (filename, path, size, mime_type, is_folder, owner, message_id) VALUES (?, ?, ?, ?, FALSE, ?, ?)",
		uniqueFilename, folderPath, docSize, docMimeType, ownerUsername, newMessageID,
	)
	_ = fileID // suppress unused warning if InsertAndGetID returns int
	if err != nil {
		unlockInsert()
		log.Printf("[BotPool] Failed to insert file record: %v", err)
		return nil
	}

	err = tx.Commit()
	unlockInsert()
	if err != nil {
		log.Printf("[BotPool] Failed to commit DB transaction: %v", err)
		return nil
	}

	// Trigger background thumbnail generation
	if AppConfig != nil {
		go func(fid int64) {
			_, _ = RegenerateFileThumbnail(context.Background(), fid, AppConfig)
		}(fileID)
	}

	// Queue a debounced confirmation message back to the Telegram sender via the sub-bot
	cleanRelFolder := strings.TrimPrefix(strings.TrimPrefix(folderPath, "/"+ownerUsername), "/")
	if cleanRelFolder == "" {
		cleanRelFolder = "/"
	}
	queueConfirmation(botClient, fromPeer, botToken, senderUserID, uniqueFilename, cleanRelFolder)

	log.Printf("[BotPool] Successfully received, forwarded, and recorded file %s (%d bytes) in %s belonging to %s as message %d", uniqueFilename, docSize, folderPath, ownerUsername, newMessageID)
	return nil
}

// Debounced confirmation structures and logic
type confirmKey struct {
	botToken string
	userID   int64
}

type pendingConfirm struct {
	filenames []string
	folder    string
	botClient *tg.Client
	fromPeer  tg.InputPeerClass
	timer     *time.Timer
}

var (
	confirmMu     sync.Mutex
	confirmations = make(map[confirmKey]*pendingConfirm)
)

func queueConfirmation(botClient *tg.Client, fromPeer tg.InputPeerClass, botToken string, userID int64, filename string, folder string) {
	confirmMu.Lock()
	defer confirmMu.Unlock()

	key := confirmKey{botToken: botToken, userID: userID}
	pc, ok := confirmations[key]
	if !ok {
		pc = &pendingConfirm{
			botClient: botClient,
			fromPeer:  fromPeer,
			folder:    folder,
		}
		confirmations[key] = pc
	}

	pc.filenames = append(pc.filenames, filename)
	pc.folder = folder

	if pc.timer != nil {
		pc.timer.Stop()
	}

	pc.timer = time.AfterFunc(1500*time.Millisecond, func() {
		sendDebouncedConfirmation(key)
	})
}

func sendDebouncedConfirmation(key confirmKey) {
	confirmMu.Lock()
	pc, ok := confirmations[key]
	if !ok {
		confirmMu.Unlock()
		return
	}
	filenames := pc.filenames
	folder := pc.folder
	botClient := pc.botClient
	fromPeer := pc.fromPeer
	delete(confirmations, key)
	confirmMu.Unlock()

	if len(filenames) == 0 {
		return
	}

	var confirmText string
	if len(filenames) == 1 {
		confirmText = fmt.Sprintf("✅ <b>Successfully received file:</b> <code>%s</code>\n📂 <b>Folder:</b> <code>/%s</code>\n☁️ <b>TeleCloud upload completed!</b>", filenames[0], folder)
	} else {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("✅ <b>Successfully received %d files:</b>\n", len(filenames)))
		
		maxShow := 15
		for i, name := range filenames {
			if i >= maxShow {
				sb.WriteString(fmt.Sprintf("• <i>and %d more files...</i>\n", len(filenames)-maxShow))
				break
			}
			sb.WriteString(fmt.Sprintf("• <code>%s</code>\n", name))
		}
		
		sb.WriteString(fmt.Sprintf("\n📂 <b>Folder:</b> <code>/%s</code>\n☁️ <b>TeleCloud upload completed!</b>", folder))
		confirmText = sb.String()
	}

	confirmMsgOpt := html.String(nil, confirmText)
	botSender := message.NewSender(botClient)
	_, sendErr := botSender.To(fromPeer).Silent().StyledText(context.Background(), confirmMsgOpt)
	if sendErr != nil {
		log.Printf("[BotPool] Failed to send debounced confirmation response to user %d: %v", key.userID, sendErr)
	}
}
