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

// ../ap/packages/pieces/community/descript/src/index.ts
var index_exports = {};
__export(index_exports, {
  descript: () => descript,
  descriptAuth: () => descriptAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common10 = require("@activepieces/pieces-common"), import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_framework11 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/descript/src/lib/actions/agent-edit.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/descript/src/lib/auth.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework"), descriptAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Token",
  description: [
    "To create an API token:",
    "1. Open **Settings** in Descript.",
    "2. Select **API tokens** from the sidebar.",
    "3. Click **Create token**, give it a name, select a Drive, and click **Create token**.",
    "4. Copy the token and paste it here \u2014 you can only view it once."
  ].join(`
`),
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: "https://descriptapi.com/v1/status",
        headers: {
          Authorization: `Bearer ${getAuthToken(auth)}`
        }
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API token. Please check and try again."
      };
    }
  }
});
function getAuthToken(auth) {
  return (typeof auth == "string" ? auth : auth.secret_text).trim();
}

// ../ap/packages/pieces/community/descript/src/lib/common/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var BASE_URL = "https://descriptapi.com/v1";
async function descriptApiCall({
  apiKey,
  method,
  path,
  body,
  queryParams
}) {
  return import_pieces_common2.httpClient.sendRequest({
    method,
    url: `${BASE_URL}${path}`,
    headers: {
      Authorization: `Bearer ${apiKey}`
    },
    body,
    queryParams
  });
}
async function fetchAllProjects(apiKey) {
  let projects = [], cursor;
  do {
    let params = {
      limit: "100",
      sort: "name",
      direction: "asc"
    };
    cursor && (params.cursor = cursor);
    let response = await descriptApiCall({
      apiKey,
      method: import_pieces_common2.HttpMethod.GET,
      path: "/projects",
      queryParams: params
    });
    projects.push(...response.body.data), cursor = response.body.pagination.next_cursor;
  } while (cursor);
  return projects;
}
var projectIdProp = import_pieces_framework2.Property.Dropdown({
  auth: descriptAuth,
  displayName: "Project",
  description: "Select the Descript project to use.",
  refreshers: [],
  required: !0,
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first"
      };
    try {
      return {
        disabled: !1,
        options: (await fetchAllProjects(getAuthToken(auth))).map((p) => ({ label: p.name, value: p.id }))
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load projects. Check your connection."
      };
    }
  }
}), compositionIdProp = (required) => import_pieces_framework2.Property.Dropdown({
  auth: descriptAuth,
  displayName: "Composition",
  description: "Select the composition (timeline) within the project. If omitted, the agent chooses automatically.",
  refreshers: ["project_id"],
  required,
  options: async ({ auth, project_id }) => {
    if (!auth || !project_id || typeof project_id != "string")
      return {
        disabled: !0,
        options: [],
        placeholder: "Please select a project first"
      };
    try {
      return {
        disabled: !1,
        options: (await descriptApiCall({
          apiKey: getAuthToken(auth),
          method: import_pieces_common2.HttpMethod.GET,
          path: `/projects/${project_id}`
        })).body.compositions.map((c) => ({
          label: c.name,
          value: c.id
        }))
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load compositions. Check your connection."
      };
    }
  }
}), descriptCommon = {
  BASE_URL,
  descriptApiCall,
  fetchAllProjects,
  getAuthToken,
  projectIdProp,
  compositionIdProp
};

