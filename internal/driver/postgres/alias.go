package postgres

import (
	"github.com/hanzoai/cloud/sdk/locus"
)

// What the SDK's store package names, spelled here as this driver reads it.
type (
	Place       = locus.Place
	Mode        = locus.Mode
	Report      = locus.Report
	TableReport = locus.TableReport
	Querier     = locus.Querier
	Online      = locus.Online
	Files       = locus.Files
	Host        = locus.Host
)

const (
	OnFile   = locus.OnFile
	OnServer = locus.OnServer
	Never    = locus.Never
	Lazy     = locus.Lazy
	Sweep    = locus.Sweep
	Env      = locus.Env
	MoveEnv  = locus.MoveEnv
)

var (
	ErrMoving  = locus.ErrMoving
	ErrBackoff = locus.ErrBackoff
)

// DSN is the hanzo-sql this process keeps its stores on, "" when it keeps files.
func DSN() string { return locus.DSN() }

// MoveMode reads the move policy.
func MoveMode() (Mode, error) { return locus.MoveMode() }
