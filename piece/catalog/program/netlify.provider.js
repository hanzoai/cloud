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

// ../ap/packages/pieces/community/netlify/src/index.ts
var index_exports = {};
__export(index_exports, {
  netlify: () => netlify
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework10 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/netlify/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), netlifyAuth = import_pieces_framework.PieceAuth.OAuth2({
  description: `
To authenticate with Netlify:

1. Go to your Netlify user settings
2. Navigate to "Applications" \u2192 "OAuth applications"
3. Click "New OAuth application"
4. Add https://cloud.activepieces.com/redirect to authorized redirect URIs
5. Copy the Client ID and Client Secret
6. Use the OAuth2 flow below

**Note:** If your team uses SAML SSO, you must grant access to the team when generating your token.
`,
  authUrl: "https://app.netlify.com/authorize",
  tokenUrl: "https://api.netlify.com/oauth/token",
  required: !0,
  scope: []
});

// ../ap/packages/pieces/community/netlify/src/lib/actions/start-deploy.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var startDeploy = (0, import_pieces_framework2.createAction)({
  name: "start_deploy",
  displayName: "Start Deploy",
  description: "Triggers a new build for a site on Netlify. Supports clearing build cache.",
  audience: "both",
  aiMetadata: { description: "Triggers a new build/deploy for a Netlify site, identified by its site ID. Optionally clears the build cache before building. Not idempotent: each call queues a fresh deploy, so calling repeatedly starts multiple builds.", idempotent: !1 },
  props: {
    siteId: import_pieces_framework2.Property.Dropdown({
      auth: netlifyAuth,
      displayName: "Site",
      description: "Select the site to deploy",
      required: !0,
      refreshers: ["auth"],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Connect your account first",
            options: []
          };
        let authentication = auth;
        if (!authentication.access_token)
          return {
            disabled: !0,
            placeholder: "Access token is required",
            options: []
          };
        try {
          let response = await import_pieces_common.httpClient.sendRequest({
            method: import_pieces_common.HttpMethod.GET,
            url: "https://api.netlify.com/api/v1/sites?per_page=50",
            headers: {
              Authorization: `Bearer ${authentication.access_token}`,
              "Content-Type": "application/json"
            }
          });
          return response.status === 200 ? {
            disabled: !1,
            options: response.body.map((site) => ({
              label: `${site.name} (${site.url})`,
              value: site.id
            }))
          } : {
            disabled: !0,
            placeholder: `Failed to fetch sites: ${response.status}`,
            options: []
          };
        } catch (error) {
          return {
            disabled: !0,
            placeholder: "Failed to fetch sites",
            options: []
          };
        }
      }
    }),
    clearCache: import_pieces_framework2.Property.Checkbox({
      displayName: "Clear Build Cache",
      description: "Whether to clear the build cache before building",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { siteId, clearCache } = context.propsValue;
    if (!context.auth)
      throw new Error("Authentication is required");
    let auth = context.auth;
    if (!auth.access_token)
      throw new Error("Access token is required");
    let response = await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: `https://api.netlify.com/api/v1/sites/${siteId}/deploys`,
      headers: {
        Authorization: `Bearer ${auth.access_token}`,
        "Content-Type": "application/json"
      },
      body: {
        clear_cache: clearCache
      }
    });
    if (response.status === 200 || response.status === 201)
      return response.body;
    throw response.status === 404 ? new Error(`Site not found: ${siteId}. Please check the site ID or domain.`) : response.status === 401 ? new Error("Unauthorized. Please check your access token.") : response.status === 403 ? new Error("Forbidden. You don't have permission to deploy this site.") : new Error(`Failed to start deploy: ${response.status}`);
  }
});

