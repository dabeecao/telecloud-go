package tgclient

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"telecloud/config"
	"telecloud/database"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tg"
)

var (
	ActiveWebAuth *WebAuth
	authMu        sync.Mutex
	LastAuthError error
)

func stopActiveAuth() {
	if ActiveWebAuth != nil {
		log.Println("Web Auth: Stopping previous active authentication...")
		ActiveWebAuth.Cancel(fmt.Errorf("SESSION_REPLACED"))
	}
}

func GetActiveWebAuth() *WebAuth {
	authMu.Lock()
	defer authMu.Unlock()
	return ActiveWebAuth
}

func StartWebAuth(cfg *config.Config) error {
	authMu.Lock()
	LastAuthError = nil
	stopActiveAuth()
	wa := NewWebAuth()
	ActiveWebAuth = wa
	authMu.Unlock()

	go func(w *WebAuth) {
		log.Println("Web Auth: Initializing Telegram client...")
		
		if err := InitClient(cfg, false); err != nil {
			log.Printf("Web Auth: InitClient failed: %v", err)
			w.Cancel(err)
			return
		}

		log.Println("Web Auth: Client initialized, starting Run...")
		err := Client.Run(tgCtx, func(ctx context.Context) error {
			log.Println("Web Auth: Client connected, starting IfNecessary loop...")
			for {
				flow := auth.NewFlow(w, auth.SendCodeOptions{})
				w.SetState("verifying")
				err := Client.Auth().IfNecessary(ctx, flow)
				if err == nil {
					break
				}
				
				errStr := err.Error()
				if strings.Contains(errStr, "PASSWORD_HASH_INVALID") || strings.Contains(errStr, "invalid password") || strings.Contains(errStr, "PASSWORD_INVALID") {
					log.Printf("Web Auth: Invalid password, retrying IfNecessary...")
					w.SetTransientErr("PASSWORD_HASH_INVALID")
					w.SetState("password_error")
					continue
				}
				
				if strings.Contains(errStr, "PHONE_CODE_INVALID") || strings.Contains(errStr, "invalid code") {
					log.Printf("Web Auth: Invalid OTP code, retrying IfNecessary...")
					w.SetTransientErr("PHONE_CODE_INVALID")
					w.SetState("code_error")
					continue
				}

				return err
			}
			log.Println("Web Auth: Authentication successful! Keeping connection alive for setup...")
			w.SetState("success")
			<-ctx.Done()
			return nil
		})

		if err != nil && err != context.Canceled {
			log.Printf("Web Auth: Client.Run exited with error: %v", err)
			w.Cancel(err)
		} else {
			log.Println("Web Auth: Connection closed.")
		}
	}(wa)
	return nil
}

func StartQRAuth(cfg *config.Config) error {
	authMu.Lock()
	LastAuthError = nil
	stopActiveAuth()
	wa := NewWebAuth()
	ActiveWebAuth = wa
	wa.SetState("qr")
	authMu.Unlock()

	go func(w *WebAuth) {
		log.Println("QR Auth: Initializing Telegram client...")
		if err := InitClient(cfg, false); err != nil {
			log.Printf("QR Auth: InitClient failed: %v", err)
			w.Cancel(err)
			return
		}

		log.Println("QR Auth: Client initialized, starting Run...")
		err := Client.Run(tgCtx, func(ctx context.Context) error {
			log.Println("QR Auth: Client connected, starting QR flow...")
			
			loggedIn := qrlogin.OnLoginToken(Dispatcher)
			
			_, err := Client.QR().Auth(ctx, loggedIn, func(ctx context.Context, token qrlogin.Token) error {
				log.Printf("QR Auth: New token generated: %s", token.URL())
				w.SetQRURL(token.URL())
				w.SetState("qr_ready")
				return nil
			})

			if err != nil {
				errStr := err.Error()
				if strings.Contains(errStr, "SESSION_PASSWORD_NEEDED") || strings.Contains(errStr, "PASSWORD_HASH_INVALID") || strings.Contains(errStr, "invalid password") {
					log.Println("QR Auth: 2FA password needed or invalid password, starting retry loop...")
					for {
						w.SetState("password")
						pwd, err := w.Password(ctx)
						if err != nil {
							log.Printf("QR Auth: Failed to get password from user: %v", err)
							return err
						}
						w.SetState("verifying")
						log.Println("QR Auth: Password received, submitting to Telegram...")
						_, err = Client.Auth().Password(ctx, pwd)
						if err == nil {
							log.Println("QR Auth: 2FA password accepted!")
							break
						}
						
						innerErrStr := err.Error()
						if strings.Contains(innerErrStr, "PASSWORD_HASH_INVALID") || strings.Contains(innerErrStr, "invalid password") {
							log.Printf("QR Auth: Invalid password, requesting again...")
							// Signal error but don't exit loop
							w.SetTransientErr("PASSWORD_HASH_INVALID")
							w.SetState("password_error")
							continue
						}
						log.Printf("QR Auth: 2FA password submission failed with unexpected error: %v", err)
						return err
					}
				} else {
					return err
				}
			}

			log.Println("QR Auth: Authentication successful! Keeping connection alive for setup...")
			w.SetState("success")
			<-ctx.Done()
			return nil
		})

		if err != nil && err != context.Canceled {
			log.Printf("QR Auth: Client.Run exited with error: %v", err)
			w.Cancel(err)
		} else {
			log.Println("QR Auth: Connection closed.")
		}
	}(wa)
	return nil
}

