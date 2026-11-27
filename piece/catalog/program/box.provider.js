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

// ../ap/packages/pieces/community/box/src/index.ts
var index_exports = {};
__export(index_exports, {
  box: () => box,
  boxAuth: () => boxAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/box/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), common = {
  baseUrl: "https://api.box.com/2.0",
  async subscribeWebhook(auth, data) {
    let request = {
      method: import_pieces_common.HttpMethod.POST,
      url: `${common.baseUrl}/webhooks`,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      },
      body: {
        address: data.webhookUrl,
        triggers: [data.event],
        target: data.target
      }
    };
    return (await import_pieces_common.httpClient.sendRequest(request)).body;
  },
  async unsubscribeWebhook(auth, webhookId) {
    let request = {
      method: import_pieces_common.HttpMethod.DELETE,
      url: `${common.baseUrl}/webhooks/${webhookId}`,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      }
    };
    return await import_pieces_common.httpClient.sendRequest(request);
  }
};

// ../ap/packages/pieces/community/box/src/lib/triggers/new-comment.ts
var import_pieces_framework = require("@activepieces/pieces-framework");
var newComment = (0, import_pieces_framework.createTrigger)({
  auth: boxAuth,
  name: "new_comment",
  classification: "READ",
  displayName: "New Comment",
  description: "Triggers when a comment is created",
  aiMetadata: {
    description: "Fires when a user posts a new comment on the specified Box file or folder. Each event represents a single created comment, including the comment text, the item it was added to, and the author."
  },
  type: import_pieces_framework.TriggerStrategy.WEBHOOK,
  props: {
    id: import_pieces_framework.Property.ShortText({
      displayName: "File/Folder ID",
      description: "The ID of the item to trigger a webhook",
      required: !0
    }),
    type: import_pieces_framework.Property.StaticDropdown({
      displayName: "Item Type",
      description: "The type of the item to trigger a webhook",
      required: !0,
      options: {
        options: [
          { label: "File", value: "file" },
          { label: "Folder", value: "folder" }
        ]
      }
    })
  },
  async onEnable(context) {
    let target = {
      id: context.propsValue.id,
      type: context.propsValue.type
    }, webhook = await common.subscribeWebhook(context.auth, {
      event: "COMMENT.CREATED",
      target,
      webhookUrl: context.webhookUrl
    });
    await context.store.put("_new_comment_trigger", webhook);
  },
  async onDisable(context) {
    let webhook = await context.store.get(
      "_new_comment_trigger"
    );
    webhook && await common.unsubscribeWebhook(context.auth, webhook.id);
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    type: "webhook_event",
    id: "9a30442d-f681-4d25-8815-aa46f0515387",
    created_at: "2023-04-19T13:25:07-07:00",
    trigger: "COMMENT.CREATED",
    webhook: { id: "1396363668", type: "webhook" },
    created_by: {
      type: "user",
      id: "24316851337",
      name: "Bonobo",
      login: "email@gmail.com"
    },
    source: {
      id: "538146815",
      type: "comment",
      is_reply_comment: !1,
      message: "Simple times...",
      created_by: {
        type: "user",
        id: "24316851337",
        name: "Bonobo",
        login: "email@gmail.com"
      },
      created_at: "2023-04-19T13:25:07-07:00",
      item: { id: "1194590019402", type: "file" },
      modified_at: "2023-04-19T13:25:07-07:00"
    },
    additional_info: []
  }
});

// ../ap/packages/pieces/community/box/src/lib/triggers/new-file.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var newFile = (0, import_pieces_framework2.createTrigger)({
  auth: boxAuth,
  name: "new_file",
  classification: "READ",
  displayName: "New File",
  description: "Triggers when a file is uploaded",
  aiMetadata: {
    description: "Fires when a new file is uploaded into the specified Box folder. Each event represents one uploaded file, including its name, ID, size, version, and parent folder."
  },
  type: import_pieces_framework2.TriggerStrategy.WEBHOOK,
  props: {
    folder: import_pieces_framework2.Property.ShortText({
      displayName: "Folder ID",
      description: "The ID of the folder in which file uploads will trigger this webhook",
      required: !0
    })
  },
  async onEnable(context) {
    let target = {
      id: context.propsValue.folder,
      type: "folder"
    }, webhook = await common.subscribeWebhook(context.auth, {
      event: "FILE.UPLOADED",
      target,
      webhookUrl: context.webhookUrl
    });
    await context.store.put("_new_file_trigger", webhook);
  },
  async onDisable(context) {
    let webhook = await context.store.get(
      "_new_file_trigger"
    );
    webhook && await common.unsubscribeWebhook(context.auth, webhook.id);
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    type: "webhook_event",
    id: "fb0bd323-33b9-4f71-9dbc-fbcc3fe109ad",
    created_at: "2023-04-19T12:06:52-07:00",
    trigger: "FILE.UPLOADED",
    webhook: { id: "1396340122", type: "webhook" },
    created_by: {
      type: "user",
      id: "24316851337",
      name: "Bonobo",
      login: "email@gmail.com"
    },
    source: {
      id: "1194585432265",
      type: "file",
      file_version: {
        type: "file_version",
        id: "1302595111465",
        sha1: "63da452d845b91ccb638510d046b902e96275359"
      },
      sequence_id: "0",
      etag: "0",
      sha1: "63da452d845b91ccb638510d046b902e96275359",
      name: "ap-logo.svg",
      description: "",
      size: 877,
      path_collection: { total_count: 2, entries: [Array] },
      created_at: "2023-04-19T12:06:52-07:00",
      modified_at: "2023-04-19T12:06:52-07:00",
      trashed_at: null,
      purged_at: null,
      content_created_at: "2023-02-02T06:54:17-08:00",
      content_modified_at: "2023-02-02T06:54:17-08:00",
      created_by: {
        type: "user",
        id: "24316851337",
        name: "Bonobo",
        login: "email@gmail.com"
      },
      modified_by: {
        type: "user",
        id: "24316851332",
        name: "Bonobo",
        login: "email@gmail.com"
      },
      owned_by: {
        type: "user",
        id: "24316851332",
        name: "Bonobo",
        login: "email@gmail.com"
      },
      shared_link: null,
      parent: {
        type: "folder",
        id: "198605434359",
        sequence_id: "0",
        etag: "0",
        name: "Kinembe"
      },
      item_status: "active"
    },
    additional_info: []
  }
});