// ../ap/packages/pieces/community/netlify/src/lib/actions/get-site.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var getSite = (0, import_pieces_framework3.createAction)({
  name: "get_site",
  displayName: "Get Site",
  description: "Get a specified site. Note: Environment variable keys and values have moved from build_settings.env and repo.env to a new endpoint. Please use getEnvVars to retrieve site environment variables.",
  audience: "both",
  aiMetadata: { description: "Retrieves the configuration and metadata for a single Netlify site by its site ID. Use it to look up details such as the site URL, build settings, or deploy info. Read-only and idempotent. Note: site environment variables are no longer included here and must be fetched from the dedicated env-vars endpoint.", idempotent: !0 },
  props: {
    siteId: import_pieces_framework3.Property.Dropdown({
      auth: netlifyAuth,
      displayName: "Site",
      description: "Select the site to retrieve information for",
      required: !0,
      refreshers: ["auth"],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Connect your account first",
            options: []
          };
        let authentication = auth;
        if (!authentication.access_token)
          return {
            disabled: !0,
            placeholder: "Access token is required",
            options: []
          };
        try {
          let response = await import_pieces_common2.httpClient.sendRequest({
            method: import_pieces_common2.HttpMethod.GET,
            url: "https://api.netlify.com/api/v1/sites?per_page=50",
            headers: {
              Authorization: `Bearer ${authentication.access_token}`,
              "Content-Type": "application/json"
            }
          });
          return response.status === 200 ? {
            disabled: !1,
            options: response.body.map((site) => ({
              label: `${site.name} (${site.url})`,
              value: site.id
            }))
          } : {
            disabled: !0,
            placeholder: `Failed to fetch sites: ${response.status}`,
            options: []
          };
        } catch (error) {
          return {
            disabled: !0,
            placeholder: "Failed to fetch sites",
            options: []
          };
        }
      }
    })
  },
  async run(context) {
    let { siteId } = context.propsValue;
    if (!context.auth)
      throw new Error("Authentication is required");
    let auth = context.auth;
    if (!auth.access_token)
      throw new Error("Access token is required");
    let response = await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.GET,
      url: `https://api.netlify.com/api/v1/sites/${siteId}`,
      headers: {
        Authorization: `Bearer ${auth.access_token}`,
        "Content-Type": "application/json"
      }
    });
    if (response.status === 200)
      return response.body;
    throw response.status === 404 ? new Error(`Site not found: ${siteId}. Please check the site ID or domain.`) : response.status === 401 ? new Error("Unauthorized. Please check your access token.") : response.status === 403 ? new Error("Forbidden. You don't have permission to access this site.") : new Error(`Failed to get site: ${response.status}`);
  }
});

// ../ap/packages/pieces/community/netlify/src/lib/actions/list-site-deploys.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var listSiteDeploys = (0, import_pieces_framework4.createAction)({
  name: "list_site_deploys",
  displayName: "List Site Deploys",
  description: "Returns a list of all deploys for a specific site.",
  audience: "both",
  aiMetadata: { description: "Lists the deploys (builds) for a Netlify site by its site ID, ordered most-recent first, with optional page and per-page pagination. Use it to inspect deploy history or find a deploy state/ID. Read-only and idempotent.", idempotent: !0 },
  props: {
    siteId: import_pieces_framework4.Property.Dropdown({
      auth: netlifyAuth,
      displayName: "Site",
      description: "Select the site to list deploys for",
      required: !0,
      refreshers: ["auth"],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Connect your account first",
            options: []
          };
        let authentication = auth;
        if (!authentication.access_token)
          return {
            disabled: !0,
            placeholder: "Access token is required",
            options: []
          };
        try {
          let response = await import_pieces_common3.httpClient.sendRequest({
            method: import_pieces_common3.HttpMethod.GET,
            url: "https://api.netlify.com/api/v1/sites?per_page=50",
            headers: {
              Authorization: `Bearer ${authentication.access_token}`,
              "Content-Type": "application/json"
            }
          });
          return response.status === 200 ? {
            disabled: !1,
            options: response.body.map((site) => ({
              label: `${site.name} (${site.url})`,
              value: site.id
            }))
          } : {
            disabled: !0,
            placeholder: `Failed to fetch sites: ${response.status}`,
            options: []
          };
        } catch (error) {
          return {
            disabled: !0,
            placeholder: "Failed to fetch sites",
            options: []
          };
        }
      }
    }),
    page: import_pieces_framework4.Property.Number({
      displayName: "Page",
      description: "Page number for pagination (starts from 1)",
      required: !1,
      defaultValue: 1
    }),
    perPage: import_pieces_framework4.Property.Number({
      displayName: "Per Page",
      description: "Number of items per page (max 100)",
      required: !1,
      defaultValue: 100
    })
  },
  async run(context) {
    let { siteId, page, perPage } = context.propsValue;
    if (!context.auth)
      throw new Error("Authentication is required");
    let auth = context.auth;
    if (!auth.access_token)
      throw new Error("Access token is required");
    let queryParams = new URLSearchParams();
    page && queryParams.append("page", page.toString()), perPage && queryParams.append("per_page", perPage.toString());
    let response = await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: `https://api.netlify.com/api/v1/sites/${siteId}/deploys?${queryParams.toString()}`,
      headers: {
        Authorization: `Bearer ${auth.access_token}`,
        "Content-Type": "application/json"
      }
    });
    if (response.status === 200)
      return response.body;
    throw response.status === 404 ? new Error(`Site not found: ${siteId}. Please check the site ID or domain.`) : response.status === 401 ? new Error("Unauthorized. Please check your access token.") : response.status === 403 ? new Error("Forbidden. You don't have permission to access this site.") : new Error(`Failed to list site deploys: ${response.status}`);
  }
});

