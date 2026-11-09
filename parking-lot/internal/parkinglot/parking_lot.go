package parkinglot

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrNoFloors          = errors.New("parking lot needs at least one floor")
	ErrInvalidVehicle    = errors.New("vehicle must have a plate")
	ErrLotFull           = errors.New("no free spot fits this vehicle")
	ErrAlreadyParked     = errors.New("vehicle is already parked")
	ErrTicketNotFound    = errors.New("ticket not found")
	ErrPaymentInProgress = errors.New("a payment for this ticket is already in progress")
	ErrNothingDue        = errors.New("nothing to pay")
	ErrPaymentDue        = errors.New("payment due before exit")
)

// defaultExitGrace is how long a driver has to leave after paying before
// more time is charged.
const defaultExitGrace = 15 * time.Minute

// ParkingLot is the public API used by the gates and pay kiosks.
//
//	entry gate ──Park()──▶ strategy.Find ──▶ floor.occupy ──▶ Ticket
//	kiosk/exit ──Pay()───▶ pricing.Fee ──▶ payments.Charge ──▶ Payment
//	exit gate ──Exit()───▶ nothing due? ──▶ floor.release
//
// One mutex guards all floors and tickets. Every operation under it is a map
// lookup or an O(log n) heap change, so it's held for microseconds.
type ParkingLot struct {
	mu            sync.Mutex
	floors        []*Floor
	tickets       map[string]*parked // tickets of vehicles still in the lot
	byPlate       map[string]string  // plate → ticket ID, stops the same vehicle parking twice
	nextTicketID  int
	nextPaymentID int

	strategy  AllocationStrategy
	pricing   PricingStrategy
	payments  PaymentProcessor
	clock     Clock
	exitGrace time.Duration
}

type parked struct {
	ticket Ticket
	spot   *ParkingSpot
}

type Option func(*ParkingLot)

func WithStrategy(s AllocationStrategy) Option { return func(l *ParkingLot) { l.strategy = s } }
func WithPricing(p PricingStrategy) Option     { return func(l *ParkingLot) { l.pricing = p } }
func WithPayments(p PaymentProcessor) Option   { return func(l *ParkingLot) { l.payments = p } }
func WithClock(c Clock) Option                 { return func(l *ParkingLot) { l.clock = c } }
func WithExitGrace(d time.Duration) Option     { return func(l *ParkingLot) { l.exitGrace = d } }

var (
	instance     *ParkingLot
	instanceErr  error
	instanceOnce sync.Once
)

// GetInstance returns the one parking lot for this process. sync.Once makes
// it safe when many goroutines call it at the same time: exactly one creates
// the lot, and the rest wait for it and get the same pointer. The arguments
// are only used by the first call; later calls get the existing lot.
func GetInstance(floors []FloorConfig, opts ...Option) (*ParkingLot, error) {
	instanceOnce.Do(func() {
		instance, instanceErr = NewParkingLot(floors, opts...)
	})
	return instance, instanceErr
}

// NewParkingLot builds a separate lot. The app should use GetInstance;
// NewParkingLot exists so tests can each start from an empty lot instead of
// sharing global state.
func NewParkingLot(floors []FloorConfig, opts ...Option) (*ParkingLot, error) {
	if len(floors) == 0 {
		return nil, ErrNoFloors
	}
	l := &ParkingLot{
		tickets:   make(map[string]*parked),
		byPlate:   make(map[string]string),
		strategy:  BestFit{},
		pricing:   DefaultPricing(),
		payments:  PaymentFunc(func(string, int64) error { return nil }),
		clock:     realClock{},
		exitGrace: defaultExitGrace,
	}
	for i, cfg := range floors {
		l.floors = append(l.floors, newFloor(i+1, cfg))
	}
	for _, opt := range opts {
		opt(l)
	}
	return l, nil
}

// Park finds a spot and takes it in one step under the lock. If finding and
// taking were separate, two gates could both find the same free spot.
func (l *ParkingLot) Park(v Vehicle) (Ticket, error) {
	if v.Plate == "" {
		return Ticket{}, ErrInvalidVehicle
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if _, ok := l.byPlate[v.Plate]; ok {
		return Ticket{}, ErrAlreadyParked
	}
	spot := l.strategy.Find(l.floors, v.Type)
	if spot == nil {
		return Ticket{}, ErrLotFull
	}
	l.floors[spot.Floor-1].occupy(spot)

	l.nextTicketID++
	t := Ticket{
		ID:        fmt.Sprintf("T-%d", l.nextTicketID),
		Vehicle:   v,
		SpotID:    spot.ID,
		EntryTime: l.clock.Now(),
		Status:    Active,
	}
	l.tickets[t.ID] = &parked{ticket: t, spot: spot}
	l.byPlate[v.Plate] = t.ID
	return t, nil
}

// AmountDue is what the kiosk or exit gate shows before taking payment.
func (l *ParkingLot) AmountDue(ticketID string) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	p, ok := l.tickets[ticketID]
	if !ok {
		return 0, ErrTicketNotFound
	}
	return l.amountDue(p.ticket, l.clock.Now()), nil
}

