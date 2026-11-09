package booking

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrInvalidCinema     = errors.New("cinema needs an ID, a city and at least one screen with seats")
	ErrCinemaExists      = errors.New("cinema already exists")
	ErrCinemaNotFound    = errors.New("cinema not found")
	ErrScreenNotFound    = errors.New("screen not found")
	ErrInvalidShow       = errors.New("show needs a movie with an ID and a duration")
	ErrMissingPrice      = errors.New("no price for a seat type on this screen")
	ErrScreenBusy        = errors.New("screen already has a show at that time")
	ErrShowNotFound      = errors.New("show not found")
	ErrShowStarted       = errors.New("show has already started")
	ErrInvalidRequest    = errors.New("need a user and 1 to 10 different seats")
	ErrSeatNotFound      = errors.New("seat not found")
	ErrSeatUnavailable   = errors.New("seat is held or booked")
	ErrBookingNotFound   = errors.New("booking not found")
	ErrHoldExpired       = errors.New("seat hold expired")
	ErrBookingClosed     = errors.New("booking is cancelled or expired")
	ErrPaymentInProgress = errors.New("a payment for this booking is already in progress")
	ErrPaymentFailed     = errors.New("payment failed")
)

const (
	defaultHoldTTL     = 5 * time.Minute
	maxSeatsPerBooking = 10
)

// Service is the public API used by the apps and the admin panel.
//
//	admin ──AddCinema / AddShow──▶ catalog ──ShowAdded──▶ CityIndex, ShowIndex
//	user  ──search──▶ indexes
//	user  ──HoldSeats──▶ show lock: all seats free? hold them for 5 min
//	user  ──Confirm──▶ payments.Charge (no lock held) ──▶ CONFIRMED
//
// Two kinds of lock:
//   - mu guards the catalog maps. Catalog data never changes once added.
//   - each show has its own lock for its seats and bookings.
//
// Lock order: a show's lock may be held while taking mu, never the other way.
type Service struct {
	mu        sync.RWMutex
	cinemas   map[string]Cinema
	shows     map[string]*showState
	byScreen  map[string][]*showState // cinema ID/screen ID → its shows, for the overlap check
	bookings  map[string]*Booking
	observers []ShowObserver
	nextID    atomic.Int64

	cities   *CityIndex
	showIdx  *ShowIndex
	payments PaymentProcessor
	clock    Clock
	holdTTL  time.Duration
}

type Option func(*Service)

func WithPayments(p PaymentProcessor) Option { return func(s *Service) { s.payments = p } }
func WithClock(c Clock) Option               { return func(s *Service) { s.clock = c } }
func WithHoldTTL(d time.Duration) Option     { return func(s *Service) { s.holdTTL = d } }

func NewService(opts ...Option) *Service {
	s := &Service{
		cinemas:  make(map[string]Cinema),
		shows:    make(map[string]*showState),
		byScreen: make(map[string][]*showState),
		bookings: make(map[string]*Booking),
		cities:   NewCityIndex(),
		showIdx:  NewShowIndex(),
		payments: PaymentFunc(func(string, int64) error { return nil }),
		clock:    realClock{},
		holdTTL:  defaultHoldTTL,
	}
	for _, opt := range opts {
		opt(s)
	}
	s.Subscribe(s.cities)
	s.Subscribe(s.showIdx)
	return s
}

// Subscribe registers an observer for new shows.
func (s *Service) Subscribe(o ShowObserver) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observers = append(s.observers, o)
}

func (s *Service) AddCinema(c Cinema) error {
	if c.ID == "" || c.City == "" || len(c.Screens) == 0 {
		return ErrInvalidCinema
	}
	// Copy the screens so the caller can't change our layout afterwards.
	screens := make([]Screen, len(c.Screens))
	for i, sc := range c.Screens {
		if len(sc.Seats) == 0 {
			return ErrInvalidCinema
		}
		screens[i] = Screen{ID: sc.ID, Seats: slices.Clone(sc.Seats)}
	}
	c.Screens = screens

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.cinemas[c.ID]; ok {
		return ErrCinemaExists
	}
	s.cinemas[c.ID] = c
	return nil
}