// ../ap/packages/pieces/community/netlify/src/lib/actions/list-files.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var listFiles = (0, import_pieces_framework5.createAction)({
  name: "list_files",
  displayName: "List Files",
  description: "Returns a list of all the files in the current deploy.",
  audience: "both",
  aiMetadata: { description: "Lists all files in the current (published) deploy of a Netlify site, identified by its site ID. Use it to inspect which files are served for the live site. Read-only and idempotent.", idempotent: !0 },
  props: {
    siteId: import_pieces_framework5.Property.Dropdown({
      auth: netlifyAuth,
      displayName: "Site",
      description: "Select the site to list files for",
      required: !0,
      refreshers: ["auth"],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Connect your account first",
            options: []
          };
        let authentication = auth;
        if (!authentication.access_token)
          return {
            disabled: !0,
            placeholder: "Access token is required",
            options: []
          };
        try {
          let response = await import_pieces_common4.httpClient.sendRequest({
            method: import_pieces_common4.HttpMethod.GET,
            url: "https://api.netlify.com/api/v1/sites?per_page=50",
            headers: {
              Authorization: `Bearer ${authentication.access_token}`,
              "Content-Type": "application/json"
            }
          });
          return response.status === 200 ? {
            disabled: !1,
            options: response.body.map((site) => ({
              label: `${site.name} (${site.url})`,
              value: site.id
            }))
          } : {
            disabled: !0,
            placeholder: `Failed to fetch sites: ${response.status}`,
            options: []
          };
        } catch (error) {
          return {
            disabled: !0,
            placeholder: "Failed to fetch sites",
            options: []
          };
        }
      }
    })
  },
  async run(context) {
    let { siteId } = context.propsValue;
    if (!context.auth)
      throw new Error("Authentication is required");
    let auth = context.auth;
    if (!auth.access_token)
      throw new Error("Access token is required");
    let response = await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.GET,
      url: `https://api.netlify.com/api/v1/sites/${siteId}/files`,
      headers: {
        Authorization: `Bearer ${auth.access_token}`,
        "Content-Type": "application/json"
      }
    });
    if (response.status === 200)
      return response.body;
    throw response.status === 404 ? new Error(`Site not found: ${siteId}. Please check the site ID or domain.`) : response.status === 401 ? new Error("Unauthorized. Please check your access token.") : response.status === 403 ? new Error("Forbidden. You don't have permission to access this site.") : new Error(`Failed to list files: ${response.status}`);
  }
});

