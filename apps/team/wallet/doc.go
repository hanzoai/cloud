// Package wallet embeds the built hanzo.team usage/wallet page — a SMALL
// @hanzo/ui@8 React static export (app/, Vite) — into the cloud binary, the
// same one-binary/one-origin precedent as the console (webui.go) and the tasks
// SPA (clients/tasks/ui). clients/team serves it session-gated at
// /v1/team/billing/ui/ (billing.go).
//
// dist/ is the committed Vite build. To refresh it:
//
//	cd clients/team/wallet/app && npm install && npm run build
//
// (build output lands in ../dist; commit it).
package wallet
