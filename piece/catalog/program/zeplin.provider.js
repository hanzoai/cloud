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

// ../ap/packages/pieces/community/zeplin/src/index.ts
var index_exports = {};
__export(index_exports, {
  zeplin: () => zeplin
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework10 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/zeplin/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/zeplin/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.zeplin.dev/v1";
async function makeRequest(access_token, method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        Authorization: `Bearer ${access_token}`,
        "Content-Type": "application/json",
        accept: "application/json"
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/zeplin/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), ziplinAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Zeplin Personal Access Token",
  description: "Zeplin Personal Access Token",
  required: !0,
  validate: async ({ auth }) => {
    if (!auth)
      return {
        valid: !0,
        error: "Personal Access Token not provided"
      };
    try {
      return await makeRequest(auth, import_pieces_common2.HttpMethod.GET, "/projects"), {
        valid: !0
      };
    } catch (error) {
      return {
        valid: !1,
        error: "Invalid Api Key"
      };
    }
  }
});

// ../ap/packages/pieces/community/zeplin/src/lib/actions/create-note.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/zeplin/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), project_idProp = import_pieces_framework2.Property.Dropdown({
  auth: ziplinAuth,
  displayName: "Project",
  description: "Select the Zeplin project",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Zeplin account first",
        options: []
      };
    try {
      let access_token = auth == null ? void 0 : auth.secret_text;
      return {
        disabled: !1,
        options: (await makeRequest(
          access_token,
          import_pieces_common3.HttpMethod.GET,
          "/projects"
        )).map((project) => ({
          label: project.name,
          value: project.id
        }))
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Could not fetch projects"
      };
    }
  }
}), screen_idProp = import_pieces_framework2.Property.Dropdown({
  auth: ziplinAuth,
  displayName: "Screen",
  description: "Select the Zeplin screen",
  required: !0,
  refreshers: ["projectId"],
  options: async ({ auth, projectId }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Zeplin account first",
        options: []
      };
    if (!projectId)
      return {
        disabled: !0,
        placeholder: "Select a project first",
        options: []
      };
    try {
      let access_token = auth == null ? void 0 : auth.secret_text;
      return {
        disabled: !1,
        options: (await makeRequest(
          access_token,
          import_pieces_common3.HttpMethod.GET,
          `/projects/${projectId}/screens`
        )).map((screen) => ({
          label: screen.name,
          value: screen.id
        }))
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Could not fetch screens"
      };
    }
  }
});

// ../ap/packages/pieces/community/zeplin/src/lib/actions/create-note.ts
var createNote = (0, import_pieces_framework3.createAction)({
  auth: ziplinAuth,
  name: "createNote",
  displayName: "Create Note",
  description: "Create a note on a screen in Zeplin",
  audience: "both",
  aiMetadata: { description: "Add a comment/annotation note pinned to a specific position on a Zeplin screen, identified by project ID and screen ID. Use to leave design feedback or markup at given X/Y coordinates with a chosen highlight color. Not idempotent: each call creates a new note even with identical content.", idempotent: !1 },
  props: {
    projectId: project_idProp,
    screenId: screen_idProp,
    content: import_pieces_framework3.Property.LongText({
      displayName: "Note content",
      description: "The content of the note",
      required: !0
    }),
    color: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Color",
      description: "The color of the note",
      required: !0,
      options: {
        options: [
          { label: "Orange", value: "orange" },
          { label: "Peach", value: "peach" },
          { label: "Green", value: "green" },
          { label: "Yellow", value: "yellow" },
          { label: "Turquoise", value: "turquoise" },
          { label: "Cornflower Blue", value: "cornflower_blue" },
          { label: "Deep Purple", value: "deep_purple" }
        ]
      }
    }),
    x: import_pieces_framework3.Property.Number({
      displayName: "X Coordinate",
      description: "The x coordinate position of the note on the screen",
      required: !0
    }),
    y: import_pieces_framework3.Property.Number({
      displayName: "Y Coordinate",
      description: "The y coordinate position of the note on the screen",
      required: !0
    }),
    x_start: import_pieces_framework3.Property.Number({
      displayName: "X Start Coordinate",
      description: "The starting x coordinate for range selection (optional)",
      required: !1
    }),
    y_start: import_pieces_framework3.Property.Number({
      displayName: "Y Start Coordinate",
      description: "The starting y coordinate for range selection (optional)",
      required: !1
    })
  },
  async run(context) {
    let { projectId, screenId, content, x, y, color, x_start, y_start } = context.propsValue, body = {
      content,
      position: {
        x,
        y,
        x_start,
        y_start
      },
      color
    };
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common4.HttpMethod.POST,
      `/projects/${projectId}/screens/${screenId}/notes`,
      body
    );
  }
});

