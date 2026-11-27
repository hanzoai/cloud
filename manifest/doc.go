// Package manifest is what the light host knows about the fleet: for every
// subsystem that ships as its own binary, its name, the absolute paths it
// answers, and whether it must already be running when the first request
// arrives.
//
// It deliberately imports NOTHING but zip. That is the whole point — cmd/cloud
// links this package and zip and stops, so adding the 70th subsystem does not
// grow the host's build by one package. What an app DOES belongs to the app's
// own binary; where it lives and what it answers is all the router needs.
//
// apps.go is the single source of truth, and it is HAND-AUTHORED — see its own
// header. Apps is the set and its load order; the per-app flags sit on the row.
// A change is made there. plugin/gen-app-cmds reads it the other way, validating
// that every row has a plugin/<name> and every plugin/<name> has a row.
package manifest
