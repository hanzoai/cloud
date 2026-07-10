package test

import (
	"time"

	// "github.com/hanzoai/cloud/clients/commerce/log"

	. "github.com/hanzoai/cloud/clients/commerce/models/types/schedule"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func date(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// see the comments in models/types/schedule.go for more information
var _ = Describe("models/types/schedule", func() {
	Context("schedule.Cutoff", func() {
		It("should correctly calculate rolling cutoffs", func() {
			s := Schedule{Type: DailyRolling}
			s.Period = 12
			cutoff := s.Cutoff(date(2100, time.August, 12))
			expected := date(2100, time.July, 31)
			Expect(cutoff).To(Equal(expected))

			s.Period = 5
			cutoff = s.Cutoff(date(1990, time.January, 2))
			expected = date(1989, time.December, 28)
			Expect(cutoff).To(Equal(expected))

			s.Period = 1
			cutoff = s.Cutoff(date(2016, time.March, 1))
			expected = date(2016, time.February, 29)
			Expect(cutoff).To(Equal(expected))

			s.Period = 1
			cutoff = s.Cutoff(date(2015, time.March, 1))
			expected = date(2015, time.February, 28)
			Expect(cutoff).To(Equal(expected))
		})

		It("should correctly calculate weekly cutoffs", func() {
			/*
				   September 2016
				Su Mo Tu We Th Fr Sa
				             1  2  3
				 4  5  6  7  8  9 10
				11 12 13 14 15 16 17
				18 19 20 21 22 23 24
				25 26 27 28 29 30
			*/
			s := Schedule{Type: WeeklyAnchored}
			s.WeeklyAnchor = "monday"

			cutoff := s.Cutoff(date(2016, time.September, 19))
			expected := date(2016, time.September, 12)
			Expect(cutoff).To(Equal(expected))

			cutoff = s.Cutoff(date(2016, time.September, 18))
			expected = date(2016, time.September, 5)
			Expect(cutoff).To(Equal(expected))

			cutoff = s.Cutoff(date(2016, time.September, 20))
			expected = date(2016, time.September, 12)
			Expect(cutoff).To(Equal(expected))

			cutoff = s.Cutoff(date(2016, time.September, 27))
			expected = date(2016, time.September, 19)
			Expect(cutoff).To(Equal(expected))
		})

		It("should correctly calculate monthly cutoffs", func() {
			/*
							       2016
				       January               February                 March
				Su Mo Tu We Th Fr Sa   Su Mo Tu We Th Fr Sa   Su Mo Tu We Th Fr Sa
						1  2       1  2  3  4  5  6          1  2  3  4  5
				 3  4  5  6  7  8  9    7  8  9 10 11 12 13    6  7  8  9 10 11 12
				10 11 12 13 14 15 16   14 15 16 17 18 19 20   13 14 15 16 17 18 19
				17 18 19 20 21 22 23   21 22 23 24 25 26 27   20 21 22 23 24 25 26
				24 25 26 27 28 29 30   28 29                  27 28 29 30 31

					  April                   May                   June
				Su Mo Tu We Th Fr Sa   Su Mo Tu We Th Fr Sa   Su Mo Tu We Th Fr Sa
						1  2    1  2  3  4  5  6  7             1  2  3  4
				 3  4  5  6  7  8  9    8  9 10 11 12 13 14    5  6  7  8  9 10 11
				10 11 12 13 14 15 16   15 16 17 18 19 20 21   12 13 14 15 16 17 18
				17 18 19 20 21 22 23   22 23 24 25 26 27 28   19 20 21 22 23 24 25
				24 25 26 27 28 29 30   29 30 31               26 27 28 29 30
			*/
			s := Schedule{Type: MonthlyAnchored}
			s.MonthlyAnchor = 31

			cutoff := s.Cutoff(date(2016, time.April, 30))
			expected := date(2016, time.March, 1)
			Expect(cutoff).To(Equal(expected))

			cutoff = s.Cutoff(date(2016, time.April, 29))
			expected = date(2016, time.February, 1)
			Expect(cutoff).To(Equal(expected))

			cutoff = s.Cutoff(date(2016, time.February, 29))
			expected = date(2016, time.January, 1)
			Expect(cutoff).To(Equal(expected))

			cutoff = s.Cutoff(date(2016, time.February, 28))
			expected = date(2015, time.December, 1)
			// log.Warn("cutoff = %v, expected = %v", cutoff, expected)
			Expect(cutoff).To(Equal(expected))
		})
	})
})
