package schedule

import (
	"time"
)

// This package provides functions that perform payment scheduling calendrical
// calculations.

// We'd like to pay our affiliates according to some predictable schedule.
// Stripe does not permit creating a refund on a stripe-to-stripe transfer once
// the destination stripe account balance is insufficiently large to complete the
// refund (e.g., if some portion of the stripe balance has been transferred to an
// external bank), so it is necessary to add a configurable amount of latency
// between receipt-of-payment and transfer-of-funds to affiliates to provide a
// low-effort buffer against refund/chargeback affiliate fraud.

// We currenly copy stripe's automated payout API. Here's their documentation:
// https://stripe.com/docs/connect/bank-transfers#payout-information
// http://web.archive.org/web/20160404035832/https://stripe.com/docs/connect/bank-transfers#payout-information

// The current scheduling modes supported are:

// - Daily, rolling payments. For example, if there's a rolling payment latency of
//   12 days, then:
//   * a payment received on 2100-07-31 will be eligible for transfer on 2100-08-12.

// - Weekly, anchored payments. Consider the following calendar:
//      September 2016
//   Su Mo Tu We Th Fr Sa
//                1  2  3
//    4  5  6  7  8  9 10
//   11 12 13 14 15 16 17
//   18 19 20 21 22 23 24
//   25 26 27 28 29 30
//   If the weekly anchor is Monday, then:
//   * a payment received on 2016-09-05 will be paid out no earlier than 2016-09-19,
//   * a payment received on 2016-09-11 will be paid out no earlier than 2016-09-19, and
//   * a payment received on 2016-09-12 will be paid out no earlier than 2016-09-26.

//   Note that this adds an extra week (7 days) of latency between
//   receipt-of-payment and transfer-of-funds.`

// - Monthly, anchored payments. Consider the following calendar:
//                                  2016
//          January               February                 March
//   Su Mo Tu We Th Fr Sa   Su Mo Tu We Th Fr Sa   Su Mo Tu We Th Fr Sa
//                   1  2       1  2  3  4  5  6          1  2  3  4  5
//    3  4  5  6  7  8  9    7  8  9 10 11 12 13    6  7  8  9 10 11 12
//   10 11 12 13 14 15 16   14 15 16 17 18 19 20   13 14 15 16 17 18 19
//   17 18 19 20 21 22 23   21 22 23 24 25 26 27   20 21 22 23 24 25 26
//   24 25 26 27 28 29 30   28 29                  27 28 29 30 31

//             April                   May                   June
//   Su Mo Tu We Th Fr Sa   Su Mo Tu We Th Fr Sa   Su Mo Tu We Th Fr Sa
//                   1  2    1  2  3  4  5  6  7             1  2  3  4
//    3  4  5  6  7  8  9    8  9 10 11 12 13 14    5  6  7  8  9 10 11
//   10 11 12 13 14 15 16   15 16 17 18 19 20 21   12 13 14 15 16 17 18
//   17 18 19 20 21 22 23   22 23 24 25 26 27 28   19 20 21 22 23 24 25
//   24 25 26 27 28 29 30   29 30 31               26 27 28 29 30

//   If the monthly anchor is the 15th, then:
//   * a payment received on 2016-01-14 will be paid out no earlier than 2016-03-15,
//   * a payment received on 2016-01-15 will be paid out no earlier than 2016-03-15,
//   * a payment received on 2016-02-01 will be paid out no earlier than 2016-04-15, and
//   * a payment received on 2016-02-15 will be paid out no earlier than 2016-04-15.

//   Additonally, if the monthly anchor is the 31st, then:
//   * a payment received on 2016-02-01 will be paid out no earlier than 2016-04-30,
//   * a payment received on 2016-02-29 will be paid out no earlier than 2016-04-30,
//   * a payment received on 2016-03-01 will be paid out no earlier than 2016-05-31, and
//   * a payment received on 2016-03-31 will be paid out no earlier than 2016-05-31.

//   Note that this adds an extra calendar-month (i.e. 28, 29, 30, 31 days) of
//   latency between receipt-of-payment and transfer-of-funds.
//   If the anchor is larger than the number of days present in the current
//   month, then the anchor is truncated to the last day of the month.

