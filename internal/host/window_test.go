package host

import (
	"fmt"
	"time"

	cloud "github.com/hanzoai/cloud/sdk"

	"github.com/hanzoai/ai/address"
)

// fullSite leaves w with addr's /64 full for short and its /48 full for long, by
// spending the /48 from other /64s inside it.
func fullSite(w *cloud.Window, addr string, limit int, short, long time.Duration) {
	b := address.Buckets(addr)
	other := func(i int) []address.Bucket { return address.Buckets(fmt.Sprintf("2001:470:1f00:%x::1", 0x100+i)) }
	w.Spend(other(0), limit, long) // opens the /48 for long
	spent := 1
	for range limit {
		w.Spend(b, limit, short)
		spent++
	}
	for i := 1; spent < limit*b[1].Scale; i++ {
		w.Spend(other(i), limit, long)
		spent++
	}
}