// ../ap/packages/pieces/community/netlify/src/lib/triggers/new-deploy-started.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var newDeployStarted = (0, import_pieces_framework6.createTrigger)({
  name: "new_deploy_started",
  displayName: "New Deploy Started",
  description: "Fires immediately when a deploy job starts on your Netlify site.",
  aiMetadata: {
    description: "Fires when a new deploy job begins building on the selected Netlify site (the deploy_created event), before it finishes. Use it to react the moment a build kicks off, e.g. to notify or track in-progress deploys."
  },
  auth: netlifyAuth,
  props: {
    siteId: import_pieces_framework6.Property.Dropdown({
      auth: netlifyAuth,
      displayName: "Site",
      description: "Select the site to monitor for deploy events",
      required: !0,
      refreshers: ["auth"],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Connect your account first",
            options: []
          };
        let authentication = auth;
        if (!authentication.access_token)
          return {
            disabled: !0,
            placeholder: "Access token is required",
            options: []
          };
        try {
          let response = await import_pieces_common5.httpClient.sendRequest({
            method: import_pieces_common5.HttpMethod.GET,
            url: "https://api.netlify.com/api/v1/sites?per_page=50",
            headers: {
              Authorization: `Bearer ${authentication.access_token}`,
              "Content-Type": "application/json"
            }
          });
          return response.status === 200 ? {
            disabled: !1,
            options: response.body.map((site) => ({
              label: `${site.name} (${site.url})`,
              value: site.id
            }))
          } : {
            disabled: !0,
            placeholder: `Failed to fetch sites: ${response.status}`,
            options: []
          };
        } catch (error) {
          return {
            disabled: !0,
            placeholder: "Failed to fetch sites",
            options: []
          };
        }
      }
    })
  },
  sampleData: {
    id: "507f1f77bcf86cd799439013",
    site_id: "b6fda79c-0cb0-44ca-8e73-3f5f2c0f0e2d",
    build_id: "5d7725b654c02c0007350e8b",
    state: "building",
    name: "my-site",
    url: "https://my-site.netlify.app",
    ssl_url: "https://my-site.netlify.app",
    admin_url: "https://app.netlify.com/sites/my-site",
    deploy_url: "https://5d7725b654c02c0007350e8b--my-site.netlify.app",
    deploy_ssl_url: "https://5d7725b654c02c0007350e8b--my-site.netlify.app",
    created_at: "2025-09-02T10:15:30.000Z",
    updated_at: "2025-09-02T10:15:30.000Z",
    user_id: "507f1f77bcf86cd799439012",
    branch: "main",
    commit_ref: "def456ghi789",
    commit_url: "https://github.com/user/repo/commit/def456ghi789",
    skipped: !1,
    locked: !1,
    review_id: null,
    framework: "react"
  },
  type: import_pieces_framework6.TriggerStrategy.WEBHOOK,
  onEnable: async (context) => {
    if (!context.auth)
      throw new Error("Authentication is required");
    let auth = context.auth;
    if (!auth.access_token)
      throw new Error("Access token is required");
    let response = await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.POST,
      url: "https://api.netlify.com/api/v1/hooks",
      headers: {
        Authorization: `Bearer ${auth.access_token}`,
        "Content-Type": "application/json"
      },
      body: {
        site_id: context.propsValue.siteId,
        type: "url",
        event: "deploy_created",
        data: {
          url: context.webhookUrl
        }
      }
    });
    if (response.status === 201)
      await context.store.put("webhook_id", response.body.id);
    else
      throw new Error(`Failed to create webhook: ${response.status}`);
  },
  onDisable: async (context) => {
    let webhookId = await context.store.get("webhook_id");
    if (webhookId) {
      if (!context.auth)
        throw new Error("Authentication is required");
      let auth = context.auth;
      if (!auth.access_token)
        throw new Error("Access token is required");
      await import_pieces_common5.httpClient.sendRequest({
        method: import_pieces_common5.HttpMethod.DELETE,
        url: `https://api.netlify.com/api/v1/hooks/${webhookId}`,
        headers: {
          Authorization: `Bearer ${auth.access_token}`
        }
      });
    }
  },
  run: async (context) => [context.payload.body]
});

