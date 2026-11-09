package parkinglot

// AllocationStrategy picks a free spot for a vehicle, or returns nil if none fits.
// It only looks: ParkingLot takes the spot while still holding its lock, so
// two gates can never be handed the same spot.
type AllocationStrategy interface {
	Find(floors []*Floor, v VehicleType) *ParkingSpot
}

// BestFit tries the smallest fitting size on every floor before moving up a
// size. A car goes to a medium spot on floor 3 rather than a large one on
// floor 1, which keeps large spots free for trucks.
type BestFit struct{}

func (BestFit) Find(floors []*Floor, v VehicleType) *ParkingSpot {
	for _, size := range fittingSizes(v) {
		for _, f := range floors {
			if s := f.nearestFree(size); s != nil {
				return s
			}
		}
	}
	return nil
}

// NearestFirst fills the lowest floor first, using any size that fits.
// Shorter walk for drivers, but cars can take spots that only trucks can use.
type NearestFirst struct{}

func (NearestFirst) Find(floors []*Floor, v VehicleType) *ParkingSpot {
	for _, f := range floors {
		for _, size := range fittingSizes(v) {
			if s := f.nearestFree(size); s != nil {
				return s
			}
		}
	}
	return nil
}
