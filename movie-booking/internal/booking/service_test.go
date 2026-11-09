package booking

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var (
	t0        = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	inception = Movie{ID: "M1", Title: "Inception", Duration: 2*time.Hour + 28*time.Minute}
	prices    = map[SeatType]int64{Regular: 1000, Premium: 1500}
)

// screen returns a screen with row A regular and row B premium.
func screen(id string, perRow int) Screen {
	sc := Screen{ID: id}
	for _, row := range []struct {
		name string
		typ  SeatType
	}{{"A", Regular}, {"B", Premium}} {
		for n := 1; n <= perRow; n++ {
			sc.Seats = append(sc.Seats, Seat{ID: row.name + string(rune('0'+n)), Type: row.typ})
		}
	}
	return sc
}

// setup builds a service with one cinema, one screen and one show at 6pm.
func setup(t *testing.T, opts ...Option) (*Service, *ManualClock, Show) {
	t.Helper()
	clock := NewManualClock(t0)
	s := NewService(append([]Option{WithClock(clock)}, opts...)...)
	if err := s.AddCinema(Cinema{ID: "C1", Name: "PVR", City: "Pune", Screens: []Screen{screen("1", 5)}}); err != nil {
		t.Fatal(err)
	}
	show, err := s.AddShow("C1", "1", inception, t0.Add(6*time.Hour), prices)
	if err != nil {
		t.Fatal(err)
	}
	return s, clock, show
}

func TestHoldConfirmCharges(t *testing.T) {
	var charged int64
	s, _, show := setup(t, WithPayments(PaymentFunc(func(_ string, c int64) error {
		charged += c
		return nil
	})))

	b, err := s.HoldSeats("u1", show.ID, []string{"A1", "B1"})
	if err != nil {
		t.Fatal(err)
	}
	if b.AmountCents != 2500 {
		t.Fatalf("want 2500 cents for one regular and one premium, got %d", b.AmountCents)
	}
	b, err = s.Confirm(b.ID)
	if err != nil || b.Status != Confirmed {
		t.Fatalf("confirm: %v %v", b.Status, err)
	}
	if charged != 2500 {
		t.Fatalf("charged %d", charged)
	}
	free, _ := s.AvailableSeats(show.ID)
	if len(free) != 8 {
		t.Fatalf("want 8 free seats, got %d", len(free))
	}
}

func TestHoldIsAllOrNothing(t *testing.T) {
	s, _, show := setup(t)
	s.HoldSeats("u1", show.ID, []string{"A2"})

	if _, err := s.HoldSeats("u2", show.ID, []string{"A1", "A2", "A3"}); !errors.Is(err, ErrSeatUnavailable) {
		t.Fatalf("want ErrSeatUnavailable, got %v", err)
	}
	// A1 and A3 must not have been held by the failed request.
	if _, err := s.HoldSeats("u3", show.ID, []string{"A1", "A3"}); err != nil {
		t.Fatalf("A1 and A3 should still be free: %v", err)
	}
}

func TestConcurrentHoldsOneWinner(t *testing.T) {
	s, _, show := setup(t)
	const users = 100
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range users {
		wg.Go(func() {
			if _, err := s.HoldSeats("u", show.ID, []string{"A1", "A2"}); err == nil {
				wins.Add(1)
			}
		})
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("want exactly 1 winner, got %d", wins.Load())
	}
}

func TestExpiredHoldFreesSeats(t *testing.T) {
	s, clock, show := setup(t)
	b, _ := s.HoldSeats("u1", show.ID, []string{"A1"})

	clock.Advance(defaultHoldTTL)
	if _, err := s.HoldSeats("u2", show.ID, []string{"A1"}); err != nil {
		t.Fatalf("A1 should be free after the hold expired: %v", err)
	}
	if _, err := s.Confirm(b.ID); !errors.Is(err, ErrHoldExpired) {
		t.Fatalf("want ErrHoldExpired, got %v", err)
	}
	if got, _ := s.GetBooking(b.ID); got.Status != Expired {
		t.Fatalf("want EXPIRED, got %v", got.Status)
	}
}

