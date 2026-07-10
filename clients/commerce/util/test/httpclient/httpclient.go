package httpclient

import "context"

func New(ctx context.Context, moduleName string) *Client {
	client := new(Client)
	client.context = ctx
	client.moduleName = moduleName
	client.setBaseUrl()
	return client
}
