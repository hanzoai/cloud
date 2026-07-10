package billing

import (
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/meter"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

// dnsUsageRequest is the input for recording a DNS query usage batch.
type dnsUsageRequest struct {
	Zone      string `json:"zone"`
	User      string `json:"user"`
	Queries   int64  `json:"queries"`
	Errors    int64  `json:"errors"`
	Timestamp string `json:"timestamp"`
}

// dnsUsageResponse is returned after recording DNS usage.
type dnsUsageResponse struct {
	Recorded  bool  `json:"recorded"`
	Remaining int64 `json:"remaining"`
	Limit     int64 `json:"limit"`
}

// dnsUsageSummaryResponse is returned for DNS usage summary queries.
type dnsUsageSummaryResponse struct {
	Queries int64  `json:"queries"`
	Zones   int    `json:"zones"`
	Records int    `json:"records"`
	Limit   int64  `json:"limit"`
	Plan    string `json:"plan"`
}

const (
	dnsQueriesMeterEvent = "dns-queries"
	dnsDefaultPlanSlug   = "dns-free"
)

// RecordDNSUsage records a batch of DNS query usage for a zone owner.
// The zone's owner is looked up via the user field. Usage is checked against
// the plan's daily query limit.
//
//	POST /v1/dns/usage
func RecordDNSUsage(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	var req dnsUsageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	if req.Zone == "" {
		http.Fail(c, 400, "zone is required", nil)
		return
	}

	if req.User == "" {
		http.Fail(c, 400, "user is required", nil)
		return
	}

	if req.Queries <= 0 {
		c.JSON(200, dnsUsageResponse{Recorded: true, Remaining: -1, Limit: -1})
		return
	}

	// Resolve the user's DNS plan to determine limits.
	plan, dailyLimit := resolveDNSPlan(db, req.User)
	_ = plan

	// Calculate today's usage so far by querying meter events.
	todayStart := todayUTC()
	todayUsage := aggregateDNSUsage(db, req.User, todayStart, todayStart.Add(24*time.Hour))

	// Check limit. -1 means unlimited.
	if dailyLimit > 0 && todayUsage+req.Queries > dailyLimit {
		remaining := dailyLimit - todayUsage
		if remaining < 0 {
			remaining = 0
		}
		http.Fail(c, 429, fmt.Sprintf("daily DNS query limit exceeded: %d/%d", todayUsage+req.Queries, dailyLimit), nil)
		return
	}

	// Parse timestamp or default to now.
	ts := time.Now()
	if req.Timestamp != "" {
		if parsed, err := time.Parse(time.RFC3339, req.Timestamp); err == nil {
			ts = parsed
		}
	}

	// Find or create the dns-queries meter.
	meterId := ensureDNSMeter(db)

	// Record the meter event.
	evt := meter.NewEvent(db)
	evt.MeterId = meterId
	evt.UserId = req.User
	evt.Value = req.Queries
	evt.Timestamp = ts
	evt.Idempotency = fmt.Sprintf("dns:%s:%s:%d", req.User, req.Zone, ts.UnixNano())
	evt.Dimensions = Map{
		"zone":   req.Zone,
		"errors": req.Errors,
	}

	if err := evt.Create(); err != nil {
		log.Error("Failed to create DNS meter event: %v", err, c)
		http.Fail(c, 500, "failed to record dns usage", err)
		return
	}

	remaining := int64(-1)
	if dailyLimit > 0 {
		remaining = dailyLimit - (todayUsage + req.Queries)
		if remaining < 0 {
			remaining = 0
		}
	}

	c.JSON(201, dnsUsageResponse{
		Recorded:  true,
		Remaining: remaining,
		Limit:     dailyLimit,
	})
}

// GetDNSUsageSummary returns a usage summary for DNS queries, zones, and records.
//
//	GET /v1/dns/usage/summary?user={owner/name}&period=day|month
func GetDNSUsageSummary(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	user := strings.TrimSpace(c.Query("user"))
	if user == "" {
		http.Fail(c, 400, "user query parameter is required", nil)
		return
	}

	period := strings.TrimSpace(c.DefaultQuery("period", "day"))

	var periodStart, periodEnd time.Time
	now := time.Now().UTC()

	switch period {
	case "month":
		periodStart = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		periodEnd = periodStart.AddDate(0, 1, 0)
	default:
		periodStart = todayUTC()
		periodEnd = periodStart.Add(24 * time.Hour)
	}

	queries := aggregateDNSUsage(db, user, periodStart, periodEnd)
	zones := countDNSZones(db, user, periodStart, periodEnd)

	planSlug, dailyLimit := resolveDNSPlan(db, user)

	c.JSON(200, gin.H{
		"user":    user,
		"period":  period,
		"queries": queries,
		"zones":   zones,
		"records": 0, // Records are tracked by the DNS service, not Commerce
		"limit":   dailyLimit,
		"plan":    planSlug,
	})
}

// ListDNSPlans returns the available DNS plans.
//
//	GET /v1/dns/plans
func ListDNSPlans(c *gin.Context) {
	c.JSON(200, dnsPlans)
}

// resolveDNSPlan looks up the user's active DNS subscription to determine
// their plan slug and daily query limit. Falls back to dns-free if no
// subscription is found.
func resolveDNSPlan(db *datastore.Datastore, user string) (planSlug string, dailyLimit int64) {
	// Look up active subscriptions with a dns-* plan.
	rootKey := db.NewKey("synckey", "", 1, nil)
	type subRecord struct {
		PlanId string `datastore:"PlanId"`
		Status string `datastore:"Status"`
	}

	subs := make([]*subRecord, 0)
	q := db.Query("subscription").Ancestor(rootKey).
		Filter("UserId=", user).
		Filter("Status=", "active")

	if _, err := q.GetAll(&subs); err == nil {
		for _, s := range subs {
			if strings.HasPrefix(s.PlanId, "dns-") {
				planSlug = s.PlanId
				break
			}
		}
	}

	if planSlug == "" {
		planSlug = dnsDefaultPlanSlug
	}

	p := lookupPlan(planSlug)
	if p != nil && p.Limits != nil && p.Limits.QueriesPerDay != nil {
		limit := int64(*p.Limits.QueriesPerDay)
		if limit < 0 {
			return planSlug, -1 // unlimited
		}
		return planSlug, limit
	}

	// Fallback: dns-free defaults
	return planSlug, 10000
}

// aggregateDNSUsage sums DNS query meter events for a user within a time range.
func aggregateDNSUsage(db *datastore.Datastore, user string, start, end time.Time) int64 {
	rootKey := db.NewKey("synckey", "", 1, nil)
	events := make([]*meter.MeterEvent, 0)
	q := meter.QueryEvents(db).Ancestor(rootKey).
		Filter("UserId=", user).
		Filter("Timestamp>=", start).
		Filter("Timestamp<", end)

	if _, err := q.GetAll(&events); err != nil {
		return 0
	}

	// Only count events that belong to the dns-queries meter.
	meterId := findDNSMeterId(db)
	var total int64
	for _, evt := range events {
		if evt.MeterId == meterId {
			total += evt.Value
		}
	}
	return total
}

// countDNSZones counts distinct zones from DNS meter events for a user within a period.
func countDNSZones(db *datastore.Datastore, user string, start, end time.Time) int {
	rootKey := db.NewKey("synckey", "", 1, nil)
	events := make([]*meter.MeterEvent, 0)
	q := meter.QueryEvents(db).Ancestor(rootKey).
		Filter("UserId=", user).
		Filter("Timestamp>=", start).
		Filter("Timestamp<", end)

	if _, err := q.GetAll(&events); err != nil {
		return 0
	}

	meterId := findDNSMeterId(db)
	zones := make(map[interface{}]struct{})
	for _, evt := range events {
		if evt.MeterId == meterId && evt.Dimensions != nil {
			if z, ok := evt.Dimensions["zone"]; ok {
				zones[z] = struct{}{}
			}
		}
	}
	return len(zones)
}

// ensureDNSMeter finds or creates the dns-queries meter and returns its ID.
func ensureDNSMeter(db *datastore.Datastore) string {
	if id := findDNSMeterId(db); id != "" {
		return id
	}

	m := meter.New(db)
	m.Name = "DNS Queries"
	m.EventName = dnsQueriesMeterEvent
	m.AggregationType = meter.AggSum
	m.Currency = "usd"
	m.Dimensions = []string{"zone", "errors"}

	if err := m.Create(); err != nil {
		log.Error("Failed to create dns-queries meter: %v", err)
		return ""
	}

	return m.Id()
}

// findDNSMeterId looks up the dns-queries meter ID from the datastore.
func findDNSMeterId(db *datastore.Datastore) string {
	rootKey := db.NewKey("synckey", "", 1, nil)
	meters := make([]*meter.Meter, 0, 1)
	q := meter.Query(db).Ancestor(rootKey).
		Filter("EventName=", dnsQueriesMeterEvent).
		Limit(1)

	if _, err := q.GetAll(&meters); err == nil && len(meters) > 0 {
		return meters[0].Id()
	}
	return ""
}

// todayUTC returns the start of the current UTC day.
func todayUTC() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}