// AddShow schedules a movie on a screen, then tells the observers so the
// searches see it.
func (s *Service) AddShow(cinemaID, screenID string, movie Movie, start time.Time, prices map[SeatType]int64) (Show, error) {
	if movie.ID == "" || movie.Duration <= 0 {
		return Show{}, ErrInvalidShow
	}
	s.mu.Lock()
	cinema, ok := s.cinemas[cinemaID]
	if !ok {
		s.mu.Unlock()
		return Show{}, ErrCinemaNotFound
	}
	screen, ok := cinema.screen(screenID)
	if !ok {
		s.mu.Unlock()
		return Show{}, ErrScreenNotFound
	}
	for _, seat := range screen.Seats {
		if _, ok := prices[seat.Type]; !ok {
			s.mu.Unlock()
			return Show{}, ErrMissingPrice
		}
	}
	end := start.Add(movie.Duration)
	key := cinemaID + "/" + screenID
	for _, other := range s.byScreen[key] {
		if start.Before(other.End) && other.Start.Before(end) {
			s.mu.Unlock()
			return Show{}, ErrScreenBusy
		}
	}
	show := Show{
		ID:       s.newID("S"),
		Movie:    movie,
		CinemaID: cinemaID,
		ScreenID: screenID,
		Start:    start,
		End:      end,
		Prices:   maps.Clone(prices),
	}
	st := newShowState(show, screen)
	s.shows[show.ID] = st
	s.byScreen[key] = append(s.byScreen[key], st)
	observers := slices.Clone(s.observers)
	s.mu.Unlock()

	// Notify without mu, so a slow observer doesn't block bookings.
	for _, o := range observers {
		o.ShowAdded(show, cinema)
	}
	return show, nil
}

// CinemasShowing lists the cinemas in a city that are showing a movie.
func (s *Service) CinemasShowing(city, movieID string) []Cinema {
	return s.cities.Cinemas(city, movieID)
}

// ShowsAt lists the upcoming shows of a movie at a cinema.
func (s *Service) ShowsAt(cinemaID, movieID string) []Show {
	return s.showIdx.Shows(cinemaID, movieID, s.clock.Now())
}

// AvailableSeats is the seat map the user picks from.
func (s *Service) AvailableSeats(showID string) ([]Seat, error) {
	show, err := s.show(showID)
	if err != nil {
		return nil, err
	}
	show.mu.Lock()
	defer show.mu.Unlock()
	now := s.clock.Now()
	var free []Seat
	for _, seat := range show.layout {
		if show.seatFree(seat.ID, now) {
			free = append(free, seat)
		}
	}
	return free, nil
}

// HoldSeats holds all the seats or none of them, for holdTTL. Checking and
// taking happen under the show's lock, so two users can't both get seat A1.
func (s *Service) HoldSeats(userID, showID string, seatIDs []string) (Booking, error) {
	if userID == "" || len(seatIDs) == 0 || len(seatIDs) > maxSeatsPerBooking {
		return Booking{}, ErrInvalidRequest
	}
	show, err := s.show(showID)
	if err != nil {
		return Booking{}, err
	}
	show.mu.Lock()
	defer show.mu.Unlock()

	now := s.clock.Now()
	if !now.Before(show.Start) {
		return Booking{}, ErrShowStarted
	}
	var amount int64
	seen := make(map[string]bool, len(seatIDs))
	for _, id := range seatIDs {
		seat, ok := show.seats[id]
		if !ok {
			return Booking{}, ErrSeatNotFound
		}
		if seen[id] {
			return Booking{}, ErrInvalidRequest
		}
		seen[id] = true
		if !show.seatFree(id, now) {
			return Booking{}, ErrSeatUnavailable
		}
		amount += show.Prices[seat.Type]
	}

	b := &Booking{
		ID:          s.newID("B"),
		UserID:      userID,
		ShowID:      showID,
		SeatIDs:     slices.Clone(seatIDs),
		AmountCents: amount,
		Status:      Pending,
		ExpiresAt:   now.Add(s.holdTTL),
		show:        show,
	}
	for _, id := range seatIDs {
		if old := show.holder[id]; old != nil {
			old.refresh(now) // the hold we're replacing had expired
		}
		show.holder[id] = b
	}
	s.mu.Lock()
	s.bookings[b.ID] = b
	s.mu.Unlock()
	return b.clone(), nil
}

