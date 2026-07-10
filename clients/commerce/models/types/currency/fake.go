package currency

import "math/rand"

var numCurrencies = len(Types)

func Fake() Type {
	return Types[rand.Intn(numCurrencies)]
}

func (c Cents) Fake() Cents {
	return Cents(rand.Intn(99) * 100)
}

func (c Cents) FakeN(max int) Cents {
	return Cents(rand.Intn(max) * 100)
}
