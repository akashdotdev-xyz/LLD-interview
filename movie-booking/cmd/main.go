package main

import (
	"errors"
	"fmt"
	"time"

	"movie-booking/internal/booking"
)

type notifier struct{}

// A third observer, besides the two search indexes: AddShow didn't change.
func (notifier) ShowAdded(s booking.Show, c booking.Cinema) {
	fmt.Printf("  [notify] %s now showing at %s, %s\n", s.Movie.Title, c.Name, s.Start.Format("15:04"))
}

func main() {
	clock := booking.NewManualClock(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	declineNext := true
	pay := booking.PaymentFunc(func(paymentID string, cents int64) error {
		if declineNext {
			declineNext = false
			return errors.New("card declined")
		}
		fmt.Printf("  charged %s $%d.%02d\n", paymentID, cents/100, cents%100)
		return nil
	})
	svc := booking.NewService(booking.WithClock(clock), booking.WithPayments(pay))
	svc.Subscribe(notifier{})

	seats := []booking.Seat{{ID: "A1"}, {ID: "A2"}, {ID: "A3"}, {ID: "B1", Type: booking.Premium}, {ID: "B2", Type: booking.Premium}}
	svc.AddCinema(booking.Cinema{ID: "C1", Name: "PVR Phoenix", City: "Pune", Screens: []booking.Screen{{ID: "1", Seats: seats}, {ID: "2", Seats: seats}}})
	svc.AddCinema(booking.Cinema{ID: "C2", Name: "INOX Amanora", City: "Pune", Screens: []booking.Screen{{ID: "1", Seats: seats}}})

	dune := booking.Movie{ID: "M1", Title: "Dune", Duration: 2*time.Hour + 46*time.Minute}
	prices := map[booking.SeatType]int64{booking.Regular: 1000, booking.Premium: 1500}
	today := func(h int) time.Time { return time.Date(2026, 1, 1, h, 0, 0, 0, time.UTC) }

	fmt.Println("admin adds shows")
	evening, _ := svc.AddShow("C1", "1", dune, today(18), prices)
	svc.AddShow("C1", "2", dune, today(21), prices)
	svc.AddShow("C2", "1", dune, today(19), prices)
	if _, err := svc.AddShow("C1", "1", dune, today(20), prices); err != nil {
		fmt.Printf("  20:00 on C1 screen 1: %v\n", err)
	}

	fmt.Println("cinemas in Pune showing Dune:")
	for _, c := range svc.CinemasShowing("Pune", "M1") {
		fmt.Printf("  %s\n", c.Name)
	}
	fmt.Println("Dune shows at PVR Phoenix:")
	for _, s := range svc.ShowsAt("C1", "M1") {
		fmt.Printf("  %s screen %s at %s\n", s.ID, s.ScreenID, s.Start.Format("15:04"))
	}

	fmt.Println("alice holds A1+B1, bob wants B1 too")
	alice, _ := svc.HoldSeats("alice", evening.ID, []string{"A1", "B1"})
	fmt.Printf("  alice %s %s, %d cents, hold until %s\n", alice.ID, alice.Status, alice.AmountCents, alice.ExpiresAt.Format("15:04"))
	if _, err := svc.HoldSeats("bob", evening.ID, []string{"B1", "B2"}); err != nil {
		fmt.Printf("  bob: %v\n", err)
	}

	fmt.Println("alice pays")
	if _, err := svc.Confirm(alice.ID); err != nil {
		fmt.Printf("  %v (try again)\n", err)
	}
	alice, _ = svc.Confirm(alice.ID)
	fmt.Printf("  alice %s\n", alice.Status)

	fmt.Println("carol holds A2 and walks away")
	carol, _ := svc.HoldSeats("carol", evening.ID, []string{"A2"})
	clock.Advance(6 * time.Minute)
	if _, err := svc.Confirm(carol.ID); err != nil {
		fmt.Printf("  carol, 6 minutes later: %v\n", err)
	}
	free, _ := svc.AvailableSeats(evening.ID)
	fmt.Printf("  free seats: %v\n", ids(free))

	fmt.Println("alice cancels")
	alice, _ = svc.Cancel(alice.ID)
	fmt.Printf("  alice %s, refund %d cents\n", alice.Status, alice.RefundCents)
	free, _ = svc.AvailableSeats(evening.ID)
	fmt.Printf("  free seats: %v\n", ids(free))
}

func ids(seats []booking.Seat) []string {
	var out []string
	for _, s := range seats {
		out = append(out, s.ID)
	}
	return out
}
