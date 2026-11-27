"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
};
var __export = (target, all) => {
  for (var name in all)
    __defProp(target, name, { get: all[name], enumerable: !0 });
}, __copyProps = (to, from, except, desc) => {
  if (from && typeof from == "object" || typeof from == "function")
    for (let key of __getOwnPropNames(from))
      !__hasOwnProp.call(to, key) && key !== except && __defProp(to, key, { get: () => from[key], enumerable: !(desc = __getOwnPropDesc(from, key)) || desc.enumerable });
  return to;
};
var __toCommonJS = (mod) => __copyProps(__defProp({}, "__esModule", { value: !0 }), mod);

// ../ap/packages/pieces/community/hashi-corp-vault/src/index.ts
var index_exports = {};
__export(index_exports, {
  hashiCorpVault: () => hashiCorpVault
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/hashi-corp-vault/src/lib/actions/read-secret.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/hashi-corp-vault/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var markdownDescription = `
Connect to HashiCorp Vault to securely manage secrets.

**Authentication Methods:**
- **Token**: Use a Vault token directly
- **AppRole**: Use Role ID and Secret ID for machine authentication
`, hashiCorpVaultAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: markdownDescription,
  props: {
    url: import_pieces_framework.Property.ShortText({
      displayName: "Vault URL",
      description: "The URL of your HashiCorp Vault instance (e.g., https://vault.example.com:8200)",
      required: !0
    }),
    authMethod: import_pieces_framework.Property.StaticDropdown({
      displayName: "Authentication Method",
      description: "Choose how to authenticate with Vault",
      required: !0,
      defaultValue: "token",
      options: {
        options: [
          { label: "Token", value: "token" },
          { label: "AppRole", value: "appRole" }
        ]
      }
    }),
    token: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Vault Token",
      description: "Your Vault authentication token",
      required: !1
    }),
    roleId: import_pieces_framework.Property.ShortText({
      displayName: "Role ID",
      description: "The Role ID for AppRole authentication",
      required: !1
    }),
    secretId: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Secret ID",
      description: "The Secret ID for AppRole authentication",
      required: !1
    }),
    appRolePath: import_pieces_framework.Property.ShortText({
      displayName: "AppRole Mount Path",
      description: "The mount path of the AppRole auth method",
      required: !1,
      defaultValue: "approle"
    }),
    namespace: import_pieces_framework.Property.ShortText({
      displayName: "Namespace",
      description: "Vault namespace (Enterprise feature, leave empty if not using)",
      required: !1
    }),
    apiVersion: import_pieces_framework.Property.StaticDropdown({
      displayName: "KV Secrets Engine Version",
      description: "The version of the KV secrets engine",
      required: !0,
      defaultValue: "v2",
      options: {
        options: [
          { label: "Version 1 (KV v1)", value: "v1" },
          { label: "Version 2 (KV v2)", value: "v2" }
        ]
      }
    })
  },
  validate: async ({ auth }) => {
    var _a;
    if (auth.authMethod === "token") {
      if (!auth.token)
        return {
          valid: !1,
          error: "Vault Token is required when using Token authentication"
        };
    } else if (auth.authMethod === "appRole" && (!auth.roleId || !auth.secretId))
      return {
        valid: !1,
        error: "Role ID and Secret ID are required when using AppRole authentication"
      };
    try {
      let baseUrl = auth.url.replace(/\/$/, ""), token;
      if (auth.authMethod === "token") {
        if (!auth.token)
          throw new Error("Vault Token is required when using Token authentication");
        token = auth.token;
      } else {
        let appRolePath = auth.appRolePath || "approle", response = await import_pieces_common.httpClient.sendRequest({
          method: import_pieces_common.HttpMethod.POST,
          url: `${baseUrl}/v1/auth/${appRolePath}/login`,
          headers: __spreadValues({
            "Content-Type": "application/json"
          }, auth.namespace && { "X-Vault-Namespace": auth.namespace }),
          body: {
            role_id: auth.roleId,
            secret_id: auth.secretId
          }
        });
        if (!((_a = response.body.auth) != null && _a.client_token))
          return {
            valid: !1,
            error: "AppRole authentication failed: No token received"
          };
        token = response.body.auth.client_token;
      }
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${baseUrl}/v1/auth/token/lookup-self`,
        headers: __spreadValues({
          "X-Vault-Token": token
        }, auth.namespace && { "X-Vault-Namespace": auth.namespace })
      }), { valid: !0 };
    } catch (error) {
      return {
        valid: !1,
        error: `Connection failed: ${error instanceof Error ? error.message : "Unknown error"}`
      };
    }
  },
  required: !0
});

// ../ap/packages/pieces/community/hashi-corp-vault/src/lib/common/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
async function getVaultToken(auth) {
  var _a;
  let baseUrl = auth.props.url.replace(/\/$/, ""), namespace = auth.props.namespace || void 0, apiVersion = auth.props.apiVersion || "v2", token;
  if (auth.props.authMethod === "token") {
    if (!auth.props.token)
      throw new Error("Vault Token is required for token authentication");
    token = auth.props.token;
  } else {
    if (!auth.props.roleId || !auth.props.secretId)
      throw new Error("Role ID and Secret ID are required for AppRole authentication");
    let appRolePath = auth.props.appRolePath || "approle", loginUrl = `${baseUrl}/v1/auth/${appRolePath}/login`, request = {
      method: import_pieces_common2.HttpMethod.POST,
      url: loginUrl,
      headers: __spreadValues({
        "Content-Type": "application/json"
      }, namespace && { "X-Vault-Namespace": namespace }),
      body: {
        role_id: auth.props.roleId,
        secret_id: auth.props.secretId
      }
    };
    try {
      let response = await import_pieces_common2.httpClient.sendRequest(request);
      if (!((_a = response.body.auth) != null && _a.client_token))
        throw new Error("Failed to obtain token from AppRole login");
      token = response.body.auth.client_token;
    } catch (error) {
      let errorMessage = error instanceof Error ? error.message : "Unknown error";
      throw new Error(`AppRole authentication failed: ${errorMessage}`);
    }
  }
  return {
    token,
    baseUrl,
    namespace,
    apiVersion
  };
}
function buildVaultHeaders(token, namespace, customHeaders) {
  return __spreadValues(__spreadValues({
    "X-Vault-Token": token,
    "Content-Type": "application/json"
  }, namespace && { "X-Vault-Namespace": namespace }), customHeaders);
}
function buildSecretUrl(baseUrl, secretEngine, secretPath, apiVersion, operation) {
  let cleanPath = secretPath.replace(/^\/+|\/+$/g, "");
  if (apiVersion === "v2")
    switch (operation) {
      case "read":
      case "write":
        return `${baseUrl}/v1/${secretEngine}/data/${cleanPath}`;
      case "delete":
      case "list":
        return `${baseUrl}/v1/${secretEngine}/metadata/${cleanPath}`;
    }
  else
    return `${baseUrl}/v1/${secretEngine}/${cleanPath}`;
}

// ../ap/packages/pieces/community/hashi-corp-vault/src/lib/actions/read-secret.ts
var readSecret = (0, import_pieces_framework2.createAction)({
  auth: hashiCorpVaultAuth,
  name: "read_secret",
  displayName: "Read Secret",
  description: "Read a secret from HashiCorp Vault",
  audience: "both",
  aiMetadata: {
    description: "Fetch the key-value data stored at a secret path in a HashiCorp Vault secrets engine (mount). Use to retrieve credentials or config an agent needs at runtime. Requires the secrets-engine mount name and the path; on KV v2 engines an optional version number reads a specific historical version (0/omitted = latest). Read-only and idempotent; a missing path returns success:false rather than erroring.",
    idempotent: !0
  },
  props: {
    secretEngine: import_pieces_framework2.Property.ShortText({
      displayName: "Secret Engine",
      description: "The name of the secrets engine (mount path)",
      required: !0,
      defaultValue: "secret"
    }),
    secretPath: import_pieces_framework2.Property.ShortText({
      displayName: "Secret Path",
      description: "The path to the secret (e.g., myapp/database)",
      required: !0
    }),
    version: import_pieces_framework2.Property.Number({
      displayName: "Version",
      description: "Version of the secret to read (0 for latest, KV v2 only)",
      required: !1,
      defaultValue: 0
    })
  },
  async run(context) {
    var _a, _b, _c;
    let { secretEngine, secretPath, version } = context.propsValue, vaultAuth = await getVaultToken(context.auth), url = buildSecretUrl(
      vaultAuth.baseUrl,
      secretEngine,
      secretPath,
      vaultAuth.apiVersion,
      "read"
    );
    vaultAuth.apiVersion === "v2" && version && version > 0 && (url += `?version=${version}`);
    try {
      let response = await import_pieces_common3.httpClient.sendRequest({
        method: import_pieces_common3.HttpMethod.GET,
        url,
        headers: buildVaultHeaders(vaultAuth.token, vaultAuth.namespace)
      });
      return vaultAuth.apiVersion === "v2" ? {
        success: !0,
        data: ((_a = response.body.data) == null ? void 0 : _a.data) || {},
        metadata: ((_b = response.body.data) == null ? void 0 : _b.metadata) || {},
        lease_duration: response.body.lease_duration,
        renewable: response.body.renewable
      } : {
        success: !0,
        data: response.body.data || {},
        lease_duration: response.body.lease_duration,
        renewable: response.body.renewable
      };
    } catch (error) {
      if (error && typeof error == "object" && "response" in error && ((_c = error.response) == null ? void 0 : _c.status) === 404)
        return {
          success: !1,
          data: {},
          path: secretPath,
          message: "Secret not found at this path"
        };
      let errorMessage = error instanceof Error ? error.message : "Unknown error";
      throw new Error(`Failed to read secret: ${errorMessage}`);
    }
  }
});

// ../ap/packages/pieces/community/hashi-corp-vault/src/lib/actions/write-secret.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var writeSecret = (0, import_pieces_framework3.createAction)({
  auth: hashiCorpVaultAuth,
  name: "write_secret",
  displayName: "Write Secret",
  description: "Write a secret to HashiCorp Vault",
  audience: "both",
  aiMetadata: {
    description: "Store key-value data at a secret path in a HashiCorp Vault secrets engine (mount), creating the secret or overwriting the data there. Use to persist credentials or config the flow generated. Requires the secrets-engine mount name, the path, and the secret data as a JSON object. On KV v2 engines this records a new secret version each call, so it is not idempotent.",
    idempotent: !1
  },
  props: {
    secretEngine: import_pieces_framework3.Property.ShortText({
      displayName: "Secret Engine",
      description: "The name of the secrets engine (mount path)",
      required: !0,
      defaultValue: "secret"
    }),
    secretPath: import_pieces_framework3.Property.ShortText({
      displayName: "Secret Path",
      description: "The path to store the secret (e.g., myapp/database)",
      required: !0
    }),
    secretData: import_pieces_framework3.Property.Json({
      displayName: "Secret Data",
      description: "The secret data to store as JSON",
      required: !0,
      defaultValue: {
        username: "myuser",
        password: "mypassword"
      }
    })
  },
  async run(context) {
    let { secretEngine, secretPath, secretData } = context.propsValue, vaultAuth = await getVaultToken(context.auth), url = buildSecretUrl(
      vaultAuth.baseUrl,
      secretEngine,
      secretPath,
      vaultAuth.apiVersion,
      "write"
    ), requestBody;
    vaultAuth.apiVersion === "v2" ? requestBody = { data: secretData } : requestBody = secretData;
    try {
      let response = await import_pieces_common4.httpClient.sendRequest({
        method: import_pieces_common4.HttpMethod.POST,
        url,
        headers: buildVaultHeaders(vaultAuth.token, vaultAuth.namespace),
        body: requestBody
      });
      return __spreadValues({
        success: !0,
        path: secretPath
      }, response.body.data && { metadata: response.body.data });
    } catch (error) {
      let errorMessage = error instanceof Error ? error.message : "Unknown error";
      throw new Error(`Failed to write secret: ${errorMessage}`);
    }
  }
});

// ../ap/packages/pieces/community/hashi-corp-vault/src/lib/actions/delete-secret.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var deleteSecret = (0, import_pieces_framework4.createAction)({
  auth: hashiCorpVaultAuth,
  name: "delete_secret",
  displayName: "Delete Secret",
  description: "Delete a secret from HashiCorp Vault",
  audience: "both",
  aiMetadata: {
    description: "Remove the secret stored at a path in a HashiCorp Vault secrets engine (mount); on KV v2 engines this deletes the metadata and all versions at that path. Use to revoke or clean up credentials. Requires the secrets-engine mount name and the path. Idempotent: deleting an already-missing path still reports success rather than erroring.",
    idempotent: !0
  },
  props: {
    secretEngine: import_pieces_framework4.Property.ShortText({
      displayName: "Secret Engine",
      description: "The name of the secrets engine (mount path)",
      required: !0,
      defaultValue: "secret"
    }),
    secretPath: import_pieces_framework4.Property.ShortText({
      displayName: "Secret Path",
      description: "The path to the secret to delete (e.g., myapp/database)",
      required: !0
    })
  },
  async run(context) {
    var _a;
    let { secretEngine, secretPath } = context.propsValue, vaultAuth = await getVaultToken(context.auth), url = buildSecretUrl(
      vaultAuth.baseUrl,
      secretEngine,
      secretPath,
      vaultAuth.apiVersion,
      "delete"
    );
    try {
      return await import_pieces_common5.httpClient.sendRequest({
        method: import_pieces_common5.HttpMethod.DELETE,
        url,
        headers: buildVaultHeaders(vaultAuth.token, vaultAuth.namespace)
      }), {
        success: !0,
        deleted: !0,
        path: secretPath
      };
    } catch (error) {
      if (error && typeof error == "object" && "response" in error && ((_a = error.response) == null ? void 0 : _a.status) === 404)
        return {
          success: !0,
          deleted: !1,
          path: secretPath,
          message: "Secret not found at this path (may already be deleted)"
        };
      let errorMessage = error instanceof Error ? error.message : "Unknown error";
      throw new Error(`Failed to delete secret: ${errorMessage}`);
    }
  }
});

// ../ap/packages/pieces/community/hashi-corp-vault/src/lib/actions/list-secrets.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var listSecrets = (0, import_pieces_framework5.createAction)({
  auth: hashiCorpVaultAuth,
  name: "list_secrets",
  displayName: "List Secrets",
  description: "List secrets at a path in HashiCorp Vault",
  audience: "both",
  aiMetadata: {
    description: "List the secret keys and sub-paths directly under a path in a HashiCorp Vault secrets engine (mount). Use to discover what secrets exist before reading them. Requires the secrets-engine mount name; the path is optional and defaults to the engine root, so leaving it empty lists the top level. Read-only and idempotent; returns only immediate child keys, not nested values, and an empty/missing path returns no keys rather than erroring.",
    idempotent: !0
  },
  props: {
    secretEngine: import_pieces_framework5.Property.ShortText({
      displayName: "Secret Engine",
      description: "The name of the secrets engine (mount path)",
      required: !0,
      defaultValue: "secret"
    }),
    listPath: import_pieces_framework5.Property.ShortText({
      displayName: "Path",
      description: "The path to list secrets from (e.g., myapp/)",
      required: !1,
      defaultValue: ""
    })
  },
  async run(context) {
    var _a, _b;
    let { secretEngine, listPath } = context.propsValue, vaultAuth = await getVaultToken(context.auth), cleanPath = (listPath || "").replace(/^\/+|\/+$/g, ""), url = buildSecretUrl(
      vaultAuth.baseUrl,
      secretEngine,
      cleanPath,
      vaultAuth.apiVersion,
      "list"
    ) + "?list=true";
    try {
      return {
        success: !0,
        keys: ((_a = (await import_pieces_common6.httpClient.sendRequest({
          method: import_pieces_common6.HttpMethod.GET,
          url,
          headers: buildVaultHeaders(vaultAuth.token, vaultAuth.namespace)
        })).body.data) == null ? void 0 : _a.keys) || [],
        path: listPath || "/"
      };
    } catch (error) {
      if (error && typeof error == "object" && "response" in error && ((_b = error.response) == null ? void 0 : _b.status) === 404)
        return {
          success: !0,
          keys: [],
          path: listPath || "/",
          message: "No secrets found at this path"
        };
      let errorMessage = error instanceof Error ? error.message : "Unknown error";
      throw new Error(`Failed to list secrets: ${errorMessage}`);
    }
  }
});

// ../ap/packages/pieces/community/hashi-corp-vault/src/index.ts
var hashiCorpVault = (0, import_pieces_framework6.createPiece)({
  displayName: "HashiCorp Vault",
  description: "Securely manage secrets and sensitive data with HashiCorp Vault",
  auth: hashiCorpVaultAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/hashi-corp-vault.png",
  authors: ["onyedikachi-david"],
  categories: [import_pieces_framework7.PieceCategory.DEVELOPER_TOOLS],
  actions: [
    readSecret,
    writeSecret,
    deleteSecret,
    listSecrets,
    (0, import_pieces_common7.createCustomApiCallAction)({
      baseUrl: (auth) => ((auth == null ? void 0 : auth.props.url) || "").replace(/\/$/, ""),
      auth: hashiCorpVaultAuth,
      authMapping: async (auth) => {
        var _a;
        let token;
        if (auth.props.authMethod === "token")
          token = auth.props.token || "";
        else {
          let appRolePath = auth.props.appRolePath || "approle", baseUrl = (auth.props.url || "").replace(/\/$/, "");
          token = ((_a = (await import_pieces_common7.httpClient.sendRequest({
            method: import_pieces_common7.HttpMethod.POST,
            url: `${baseUrl}/v1/auth/${appRolePath}/login`,
            headers: __spreadValues({
              "Content-Type": "application/json"
            }, auth.props.namespace && { "X-Vault-Namespace": auth.props.namespace }),
            body: {
              role_id: auth.props.roleId || "",
              secret_id: auth.props.secretId || ""
            }
          })).body.auth) == null ? void 0 : _a.client_token) || "";
        }
        return __spreadValues({
          "X-Vault-Token": token
        }, auth.props.namespace && { "X-Vault-Namespace": auth.props.namespace });
      }
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  hashiCorpVault
});
