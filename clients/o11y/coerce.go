package o11y

import "time"

// Native ClickHouse scan-type coercers. aiobject.DatastoreQuery returns each
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

func asUint64(v any) uint64 {
	switch n := v.(type) {
	case uint64:
		return n
	case uint32:
		return uint64(n)
	case uint16:
		return uint64(n)
	case uint8:
		return uint64(n)
	case uint:
		return uint64(n)
	case int64:
		if n >= 0 {
			return uint64(n)
		}
	case int32:
		if n >= 0 {
			return uint64(n)
		}
	case int:
		if n >= 0 {
			return uint64(n)
		}
	case float64:
		if n >= 0 {
			return uint64(n)
		}
	case *uint64:
		if n != nil {
			return *n
		}
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
