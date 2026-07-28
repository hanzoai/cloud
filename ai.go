package cloud

import (
	"context"

	tasksclient "github.com/hanzoai/tasks/pkg/sdk/client"
)

// Callbacks the ai module makes back into the host. Declared here, installed by
// apps/ — importing ai/object here would put ~1480 packages under every
// subsystem, and nothing here reads back from it.

// UsageEvent mirrors the ai module's payload. Separate on purpose: sharing the
// type would reintroduce the import.
type UsageEvent struct {
	Subject   string
	Namespace string
	USD       string // exact decimal USD ("0.00132"), never a rounded cent
	Currency  string
	Model     string
	Provider  string
	RequestID string
}

type (
	TierReaderFunc       func(ctx context.Context, subject, namespace string) (string, error)
	BalanceReaderFunc    func(ctx context.Context, subject, namespace, currency string) (int64, error)
	UsageRecorderFunc    func(ctx context.Context, u UsageEvent) error
	IngestDialerFunc     func(org string) (tasksclient.Client, error)
	RollingCapReaderFunc func(ctx context.Context, subject, namespace string) (bool, error)
)

var (
	tierReader       TierReaderFunc
	balanceReader    BalanceReaderFunc
	usageRecorder    UsageRecorderFunc
	ingestDialer     IngestDialerFunc
	rollingCapReader RollingCapReaderFunc
)

// nil means that subsystem isn't co-resident; apps/ leaves it uninstalled.
func TierReader() TierReaderFunc             { return tierReader }
func BalanceReader() BalanceReaderFunc       { return balanceReader }
func UsageRecorder() UsageRecorderFunc       { return usageRecorder }
func IngestDialer() IngestDialerFunc         { return ingestDialer }
func RollingCapReader() RollingCapReaderFunc { return rollingCapReader }

// SetRollingCapReader is the one EXPORTED setter here, and the deviation is
// forced: the four above are written directly by build.go/durable.go, which are
// inside this package, but the rolling cap is produced by clients/rollingcap —
// it imports clients/flags, which imports this package, so it can only ever live
// above that edge and needs a door. nil clears it (no cap installed).
func SetRollingCapReader(f RollingCapReaderFunc) { rollingCapReader = f }
