"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __hasOwnProp = Object.prototype.hasOwnProperty;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value;
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
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key != "symbol" ? key + "" : key, value);

// ../ap/packages/pieces/community/gitlab/src/index.ts
var index_exports = {};
__export(index_exports, {
  gitlab: () => gitlab
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/gitlab/src/lib/actions/create-issue-action.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/gitlab/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), gitlabAuth = import_pieces_framework.PieceAuth.OAuth2({
  required: !0,
  authUrl: "https://gitlab.com/oauth/authorize",
  tokenUrl: "https://gitlab.com/oauth/token",
  scope: ["api", "read_user"]
});

// ../ap/packages/pieces/community/gitlab/src/lib/common/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/gitlab/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
var GitlabApi = class {
  constructor(accessToken) {
    __publicField(this, "accessToken", accessToken);
  }
  async makeRequest(method, url, query, body) {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: "https://gitlab.com/api/v4" + url,
      queryParams: query,
      body,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: this.accessToken
      }
    })).body;
  }
  async listProjects(request) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/projects",
      prepareQuery(request)
    );
  }
  async createProjectIssue(projectId, request) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      `/projects/${projectId}/issues`,
      void 0,
      request
    );
  }
  async subscribeProjectWebhook(projectId, request) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      `/projects/${projectId}/hooks`,
      void 0,
      request
    );
  }
  async unsubscribeProjectWebhook(projectId, webhookId) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.DELETE,
      `/projects/${projectId}/hooks/${webhookId}`,
      void 0
    );
  }
};
function emptyValueFilter(accessor) {
  return (key) => {
    let val = accessor(key);
    return val != null && (typeof val != "string" || val.length > 0);
  };
}
function prepareQuery(request) {
  let params = {};
  return request && Object.keys(request).filter(emptyValueFilter((k) => request[k])).forEach((k) => {
    params[k] = request[k].toString();
  }), params;
}

// ../ap/packages/pieces/community/gitlab/src/lib/common/index.ts
var gitlabCommon = {
  projectId: (required = !0) => import_pieces_framework2.Property.Dropdown({
    auth: gitlabAuth,
    displayName: "Project",
    required,
    refreshers: [],
    options: async ({ auth }) => auth ? {
      disabled: !1,
      options: (await makeClient({
        auth
      }).listProjects({
        simple: !0,
        membership: !0
      })).map((project) => ({
        label: project.name,
        value: project.id
      }))
    } : {
      disabled: !0,
      placeholder: "Setup authentication first",
      options: []
    }
  })
};
function makeClient(propsValue) {
  let token = (0, import_pieces_common2.getAccessTokenOrThrow)(propsValue.auth);
  return new GitlabApi(token);
}

// ../ap/packages/pieces/community/gitlab/src/lib/actions/create-issue-action.ts
var createIssueAction = (0, import_pieces_framework3.createAction)({
  auth: gitlabAuth,
  name: "create_issue",
  classification: "WRITE",
  description: "Create a project issue",
  audience: "both",
  aiMetadata: { description: "Creates a new issue in a GitLab project, identified by its numeric project ID, with a title and optional description. Use to open a bug report, feature request, or task. Not idempotent: each call creates a separate issue even with identical inputs.", idempotent: !1 },
  displayName: "Create Issue",
  props: {
    projectId: gitlabCommon.projectId(),
    title: import_pieces_framework3.Property.ShortText({
      displayName: "Issue Title",
      required: !0
    }),
    description: import_pieces_framework3.Property.LongText({
      displayName: "Issue Description",
      required: !1
    })
  },
  async run({ propsValue, auth }) {
    let { projectId, title, description } = propsValue;
    return await makeClient({ auth }).createProjectIssue(projectId, {
      title,
      description
    });
  }
});

