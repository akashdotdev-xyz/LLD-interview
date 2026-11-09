package parkinglot

import (
	"time"
)

// PricingStrategy returns the fee in cents. Money is an integer because a
// float can't represent 0.10 exactly, and rounding errors add up.
type PricingStrategy interface {
	Fee(v VehicleType, entry, exit time.Time) int64
}

// HourlyPricing charges per started hour, with a minimum of one hour.
type HourlyPricing struct {
	Rates map[VehicleType]int64 // cents per hour
}

func DefaultPricing() HourlyPricing {
	return HourlyPricing{Rates: map[VehicleType]int64{
		Motorcycle: 100,
		Car:        300,
		Truck:      600,
	}}
}

func (p HourlyPricing) Fee(v VehicleType, entry, exit time.Time) int64 {
	hours := int64((exit.Sub(entry) + time.Hour - 1) / time.Hour) // round up
	if hours < 1 {
		hours = 1
	}
	return hours * p.Rates[v]
}