// ../ap/packages/pieces/community/descript/src/lib/actions/agent-edit.ts
var descriptAgentEditAction = (0, import_pieces_framework3.createAction)({
  auth: descriptAuth,
  name: "agent_edit",
  classification: "WRITE",
  displayName: "Agent Edit (Underlord)",
  description: `Use Descript's AI agent "Underlord" to edit an existing project or create a new one with a natural language prompt.`,
  audience: "both",
  aiMetadata: { description: "Starts an asynchronous Descript Underlord AI editing job driven by a natural-language prompt, operating in one of two modes: editing an existing project (optionally targeting a specific composition) or creating a new project from the prompt. Use to apply AI edits like removing filler words, adding studio sound, or assembling highlight reels. Frame the prompt as a single one-shot instruction. Not idempotent: each call queues a new job and returns a job_id to poll for completion.", idempotent: !1 },
  props: {
    mode: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Mode",
      description: "Choose whether to edit an existing project or create a brand new one.",
      required: !0,
      defaultValue: "existing",
      options: {
        options: [
          { label: "Edit existing project", value: "existing" },
          { label: "Create new project from prompt", value: "new" }
        ]
      }
    }),
    project_id: import_pieces_framework3.Property.Dropdown({
      auth: descriptAuth,
      displayName: "Project",
      description: "Select the project for the agent to edit.",
      refreshers: ["mode"],
      required: !1,
      options: async ({ auth, mode }) => {
        if (mode !== "existing")
          return {
            disabled: !0,
            options: [],
            placeholder: "Not needed for new projects"
          };
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        try {
          return {
            disabled: !1,
            options: (await descriptCommon.fetchAllProjects(
              descriptCommon.getAuthToken(auth)
            )).map((p) => ({ label: p.name, value: p.id }))
          };
        } catch (e) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load projects. Check your connection."
          };
        }
      }
    }),
    project_name: import_pieces_framework3.Property.ShortText({
      displayName: "New Project Name",
      description: "Name for the new project (only used when creating a new project).",
      required: !1
    }),
    team_access: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Team Access",
      description: "Access level for team members on the new project. Only applies when creating a new project.",
      required: !1,
      defaultValue: "none",
      options: {
        options: [
          { label: "None (private to owner)", value: "none" },
          { label: "View", value: "view" },
          { label: "Comment", value: "comment" },
          { label: "Edit", value: "edit" }
        ]
      }
    }),
    composition_id: descriptCommon.compositionIdProp(!1),
    prompt: import_pieces_framework3.Property.LongText({
      displayName: "Prompt",
      description: 'Natural language instruction for Underlord. Examples: "Add studio sound to every clip", "Remove all filler words", "Create a 30-second highlight reel with the best moments". Be specific \u2014 frame edits as a single one-shot instruction.',
      required: !0
    }),
    model: import_pieces_framework3.Property.ShortText({
      displayName: "AI Model",
      description: "AI model to use for editing. Leave blank to use the Descript default.",
      required: !1
    }),
    callback_url: import_pieces_framework3.Property.ShortText({
      displayName: "Callback URL",
      description: "Optional webhook URL. When the job finishes or fails, Descript will POST the job status to this URL (same payload as Get Job Status).",
      required: !1
    })
  },
  async run(context) {
    let {
      mode,
      project_id,
      project_name,
      team_access,
      composition_id,
      prompt,
      model,
      callback_url
    } = context.propsValue, body = { prompt };
    if (mode === "existing") {
      if (!project_id) throw new Error("Please select a project to edit.");
      body.project_id = project_id, composition_id && (body.composition_id = composition_id);
    } else {
      if (!project_name)
        throw new Error("Please enter a project name for the new project.");
      body.project_name = project_name, team_access && (body.team_access = team_access);
    }
    return model && (body.model = model), callback_url && (body.callback_url = callback_url), (await descriptCommon.descriptApiCall({
      apiKey: descriptCommon.getAuthToken(context.auth),
      method: import_pieces_common3.HttpMethod.POST,
      path: "/jobs/agent",
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/descript/src/lib/actions/get-job-status.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var descriptGetJobStatusAction = (0, import_pieces_framework4.createAction)({
  auth: descriptAuth,
  name: "get_job_status",
  classification: "READ",
  displayName: "Get Job Status",
  description: "Retrieves the current status of a Descript background job (import, agent edit, or publish). Use the job_id returned by any job-creation action.",
  audience: "both",
  aiMetadata: { description: "Looks up the current state and result of a single Descript background job (import, agent edit, or publish) by its job_id. Use to poll for completion after starting an asynchronous job and to read outputs such as share/download URLs or error messages. Read-only and idempotent.", idempotent: !0 },
  props: {
    job_id: import_pieces_framework4.Property.ShortText({
      displayName: "Job ID",
      description: 'The job ID returned when you created the job (e.g. from "Import Media", "Agent Edit", or "Publish Project" actions).',
      required: !0
    })
  },
  async run(context) {
    return (await descriptCommon.descriptApiCall({
      apiKey: descriptCommon.getAuthToken(context.auth),
      method: import_pieces_common4.HttpMethod.GET,
      path: `/jobs/${context.propsValue.job_id}`
    })).body;
  }
});

// ../ap/packages/pieces/community/descript/src/lib/actions/get-project.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var descriptGetProjectAction = (0, import_pieces_framework5.createAction)({
  auth: descriptAuth,
  name: "get_project",
  classification: "READ",
  displayName: "Get Project",
  description: "Retrieves details for a Descript project including its compositions and media files.",
  audience: "both",
  aiMetadata: { description: "Fetches full details for a single Descript project by ID, including its compositions and media files. Use to resolve composition IDs or inspect project contents before editing or publishing. Read-only and idempotent.", idempotent: !0 },
  props: {
    project_id: descriptCommon.projectIdProp
  },
  async run(context) {
    return (await descriptCommon.descriptApiCall({
      apiKey: descriptCommon.getAuthToken(context.auth),
      method: import_pieces_common5.HttpMethod.GET,
      path: `/projects/${context.propsValue.project_id}`
    })).body;
  }
});

// ../ap/packages/pieces/community/descript/src/lib/actions/import-media.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var descriptImportMediaAction = (0, import_pieces_framework6.createAction)({
  auth: descriptAuth,
  name: "import_media",
  classification: "WRITE",
  displayName: "Import Media",
  description: "Imports a media file from a URL into a new or existing Descript project and optionally creates a composition.",
  audience: "both",
  aiMetadata: { description: "Starts an asynchronous Descript job that imports a media file from a public URL into either a newly created project or an existing one (selected via the project mode), optionally building a composition/timeline from it. Use to bring audio/video into Descript before editing or publishing; the URL must be reachable by Descript servers and support HTTP Range requests. Not idempotent: each call queues a new import job and returns a job_id to poll for completion.", idempotent: !1 },
  props: {
    mode: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Project",
      description: "Create a new project or import into an existing one.",
      required: !0,
      defaultValue: "new",
      options: {
        options: [
          { label: "Create new project", value: "new" },
          { label: "Import into existing project", value: "existing" }
        ]
      }
    }),
    project_id: import_pieces_framework6.Property.Dropdown({
      auth: descriptAuth,
      displayName: "Existing Project",
      description: "Select the project to import media into.",
      refreshers: ["mode"],
      required: !1,
      options: async ({ auth, mode }) => {
        if (mode !== "existing") return { disabled: !0, options: [], placeholder: "Not needed for new projects" };
        if (!auth) return { disabled: !0, options: [], placeholder: "Please connect your account first" };
        try {
          return {
            disabled: !1,
            options: (await descriptCommon.fetchAllProjects(
              descriptCommon.getAuthToken(auth)
            )).map((p) => ({ label: p.name, value: p.id }))
          };
        } catch (e) {
          return { disabled: !0, options: [], placeholder: "Failed to load projects. Check your connection." };
        }
      }
    }),
    project_name: import_pieces_framework6.Property.ShortText({
      displayName: "New Project Name",
      description: "Name for the new project. Only used when creating a new project.",
      required: !1
    }),
    folder_name: import_pieces_framework6.Property.ShortText({
      displayName: "Project Folder",
      description: 'Folder path for the new project (e.g. "Clients/Acme"). Supports nested paths using "/". Missing folders are created automatically. Only applies when creating a new project.',
      required: !1
    }),
    team_access: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Team Access",
      description: "Access level for Drive members on the new project. Only applies when creating a new project.",
      required: !1,
      defaultValue: "none",
      options: {
        options: [
          { label: "None (private to owner)", value: "none" },
          { label: "View", value: "view" },
          { label: "Comment", value: "comment" },
          { label: "Edit", value: "edit" }
        ]
      }
    }),
    media_name: import_pieces_framework6.Property.ShortText({
      displayName: "Media File Name",
      description: 'Display name for the media file (e.g. "intro.mp4" or "Recordings/interview.mp4"). Use "/" to organize into sub-folders. Must not conflict with existing file names when importing into an existing project.',
      required: !0
    }),
    media_url: import_pieces_framework6.Property.ShortText({
      displayName: "Media URL",
      description: "Public URL of the media file. Must be accessible by Descript servers and support HTTP Range requests. Sign URLs for 12\u201348 hours if they expire.",
      required: !0
    }),
    language: import_pieces_framework6.Property.ShortText({
      displayName: "Transcription Language",
      description: 'ISO 639-1 language code for transcription (e.g. "en", "es", "fr"). Leave blank to auto-detect from the audio.',
      required: !1
    }),
    composition_name: import_pieces_framework6.Property.ShortText({
      displayName: "Composition Name",
      description: "Name for the composition (timeline) to create from the imported media. Leave blank to skip composition creation.",
      required: !1
    }),
    composition_width: import_pieces_framework6.Property.Number({
      displayName: "Composition Width (px)",
      description: "Width of the composition in pixels. Defaults to 1920. Only used when a Composition Name is provided.",
      required: !1,
      defaultValue: 1920
    }),
    composition_height: import_pieces_framework6.Property.Number({
      displayName: "Composition Height (px)",
      description: "Height of the composition in pixels. Defaults to 1080. Only used when a Composition Name is provided.",
      required: !1,
      defaultValue: 1080
    }),
    callback_url: import_pieces_framework6.Property.ShortText({
      displayName: "Callback URL",
      description: "Optional webhook URL. When the import finishes or fails, Descript will POST the job status to this URL (same payload as Get Job Status).",
      required: !1
    })
  },
  async run(context) {
    let {
      mode,
      project_id,
      project_name,
      folder_name,
      team_access,
      media_name,
      media_url,
      language,
      composition_name,
      composition_width,
      composition_height,
      callback_url
    } = context.propsValue, mediaItem = { url: media_url };
    language && (mediaItem.language = language);
    let body = {
      add_media: { [media_name]: mediaItem }
    };
    if (mode === "existing") {
      if (!project_id) throw new Error("Please select a project to import into.");
      body.project_id = project_id;
    } else {
      if (!project_name) throw new Error("Please enter a name for the new project.");
      body.project_name = project_name, folder_name && (body.folder_name = folder_name), team_access && (body.team_access = team_access);
    }
    return composition_name && (body.add_compositions = [
      {
        name: composition_name,
        width: composition_width != null ? composition_width : 1920,
        height: composition_height != null ? composition_height : 1080,
        clips: [{ media: media_name }]
      }
    ]), callback_url && (body.callback_url = callback_url), (await descriptCommon.descriptApiCall({
      apiKey: descriptCommon.getAuthToken(context.auth),
      method: import_pieces_common6.HttpMethod.POST,
      path: "/jobs/import/project_media",
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/descript/src/lib/actions/list-projects.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var descriptListProjectsAction = (0, import_pieces_framework7.createAction)({
  auth: descriptAuth,
  name: "list_projects",
  classification: "SEARCH",
  displayName: "List Projects",
  description: "Returns projects in your Descript Drive. Supports filtering by name, date, and creator.",
  audience: "both",
  aiMetadata: { description: "Lists projects in the connected Descript Drive, auto-paginating through all matches. Optional filters narrow by name substring, creator (me vs anyone), and created/updated date ranges, with configurable sort field and direction; leaving filters blank returns all projects. Use to discover project IDs for other actions. Read-only and idempotent.", idempotent: !0 },
  props: {
    name: import_pieces_framework7.Property.ShortText({
      displayName: "Name Filter",
      description: "Return only projects whose name contains this string (case-insensitive).",
      required: !1
    }),
    created_by: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Created By",
      description: "Filter projects by creator.",
      required: !1,
      options: {
        options: [
          { label: "Anyone", value: "" },
          { label: "Me", value: "me" }
        ]
      }
    }),
    sort: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Sort By",
      description: "Field to sort results by.",
      required: !1,
      defaultValue: "created_at",
      options: {
        options: [
          { label: "Date created", value: "created_at" },
          { label: "Date updated", value: "updated_at" },
          { label: "Name", value: "name" },
          { label: "Last viewed", value: "last_viewed_at" }
        ]
      }
    }),
    direction: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Sort Direction",
      description: "Sort order for results.",
      required: !1,
      defaultValue: "desc",
      options: {
        options: [
          { label: "Newest first (desc)", value: "desc" },
          { label: "Oldest first (asc)", value: "asc" }
        ]
      }
    }),
    created_after: import_pieces_framework7.Property.ShortText({
      displayName: "Created After",
      description: "Return only projects created after this date (ISO 8601, e.g. 2025-01-01T00:00:00Z).",
      required: !1
    }),
    created_before: import_pieces_framework7.Property.ShortText({
      displayName: "Created Before",
      description: "Return only projects created before this date (ISO 8601, e.g. 2025-12-31T23:59:59Z).",
      required: !1
    }),
    updated_after: import_pieces_framework7.Property.ShortText({
      displayName: "Updated After",
      description: "Return only projects updated after this date (ISO 8601, e.g. 2025-01-01T00:00:00Z).",
      required: !1
    }),
    updated_before: import_pieces_framework7.Property.ShortText({
      displayName: "Updated Before",
      description: "Return only projects updated before this date (ISO 8601, e.g. 2025-12-31T23:59:59Z).",
      required: !1
    })
  },
  async run(context) {
    let {
      name,
      created_by,
      sort,
      direction,
      created_after,
      created_before,
      updated_after,
      updated_before
    } = context.propsValue, apiKey = descriptCommon.getAuthToken(context.auth), baseParams = { limit: "100" };
    sort && (baseParams.sort = sort), direction && (baseParams.direction = direction), name && (baseParams.name = name), created_by && (baseParams.created_by = created_by), created_after && (baseParams.created_after = created_after), created_before && (baseParams.created_before = created_before), updated_after && (baseParams.updated_after = updated_after), updated_before && (baseParams.updated_before = updated_before);
    let projects = [], cursor;
    do {
      let queryParams = __spreadValues(__spreadValues({}, baseParams), cursor ? { cursor } : {}), response = await descriptCommon.descriptApiCall({
        apiKey,
        method: import_pieces_common7.HttpMethod.GET,
        path: "/projects",
        queryParams
      });
      projects.push(...response.body.data), cursor = response.body.pagination.next_cursor;
    } while (cursor);
    return projects.map((p) => ({
      project_id: p.id,
      project_name: p.name,
      created_at: p.created_at,
      updated_at: p.updated_at
    }));
  }
});

