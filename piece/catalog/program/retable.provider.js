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

// ../ap/packages/pieces/community/retable/src/index.ts
var index_exports = {};
__export(index_exports, {
  retable: () => retable,
  retableAuth: () => retableAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_framework11 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/retable/src/lib/actions/create-project.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/retable/src/lib/common/index.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/retable/src/lib/common/models.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), RetableFieldMapping = {
  text: import_pieces_framework.Property.ShortText,
  updated_by: import_pieces_framework.Property.ShortText,
  updated_at: import_pieces_framework.Property.ShortText,
  created_by: import_pieces_framework.Property.ShortText,
  created_at: import_pieces_framework.Property.ShortText,
  user: import_pieces_framework.Property.ShortText,
  url: import_pieces_framework.Property.ShortText,
  formula: import_pieces_framework.Property.ShortText,
  rating: import_pieces_framework.Property.ShortText,
  dropdown: import_pieces_framework.Property.ShortText,
  percent: import_pieces_framework.Property.ShortText,
  email: import_pieces_framework.Property.ShortText,
  phonenumber: import_pieces_framework.Property.ShortText,
  currency: import_pieces_framework.Property.ShortText,
  color: import_pieces_framework.Property.ShortText,
  calendar: import_pieces_framework.Property.ShortText,
  checkbox: import_pieces_framework.Property.Checkbox,
  number: import_pieces_framework.Property.ShortText
}, RetableNotSupportedFields = [
  "attachment",
  "image",
  "updated_by",
  "updated_at",
  "created_by",
  "created_at",
  "user",
  "vote",
  "qr_code",
  "richtext"
];

// ../ap/packages/pieces/community/retable/src/lib/common/index.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var retableCommon = {
  baseUrl: "https://api.retable.io/v1/public",
  workspace_id: (required = !0) => import_pieces_framework2.Property.Dropdown({
    auth: retableAuth,
    displayName: "Workspace",
    required,
    refreshers: [],
    options: async ({ auth }) => auth ? {
      disabled: !1,
      options: (await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${retableCommon.baseUrl}/workspace`,
        headers: {
          ApiKey: auth.secret_text
        }
      })).body.data.workspaces.map((workspace) => ({
        label: workspace.name,
        value: workspace.id
      }))
    } : {
      disabled: !0,
      options: [],
      placeholder: "Please connect your account"
    }
  }),
  project_id: (required = !0) => import_pieces_framework2.Property.Dropdown({
    auth: retableAuth,
    displayName: "Project",
    required,
    refreshers: ["workspace_id"],
    options: async ({ auth, workspace_id }) => !auth || !workspace_id ? {
      disabled: !0,
      options: [],
      placeholder: "Please connect your account and select workspace"
    } : {
      disabled: !1,
      options: (await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${retableCommon.baseUrl}/workspace/${workspace_id}/project`,
        headers: {
          ApiKey: auth.secret_text
        }
      })).body.data.projects.map((project) => ({
        label: project.name,
        value: project.id
      }))
    }
  }),
  retable_id: (required = !0) => import_pieces_framework2.Property.Dropdown({
    auth: retableAuth,
    displayName: "Retable",
    required,
    refreshers: ["project_id"],
    options: async ({ auth, project_id }) => !auth || !project_id ? {
      disabled: !0,
      options: [],
      placeholder: "Please connect your account and select project"
    } : {
      disabled: !1,
      options: (await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${retableCommon.baseUrl}/project/${project_id}/retable`,
        headers: {
          ApiKey: auth.secret_text
        }
      })).body.data.retables.map((retable2) => ({
        label: retable2.title,
        value: retable2.id
      }))
    }
  }),
  fields: import_pieces_framework2.Property.DynamicProperties({
    auth: retableAuth,
    displayName: "Fields",
    required: !0,
    refreshers: ["retable_id"],
    props: async ({ auth, retable_id }) => {
      if (!auth || !retable_id)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please connect your account and select retable"
        };
      let fields = {};
      return (await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${retableCommon.baseUrl}/retable/${retable_id}`,
        headers: {
          ApiKey: auth
        }
      })).body.data.columns.forEach((field) => {
        if (!RetableNotSupportedFields.includes(field.type)) {
          let params = {
            displayName: field.title,
            required: !1
          };
          (0, import_pieces_framework3.isNil)(RetableFieldMapping[field.type]) ? fields[field.column_id] = import_pieces_framework2.Property.ShortText(__spreadValues({}, params)) : fields[field.column_id] = RetableFieldMapping[field.type](params);
        }
      }), fields;
    }
  })
};

