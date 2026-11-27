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

// ../ap/packages/pieces/community/wistia/src/index.ts
var index_exports = {};
__export(index_exports, {
  wistia: () => wistia,
  wistiaAuth: () => wistiaAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common11 = require("@activepieces/pieces-common"), import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_framework12 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/wistia/src/lib/actions/copy-media.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/wistia/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");
var BASE_URL = "https://api.wistia.com/v1";
async function wistiaApiCall({
  token,
  method,
  resourceUrl,
  query,
  body
}) {
  let queryParams = {};
  if (query)
    for (let [key, value] of Object.entries(query))
      value != null && value !== "" && (queryParams[key] = String(value));
  return import_pieces_common.httpClient.sendRequest({
    method,
    url: `${BASE_URL}${resourceUrl}`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token
    },
    queryParams,
    body
  });
}
function getAuthToken(auth) {
  return auth.secret_text;
}
var wistiaCommon = {
  projectDropdown: (required) => import_pieces_framework.Property.Dropdown({
    displayName: "Project",
    description: "The project to use.",
    auth: wistiaAuth,
    required,
    refreshers: [],
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please connect your Wistia account first."
        };
      try {
        return {
          disabled: !1,
          options: (await wistiaApiCall({
            token: getAuthToken(auth),
            method: import_pieces_common.HttpMethod.GET,
            resourceUrl: "/projects.json",
            query: { per_page: 100, sort_by: "name", sort_direction: 1 }
          })).body.map((project) => ({
            label: `${project.name} (${project.mediaCount} media)`,
            value: project.hashedId
          }))
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
  projectIdDropdown: (required) => import_pieces_framework.Property.Dropdown({
    displayName: "Project",
    description: "Only return media that belong to this project.",
    auth: wistiaAuth,
    required,
    refreshers: [],
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please connect your Wistia account first."
        };
      try {
        return {
          disabled: !1,
          options: (await wistiaApiCall({
            token: getAuthToken(auth),
            method: import_pieces_common.HttpMethod.GET,
            resourceUrl: "/projects.json",
            query: { per_page: 100, sort_by: "name", sort_direction: 1 }
          })).body.map((project) => ({
            label: `${project.name} (${project.mediaCount} media)`,
            value: project.id
          }))
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
  mediaDropdown: (required) => import_pieces_framework.Property.Dropdown({
    displayName: "Media",
    description: "The video or media file to use.",
    auth: wistiaAuth,
    required,
    refreshers: [],
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please connect your Wistia account first."
        };
      try {
        return {
          disabled: !1,
          options: (await wistiaApiCall({
            token: getAuthToken(auth),
            method: import_pieces_common.HttpMethod.GET,
            resourceUrl: "/medias.json",
            query: { per_page: 100, sort_by: "name", sort_direction: 1 }
          })).body.map((media) => ({
            label: `${media.name} (${media.type})`,
            value: media.hashed_id
          }))
        };
      } catch (e) {
        return {
          disabled: !0,
          options: [],
          placeholder: "Failed to load media. Check your connection."
        };
      }
    }
  })
};
function flattenMedia(media) {
  var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n, _o, _p, _q, _r, _s;
  return {
    id: media.id,
    hashed_id: media.hashed_id,
    name: media.name,
    type: media.type,
    status: (_a = media.status) != null ? _a : null,
    progress: (_b = media.progress) != null ? _b : null,
    section: (_c = media.section) != null ? _c : null,
    description: (_d = media.description) != null ? _d : null,
    duration: (_e = media.duration) != null ? _e : null,
    created: (_f = media.created) != null ? _f : null,
    updated: (_g = media.updated) != null ? _g : null,
    thumbnail_url: (_i = (_h = media.thumbnail) == null ? void 0 : _h.url) != null ? _i : null,
    thumbnail_width: (_k = (_j = media.thumbnail) == null ? void 0 : _j.width) != null ? _k : null,
    thumbnail_height: (_m = (_l = media.thumbnail) == null ? void 0 : _l.height) != null ? _m : null,
    project_id: (_o = (_n = media.project) == null ? void 0 : _n.id) != null ? _o : null,
    project_name: (_q = (_p = media.project) == null ? void 0 : _p.name) != null ? _q : null,
    project_hashed_id: (_s = (_r = media.project) == null ? void 0 : _r.hashed_id) != null ? _s : null
  };
}
function flattenProject(project) {
  var _a, _b, _c, _d, _e, _f, _g, _h;
  return {
    id: project.id,
    hashed_id: project.hashedId,
    name: project.name,
    description: (_a = project.description) != null ? _a : null,
    media_count: (_b = project.mediaCount) != null ? _b : null,
    public: (_c = project.public) != null ? _c : null,
    public_id: (_d = project.publicId) != null ? _d : null,
    anonymous_can_upload: (_e = project.anonymousCanUpload) != null ? _e : null,
    anonymous_can_download: (_f = project.anonymousCanDownload) != null ? _f : null,
    created: (_g = project.created) != null ? _g : null,
    updated: (_h = project.updated) != null ? _h : null
  };
}

// ../ap/packages/pieces/community/wistia/src/lib/actions/copy-media.ts
var copyMediaAction = (0, import_pieces_framework2.createAction)({
  auth: wistiaAuth,
  name: "copy_media",
  displayName: "Copy Media",
  description: "Create a copy of a video or media file, optionally into a different project.",
  audience: "both",
  aiMetadata: {
    description: "Duplicates an existing Wistia media item (referenced by its hashed media id), optionally placing the copy in a target project and assigning a new owner by email. Use to clone a video or asset rather than re-uploading. Not idempotent: each call produces a new, distinct copy.",
    idempotent: !1
  },
  props: {
    mediaId: wistiaCommon.mediaDropdown(!0),
    projectId: wistiaCommon.projectDropdown(!1),
    owner: import_pieces_framework2.Property.ShortText({
      displayName: "New Owner Email",
      description: "The email of the user who will own the copy. Defaults to the owner of the original media if left empty.",
      required: !1
    })
  },
  async run(context) {
    let { mediaId, projectId, owner } = context.propsValue, body = {};
    projectId !== void 0 && (body.project_id = projectId), owner !== void 0 && (body.owner = owner);
    let response = await wistiaApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common2.HttpMethod.POST,
      resourceUrl: `/medias/${mediaId}/copy.json`,
      body
    });
    return flattenMedia(response.body);
  }
});

// ../ap/packages/pieces/community/wistia/src/lib/actions/create-project.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var createProjectAction = (0, import_pieces_framework3.createAction)({
  auth: wistiaAuth,
  name: "create_project",
  displayName: "Create Project",
  description: "Create a new project in your Wistia account.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new Wistia project to hold media, with an optional admin owner email and anonymous-upload/download and public-link visibility flags. Use when an agent needs a new container before adding media. Not idempotent: each call creates a separate project even with the same name.",
    idempotent: !1
  },
  props: {
    name: import_pieces_framework3.Property.ShortText({
      displayName: "Project Name",
      description: "The name of the new project.",
      required: !0
    }),
    adminEmail: import_pieces_framework3.Property.ShortText({
      displayName: "Admin Email",
      description: "The email of the user who will own the project. Defaults to the owner of the API token if left empty.",
      required: !1
    }),
    anonymousCanUpload: import_pieces_framework3.Property.Checkbox({
      displayName: "Allow Anonymous Uploads",
      description: "Allow anonymous (logged-out) users to upload media to this project.",
      required: !1,
      defaultValue: !1
    }),
    anonymousCanDownload: import_pieces_framework3.Property.Checkbox({
      displayName: "Allow Anonymous Downloads",
      description: "Allow anonymous (logged-out) users to download media from this project.",
      required: !1,
      defaultValue: !1
    }),
    public: import_pieces_framework3.Property.Checkbox({
      displayName: "Public",
      description: "Make the project accessible to anyone with the public link.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { name, adminEmail, anonymousCanUpload, anonymousCanDownload, public: isPublic } = context.propsValue, response = await wistiaApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common3.HttpMethod.POST,
      resourceUrl: "/projects.json",
      body: {
        name,
        adminEmail,
        anonymousCanUpload: anonymousCanUpload ? 1 : 0,
        anonymousCanDownload: anonymousCanDownload ? 1 : 0,
        public: isPublic ? 1 : 0
      }
    });
    return flattenProject(response.body);
  }
});