// Pay charges whatever is due now. It runs in three steps so the payment
// call happens without the lock:
//  1. under the lock: work out the amount and add a PENDING payment. While it
//     is pending, another Pay or an Exit on this ticket fails with
//     ErrPaymentInProgress, so the driver can't be charged twice.
//  2. without the lock: charge. Other gates keep working meanwhile.
//  3. under the lock: mark the payment SUCCEEDED or FAILED.
func (l *ParkingLot) Pay(ticketID string) (Payment, error) {
	l.mu.Lock()
	p, err := l.activeTicket(ticketID)
	if err != nil {
		l.mu.Unlock()
		return Payment{}, err
	}
	now := l.clock.Now()
	due := l.amountDue(p.ticket, now)
	if due == 0 {
		l.mu.Unlock()
		return Payment{}, ErrNothingDue
	}
	l.nextPaymentID++
	pay := Payment{
		ID:          fmt.Sprintf("P-%d", l.nextPaymentID),
		TicketID:    ticketID,
		AmountCents: due,
		Status:      PaymentPending,
		CreatedAt:   now,
	}
	p.ticket.Payments = append(p.ticket.Payments, pay)
	i := len(p.ticket.Payments) - 1 // stays valid: nothing else is appended while this one is pending
	l.mu.Unlock()

	err = l.payments.Charge(pay.ID, pay.AmountCents)

	l.mu.Lock()
	defer l.mu.Unlock()
	if err != nil {
		pay.Status = PaymentFailed
	} else {
		pay.Status = PaymentSucceeded
	}
	p.ticket.Payments[i] = pay
	if err != nil {
		return pay, fmt.Errorf("payment failed: %w", err)
	}
	return pay, nil
}

// Exit opens the barrier only if nothing is owed, then frees the spot.
func (l *ParkingLot) Exit(ticketID string) (Ticket, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	p, err := l.activeTicket(ticketID)
	if err != nil {
		return Ticket{}, err
	}
	now := l.clock.Now()
	if l.amountDue(p.ticket, now) > 0 {
		return Ticket{}, ErrPaymentDue
	}
	p.ticket.Status = Exited
	p.ticket.ExitTime = now
	l.floors[p.spot.Floor-1].release(p.spot)
	delete(l.tickets, ticketID)
	delete(l.byPlate, p.ticket.Vehicle.Plate)
	return p.ticket.clone(), nil
}

// Available returns free spots per floor number and size, for the display boards.
func (l *ParkingLot) Available() map[int]map[SpotSize]int {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[int]map[SpotSize]int, len(l.floors))
	for _, f := range l.floors {
		out[f.Number] = map[SpotSize]int{
			Small:  f.freeCount(Small),
			Medium: f.freeCount(Medium),
			Large:  f.freeCount(Large),
		}
	}
	return out
}

// activeTicket returns a ticket that has no payment in progress. Caller holds mu.
func (l *ParkingLot) activeTicket(ticketID string) (*parked, error) {
	p, ok := l.tickets[ticketID]
	if !ok {
		return nil, ErrTicketNotFound
	}
	for _, pay := range p.ticket.Payments {
		if pay.Status == PaymentPending {
			return nil, ErrPaymentInProgress
		}
	}
	return p, nil
}

// amountDue is the fee up to now minus what's already been paid. Leaving
// within the grace period after a payment adds no time, so a driver who pays
// at a kiosk isn't charged for the walk to the car. Caller holds mu.
func (l *ParkingLot) amountDue(t Ticket, now time.Time) int64 {
	billUntil := now
	for _, pay := range t.Payments {
		if pay.Status == PaymentSucceeded && now.Sub(pay.CreatedAt) <= l.exitGrace {
			billUntil = pay.CreatedAt
		}
	}
	due := l.pricing.Fee(t.Vehicle.Type, t.EntryTime, billUntil) - t.PaidCents()
	return max(due, 0)
}