// ../ap/packages/pieces/community/retable/src/lib/actions/create-project.ts
var retableCreateProjectAction = (0, import_pieces_framework4.createAction)({
  auth: retableAuth,
  name: "retable_create_project",
  classification: "WRITE",
  displayName: "Create a Project",
  description: "Creates a project in the given workspace",
  audience: "both",
  aiMetadata: { description: "Creates a new project inside an existing Retable workspace, with a name and optional description and color. Use to set up a project container for retables; requires the target workspace ID. Not idempotent \u2014 each call creates another project.", idempotent: !1 },
  props: {
    workspace_id: retableCommon.workspace_id(),
    name: import_pieces_framework4.Property.ShortText({
      displayName: "Project Name",
      required: !0
    }),
    desc: import_pieces_framework4.Property.LongText({
      displayName: "Project Description",
      required: !1
    }),
    color: import_pieces_framework4.Property.ShortText({
      displayName: "Project Color",
      required: !1
    })
  },
  async run(context) {
    let { workspace_id, name, desc, color } = context.propsValue;
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: `${retableCommon.baseUrl}/workspace/${workspace_id}/project`,
      headers: {
        ApiKey: context.auth.secret_text
      },
      body: {
        name,
        description: desc,
        color
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/retable/src/lib/actions/create-workspace.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var retableCreateWorkspaceAction = (0, import_pieces_framework5.createAction)({
  auth: retableAuth,
  name: "retable_create_workspace",
  classification: "WRITE",
  displayName: "Create a Workspace",
  description: "Creates a workspace",
  audience: "both",
  aiMetadata: { description: "Creates a new top-level Retable workspace with the given name and optional description. Use when an agent needs a fresh workspace to organize projects under. Not idempotent \u2014 each call creates another workspace, even with the same name.", idempotent: !1 },
  props: {
    name: import_pieces_framework5.Property.ShortText({
      displayName: "Workspace Name",
      required: !0
    }),
    desc: import_pieces_framework5.Property.LongText({
      displayName: "Workspace Description",
      required: !1
    })
  },
  async run(context) {
    let { name, desc } = context.propsValue;
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: `${retableCommon.baseUrl}/workspace`,
      headers: {
        ApiKey: context.auth.secret_text
      },
      body: {
        name,
        description: desc
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/retable/src/lib/actions/get-all-projects.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var retableGetAllProjectsAction = (0, import_pieces_framework6.createAction)({
  auth: retableAuth,
  name: "retable_get_projects",
  classification: "SEARCH",
  displayName: "Get Projects",
  description: "Gets all projects in given workspace",
  audience: "both",
  aiMetadata: { description: "Lists all projects within a given Retable workspace. Use to discover project IDs after selecting a workspace, before fetching retables or records. Requires the workspace ID; read-only and idempotent.", idempotent: !0 },
  props: {
    workspace_id: retableCommon.workspace_id()
  },
  async run(context) {
    let { workspace_id } = context.propsValue;
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.GET,
      url: `${retableCommon.baseUrl}/workspace/${workspace_id}/project`,
      headers: {
        ApiKey: context.auth.secret_text
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/retable/src/lib/actions/get-all-retables.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var retableGetAllRetablesAction = (0, import_pieces_framework7.createAction)({
  auth: retableAuth,
  name: "retable_get_retables",
  classification: "SEARCH",
  displayName: "Get Retables",
  description: "Gets all retables in given project",
  audience: "both",
  aiMetadata: { description: "Lists all retables (tables) within a given Retable project. Use to discover retable IDs needed before reading or writing records. Requires the project ID; read-only and idempotent.", idempotent: !0 },
  props: {
    workspace_id: retableCommon.workspace_id(),
    project_id: retableCommon.project_id()
  },
  async run(context) {
    let { project_id } = context.propsValue;
    return (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.GET,
      url: `${retableCommon.baseUrl}/project/${project_id}/retable`,
      headers: {
        ApiKey: context.auth.secret_text
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/retable/src/lib/actions/get-all-workspaces.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var retableGetAllWorkspacesAction = (0, import_pieces_framework8.createAction)({
  auth: retableAuth,
  name: "retable_get_workspaces",
  classification: "SEARCH",
  displayName: "Get Workspaces",
  description: "Gets all workspaces",
  audience: "both",
  aiMetadata: { description: "Lists all Retable workspaces accessible with the connected API key. Use as the entry point to discover workspace IDs needed by project, retable, and record actions. Takes no input; read-only and idempotent.", idempotent: !0 },
  props: {},
  async run(context) {
    return (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.GET,
      url: `${retableCommon.baseUrl}/workspace`,
      headers: {
        ApiKey: context.auth.secret_text
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/retable/src/lib/actions/insert-record.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var retableCreateRecordAction = (0, import_pieces_framework9.createAction)({
  auth: retableAuth,
  name: "retable_create_record",
  classification: "WRITE",
  displayName: "Create Retable Record",
  description: "Adds a record into a retable",
  audience: "both",
  aiMetadata: { description: "Appends a new row to a Retable table, mapping the supplied column values into a single record. Use to write data into a specific retable; requires the workspace, project, and retable identifiers plus the field values (empty cell values are skipped). Not idempotent \u2014 each call inserts another record.", idempotent: !1 },
  props: {
    workspace_id: retableCommon.workspace_id(),
    project_id: retableCommon.project_id(),
    retable_id: retableCommon.retable_id(),
    fields: retableCommon.fields
  },
  async run(context) {
    let { retable_id } = context.propsValue, fields = context.propsValue.fields, outputData = Object.entries(fields).map(([column_id, cell_value]) => cell_value !== "" ? {
      column_id,
      cell_value
    } : null).filter((entry) => entry !== null);
    return (await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.POST,
      url: `${retableCommon.baseUrl}/retable/${retable_id}/data`,
      headers: {
        ApiKey: context.auth.secret_text
      },
      body: {
        data: [{ columns: outputData }]
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/retable/src/index.ts
var markdown = `
To obtain your API key, follow these steps:

1. Go to Account Overview by clicking your profile-pic (top-right).
2. Go to API section and enable API key.
3. Copy API key.`, retableAuth = import_pieces_framework10.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: markdown,
  validate: async ({ auth }) => auth.startsWith("RTBLv1-") ? {
    valid: !0
  } : {
    valid: !1,
    error: "Invalid API Key"
  }
}), retable = (0, import_pieces_framework10.createPiece)({
  displayName: "Retable",
  description: "Turn your spreadsheets into smart database apps",
  auth: retableAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/retable.png",
  categories: [import_pieces_framework11.PieceCategory.PRODUCTIVITY],
  authors: ["kishanprmr", "MoShizzle", "abuaboud"],
  actions: [
    retableCreateRecordAction,
    retableGetAllWorkspacesAction,
    retableGetAllProjectsAction,
    retableGetAllRetablesAction,
    retableCreateWorkspaceAction,
    retableCreateProjectAction,
    (0, import_pieces_common8.createCustomApiCallAction)({
      baseUrl: () => retableCommon.baseUrl,
      auth: retableAuth,
      authMapping: async (auth) => ({
        ApiKey: auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  retable,
  retableAuth
});
