package httpclient

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	// "github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
)

func getModuleHost(ctx context.Context, moduleName string) string {
	host := "localhost"
	port := os.Getenv("DEV_APPSERVER_PORT")

	switch moduleName {
	case "default":
		return host + ":" + port
	case "api":
		n, _ := strconv.Atoi(port)
		return host + ":" + strconv.Itoa(n+1)
	default:
		return host + ":" + port
	}

}

type Client struct {
	context    context.Context
	moduleName string
	baseURL    string
}

func (c *Client) URL(path string) string {
	return c.baseURL + path
}

func (c *Client) setBaseUrl() {
	moduleHost := getModuleHost(c.context, c.moduleName)
	url := "http://" + strings.Trim(moduleHost, "/")
	c.baseURL = strings.Trim(url, "/")
	log.Warn("%s baseURL %s", c.moduleName, c.baseURL)
}

func (c *Client) getURL(path string) string {
	return c.baseURL + "/" + strings.TrimLeft(path, "/")
}

func (c *Client) Get(path string) (res Response, err error) {
	res.Response, err = http.Get(c.getURL(path))
	return res, err
}

func (c *Client) Post(path, bodyType string, reader io.Reader) (res Response, err error) {
	res.Response, err = http.Post(c.getURL(path), bodyType, reader)
	return res, err
}

func (c *Client) PostForm(path string, data url.Values) (res Response, err error) {
	res.Response, err = http.PostForm(c.getURL(path), data)
	return res, err
}

func (c *Client) PostJSON(path string, src interface{}) (res Response, err error) {
	encoded := json.Encode(src)
	res.Response, err = http.Post(c.getURL(path), "application/json", strings.NewReader(encoded))
	return res, err
}