// ../ap/packages/pieces/community/wistia/src/lib/actions/delete-media.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var deleteMediaAction = (0, import_pieces_framework4.createAction)({
  auth: wistiaAuth,
  name: "delete_media",
  displayName: "Delete Media",
  description: "Permanently delete a video or media file from your account.",
  audience: "both",
  aiMetadata: {
    description: "Permanently removes a Wistia media item identified by its hashed media id. Use only when the media should be irreversibly deleted; this cannot be undone. Not idempotent in effect: the first call deletes the media and subsequent calls target a media that no longer exists.",
    idempotent: !1
  },
  props: {
    mediaId: wistiaCommon.mediaDropdown(!0)
  },
  async run(context) {
    return await wistiaApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common4.HttpMethod.DELETE,
      resourceUrl: `/medias/${context.propsValue.mediaId}.json`
    }), { success: !0, hashed_id: context.propsValue.mediaId };
  }
});

// ../ap/packages/pieces/community/wistia/src/lib/actions/find-media.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var findMediaAction = (0, import_pieces_framework5.createAction)({
  auth: wistiaAuth,
  name: "find_media",
  displayName: "Find Media",
  description: "Search and list media in your account, optionally filtered by project, name, or type.",
  audience: "both",
  aiMetadata: {
    description: "Lists media in the account, returning the most recent first up to the limit. With no filters set it fetches recent media broadly; supplying a project, an exact name match, or a media type narrows the results. Use to discover media or resolve a name to a hashed media id. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    projectId: wistiaCommon.projectIdDropdown(!1),
    name: import_pieces_framework5.Property.ShortText({
      displayName: "Name",
      description: "Only return media whose name exactly matches this value.",
      required: !1
    }),
    type: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Type",
      description: "Only return media of this type.",
      required: !1,
      options: {
        options: [
          { label: "Video", value: "Video" },
          { label: "Audio", value: "Audio" },
          { label: "Image", value: "Image" },
          { label: "PDF Document", value: "PdfDocument" },
          { label: "Microsoft Office Document", value: "MicrosoftOfficeDocument" },
          { label: "Flash (Swf)", value: "Swf" },
          { label: "Unknown", value: "UnknownType" }
        ]
      }
    }),
    limit: import_pieces_framework5.Property.Number({
      displayName: "Limit",
      description: "Maximum number of media to return (up to 100).",
      required: !1,
      defaultValue: 25
    })
  },
  async run(context) {
    let { projectId, name, type, limit } = context.propsValue;
    return (await wistiaApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common5.HttpMethod.GET,
      resourceUrl: "/medias.json",
      query: {
        project_id: projectId,
        name,
        type,
        per_page: Math.min(limit != null ? limit : 25, 100),
        sort_by: "created",
        sort_direction: 0
      }
    })).body.map(flattenMedia);
  }
});

