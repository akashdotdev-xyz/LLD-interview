package booking

import "time"

type SeatType int

const (
	Regular SeatType = iota
	Premium
	Recliner
)

func (t SeatType) String() string {
	switch t {
	case Regular:
		return "REGULAR"
	case Premium:
		return "PREMIUM"
	case Recliner:
		return "RECLINER"
	}
	return "UNKNOWN"
}

// Seat is part of a screen's fixed layout. It has no status: the same seat is
// free for the 6pm show and booked for the 9pm one, so status lives on the show.
type Seat struct {
	ID   string // "A1"
	Type SeatType
}

// Screen is one auditorium inside a cinema.
type Screen struct {
	ID    string
	Seats []Seat
}

// Cinema (cinema hall) has one or more screens. Catalog data like this is
// never changed after it's added, so it can be shared without a lock.
type Cinema struct {
	ID      string
	Name    string
	City    string
	Screens []Screen
}

func (c Cinema) screen(id string) (Screen, bool) {
	for _, s := range c.Screens {
		if s.ID == id {
			return s, true
		}
	}
	return Screen{}, false
}

type Movie struct {
	ID       string
	Title    string
	Duration time.Duration
}
