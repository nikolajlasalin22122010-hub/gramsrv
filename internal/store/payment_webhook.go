package store

import (
	"context"
)

// PaymentWebhook is a per-bot endpoint that receives successful_payment events.
type PaymentWebhook struct {
	BotUserID int64
	URL       string
	Secret    string
}

// PaymentWebhookStore keeps per-bot payment webhook settings.
type PaymentWebhookStore interface {
	GetPaymentWebhook(ctx context.Context, botUserID int64) (PaymentWebhook, bool, error)
	SetPaymentWebhook(ctx context.Context, hook PaymentWebhook) error
	DeletePaymentWebhook(ctx context.Context, botUserID int64) error
}