// ../ap/packages/pieces/community/wistia/src/lib/actions/get-media.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var getMediaAction = (0, import_pieces_framework6.createAction)({
  auth: wistiaAuth,
  name: "get_media",
  displayName: "Get Media",
  description: "Retrieve the details of a single video or media file.",
  audience: "both",
  aiMetadata: {
    description: "Retrieves the full details of a single Wistia media item by its hashed media id. Use when an agent already has a specific media id and needs its current attributes. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    mediaId: wistiaCommon.mediaDropdown(!0)
  },
  async run(context) {
    let response = await wistiaApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common6.HttpMethod.GET,
      resourceUrl: `/medias/${context.propsValue.mediaId}.json`
    });
    return flattenMedia(response.body);
  }
});

// ../ap/packages/pieces/community/wistia/src/lib/actions/update-media.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var updateMediaAction = (0, import_pieces_framework7.createAction)({
  auth: wistiaAuth,
  name: "update_media",
  displayName: "Update Media",
  description: "Update the name or description of a video or media file.",
  audience: "both",
  aiMetadata: {
    description: "Updates the name and/or description of an existing Wistia media item identified by its hashed media id; omitted fields are left unchanged. Use to rename or re-describe a known media. Idempotent: repeating with the same values leaves the media in the same state.",
    idempotent: !0
  },
  props: {
    mediaId: wistiaCommon.mediaDropdown(!0),
    name: import_pieces_framework7.Property.ShortText({
      displayName: "Name",
      description: "A new name for the media. Leave empty to keep the current name.",
      required: !1
    }),
    description: import_pieces_framework7.Property.LongText({
      displayName: "Description",
      description: "A new description shown next to the media in Wistia. Plain text or Markdown is supported.",
      required: !1
    })
  },
  async run(context) {
    let { mediaId, name, description } = context.propsValue, body = {};
    name !== void 0 && (body.name = name), description !== void 0 && (body.description = description);
    let response = await wistiaApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common7.HttpMethod.PUT,
      resourceUrl: `/medias/${mediaId}.json`,
      body
    });
    return flattenMedia(response.body);
  }
});