// Confirm pays for a held booking. It runs in three steps so the payment
// call happens without the lock:
//  1. under the lock: check the hold is still valid and mark it PAYING. A
//     PAYING booking keeps its seats even past ExpiresAt, and a second
//     Confirm gets ErrPaymentInProgress, so the user can't be charged twice.
//  2. without the lock: charge. Others keep booking this show meanwhile.
//  3. under the lock: CONFIRMED, or back to PENDING so the user can retry
//     until the hold runs out.
func (s *Service) Confirm(bookingID string) (Booking, error) {
	b, err := s.booking(bookingID)
	if err != nil {
		return Booking{}, err
	}
	show := b.show
	show.mu.Lock()
	b.refresh(s.clock.Now())
	switch b.Status {
	case Confirmed:
		c := b.clone()
		show.mu.Unlock()
		return c, nil // already paid; safe to call again
	case Paying:
		show.mu.Unlock()
		return Booking{}, ErrPaymentInProgress
	case Expired:
		show.mu.Unlock()
		return Booking{}, ErrHoldExpired
	case Cancelled:
		show.mu.Unlock()
		return Booking{}, ErrBookingClosed
	}
	b.Status = Paying
	b.attempts++
	paymentID := fmt.Sprintf("%s-%d", b.ID, b.attempts)
	amount := b.AmountCents
	show.mu.Unlock()

	err = s.payments.Charge(paymentID, amount)

	show.mu.Lock()
	defer show.mu.Unlock()
	if err != nil {
		b.Status = Pending
		b.refresh(s.clock.Now())
		return b.clone(), fmt.Errorf("%w: %v", ErrPaymentFailed, err)
	}
	b.Status = Confirmed
	return b.clone(), nil
}

// Cancel releases the seats. A confirmed booking can be cancelled until the
// show starts and is refunded in full.
func (s *Service) Cancel(bookingID string) (Booking, error) {
	b, err := s.booking(bookingID)
	if err != nil {
		return Booking{}, err
	}
	show := b.show
	show.mu.Lock()
	defer show.mu.Unlock()
	now := s.clock.Now()
	b.refresh(now)
	switch b.Status {
	case Paying:
		return Booking{}, ErrPaymentInProgress
	case Cancelled, Expired:
		return Booking{}, ErrBookingClosed
	case Confirmed:
		if !now.Before(show.Start) {
			return Booking{}, ErrShowStarted
		}
		b.RefundCents = b.AmountCents
	}
	b.Status = Cancelled
	for _, id := range b.SeatIDs {
		if show.holder[id] == b {
			delete(show.holder, id)
		}
	}
	return b.clone(), nil
}

// GetBooking returns the booking as it is now, with expiry applied.
func (s *Service) GetBooking(bookingID string) (Booking, error) {
	b, err := s.booking(bookingID)
	if err != nil {
		return Booking{}, err
	}
	b.show.mu.Lock()
	defer b.show.mu.Unlock()
	b.refresh(s.clock.Now())
	return b.clone(), nil
}

func (s *Service) show(id string) (*showState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	show, ok := s.shows[id]
	if !ok {
		return nil, ErrShowNotFound
	}
	return show, nil
}

func (s *Service) booking(id string) (*Booking, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.bookings[id]
	if !ok {
		return nil, ErrBookingNotFound
	}
	return b, nil
}

func (s *Service) newID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, s.nextID.Add(1))
}
