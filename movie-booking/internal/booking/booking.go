package booking

import (
	"slices"
	"time"
)

// BookingStatus:
//
//	PENDING ──Confirm──▶ PAYING ──ok──▶ CONFIRMED ──Cancel──▶ CANCELLED
//	   │  ▲                 │
//	   │  └───charge failed─┘
//	   ├──hold timed out──▶ EXPIRED
//	   └──Cancel──────────▶ CANCELLED
type BookingStatus int

const (
	Pending   BookingStatus = iota // seats held, waiting for payment
	Paying                         // charge in flight; the hold can't expire now
	Confirmed                      // paid, seats are the user's
	Cancelled
	Expired // not paid before ExpiresAt; seats went back on sale
)

func (s BookingStatus) String() string {
	switch s {
	case Pending:
		return "PENDING"
	case Paying:
		return "PAYING"
	case Confirmed:
		return "CONFIRMED"
	case Cancelled:
		return "CANCELLED"
	case Expired:
		return "EXPIRED"
	}
	return "UNKNOWN"
}

// Booking is returned by value. Its fields are guarded by its show's lock.
type Booking struct {
	ID          string
	UserID      string
	ShowID      string
	SeatIDs     []string
	AmountCents int64
	Status      BookingStatus
	ExpiresAt   time.Time // end of the hold while PENDING
	RefundCents int64     // set when a confirmed booking is cancelled

	show     *showState
	attempts int // payment attempts, for a fresh idempotency key each time
}

// holdsSeats reports whether this booking still keeps its seats from
// everyone else. Caller holds show.mu.
func (b *Booking) holdsSeats(now time.Time) bool {
	switch b.Status {
	case Paying, Confirmed:
		return true
	case Pending:
		return now.Before(b.ExpiresAt)
	}
	return false
}

// refresh turns a timed-out hold into EXPIRED. Expiry is lazy: it's noticed
// the next time anyone looks, not by a timer. Caller holds show.mu.
func (b *Booking) refresh(now time.Time) {
	if b.Status == Pending && !now.Before(b.ExpiresAt) {
		b.Status = Expired
	}
}

// clone copies SeatIDs so the caller's copy doesn't share an array with ours.
func (b *Booking) clone() Booking {
	c := *b
	c.SeatIDs = slices.Clone(b.SeatIDs)
	c.show = nil
	return c
}
