package store

import (
	"context"

	"telesrv/internal/domain"
)

// PaymentStore persists bot payment invoices.
type PaymentStore interface {
	CreatePaymentInvoice(ctx context.Context, inv domain.PaymentInvoice) (domain.PaymentInvoice, error)
	SetPaymentInvoiceURL(ctx context.Context, id int64, paymentURL string) error
	GetPaymentInvoice(ctx context.Context, id int64) (domain.PaymentInvoice, bool, error)
	MarkPaymentInvoicePaid(ctx context.Context, id int64, externalID string) (bool, error)
	ListPaymentInvoices(ctx context.Context, botUserID int64, limit int) ([]domain.PaymentInvoice, error)
}
