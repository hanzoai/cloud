// commerce.zap — ZAP schema for the Commerce subsystem.
//
// HIP-0106 unified-binary contract: every Hanzo subsystem ships its
// inter-subsystem call surface as a .zap schema; zapc generates the
// in-process + ZAP-RPC client/server pair, so cloud co-resident and
// split-deploy paths share one source of truth.
//
// Commerce is a LIGHT ROUTER, NOT in PCI-DSS scope. PAN never crosses
// this boundary; the Payments + Vault services do.

package commerce.v1;

service Health {
  // Check returns OK when the commerce mount is wired and the
  // underlying gin engine is responding.
  Check(HealthRequest) -> HealthResponse;
}

message HealthRequest {}

message HealthResponse {
  string status = 1; // "ok"
  string version = 2; // commerce.Version
}

service Tenant {
  // Get returns the per-org tenant configuration (branding + enabled
  // payment methods). Tokens only — never credentials. Mirrors
  // GET /v1/commerce/tenant.
  Get(TenantRequest) -> TenantConfig;
}

message TenantRequest {
  string org_id = 1;
}

message TenantConfig {
  string org_id = 1;
  string brand = 2;
  repeated string enabled_methods = 3;
}
