package domain

import "time"

// PaymentStatus is the lifecycle of a payment invoice.
type PaymentStatus string

const (
	PaymentStatusPending PaymentStatus = "pending"
	PaymentStatusPaid    PaymentStatus = "paid"
	PaymentStatusFailed  PaymentStatus = "failed"
)

// PaymentInvoice is one bill created by a bot for a user. The provider turns it
// into an external payment (YooMoney quickpay, Platega, Stars).
type PaymentInvoice struct {
	ID          int64
	BotUserID   int64
	UserID      int64
	Provider    string
	Currency    string
	Amount      int64 // minor units: kopecks for RUB, stars for XTR
	Title       string
	Description string
	Payload     string
	Status      PaymentStatus
	ExternalID  string
	PaymentURL  string
	CreatedAt   time.Time
	PaidAt      *time.Time
}
