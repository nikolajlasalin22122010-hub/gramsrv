package botapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"telesrv/internal/domain"
)

// PaymentsService creates YooMoney/Platega invoices for bots.
type PaymentsService interface {
	CreateYooMoneyInvoice(ctx context.Context, botUserID, userID, amount int64, title, description, payload string) (domain.PaymentInvoice, error)
	CreatePlategaInvoice(ctx context.Context, botUserID, userID, amount int64, title, description, payload string) (domain.PaymentInvoice, error)
	GetInvoice(ctx context.Context, id int64) (domain.PaymentInvoice, bool, error)
}

// createInvoiceLink creates an external payment link (YooMoney or Platega).
// Result: {"url": "...", "invoice_id": N}.
func (h *handler) createInvoiceLink(w http.ResponseWriter, r *http.Request, botID int64) {
	if h.payments == nil {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	title := strings.TrimSpace(values["title"])
	if title == "" {
		writeAPIError(w, http.StatusBadRequest, "TITLE_INVALID")
		return
	}
	currency := strings.ToUpper(strings.TrimSpace(values["currency"]))
	if currency == "" {
		currency = "RUB"
	}
	if currency != "RUB" {
		writeAPIError(w, http.StatusBadRequest, "CURRENCY_INVALID")
		return
	}
	var prices []struct {
		Label  string `json:"label"`
		Amount int64  `json:"amount"`
	}
	if err := json.Unmarshal([]byte(values["prices"]), &prices); err != nil || len(prices) == 0 {
		writeAPIError(w, http.StatusBadRequest, "PRICE_INVALID")
		return
	}
	var total int64
	for _, price := range prices {
		if price.Amount <= 0 {
			writeAPIError(w, http.StatusBadRequest, "PRICE_INVALID")
			return
		}
		total += price.Amount
	}
	var invoice domain.PaymentInvoice
	if strings.EqualFold(strings.TrimSpace(values["provider"]), "platega") {
		invoice, err = h.payments.CreatePlategaInvoice(r.Context(), botID, 0, total, title, values["description"], values["payload"])
	} else {
		invoice, err = h.payments.CreateYooMoneyInvoice(r.Context(), botID, 0, total, title, values["description"], values["payload"])
	}
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, map[string]any{"url": invoice.PaymentURL, "invoice_id": invoice.ID})
}

// getPaymentStatus reports whether an invoice was paid.
func (h *handler) getPaymentStatus(w http.ResponseWriter, r *http.Request, botID int64) {
	if h.payments == nil {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(values["invoice_id"]), 10, 64)
	if err != nil || id <= 0 {
		writeAPIError(w, http.StatusBadRequest, "INVOICE_ID_INVALID")
		return
	}
	invoice, found, err := h.payments.GetInvoice(r.Context(), id)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL")
		return
	}
	if !found || invoice.BotUserID != botID {
		writeAPIError(w, http.StatusNotFound, "INVOICE_NOT_FOUND")
		return
	}
	writeAPIOK(w, map[string]any{
		"invoice_id": invoice.ID,
		"status":     string(invoice.Status),
		"paid":       invoice.Status == domain.PaymentStatusPaid,
		"amount":     invoice.Amount,
		"currency":   invoice.Currency,
		"payload":    invoice.Payload,
	})
}
