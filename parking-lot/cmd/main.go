package main

import (
	"errors"
	"fmt"
	"time"

	"parking-lot/internal/parkinglot"
)

func main() {
	clock := parkinglot.NewManualClock(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC))
	declineNext := true
	pay := parkinglot.PaymentFunc(func(paymentID string, cents int64) error {
		if declineNext {
			declineNext = false
			return errors.New("card declined")
		}
		fmt.Printf("  charged %s $%d.%02d\n", paymentID, cents/100, cents%100)
		return nil
	})

	lot, err := parkinglot.GetInstance(
		[]parkinglot.FloorConfig{{Small: 1, Medium: 1, Large: 1}, {Medium: 2}},
		parkinglot.WithClock(clock),
		parkinglot.WithPayments(pay),
	)
	if err != nil {
		panic(err)
	}
	same, _ := parkinglot.GetInstance(nil) // anywhere else in the app: same lot, arguments ignored
	fmt.Printf("singleton: same instance = %v\n", same == lot)
	show := func() {
		a := lot.Available()
		for floor := 1; floor <= len(a); floor++ {
			f := a[floor]
			fmt.Printf("  floor %d free: small=%d medium=%d large=%d\n", floor, f[parkinglot.Small], f[parkinglot.Medium], f[parkinglot.Large])
		}
	}
	park := func(v parkinglot.Vehicle) parkinglot.Ticket {
		t, err := lot.Park(v)
		if err != nil {
			fmt.Printf("park %s %s: %v\n", v.Type, v.Plate, err)
			return t
		}
		fmt.Printf("park %s %s → %s (%s)\n", v.Type, v.Plate, t.SpotID, t.ID)
		return t
	}

	park(parkinglot.Vehicle{Plate: "BIKE-1", Type: parkinglot.Motorcycle})
	c1 := park(parkinglot.Vehicle{Plate: "CAR-1", Type: parkinglot.Car})
	park(parkinglot.Vehicle{Plate: "CAR-2", Type: parkinglot.Car}) // floor 2 medium, not floor 1 large
	park(parkinglot.Vehicle{Plate: "TRUCK-1", Type: parkinglot.Truck})
	park(parkinglot.Vehicle{Plate: "CAR-1", Type: parkinglot.Car})     // already parked
	park(parkinglot.Vehicle{Plate: "TRUCK-2", Type: parkinglot.Truck}) // full
	show()

	clock.Advance(2*time.Hour + 50*time.Minute)
	due, _ := lot.AmountDue(c1.ID)
	fmt.Printf("CAR-1 pays at the kiosk after 2h50m, %d cents due\n", due)
	if _, err := lot.Pay(c1.ID); err != nil {
		fmt.Printf("  %v (try again)\n", err)
	}
	lot.Pay(c1.ID)

	clock.Advance(25 * time.Minute)
	fmt.Println("CAR-1 reaches the exit 25m later, past the 15m grace")
	if _, err := lot.Exit(c1.ID); err != nil {
		due, _ := lot.AmountDue(c1.ID)
		fmt.Printf("  %v: %d cents\n", err, due)
		lot.Pay(c1.ID)
	}
	t, _ := lot.Exit(c1.ID)
	fmt.Printf("  ticket %s %s, paid %d cents in total\n", t.ID, t.Status, t.PaidCents())
	for _, p := range t.Payments {
		fmt.Printf("    %s %-9s %d cents\n", p.ID, p.Status, p.AmountCents)
	}
	show()
}