// ../ap/packages/pieces/community/zeplin/src/lib/actions/find-project.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var findProject = (0, import_pieces_framework4.createAction)({
  auth: ziplinAuth,
  name: "findProject",
  displayName: "Find Project",
  description: "Find a project by name or retrieve all projects",
  audience: "both",
  aiMetadata: { description: "Look up Zeplin projects accessible to the authenticated user. With a project name it filters to partial (case-insensitive) name matches; with the name left empty it returns all projects. Use to resolve a project ID before screen or note operations. Read-only and idempotent. Note: if the name filter matches nothing, it falls back to returning all projects.", idempotent: !0 },
  props: {
    projectName: import_pieces_framework4.Property.ShortText({
      displayName: "Project Name",
      description: "Optional project name to search for (partial match)",
      required: !1
    })
  },
  async run(context) {
    let { projectName } = context.propsValue, projects = await makeRequest(
      context.auth.secret_text,
      import_pieces_common5.HttpMethod.GET,
      "/projects"
    ) || [];
    if (projectName) {
      let filtered = projects.filter(
        (project) => project.name.toLowerCase().includes(projectName.toLowerCase())
      );
      return filtered.length > 0 ? filtered : projects;
    }
    return projects;
  }
});

// ../ap/packages/pieces/community/zeplin/src/lib/actions/find-screen.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var findScreen = (0, import_pieces_framework5.createAction)({
  auth: ziplinAuth,
  name: "findScreen",
  displayName: "Find Screen",
  description: "Find a screen by name or retrieve all screens in a project",
  audience: "both",
  aiMetadata: { description: "List screens within a given Zeplin project (by project ID). With a screen name it filters to partial (case-insensitive) title matches; with the name left empty it returns all screens in the project. Use to resolve a screen ID before creating notes or updating a screen. Read-only and idempotent. Note: if the name filter matches nothing, it falls back to returning all screens.", idempotent: !0 },
  props: {
    projectId: project_idProp,
    screenName: import_pieces_framework5.Property.ShortText({
      displayName: "Screen Name",
      description: "Optional screen name to search for (partial match)",
      required: !1
    })
  },
  async run(context) {
    let { projectId, screenName } = context.propsValue, screens = await makeRequest(
      context.auth.secret_text,
      import_pieces_common6.HttpMethod.GET,
      `/projects/${projectId}/screens`
    ) || [];
    if (screenName) {
      let filtered = screens.filter(
        (screen) => screen.title.toLowerCase().includes(screenName.toLowerCase())
      );
      return filtered.length > 0 ? filtered : screens;
    }
    return screens;
  }
});