// ../ap/packages/pieces/community/netlify/src/lib/triggers/new-deploy-succeeded.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var newDeploySucceeded = (0, import_pieces_framework7.createTrigger)({
  name: "new_deploy_succeeded",
  displayName: "New Deploy Succeeded",
  description: "Fires when a new site version has successfully deployed.",
  aiMetadata: {
    description: "Fires when a deploy on the selected Netlify site finishes successfully and the new version goes live (the deploy_succeeded event). Use it to run follow-up steps after a site is published, e.g. cache purges, notifications, or downstream syncs."
  },
  auth: netlifyAuth,
  props: {
    siteId: import_pieces_framework7.Property.Dropdown({
      auth: netlifyAuth,
      displayName: "Site",
      description: "Select the site to monitor for successful deploy events",
      required: !0,
      refreshers: ["auth"],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Connect your account first",
            options: []
          };
        let authentication = auth;
        if (!authentication.access_token)
          return {
            disabled: !0,
            placeholder: "Access token is required",
            options: []
          };
        try {
          let response = await import_pieces_common6.httpClient.sendRequest({
            method: import_pieces_common6.HttpMethod.GET,
            url: "https://api.netlify.com/api/v1/sites?per_page=50",
            headers: {
              Authorization: `Bearer ${authentication.access_token}`,
              "Content-Type": "application/json"
            }
          });
          return response.status === 200 ? {
            disabled: !1,
            options: response.body.map((site) => ({
              label: `${site.name} (${site.url})`,
              value: site.id
            }))
          } : {
            disabled: !0,
            placeholder: `Failed to fetch sites: ${response.status}`,
            options: []
          };
        } catch (error) {
          return {
            disabled: !0,
            placeholder: "Failed to fetch sites",
            options: []
          };
        }
      }
    })
  },
  sampleData: {
    id: "507f1f77bcf86cd799439014",
    site_id: "b6fda79c-0cb0-44ca-8e73-3f5f2c0f0e2d",
    build_id: "5d7725b654c02c0007350e8c",
    state: "ready",
    name: "my-site",
    url: "https://my-site.netlify.app",
    ssl_url: "https://my-site.netlify.app",
    admin_url: "https://app.netlify.com/sites/my-site",
    deploy_url: "https://5d7725b654c02c0007350e8c--my-site.netlify.app",
    deploy_ssl_url: "https://5d7725b654c02c0007350e8c--my-site.netlify.app",
    created_at: "2025-09-02T10:15:30.000Z",
    updated_at: "2025-09-02T10:18:45.000Z",
    published_at: "2025-09-02T10:18:45.000Z",
    user_id: "507f1f77bcf86cd799439012",
    branch: "main",
    commit_ref: "ghi789jkl012",
    commit_url: "https://github.com/user/repo/commit/ghi789jkl012",
    skipped: !1,
    locked: !1,
    review_id: null,
    framework: "react",
    function_schedules: []
  },
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  onEnable: async (context) => {
    if (!context.auth)
      throw new Error("Authentication is required");
    let auth = context.auth;
    if (!auth.access_token)
      throw new Error("Access token is required");
    let response = await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.POST,
      url: "https://api.netlify.com/api/v1/hooks",
      headers: {
        Authorization: `Bearer ${auth.access_token}`,
        "Content-Type": "application/json"
      },
      body: {
        site_id: context.propsValue.siteId,
        type: "url",
        event: "deploy_succeeded",
        data: {
          url: context.webhookUrl
        }
      }
    });
    if (response.status === 201)
      await context.store.put("webhook_id", response.body.id);
    else
      throw new Error(`Failed to create webhook: ${response.status}`);
  },
  onDisable: async (context) => {
    let webhookId = await context.store.get("webhook_id");
    if (webhookId) {
      if (!context.auth)
        throw new Error("Authentication is required");
      let auth = context.auth;
      if (!auth.access_token)
        throw new Error("Access token is required");
      await import_pieces_common6.httpClient.sendRequest({
        method: import_pieces_common6.HttpMethod.DELETE,
        url: `https://api.netlify.com/api/v1/hooks/${webhookId}`,
        headers: {
          Authorization: `Bearer ${auth.access_token}`
        }
      });
    }
  },
  run: async (context) => [context.payload.body]
});

