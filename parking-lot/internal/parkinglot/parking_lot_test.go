package parkinglot

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newLot uses NewParkingLot rather than GetInstance so every test starts
// with an empty lot of its own.
func newLot(t *testing.T, floors []FloorConfig, opts ...Option) *ParkingLot {
	t.Helper()
	l, err := NewParkingLot(floors, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// leave pays what's due and drives out, like a driver at the exit gate.
func leave(t *testing.T, l *ParkingLot, ticketID string) Ticket {
	t.Helper()
	if _, err := l.Pay(ticketID); err != nil && !errors.Is(err, ErrNothingDue) {
		t.Fatalf("pay: %v", err)
	}
	tk, err := l.Exit(ticketID)
	if err != nil {
		t.Fatalf("exit: %v", err)
	}
	return tk
}

func car(plate string) Vehicle   { return Vehicle{Plate: plate, Type: Car} }
func bike(plate string) Vehicle  { return Vehicle{Plate: plate, Type: Motorcycle} }
func truck(plate string) Vehicle { return Vehicle{Plate: plate, Type: Truck} }

func TestVehicleTakesSmallestFittingSpot(t *testing.T) {
	l := newLot(t, []FloorConfig{{Small: 1, Medium: 1, Large: 1}})

	b, _ := l.Park(bike("B1"))
	c, _ := l.Park(car("C1"))
	tr, _ := l.Park(truck("T1"))

	// Spots are numbered small, medium, large.
	for got, want := range map[string]string{b.SpotID: "F1-01", c.SpotID: "F1-02", tr.SpotID: "F1-03"} {
		if got != want {
			t.Errorf("got spot %s, want %s", got, want)
		}
	}
}

func TestCarUsesLargeSpotOnlyWhenNoMediumLeft(t *testing.T) {
	l := newLot(t, []FloorConfig{{Medium: 1, Large: 1}})

	l.Park(car("C1"))
	c2, err := l.Park(car("C2"))
	if err != nil || c2.SpotID != "F1-02" {
		t.Fatalf("want C2 in the large spot, got %q %v", c2.SpotID, err)
	}
	if _, err := l.Park(truck("T1")); !errors.Is(err, ErrLotFull) {
		t.Fatalf("want ErrLotFull for the truck, got %v", err)
	}
}

func TestTruckNeverGoesInMediumSpot(t *testing.T) {
	l := newLot(t, []FloorConfig{{Small: 5, Medium: 5}})
	if _, err := l.Park(truck("T1")); !errors.Is(err, ErrLotFull) {
		t.Fatalf("want ErrLotFull, got %v", err)
	}
}

func TestStrategiesDiffer(t *testing.T) {
	floors := []FloorConfig{{Large: 1}, {Medium: 1}}

	best := newLot(t, floors)
	tk, _ := best.Park(car("C1"))
	if tk.SpotID != "F2-01" {
		t.Errorf("BestFit: want F2-01 (medium, keeps large free), got %s", tk.SpotID)
	}

	nearest := newLot(t, floors, WithStrategy(NearestFirst{}))
	tk, _ = nearest.Park(car("C1"))
	if tk.SpotID != "F1-01" {
		t.Errorf("NearestFirst: want F1-01 (lowest floor), got %s", tk.SpotID)
	}
}

func TestFreedSpotIsReused(t *testing.T) {
	l := newLot(t, []FloorConfig{{Medium: 3}})
	t1, _ := l.Park(car("C1"))
	l.Park(car("C2"))
	leave(t, l, t1.ID)

	t3, _ := l.Park(car("C3"))
	if t3.SpotID != t1.SpotID {
		t.Fatalf("want nearest freed spot %s, got %s", t1.SpotID, t3.SpotID)
	}
}

func TestSameVehicleCannotParkTwice(t *testing.T) {
	l := newLot(t, []FloorConfig{{Medium: 2}})
	tk, _ := l.Park(car("C1"))
	if _, err := l.Park(car("C1")); !errors.Is(err, ErrAlreadyParked) {
		t.Fatalf("want ErrAlreadyParked, got %v", err)
	}
	leave(t, l, tk.ID)
	if _, err := l.Park(car("C1")); err != nil {
		t.Fatalf("should park again after leaving, got %v", err)
	}
}

func TestFeeRoundsUpToStartedHour(t *testing.T) {
	clock := NewManualClock(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC))
	l := newLot(t, []FloorConfig{{Medium: 1}}, WithClock(clock))

	cases := []struct {
		stay time.Duration
		want int64
	}{
		{0, 300},         // minimum one hour
		{time.Hour, 300}, // exactly one hour
		{time.Hour + time.Minute, 600},
		{150 * time.Minute, 900},
	}
	for _, c := range cases {
		tk, _ := l.Park(car("C1"))
		clock.Advance(c.stay)
		if due, _ := l.AmountDue(tk.ID); due != c.want {
			t.Errorf("stay %v: want %d cents due, got %d", c.stay, c.want, due)
		}
		if got := leave(t, l, tk.ID); got.PaidCents() != c.want || got.Status != Exited {
			t.Errorf("stay %v: want %d paid and EXITED, got %d %v", c.stay, c.want, got.PaidCents(), got.Status)
		}
	}
}

func TestCannotExitWithoutPaying(t *testing.T) {
	l := newLot(t, []FloorConfig{{Medium: 1}})
	tk, _ := l.Park(car("C1"))
	if _, err := l.Exit(tk.ID); !errors.Is(err, ErrPaymentDue) {
		t.Fatalf("want ErrPaymentDue, got %v", err)
	}
	if got := l.Available()[1][Medium]; got != 0 {
		t.Fatalf("spot must stay occupied, %d free", got)
	}
}

func TestPaidVehicleIsStillParkedUntilExit(t *testing.T) {
	l := newLot(t, []FloorConfig{{Medium: 1}})
	tk, _ := l.Park(car("C1"))
	if _, err := l.Pay(tk.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Pay(tk.ID); !errors.Is(err, ErrNothingDue) {
		t.Fatalf("second pay: want ErrNothingDue, got %v", err)
	}
	if got := l.Available()[1][Medium]; got != 0 {
		t.Fatalf("paying must not free the spot, %d free", got)
	}
	if _, err := l.Exit(tk.ID); err != nil {
		t.Fatal(err)
	}
	if got := l.Available()[1][Medium]; got != 1 {
		t.Fatalf("exit must free the spot, %d free", got)
	}
}

func TestExitGracePeriod(t *testing.T) {
	clock := NewManualClock(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC))
	l := newLot(t, []FloorConfig{{Medium: 2}}, WithClock(clock), WithExitGrace(15*time.Minute))

	// Pays at 2h55 for 3 hours, leaves at 3h05: within grace, nothing more to pay.
	a, _ := l.Park(car("A"))
	clock.Advance(2*time.Hour + 55*time.Minute)
	l.Pay(a.ID)
	clock.Advance(10 * time.Minute)
	if _, err := l.Exit(a.ID); err != nil {
		t.Fatalf("within grace: want exit, got %v", err)
	}

	// Pays at 2h55, leaves at 3h15: grace is over, the 4th hour is owed.
	b, _ := l.Park(car("B"))
	clock.Advance(2*time.Hour + 55*time.Minute)
	l.Pay(b.ID)
	clock.Advance(20 * time.Minute)
	if _, err := l.Exit(b.ID); !errors.Is(err, ErrPaymentDue) {
		t.Fatalf("after grace: want ErrPaymentDue, got %v", err)
	}
	if due, _ := l.AmountDue(b.ID); due != 300 {
		t.Fatalf("want 300 cents for the extra hour, got %d", due)
	}
	got := leave(t, l, b.ID)
	if len(got.Payments) != 2 || got.PaidCents() != 1200 {
		t.Fatalf("want 2 payments totalling 1200, got %d totalling %d", len(got.Payments), got.PaidCents())
	}
}

func TestUnknownAndRepeatedTicket(t *testing.T) {
	l := newLot(t, []FloorConfig{{Medium: 1}})
	if _, err := l.Pay("nope"); !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("want ErrTicketNotFound, got %v", err)
	}
	tk, _ := l.Park(car("C1"))
	leave(t, l, tk.ID)
	if _, err := l.Exit(tk.ID); !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("second exit: want ErrTicketNotFound, got %v", err)
	}
}

