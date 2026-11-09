package booking

// PaymentProcessor charges the user. In a real system it's a network call
// that can be slow or fail, so the service never calls it while holding a
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
