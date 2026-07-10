package gob

import (
	"bytes"
	"encoding/gob"
)

func Encode(src interface{}) ([]byte, error) {
	var buf bytes.Buffer

	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(src); err != nil {
		return buf.Bytes(), err
	}

	return buf.Bytes(), nil
}

func Decode(src []byte, dst interface{}) error {
	dec := gob.NewDecoder(bytes.NewBuffer(src))
	return dec.Decode(dst)
}

func Register(val interface{}) {
	gob.Register(val)
}