// There are other scheduling methods possible. We could support a payroll-style
// schedule, in which payments are made monthly (this is equivalent to "monthly,
// anchored payments"), semimonthly (in which the year is divided into 24
// payment-and-latency periods aligned with monthly boundaries), biweekly (in which
// the year is divided into 26 payment-and-latency periods that fall in and out
// of sync with monthly boundaries), and weekly (this is equivalent to "weekly,
// anchored payments").

type Type string

const (
	DailyRolling    Type = "daily-rolling"
	WeeklyAnchored       = "weekly-anchored"
	MonthlyAnchored      = "monthly-anchored"
)

type Schedule struct {
	Type          Type   `json:"type"`
	Period        int    `json:"period"`                  // DailyRolling: number of days of payment latency
	WeeklyAnchor  string `json:"weeklyAnchor,omitempty"`  // WeeklyAnchored: day of the week when the payout should occur
	MonthlyAnchor int    `json:"monthlyAnchor,omitempty"` // MonthlyAnchored: day of the month when the payout should occur
}

func (s Schedule) Cutoff(t time.Time) time.Time {
	switch s.Type {
	case DailyRolling:
		return rollingCutoff(t, -s.Period)
	case WeeklyAnchored:
		return weeklyCutoff(t, parseDayOfWeek(s.WeeklyAnchor))
	case MonthlyAnchored:
		return monthlyCutoff(t, s.MonthlyAnchor)
	default:
		panic("invalid cutoff type")
	}
}

func rollingCutoff(t time.Time, dayDelta int) time.Time {
	year, month, day := t.UTC().Date()
	ret := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	ret = ret.AddDate(0, 0, dayDelta)
	return ret
}

func weeklyCutoff(t time.Time, anchor time.Weekday) time.Time {
	weekday := t.Weekday()
	delta := -7
	if weekday < anchor {
		delta = -14
	}
	year, month, day := t.UTC().Date()
	closestWeekday := day + int(anchor) - int(weekday) + delta
	return time.Date(year, month, closestWeekday, 0, 0, 0, 0, time.UTC)
}

func monthlyCutoff(t time.Time, rawAnchor int) time.Time {
	year, month, day := t.UTC().Date()
	maxDay := daysInMonth(year, month)
	anchor := clamp(rawAnchor, 1, maxDay)
	// the rightmost bound is exclusive; e.g. given a payout date of
	// 2016-03-15 and a current date of 2016-03-15, then all fees from
	// (-infinity, 2016-01-31] = (-infinity, 2016-02-01) are eligible for
	// payment
	monthDelta := 1
	if day < anchor {
		// the anchor day hasn't yet been hit, so the only eligible
		// payments are from three months ago
		monthDelta = 2
	}
	ret := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	ret = ret.AddDate(0, -monthDelta, 0)
	return ret
}

func clamp(val int, min int, max int) int {
	if val < min {
		return min
	}
	if val > max {
		return max
	}
	return val
}

func isLeapYear(year int) bool {
	if year%4 != 0 {
		return false
	}
	if year%100 != 0 {
		return true
	}
	if year%400 != 0 {
		return false
	}
	return true
}

func daysInMonth(year int, month time.Month) int {
	switch month {
	case time.January:
		fallthrough
	case time.March:
		fallthrough
	case time.May:
		fallthrough
	case time.July:
		fallthrough
	case time.August:
		fallthrough
	case time.October:
		fallthrough
	case time.December:
		return 31
	case time.February:
		if isLeapYear(year) {
			return 29
		} else {
			return 28
		}
	case time.April:
		fallthrough
	case time.June:
		fallthrough
	case time.September:
		fallthrough
	case time.November:
		return 30
	default:
		panic("month out of range")
	}
}

func parseDayOfWeek(s string) time.Weekday {
	switch s {
	case "sunday":
		return time.Sunday
	case "monday":
		return time.Monday
	case "tuesday":
		return time.Tuesday
	case "wednesday":
		return time.Wednesday
	case "thursday":
		return time.Thursday
	case "friday":
		return time.Friday
	case "saturday":
		return time.Saturday
	default:
		panic("bad time string")
	}
}
