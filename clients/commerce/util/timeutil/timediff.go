
package timeutil

import (
	"time"
)

func YearMonthDiff(t1, t2 time.Time) (years, months int) {
	t2 = t2.AddDate(0, 0, 1) // advance t2 to make the range inclusive
	years = 0
	for t1.AddDate(years, 0, 0).Before(t2) {
		years++
	}
	years--

	months = 0
	for t1.AddDate(years, months, 0).Before(t2) {
		months++
	}
	months--


	return years, months
}
