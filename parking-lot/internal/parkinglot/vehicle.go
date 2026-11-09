package parkinglot

type VehicleType int

const (
	Motorcycle VehicleType = iota
	Car
	Truck
)

func (v VehicleType) String() string {
	switch v {
	case Motorcycle:
		return "MOTORCYCLE"
	case Car:
		return "CAR"
	case Truck:
		return "TRUCK"
	}
	return "UNKNOWN"
}

type Vehicle struct {
	Plate string
	Type  VehicleType
}

type SpotSize int

const (
	Small SpotSize = iota
	Medium
	Large
)

func (s SpotSize) String() string {
	switch s {
	case Small:
		return "SMALL"
	case Medium:
		return "MEDIUM"
	case Large:
		return "LARGE"
	}
	return "UNKNOWN"
}

// fittingSizes lists the spot sizes a vehicle can use, smallest first.
// A vehicle may take a bigger spot, but only when every smaller one that fits
// is taken, so cars don't use up the spots only trucks can park in.
func fittingSizes(v VehicleType) []SpotSize {
	switch v {
	case Motorcycle:
		return []SpotSize{Small, Medium, Large}
	case Car:
		return []SpotSize{Medium, Large}
	case Truck:
		return []SpotSize{Large}
	}
	return nil
}
