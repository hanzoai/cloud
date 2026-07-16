//go:build iam2_ldap

// Package iam2: LDAP directory support is opt-in via `-tags iam2_ldap`. hanzoiam/ldap
// links goldap (GPL-2.0); keeping it behind the tag keeps the DEFAULT cloud binary
// free of copyleft. Enable it only in a build you intend to distribute under GPL-2.0
// (or run LDAP as its own binary).
package iam2

import _ "github.com/hanzoiam/ldap" // self-registers; only compiled under -tags iam2_ldap
