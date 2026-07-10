package currency

import (
	// "fmt"
	// . "math/big"
	"strconv"
)

type Cents int64

func CentsFromString(s string) Cents {
	f, _ := strconv.ParseFloat(s, 64)
	return Cents(int64(f * 100))
}