func TestFailedPaymentIsKeptInHistory(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	pay := PaymentFunc(func(string, int64) error {
		if fail.Load() {
			return errors.New("card declined")
		}
		return nil
	})
	l := newLot(t, []FloorConfig{{Medium: 1}}, WithPayments(pay))

	tk, _ := l.Park(car("C1"))
	p, err := l.Pay(tk.ID)
	if err == nil || p.Status != PaymentFailed {
		t.Fatalf("want FAILED payment and an error, got %v %v", p.Status, err)
	}
	if _, err := l.Exit(tk.ID); !errors.Is(err, ErrPaymentDue) {
		t.Fatalf("want ErrPaymentDue after failed payment, got %v", err)
	}

	fail.Store(false)
	got := leave(t, l, tk.ID)
	if len(got.Payments) != 2 || got.Payments[0].Status != PaymentFailed || got.Payments[1].Status != PaymentSucceeded {
		t.Fatalf("want [FAILED SUCCEEDED], got %+v", got.Payments)
	}
	if got.Payments[0].ID == got.Payments[1].ID {
		t.Fatal("each attempt needs its own ID (idempotency key)")
	}
}

// Many gates race for few spots: exactly as many succeed as there are spots,
// and no spot is handed out twice. Run with -race.
func TestConcurrentParkNeverDoubleBooks(t *testing.T) {
	const spots, cars = 10, 100
	l := newLot(t, []FloorConfig{{Medium: spots / 2}, {Medium: spots / 2}})

	var mu sync.Mutex
	taken := map[string]bool{}
	var full atomic.Int32
	var wg sync.WaitGroup
	for i := range cars {
		wg.Go(func() {
			tk, err := l.Park(car(fmt.Sprintf("CAR-%d", i)))
			if errors.Is(err, ErrLotFull) {
				full.Add(1)
				return
			}
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if taken[tk.SpotID] {
				t.Errorf("spot %s given out twice", tk.SpotID)
			}
			taken[tk.SpotID] = true
		})
	}
	wg.Wait()
	if len(taken) != spots || full.Load() != cars-spots {
		t.Fatalf("want %d parked and %d full, got %d and %d", spots, cars-spots, len(taken), full.Load())
	}
}

