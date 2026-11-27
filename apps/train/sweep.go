package train

// sweep.go — the hourly pass over every org this process owns: lapsed leases end,
// charges a report could not send are sent, what each payer keeps in the store is
// billed for the hour, uploads whose grants lapsed unconfirmed are deleted,
// unpublished objects past retention are deleted, and an org with nothing left is
// let go.
//
// EVERY PROCESS SWEEPS, EACH ITS OWN ORGS. An org's file lives on its owner, so a
// sweep that ran in one elected process would visit only that process's orgs on a
// sharded deployment; Owned picks the orgs, and the fenced ship before every debit
// settles a race over one.
//
// STORAGE IS BILLED BY THE GB-MONTH FOR AS LONG AS IT IS KEPT. Each payer of an
// org carries a watermark, the second through which its storage was billed; a
// pass charges the span since at the bytes the payer keeps now (cloud.RuntimeSweep:
// the watermark moves, the org's file ships, then the debit is emitted, so a span
// is billed once). Every stored object is charged, whatever produced it and
// however that job ended, from the pass after it is stored until it is deleted.
// Bytes granted and not confirmed are charged nothing: at most pendingCeiling of
// them, and deleted once their grants lapse.
//
// DELETION IS THE PLATFORM'S ACT, SO IT IS AUDITED. An object stored more than
// TRAIN_RETENTION_DAYS ago that is not published and that no job which has not
// ended reads is deleted, and so are an upload's bytes whose grants lapsed
// unconfirmed; each deletion is on the platform's audit trail before it is made,
// and a deployment with no trail deletes nothing.

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/account"
	"github.com/hanzoai/namespace"
)

// storageFloorMicros is $0.02 per GB-month: the object storage price the catalog
// publishes (@hanzo/plans services.json, "S3 / Storage").
// TestTheStorageFloorIsTheCatalogs pins it.
const storageFloorMicros = 20_000

const (
	// sweepEvery is how often every org is swept. A storage charge is (now −
	// watermark) at the bytes kept, so the cadence decides when money moves and how
	// fresh the size is, never what a span costs.
	sweepEvery = time.Hour
	// storageMeter names the GB-month in the meter authority and on the invoice.
	storageMeter = "storage-gb-month"
	// gb is the unit the catalog prices storage in: a decimal gigabyte.
	gb int64 = 1_000_000_000
	// hoursPerMonth is a 30-day month.
	hoursPerMonth int64 = 30 * 24
)

// storageRate is the published price of one GB-month, in micro-USD.
func storageRate(ctx context.Context) int64 {
	return cloud.RateMicros(ctx, "train", storageMeter, storageFloorMicros)
}

// monthMicros prices keeping bytes for a month at perGBMonth micro-USD a GB-month,
// rounded half up: an hour at hoursPerMonth times the price.
func monthMicros(bytes, perGBMonth int64) int64 {
	return hourMicros(bytes, perGBMonth*hoursPerMonth)
}

// hourMicros prices keeping bytes for one hour at perGBMonth micro-USD a
// GB-month, rounded half up. Big integers: bytes times a price passes int64 at a
// few hundred terabytes.
func hourMicros(bytes, perGBMonth int64) int64 {
	if bytes <= 0 || perGBMonth <= 0 {
		return 0
	}
	n := new(big.Int).Mul(big.NewInt(bytes), big.NewInt(perGBMonth))
	d := big.NewInt(gb * hoursPerMonth)
	n.Add(n, new(big.Int).Rsh(d, 1))
	return n.Quo(n, d).Int64()
}

// run sweeps every sweepEvery until life ends.
func run(s *cloud.Service[state], life context.Context) {
	cloud.SayDurability(s.Log, s.Base, "train meter")
	o := ops{s: s}
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-life.Done():
			return
		case <-t.C:
			o.sweep(life, unix())
		}
	}
}

// sweep is one pass over the orgs this process owns.
func (o ops) sweep(ctx context.Context, now int64) {
	perGB := storageRate(ctx)
	_ = o.s.State.stores.Each(func(ns namespace.Namespace, st *store, err error) {
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			o.s.Log.Warn("train sweep: open store", "namespace", ns, "err", err)
			return
		}
		if !o.s.State.stores.Owned(ns) {
			return
		}
		began := time.Now()
		org := ns.ID()
		if err := st.expire(ctx, now, o.s.State.ttl); err != nil {
			o.s.Log.Warn("train sweep: expire", "org", org, "err", err)
			return
		}
		if cs, err := st.owed(ctx); err == nil && len(cs) > 0 {
			if err := o.settle(ctx, st, org); err != nil {
				o.s.Log.Warn("train sweep: charges owed not sent; the next ship sends them", "org", org, "err", err)
			}
		}
		if err := o.bill(ctx, ns, st, org, now, perGB); err != nil {
			o.s.Log.Warn("train sweep: storage claimed and not billed (the watermark is not durable, so the next owner bills it)", "org", org, "err", err)
		}
		if err := o.reap(ctx, st, org, now); err != nil {
			o.s.Log.Warn("train sweep: lapsed uploads stopped early", "org", org, "err", err)
		}
		if err := o.retain(ctx, st, org, now); err != nil {
			o.s.Log.Warn("train sweep: retention stopped early", "org", org, "err", err)
		}
		if err := o.lapseCaptures(ctx, st, org, now); err != nil {
			o.s.Log.Warn("train sweep: captures past their days stopped early", "org", org, "err", err)
		}
		if idle, err := st.idle(ctx); err == nil && idle {
			o.s.State.stores.Settle(ns, began)
		}
	})
}

