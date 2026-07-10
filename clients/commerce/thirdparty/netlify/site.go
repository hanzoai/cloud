package netlify

import (
	"github.com/hanzoai/cloud/clients/commerce/log"

	"github.com/netlify/netlify-go"
)

func (c *Client) CreateSite(s *Site) (*Site, error) {
	log.Debug("Creating site: %#v", s, c.ctx)

	// Create new site on Netlify's side
	nsite, _, err := c.client.Sites.Create(&netlify.SiteAttributes{
		Name: s.Name,
	})

	if err != nil {
		return &Site{}, err
	}

	log.Debug("Created site: %#v", nsite, c.ctx)
	return newSite(nsite), err
}

func (c *Client) GetSite(s *Site) (*Site, error) {
	log.Debug("Getting site: %#v", s, c.ctx)

	// Get site
	nsite, _, err := c.client.Sites.Get(s.Id)

	if err != nil {
		return &Site{}, err
	}

	return newSite(nsite), err
}

func (c *Client) UpdateSite(s *Site) (*Site, error) {
	log.Debug("Update site: %#v", s, c.ctx)

	nsite, _, err := c.client.Sites.Get(s.Id)

	if err != nil {
		return &Site{}, err
	}

	nsite.Url = s.Url
	nsite.Name = s.Name
	nsite.State = s.State
	nsite.UserId = s.UserId
	nsite.Premium = s.Premium
	nsite.Claimed = s.Claimed
	nsite.Password = s.Password
	nsite.AdminUrl = s.AdminUrl
	nsite.DeployUrl = s.DeployUrl
	nsite.CustomDomain = s.Domain

	_, err = nsite.Update()
	if err != nil {
		return &Site{}, err
	}

	return newSite(nsite), nil
}

func (c *Client) DeleteSite(s *Site) error {
	log.Debug("Delete site: %#v", s, c.ctx)

	nsite, _, err := c.client.Sites.Get(s.Id)

	if err != nil {
		return err
	}

	_, err = nsite.Destroy()

	return err
}

// func ListSites(ctx context.Context) ([]Site, error) {
// 	client := createClient(ctx)

// 	// Create new site on Netlify's side
// 	nsites, _, err := client.Sites.List(&netlify.ListOptions{})

// 	return nsites, err
// }
