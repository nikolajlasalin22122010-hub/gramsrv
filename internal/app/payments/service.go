package payments

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// Config carries provider credentials (env or DB settings).
type Config struct {
	YooMoneyWallet  string
	YooMoneySecret  string
	ReturnURL       string
	PlategaMerchant string
	PlategaSecret   string
	PlategaAPIBase  string
	PublicBaseURL   string
}

// SettingsReader reads admin-editable settings (DB overrides env).
type SettingsReader interface {
	GetSetting(ctx context.Context, key string) (string, error)
}

const (
	keyYooMoneyWallet  = "payments.yoomoney.wallet"
	keyYooMoneySecret  = "payments.yoomoney.secret"
	keyPlategaMerchant = "payments.platega.merchant"
	keyPlategaSecret   = "payments.platega.secret"
)

// Service creates payment invoices and settles provider notifications.
type Service struct {
	store    store.PaymentStore
	settings SettingsReader
	hooks    store.PaymentWebhookStore
	cfg      Config
	log      *zap.Logger
	onPaid   func(context.Context, domain.PaymentInvoice)
}

func NewService(st store.PaymentStore, cfg Config, log *zap.Logger) *Service {
	if log == nil {
		log = zap.NewNop()
	}
	return &Service{store: st, cfg: cfg, log: log}
}

// WithSettings injects the DB-backed settings store.
func (s *Service) WithSettings(settings SettingsReader) *Service {
	s.settings = settings
	return s
}

// WithPaymentWebhooks injects the per-bot successful_payment webhook store.
func (s *Service) WithPaymentWebhooks(hooks store.PaymentWebhookStore) *Service {
	s.hooks = hooks
	return s
}

func (s *Service) yooWallet(ctx context.Context) string {
	if s.settings != nil {
		if value, err := s.settings.GetSetting(ctx, keyYooMoneyWallet); err == nil && value != "" {
			return value
		}
	}
	return s.cfg.YooMoneyWallet
}

func (s *Service) yooSecret(ctx context.Context) string {
	if s.settings != nil {
		if value, err := s.settings.GetSetting(ctx, keyYooMoneySecret); err == nil && value != "" {
			return value
		}
	}
	return s.cfg.YooMoneySecret
}

// CreateYooMoneyInvoice stores an invoice and returns it with the quickpay URL.
func (s *Service) CreateYooMoneyInvoice(ctx context.Context, botUserID, userID, amount int64, title, description, payload string) (domain.PaymentInvoice, error) {
	title = strings.TrimSpace(title)
	wallet := s.yooWallet(ctx)
	if wallet == "" {
		return domain.PaymentInvoice{}, domain.ErrPaymentProviderNotConfigured
	}
	if amount <= 0 || title == "" {
		return domain.PaymentInvoice{}, domain.ErrPaymentInvalid
	}
	inv, err := s.store.CreatePaymentInvoice(ctx, domain.PaymentInvoice{
		BotUserID:   botUserID,
		UserID:      userID,
		Provider:    "yoomoney",
		Currency:    "RUB",
		Amount:      amount,
		Title:       title,
		Description: strings.TrimSpace(description),
		Payload:     payload,
	})
	if err != nil {
		return domain.PaymentInvoice{}, err
	}
	inv.PaymentURL = s.yooMoneyQuickpayURL(wallet, inv)
	if err := s.store.SetPaymentInvoiceURL(ctx, inv.ID, inv.PaymentURL); err != nil {
		return domain.PaymentInvoice{}, err
	}
	return inv, nil
}

func (s *Service) yooMoneyQuickpayURL(wallet string, inv domain.PaymentInvoice) string {
	q := url.Values{}
	q.Set("receiver", wallet)
	q.Set("quickpay-form", "button")
	q.Set("paymentType", "AC")
	q.Set("sum", fmt.Sprintf("%.2f", float64(inv.Amount)/100))
	q.Set("label", "inv"+strconv.FormatInt(inv.ID, 10))
	q.Set("targets", inv.Title)
	if s.cfg.ReturnURL != "" {
		q.Set("successURL", s.cfg.ReturnURL)
	}
	return "https://yoomoney.ru/quickpay/confirm.xml?" + q.Encode()
}

func (s *Service) GetInvoice(ctx context.Context, id int64) (domain.PaymentInvoice, bool, error) {
	return s.store.GetPaymentInvoice(ctx, id)
}