// bill charges each payer of the org the span since it was last billed, at the
// bytes it keeps now.
func (o ops) bill(ctx context.Context, ns namespace.Namespace, st *store, org string, now, perGB int64) error {
	hs, err := st.holdings(ctx)
	if err != nil {
		return err
	}
	rows := make([]cloud.Running, 0, len(hs))
	for _, h := range hs {
		rows = append(rows, cloud.Running{
			ID:        "storage:" + h.Payer,
			Payer:     payerOf(org, h.Payer).String(),
			Model:     storageMeter,
			MeteredAt: h.MeteredAt,
			Rate:      hourMicros(h.Bytes, perGB),
		})
	}
	advance := func(ctx context.Context, id string, was, at int64) (bool, error) {
		return st.meter(ctx, strings.TrimPrefix(id, "storage:"), was, at)
	}
	_, err = cloud.RuntimeSweep(ctx, rows, now, advance,
		func() (bool, error) { return o.s.State.stores.Sync(ns) },
		func(payer string, u meter.Usage) {
			// Summed under the app's own service, which a train spend cap reads.
			u.Service = ""
			o.s.State.debit(account.Parse(payer), "storage", u)
		})
	return err
}

// retain deletes the org's objects past retention that nothing keeps, each on
// the audit trail first.
func (o ops) retain(ctx context.Context, st *store, org string, now int64) error {
	if o.s.State.retain <= 0 || o.s.State.audit == nil {
		return nil
	}
	shas, err := st.stale(ctx, now-o.s.State.retain)
	if err != nil {
		return err
	}
	for _, sum := range shas {
		ob, err := st.object(ctx, sum)
		if err != nil {
			return err
		}
		if err := o.deleting(ctx, org, sum, "train.artifact.retain", "unpublished past the retention period",
			map[string]any{"stored": ob.Stored, "retain_seconds": o.s.State.retain}); err != nil {
			return err
		}
		if err := o.forget(ctx, st, org, sum); err != nil {
			return err
		}
		o.s.Log.Info("train artifact deleted by retention", "org", org, "sha256", sum, "stored", ob.Stored)
	}
	return nil
}

// reap deletes the bytes of uploads whose every grant lapsed unconfirmed: the
// multipart uploads are aborted, the object is removed, and the registrations
// read deleted — or stored, where the org already keeps an object of those bytes
// (under another registration, or stored before objects were sealed), which is
// left as it is. Each deletion is on the audit trail first.
func (o ops) reap(ctx context.Context, st *store, org string, now int64) error {
	if o.s.State.audit == nil {
		return nil
	}
	shas, err := st.lapsed(ctx, now-int64(grantTTL/time.Second))
	if err != nil {
		return err
	}
	for _, sum := range shas {
		_, err := st.object(ctx, sum)
		if err != nil && !errors.Is(err, errNotFound) {
			return err
		}
		held := err == nil
		ids, err := st.uploadIDs(ctx, sum)
		if err != nil {
			return err
		}
		if held {
			if err := o.s.State.objects.abort(ctx, key(org, sum), ids); err != nil {
				return err
			}
		} else {
			if err := o.deleting(ctx, org, sum, "train.artifact.lapse", "uploaded under a grant that lapsed unconfirmed",
				map[string]any{"grant_seconds": int64(grantTTL / time.Second)}); err != nil {
				return err
			}
			if err := o.s.State.objects.remove(ctx, key(org, sum), ids); err != nil {
				return err
			}
		}
		if err := st.lapse(ctx, sum, held); err != nil {
			return err
		}
		if err := o.ship(org); err != nil {
			return err
		}
		o.s.Log.Info("train upload lapsed unconfirmed", "org", org, "sha256", sum, "held", held)
	}
	return nil
}

// capturesAPass bounds the captures one pass deletes for one org.
const capturesAPass = 1000

// lapseCaptures deletes the decisions captured longer ago than the org keeps
// them that no correction names: the bytes first, then the rows, so a row never
// names bytes that are gone.
func (o ops) lapseCaptures(ctx context.Context, st *store, org string, now int64) error {
	if o.s.State.keepCaptures <= 0 {
		return nil
	}
	ds, err := st.expired(ctx, now-o.s.State.keepCaptures, capturesAPass)
	if err != nil {
		return err
	}
	for _, d := range ds {
		if err := o.s.State.objects.remove(ctx, captureKey(org, d), nil); err != nil {
			return err
		}
		if err := st.uncapture(ctx, d); err != nil {
			return err
		}
	}
	if len(ds) > 0 {
		o.s.Log.Info("train sweep: captures past their days deleted", "org", org, "decisions", len(ds))
	}
	return nil
}
