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

// ../ap/packages/pieces/community/posthog/src/index.ts
var index_exports = {};
__export(index_exports, {
  posthog: () => posthog,
  posthogAuth: () => posthogAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/posthog/src/lib/actions/create-event.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var posthogCreateEvent = (0, import_pieces_framework.createAction)({
  auth: posthogAuth,
  name: "create_event",
  displayName: "Capture Event",
  description: "Capture a custom event in PostHog",
  audience: "both",
  aiMetadata: { description: "Sends a custom analytics event to PostHog attributed to a user via their distinct ID (e.g. user ID or email), with optional event properties. Use to record a product action or behavior in PostHog. Not idempotent: each call appends a new event, so repeating it records duplicate events.", idempotent: !1 },
  props: {
    event: import_pieces_framework.Property.ShortText({
      displayName: "Event Name",
      description: "The name of the event to capture",
      required: !0
    }),
    distinct_id: import_pieces_framework.Property.ShortText({
      displayName: "Distinct ID",
      description: "Unique identifier for the user performing the action (e.g. user ID or email address)",
      required: !0
    }),
    properties: import_pieces_framework.Property.Object({
      displayName: "Properties",
      description: "Additional event properties as key/value pairs",
      required: !1
    })
  },
  async run(context) {
    var _a;
    let { personal_api_key, project_id, api_host, ingestion_host } = context.auth.props, apiBase = api_host || "https://us.posthog.com", ingestionBase = ingestion_host || "https://us.i.posthog.com", projectResult = await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: `${apiBase}/api/projects/${project_id}/`,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: personal_api_key
      }
    });
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: `${ingestionBase}/i/v0/e/`,
      body: {
        api_key: projectResult.body.api_token,
        distinct_id: context.propsValue.distinct_id,
        event: context.propsValue.event,
        properties: (_a = context.propsValue.properties) != null ? _a : {}
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/posthog/src/lib/actions/create-project.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var posthogCreateProject = (0, import_pieces_framework2.createAction)({
  auth: posthogAuth,
  name: "create_project",
  displayName: "Create Project",
  description: "Create a new PostHog project in your organization",
  audience: "both",
  aiMetadata: { description: "Creates a new project within a chosen PostHog organization, optionally anonymizing incoming IP addresses. Use when provisioning a fresh analytics project. Not idempotent: each call creates a separate project even with the same name.", idempotent: !1 },
  props: {
    organization_id: import_pieces_framework2.Property.Dropdown({
      displayName: "Organization",
      description: "The organization to create the project in",
      required: !0,
      refreshers: [],
      auth: posthogAuth,
      options: async ({ auth }) => {
        var _a;
        if (!auth)
          return { disabled: !0, options: [], placeholder: "Connect your account first" };
        let { personal_api_key, api_host } = auth.props, apiBase = api_host || "https://us.posthog.com";
        try {
          return {
            disabled: !1,
            options: ((_a = (await import_pieces_common2.httpClient.sendRequest({
              method: import_pieces_common2.HttpMethod.GET,
              url: `${apiBase}/api/organizations/`,
              authentication: {
                type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
                token: personal_api_key
              }
            })).body.results) != null ? _a : []).map((org) => ({
              label: org.name,
              value: org.id
            }))
          };
        } catch (e) {
          return { disabled: !0, options: [], placeholder: "Failed to load organizations. Check your connection." };
        }
      }
    }),
    name: import_pieces_framework2.Property.ShortText({
      displayName: "Project Name",
      description: "The name of the new project",
      required: !0
    }),
    anonymize_ips: import_pieces_framework2.Property.Checkbox({
      displayName: "Anonymize IPs",
      description: "Whether to anonymize incoming IP addresses",
      required: !1
    })
  },
  async run(context) {
    var _a, _b;
    let { personal_api_key, api_host } = context.auth.props, apiBase = api_host || "https://us.posthog.com", { organization_id, name, anonymize_ips } = context.propsValue, p = (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: `${apiBase}/api/organizations/${organization_id}/projects/`,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: personal_api_key
      },
      body: { name, anonymize_ips }
    })).body;
    return {
      id: p.id,
      uuid: p.uuid,
      name: p.name,
      api_token: p.api_token,
      organization: p.organization,
      timezone: (_a = p.timezone) != null ? _a : null,
      anonymize_ips: (_b = p.anonymize_ips) != null ? _b : !1,
      created_at: p.created_at,
      updated_at: p.updated_at
    };
  }
});

