package dimensions

import "strconv"

const (
	Centimeter Unit = "cm"
	Meter      Unit = "m"
	Inch       Unit = "in"
	Foot       Unit = "ft"
)

type Size struct {
	Length float64 `json:"length"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

func (s Size) String() string {
	str := strconv.FormatFloat(float64(s.Length), 'f', 2, 64)
	str += " x " + strconv.FormatFloat(float64(s.Width), 'f', 2, 64)
	str += " x " + strconv.FormatFloat(float64(s.Height), 'f', 2, 64)

	return str
}

type Unit string

// Print nice names
func (u Unit) Name() string {
	switch u {
	case "lb":
		return "Pound"
	case "oz":
		return "Ounce"
	case "kg":
		return "Kilogram"
	case "g":
		return "Gram"
	}
	return ""
}

// Convert everything to grams
var conversions = map[Unit]float64{Centimeter: 0.01, Meter: 1, Inch: 0.0254, Foot: 0.3048}

func Convert(size Size, from, to Unit) Size {
	fromM := conversions[from]
	toM := 1 / conversions[to]

	// Convert to and then from grams
	return Size{
		size.Length * toM * fromM,
		size.Width * toM * fromM,
		size.Height * toM * fromM,
	}
}

var Units = []Unit{Centimeter, Meter, Inch, Foot}
