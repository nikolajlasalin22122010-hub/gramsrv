package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"telesrv/internal/domain"
)

// PaymentStore is the in-memory payment invoice store used by tests.
type PaymentStore struct {
	mu     sync.Mutex
	nextID int64
	items  map[int64]domain.PaymentInvoice
}

func NewPaymentStore() *PaymentStore {
	return &PaymentStore{items: make(map[int64]domain.PaymentInvoice)}
}

func (s *PaymentStore) CreatePaymentInvoice(_ context.Context, inv domain.PaymentInvoice) (domain.PaymentInvoice, error) {
	if inv.BotUserID == 0 || inv.Amount <= 0 || inv.Provider == "" {
		return domain.PaymentInvoice{}, domain.ErrPaymentInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	inv.ID = s.nextID
	inv.Status = domain.PaymentStatusPending
	inv.CreatedAt = time.Now()
	s.items[inv.ID] = inv
	return inv, nil
}

func (s *PaymentStore) SetPaymentInvoiceURL(_ context.Context, id int64, paymentURL string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv, ok := s.items[id]
	if !ok {
		return domain.ErrPaymentInvalid
	}
	inv.PaymentURL = paymentURL
	s.items[id] = inv
	return nil
}

func (s *PaymentStore) GetPaymentInvoice(_ context.Context, id int64) (domain.PaymentInvoice, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv, ok := s.items[id]
	return inv, ok, nil
}

func (s *PaymentStore) MarkPaymentInvoicePaid(_ context.Context, id int64, externalID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv, ok := s.items[id]
	if !ok || inv.Status != domain.PaymentStatusPending {
		return false, nil
	}
	now := time.Now()
	inv.Status = domain.PaymentStatusPaid
	inv.ExternalID = externalID
	inv.PaidAt = &now
	s.items[id] = inv
	return true, nil
}

func (s *PaymentStore) ListPaymentInvoices(_ context.Context, botUserID int64, limit int) ([]domain.PaymentInvoice, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.PaymentInvoice, 0, len(s.items))
	for _, inv := range s.items {
		if botUserID != 0 && inv.BotUserID != botUserID {
			continue
		}
		out = append(out, inv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
