package fake

import (
	"math/rand"
	"reflect"

	"github.com/hanzoai/cloud/clients/commerce/util/slug"
)

type fieldMap map[string]reflect.Value

// Check if field is in set
func in(fields []string, field string) bool {
	for _, f := range fields {
		if field == f {
			return true
		}
	}
	return false
}

// Set field to zero value
func zero(v reflect.Value) {
	v.Set(reflect.Zero(v.Type().Elem()))
}

// Get exported / addressable fields on struct
func fields(fake interface{}) fieldMap {
	t := reflect.TypeOf(fake).Elem()
	v := reflect.ValueOf(fake).Elem()
	m := make(fieldMap)

	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		ft := t.Field(i)

		// Skip private fields, non-addressable fields
		if ft.PkgPath != "" || !f.CanSet() {
			continue
		}

		m[ft.Name] = f
	}

	return m
}

// Zero out non-specified fields on fake
func Only(fake interface{}, only ...string) interface{} {
	for name, f := range fields(fake) {
		if !in(only, name) {
			zero(f)
		}
	}

	return fake
}

// Zero all fields except specified on fake
func Except(fake interface{}, except ...string) interface{} {
	for name, f := range fields(fake) {
		if in(except, name) {
			zero(f)
		}
	}

	return fake
}

func RandSeq(n int, runes []rune) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = runes[rand.Intn(len(runes))]
	}
	return string(b)
}

func Bool() bool {
	return bool(rand.Intn(10)&1 == 0)
}

func Url() string {
	return "http://" + DomainName()
}

func Id() string {
	return RandSeq(10, []rune("abcdefghijklmnopqrstuvwxyZABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890"))
}

func SKU() string {
	return slug.Slugify(ProductName())
}

func Slug() string {
	return slug.Slugify(ProductName())
}

func TaxID() string {
	return RandSeq(3, []rune("1234567890")) + "-" + RandSeq(2, []rune("1234567890")) + "-" + RandSeq(4, []rune("1234567890"))
}

// 0xb1c0abd217193ffe64f97caedad8fa6f0f9c0265967d2ab9fb782280c928fb47
func EthereumAddress() string {
	return "0x" + RandSeq(63, []rune("abcdef1234567890"))
}

// EOS5krVucV7EL3Q7gdQSiWahM1YkjZ1WjVeaNsBwcAWn96rgdAfzP
func EOSAddress() string {
	return "EOS" + RandSeq(49, []rune("abcdefghijklmnopqrstuvwxyZABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890"))
}

// 0xb1c0abd217193ffe64f97caedad8fa6f0f9c0265967d2ab9fb782280c928fb47
func EOSTransactionHash() string {
	return RandSeq(63, []rune("abcdef1234567890"))
}
