package datastore

import (
	"encoding/json"
)

// Property represents a datastore property
type Property struct {
	Name    string
	Value   interface{}
	NoIndex bool
	Multi   bool
}

// PropertyList is a list of properties
type PropertyList []Property

// PropertyLoadSaver is the interface for custom property loading/saving
type PropertyLoadSaver interface {
	Load([]Property) error
	Save() ([]Property, error)
}

// LoadStruct loads properties into a struct (dst should be a pointer)
func LoadStruct(dst interface{}, ps []Property) error {
	// Convert properties to map
	m := make(map[string]interface{})
	for _, p := range ps {
		m[p.Name] = p.Value
	}

	// Marshal to JSON and unmarshal to struct
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}

	return json.Unmarshal(data, dst)
}

// SaveStruct saves a struct to properties (src should be a pointer)
func SaveStruct(src interface{}) ([]Property, error) {
	// Marshal struct to JSON
	data, err := json.Marshal(src)
	if err != nil {
		return nil, err
	}

	// Unmarshal to map
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}

	// Convert map to properties
	ps := make([]Property, 0, len(m))
	for k, v := range m {
		ps = append(ps, Property{
			Name:  k,
			Value: v,
		})
	}

	return ps, nil
}
