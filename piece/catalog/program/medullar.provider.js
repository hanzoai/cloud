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

// ../ap/packages/pieces/community/medullar/src/index.ts
var index_exports = {};
__export(index_exports, {
  medullar: () => medullar
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/medullar/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), medullarAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "Please use your personal **API KEY**. You can generate one in your [Medullar account profile](https://my.medullar.com/my-account), under the **API Keys** section."
});

// ../ap/packages/pieces/community/medullar/src/lib/actions/create-space.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/medullar/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var medullarCommon = {
  baseUrl: "https://api.medullar.com",
  authUrl: "https://api.medullar.com/auth/v1",
  exploratorUrl: "https://api.medullar.com/explorator/v1",
  aiUrl: "https://api.medullar.com/ai/v1"
};
async function getUser(authentication) {
  let userData = (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: `${medullarCommon.authUrl}/users/me/`,
    headers: {
      Authorization: `Bearer ${authentication}`
    }
  })).body;
  if (!userData)
    throw new Error("User data not found.");
  if (!userData.company)
    throw new Error("User does not belong to any company.");
  return userData;
}
async function getUserSpaces(authentication) {
  let userData = await getUser(authentication);
  return (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: `${medullarCommon.aiUrl}/spaces/?user=${userData.uuid}&limit=1000&offset=0`,
    headers: {
      Authorization: `Bearer ${authentication.secret_text}`
    }
  })).body.results;
}
var medullarPropsCommon = {
  spaceId: import_pieces_framework2.Property.Dropdown({
    auth: medullarAuth,
    displayName: "Space",
    description: "Select an Space",
    required: !0,
    refreshers: ["auth"],
    refreshOnSearch: !1,
    options: async ({ auth }) => {
      let authentication = auth;
      return authentication ? {
        disabled: !1,
        options: (await getUserSpaces(authentication)).flat().map((list) => ({
          label: list.name,
          value: list.uuid
        }))
      } : {
        disabled: !0,
        placeholder: "connect your account first",
        options: []
      };
    }
  }),
  chatId: import_pieces_framework2.Property.Dropdown({
    auth: medullarAuth,
    displayName: "Chat",
    description: "Optional. Select a Chat where messages will be stored, if not selected, a default chat will be created with the name `automated`",
    required: !1,
    refreshers: ["auth", "spaceId"],
    refreshOnSearch: !1,
    options: async ({ auth, spaceId }) => {
      let authentication = auth;
      return authentication ? spaceId ? {
        disabled: !1,
        options: (await import_pieces_common.httpClient.sendRequest({
          method: import_pieces_common.HttpMethod.GET,
          url: `${medullarCommon.aiUrl}/chats/?space=${spaceId}&limit=1000&offset=0`,
          headers: {
            Authorization: `Bearer ${authentication}`
          }
        })).body.results.flat().map((list) => ({
          label: list.name,
          value: list.uuid
        }))
      } : {
        disabled: !0,
        options: [],
        placeholder: "Please select a Space first"
      } : {
        disabled: !0,
        placeholder: "connect your account first",
        options: []
      };
    }
  })
};

