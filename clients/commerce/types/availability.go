package types

import "time"

type Availability struct {
	Active    bool      `json:"active"`
	StartDate time.Time `json:"startDate"`
	EndDate   time.Time `json:"endDate"`
}
