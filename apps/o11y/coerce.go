package o11y

import "time"

// Native datastore scan-type coercers. datastore.Query returns each
// column already decoded into its native Go type (String→string,
// UInt64→uint64, DateTime64→time.Time, …); these accept the native value (and
// defensively its pointer form) so a nil/absent column degrades to a zero value
// rather than panicking. Kept package-local (the eval package has its own copy;
// duplicating three tiny total-functions is cheaper than a shared coercion dep).

func asString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case *string:
		if s != nil {
			return *s
		}
	}
	return ""
}

func asFloat(v any) float64 {
	switch f := v.(type) {
	case float64:
		return f
	case *float64:
		if f != nil {
			return *f
		}
	case float32:
		return float64(f)
	}
	return 0
}

func asInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case int16:
		return int64(n)
	case int8:
		return int64(n)
	case int:
		return int64(n)
	case uint64:
		return int64(n)
	case uint32:
		return int64(n)
	case uint16:
		return int64(n)
	case uint8:
		return int64(n)
	case uint:
		return int64(n)
	case float64:
		return int64(n)
	case float32:
		return int64(n)
	case *int64:
		if n != nil {
			return *n
		}
	case *uint64:
		if n != nil {
			return int64(*n)
		}
	}
	return 0
}

func asTime(v any) time.Time {
	switch t := v.(type) {
	case time.Time:
		return t
	case *time.Time:
		if t != nil {
			return *t
		}
	}
	return time.Time{}
}
