"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __hasOwnProp = Object.prototype.hasOwnProperty;
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

// ../ap/packages/pieces/community/intruder/src/index.ts
var index_exports = {};
__export(index_exports, {
  intruder: () => intruder
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/intruder/src/lib/actions/add-target.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/intruder/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/intruder/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.intruder.io/v1";
async function makeRequest(access_token, method, path, body) {
  try {
    let url = `${BASE_URL}${path}`;
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url,
      headers: {
        Authorization: `Bearer ${access_token}`,
        "Content-Type": "application/json"
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/intruder/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), intruderAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Intruder API Key",
  description: "API Key for Intruder. Get it from your Intruder account.",
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await makeRequest(auth, import_pieces_common2.HttpMethod.GET, "/health"), {
        valid: !0
      };
    } catch (err) {
      return {
        valid: !1,
        error: "Connection failed. Please check your credentials and try again."
      };
    }
  }
});

// ../ap/packages/pieces/community/intruder/src/lib/actions/add-target.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), addTarget = (0, import_pieces_framework2.createAction)({
  auth: intruderAuth,
  name: "addTarget",
  displayName: "Add Target",
  description: "Add a new target to your Intruder account",
  audience: "both",
  aiMetadata: {
    description: "Registers a new scan target (a domain or IP address) in the Intruder account so it can be included in future vulnerability scans, optionally tagging it for organization. Use when an agent needs to onboard an asset before scanning it. Not idempotent: each call attempts to create another target for the given address.",
    idempotent: !1
  },
  props: {
    address: import_pieces_framework2.Property.ShortText({
      displayName: "Target Address",
      description: "The address or domain of the target (e.g., example.com, 192.168.1.1)",
      required: !0
    }),
    tags: import_pieces_framework2.Property.Array({
      displayName: "Tags",
      description: "Tags to organize and filter targets",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let body = {
      address: propsValue.address
    };
    return propsValue.tags && propsValue.tags.length > 0 && (body.tags = propsValue.tags), await makeRequest(
      auth.secret_text,
      import_pieces_common3.HttpMethod.POST,
      "/targets/",
      body
    );
  }
});

// ../ap/packages/pieces/community/intruder/src/lib/actions/start-scan.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common"), startScan = (0, import_pieces_framework3.createAction)({
  auth: intruderAuth,
  name: "startScan",
  displayName: "Start Scan",
  description: "Start a new scan. Optionally specify target addresses and/or tag names. If no targets or tags are provided, the scan will run on all targets.",
  audience: "both",
  aiMetadata: {
    description: "Launches a new vulnerability scan in Intruder. Operates in two modes: when target addresses and/or tag names are supplied it scans only the matching targets, and when both are left empty it scans all targets in the account. Use when an agent needs to actively assess assets for security issues. Not idempotent: each call kicks off a separate scan run.",
    idempotent: !1
  },
  props: {
    target_addresses: import_pieces_framework3.Property.Array({
      displayName: "Target Addresses",
      description: "Target addresses to scan (e.g., example.com, 192.168.1.1). Leave empty to scan all targets.",
      required: !1
    }),
    tag_names: import_pieces_framework3.Property.Array({
      displayName: "Tag Names",
      description: "Tag names to filter targets. Leave empty to scan all targets.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let body = {};
    return propsValue.target_addresses && propsValue.target_addresses.length > 0 && (body.target_addresses = propsValue.target_addresses), propsValue.tag_names && propsValue.tag_names.length > 0 && (body.tag_names = propsValue.tag_names), await makeRequest(
      auth.secret_text,
      import_pieces_common4.HttpMethod.POST,
      "/scans/",
      Object.keys(body).length > 0 ? body : void 0
    );
  }
});

// ../ap/packages/pieces/community/intruder/src/lib/actions/search-for-a-target.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common"), searchForATarget = (0, import_pieces_framework4.createAction)({
  auth: intruderAuth,
  name: "searchForATarget",
  displayName: "Search For a Target",
  description: "Search for targets by address",
  audience: "both",
  aiMetadata: {
    description: "Looks up registered targets in the Intruder account that match a given address or domain. Use when an agent needs to find an existing target or confirm one is registered before scanning. The address is required. Idempotent: a read-only lookup that does not modify any data.",
    idempotent: !0
  },
  props: {
    address: import_pieces_framework4.Property.ShortText({
      displayName: "Target Address",
      description: "The address or domain to search for (e.g., example.com, 192.168.1.1)",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    return await makeRequest(
      auth.secret_text,
      import_pieces_common5.HttpMethod.GET,
      `/targets/?address=${encodeURIComponent(propsValue.address)}`
    );
  }
});

