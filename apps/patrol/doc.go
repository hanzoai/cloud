// Package patrol stands watch over an estate: sites, units, tours, keys,
// cameras, and the incidents that interrupt them.
//
// # Two planes, one engine
//
// A site IS a framework document in module "patrol"; an incident's lifecycle IS
// a state field; a unit, a tour, a checkpoint, a key, a camera, an activation
// are framework documents. So the whole CRUD plane — edit a site, register a
// camera, list movements — is the framework's generic, role-gated surface
// (/v1/framework/patrol.*) and the same DocType renderer the console draws CMS
// and ERP with. That plane needs no code here beyond the fixtures.
//
// /v1/patrol (subsystem.go) adds the four things that surface cannot serve: the
// operational reads shaped for one screen, the incident state machine
// (incident.go), alarm intake (alarm.go), and the live feed (feed.go). It owns
// no store — every read and write goes through the framework in-process API.
//
// # Tenancy
//
// There is no tenant column. The framework opens one SQLite file per org and
// every call takes the org as its second argument, so the tenant boundary is
// the storage boundary.
//
// # Geography
//
// lat/lon are plain floats and distance is a haversine (geo.go). The engine has
// no geo index and no range query; an estate is hundreds of sites and dozens of
// units, so nearest-unit is a linear scan and every list route is bounded.
package patrol