// ../ap/packages/pieces/community/gitlab/src/lib/trigger/issue-event.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var sampleData = {
  object_kind: "issue",
  event_type: "issue",
  user: {
    id: 15269532,
    name: "Kishan Parmar",
    username: "kishanprmr",
    avatar_url: "https://secure.gravatar.com/avatar/4eabe7154116891e3ebc205e9754a832?s=80&d=identicon",
    email: "[REDACTED]"
  },
  project: {
    id: 48074457,
    name: "basic-project-demo",
    description: null,
    web_url: "https://gitlab.com/basic-group-demo/basic-project-demo",
    avatar_url: null,
    git_ssh_url: "git@gitlab.com:basic-group-demo/basic-project-demo.git",
    git_http_url: "https://gitlab.com/basic-group-demo/basic-project-demo.git",
    namespace: "basic-group-demo",
    visibility_level: 0,
    path_with_namespace: "basic-group-demo/basic-project-demo",
    default_branch: "main",
    ci_config_path: "",
    homepage: "https://gitlab.com/basic-group-demo/basic-project-demo",
    url: "git@gitlab.com:basic-group-demo/basic-project-demo.git",
    ssh_url: "git@gitlab.com:basic-group-demo/basic-project-demo.git",
    http_url: "https://gitlab.com/basic-group-demo/basic-project-demo.git"
  },
  object_attributes: {
    author_id: 15269532,
    closed_at: null,
    confidential: !1,
    created_at: "2023-09-01 07:03:02 UTC",
    description: "",
    discussion_locked: null,
    due_date: null,
    id: 133093523,
    iid: 32,
    last_edited_at: null,
    last_edited_by_id: null,
    milestone_id: null,
    moved_to_id: null,
    duplicated_to_id: null,
    project_id: 48074457,
    relative_position: null,
    state_id: 1,
    time_estimate: 0,
    title: "Activepieces Testing",
    updated_at: "2023-09-01 07:03:02 UTC",
    updated_by_id: null,
    weight: null,
    health_status: null,
    url: "https://gitlab.com/basic-group-demo/basic-project-demo/-/issues/32",
    total_time_spent: 0,
    time_change: 0,
    human_total_time_spent: null,
    human_time_change: null,
    human_time_estimate: null,
    assignee_ids: [],
    assignee_id: null,
    labels: [],
    state: "opened",
    severity: "unknown",
    customer_relations_contacts: [],
    action: "open"
  },
  labels: [],
  changes: {
    author_id: { previous: null, current: 15269532 },
    created_at: { previous: null, current: "2023-09-01 07:03:02 UTC" },
    description: { previous: null, current: "" },
    id: { previous: null, current: 133093523 },
    iid: { previous: null, current: 32 },
    project_id: { previous: null, current: 48074457 },
    time_estimate: { previous: null, current: 0 },
    title: { previous: null, current: "Activepieces Testing" },
    updated_at: { previous: null, current: "2023-09-01 07:03:02 UTC" }
  },
  repository: {
    name: "basic-project-demo",
    url: "git@gitlab.com:basic-group-demo/basic-project-demo.git",
    description: null,
    homepage: "https://gitlab.com/basic-group-demo/basic-project-demo"
  }
}, issuesEventTrigger = (0, import_pieces_framework4.createTrigger)({
  auth: gitlabAuth,
  name: "project_issue_event",
  classification: "READ",
  displayName: "New Project Issue Event",
  description: "Triggers on project issue events when an issue is created or when an existing issue is updated, closed, or reopened.",
  aiMetadata: {
    description: "Fires when an issue event occurs in the configured GitLab project via webhook. The Issue Event prop scopes which actions fire: All, or only Opened, Closed, or Updated. Represents the issue and its change set at the moment of the event."
  },
  props: {
    projectId: gitlabCommon.projectId(),
    actiontype: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Issue Event",
      description: "Issue Event type for trigger",
      defaultValue: "all",
      required: !0,
      options: {
        disabled: !1,
        options: [
          { label: "All", value: "all" },
          { label: "Opened", value: "open" },
          { label: "Closed", value: "close" },
          { label: "Updated", value: "update" }
        ]
      }
    })
  },
  type: import_pieces_framework4.TriggerStrategy.WEBHOOK,
  sampleData,
  async onEnable({ store, auth, propsValue, webhookUrl }) {
    let projectId = propsValue.projectId, res = await makeClient({ auth }).subscribeProjectWebhook(projectId, {
      url: webhookUrl,
      issues_events: !0,
      push_events: !1
    });
    await store.put("gitlab_issue_trigger", {
      webhookId: res.id,
      projectId
    });
  },
  async onDisable({ auth, store }) {
    let response = await store.get(
      "gitlab_issue_trigger"
    );
    response != null && makeClient({ auth }).unsubscribeProjectWebhook(response.projectId, response.webhookId);
  },
  async run(context) {
    let { actiontype } = context.propsValue;
    return isVerificationCall(
      context.payload.body,
      actiontype
    ) ? [context.payload.body] : [];
  }
});
function isVerificationCall(payload, actiontype) {
  return actiontype == "all" ? !0 : payload.object_attributes.action == actiontype;
}

// ../ap/packages/pieces/community/gitlab/src/index.ts
var gitlab = (0, import_pieces_framework5.createPiece)({
  displayName: "GitLab",
  description: "Collaboration tool for developers",
  auth: gitlabAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/gitlab.png",
  categories: [import_pieces_framework6.PieceCategory.DEVELOPER_TOOLS],
  authors: ["kishanprmr", "MoShizzle", "khaledmashaly", "abuaboud"],
  actions: [
    createIssueAction,
    (0, import_pieces_common3.createCustomApiCallAction)({
      baseUrl: () => "https://gitlab.com/api/v4",
      auth: gitlabAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.access_token}`
      })
    })
  ],
  triggers: [issuesEventTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  gitlab
});