// ../ap/packages/pieces/community/posthog/src/lib/actions/get-feature-flags.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var posthogGetFeatureFlags = (0, import_pieces_framework3.createAction)({
  auth: posthogAuth,
  name: "get_feature_flags",
  displayName: "Get Feature Flags",
  description: "List all feature flags in your PostHog project",
  audience: "both",
  aiMetadata: { description: "Retrieves feature flags defined in the connected PostHog project, including each flag key, name, and active state, with an optional result limit. Use to inspect or audit which feature flags exist. Idempotent: a read-only lookup with no side effects.", idempotent: !0 },
  props: {
    limit: import_pieces_framework3.Property.Number({
      displayName: "Limit",
      description: "Maximum number of feature flags to return (default: 100)",
      required: !1,
      defaultValue: 100
    })
  },
  async run(context) {
    var _a, _b;
    let { personal_api_key, project_id, api_host } = context.auth.props, apiBase = api_host || "https://us.posthog.com";
    return ((_b = (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: `${apiBase}/api/projects/${project_id}/feature_flags/`,
      authentication: {
        type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
        token: personal_api_key
      },
      queryParams: {
        limit: String((_a = context.propsValue.limit) != null ? _a : 100)
      }
    })).body.results) != null ? _b : []).map((flag) => {
      var _a2, _b2;
      return {
        id: flag.id,
        name: flag.name,
        key: flag.key,
        active: flag.active,
        deleted: flag.deleted,
        created_at: flag.created_at,
        updated_at: flag.updated_at,
        tags: Array.isArray(flag.tags) ? flag.tags.join(", ") : null,
        created_by_email: (_b2 = (_a2 = flag.created_by) == null ? void 0 : _a2.email) != null ? _b2 : null,
        created_by_name: flag.created_by ? `${flag.created_by.first_name} ${flag.created_by.last_name}`.trim() : null
      };
    });
  }
});

// ../ap/packages/pieces/community/posthog/src/lib/actions/list-persons.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var posthogListPersons = (0, import_pieces_framework4.createAction)({
  auth: posthogAuth,
  name: "list_persons",
  displayName: "List Persons",
  description: "Get a list of identified users in your PostHog project",
  audience: "both",
  aiMetadata: { description: "Lists identified users (persons) in the connected PostHog project; with an empty search it returns all persons up to the limit, or pass a search string to filter by email or name. Use to look up users or find a specific person before acting on them. Idempotent: a read-only lookup with no side effects.", idempotent: !0 },
  props: {
    limit: import_pieces_framework4.Property.Number({
      displayName: "Limit",
      description: "Maximum number of persons to return (default: 100)",
      required: !1,
      defaultValue: 100
    }),
    search: import_pieces_framework4.Property.ShortText({
      displayName: "Search",
      description: "Filter persons by email or name",
      required: !1
    })
  },
  async run(context) {
    var _a, _b;
    let { personal_api_key, project_id, api_host } = context.auth.props, apiBase = api_host || "https://us.posthog.com", queryParams = {
      limit: String((_a = context.propsValue.limit) != null ? _a : 100)
    };
    return context.propsValue.search && (queryParams.search = context.propsValue.search), ((_b = (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.GET,
      url: `${apiBase}/api/projects/${project_id}/persons/`,
      authentication: {
        type: import_pieces_common4.AuthenticationType.BEARER_TOKEN,
        token: personal_api_key
      },
      queryParams
    })).body.results) != null ? _b : []).map((person) => {
      var _a2, _b2, _c, _d;
      return {
        id: person.id,
        uuid: person.uuid,
        distinct_ids: Array.isArray(person.distinct_ids) ? person.distinct_ids.join(", ") : null,
        name: (_a2 = person.name) != null ? _a2 : null,
        email: (_c = (_b2 = person.properties) == null ? void 0 : _b2.email) != null ? _c : null,
        created_at: person.created_at,
        updated_at: (_d = person.updated_at) != null ? _d : null
      };
    });
  }
});