// ../ap/packages/pieces/community/zeplin/src/lib/actions/update-project.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var updateProject = (0, import_pieces_framework6.createAction)({
  auth: ziplinAuth,
  name: "updateProject",
  displayName: "Update Project",
  description: "Update a project's name and description",
  audience: "both",
  aiMetadata: { description: "Update a Zeplin project's name and/or description, identified by project ID. Only the fields you supply are changed; omitted fields are left untouched. Use to rename a project or revise its description. Idempotent: repeating the call with the same values leaves the project in the same state.", idempotent: !0 },
  props: {
    projectId: project_idProp,
    name: import_pieces_framework6.Property.ShortText({
      displayName: "Project Name",
      description: "The new name for the project",
      required: !1
    }),
    description: import_pieces_framework6.Property.LongText({
      displayName: "Description",
      description: "The new description for the project",
      required: !1
    })
  },
  async run(context) {
    let { projectId, name, description } = context.propsValue, body = {};
    return name && (body.name = name), description && (body.description = description), {
      success: !0,
      message: "Project updated",
      data: await makeRequest(
        context.auth.secret_text,
        import_pieces_common7.HttpMethod.PATCH,
        `/projects/${projectId}`,
        body
      )
    };
  }
});

// ../ap/packages/pieces/community/zeplin/src/lib/actions/update-screen.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var updateScreen = (0, import_pieces_framework7.createAction)({
  auth: ziplinAuth,
  name: "updateScreen",
  displayName: "Update Screen",
  description: "Update a screen's description",
  audience: "both",
  aiMetadata: { description: "Set the description of a specific Zeplin screen, identified by project ID and screen ID. Use to revise the textual description shown for a screen. Idempotent: repeating the call with the same description leaves the screen in the same state.", idempotent: !0 },
  props: {
    projectId: project_idProp,
    screenId: screen_idProp,
    description: import_pieces_framework7.Property.LongText({
      displayName: "Description",
      description: "The new description for the screen",
      required: !0
    })
  },
  async run(context) {
    let { projectId, screenId, description } = context.propsValue, body = {
      description
    };
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common8.HttpMethod.PATCH,
      `/projects/${projectId}/screens/${screenId}`,
      body
    );
  }
});

