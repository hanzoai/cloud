package mixin

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"sync"
)

var (
	fieldNameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
	languageRE  = regexp.MustCompile(`^[a-z]{2}$`)
)

// validFieldName is the Go equivalent of Python's _CheckFieldName. It checks
// the validity of both field and facet names.
func validFieldName(s string) bool {
	return len(s) <= 500 && fieldNameRE.MatchString(s)
}

// SearchField represents a field in a search document
type SearchField struct {
	Name  string
	Value interface{}
}

// SearchFacet represents a facet in a search document
type SearchFacet struct {
	Name  string
	Value interface{}
}

// SearchDocumentMetadata holds metadata for a search document
type SearchDocumentMetadata struct {
	Facets []SearchFacet
}

// ErrFieldMismatch is returned when a field is to be loaded into a different
// than the one it was stored from, or when a field is missing or unexported in
// the destination struct.
type ErrFieldMismatch struct {
	FieldName string
	Reason    string
}

func (e *ErrFieldMismatch) Error() string {
	return fmt.Sprintf("search: cannot load field %q: %s", e.FieldName, e.Reason)
}

// ErrFacetMismatch is returned when a facet is to be loaded into a different
// type than the one it was stored from, or when a field is missing or
// unexported in the destination struct. StructType is the type of the struct
// pointed to by the destination argument passed to Iterator.Next.
type ErrFacetMismatch struct {
	StructType reflect.Type
	FacetName  string
	Reason     string
}

func (e *ErrFacetMismatch) Error() string {
	return fmt.Sprintf("search: cannot load facet %q into a %q: %s", e.FacetName, e.StructType, e.Reason)
}

// structCodec defines how to convert a given struct to/from a search document.
type structCodec struct {
	// byIndex returns the struct tag for the i'th struct field.
	byIndex []structTag

	// fieldByName returns the index of the struct field for the given field name.
	fieldByName map[string]int

	// facetByName returns the index of the struct field for the given facet name,
	facetByName map[string]int
}

// structTag holds a structured version of each struct field's parsed tag.
type structTag struct {
	name   string
	facet  bool
	ignore bool
}

var (
	codecsMu sync.RWMutex
	codecs   = map[reflect.Type]*structCodec{}
)

func loadCodec(t reflect.Type) (*structCodec, error) {
	codecsMu.RLock()
	codec, ok := codecs[t]
	codecsMu.RUnlock()
	if ok {
		return codec, nil
	}

	codecsMu.Lock()
	defer codecsMu.Unlock()
	if codec, ok := codecs[t]; ok {
		return codec, nil
	}

	codec = &structCodec{
		fieldByName: make(map[string]int),
		facetByName: make(map[string]int),
	}

	for i, I := 0, t.NumField(); i < I; i++ {
		f := t.Field(i)
		name, opts := f.Tag.Get("search"), ""
		if i := strings.Index(name, ","); i != -1 {
			name, opts = name[:i], name[i+1:]
		}
		ignore := false
		if name == "-" {
			ignore = true
		} else if name == "" {
			name = f.Name
		} else if !validFieldName(name) {
			return nil, fmt.Errorf("search: struct tag has invalid field name: %q", name)
		}
		facet := strings.Index(opts, "facet") >= 0
		codec.byIndex = append(codec.byIndex, structTag{name: name, facet: facet, ignore: ignore})
		if facet {
			codec.facetByName[name] = i
		} else {
			codec.fieldByName[name] = i
		}
	}

	codecs[t] = codec
	return codec, nil
}

type DocumentSaveLoad struct {
	document reflect.Value

	// Dummy field for gob, see: https://github.com/golang/go/issues/5819
	Dummy string `json:"-" datastore:"-"`
}

func (s *DocumentSaveLoad) SetDocument(doc interface{}) {
	s.document = reflect.Indirect(reflect.ValueOf(doc))
}

func (s *DocumentSaveLoad) GetDocument() reflect.Value {
	return s.document
}

func (s *DocumentSaveLoad) Load(fields []SearchField, meta *SearchDocumentMetadata) error {
	var err error

	val := s.document
	codec, err := loadCodec(val.Type())
	if err != nil {
		return err
	}

	for _, field := range fields {
		i, ok := codec.fieldByName[field.Name]
		if !ok {
			// Note the error, but keep going.
			err = &ErrFieldMismatch{
				FieldName: field.Name,
				Reason:    "no such struct field",
			}
			continue

		}
		f := val.Field(i)
		if !f.CanSet() {
			// Note the error, but keep going.
			err = &ErrFieldMismatch{
				FieldName: field.Name,
				Reason:    "cannot set struct field",
			}
			continue
		}
		v := reflect.ValueOf(field.Value)
		if ft, vt := f.Type(), v.Type(); ft != vt {
			err = &ErrFieldMismatch{
				FieldName: field.Name,
				Reason:    fmt.Sprintf("type mismatch: %v for %v data", ft, vt),
			}
			continue
		}
		f.Set(v)
	}
	if meta == nil {
		return err
	}
	for _, facet := range meta.Facets {
		i, ok := codec.facetByName[facet.Name]
		if !ok {
			// Note the error, but keep going.
			if err == nil {
				err = &ErrFacetMismatch{
					StructType: val.Type(),
					FacetName:  facet.Name,
					Reason:     "no matching field found",
				}
			}
			continue
		}
		f := val.Field(i)
		if !f.CanSet() {
			// Note the error, but keep going.
			if err == nil {
				err = &ErrFacetMismatch{
					StructType: val.Type(),
					FacetName:  facet.Name,
					Reason:     "unable to set unexported field of struct",
				}
			}
			continue
		}
		v := reflect.ValueOf(facet.Value)
		if ft, vt := f.Type(), v.Type(); ft != vt {
			if err == nil {
				err = &ErrFacetMismatch{
					StructType: val.Type(),
					FacetName:  facet.Name,
					Reason:     fmt.Sprintf("type mismatch: %v for %d data", ft, vt),
				}
				continue
			}
		}
		f.Set(v)
	}

	// None of the errors are blocking
	return nil
}

func (s DocumentSaveLoad) Save() ([]SearchField, *SearchDocumentMetadata, error) {
	val := s.GetDocument()
	codec, err := loadCodec(val.Type())
	if err != nil {
		return nil, nil, err
	}

	fields := make([]SearchField, 0, len(codec.fieldByName))
	var facets []SearchFacet
	for i, tag := range codec.byIndex {
		if tag.ignore {
			continue
		}
		f := val.Field(i)
		if !f.CanSet() {
			continue
		}

		// ignore this mixin
		if f.Type() != reflect.TypeOf(s) {
			if tag.facet {
				// ignore zeroed facets
				if reflect.Zero(f.Type()).Interface() != f.Interface() {
					facets = append(facets, SearchFacet{Name: tag.name, Value: f.Interface()})
				}
			} else {
				fields = append(fields, SearchField{Name: tag.name, Value: f.Interface()})
			}
		}
	}
	return fields, &SearchDocumentMetadata{Facets: facets}, nil
}
