package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Payment settings and invoices (YooMoney / Platega).
//
// Settings live in app_settings as key/value rows; the payments service in the
// main server reads them at request time, so saving here takes effect without a
// restart.

const (
	keyPayYooWallet       = "payments.yoomoney.wallet"
	keyPayYooSecret       = "payments.yoomoney.secret"
	keyPayPlategaMerchant = "payments.platega.merchant"
	keyPayPlategaSecret   = "payments.platega.secret"
)

func (s *server) paymentSetting(r *http.Request, key string) string {
	if s.read == nil || s.read.pool == nil {
		return ""
	}
	var value string
	if err := s.read.pool.QueryRow(r.Context(), `SELECT value FROM app_settings WHERE key=$1`, key).Scan(&value); err != nil {
		return ""
	}
	return value
}

func (s *server) setPaymentSetting(r *http.Request, key, value string) error {
	_, err := s.read.pool.Exec(r.Context(), `
INSERT INTO app_settings (key, value, updated_at) VALUES ($1,$2,now())
ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`, key, value)
	return err
}

func (s *server) handlePaymentSettingsAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil || s.read.pool == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"wallet":             s.paymentSetting(r, keyPayYooWallet),
		"secret_set":         s.paymentSetting(r, keyPayYooSecret) != "",
		"platega_merchant":   s.paymentSetting(r, keyPayPlategaMerchant),
		"platega_secret_set": s.paymentSetting(r, keyPayPlategaSecret) != "",
	})
}

func (s *server) handleSetPaymentSettingsAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil || s.read.pool == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	var body struct {
		Wallet          string `json:"wallet"`
		Secret          string `json:"secret"`
		PlategaMerchant string `json:"platega_merchant"`
		PlategaSecret   string `json:"platega_secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := s.setPaymentSetting(r, keyPayYooWallet, strings.TrimSpace(body.Wallet)); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if secret := strings.TrimSpace(body.Secret); secret != "" {
		if err := s.setPaymentSetting(r, keyPayYooSecret, secret); err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if err := s.setPaymentSetting(r, keyPayPlategaMerchant, strings.TrimSpace(body.PlategaMerchant)); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if secret := strings.TrimSpace(body.PlategaSecret); secret != "" {
		if err := s.setPaymentSetting(r, keyPayPlategaSecret, secret); err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *server) handlePaymentInvoicesAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil || s.read.pool == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	rows, err := s.read.pool.Query(r.Context(), `
SELECT id, bot_user_id, amount, currency, status, title, created_at, paid_at
FROM payment_invoices ORDER BY id DESC LIMIT 100`)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0, 32)
	for rows.Next() {
		var (
			id, botID, amount int64
			currency, status  string
			title             string
			createdAt         any
			paidAt            any
		)
		if err := rows.Scan(&id, &botID, &amount, &currency, &status, &title, &createdAt, &paidAt); err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		items = append(items, map[string]any{
			"id":         id,
			"bot_id":     botID,
			"amount":     amount,
			"currency":   currency,
			"status":     status,
			"title":      title,
			"created_at": createdAt,
			"paid_at":    paidAt,
		})
	}
	if err := rows.Err(); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"invoices": items})
}
