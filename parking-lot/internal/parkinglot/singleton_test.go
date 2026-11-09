package parkinglot

import (
	"sync"
	"testing"
)

// This is the only test that uses GetInstance: the instance is global, so
// other tests would see each other's parked cars.
func TestGetInstanceReturnsOneLot(t *testing.T) {
	const callers = 50
	got := make([]*ParkingLot, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() {
			l, err := GetInstance([]FloorConfig{{Medium: 1}})
			if err != nil {
				t.Error(err)
			}
			got[i] = l
		})
	}
	wg.Wait()
	for _, l := range got {
		if l == nil || l != got[0] {
			t.Fatal("every caller must get the same non-nil instance")
		}
	}

	// Later calls ignore their arguments and return the existing lot.
	again, _ := GetInstance([]FloorConfig{{Medium: 100}})
	if again != got[0] {
		t.Fatal("second GetInstance returned a different lot")
	}
	if free := again.Available()[1][Medium]; free != 1 {
		t.Fatalf("want the original 1-spot lot, got %d free", free)
	}
}
