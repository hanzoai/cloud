// Package content is marketing content from draft to published, on every channel.
//
// The Hanzo agentic-marketing lane: the content loop (generate → CMS → review →
// approve → publish → distribute) built natively on the framework DocType
// engine, the ONE Go-native replacement for the bespoke karma Python scripts,
// multi-tenant for ANY brand (org = tenant, project = brand/site sub-scope).
//
// It follows the knowledge lane's proven shape — a framework MODULE (DocType
// fixtures + lifecycle hook) PLUS a thin control-plane subsystem — NOT a second
// store:
//
//   - The content model IS framework DocTypes in module "marketing" (this file):
//     Campaign, SocialPost, Asset. A "collection" is a DocType, a content item is a
//     framework document, media is an Attach URL, and the editorial lifecycle is the
//     ONE `status` Select field (lifecycle.go). All CRUD, tenancy, permissions, and
//     install are the framework's already-live generic surface (/v1/framework/*) —
//     content adds NO parallel CRUD.
//   - The control-plane (content.go) mounts /v1/content/* for the three things the
//     generic engine cannot be: the cross-DocType board (aggregate read), the
//     side-effecting lifecycle transition, and the generate/publish orchestration
//     that reaches out to zen5 (deps.AI), studio, and hanzoai/social.
//
// The marketing module's `status` is therefore the ONE publishable-content lifecycle
// in service. apps/cms declares a second content model (Page/Post/Article) on its own
// Draft/Published field, but no binary imports that package, so its init never runs,
// the "cms" module is never registered, and no org can install it.
package content
