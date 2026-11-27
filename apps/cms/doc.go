// Package cms declares the Hanzo CMS content model as DocType fixtures on the
// framework engine (apps/framework). CMS is NOT a bespoke subsystem and mounts
// NO HTTP surface of its own: a "collection" IS a framework DocType in module
// "cms", a content entry IS a framework document, media IS an Attach-backed
// document, and publishing IS a status field. All CRUD, permissions, tenant
// isolation, and install are the framework's generic, already-live surface
// (/v1/framework/*). This package only DECLARES the fixtures and registers them
// with the engine at init — the ONE source of truth for the CMS content model,
// per-org on Base/SQLite.
//
// erp declares its model the same way, on the same engine.
package cms
