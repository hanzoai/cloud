package types

import "time"

// Custom time type for parsing Shipwire's funky timestamps
type Date struct {
	time.Time
}

func (d *Date) UnmarshalJSON(data []byte) (err error) {
	str := string(data)

	// Ignore null timestamps
	if str == "null" {
		return nil
	}

	// Layout v1
	if d.Time, err = time.Parse(`"2006-01-02 15:04:05"`, str); err == nil {
		return nil
	}

	// Layout v2
	if d.Time, err = time.Parse(`"2006-01-02T15:04:05-07:00"`, str); err == nil {
		return nil
	}

	// Fallback to normal Unmarshal method
	return d.Time.UnmarshalJSON(data)
}