// ../ap/packages/pieces/community/intruder/src/lib/actions/search-for-an-issue.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common"), searchForAnIssue = (0, import_pieces_framework5.createAction)({
  auth: intruderAuth,
  name: "searchForAnIssue",
  displayName: "Search For an Issue",
  description: "Search for issues with optional filters",
  audience: "both",
  aiMetadata: {
    description: "Lists vulnerability issues detected by Intruder, optionally narrowing by issue IDs, target addresses, tag names, severity, or snoozed state; with no filters it returns all issues. Use when an agent needs to review or triage discovered security findings. Idempotent: a read-only search that does not modify any data.",
    idempotent: !0
  },
  props: {
    issueIds: import_pieces_framework5.Property.Array({
      displayName: "Issue IDs",
      description: "Filter by specific issue IDs",
      required: !1
    }),
    targetAddresses: import_pieces_framework5.Property.Array({
      displayName: "Target Addresses",
      description: "Filter by target addresses (e.g., example.com, 192.168.1.1)",
      required: !1
    }),
    tagNames: import_pieces_framework5.Property.Array({
      displayName: "Tag Names",
      description: "Filter by tag names",
      required: !1
    }),
    severity: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Severity",
      description: "Filter by severity level",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Low", value: "low" },
          { label: "Medium", value: "medium" },
          { label: "High", value: "high" },
          { label: "Critical", value: "critical" }
        ]
      }
    }),
    snoozed: import_pieces_framework5.Property.Checkbox({
      displayName: "Snoozed",
      description: "Filter for snoozed issues",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let params = new URLSearchParams();
    propsValue.issueIds && propsValue.issueIds.length > 0 && propsValue.issueIds.forEach((id) => {
      params.append("issue_ids", String(id));
    }), propsValue.targetAddresses && propsValue.targetAddresses.length > 0 && propsValue.targetAddresses.forEach((address) => {
      params.append("target_addresses", String(address));
    }), propsValue.tagNames && propsValue.tagNames.length > 0 && propsValue.tagNames.forEach((tag) => {
      params.append("tag_names", String(tag));
    }), propsValue.severity && params.append("severity", propsValue.severity), propsValue.snoozed !== void 0 && propsValue.snoozed !== null && params.append("snoozed", String(propsValue.snoozed));
    let queryString = params.toString(), url = `/issues/${queryString ? "?" + queryString : ""}`;
    return await makeRequest(
      auth.secret_text,
      import_pieces_common6.HttpMethod.GET,
      url
    );
  }
});

// ../ap/packages/pieces/community/intruder/src/lib/actions/search-for-an-issue-occurrence.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common"), searchForAnIssueOccurrence = (0, import_pieces_framework6.createAction)({
  auth: intruderAuth,
  name: "searchForAnIssueOccurrence",
  displayName: "Search For an Issue Occurrence",
  description: "Search for occurrences of an issue by ID with optional snoozed filter",
  audience: "both",
  aiMetadata: {
    description: "Retrieves the individual occurrences of a specific Intruder issue, identified by its issue ID, with an optional filter for snoozed occurrences. Use when an agent needs to see where and how a known issue manifests across targets. Requires the issue ID. Idempotent: a read-only lookup that does not modify any data.",
    idempotent: !0
  },
  props: {
    issueId: import_pieces_framework6.Property.ShortText({
      displayName: "Issue ID",
      description: "The ID of the issue to search for occurrences",
      required: !0
    }),
    snoozed: import_pieces_framework6.Property.Checkbox({
      displayName: "Snoozed",
      description: "Filter for snoozed occurrences",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let url = `/issues/${encodeURIComponent(propsValue.issueId)}/occurrences/`;
    return propsValue.snoozed !== void 0 && propsValue.snoozed !== null && (url += `?snoozed=${propsValue.snoozed}`), await makeRequest(auth.secret_text, import_pieces_common7.HttpMethod.GET, url);
  }
});

// ../ap/packages/pieces/community/intruder/src/index.ts
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework"), intruder = (0, import_pieces_framework7.createPiece)({
  displayName: "Intruder",
  auth: intruderAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/intruder.png",
  authors: ["sanket-a11y"],
  categories: [import_pieces_framework8.PieceCategory.DEVELOPER_TOOLS],
  description: " Intruder is a powerful vulnerability management platform that helps businesses identify, assess, and remediate security risks across their digital assets. With its comprehensive scanning capabilities and user-friendly interface, Intruder enables organizations to stay ahead of potential threats and maintain a robust security posture.",
  actions: [
    addTarget,
    startScan,
    searchForATarget,
    searchForAnIssue,
    searchForAnIssueOccurrence,
    (0, import_pieces_common8.createCustomApiCallAction)({
      auth: intruderAuth,
      baseUrl: () => "https://api.intruder.io/v1",
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  intruder
});
