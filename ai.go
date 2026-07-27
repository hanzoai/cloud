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
	TierReaderFunc    func(ctx context.Context, subject, namespace string) (string, error)
	BalanceReaderFunc func(ctx context.Context, subject, namespace, currency string) (int64, error)
	UsageRecorderFunc func(ctx context.Context, u UsageEvent) error
	IngestDialerFunc  func(org string) (tasksclient.Client, error)
)

var (
	tierReader    TierReaderFunc
	balanceReader BalanceReaderFunc
	usageRecorder UsageRecorderFunc
	ingestDialer  IngestDialerFunc
)

// nil means that subsystem isn't co-resident; apps/ leaves it uninstalled.
func TierReader() TierReaderFunc       { return tierReader }
func BalanceReader() BalanceReaderFunc { return balanceReader }
func UsageRecorder() UsageRecorderFunc { return usageRecorder }
func IngestDialer() IngestDialerFunc   { return ingestDialer }
