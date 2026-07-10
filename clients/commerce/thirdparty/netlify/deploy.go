package netlify

import (
	"io/ioutil"
	"net/url"

	"github.com/netlify/netlify-go"

	"github.com/hanzoai/cloud/clients/commerce/log"
)

func (c *Client) CreateDeploy(ste *Site, digest *Digest, draft bool) (*Deploy, error) {
	log.Debug("Creating deploy for site: %s, using digest: %#v", ste.Id, digest, c.ctx)

	// Manually construct call to netlify for deploy
	params := url.Values{}
	if draft {
		params["draft"] = []string{"true"}
	}
	options := &netlify.RequestOptions{JsonBody: digest, QueryParams: &params}
	ndeploy := &netlify.Deploy{}
	res, err := c.client.Request("POST", "/sites/"+ste.Id+"/deploys", options, ndeploy)

	if err != nil {
		buf, _ := ioutil.ReadAll(res.Body)
		log.Error("Deploy failed, response: %s", string(buf), c.ctx)
		return &Deploy{}, err
	}

	return newDeploy(ndeploy), err
}