// Two kiosks scan the same ticket while payment is slow: only one charge
// happens, and the exit gate waits for it.
func TestConcurrentPayChargesOnce(t *testing.T) {
	var charges atomic.Int32
	release := make(chan struct{})
	pay := PaymentFunc(func(string, int64) error {
		charges.Add(1)
		<-release
		return nil
	})
	l := newLot(t, []FloorConfig{{Medium: 1}}, WithPayments(pay))
	tk, _ := l.Park(car("C1"))

	first := make(chan error)
	go func() { _, err := l.Pay(tk.ID); first <- err }()
	waitFor(t, func() bool { return charges.Load() == 1 })

	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if _, err := l.Pay(tk.ID); !errors.Is(err, ErrPaymentInProgress) {
				t.Errorf("want ErrPaymentInProgress, got %v", err)
			}
		})
	}
	wg.Wait()
	if _, err := l.Exit(tk.ID); !errors.Is(err, ErrPaymentInProgress) {
		t.Fatalf("exit during payment: want ErrPaymentInProgress, got %v", err)
	}

	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if charges.Load() != 1 {
		t.Fatalf("want 1 charge, got %d", charges.Load())
	}
}

func TestSlowPaymentDoesNotBlockEntry(t *testing.T) {
	release := make(chan struct{})
	pay := PaymentFunc(func(string, int64) error { <-release; return nil })
	l := newLot(t, []FloorConfig{{Medium: 2}}, WithPayments(pay))

	tk, _ := l.Park(car("C1"))
	done := make(chan struct{})
	go func() { l.Pay(tk.ID); close(done) }()

	parked := make(chan error)
	go func() { _, err := l.Park(car("C2")); parked <- err }()
	select {
	case err := <-parked:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Park blocked while a payment was in progress")
	}
	close(release)
	<-done
}

func TestNoFloors(t *testing.T) {
	if _, err := NewParkingLot(nil); !errors.Is(err, ErrNoFloors) {
		t.Fatalf("want ErrNoFloors, got %v", err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
