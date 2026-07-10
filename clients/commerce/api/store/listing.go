package store

import (
	"errors"
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/store"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

// orgNamespacedDB returns a datastore scoped to the authenticated org's namespace.
// All listing handlers MUST use this instead of raw datastore.New(c) to ensure
// tenant isolation.
func orgNamespacedDB(c *gin.Context) *datastore.Datastore {
	org := middleware.GetOrganization(c)
	// Red MED-1: datastore.New binds the shared systemDB regardless of the ctx
	// namespace, so the listing sub-routes (create/update/patch/delete/get/list)
	// read/write systemDB while the store itself is created per-org via
	// NewNamespaced (rest.newEntity) — a mismatch that 404s every listing op on a
	// real per-org store. NewNamespaced routes to the caller org's own SQLite,
	// matching where the store lives.
	return datastore.NewNamespaced(org.Namespaced(c))
}

// Return all listings
func listListing(c *gin.Context) {
	id := c.Params.ByName("storeid")
	db := orgNamespacedDB(c)

	stor := store.New(db)
	if err := stor.GetById(id); err != nil {
		http.Fail(c, 404, fmt.Sprintf("Failed to retrieve store '%v': %v", id, err), err)
		return
	}

	http.Render(c, 200, stor.Listings)
}

// Get single store listing for given product/variant
func getListing(c *gin.Context) {
	db := orgNamespacedDB(c)
	id := c.Params.ByName("storeid")
	key := c.Params.ByName("key")

	// Get store
	stor := store.New(db)
	if err := stor.GetById(id); err != nil {
		http.Fail(c, 404, fmt.Sprintf("Failed to retrieve store '%v': %v", id, err), err)
		return
	}

	// Try and grab listing
	listing, ok := stor.Listings[key]

	// Maybe we have a slug or sku?
	if !ok {
		for _, listing = range stor.Listings {
			if key == listing.Slug || key == listing.SKU {
				ok = true
				break
			}
		}
	}

	// Do not override on create, user should explicitly update instead
	if !ok {
		msg := fmt.Sprintf("No listing exists for '%v' in store '%v'", key, id)
		http.Fail(c, 404, msg, errors.New(msg))
		return
	}

	http.Render(c, 200, listing)
}

func createListing(c *gin.Context) {
	id := c.Params.ByName("storeid")
	key := c.Params.ByName("key")
	db := orgNamespacedDB(c)

	stor := store.New(db)
	if err := stor.GetById(id); err != nil {
		msg := fmt.Sprintf("Failed to retrieve store '%v'", id)
		http.Fail(c, 404, msg, err)
		return
	}

	// Try and grab listing
	listing, ok := stor.Listings[key]

	// Do not override on create, user should explicitly update instead
	if ok {
		msg := fmt.Sprintf("'%v' already exists in store '%v' listing", key, id)
		http.Fail(c, 400, msg, errors.New(msg))
		return
	}

	// Decode response body for listing
	if err := json.Decode(c.Request.Body, &listing); err != nil {
		http.Fail(c, 400, "Failed decode request body", err)
		return
	}

	// Update include listing in listings
	stor.AddListing(key, listing)

	// Save store
	if err := stor.Put(); err != nil {
		http.Fail(c, 500, "Failed to save store listings", err)
	} else {
		c.Writer.Header().Add("Location", c.Request.URL.Path)
		http.Render(c, 201, stor.Listings)
	}
}

func updateListing(c *gin.Context) {
	id := c.Params.ByName("storeid")
	key := c.Params.ByName("key")
	db := orgNamespacedDB(c)

	stor := store.New(db)
	if err := stor.GetById(id); err != nil {
		http.Fail(c, 404, fmt.Sprintf("Failed to retrieve store '%v': %v", id, err), err)
		return
	}

	listing, ok := stor.Listings[key]

	// Decode response body to create new listings
	if err := json.Decode(c.Request.Body, &listing); err != nil {
		http.Fail(c, 400, "Failed decode request body", err)
		return
	}

	// Override listing potentially
	stor.Listings[key] = listing

	// Try to save store
	if err := stor.Put(); err != nil {
		http.Fail(c, 500, "Failed to save store listings", err)
	} else {
		if ok {
			http.Render(c, 200, stor.Listings)
		} else {
			c.Writer.Header().Add("Location", c.Request.URL.Path)
			http.Render(c, 201, stor.Listings)
		}
	}
}

func patchListing(c *gin.Context) {
	id := c.Params.ByName("storeid")
	key := c.Params.ByName("key")
	db := orgNamespacedDB(c)

	stor := store.New(db)
	if err := stor.GetById(id); err != nil {
		http.Fail(c, 404, fmt.Sprintf("Failed to retrieve store '%v': %v", id, err), err)
		return
	}

	listing, ok := stor.Listings[key]
	// Can't patch an object that doesn't exist!
	if !ok {
		msg := fmt.Sprintf("No listing exists for '%v' in store '%v'", key, id)
		http.Fail(c, 404, msg, errors.New(msg))
		return
	}

	// Decode response body to update listings
	if err := json.Decode(c.Request.Body, &listing); err != nil {
		http.Fail(c, 400, "Failed decode request body", err)
		return
	}

	// Try to save store
	if err := stor.Put(); err != nil {
		http.Fail(c, 500, "Failed to save store listings", err)
	} else {
		http.Render(c, 200, stor.Listings)
	}
}

func deleteListing(c *gin.Context) {
	id := c.Params.ByName("storeid")
	key := c.Params.ByName("key")
	db := orgNamespacedDB(c)

	stor := store.New(db)
	if err := stor.GetById(id); err != nil {
		http.Fail(c, 404, fmt.Sprintf("Failed to retrieve store '%v': %v", id, err), err)
		return
	}

	// Check if file even exists
	_, ok := stor.Listings[key]
	if !ok {
		msg := fmt.Sprintf("No listing exists for '%v' in store '%v'", key, id)
		http.Fail(c, 404, msg, errors.New(msg))
		return
	}

	// Delete key from map
	delete(stor.Listings, key)

	// Try to save store
	if err := stor.Put(); err != nil {
		http.Fail(c, 500, "Failed to save store listings", err)
	} else {
		c.Data(204, "application/json", make([]byte, 0))
	}
}
