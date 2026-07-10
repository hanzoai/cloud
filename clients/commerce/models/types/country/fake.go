package country

import (
	"math/rand"
	"strings"
)

func Fake() string {
	return strings.ToLower(Countries[rand.Intn(len(Countries))].Codes.Alpha2)
}
