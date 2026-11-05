package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// PaymentStore persists payment invoices.
type PaymentStore struct {
	db sqlcgen.DBTX
}

func NewPaymentStore(db sqlcgen.DBTX) *PaymentStore {
	return &PaymentStore{db: db}
}

func (s *PaymentStore) CreatePaymentInvoice(ctx context.Context, inv domain.PaymentInvoice) (domain.PaymentInvoice, error) {
	if inv.BotUserID == 0 || inv.Amount <= 0 || inv.Provider == "" {
		return domain.PaymentInvoice{}, domain.ErrPaymentInvalid
	}
	row := s.db.QueryRow(ctx, `
INSERT INTO payment_invoices (bot_user_id, user_id, provider, currency, amount, title, description, payload, status)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'pending')
RETURNING id, created_at`,
		inv.BotUserID, inv.UserID, inv.Provider, inv.Currency, inv.Amount, inv.Title, inv.Description, inv.Payload)
	if err := row.Scan(&inv.ID, &inv.CreatedAt); err != nil {
		return domain.PaymentInvoice{}, fmt.Errorf("create payment invoice: %w", err)
	}
	inv.Status = domain.PaymentStatusPending
	return inv, nil
}

func (s *PaymentStore) SetPaymentInvoiceURL(ctx context.Context, id int64, paymentURL string) error {
	if _, err := s.db.Exec(ctx, `UPDATE payment_invoices SET payment_url=$2 WHERE id=$1`, id, paymentURL); err != nil {
		return fmt.Errorf("set payment invoice url: %w", err)
	}
	return nil
}

func (s *PaymentStore) GetPaymentInvoice(ctx context.Context, id int64) (domain.PaymentInvoice, bool, error) {
	var inv domain.PaymentInvoice
	var paidAt *time.Time
	err := s.db.QueryRow(ctx, `
SELECT id, bot_user_id, user_id, provider, currency, amount, title, description, payload,
       status, external_id, payment_url, created_at, paid_at
FROM payment_invoices WHERE id=$1`, id).
		Scan(&inv.ID, &inv.BotUserID, &inv.UserID, &inv.Provider, &inv.Currency, &inv.Amount,
			&inv.Title, &inv.Description, &inv.Payload, &inv.Status, &inv.ExternalID,
			&inv.PaymentURL, &inv.CreatedAt, &paidAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.PaymentInvoice{}, false, nil
		}
		return domain.PaymentInvoice{}, false, fmt.Errorf("get payment invoice: %w", err)
	}
	inv.PaidAt = paidAt
	return inv, true, nil
}

func (s *PaymentStore) MarkPaymentInvoicePaid(ctx context.Context, id int64, externalID string) (bool, error) {
	tag, err := s.db.Exec(ctx, `
UPDATE payment_invoices SET status='paid', external_id=$2, paid_at=now()
WHERE id=$1 AND status='pending'`, id, externalID)
	if err != nil {
		return false, fmt.Errorf("mark payment invoice paid: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *PaymentStore) ListPaymentInvoices(ctx context.Context, botUserID int64, limit int) ([]domain.PaymentInvoice, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `
SELECT id, bot_user_id, user_id, provider, currency, amount, title, description, payload,
       status, external_id, payment_url, created_at, paid_at
FROM payment_invoices`
	args := []any{}
	if botUserID != 0 {
		query += ` WHERE bot_user_id=$1`
		args = append(args, botUserID)
	}
	query += fmt.Sprintf(` ORDER BY id DESC LIMIT %d`, limit)
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list payment invoices: %w", err)
	}
	defer rows.Close()
	var out []domain.PaymentInvoice
	for rows.Next() {
		var inv domain.PaymentInvoice
		var paidAt *time.Time
		if err := rows.Scan(&inv.ID, &inv.BotUserID, &inv.UserID, &inv.Provider, &inv.Currency, &inv.Amount,
			&inv.Title, &inv.Description, &inv.Payload, &inv.Status, &inv.ExternalID,
			&inv.PaymentURL, &inv.CreatedAt, &paidAt); err != nil {
			return nil, fmt.Errorf("scan payment invoice: %w", err)
		}
		inv.PaidAt = paidAt
		out = append(out, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list payment invoices: %w", err)
	}
	return out, nil
}
