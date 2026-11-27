package event

import (
	"context"
	"fmt"
	"maps"
	"net"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/hanzoai/cloud/internal/consent"
	"github.com/hanzoai/cloud/sdk/geo"
	"github.com/zap-proto/zip"
	"go.opentelemetry.io/otel"
	attr "go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// visitors is the ingest stamp. The browser calls this endpoint itself, so the request's
// address, country and user agent are the visitor's own: they are stamped here, once,
// and never taken from the body. The address rides the bus beside the project key
// (never an attribute); the country and user agent are attributes every reader sees.
//
// It also drops what a developer's own machine sent (localhost, 127.0.0.0/8, ::1,
// 0.0.0.0, *.localhost, *.local) unless the project keeps them, and files a legacy
// `$exception` act as the error it always meant. It returns the events that remain and
// how many it dropped as local. Every other event is kept: the first-party stream is
// essential (consent.go), and consent only says what may be forwarded.
//
// And it says who sent them (Audience, for the site org owns): a program's events
// carry `bot: true` and the site's own team's `internal: true`, stamped here from the
// request so a client that predates the classifier is covered and none can clear it.
func visitors(c *zip.Ctx, org string, evs []CaptureEvent) ([]CaptureEvent, int) {
	// ClientIP, ClientCountry and ClientRegion read Cloudflare's word only from a Cloudflare hop.
	v := visit{
		ip: strings.TrimSpace(geo.IP(c)), country: geo.Country(c), region: geo.Region(c),
		ua: strings.TrimSpace(c.Header("User-Agent")), origin: c.Header("Origin"),
		refused: consent.OptedOut(c),
		from:    callerOf(c, org),
	}
	return stamp(evs, v)
}

// legacy reports whether an event comes from an @hanzo/event built before every
// visitor opted in (0.3.58): its stated consent may be one the client presumed, so it
// is forwarded under none.
func legacy(e CaptureEvent) bool {
	if strings.TrimSpace(e.Library) != "@hanzo/event" {
		return false
	}
	var major, minor, patch int
	if n, _ := fmt.Sscanf(strings.TrimSpace(e.LibraryVer), "%d.%d.%d", &major, &minor, &patch); n != 3 {
		return true
	}
	return major == 0 && (minor < 3 || (minor == 3 && patch < 58))
}

// visit is the request's own facts: what the edge attested of the visitor and what the
// browser sent. No body sets any of it.
type visit struct {
	ip, country, region, ua, origin string
	// refused is the browser's Global Privacy Control or Do Not Track signal.
	refused bool
	// from is who sent the request, for the site the events are filed under.
	from caller
}

// stamp is visitors over the facts it turns on. It also says what each event may be
// forwarded under: the grants the page stated, and nothing when it stated none or the
// browser refused (Global Privacy Control or Do Not Track), stamped on the event as
// `consent` so every forwarder reads the same answer. The event itself is kept either
// way: the stream is first party.
func stamp(evs []CaptureEvent, v visit) (out []CaptureEvent, local int) {
	ip, country, ua, origin := v.ip, v.country, v.ua, v.origin
	dec := Policy.Decide(country, v.region, v.refused)
	out = evs[:0:0]
	for _, e := range evs {
		if !e.keepLocal && fromLocal(e.URL, origin) {
			local++
			continue
		}
		stated := e.Properties["consent"]
		if legacy(e) {
			// A client built before every visitor opted in presumed a grant from the
			// visitor's clock; what it states is not a choice.
			stated = nil
		}
		grants := dec.Effective(stated)
		if strings.TrimSpace(e.Event) == "$exception" {
			if e.Error == nil {
				e.Error = legacyException(e.Properties)
			}
			if e.Error != nil {
				e.Type, e.Event = "error", ""
			}
		}
		e.ip = ip
		props := make(map[string]any, len(e.Properties)+2)
		maps.Copy(props, e.Properties)
		delete(props, "ip")
		if country != "" {
			props["country"] = country
		} else {
			delete(props, "country")
		}
		for _, k := range []string{"user_agent", "browser", "os", "device"} {
			delete(props, k)
		}
		if ua != "" {
			props["user_agent"] = ua
			b, o, d := device(ua)
			props["browser"], props["os"], props["device"] = b, o, d
		}
		if sc, ok := screenOf(props["screen"]); ok {
			props["screen"] = sc
		} else {
			delete(props, "screen")
		}
		// A backend's own statement (the org's secret key) is not a visit, so it is not
		// classified; its agent and address are the backend's.
		if !e.vouched {
			switch v.from.audience(e.networks) {
			case Agent:
				props[Agent] = true
				props["agent_family"] = v.from.agent
			case Bot:
				props[Bot] = true
			case Internal:
				props[Internal] = true
			}
		}
		props["consent"] = grants.String()
		props["consent_mode"] = dec.Mode
		if dec.Refused {
			props["refused"] = "1"
		} else {
			delete(props, "refused")
		}
		delete(props, "gpc")
		e.Properties = props
		out = append(out, e)
	}
	if local > 0 {
		observeLocal(local)
	}
	return out, local
}

// fromLocal reports whether the page an event came from is on the sender's own machine.
// The event's url names the page; the request's Origin answers for an event with none.
func fromLocal(rawURL, origin string) bool {
	for _, raw := range []string{rawURL, origin} {
		if h := hostOfURL(raw); h != "" {
			return localHost(h)
		}
	}
	return false
}

func hostOfURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.Contains(raw, "://") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func localHost(h string) bool {
	h = strings.ToLower(strings.Trim(strings.TrimSpace(h), "[]"))
	switch {
	case h == "localhost", h == "0.0.0.0", h == "::1":
		return true
	case strings.HasSuffix(h, ".localhost"), strings.HasSuffix(h, ".local"):
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

var (
	localOnce    sync.Once
	localCounter metric.Int64Counter
)

// observeLocal counts the events dropped as local. A counter, never a row.
func observeLocal(n int) {
	localOnce.Do(func() {
		localCounter, _ = otel.Meter("github.com/hanzoai/cloud/apps/event").Int64Counter("hanzo_ingest_local_dropped_total",
			metric.WithDescription("Events dropped at ingest because the page was on the sender's own machine."))
	})
	if localCounter != nil {
		localCounter.Add(context.Background(), int64(n), metric.WithAttributes(attr.String("reason", "local")))
	}
}

var (
	withheldOnce    sync.Once
	withheldCounter metric.Int64Counter
)

// observeWithheld counts the events not kept because their purpose was not granted.
func observeWithheld(n int) {
	withheldOnce.Do(func() {
		withheldCounter, _ = otel.Meter("github.com/hanzoai/cloud/apps/event").Int64Counter("hanzo_ingest_consent_withheld_total",
			metric.WithDescription("Events not kept at ingest because the visitor had not granted analytics."))
	})
	if withheldCounter != nil {
		withheldCounter.Add(context.Background(), int64(n), metric.WithAttributes(attr.String("reason", "analytics")))
	}
}

// device is the ONE device vocabulary every reader shares, parsed once here from the
// request's user agent: the browser family, the operating system, and the class of device
// (desktop, mobile or tablet). Order matters: each family's token appears inside a
// sibling's (Edge and Opera say Chrome, Chrome says Safari), so the most specific is tried first.
func device(ua string) (browser, os, class string) {
	l := strings.ToLower(ua)
	has := func(s ...string) bool {
		for _, t := range s {
			if strings.Contains(l, t) {
				return true
			}
		}
		return false
	}
	switch {
	case has("edg/", "edga/", "edgios/"):
		browser = "Edge"
	case has("opr/", "opera"):
		browser = "Opera"
	case has("samsungbrowser"):
		browser = "Samsung Internet"
	case has("firefox/", "fxios/"):
		browser = "Firefox"
	case has("chrome/", "crios/"):
		browser = "Chrome"
	case has("safari/"):
		browser = "Safari"
	default:
		browser = "Other"
	}
	switch {
	case has("iphone", "ipad", "ipod"):
		os = "iOS"
	case has("android"):
		os = "Android"
	case has("cros"):
		os = "ChromeOS"
	case has("windows"):
		os = "Windows"
	case has("mac os x", "macintosh"):
		os = "macOS"
	case has("linux", "x11"):
		os = "Linux"
	default:
		os = "Other"
	}
	switch {
	case has("ipad", "tablet") || (has("android") && !has("mobile")):
		class = "tablet"
	case has("mobi", "iphone", "ipod"):
		class = "mobile"
	default:
		class = "desktop"
	}
	return browser, os, class
}

// legacyException reads the error a `$exception` act carried in its properties, the shape
// @hanzo/event sent before 0.3.49: `$exception_list` (type, value, stacktrace frames), or
// the flat `$exception_type` and `$exception_message`. nil when the act carries neither,
// so it stays the act it was.
func legacyException(p map[string]any) *Exception {
	ex := &Exception{}
	if list, ok := p["$exception_list"].([]any); ok && len(list) > 0 {
		if first, ok := list[0].(map[string]any); ok {
			ex.Type, _ = first["type"].(string)
			ex.Message, _ = first["value"].(string)
			if st, ok := first["stacktrace"].(map[string]any); ok {
				frames, _ := st["frames"].([]any)
				for _, f := range frames {
					m, _ := f.(map[string]any)
					fr := Frame{}
					fr.Function, _ = firstString(m, "mangled_name", "function")
					fr.File, _ = firstString(m, "source", "filename", "file")
					if n, ok := m["line"].(float64); ok && n > 0 {
						fr.Line = uint32(n)
					}
					if n, ok := m["column"].(float64); ok && n > 0 {
						fr.Column = uint32(n)
					}
					ex.Frames = append(ex.Frames, fr)
				}
			}
		}
	}
	if ex.Type == "" {
		ex.Type, _ = p["$exception_type"].(string)
	}
	if ex.Message == "" {
		ex.Message, _ = p["$exception_message"].(string)
	}
	if h, ok := p["$exception_handled"].(bool); ok {
		ex.Handled = &h
	}
	if ex.Type == "" && ex.Message == "" {
		return nil
	}
	return ex
}

func firstString(m map[string]any, keys ...string) (string, bool) {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v, true
		}
	}
	return "", false
}

// screenOf reads the page's screen size as WxH: "1920x1080", "1920 × 1080" and
// "1920X1080" all become "1920x1080"; anything else is not a size.
func screenOf(v any) (string, bool) {
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	m := screenRe.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	return m[1] + "x" + m[2], true
}

var screenRe = regexp.MustCompile(`^\s*(\d{1,5})\s*[xX×]\s*(\d{1,5})\s*$`)
