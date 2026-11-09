package booking

import (
	"slices"
	"strings"
	"sync"
	"time"
)

// ShowObserver is told about every new show. The search indexes are
// observers, so AddShow doesn't know or care which indexes exist, and a new
// one (say, "notify users who follow this movie") is added without touching it.
type ShowObserver interface {
	ShowAdded(show Show, cinema Cinema)
}

// CityIndex answers "which cinemas in this city are showing this movie?"
// without scanning every show.
type CityIndex struct {
	mu      sync.RWMutex
	cinemas map[string]map[string]map[string]Cinema // city → movie ID → cinema ID → cinema
}

func NewCityIndex() *CityIndex {
	return &CityIndex{cinemas: make(map[string]map[string]map[string]Cinema)}
}

func (x *CityIndex) ShowAdded(show Show, c Cinema) {
	x.mu.Lock()
	defer x.mu.Unlock()
	byMovie := x.cinemas[c.City]
	if byMovie == nil {
		byMovie = make(map[string]map[string]Cinema)
		x.cinemas[c.City] = byMovie
	}
	if byMovie[show.Movie.ID] == nil {
		byMovie[show.Movie.ID] = make(map[string]Cinema)
	}
	byMovie[show.Movie.ID][c.ID] = c
}

// Cinemas returns the cinemas sorted by name.
func (x *CityIndex) Cinemas(city, movieID string) []Cinema {
	x.mu.RLock()
	defer x.mu.RUnlock()
	var out []Cinema
	for _, c := range x.cinemas[city][movieID] {
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b Cinema) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// ShowIndex answers "which shows of this movie are on at this cinema?".
type ShowIndex struct {
	mu    sync.RWMutex
	shows map[string]map[string][]Show // cinema ID → movie ID → shows sorted by start
}

func NewShowIndex() *ShowIndex {
	return &ShowIndex{shows: make(map[string]map[string][]Show)}
}

func (x *ShowIndex) ShowAdded(show Show, c Cinema) {
	x.mu.Lock()
	defer x.mu.Unlock()
	byMovie := x.shows[c.ID]
	if byMovie == nil {
		byMovie = make(map[string][]Show)
		x.shows[c.ID] = byMovie
	}
	list := byMovie[show.Movie.ID]
	i, _ := slices.BinarySearchFunc(list, show.Start, func(s Show, t time.Time) int { return s.Start.Compare(t) })
	byMovie[show.Movie.ID] = slices.Insert(list, i, show)
}

// Shows returns the shows starting after the given time, earliest first.
func (x *ShowIndex) Shows(cinemaID, movieID string, after time.Time) []Show {
	x.mu.RLock()
	defer x.mu.RUnlock()
	list := x.shows[cinemaID][movieID]
	i, _ := slices.BinarySearchFunc(list, after, func(s Show, t time.Time) int {
		if s.Start.After(t) {
			return 1
		}
		return -1
	})
	return slices.Clone(list[i:])
}
