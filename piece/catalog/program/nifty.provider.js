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

// ../ap/packages/pieces/community/nifty/src/index.ts
var index_exports = {};
__export(index_exports, {
  nifty: () => nifty
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/nifty/src/lib/actions/create-task.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/nifty/src/lib/common/index.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/nifty/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), mddescription = `
# How to add a new connection
1. Login to your nifty account at https://niftypm.com/
2. From your account settings, click on App Center
3. After that click on Integrate with API
4. Then Create a new app
5. Select the Name and Description you want
6. copy the redirect url from the piece and fill the url field ( without https:// )
7. check out Milestones , Subtasks , Projects , Statuses , Tasks and Portfolios
8. copy the client id and client secret and paste them in the piece
`, niftyAuth = import_pieces_framework.PieceAuth.OAuth2({
  authUrl: "https://nifty.pm/authorize",
  tokenUrl: "https://openapi.niftypm.com/oauth/token",
  required: !0,
  description: mddescription,
  scope: ["task", "project", "subtask", "milestone", "subteam"]
});

// ../ap/packages/pieces/community/nifty/src/lib/common/index.ts
var nitfyCommon = {
  portfolio: import_pieces_framework2.Property.Dropdown({
    auth: niftyAuth,
    displayName: "Portfolio",
    required: !0,
    refreshers: [],
    options: async ({ auth }) => {
      let authentication = auth;
      if (!authentication)
        return {
          disabled: !0,
          placeholder: "connect your account first",
          options: []
        };
      let accessToken = authentication.access_token;
      return {
        disabled: !1,
        options: (await callNitfyApi(import_pieces_common.HttpMethod.GET, "subteams", accessToken, void 0)).body.subteams.map((team) => ({
          label: team.name,
          value: team.id
        }))
      };
    }
  }),
  project: import_pieces_framework2.Property.Dropdown({
    auth: niftyAuth,
    displayName: "Project",
    required: !0,
    refreshers: ["portfolio"],
    options: async ({ auth, portfolio }) => {
      let authentication = auth;
      if (!authentication)
        return {
          disabled: !0,
          placeholder: "connect your account first",
          options: []
        };
      if (!portfolio)
        return {
          disabled: !0,
          placeholder: "Select portfolio first",
          options: []
        };
      let accessToken = authentication.access_token, response = (await callNitfyApi(import_pieces_common.HttpMethod.GET, "projects", accessToken, void 0)).body;
      return response.projects = response.projects.filter((project) => project.subteam == portfolio), {
        disabled: !1,
        options: response.projects.map((project) => ({
          label: project.name,
          value: project.id
        }))
      };
    }
  }),
  status: import_pieces_framework2.Property.Dropdown({
    auth: niftyAuth,
    displayName: "Status",
    required: !0,
    refreshers: ["project"],
    options: async ({ auth, project }) => {
      let authentication = auth;
      if (!authentication)
        return {
          disabled: !0,
          placeholder: "connect your account first",
          options: []
        };
      if (!project)
        return {
          disabled: !0,
          placeholder: "Select portfolio first",
          options: []
        };
      let accessToken = authentication.access_token;
      return {
        disabled: !1,
        options: (await callNitfyApi(
          import_pieces_common.HttpMethod.GET,
          `taskgroups?project_id=${project}&archived=false`,
          accessToken,
          void 0
        )).body.items.map((list) => ({
          label: list.name,
          value: list.id
        }))
      };
    }
  }),
  milestone: import_pieces_framework2.Property.Dropdown({
    auth: niftyAuth,
    displayName: "Milestone",
    required: !0,
    refreshers: ["project"],
    options: async ({ auth, project }) => {
      let authentication = auth;
      if (!authentication)
        return {
          disabled: !0,
          placeholder: "connect your account first",
          options: []
        };
      if (!project)
        return {
          disabled: !0,
          placeholder: "Select project first",
          options: []
        };
      let accessToken = authentication.access_token;
      return {
        disabled: !1,
        options: (await callNitfyApi(
          import_pieces_common.HttpMethod.GET,
          `milestones?project_id=${project}&is_list=true`,
          accessToken,
          void 0
        )).body.items.map((list) => ({
          label: list.name,
          value: list.id
        }))
      };
    }
  })
};
async function callNitfyApi(method, apiUrl, accessToken, body) {
  return await import_pieces_common.httpClient.sendRequest({
    method,
    url: `https://openapi.niftypm.com/api/v1.0/${apiUrl}`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token: accessToken
    },
    body
  });
}

// ../ap/packages/pieces/community/nifty/src/lib/actions/create-task.ts
var createTask = (0, import_pieces_framework3.createAction)({
  name: "create_task",
  auth: niftyAuth,
  displayName: "Create Task",
  description: "Create a task in nitfy",
  audience: "both",
  aiMetadata: { description: "Creates a new task in Nifty under a chosen project, status (task group), and optional milestone. Use when an agent needs to add a work item to a Nifty project; requires a task name and a target status. Not idempotent \u2014 each call creates a separate task even with identical input.", idempotent: !1 },
  props: {
    portfolio: nitfyCommon.portfolio,
    project: nitfyCommon.project,
    status: nitfyCommon.status,
    milestone: nitfyCommon.milestone,
    task_name: import_pieces_framework3.Property.ShortText({
      displayName: "Task Name",
      required: !0
    }),
    task_description: import_pieces_framework3.Property.LongText({
      displayName: "Task Description",
      required: !1
    })
  },
  async run(context) {
    let accessToken = context.auth.access_token, status = context.propsValue.status, task_name = context.propsValue.task_name, task_description = context.propsValue.task_description, milestone = context.propsValue.milestone;
    return [(await callNitfyApi(import_pieces_common2.HttpMethod.POST, "tasks", accessToken, {
      name: task_name,
      task_group_id: status,
      description: task_description,
      milestone_id: milestone
    })).body];
  }
});

// ../ap/packages/pieces/community/nifty/src/index.ts
var nifty = (0, import_pieces_framework4.createPiece)({
  displayName: "Nifty",
  description: "Project management made simple",
  auth: niftyAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/nifty.png",
  categories: [import_pieces_framework5.PieceCategory.PRODUCTIVITY],
  authors: ["kishanprmr", "MoShizzle", "abuaboud"],
  actions: [
    createTask,
    (0, import_pieces_common3.createCustomApiCallAction)({
      baseUrl: () => "https://openapi.niftypm.com/api/v1.0",
      auth: niftyAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.access_token}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  nifty
});
