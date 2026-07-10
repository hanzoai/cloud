package query

import "reflect"

// Ensure this is a pointer to a slice
func isPtrSlice(v reflect.Value) bool {
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
func isSliceOfPtr(slice reflect.Value) bool {
	if slice.Len() == 0 {
		return false
	}

	v := slice.Index(0)
	if v.Type().Kind() == reflect.Ptr {
		return true
	}

	return false
}
