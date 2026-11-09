package booking

import (
	"sync"
	"time"
)

// Show is one screening of a movie on one screen. This is the catalog part,
// returned by value from searches. Seat state lives in showState.
type Show struct {
	ID       string
	Movie    Movie
	CinemaID string
	ScreenID string
	Start    time.Time
	End      time.Time
	Prices   map[SeatType]int64 // cents per seat; read-only after AddShow
}

// showState is a show plus who holds each seat. Each show has its own lock:
// people booking different shows never wait for each other.
type showState struct {
	Show
	layout []Seat              // seats in screen order, for listing
	seats  map[string]Seat     // seat ID → seat
	mu     sync.Mutex          // guards holders and the Booking fields of this show's bookings
	holder map[string]*Booking // seat ID → booking holding it; free if missing or that booking no longer holds it
}

func newShowState(show Show, screen Screen) *showState {
	s := &showState{
		Show:   show,
		layout: screen.Seats,
		seats:  make(map[string]Seat, len(screen.Seats)),
		holder: make(map[string]*Booking),
	}
	for _, seat := range screen.Seats {
		s.seats[seat.ID] = seat
	}
	return s
}

// seatFree checks the holder's status at this moment, so an expired hold
// frees its seats without any background job. Caller holds mu.
func (s *showState) seatFree(seatID string, now time.Time) bool {
	b := s.holder[seatID]
	return b == nil || !b.holdsSeats(now)
}