type WebAuth struct {
	phoneChan    chan string
	codeChan     chan string
	passwordChan chan string
	errChan      chan error
	mu           sync.RWMutex
	State        string
	QRURL        string
	LastErr      error
	TransientErr string
	ctx          context.Context
	cancel       context.CancelFunc
}

func NewWebAuth() *WebAuth {
	ctx, cancel := context.WithCancel(context.Background())
	return &WebAuth{
		phoneChan:    make(chan string, 1),
		codeChan:     make(chan string, 1),
		passwordChan: make(chan string, 1),
		errChan:      make(chan error, 1),
		State:        "phone",
		ctx:          ctx,
		cancel:       cancel,
	}
}

func (w *WebAuth) Phone(ctx context.Context) (string, error) {
	log.Println("Web Auth: Requesting phone number...")
	w.SetState("phone")
	select {
	case p := <-w.phoneChan:
		log.Printf("Web Auth: Received phone: %s", p)
		return p, nil
	case err := <-w.errChan:
		return "", err
	case <-ctx.Done():
		return "", ctx.Err()
	case <-w.ctx.Done():
		return "", w.ctx.Err()
	}
}

func (w *WebAuth) Password(ctx context.Context) (string, error) {
	w.SetState("password")
	select {
	case p := <-w.passwordChan:
		return p, nil
	case err := <-w.errChan:
		return "", err
	case <-ctx.Done():
		return "", ctx.Err()
	case <-w.ctx.Done():
		return "", w.ctx.Err()
	}
}

func (w *WebAuth) AcceptTermsOfService(ctx context.Context, tos tg.HelpTermsOfService) error {
	return nil
}

func (w *WebAuth) SignUp(ctx context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, fmt.Errorf("signup not supported")
}

func (w *WebAuth) Code(ctx context.Context, sentCode *tg.AuthSentCode) (string, error) {
	log.Println("Web Auth: Requesting OTP code...")
	w.SetState("code")
	select {
	case c := <-w.codeChan:
		log.Println("Web Auth: Received OTP code")
		return c, nil
	case err := <-w.errChan:
		return "", err
	case <-ctx.Done():
		return "", ctx.Err()
	case <-w.ctx.Done():
		return "", w.ctx.Err()
	}
}

func (w *WebAuth) SetState(state string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.State = state
}

func (w *WebAuth) GetState() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.State
}

func (w *WebAuth) SetQRURL(url string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.QRURL = url
}

func (w *WebAuth) GetQRURL() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.QRURL
}

func (w *WebAuth) SetTransientErr(err string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.TransientErr = err
}

func (w *WebAuth) GetTransientErr() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.TransientErr
}

func (w *WebAuth) SubmitPhone(phone string) {
	w.SetTransientErr("")
	w.SetState("verifying")
	select {
	case <-w.phoneChan:
	default:
	}
	w.phoneChan <- phone
}

func (w *WebAuth) SubmitCode(code string) {
	w.SetTransientErr("")
	w.SetState("verifying")
	select {
	case <-w.codeChan:
	default:
	}
	w.codeChan <- code
}

func (w *WebAuth) SubmitPassword(password string) {
	w.SetTransientErr("")
	w.SetState("verifying")
	select {
	case <-w.passwordChan:
	default:
	}
	w.passwordChan <- password
}

func (w *WebAuth) Cancel(err error) {
	log.Printf("Web Auth: Cancelling flow due to error: %v", err)
	
	errStr := err.Error()
	if strings.Contains(errStr, "API_ID_INVALID") || strings.Contains(errStr, "API_HASH_INVALID") {
		log.Println("Web Auth: Invalid API credentials detected, clearing from database...")
		database.DeleteSetting("api_id")
		database.DeleteSetting("api_hash")
	}

	authMu.Lock()
	LastAuthError = err
	authMu.Unlock()
	w.cancel()
	select {
	case w.errChan <- err:
	default:
	}
	authMu.Lock()
	if ActiveWebAuth == w {
		ActiveWebAuth = nil
	}
	authMu.Unlock()
	StopClient()
}