// ../ap/packages/pieces/community/zeplin/src/lib/triggers/new-note.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var newNote = (0, import_pieces_framework8.createTrigger)({
  auth: ziplinAuth,
  name: "newNote",
  displayName: "New Note",
  description: "Triggered when a new note is created on a screen",
  aiMetadata: {
    description: "Fires when a new note (comment/annotation) is added to a screen in a Zeplin project, delivering the note plus its project and screen context. Requires the corresponding webhook to be configured in the Zeplin dashboard."
  },
  props: {},
  sampleData: {
    event: "project.note",
    action: "created",
    timestamp: 1572347818,
    resource: {
      id: "5dbad85a76ea51c1f35b6f69",
      type: "ScreenNote",
      data: {
        id: "5dbad85a76ea51c1f35b6f69",
        created: 1572347818,
        order: 1,
        status: "open",
        creator: {
          id: "5d9caaecb4a3fa9bc9718686",
          email: "5d9caaecb4a3fa9bc9718686@user.zeplin.io",
          username: "zozo",
          emotar: "\u{1F34E}",
          avatar: "http://placekitten.com/200/300",
          last_seen: 1616739240
        }
      }
    },
    context: {
      project: {
        id: "5db81e73e1e36ee19f138c1a",
        name: "HAL 9000",
        description: "UI designs for the onboard computer on the spaceship Discovery 1",
        platform: "web",
        thumbnail: "http://placekitten.com/200/300",
        status: "active",
        scene_url: "https://scene.zeplin.io/project/5db81e73e1e36ee19f138c1a",
        created: 1517184e3,
        updated: 1572347818,
        number_of_members: 47,
        number_of_screens: 112,
        number_of_components: 46,
        number_of_connected_components: 32,
        number_of_text_styles: 28,
        number_of_colors: 17,
        linked_styleguide: {
          id: "5db81e6e6a4462065f04d932"
        }
      },
      screen: {
        id: "5dbad85a76ea51c1f35b6f69",
        name: "Login",
        description: "Login screen for HAL 9000",
        tags: ["mobile", "login"],
        image: {
          width: 2560,
          height: 1920,
          original_url: "http://placekitten.com/2560/1920",
          thumbnails: {
            small: "http://placekitten.com/256/256",
            medium: "http://placekitten.com/512/512",
            large: "http://placekitten.com/1024/1024"
          }
        },
        created: 1517184e3,
        updated: 1572347818,
        number_of_notes: 7,
        number_of_versions: 4,
        number_of_annotations: 2,
        section: {
          id: "5db81e6e6a4462065f04d932"
        },
        variant: {
          value: "Default",
          group: {
            id: "607437cd62d37a0bc869fc63",
            name: "Login"
          }
        }
      }
    },
    actor: {
      user: {
        id: "5d9caaecb4a3fa9bc9718686",
        email: "5d9caaecb4a3fa9bc9718686@user.zeplin.io",
        username: "zozo",
        emotar: "\u{1F34E}",
        avatar: "http://placekitten.com/200/300",
        last_seen: 1616739240
      }
    }
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/zeplin/src/lib/triggers/new-screen.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var newScreen = (0, import_pieces_framework9.createTrigger)({
  auth: ziplinAuth,
  name: "newScreen",
  displayName: "New Screen",
  description: "Triggered when a new screen is created in a project",
  aiMetadata: {
    description: "Fires when a new screen is added to a Zeplin project, delivering the screen details plus its project context. Requires the corresponding webhook to be configured in the Zeplin dashboard."
  },
  props: {},
  sampleData: {
    event: "project.screen",
    action: "created",
    timestamp: 1572347818,
    resource: {
      id: "5dbad85a76ea51c1f35b6f69",
      type: "Screen",
      data: {
        id: "5dbad85a76ea51c1f35b6f69",
        name: "Login",
        description: "Login screen for HAL 9000",
        tags: ["mobile", "login"],
        image: {
          width: 2560,
          height: 1920,
          original_url: "http://placekitten.com/2560/1920",
          thumbnails: {
            small: "http://placekitten.com/256/256",
            medium: "http://placekitten.com/512/512",
            large: "http://placekitten.com/1024/1024"
          }
        },
        created: 1517184e3,
        updated: 1572347818,
        number_of_notes: 0,
        number_of_versions: 1,
        number_of_annotations: 0,
        section: {
          id: "5db81e6e6a4462065f04d932"
        },
        variant: {
          value: "Default",
          group: {
            id: "607437cd62d37a0bc869fc63",
            name: "Login"
          }
        }
      }
    },
    context: {
      project: {
        id: "5db81e73e1e36ee19f138c1a",
        name: "HAL 9000",
        description: "UI designs for the onboard computer on the spaceship Discovery 1",
        platform: "web",
        thumbnail: "http://placekitten.com/200/300",
        status: "active",
        scene_url: "https://scene.zeplin.io/project/5db81e73e1e36ee19f138c1a",
        created: 1517184e3,
        updated: 1572347818,
        number_of_members: 47,
        number_of_screens: 112,
        number_of_components: 46,
        number_of_connected_components: 32,
        number_of_text_styles: 28,
        number_of_colors: 17,
        linked_styleguide: {
          id: "5db81e6e6a4462065f04d932"
        }
      }
    },
    actor: {
      user: {
        id: "5d9caaecb4a3fa9bc9718686",
        email: "5d9caaecb4a3fa9bc9718686@user.zeplin.io",
        username: "zozo",
        emotar: "\u{1F34E}",
        avatar: "http://placekitten.com/200/300",
        last_seen: 1616739240
      }
    }
  },
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/zeplin/src/index.ts
var import_pieces_common9 = require("@activepieces/pieces-common");
var zeplin = (0, import_pieces_framework10.createPiece)({
  displayName: "Zeplin",
  auth: ziplinAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/zeplin.png",
  authors: ["sanket-a11y"],
  actions: [
    createNote,
    findProject,
    findScreen,
    updateProject,
    updateScreen,
    (0, import_pieces_common9.createCustomApiCallAction)({
      baseUrl: () => BASE_URL,
      auth: ziplinAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [newNote, newScreen]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  zeplin
});
