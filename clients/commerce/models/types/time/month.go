package time

type Month string

const (
	January   Month = "January"
	Febuary         = "Febuary"
	March           = "March"
	April           = "April"
	May             = "May"
	June            = "June"
	July            = "July"
	August          = "August"
	September       = "September"
	October         = "October"
	November        = "November"
	December        = "December"
)

var Months = []Month{
	January,
	Febuary,
	March,
	April,
	May,
	June,
	July,
	August,
	September,
	October,
	November,
	December,
}

func GetMonth(i int) Month {
	return Months[i-1]
}
