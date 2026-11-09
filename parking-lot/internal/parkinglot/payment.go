package parkinglot

import "time"

// PaymentStatus: PENDING → SUCCEEDED or FAILED. A failed payment stays in the
// ticket's history; trying again creates a new Payment.
type PaymentStatus int

const (
	PaymentPending PaymentStatus = iota
	PaymentSucceeded
	PaymentFailed
)

func (s PaymentStatus) String() string {
	switch s {
	case PaymentPending:
		return "PENDING"
	case PaymentSucceeded:
		return "SUCCEEDED"
	case PaymentFailed:
		return "FAILED"
	}
	return "UNKNOWN"
}

type Payment struct {
	ID          string
	TicketID    string
	AmountCents int64
	Status      PaymentStatus
	CreatedAt   time.Time // the fee was worked out up to this time
}

// PaymentProcessor charges the driver. In a real system it's a network call
// that can be slow or fail, so ParkingLot never calls it while holding its
// lock. paymentID is unique per attempt, so the provider can use it as an
// idempotency key and won't charge twice if a request is retried.
type PaymentProcessor interface {
	Charge(paymentID string, amountCents int64) error
}

// PaymentFunc lets a plain function be used as a PaymentProcessor.
type PaymentFunc func(paymentID string, amountCents int64) error

func (f PaymentFunc) Charge(paymentID string, amountCents int64) error {
	return f(paymentID, amountCents)
}