// ../ap/packages/pieces/community/descript/src/lib/actions/publish-project.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var descriptPublishProjectAction = (0, import_pieces_framework8.createAction)({
  auth: descriptAuth,
  name: "publish_project",
  classification: "WRITE",
  displayName: "Publish Project",
  description: "Publishes a composition from a project to create a shareable link and a downloadable export file.",
  audience: "both",
  aiMetadata: { description: "Starts an asynchronous Descript publish/export job for a project composition, producing a shareable link and a downloadable file as video or audio at a chosen resolution and share access level. Use to render and distribute a finished project. Requesting a share access level the Drive disallows returns an error. Not idempotent: each call queues a new export job and returns a job_id to poll for the share_url and download_url.", idempotent: !1 },
  props: {
    project_id: descriptCommon.projectIdProp,
    composition_id: descriptCommon.compositionIdProp(!1),
    media_type: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Output Type",
      description: "Choose whether to export the project as a video or audio file.",
      required: !1,
      defaultValue: "Video",
      options: {
        options: [
          { label: "Video", value: "Video" },
          { label: "Audio only", value: "Audio" }
        ]
      }
    }),
    resolution: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Video Resolution",
      description: "Resolution for the exported video. Only applies when Output Type is Video.",
      required: !1,
      defaultValue: "1080p",
      options: {
        options: [
          { label: "480p", value: "480p" },
          { label: "720p", value: "720p" },
          { label: "1080p (Full HD)", value: "1080p" },
          { label: "1440p (2K)", value: "1440p" },
          { label: "4K", value: "4K" }
        ]
      }
    }),
    access_level: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Share Access Level",
      description: "Who can view the published share page. Leave blank to use your Drive's default. Note: requesting a level your Drive disallows (e.g. Public when search-engine indexing is off) will return an error.",
      required: !1,
      options: {
        options: [
          { label: "Public (anyone)", value: "public" },
          { label: "Unlisted (link only)", value: "unlisted" },
          { label: "Drive members only", value: "drive" },
          { label: "Private (owner only)", value: "private" }
        ]
      }
    }),
    callback_url: import_pieces_framework8.Property.ShortText({
      displayName: "Callback URL",
      description: "Optional webhook URL. When the publish job finishes or fails, Descript will POST the job status to this URL (same payload as Get Job Status). The result includes share_url and download_url once complete.",
      required: !1
    })
  },
  async run(context) {
    let {
      project_id,
      composition_id,
      media_type,
      resolution,
      access_level,
      callback_url
    } = context.propsValue, body = { project_id };
    return composition_id && (body.composition_id = composition_id), media_type && (body.media_type = media_type), media_type === "Video" && resolution && (body.resolution = resolution), access_level && (body.access_level = access_level), callback_url && (body.callback_url = callback_url), (await descriptCommon.descriptApiCall({
      apiKey: descriptCommon.getAuthToken(context.auth),
      method: import_pieces_common8.HttpMethod.POST,
      path: "/jobs/publish",
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/descript/src/lib/triggers/job-completed.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var TERMINAL_STATES = /* @__PURE__ */ new Set(["stopped", "cancelled"]);
function isCompletedJob(job) {
  return TERMINAL_STATES.has(job.job_state);
}
var SAMPLE_DATA = {
  job_id: "6dc3f30a-58c2-4174-96a6-dc18cf3c7776",
  job_type: "import/project_media",
  job_state: "stopped",
  created_at: "2025-11-18T10:30:00Z",
  stopped_at: "2025-11-18T10:35:00Z",
  drive_id: "c9c5c47e-158a-49f7-846b-4f6ee2a229a2",
  project_id: "9f36ee32-5a2c-47e7-b1a3-94991d3e3ddb",
  project_url: "https://web.descript.com/9f36ee32-5a2c-47e7-b1a3-94991d3e3ddb",
  result: {
    status: "success",
    media_seconds_used: 136
  }
}, polling = {
  strategy: import_pieces_common9.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => {
    let { job_type_filter } = propsValue, apiKey = descriptCommon.getAuthToken(auth), params = { limit: "100" };
    job_type_filter && job_type_filter !== "all" && (params.type = job_type_filter), lastFetchEpochMS > 0 && (params.created_after = new Date(lastFetchEpochMS).toISOString());
    let allJobs = [], cursor, pageCount = 0, maxPages = 10;
    do {
      pageCount++;
      let queryParams = __spreadValues(__spreadValues({}, params), cursor ? { cursor } : {}), response = await descriptCommon.descriptApiCall({
        apiKey,
        method: import_pieces_common9.HttpMethod.GET,
        path: "/jobs",
        queryParams
      });
      allJobs.push(...response.body.data), cursor = response.body.pagination.next_cursor;
    } while (cursor && pageCount < maxPages);
    return allJobs.filter(isCompletedJob).map((job) => {
      var _a;
      return {
        epochMilliSeconds: new Date((_a = job.stopped_at) != null ? _a : job.created_at).getTime(),
        data: __spreadValues({}, job)
      };
    });
  }
}, descriptJobCompletedTrigger = (0, import_pieces_framework9.createTrigger)({
  auth: descriptAuth,
  name: "job_completed",
  classification: "READ",
  displayName: "Job Completed",
  description: "Triggers when a Descript background job (import, agent edit, or publish) finishes.",
  aiMetadata: {
    description: "Fires when a Descript background job reaches a terminal state (completed, failed, or cancelled), representing a finished import, agent edit, or publish job. Can be scoped to a single job type or left open to fire for any completed job type."
  },
  props: {
    job_type_filter: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Job Type Filter",
      description: 'Only trigger for jobs of this type. Select "All job types" to trigger for any completed job.',
      required: !1,
      defaultValue: "all",
      options: {
        options: [
          { label: "All job types", value: "all" },
          { label: "Import media", value: "import/project_media" },
          { label: "Agent edit (Underlord)", value: "agent" }
        ]
      }
    })
  },
  sampleData: SAMPLE_DATA,
  type: import_pieces_framework9.TriggerStrategy.POLLING,
  async test() {
    return [SAMPLE_DATA];
  },
  async onEnable(context) {
    await import_pieces_common9.pollingHelper.onEnable(polling, context);
  },
  async onDisable(context) {
    await import_pieces_common9.pollingHelper.onDisable(polling, context);
  },
  async run(context) {
    return await import_pieces_common9.pollingHelper.poll(polling, context);
  }
});

// ../ap/packages/pieces/community/descript/src/index.ts
var descript = (0, import_pieces_framework10.createPiece)({
  displayName: "Descript",
  description: "AI-powered video and podcast editor. Import media, run AI edits with Underlord, and publish.",
  minimumSupportedRelease: "0.82.1",
  logoUrl: "https://cdn.activepieces.com/pieces/descript.png",
  categories: [import_pieces_framework11.PieceCategory.CONTENT_AND_FILES],
  auth: descriptAuth,
  authors: ["hugh-codes", "onyedikachi-david"],
  actions: [
    descriptImportMediaAction,
    descriptAgentEditAction,
    descriptPublishProjectAction,
    descriptGetJobStatusAction,
    descriptListProjectsAction,
    descriptGetProjectAction,
    (0, import_pieces_common10.createCustomApiCallAction)({
      baseUrl: () => "https://descriptapi.com/v1",
      auth: descriptAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${getAuthToken(auth)}`
      })
    })
  ],
  triggers: [descriptJobCompletedTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  descript,
  descriptAuth
});
