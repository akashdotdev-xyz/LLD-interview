package parkinglot

import (
	"slices"
	"time"
)

// TicketStatus is about the vehicle: ACTIVE while it's in the lot, EXITED once
// it has left. Money is tracked separately in Payment, because a ticket can
// have several payment attempts and "paid" doesn't mean "left".
type TicketStatus int

const (
	Active TicketStatus = iota
	Exited
)

func (s TicketStatus) String() string {
	switch s {
	case Active:
		return "ACTIVE"
	case Exited:
		return "EXITED"
	}
	return "UNKNOWN"
}

// Ticket is returned by value, so callers can't change the lot's copy
// without holding its lock.
type Ticket struct {
	ID        string
	Vehicle   Vehicle
	SpotID    string
	EntryTime time.Time
	ExitTime  time.Time // set on exit
	Status    TicketStatus
	Payments  []Payment // every attempt, oldest first
}

// PaidCents is the total of the successful payments.
func (t Ticket) PaidCents() int64 {
	var paid int64
	for _, p := range t.Payments {
		if p.Status == PaymentSucceeded {
			paid += p.AmountCents
		}
	}
	return paid
}

// clone copies the Payments slice too. Without it the caller's copy would
// share an array with the lot's copy, and the lock wouldn't protect it.
func (t Ticket) clone() Ticket {
	t.Payments = slices.Clone(t.Payments)
	return t
}