// ../ap/packages/pieces/community/netlify/src/lib/triggers/new-deploy-failed.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var newDeployFailed = (0, import_pieces_framework8.createTrigger)({
  name: "new_deploy_failed",
  displayName: "New Deploy Failed",
  description: "Fires when a site deploy fails.",
  aiMetadata: {
    description: "Fires when a deploy on the selected Netlify site fails to build or publish (the deploy_failed event); the payload includes the error message. Use it to alert on or remediate broken deploys."
  },
  auth: netlifyAuth,
  props: {
    siteId: import_pieces_framework8.Property.Dropdown({
      auth: netlifyAuth,
      displayName: "Site",
      description: "Select the site to monitor for failed deploy events",
      required: !0,
      refreshers: ["auth"],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Connect your account first",
            options: []
          };
        let authentication = auth;
        if (!authentication.access_token)
          return {
            disabled: !0,
            placeholder: "Access token is required",
            options: []
          };
        try {
          let response = await import_pieces_common7.httpClient.sendRequest({
            method: import_pieces_common7.HttpMethod.GET,
            url: "https://api.netlify.com/api/v1/sites?per_page=50",
            headers: {
              Authorization: `Bearer ${authentication.access_token}`,
              "Content-Type": "application/json"
            }
          });
          return response.status === 200 ? {
            disabled: !1,
            options: response.body.map((site) => ({
              label: `${site.name} (${site.url})`,
              value: site.id
            }))
          } : {
            disabled: !0,
            placeholder: `Failed to fetch sites: ${response.status}`,
            options: []
          };
        } catch (error) {
          return {
            disabled: !0,
            placeholder: "Failed to fetch sites",
            options: []
          };
        }
      }
    })
  },
  sampleData: {
    id: "507f1f77bcf86cd799439011",
    site_id: "b6fda79c-0cb0-44ca-8e73-3f5f2c0f0e2d",
    build_id: "5d7725b654c02c0007350e8a",
    state: "error",
    name: "my-site",
    url: "https://my-site.netlify.app",
    ssl_url: "https://my-site.netlify.app",
    admin_url: "https://app.netlify.com/sites/my-site",
    deploy_url: "https://5d7725b654c02c0007350e8a--my-site.netlify.app",
    deploy_ssl_url: "https://5d7725b654c02c0007350e8a--my-site.netlify.app",
    created_at: "2025-09-02T10:15:30.000Z",
    updated_at: "2025-09-02T10:16:45.000Z",
    user_id: "507f1f77bcf86cd799439012",
    error_message: "Build failed due to missing dependency",
    branch: "main",
    commit_ref: "abc123def456",
    commit_url: "https://github.com/user/repo/commit/abc123def456",
    skipped: !1,
    locked: !1
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  onEnable: async (context) => {
    if (!context.auth)
      throw new Error("Authentication is required");
    let auth = context.auth;
    if (!auth.access_token)
      throw new Error("Access token is required");
    let response = await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.POST,
      url: "https://api.netlify.com/api/v1/hooks",
      headers: {
        Authorization: `Bearer ${auth.access_token}`,
        "Content-Type": "application/json"
      },
      body: {
        site_id: context.propsValue.siteId,
        type: "url",
        event: "deploy_failed",
        data: {
          url: context.webhookUrl
        }
      }
    });
    if (response.status === 201)
      await context.store.put("webhook_id", response.body.id);
    else
      throw new Error(`Failed to create webhook: ${response.status}`);
  },
  onDisable: async (context) => {
    let webhookId = await context.store.get("webhook_id");
    if (webhookId) {
      if (!context.auth)
        throw new Error("Authentication is required");
      let auth = context.auth;
      if (!auth.access_token)
        throw new Error("Access token is required");
      await import_pieces_common7.httpClient.sendRequest({
        method: import_pieces_common7.HttpMethod.DELETE,
        url: `https://api.netlify.com/api/v1/hooks/${webhookId}`,
        headers: {
          Authorization: `Bearer ${auth.access_token}`
        }
      });
    }
  },
  run: async (context) => [context.payload.body]
});

