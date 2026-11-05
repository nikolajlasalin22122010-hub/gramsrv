package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// PaymentWebhookStore persists per-bot payment webhooks.
type PaymentWebhookStore struct {
	db sqlcgen.DBTX
}

func NewPaymentWebhookStore(db sqlcgen.DBTX) *PaymentWebhookStore {
	return &PaymentWebhookStore{db: db}
}

func (s *PaymentWebhookStore) GetPaymentWebhook(ctx context.Context, botUserID int64) (store.PaymentWebhook, bool, error) {
	var hook store.PaymentWebhook
	err := s.db.QueryRow(ctx, `SELECT bot_user_id, url, secret FROM bot_payment_webhooks WHERE bot_user_id=$1`, botUserID).
		Scan(&hook.BotUserID, &hook.URL, &hook.Secret)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.PaymentWebhook{}, false, nil
		}
		return store.PaymentWebhook{}, false, fmt.Errorf("get payment webhook: %w", err)
	}
	return hook, true, nil
}

func (s *PaymentWebhookStore) SetPaymentWebhook(ctx context.Context, hook store.PaymentWebhook) error {
	if hook.BotUserID == 0 || hook.URL == "" {
		return errors.New("invalid payment webhook")
	}
	_, err := s.db.Exec(ctx, `
INSERT INTO bot_payment_webhooks (bot_user_id, url, secret, updated_at) VALUES ($1,$2,$3,now())
ON CONFLICT (bot_user_id) DO UPDATE SET url=EXCLUDED.url, secret=EXCLUDED.secret, updated_at=now()`,
		hook.BotUserID, hook.URL, hook.Secret)
	if err != nil {
		return fmt.Errorf("set payment webhook: %w", err)
	}
	return nil
}

func (s *PaymentWebhookStore) DeletePaymentWebhook(ctx context.Context, botUserID int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM bot_payment_webhooks WHERE bot_user_id=$1`, botUserID)
	if err != nil {
		return fmt.Errorf("delete payment webhook: %w", err)
	}
	return nil
}
