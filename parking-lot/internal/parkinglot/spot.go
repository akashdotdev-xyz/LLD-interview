package parkinglot

type ParkingSpot struct {
	ID     string
	Floor  int
	Number int // lower number = closer to the entrance
	Size   SpotSize

	index int // position in its floor's free heap, -1 while occupied
}

// spotHeap is a min-heap of free spots ordered by Number, so the nearest free
// spot is always at the top: O(1) to look at, O(log n) to take or give back.
type spotHeap []*ParkingSpot

func (h spotHeap) Len() int           { return len(h) }
func (h spotHeap) Less(i, j int) bool { return h[i].Number < h[j].Number }
func (h spotHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *spotHeap) Push(x any) {
	s := x.(*ParkingSpot)
	s.index = len(*h)
	*h = append(*h, s)
}

func (h *spotHeap) Pop() any {
	old := *h
	n := len(old)
	s := old[n-1]
	old[n-1] = nil
	s.index = -1
	*h = old[:n-1]
	return s
}
