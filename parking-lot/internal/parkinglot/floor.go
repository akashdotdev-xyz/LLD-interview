package parkinglot

import (
	"container/heap"
	"fmt"
)

// FloorConfig is how many spots of each size a floor has.
type FloorConfig struct {
	Small, Medium, Large int
}

// Floor keeps one heap of free spots per size. Finding a spot never scans
// the whole floor. Floor is not thread-safe; ParkingLot's lock guards it.
type Floor struct {
	Number int
	free   map[SpotSize]*spotHeap
}

func newFloor(number int, cfg FloorConfig) *Floor {
	f := &Floor{Number: number, free: make(map[SpotSize]*spotHeap)}
	n := 0
	for _, group := range []struct {
		size  SpotSize
		count int
	}{{Small, cfg.Small}, {Medium, cfg.Medium}, {Large, cfg.Large}} {
		h := &spotHeap{}
		for range group.count {
			n++
			heap.Push(h, &ParkingSpot{
				ID:     fmt.Sprintf("F%d-%02d", number, n),
				Floor:  number,
				Number: n,
				Size:   group.size,
			})
		}
		f.free[group.size] = h
	}
	return f
}

// nearestFree returns the nearest free spot of this size without taking it, or nil.
func (f *Floor) nearestFree(size SpotSize) *ParkingSpot {
	h := f.free[size]
	if h.Len() == 0 {
		return nil
	}
	return (*h)[0]
}

func (f *Floor) occupy(s *ParkingSpot) {
	heap.Remove(f.free[s.Size], s.index)
}

func (f *Floor) release(s *ParkingSpot) {
	heap.Push(f.free[s.Size], s)
}

func (f *Floor) freeCount(size SpotSize) int {
	return f.free[size].Len()
}
