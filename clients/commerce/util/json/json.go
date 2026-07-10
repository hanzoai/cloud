package json

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strings"
)

// isDevelopment checks if we're in development mode without importing config
// to avoid import cycles. Checks GAE_ENV and HANZO_ENV environment variables.
var isDevelopment = func() bool {
	env := os.Getenv("GAE_ENV")
	if env == "" {
		env = os.Getenv("HANZO_ENV")
	}
	return env == "" || strings.ToLower(env) == "development" || strings.ToLower(env) == "dev"
}()

func Encode(value interface{}) string {
	return string(EncodeBytes(value))
}

func EncodeBytes(value interface{}) []byte {
	b, err := json.Marshal(value)
	if err != nil {
		fmt.Printf("%v\n", err)
	}
	return b
}

func EncodeIndentBytes(value interface{}, prefix, indent string) []byte {
	var b []byte
	var err error

	b, err = json.MarshalIndent(value, prefix, indent)

	if err != nil {
		fmt.Printf("%v\n", err)
	}
	return b
}

func EncodeRaw(value interface{}) json.RawMessage {
	return json.RawMessage(EncodeBytes(value))
}

func EncodeBuffer(value interface{}) *bytes.Buffer {
	return bytes.NewBuffer(EncodeBytes(value))
}

func Decode(body io.ReadCloser, dst interface{}) error {
	data, err := ioutil.ReadAll(body)
	body.Close()

	if err != nil {
		return err
	}

	if err := json.Unmarshal(data, dst); err != nil {
		return err
	}

	return nil
}

func DecodeBytes(data []byte, dst interface{}) error {
	if err := json.Unmarshal(data, dst); err != nil {
		return err
	}
	return nil
}

func DecodeBuffer(buf *bytes.Buffer, dst interface{}) error {
	if err := json.Unmarshal(buf.Bytes(), dst); err != nil {
		return err
	}
	return nil
}

var Unmarshal = json.Unmarshal
