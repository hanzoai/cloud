// Copyright (c) Hanzo AI. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runner

import "fmt"

// AppConfig is the GitHub credential bag for one tenant: either a PAT
// (Token) or a GitHub App triple (AppID + AppInstallationID +
// AppPrivateKey), never both.
//
// This is the minimal projection of arc's
// apis/actions.github.com/v1alpha1/appconfig.AppConfig — only the four
// fields and the Validate method the host-role credential merge uses.
// The full upstream type also carries Secret/JSON constructors that
// depend on k8s.io/api; those are not needed here.
type AppConfig struct {
	AppID             string `json:"github_app_id"`
	AppInstallationID int64  `json:"github_app_installation_id"`
	AppPrivateKey     string `json:"github_app_private_key"`

	Token string `json:"github_token"`
}

// Validate rejects a credential bag that is empty or that ambiguously
// carries both a PAT and GitHub App credentials.
func (c *AppConfig) Validate() error {
	if c == nil {
		return fmt.Errorf("missing app config")
	}
	hasToken := len(c.Token) > 0
	hasGitHubAppAuth := c.hasGitHubAppAuth()
	if hasToken && hasGitHubAppAuth {
		return fmt.Errorf("both PAT and GitHub App credentials provided. should only provide one")
	}
	if !hasToken && !hasGitHubAppAuth {
		return fmt.Errorf("no credentials provided: either a PAT or GitHub App credentials should be provided")
	}
	return nil
}

func (c *AppConfig) hasGitHubAppAuth() bool {
	return len(c.AppID) > 0 && c.AppInstallationID > 0 && len(c.AppPrivateKey) > 0
}