// ../ap/packages/pieces/community/box/src/lib/triggers/new-folder.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var newFolder = (0, import_pieces_framework3.createTrigger)({
  auth: boxAuth,
  name: "new_folder",
  classification: "READ",
  displayName: "New Folder",
  description: "Triggers when a folder is created",
  aiMetadata: {
    description: "Fires when a new folder is created inside the specified Box parent folder. Each event represents one newly created folder, including its name, ID, parent folder, and creation timestamp."
  },
  type: import_pieces_framework3.TriggerStrategy.WEBHOOK,
  props: {
    folder: import_pieces_framework3.Property.ShortText({
      displayName: "Folder ID",
      description: "The ID of the folder in which file uploads will trigger this webhook",
      required: !0
    })
  },
  async onEnable(context) {
    let target = {
      id: context.propsValue.folder,
      type: "folder"
    }, webhook = await common.subscribeWebhook(context.auth, {
      event: "FOLDER.CREATED",
      target,
      webhookUrl: context.webhookUrl
    });
    await context.store.put("_new_folder_trigger", webhook);
  },
  async onDisable(context) {
    let webhook = await context.store.get(
      "_new_folder_trigger"
    );
    webhook && await common.unsubscribeWebhook(context.auth, webhook.id);
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    id: "3f08aca1-aa0b-49a5-8e5a-5d8980bfbdef",
    type: "webhook_event",
    source: {
      id: "218634717358",
      etag: "0",
      name: "test folder",
      size: 0,
      type: "folder",
      parent: {
        id: "218635125044",
        etag: "0",
        name: "Desktop",
        type: "folder",
        sequence_id: "0"
      },
      purged_at: null,
      created_at: "2023-07-25T05:55:08-07:00",
      trashed_at: null,
      description: "",
      item_status: "active",
      modified_at: "2023-07-25T05:55:08-07:00",
      sequence_id: "0",
      shared_link: null,
      path_collection: {
        entries: [
          {
            id: "0",
            etag: null,
            name: "All Files",
            type: "folder",
            sequence_id: null
          },
          {
            id: "218635125044",
            etag: "0",
            name: "Desktop",
            type: "folder",
            sequence_id: "0"
          }
        ],
        total_count: 2
      },
      content_created_at: "2023-07-25T05:55:08-07:00",
      content_modified_at: "2023-07-25T05:55:08-07:00",
      folder_upload_email: null
    },
    trigger: "FOLDER.CREATED",
    webhook: {
      id: "1738566186",
      type: "webhook"
    },
    created_at: "2023-07-25T05:55:09-07:00",
    additional_info: []
  }
});

// ../ap/packages/pieces/community/box/src/index.ts
var boxAuth = import_pieces_framework4.PieceAuth.OAuth2({
  required: !0,
  authUrl: "https://account.box.com/api/oauth2/authorize",
  tokenUrl: "https://api.box.com/oauth2/token",
  scope: ["manage_webhook", "root_readonly", "root_readwrite"]
}), box = (0, import_pieces_framework4.createPiece)({
  displayName: "Box",
  description: "Secure content management and collaboration",
  auth: boxAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/box.png",
  categories: [import_pieces_framework5.PieceCategory.CONTENT_AND_FILES],
  authors: ["kishanprmr", "MoShizzle", "abuaboud"],
  actions: [
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: () => common.baseUrl,
      auth: boxAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.access_token}`
      })
    })
  ],
  triggers: [newFile, newFolder, newComment]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  box,
  boxAuth
});