// ../ap/packages/pieces/community/wistia/src/lib/actions/update-project.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var updateProjectAction = (0, import_pieces_framework8.createAction)({
  auth: wistiaAuth,
  name: "update_project",
  displayName: "Update Project",
  description: "Update the attributes of an existing project.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing Wistia project (referenced by its id) \u2014 its name and its anonymous-upload/download and public-link visibility flags; omitted fields are left unchanged. Use to rename or change the sharing settings of a known project. Idempotent: repeating with the same values leaves the project in the same state.",
    idempotent: !0
  },
  props: {
    projectId: wistiaCommon.projectDropdown(!0),
    name: import_pieces_framework8.Property.ShortText({
      displayName: "Project Name",
      description: "A new name for the project. Leave empty to keep the current name.",
      required: !1
    }),
    anonymousCanUpload: import_pieces_framework8.Property.Checkbox({
      displayName: "Allow Anonymous Uploads",
      description: "Allow anonymous (logged-out) users to upload media to this project.",
      required: !1
    }),
    anonymousCanDownload: import_pieces_framework8.Property.Checkbox({
      displayName: "Allow Anonymous Downloads",
      description: "Allow anonymous (logged-out) users to download media from this project.",
      required: !1
    }),
    public: import_pieces_framework8.Property.Checkbox({
      displayName: "Public",
      description: "Make the project accessible to anyone with the public link.",
      required: !1
    })
  },
  async run(context) {
    let { projectId, name, anonymousCanUpload, anonymousCanDownload, public: isPublic } = context.propsValue, body = {};
    name !== void 0 && (body.name = name), anonymousCanUpload !== void 0 && (body.anonymousCanUpload = anonymousCanUpload ? 1 : 0), anonymousCanDownload !== void 0 && (body.anonymousCanDownload = anonymousCanDownload ? 1 : 0), isPublic !== void 0 && (body.public = isPublic ? 1 : 0);
    let response = await wistiaApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common8.HttpMethod.PUT,
      resourceUrl: `/projects/${projectId}.json`,
      body
    });
    return flattenProject(response.body);
  }
});

// ../ap/packages/pieces/community/wistia/src/lib/triggers/new-media.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var props = {
  projectId: wistiaCommon.projectIdDropdown(!1)
}, polling = {
  strategy: import_pieces_common9.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue }) => (await wistiaApiCall({
    token: auth.secret_text,
    method: import_pieces_common9.HttpMethod.GET,
    resourceUrl: "/medias.json",
    query: {
      project_id: propsValue.projectId,
      per_page: 100,
      sort_by: "created",
      sort_direction: 0
    }
  })).body.map((media) => {
    var _a;
    return {
      epochMilliSeconds: new Date((_a = media.created) != null ? _a : 0).getTime(),
      data: flattenMedia(media)
    };
  })
}, newMediaTrigger = (0, import_pieces_framework9.createTrigger)({
  auth: wistiaAuth,
  name: "new_media",
  displayName: "New Media",
  description: "Triggers when a new video or media file is added to your account.",
  aiMetadata: {
    description: "Fires when a new media item (video, audio, image, or document) appears in the Wistia account, optionally scoped to a single project. Represents a newly added asset, detected by polling for recently created media."
  },
  props,
  type: import_pieces_framework9.TriggerStrategy.POLLING,
  async test(context) {
    return import_pieces_common9.pollingHelper.test(polling, context);
  },
  async onEnable(context) {
    await import_pieces_common9.pollingHelper.onEnable(polling, context);
  },
  async onDisable(context) {
    await import_pieces_common9.pollingHelper.onDisable(polling, context);
  },
  async run(context) {
    return import_pieces_common9.pollingHelper.poll(polling, context);
  },
  sampleData: {
    id: 6122606,
    hashed_id: "abc123def4",
    name: "My Awesome Video",
    type: "Video",
    status: "ready",
    progress: 1,
    section: null,
    description: "A short description of the video.",
    duration: 42.13,
    created: "2024-01-15T10:30:00.000Z",
    updated: "2024-01-15T10:35:00.000Z",
    thumbnail_url: "https://embed-ssl.wistia.com/deliveries/abc123.jpg",
    thumbnail_width: 1280,
    thumbnail_height: 720,
    project_id: 215140,
    project_name: "Marketing Videos",
    project_hashed_id: "xy12ab34cd"
  }
});