// HandleYooMoneyNotification verifies the sha1 signature and marks the invoice paid.
func (s *Service) HandleYooMoneyNotification(ctx context.Context, form url.Values) (int64, bool, error) {
	secret := s.yooSecret(ctx)
	if secret == "" {
		return 0, false, domain.ErrPaymentProviderNotConfigured
	}
	base := strings.Join([]string{
		form.Get("notification_type"),
		form.Get("operation_id"),
		form.Get("amount"),
		form.Get("currency"),
		form.Get("datetime"),
		form.Get("sender"),
		form.Get("codepro"),
		secret,
		form.Get("label"),
	}, "&")
	sum := sha1.Sum([]byte(base))
	want := hex.EncodeToString(sum[:])
	got := strings.ToLower(strings.TrimSpace(form.Get("sha1_hash")))
	if subtle.ConstantTimeCompare([]byte(want), []byte(got)) != 1 {
		s.log.Warn("payments: yoomoney signature mismatch", zap.String("label", form.Get("label")))
		return 0, false, domain.ErrPaymentInvalid
	}
	label := strings.TrimPrefix(strings.TrimSpace(form.Get("label")), "inv")
	id, err := strconv.ParseInt(label, 10, 64)
	if err != nil || id <= 0 {
		s.log.Info("payments: yoomoney notification without label", zap.String("label", form.Get("label")))
		return 0, false, nil
	}
	paid, err := s.store.MarkPaymentInvoicePaid(ctx, id, strings.TrimSpace(form.Get("operation_id")))
	if err == nil && paid {
		if invoice, found, getErr := s.store.GetPaymentInvoice(ctx, id); getErr == nil && found {
			s.notifyPayment(ctx, invoice)
			s.firePaid(ctx, invoice)
		}
	}
	return id, paid, err
}

// WithPaidHook registers a callback invoked right after an invoice is marked paid.
func (s *Service) WithPaidHook(hook func(context.Context, domain.PaymentInvoice)) *Service {
	s.onPaid = hook
	return s
}

// firePaid runs the paid hook without letting a panic break the webhook path.
func (s *Service) firePaid(ctx context.Context, invoice domain.PaymentInvoice) {
	if s.onPaid == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			s.log.Warn("payments: paid hook panic", zap.Any("panic", r))
		}
	}()
	s.onPaid(ctx, invoice)
}

// ProviderConfigured reports whether a provider has the credentials it needs.
func (s *Service) ProviderConfigured(ctx context.Context, provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "yoomoney":
		return s.yooWallet(ctx) != ""
	case "platega":
		return s.plategaMerchant(ctx) != "" && s.plategaSecret(ctx) != ""
	default:
		return false
	}
}

// SetPaymentWebhook stores the bot's payment callback endpoint.
func (s *Service) SetPaymentWebhook(ctx context.Context, botUserID int64, rawURL, secret string) error {
	if s.hooks == nil {
		return domain.ErrPaymentProviderNotConfigured
	}
	rawURL = strings.TrimSpace(rawURL)
	if !strings.HasPrefix(rawURL, "https://") {
		return domain.ErrPaymentInvalid
	}
	return s.hooks.SetPaymentWebhook(ctx, store.PaymentWebhook{BotUserID: botUserID, URL: rawURL, Secret: strings.TrimSpace(secret)})
}

// DeletePaymentWebhook removes the bot's payment callback endpoint.
func (s *Service) DeletePaymentWebhook(ctx context.Context, botUserID int64) error {
	if s.hooks == nil {
		return domain.ErrPaymentProviderNotConfigured
	}
	return s.hooks.DeletePaymentWebhook(ctx, botUserID)
}

// notifyPayment posts a successful_payment event to the bot's payment webhook.
func (s *Service) notifyPayment(ctx context.Context, invoice domain.PaymentInvoice) {
	if s.hooks == nil || invoice.Status != domain.PaymentStatusPaid {
		return
	}
	hook, found, err := s.hooks.GetPaymentWebhook(ctx, invoice.BotUserID)
	if err != nil || !found || hook.URL == "" {
		return
	}
	body, err := json.Marshal(map[string]any{
		"update_id": 0,
		"successful_payment": map[string]any{
			"invoice_id":                 invoice.ID,
			"amount":                     invoice.Amount,
			"currency":                   invoice.Currency,
			"payload":                    invoice.Payload,
			"provider_payment_charge_id": invoice.ExternalID,
			"total_amount":               invoice.Amount,
		},
	})
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook.URL, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if hook.Secret != "" {
		mac := hmac.New(sha256.New, []byte(hook.Secret))
		mac.Write(body)
		req.Header.Set("X-Payment-Signature", hex.EncodeToString(mac.Sum(nil)))
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		s.log.Warn("payments: payment webhook failed", zap.Int64("bot_user_id", invoice.BotUserID), zap.Error(err))
		return
	}
	_ = resp.Body.Close()
}
