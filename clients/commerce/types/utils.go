package types

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	humanize "github.com/dustin/go-humanize"

	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

func FloatPrice(price currency.Cents) float64 {
	return math.Floor(float64(price)*100+0.5) / 10000
}

func DisplayPrice(t currency.Type, price currency.Cents) string {
	f := ""
	if t.IsZeroDecimal() {
		f = strconv.FormatFloat(float64(price), 'f', 0, 64)
	} else {
		f = strconv.FormatFloat(FloatPrice(price), 'f', 2, 64)
	}
	bits := strings.Split(f, ".")
	decimal := ""
	if len(bits) > 1 {
		decimal = "." + bits[1]
	}
	integer, _ := strconv.ParseInt(bits[0], 10, 64)
	return t.Symbol() + humanize.Comma(integer) + decimal
}

// Non-breaking hyphens in title
func DisplayTitle(title string) string {
	return strings.Replace(title, "-", "&#8209;", -1)
}

func SplitParagraph(text string) []string {
	return regexp.MustCompile("\\n\\s*\\n").Split(text, -1)
}