// ../ap/packages/pieces/community/posthog/src/lib/triggers/new-event.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var props = {
  event_name: import_pieces_framework5.Property.ShortText({
    displayName: "Event Name Filter",
    description: "Filter by specific event name (leave empty for all events)",
    required: !1
  })
}, polling = {
  strategy: import_pieces_common5.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => {
    var _a;
    let { personal_api_key, project_id, api_host } = auth.props, apiBase = api_host || "https://us.posthog.com", queryParams = { limit: "100" };
    return lastFetchEpochMS && (queryParams.after = new Date(lastFetchEpochMS).toISOString()), propsValue.event_name && (queryParams.event = propsValue.event_name), ((_a = (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.GET,
      url: `${apiBase}/api/projects/${project_id}/events/`,
      authentication: {
        type: import_pieces_common5.AuthenticationType.BEARER_TOKEN,
        token: personal_api_key
      },
      queryParams
    })).body.results) != null ? _a : []).map((event) => ({
      epochMilliSeconds: new Date(event.timestamp).getTime(),
      data: event
    }));
  }
}, posthogNewEvent = (0, import_pieces_framework5.createTrigger)({
  auth: posthogAuth,
  name: "new_event",
  displayName: "New Event",
  description: "Triggers when a new event is captured in PostHog",
  aiMetadata: {
    description: "Fires when a new analytics event is captured in the connected PostHog project; polls for recently captured events and emits each one. Optionally filter to a single event name, or leave it empty to fire on every event type."
  },
  type: import_pieces_framework5.TriggerStrategy.POLLING,
  props,
  sampleData: {
    id: "1",
    event: "pageview",
    distinct_id: "user_123",
    timestamp: "2024-01-01T00:00:00Z",
    properties: { $current_url: "https://example.com" }
  },
  async test(context) {
    return await import_pieces_common5.pollingHelper.test(polling, context);
  },
  async onEnable(context) {
    await import_pieces_common5.pollingHelper.onEnable(polling, context);
  },
  async onDisable(context) {
    await import_pieces_common5.pollingHelper.onDisable(polling, context);
  },
  async run(context) {
    return await import_pieces_common5.pollingHelper.poll(polling, context);
  }
});

// ../ap/packages/pieces/community/posthog/src/index.ts
var posthogAuth = import_pieces_framework6.PieceAuth.CustomAuth({
  description: `Connect your PostHog account.

**Personal API Key**: Go to PostHog \u2192 Settings \u2192 Personal API Keys \u2192 Create personal API key.

**Project ID**: Found in the URL when viewing your project: \`/project/{id}/\`

**API Host**: The base URL for private API calls.
- US Cloud (default): \`https://us.posthog.com\`
- EU Cloud: \`https://eu.posthog.com\`
- Self-hosted: your instance URL (e.g. \`https://posthog.mycompany.com\`)

**Ingestion Host**: The base URL for event capture.
- US Cloud (default): \`https://us.i.posthog.com\`
- EU Cloud: \`https://eu.i.posthog.com\`
- Self-hosted: same as your API Host`,
  required: !0,
  props: {
    personal_api_key: import_pieces_framework6.PieceAuth.SecretText({
      displayName: "Personal API Key",
      description: "Your PostHog personal API key",
      required: !0
    }),
    project_id: import_pieces_framework6.Property.ShortText({
      displayName: "Project ID",
      description: "Your PostHog project ID (found in project settings URL)",
      required: !0
    }),
    api_host: import_pieces_framework6.Property.ShortText({
      displayName: "API Host",
      description: "Base URL for private API calls (default: https://us.posthog.com)",
      required: !1,
      defaultValue: "https://us.posthog.com"
    }),
    ingestion_host: import_pieces_framework6.Property.ShortText({
      displayName: "Ingestion Host",
      description: "Base URL for event capture (default: https://us.i.posthog.com)",
      required: !1,
      defaultValue: "https://us.i.posthog.com"
    })
  },
  validate: async ({ auth }) => {
    try {
      let apiHost = auth.api_host || "https://us.posthog.com";
      return await import_pieces_common6.httpClient.sendRequest({
        method: import_pieces_common6.HttpMethod.GET,
        url: `${apiHost}/api/projects/${auth.project_id}/`,
        authentication: {
          type: import_pieces_common6.AuthenticationType.BEARER_TOKEN,
          token: auth.personal_api_key
        }
      }), { valid: !0 };
    } catch (e) {
      return { valid: !1, error: "Invalid credentials. Check your Personal API Key and Project ID." };
    }
  }
}), posthog = (0, import_pieces_framework6.createPiece)({
  displayName: "PostHog",
  description: "Open-source product analytics",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/posthog.png",
  categories: [import_pieces_framework7.PieceCategory.BUSINESS_INTELLIGENCE],
  auth: posthogAuth,
  actions: [
    posthogCreateEvent,
    posthogCreateProject,
    posthogGetFeatureFlags,
    posthogListPersons,
    (0, import_pieces_common6.createCustomApiCallAction)({
      baseUrl: (auth) => auth.props.api_host || "https://us.posthog.com",
      auth: posthogAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.props.personal_api_key}`
      })
    })
  ],
  authors: ["kishanprmr", "MoShizzle", "khaledmashaly", "abuaboud", "Tosh94"],
  triggers: [posthogNewEvent]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  posthog,
  posthogAuth
});
