package client

import (
	"strconv"

	"github.com/gin-gonic/gin"
)

type GeoPoint struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

type Client struct {
	Ip        string   `json:"ip,omitempty" datastore:",noindex"`
	UserAgent string   `json:"userAgent,omitempty" datastore:",noindex"`
	Referer   string   `json:"referer,omitempty" datastore:",noindex"`
	Language  string   `json:"language,omitempty"`
	Country   string   `json:"country,omitempty"`
	Region    string   `json:"region,omitempty"`
	City      string   `json:"city,omitempty"`
	GeoPoint  GeoPoint `json:"geoPoint,omitempty"`
}

func New(c *gin.Context) Client {
	req := c.Request

	client := Client{
		UserAgent: req.UserAgent(),
		Referer:   req.Referer(),
		Language:  req.Header.Get("Accept-Language"),
	}

	// Check for proxied values from Cloudflare
	client.Ip = req.Header.Get("CF-Connecting-IP")
	client.Country = req.Header.Get("CF-IPCountry")

	// Not behind a proxy
	if client.Ip == "" {
		client.Ip = req.RemoteAddr
		client.Country = req.Header.Get("X-AppEngine-Country")
		client.Region = req.Header.Get("X-AppEngine-Region")
		client.City = req.Header.Get("X-AppEngine-City")

		// Parse latitude and longitude into geopoint
		geo := c.Request.Header.Get("X-AppEngine-CitLatLong")
		lat, _ := strconv.ParseFloat(geo, 64)
		long, _ := strconv.ParseFloat(geo, 64)
		client.GeoPoint = GeoPoint{Lat: lat, Lng: long}
	}

	return client
}

func (c Client) Blacklisted() bool {
	// Should check against a blacklist (probably configurable?)
	return false
}
