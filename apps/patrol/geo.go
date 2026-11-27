package patrol

import (
	"math"
	"sort"

	"github.com/hanzoai/cloud/apps/framework"
)

// geo.go is distance on a sphere, and nothing else.
//
// The document engine indexes by (org, doctype, name) and has no geo index and
// no range query, so a nearest-unit answer is a scan over one org's units. An
// estate runs dozens of them.

// earthKm is the mean earth radius the haversine is taken against.
const earthKm = 6371.0088

// callKmPerMinute is the speed a control room plans a response at: 40 km/h.
const callKmPerMinute = 40.0 / 60.0

// haversine returns the great-circle distance in kilometres between two
// coordinates given in degrees.
func haversine(aLat, aLon, bLat, bLon float64) float64 {
	p := math.Pi / 180
	dLat, dLon := (bLat-aLat)*p, (bLon-aLon)*p
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(aLat*p)*math.Cos(bLat*p)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthKm * math.Asin(math.Min(1, math.Sqrt(h)))
}

// closest is one unit and how far away it is.
type closest struct {
	unit string
	km   float64
	eta  int
}

// nearest returns the n units closest to (lat, lon). A unit that is available
// sorts ahead of every busy one at any distance — a controller asking for the
// nearest unit is asking for the nearest free one — and within each group the
// order is by distance. A unit with no position is left out.
func nearest(units []framework.Document, lat, lon float64, n int) []closest {
	type row struct {
		closest
		free bool
	}
	rows := make([]row, 0, len(units))
	for _, u := range units {
		ulat, ulon := floatField(u, "lat"), floatField(u, "lon")
		if ulat == 0 && ulon == 0 {
			continue
		}
		km := haversine(lat, lon, ulat, ulon)
		rows = append(rows, row{
			unit: u.Name, km: round(km, 2), eta: int(math.Ceil(km / callKmPerMinute)),
			free: strField(u, "state") == UnitAvailable,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].free != rows[j].free {
			return rows[i].free
		}
		return rows[i].km < rows[j].km
	})
	if n > 0 && len(rows) > n {
		rows = rows[:n]
	}
	out := make([]closest, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.closest)
	}
	return out
}

// round trims a float to d decimal places, so a distance reads as a distance.
func round(v float64, d int) float64 {
	p := math.Pow(10, float64(d))
	return math.Round(v*p) / p
}