func TestFailedPaymentCanBeRetried(t *testing.T) {
	var ids []string
	fail := true
	s, _, show := setup(t, WithPayments(PaymentFunc(func(id string, _ int64) error {
		ids = append(ids, id)
		if fail {
			fail = false
			return errors.New("card declined")
		}
		return nil
	})))
	b, _ := s.HoldSeats("u1", show.ID, []string{"A1"})

	got, err := s.Confirm(b.ID)
	if !errors.Is(err, ErrPaymentFailed) || got.Status != Pending {
		t.Fatalf("want PENDING + ErrPaymentFailed, got %v %v", got.Status, err)
	}
	if _, err := s.HoldSeats("u2", show.ID, []string{"A1"}); !errors.Is(err, ErrSeatUnavailable) {
		t.Fatal("a failed payment must not release the hold")
	}
	if got, err := s.Confirm(b.ID); err != nil || got.Status != Confirmed {
		t.Fatalf("retry: %v %v", got.Status, err)
	}
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("each attempt needs its own idempotency key, got %v", ids)
	}
}

// A hold that runs out while the payment is in flight must not lose its
// seats: the user has paid.
func TestHoldCannotExpireDuringPayment(t *testing.T) {
	var s *Service
	var clock *ManualClock
	var show Show
	stolen := make(chan error, 1)
	s, clock, show = setup(t, WithPayments(PaymentFunc(func(string, int64) error {
		clock.Advance(time.Hour)
		_, err := s.HoldSeats("u2", show.ID, []string{"A1"})
		stolen <- err
		return nil
	})))
	b, _ := s.HoldSeats("u1", show.ID, []string{"A1"})

	if got, err := s.Confirm(b.ID); err != nil || got.Status != Confirmed {
		t.Fatalf("confirm: %v %v", got.Status, err)
	}
	if err := <-stolen; !errors.Is(err, ErrSeatUnavailable) {
		t.Fatalf("u2 took a seat that was being paid for: %v", err)
	}
}

