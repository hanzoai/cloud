// Package form mounts forms (github.com/hanzoai/form) at /v1/form, the way apps/iam
// mounts hanzoai/iam: the module owns every route and every rule of a form, and
// this package hands it what only cloud knows.
//
//   - where records live: each org's Base, over the base plane (base_import,
//     base_upsert, base_get, base_list, base_delete), acting for the org named,
//     with the platform org's Base holding the public index;
//   - who is calling: the org the identity boundary validated, whether they
//     administer it, and a signed-in person's subject and address from their token;
//   - the caller's address and Sec-GPC/DNT, the anti-forgery verdict and the
//     deployment-wide rate window;
//   - the platform's settings of forms, stored as product `form`'s fleet
//     configuration and edited by the SuperAdmin ops beside it (settings.go);
//   - the sinks: the event plane (form_viewed, form_started, form_submitted), the
//     CRM (apps/crm FileForm, keyed by the CRM person id), the notify rail for a
//     forward or a welcome, and KMS for webhook signing secrets.
package form
