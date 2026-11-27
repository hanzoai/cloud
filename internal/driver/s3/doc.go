// Package s3 is the Hanzo S3 driver for sdk/object: the shared object store a
// production deployment keeps its files in. Linking it registers it; the shared
// admin credential (S3_ADMIN_*) then selects it, and without that credential a
// process keeps its files on local disk where the SDK allows it.
//
// The store is the S3 gateway (s3.hanzo.svc:9000), which speaks the S3 API, so
// hanzos3/go is the client. Control and data go to the internal admin endpoint; a
// SEPARATE public-host client only signs the links a browser follows, since a
// signature is pure arithmetic over the client's endpoint and the signed host must
// be the one the browser can reach.
//
// Admin is also exported for the one consumer whose job IS the S3 surface
// (apps/s3, the /v1/s3 product) and for the few that hand the credential to
// another process; everything else goes through sdk/object.
package s3
