package netlify

import (
	"time"

	"github.com/netlify/netlify-go"
)

// {"files": {"/index.html": "907d14fb3af2b0d4f18c2d46abe8aedce17367bd"}}
type Digest struct {
	Files map[string]string `json:"files"`
}

// Represents a Netlify deploy
type Deploy struct {
	Id     string `json:"id"`
	SiteId string `json:"siteId"`
	UserId string `json:"-"`

	// State of the deploy (uploading/uploaded/processing/ready/error)
	State string `json:"state"`

	// Cause of error if State is "error"
	ErrorMessage string `json:"errorMessage,omitempty"`

	// Shas of files that needs to be uploaded before the deploy is ready
	Required []string `json:"required"`

	DeployUrl     string `json:"deployUrl"`
	SiteUrl       string `json:"url"`
	ScreenshotUrl string `json:"screenshotUrl,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func newDeploy(d *netlify.Deploy) *Deploy {
	return &Deploy{
		Id:            d.Id,
		SiteId:        d.SiteId,
		UserId:        d.UserId,
		State:         d.State,
		ErrorMessage:  d.ErrorMessage,
		Required:      d.Required,
		DeployUrl:     d.DeployUrl,
		SiteUrl:       d.SiteUrl,
		ScreenshotUrl: d.ScreenshotUrl,
		CreatedAt:     d.CreatedAt.Time,
		UpdatedAt:     d.UpdatedAt.Time,
	}
}

// Represents a Netlify site
type Site struct {
	Id     string `json:"id"`
	UserId string `json:"userId"`

	// These fields can be updated through the API
	Name              string `json:"name"`
	Domain            string `json:"domain"`
	Password          string `json:"password"`
	NotificationEmail string `json:"notificationEmail"`

	State   string `json:"state"`
	Premium bool   `json:"premium"`
	Claimed bool   `json:"claimed"`

	Url           string `json:"url"`
	AdminUrl      string `json:"adminUrl"`
	DeployUrl     string `json:"deployUrl"`
	ScreenshotUrl string `json:"screenshotUrl"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func newSite(s *netlify.Site) *Site {
	return &Site{
		Id:                s.Id,
		UserId:            s.UserId,
		Name:              s.Name,
		Domain:            s.CustomDomain,
		Password:          s.Password,
		NotificationEmail: s.NotificationEmail,
		State:             s.State,
		Premium:           s.Premium,
		Claimed:           s.Claimed,
		Url:               s.Url,
		AdminUrl:          s.AdminUrl,
		DeployUrl:         s.DeployUrl,
		ScreenshotUrl:     s.ScreenshotUrl,
		CreatedAt:         s.CreatedAt.Time,
		UpdatedAt:         s.UpdatedAt.Time,
	}
}