// ../ap/packages/pieces/community/medullar/src/lib/actions/create-space.ts
var createSpace = (0, import_pieces_framework3.createAction)({
  auth: medullarAuth,
  name: "createSpace",
  displayName: "Create new Space",
  description: "Create a new Space.",
  audience: "both",
  aiMetadata: { description: "Creates a new Medullar Space (a knowledge container scoped to the authenticated user's company) with the given name. Use to set up a fresh Space before adding records or asking questions. Not idempotent: each call creates a separate Space even with the same name.", idempotent: !1 },
  props: {
    space_name: import_pieces_framework3.Property.ShortText({
      displayName: "Space Name",
      required: !0
    })
  },
  async run(context) {
    let userData = await getUser(context.auth);
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: `${medullarCommon.aiUrl}/spaces/`,
      body: {
        name: context.propsValue.space_name,
        company: {
          uuid: userData.company.uuid
        }
      },
      headers: {
        Authorization: `Bearer ${context.auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/medullar/src/lib/actions/list-spaces.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var listSpaces = (0, import_pieces_framework4.createAction)({
  auth: medullarAuth,
  name: "listSpaces",
  displayName: "List Spaces",
  description: "List all user Spaces",
  audience: "both",
  aiMetadata: { description: "Retrieves all Medullar Spaces belonging to the authenticated user. Use to discover available Spaces and their UUIDs before targeting one in another action (add record, ask, rename, delete). Read-only and idempotent; takes no input.", idempotent: !0 },
  props: {},
  async run(context) {
    return await getUserSpaces(context.auth);
  }
});

// ../ap/packages/pieces/community/medullar/src/lib/actions/add-space-record.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var addSpaceRecord = (0, import_pieces_framework5.createAction)({
  auth: medullarAuth,
  name: "addSpaceRecord",
  displayName: "Add Space Record",
  description: "Adds a Record into a Space",
  audience: "both",
  aiMetadata: { description: "Ingests a new content record into a Medullar Space identified by its UUID, so the content becomes part of that Space's knowledge base. The source type selects what kind of content is added (url, text, image, or file), with content and/or url supplied accordingly. Use to feed material into a Space before asking questions about it. Not idempotent: each call appends a new record.", idempotent: !1 },
  props: {
    spaceId: medullarPropsCommon.spaceId,
    sourceType: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Source Type",
      description: "Select source type",
      required: !0,
      options: {
        options: [
          {
            label: "URL",
            value: "url"
          },
          {
            label: "Text",
            value: "text"
          },
          {
            label: "Image",
            value: "image"
          },
          {
            label: "File",
            value: "file"
          }
        ]
      }
    }),
    content: import_pieces_framework5.Property.LongText({
      displayName: "Content",
      description: "Optional. Content of the record",
      required: !1
    }),
    url: import_pieces_framework5.Property.ShortText({
      displayName: "URL",
      description: "Optional. URL of the record",
      required: !1
    })
  },
  async run(context) {
    let userData = await getUser(context.auth), url = " ", content = " ";
    return context.propsValue.url != null && (url = context.propsValue.url), context.propsValue.content != null && (content = context.propsValue.content), (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: `${medullarCommon.aiUrl}/records/`,
      body: {
        spaces: [
          {
            uuid: context.propsValue.spaceId
          }
        ],
        company: {
          uuid: userData.company.uuid
        },
        user: {
          uuid: userData.uuid
        },
        source: context.propsValue.sourceType,
        data: {
          content,
          url
        }
      },
      headers: {
        Authorization: `Bearer ${context.auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/medullar/src/lib/actions/ask-space.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common");
var askSpace = (0, import_pieces_framework6.createAction)({
  auth: medullarAuth,
  name: "askSpace",
  displayName: "Ask Space",
  description: "Ask anything to a Space",
  audience: "both",
  aiMetadata: { description: "Sends a question or message to a Medullar Space identified by its UUID and returns the AI agent's response grounded in that Space. The chat mode selects which agent answers (general agent, Grok-backed agent, fact-check, researcher, sales researcher, or search), and an optional deep-analysis flag trades speed for accuracy. If no chat UUID is given a new chat is created automatically; supplying one continues that conversation. Not idempotent: each call posts a message and generates a fresh response.", idempotent: !1 },
  props: {
    spaceId: medullarPropsCommon.spaceId,
    chatId: medullarPropsCommon.chatId,
    selectedMode: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Chat Mode",
      description: "Select chat mode.",
      required: !0,
      options: {
        options: [
          {
            label: "MedullaryAI Agent",
            value: "single_agent"
          },
          {
            label: "MedullaryAI Agent (xAI Grok)",
            value: "single_agent_xai"
          },
          {
            label: "MedullaryAI Fact Check",
            value: "fact_check_agent"
          },
          {
            label: "MedullaryAI Researcher",
            value: "research_agent"
          },
          {
            label: "MedullaryAI Sales Researcher",
            value: "sales_research_agent"
          },
          {
            label: "MedullaryAI Search Agent",
            value: "`search_agent`"
          }
        ]
      }
    }),
    isReasoning: import_pieces_framework6.Property.Checkbox({
      displayName: "Deep Analysis",
      description: "Optional. Enable Deep Analysis to get more accurate results but slower response time",
      required: !1,
      defaultValue: !1
    }),
    text: import_pieces_framework6.Property.LongText({
      displayName: "Message",
      description: "Message to send to the Space",
      required: !0
    })
  },
  async run(context) {
    let userData = await getUser(context.auth), chatId = context.propsValue.chatId;
    return chatId == null && (chatId = (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: `${medullarCommon.aiUrl}/chats/`,
      body: {
        name: "activepieces automated",
        space: {
          uuid: context.propsValue.spaceId
        }
      },
      headers: {
        Authorization: `Bearer ${context.auth.secret_text}`
      }
    })).body.uuid), (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: `${medullarCommon.aiUrl}/messages/get_response/?chat=${chatId}`,
      body: {
        chat: {
          uuid: chatId
        },
        text: context.propsValue.text,
        is_bot: !1,
        is_reasoning_selected: context.propsValue.isReasoning,
        selected_mode: context.propsValue.selectedMode,
        source: "external_api"
      },
      headers: {
        Authorization: `Bearer ${context.auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/medullar/src/lib/actions/delete-space.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var deleteSpace = (0, import_pieces_framework7.createAction)({
  auth: medullarAuth,
  name: "deleteSpace",
  displayName: "Delete Space",
  description: "Delete an existing Space.",
  audience: "both",
  aiMetadata: { description: "Permanently deletes a Medullar Space identified by its UUID, along with its associated content. Use to remove a Space the user no longer needs. Destructive and not safely repeatable: a second call for the same UUID targets an already-removed Space.", idempotent: !1 },
  props: {
    spaceId: medullarPropsCommon.spaceId
  },
  async run(context) {
    return await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.DELETE,
      url: `${medullarCommon.aiUrl}/spaces/${context.propsValue.spaceId}/`,
      headers: {
        Authorization: `Bearer ${context.auth.secret_text}`
      }
    }), { deleted: !0, spaceId: context.propsValue.spaceId };
  }
});

// ../ap/packages/pieces/community/medullar/src/lib/actions/rename-space.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var renameSpace = (0, import_pieces_framework8.createAction)({
  auth: medullarAuth,
  name: "renameSpace",
  displayName: "Rename Space",
  description: "Rename an existing Space.",
  audience: "both",
  aiMetadata: { description: "Updates the name of an existing Medullar Space identified by its UUID. Use to relabel a Space without affecting its records or chats. Idempotent: repeating with the same UUID and name leaves the Space at that name.", idempotent: !0 },
  props: {
    spaceId: medullarPropsCommon.spaceId,
    space_name: import_pieces_framework8.Property.ShortText({
      displayName: "Space Name",
      required: !0
    })
  },
  async run(context) {
    return (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.PATCH,
      url: `${medullarCommon.aiUrl}/spaces/${context.propsValue.spaceId}/`,
      headers: {
        Authorization: `Bearer ${context.auth.secret_text}`
      },
      body: {
        name: context.propsValue.space_name
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/medullar/src/index.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var medullar = (0, import_pieces_framework9.createPiece)({
  displayName: "Medullar",
  description: "AI-powered discovery & insight platform that acts as your extended digital mind",
  auth: medullarAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/medullar.png",
  authors: ["mllopart"],
  actions: [createSpace, listSpaces, addSpaceRecord, askSpace, deleteSpace, renameSpace],
  triggers: [],
  categories: [
    import_pieces_framework10.PieceCategory.ARTIFICIAL_INTELLIGENCE,
    import_pieces_framework10.PieceCategory.PRODUCTIVITY
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  medullar
});
