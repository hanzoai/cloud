package site

import (
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/netlify"
	"github.com/hanzoai/cloud/clients/commerce/util/webhook"
)

// Create
func (s *Site) BeforeCreate() error {
	log.Debug("Creating site on Netlify", s.Context())

	client := netlify.NewFromNamespace(s.Context(), s.Namespace())
	nsite, err := client.CreateSite(s.Netlify())
	if err != nil {
		log.Error("netlify.CreateSite failed: %v", err, s.Context())
		return err
	}

	s.SetNetlify(nsite)

	return nil
}

func (s *Site) AfterCreate() error {
	webhook.Emit(s.Context(), s.Namespace(), "site.created", s)
	return nil
}

// Update
func (s *Site) BeforeUpdate(previous *Site) error {
	client := netlify.NewFromNamespace(s.Context(), s.Namespace())
	nsite, err := client.UpdateSite(s.Netlify())
	if err != nil {
		return err
	}

	s.SetNetlify(nsite)

	return nil
}

func (s *Site) AfterUpdate(previous *Site) error {
	webhook.Emit(s.Context(), s.Namespace(), "site.spdated", s)
	return nil
}

// Delete
func (s *Site) BeforeDelete() error {
	client := netlify.NewFromNamespace(s.Context(), s.Namespace())
	if err := client.DeleteSite(s.Netlify()); err != nil {
		return err
	}
	return nil
}

func (s *Site) AfterDelete() error {
	webhook.Emit(s.Context(), s.Namespace(), "site.deleted", s)
	return nil
}