// ../ap/packages/pieces/community/netlify/src/lib/triggers/new-form-submission.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var newFormSubmission = (0, import_pieces_framework9.createTrigger)({
  name: "new_form_submission",
  displayName: "New Form Submission",
  description: "Fires when a Netlify form submission is received.",
  aiMetadata: {
    description: "Fires when a new form submission is received on the selected Netlify site (the submission_created event); the payload includes the submitted field data and submitter metadata. Use it to process or route contact/lead form entries."
  },
  auth: netlifyAuth,
  props: {
    siteId: import_pieces_framework9.Property.Dropdown({
      auth: netlifyAuth,
      displayName: "Site",
      description: "Select the site to monitor for form submissions",
      required: !0,
      refreshers: ["auth"],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Connect your account first",
            options: []
          };
        let authentication = auth;
        if (!authentication.access_token)
          return {
            disabled: !0,
            placeholder: "Access token is required",
            options: []
          };
        try {
          let response = await import_pieces_common8.httpClient.sendRequest({
            method: import_pieces_common8.HttpMethod.GET,
            url: "https://api.netlify.com/api/v1/sites?per_page=50",
            headers: {
              Authorization: `Bearer ${authentication.access_token}`,
              "Content-Type": "application/json"
            }
          });
          return response.status === 200 ? {
            disabled: !1,
            options: response.body.map((site) => ({
              label: `${site.name} (${site.url})`,
              value: site.id
            }))
          } : {
            disabled: !0,
            placeholder: `Failed to fetch sites: ${response.status}`,
            options: []
          };
        } catch (error) {
          return {
            disabled: !0,
            placeholder: "Failed to fetch sites",
            options: []
          };
        }
      }
    })
  },
  sampleData: {
    id: "507f1f77bcf86cd799439015",
    number: 42,
    email: "user@example.com",
    name: "John Doe",
    first_name: "John",
    last_name: "Doe",
    company: "Acme Corp",
    summary: "Contact form submission from website",
    body: "Hello, I'm interested in your services. Please contact me.",
    data: {
      name: "John Doe",
      email: "user@example.com",
      message: "Hello, I'm interested in your services. Please contact me.",
      phone: "+1-555-123-4567"
    },
    created_at: "2025-09-02T10:15:30.000Z",
    site_url: "https://my-site.netlify.app",
    form_id: "507f1f77bcf86cd799439016",
    form_name: "contact",
    ip: "192.168.1.100",
    user_agent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
    referrer: "https://my-site.netlify.app/contact"
  },
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  onEnable: async (context) => {
    if (!context.auth)
      throw new Error("Authentication is required");
    let auth = context.auth;
    if (!auth.access_token)
      throw new Error("Access token is required");
    let response = await import_pieces_common8.httpClient.sendRequest({
      method: import_pieces_common8.HttpMethod.POST,
      url: "https://api.netlify.com/api/v1/hooks",
      headers: {
        Authorization: `Bearer ${auth.access_token}`,
        "Content-Type": "application/json"
      },
      body: {
        site_id: context.propsValue.siteId,
        type: "url",
        event: "submission_created",
        data: {
          url: context.webhookUrl
        }
      }
    });
    if (response.status === 201)
      await context.store.put("webhook_id", response.body.id);
    else
      throw new Error(`Failed to create webhook: ${response.status}`);
  },
  onDisable: async (context) => {
    let webhookId = await context.store.get("webhook_id");
    if (webhookId) {
      if (!context.auth)
        throw new Error("Authentication is required");
      let auth = context.auth;
      if (!auth.access_token)
        throw new Error("Access token is required");
      await import_pieces_common8.httpClient.sendRequest({
        method: import_pieces_common8.HttpMethod.DELETE,
        url: `https://api.netlify.com/api/v1/hooks/${webhookId}`,
        headers: {
          Authorization: `Bearer ${auth.access_token}`
        }
      });
    }
  },
  run: async (context) => [context.payload.body]
});

// ../ap/packages/pieces/community/netlify/src/index.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), netlify = (0, import_pieces_framework10.createPiece)({
  displayName: "Netlify",
  auth: netlifyAuth,
  minimumSupportedRelease: "0.36.1",
  description: "Netlify is a platform for building and deploying websites and apps.",
  logoUrl: "https://cdn.activepieces.com/pieces/netlify.png",
  authors: ["sparkybug"],
  categories: [import_pieces_framework11.PieceCategory.DEVELOPER_TOOLS],
  actions: [
    startDeploy,
    getSite,
    listSiteDeploys,
    listFiles
  ],
  triggers: [
    newDeployStarted,
    newDeploySucceeded,
    newDeployFailed,
    newFormSubmission
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  netlify
});
