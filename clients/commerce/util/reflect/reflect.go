package reflect

import (
	"fmt"
	"reflect"
)

// Ensure this is a pointer to a slice
func IsPtrSlice(v reflect.Value) bool {
	if v.Kind() != reflect.Ptr {
		return false
	}

	v = v.Elem()
	if v.Kind() != reflect.Slice {
		return false
	}

	return true
}

// Ensure this is a slice of pointers
func IsSliceOfPtr(slice reflect.Value) bool {
	v := slice.Index(0)
	if v.Type().Kind() == reflect.Ptr {
		return true
	}

	return false
}

// Set field of addressable struct (pointer to struct)
func SetField(ps reflect.Value, name string, value interface{}) error {
	// Get struct
	s := ps.Elem()

	// Get field
	f := s.FieldByName(name)

	// Ensure we can set
	if f.IsValid() && f.CanSet() {
		return fmt.Errorf("Not addressable: %v", ps)
	}

	f.Set(reflect.ValueOf(value))
	return nil
}

func FieldNames(s interface{}) []string {
	typ := reflect.ValueOf(s).Type()
	num := typ.NumField()

	names := make([]string, num, num)

	for i := 0; i < num; i++ {
		field := typ.Field(i)
		names[i] = field.Name
	}

	return names
}

func IsZero(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Interface, reflect.Ptr:
		return v.IsNil()
	}
	return false
}
