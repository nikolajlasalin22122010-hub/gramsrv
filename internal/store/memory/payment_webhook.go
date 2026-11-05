package memory

import (
	"context"
	"sync"

	"telesrv/internal/store"
)

// PaymentWebhookStore is the in-memory payment webhook store used by tests.
type PaymentWebhookStore struct {
	mu    sync.Mutex
	hooks map[int64]store.PaymentWebhook
}

func NewPaymentWebhookStore() *PaymentWebhookStore {
	return &PaymentWebhookStore{hooks: make(map[int64]store.PaymentWebhook)}
}

func (s *PaymentWebhookStore) GetPaymentWebhook(_ context.Context, botUserID int64) (store.PaymentWebhook, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hook, ok := s.hooks[botUserID]
	return hook, ok, nil
}

func (s *PaymentWebhookStore) SetPaymentWebhook(_ context.Context, hook store.PaymentWebhook) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hooks[hook.BotUserID] = hook
	return nil
}

func (s *PaymentWebhookStore) DeletePaymentWebhook(_ context.Context, botUserID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.hooks, botUserID)
	return nil
}
