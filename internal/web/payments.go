package web

import (
	"context"
	"io"
	"net/http"
	"net/url"
)

// PaymentsService settles external provider notifications.
type PaymentsService interface {
	HandleYooMoneyNotification(ctx context.Context, form url.Values) (int64, bool, error)
	HandlePlategaCallback(ctx context.Context, header http.Header, body []byte) (int64, bool, error)
}

// yooMoneyCallback accepts YooMoney HTTP notifications and settles the invoice.
func (h *handler) yooMoneyCallback(w http.ResponseWriter, r *http.Request) {
	if h.payments == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if _, _, err := h.payments.HandleYooMoneyNotification(r.Context(), r.PostForm); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	_, _ = w.Write([]byte("OK"))
}

// plategaCallback settles a Platega webhook.
func (h *handler) plategaCallback(w http.ResponseWriter, r *http.Request) {
	if h.payments == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if _, _, err := h.payments.HandlePlategaCallback(r.Context(), r.Header, body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// plategaReturn is the browser return page after a Platega payment.
func (h *handler) plategaReturn(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte("<!doctype html><meta charset=\"utf-8\"><title>Оплата</title><body style=\"font:16px system-ui;padding:32px\">Спасибо! Вернитесь в чат — платёж обрабатывается.</body>"))
}
