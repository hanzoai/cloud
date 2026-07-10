package httpclient

import (
	"io/ioutil"
	"net/http"

	"github.com/hanzoai/cloud/clients/commerce/log"
)

type Response struct {
	*http.Response
}

// Returns body as a string
func (r *Response) Text() string {
	defer r.Response.Body.Close()

	if bytes, err := ioutil.ReadAll(r.Response.Body); err != nil {
		log.Error("Unable to read response body: %v", err)
		return ""
	} else {
		return string(bytes)
	}
}