// ../ap/packages/pieces/community/wistia/src/lib/triggers/new-project.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var polling2 = {
  strategy: import_pieces_common10.DedupeStrategy.TIMEBASED,
  items: async ({ auth }) => (await wistiaApiCall({
    token: auth.secret_text,
    method: import_pieces_common10.HttpMethod.GET,
    resourceUrl: "/projects.json",
    query: {
      per_page: 100,
      sort_by: "created",
      sort_direction: 0
    }
  })).body.map((project) => {
    var _a;
    return {
      epochMilliSeconds: new Date((_a = project.created) != null ? _a : 0).getTime(),
      data: flattenProject(project)
    };
  })
}, newProjectTrigger = (0, import_pieces_framework10.createTrigger)({
  auth: wistiaAuth,
  name: "new_project",
  displayName: "New Project",
  description: "Triggers when a new project is created in your account.",
  aiMetadata: {
    description: "Fires when a new project is created in the Wistia account. Represents a newly added media container, detected by polling for recently created projects."
  },
  props: {},
  type: import_pieces_framework10.TriggerStrategy.POLLING,
  async test(context) {
    return import_pieces_common10.pollingHelper.test(polling2, context);
  },
  async onEnable(context) {
    await import_pieces_common10.pollingHelper.onEnable(polling2, context);
  },
  async onDisable(context) {
    await import_pieces_common10.pollingHelper.onDisable(polling2, context);
  },
  async run(context) {
    return import_pieces_common10.pollingHelper.poll(polling2, context);
  },
  sampleData: {
    id: 215140,
    hashed_id: "xy12ab34cd",
    name: "Marketing Videos",
    description: "All our marketing content.",
    media_count: 12,
    public: !1,
    public_id: "xy12ab34cd",
    anonymous_can_upload: !1,
    anonymous_can_download: !1,
    created: "2024-01-10T09:00:00.000Z",
    updated: "2024-01-15T10:35:00.000Z"
  }
});

// ../ap/packages/pieces/community/wistia/src/index.ts
var wistiaAuth = import_pieces_framework11.PieceAuth.SecretText({
  displayName: "API Access Token",
  description: `To get your Wistia API access token:
1. Log in to your Wistia account.
2. Go to **Account Settings > API Access** (or visit https://my.wistia.com/account/api).
3. Click **Create Token** and give it a name.
4. Grant it the permissions you need (read/update/delete) and copy the token \u2014 it is only shown once.
5. Paste the token below.`,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common11.httpClient.sendRequest({
        method: import_pieces_common11.HttpMethod.GET,
        url: "https://api.wistia.com/v1/projects.json",
        authentication: {
          type: import_pieces_common11.AuthenticationType.BEARER_TOKEN,
          token: auth
        },
        queryParams: { per_page: "1" }
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API access token. Please check your token and try again."
      };
    }
  }
}), wistia = (0, import_pieces_framework11.createPiece)({
  displayName: "Wistia",
  description: "Video hosting and analytics for business. Manage your projects and media library.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/wistia.png",
  categories: [import_pieces_framework12.PieceCategory.MARKETING, import_pieces_framework12.PieceCategory.CONTENT_AND_FILES],
  auth: wistiaAuth,
  authors: ["sanket-a11y"],
  actions: [
    createProjectAction,
    updateProjectAction,
    getMediaAction,
    findMediaAction,
    updateMediaAction,
    copyMediaAction,
    deleteMediaAction,
    (0, import_pieces_common11.createCustomApiCallAction)({
      baseUrl: () => "https://api.wistia.com/v1",
      auth: wistiaAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [newMediaTrigger, newProjectTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  wistia,
  wistiaAuth
});
