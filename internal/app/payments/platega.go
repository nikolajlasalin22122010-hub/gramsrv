package payments

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"telesrv/internal/domain"
)

// Platega provider: POST {base}/v2/transaction/process with X-MerchantId and
// X-Secret headers (contract taken from the autodonate project).

const defaultPlategaAPIBase = "https://app.platega.io"

func (s *Service) plategaMerchant(ctx context.Context) string {
	if s.settings != nil {
		if value, err := s.settings.GetSetting(ctx, keyPlategaMerchant); err == nil && value != "" {
			return value
		}
	}
	return s.cfg.PlategaMerchant
}

func (s *Service) plategaSecret(ctx context.Context) string {
	if s.settings != nil {
		if value, err := s.settings.GetSetting(ctx, keyPlategaSecret); err == nil && value != "" {
			return value
		}
	}
	return s.cfg.PlategaSecret
}

func (s *Service) plategaBase() string {
	if base := strings.TrimRight(strings.TrimSpace(s.cfg.PlategaAPIBase), "/"); base != "" {
		return base
	}
	return defaultPlategaAPIBase
}

// CreatePlategaInvoice creates a Platega payment and stores the invoice.
func (s *Service) CreatePlategaInvoice(ctx context.Context, botUserID, userID, amount int64, title, description, payload string) (domain.PaymentInvoice, error) {
	title = strings.TrimSpace(title)
	merchant := s.plategaMerchant(ctx)
	secret := s.plategaSecret(ctx)
	if merchant == "" || secret == "" {
		return domain.PaymentInvoice{}, domain.ErrPaymentProviderNotConfigured
	}
	if amount <= 0 || title == "" {
		return domain.PaymentInvoice{}, domain.ErrPaymentInvalid
	}
	invoice, err := s.store.CreatePaymentInvoice(ctx, domain.PaymentInvoice{
		BotUserID:   botUserID,
		UserID:      userID,
		Provider:    "platega",
		Currency:    "RUB",
		Amount:      amount,
		Title:       title,
		Description: strings.TrimSpace(description),
		Payload:     payload,
	})
	if err != nil {
		return domain.PaymentInvoice{}, err
	}

	returnURL := strings.TrimRight(s.cfg.PublicBaseURL, "/") + "/payments/platega/return"
	body, err := json.Marshal(map[string]any{
		"paymentDetails": map[string]any{
			"amount":   float64(amount) / 100,
			"currency": "RUB",
		},
		"description": title,
		"return":      returnURL,
		"failedUrl":   returnURL,
		"payload":     strconv.FormatInt(invoice.ID, 10),
	})
	if err != nil {
		return domain.PaymentInvoice{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.plategaBase()+"/v2/transaction/process", bytes.NewReader(body))
	if err != nil {
		return domain.PaymentInvoice{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-MerchantId", merchant)
	req.Header.Set("X-Secret", secret)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return domain.PaymentInvoice{}, fmt.Errorf("platega create: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return domain.PaymentInvoice{}, fmt.Errorf("platega create: status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var created struct {
		URL           string `json:"url"`
		PaymentURL    string `json:"paymentUrl"`
		TransactionID string `json:"transactionId"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		return domain.PaymentInvoice{}, fmt.Errorf("platega create: bad response: %w", err)
	}
	paymentURL := created.URL
	if paymentURL == "" {
		paymentURL = created.PaymentURL
	}
	if paymentURL == "" {
		return domain.PaymentInvoice{}, errors.New("platega create: no payment url in response")
	}
	invoice.PaymentURL = paymentURL
	if err := s.store.SetPaymentInvoiceURL(ctx, invoice.ID, paymentURL); err != nil {
		return domain.PaymentInvoice{}, err
	}
	return invoice, nil
}

// HandlePlategaCallback settles a Platega webhook. Platega sends the merchant
// credentials in headers; the invoice id travels in "payload".
func (s *Service) HandlePlategaCallback(ctx context.Context, header http.Header, body []byte) (int64, bool, error) {
	merchant := s.plategaMerchant(ctx)
	secret := s.plategaSecret(ctx)
	if merchant == "" || secret == "" {
		return 0, false, domain.ErrPaymentProviderNotConfigured
	}
	if header.Get("X-MerchantId") != merchant || header.Get("X-Secret") != secret {
		return 0, false, domain.ErrPaymentInvalid
	}
	var payload struct {
		Payload       string `json:"payload"`
		Status        string `json:"status"`
		TransactionID string `json:"transactionId"`
		ID            string `json:"id"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, false, domain.ErrPaymentInvalid
	}
	status := strings.ToLower(strings.TrimSpace(payload.Status))
	switch status {
	case "success", "paid", "completed", "confirmed", "successed":
	default:
		return 0, false, nil
	}
	id, err := strconv.ParseInt(strings.TrimSpace(payload.Payload), 10, 64)
	if err != nil || id <= 0 {
		return 0, false, domain.ErrPaymentInvalid
	}
	externalID := payload.TransactionID
	if externalID == "" {
		externalID = payload.ID
	}
	paid, err := s.store.MarkPaymentInvoicePaid(ctx, id, externalID)
	if err == nil && paid {
		if invoice, found, getErr := s.store.GetPaymentInvoice(ctx, id); getErr == nil && found {
			s.notifyPayment(ctx, invoice)
			s.firePaid(ctx, invoice)
		}
	}
	return id, paid, err
}