func TestConcurrentConfirmChargesOnce(t *testing.T) {
	var charges atomic.Int32
	release := make(chan struct{})
	s, _, show := setup(t, WithPayments(PaymentFunc(func(string, int64) error {
		charges.Add(1)
		<-release
		return nil
	})))
	b, _ := s.HoldSeats("u1", show.ID, []string{"A1"})

	done := make(chan error)
	go func() { _, err := s.Confirm(b.ID); done <- err }()
	for charges.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	if _, err := s.Confirm(b.ID); !errors.Is(err, ErrPaymentInProgress) {
		t.Fatalf("want ErrPaymentInProgress, got %v", err)
	}
	if _, err := s.Cancel(b.ID); !errors.Is(err, ErrPaymentInProgress) {
		t.Fatalf("want ErrPaymentInProgress on cancel, got %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Confirm(b.ID); err != nil {
		t.Fatalf("confirming again should be a no-op: %v", err)
	}
	if charges.Load() != 1 {
		t.Fatalf("charged %d times", charges.Load())
	}
}

// The payment call is made without the show's lock, so others can still book.
func TestSlowPaymentDoesNotBlockSameShow(t *testing.T) {
	release := make(chan struct{})
	s, _, show := setup(t, WithPayments(PaymentFunc(func(string, int64) error {
		<-release
		return nil
	})))
	defer close(release)
	b, _ := s.HoldSeats("u1", show.ID, []string{"A1"})
	go s.Confirm(b.ID)

	done := make(chan error)
	go func() { _, err := s.HoldSeats("u2", show.ID, []string{"A2"}); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("HoldSeats waited for another user's payment")
	}
}

func TestCancel(t *testing.T) {
	s, clock, show := setup(t)
	held, _ := s.HoldSeats("u1", show.ID, []string{"A1"})
	if c, err := s.Cancel(held.ID); err != nil || c.RefundCents != 0 {
		t.Fatalf("cancel a hold: refund %d, %v", c.RefundCents, err)
	}

	paid, _ := s.HoldSeats("u2", show.ID, []string{"A1"})
	s.Confirm(paid.ID)
	c, err := s.Cancel(paid.ID)
	if err != nil || c.Status != Cancelled || c.RefundCents != 1000 {
		t.Fatalf("cancel a confirmed booking: %v refund %d, %v", c.Status, c.RefundCents, err)
	}
	if _, err := s.Cancel(paid.ID); !errors.Is(err, ErrBookingClosed) {
		t.Fatalf("want ErrBookingClosed, got %v", err)
	}

	late, _ := s.HoldSeats("u3", show.ID, []string{"A1"})
	s.Confirm(late.ID)
	clock.Advance(6 * time.Hour)
	if _, err := s.Cancel(late.ID); !errors.Is(err, ErrShowStarted) {
		t.Fatalf("want ErrShowStarted, got %v", err)
	}
}

func TestHoldValidation(t *testing.T) {
	s, clock, show := setup(t)
	cases := map[string]struct {
		user, show string
		seats      []string
		want       error
	}{
		"no seats":     {"u", show.ID, nil, ErrInvalidRequest},
		"duplicate":    {"u", show.ID, []string{"A1", "A1"}, ErrInvalidRequest},
		"no user":      {"", show.ID, []string{"A1"}, ErrInvalidRequest},
		"unknown seat": {"u", show.ID, []string{"Z9"}, ErrSeatNotFound},
		"unknown show": {"u", "S-404", []string{"A1"}, ErrShowNotFound},
	}
	for name, c := range cases {
		if _, err := s.HoldSeats(c.user, c.show, c.seats); !errors.Is(err, c.want) {
			t.Errorf("%s: want %v, got %v", name, c.want, err)
		}
	}
	clock.Advance(6 * time.Hour)
	if _, err := s.HoldSeats("u", show.ID, []string{"A1"}); !errors.Is(err, ErrShowStarted) {
		t.Errorf("want ErrShowStarted, got %v", err)
	}
}

func TestAddShowRejectsOverlapAndMissingPrice(t *testing.T) {
	s, _, show := setup(t)
	if _, err := s.AddShow("C1", "1", inception, show.End.Add(-time.Minute), prices); !errors.Is(err, ErrScreenBusy) {
		t.Fatalf("want ErrScreenBusy, got %v", err)
	}
	if _, err := s.AddShow("C1", "1", inception, show.End, prices); err != nil {
		t.Fatalf("a show starting as the last one ends is fine: %v", err)
	}
	if _, err := s.AddShow("C1", "1", inception, show.End.Add(5*time.Hour), map[SeatType]int64{Regular: 1}); !errors.Is(err, ErrMissingPrice) {
		t.Fatalf("want ErrMissingPrice, got %v", err)
	}
}

type recorder struct{ got []string }

func (r *recorder) ShowAdded(show Show, _ Cinema) { r.got = append(r.got, show.ID) }

func TestSearchUpdatesWhenShowAdded(t *testing.T) {
	s, clock, show := setup(t)
	rec := &recorder{}
	s.Subscribe(rec)
	s.AddCinema(Cinema{ID: "C2", Name: "INOX", City: "Pune", Screens: []Screen{screen("1", 2)}})
	s.AddCinema(Cinema{ID: "C3", Name: "Cinepolis", City: "Mumbai", Screens: []Screen{screen("1", 2)}})

	if got := s.CinemasShowing("Pune", "M1"); len(got) != 1 {
		t.Fatalf("before: want 1 cinema, got %d", len(got))
	}
	s.AddShow("C2", "1", inception, t0.Add(time.Hour), prices)
	s.AddShow("C3", "1", inception, t0.Add(time.Hour), prices)
	later, _ := s.AddShow("C1", "1", inception, t0.Add(10*time.Hour), prices)

	got := s.CinemasShowing("Pune", "M1")
	if len(got) != 2 || got[0].Name != "INOX" || got[1].Name != "PVR" {
		t.Fatalf("want INOX and PVR, got %v", got)
	}
	shows := s.ShowsAt("C1", "M1")
	if len(shows) != 2 || shows[0].ID != show.ID || shows[1].ID != later.ID {
		t.Fatalf("want both C1 shows in start order, got %v", shows)
	}
	if len(rec.got) != 3 {
		t.Fatalf("extra observer saw %d shows, want 3", len(rec.got))
	}

	clock.Advance(7 * time.Hour) // the 6pm show has started
	if shows := s.ShowsAt("C1", "M1"); len(shows) != 1 || shows[0].ID != later.ID {
		t.Fatalf("started shows should be hidden, got %v", shows)
	}
}
