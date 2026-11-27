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

// ../ap/packages/pieces/community/webex/src/index.ts
var index_exports = {};
__export(index_exports, {
  webex: () => webex
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework10 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/webex/src/lib/actions/create-message.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/webex/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://webexapis.com/v1";
async function makeRequest(access_token, method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        Authorization: `Bearer ${access_token}`,
        "Content-Type": "application/json"
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/webex/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), webexAuth = import_pieces_framework.PieceAuth.OAuth2({
  description: "Connect your Webex account",
  authUrl: "https://webexapis.com/v1/authorize",
  tokenUrl: "https://webexapis.com/v1/access_token",
  required: !0,
  scope: ["spark:all", "meeting:schedules_read"]
});

// ../ap/packages/pieces/community/webex/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common");
async function fetchRooms(access_token) {
  return (await makeRequest(access_token, import_pieces_common2.HttpMethod.GET, "/rooms")).items;
}
var roomIdDropdown = (rooms) => import_pieces_framework2.Property.StaticDropdown({
  displayName: "Room",
  description: "Select the room",
  required: !0,
  options: {
    disabled: !1,
    options: rooms.map((room) => ({ label: room.title, value: room.id }))
  }
});

// ../ap/packages/pieces/community/webex/src/lib/actions/create-message.ts
var createMessage = (0, import_pieces_framework3.createAction)({
  auth: webexAuth,
  name: "createMessage",
  displayName: "Create Message",
  description: "Post a plain text or rich text message to a Webex room or send a direct message",
  audience: "both",
  aiMetadata: {
    description: "Sends a message via Webex, in one of two modes selected by the message type: post to a room (by room ID) or send a direct message to a person (by person ID or email). Use it to deliver notifications, replies, or rich-text/markdown content with an optional file attachment. Each call posts a new message, so it is not idempotent; for direct messages either a person ID or email is required.",
    idempotent: !1
  },
  props: {
    messageType: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Message Type",
      description: "Choose between sending to a room or a direct message to a person",
      required: !0,
      options: {
        options: [
          { label: "Room Message", value: "room" },
          { label: "Direct Message", value: "direct" }
        ]
      }
    }),
    destination: import_pieces_framework3.Property.DynamicProperties({
      displayName: "Destination",
      required: !0,
      refreshers: ["messageType"],
      auth: webexAuth,
      async props({ messageType, auth }) {
        if (messageType === "room") {
          let rooms = await fetchRooms(auth.access_token);
          return {
            roomId: roomIdDropdown(rooms)
          };
        } else
          return {
            toPersonId: import_pieces_framework3.Property.ShortText({
              displayName: "Person ID",
              description: "The person ID of the recipient",
              required: !1
            }),
            toPersonEmail: import_pieces_framework3.Property.ShortText({
              displayName: "Person Email",
              description: "The email address of the recipient",
              required: !1
            })
          };
      }
    }),
    text: import_pieces_framework3.Property.LongText({
      displayName: "Message Text",
      description: "The message to send",
      required: !0
    }),
    markdown: import_pieces_framework3.Property.LongText({
      displayName: "Message Markdown",
      description: "The message in Markdown format for rich text. If provided, text will be used as alternate text",
      required: !1
    }),
    parentId: import_pieces_framework3.Property.ShortText({
      displayName: "Parent Message ID",
      description: "The parent message ID to reply to (optional)",
      required: !1
    }),
    files: import_pieces_framework3.Property.Array({
      displayName: "File URLs",
      description: "Public URLs to files to attach (only one file per message)",
      properties: {
        url: import_pieces_framework3.Property.ShortText({
          displayName: "File URL",
          description: "Public URL to the file",
          required: !0
        })
      },
      required: !1
    })
  },
  async run(context) {
    let { messageType, destination, text, markdown, parentId } = context.propsValue, files = context.propsValue.files, body = {
      text
    };
    if (messageType === "room") {
      let roomId = destination.roomId;
      body.roomId = roomId;
    } else {
      let toPersonId = destination.toPersonId, toPersonEmail = destination.toPersonEmail;
      if (!toPersonId && !toPersonEmail)
        throw new Error(
          "Either Person ID or Person Email is required for direct messages"
        );
      toPersonId && (body.toPersonId = toPersonId), toPersonEmail && (body.toPersonEmail = toPersonEmail);
    }
    return markdown && (body.markdown = markdown), parentId && (body.parentId = parentId), files && files.length > 0 && (body.files = [files[0].url]), await makeRequest(
      context.auth.access_token,
      import_pieces_common3.HttpMethod.POST,
      "/messages",
      body
    );
  }
});

// ../ap/packages/pieces/community/webex/src/lib/actions/create-room.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var createRoom = (0, import_pieces_framework4.createAction)({
  auth: webexAuth,
  name: "createRoom",
  displayName: "Create Room",
  description: "Create a new Webex room (space). The authenticated user is automatically added as a member.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new Webex room (space) with the given title, optionally associating it with a team and configuring public/discoverable, locked/moderated, or announcement-only modes. Use it to provision a fresh space for messaging. Each call creates a distinct room, so it is not idempotent; a description is required when making the room public, and announcement-only mode requires the room to be locked.",
    idempotent: !1
  },
  props: {
    title: import_pieces_framework4.Property.ShortText({
      displayName: "Room Title",
      description: "A user-friendly name for the room",
      required: !0
    }),
    description: import_pieces_framework4.Property.LongText({
      displayName: "Description",
      description: "The description of the room. Required if isPublic is true.",
      required: !1
    }),
    teamId: import_pieces_framework4.Property.ShortText({
      displayName: "Team ID",
      description: "The ID for the team with which this room is associated (optional)",
      required: !1
    }),
    classificationId: import_pieces_framework4.Property.ShortText({
      displayName: "Classification ID",
      description: "The classificationId for the room (optional)",
      required: !1
    }),
    isPublic: import_pieces_framework4.Property.Checkbox({
      displayName: "Public Room",
      description: "Make the room public and discoverable within the org. Note: description is required if this is true.",
      required: !1
    }),
    isLocked: import_pieces_framework4.Property.Checkbox({
      displayName: "Locked/Moderated",
      description: "Set the space as locked/moderated. The creator becomes a moderator.",
      required: !1
    }),
    isAnnouncementOnly: import_pieces_framework4.Property.Checkbox({
      displayName: "Announcement Only",
      description: "Sets the space into announcement mode. Only moderators can post messages. Note: space must be locked (moderated) to enable this.",
      required: !1
    })
  },
  async run(context) {
    let {
      title,
      description,
      teamId,
      classificationId,
      isPublic,
      isLocked,
      isAnnouncementOnly
    } = context.propsValue;
    if (isPublic && !description)
      throw new Error("Description is required for public rooms");
    if (isAnnouncementOnly && !isLocked)
      throw new Error(
        "Room must be locked/moderated to enable announcement-only mode"
      );
    let body = {
      title
    };
    return description && (body.description = description), teamId && (body.teamId = teamId), classificationId && (body.classificationId = classificationId), isPublic !== void 0 && isPublic && (body.isPublic = isPublic), isLocked !== void 0 && isLocked && (body.isLocked = isLocked), isAnnouncementOnly !== void 0 && isAnnouncementOnly && (body.isAnnouncementOnly = isAnnouncementOnly), await makeRequest(
      context.auth.access_token,
      import_pieces_common4.HttpMethod.POST,
      "/rooms",
      body
    );
  }
});

// ../ap/packages/pieces/community/webex/src/lib/actions/create-team.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var createTeam = (0, import_pieces_framework5.createAction)({
  auth: webexAuth,
  name: "createTeam",
  displayName: "Create Team",
  description: "Create a new Webex team. The authenticated user is automatically added as a member.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new Webex team with the given name and an optional description. Use it to set up a team that can group multiple rooms. Each call creates a distinct team, so it is not idempotent.",
    idempotent: !1
  },
  props: {
    name: import_pieces_framework5.Property.ShortText({
      displayName: "Team Name",
      description: "A user-friendly name for the team",
      required: !0
    }),
    description: import_pieces_framework5.Property.LongText({
      displayName: "Description",
      description: "The description of the team (optional)",
      required: !1
    })
  },
  async run(context) {
    let name = context.propsValue.name, description = context.propsValue.description, body = {
      name
    };
    return description && (body.description = description), await makeRequest(
      context.auth.access_token,
      import_pieces_common5.HttpMethod.POST,
      "/teams",
      body
    );
  }
});

// ../ap/packages/pieces/community/webex/src/lib/actions/find-message.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var findMessage = (0, import_pieces_framework6.createAction)({
  auth: webexAuth,
  name: "findMessage",
  displayName: "Find Message",
  description: "Retrieve details for a specific message by message ID",
  audience: "both",
  aiMetadata: {
    description: "Retrieves the details of a single Webex message by its message ID. Use it to look up the content and metadata of a known message. Read-only and idempotent; requires the exact message ID (this is not a search across messages).",
    idempotent: !0
  },
  props: {
    messageId: import_pieces_framework6.Property.ShortText({
      displayName: "Message ID",
      description: "The unique identifier for the message",
      required: !0
    })
  },
  async run(context) {
    let messageId = context.propsValue.messageId;
    return await makeRequest(
      context.auth.access_token,
      import_pieces_common6.HttpMethod.GET,
      `/messages/${encodeURIComponent(messageId)}`,
      void 0
    );
  }
});

// ../ap/packages/pieces/community/webex/src/lib/actions/find-room.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common"), findRoom = (0, import_pieces_framework7.createAction)({
  auth: webexAuth,
  name: "findRoom",
  displayName: "Find room",
  description: "Retrieve details for a specific room by room id",
  audience: "both",
  aiMetadata: {
    description: "Retrieves the details of a single Webex room (space) by its room ID. Use it to look up a known room's metadata. Read-only and idempotent; requires the exact room ID (this is not a search across rooms).",
    idempotent: !0
  },
  props: {
    roomId: import_pieces_framework7.Property.ShortText({
      displayName: "Room Id",
      description: "",
      required: !0
    })
  },
  async run(context) {
    let roomId = context.propsValue.roomId;
    return await makeRequest(
      context.auth.access_token,
      import_pieces_common7.HttpMethod.GET,
      `/rooms/${encodeURIComponent(roomId)}`,
      void 0
    );
  }
});

// ../ap/packages/pieces/community/webex/src/lib/triggers/new-room.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var import_pieces_common8 = require("@activepieces/pieces-common"), newRoom = (0, import_pieces_framework8.createTrigger)({
  auth: webexAuth,
  name: "newRoom",
  displayName: "New room",
  description: "Triggers when a new room is created",
  aiMetadata: {
    description: "Fires when a new Webex room (space) is created in the organization, delivering the room creation event via webhook."
  },
  props: {},
  sampleData: {
    id: "Y2lzY29zcGFyazovL3VybjpURUFNOnVzLWVhc3QtMS9XRUJIT09LLzEyMzQ1Njc4LWFiYzEtNGRlZi1hYmNkLWU5ODc2NTQzMjEwMQ",
    name: "Dummy Room Trigger Webhook",
    targetUrl: "https://example-webhook-url.com/api/v1/webhooks/abcd1234",
    resource: "rooms",
    event: "created",
    orgId: "Y2lzY29zcGFyazovL3VzL09SR0FOSVpBVElPTi84NzY1NDMyLTEyMzQtNDU2Ny1hYmNkLTU0MzIxMGV5ZXk4Nw",
    createdBy: "Y2lzY29zcGFyazovL3VzL1BFT1BMRS8xMjM0NTY3OC05YWJjLTRkZWYtOTg3Ni1lNTQzMjEwYWJjZGU",
    appId: "Y2lzY29zcGFyazovL3VzL0FQUExJQ0FUSU9OLzEyMzQ1Njc4LTkwYWItNGRlZi05ODc2LWU1NDMyMTBhYmNkZQ",
    ownedBy: "creator",
    status: "active",
    created: "2025-11-13T10:00:00.000Z",
    actorId: "Y2lzY29zcGFyazovL3VzL1BFT1BMRS8xMjM0NTY3OC05YWJjLTRkZWYtOTg3Ni1lNTQzMjEwYWJjZGU",
    data: {
      id: "Y2lzY29zcGFyazovL3VybjpURUFNOnVzLWVhc3QtMS9ST09NLzEyMzQ1Njc4LTkwYWItNGRlZi05ODc2LWU1NDMyMTBhYmNkZQ",
      type: "group",
      isLocked: !1,
      lastActivity: "2025-11-13T10:01:00.000Z",
      titleEncryptionKeyUrl: "kms://kms-example.com/keys/12345678-abcd-efgh-ijkl-9876543210mn",
      creatorId: "Y2lzY29zcGFyazovL3VzL1BFT1BMRS8xMjM0NTY3OC05YWJjLTRkZWYtOTg3Ni1lNTQzMjEwYWJjZGU",
      created: "2025-11-13T10:01:00.000Z",
      isAnnouncementOnly: !1
    }
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let response = await makeRequest(
      context.auth.access_token,
      import_pieces_common8.HttpMethod.POST,
      "/webhooks",
      {
        event: "created",
        resource: "rooms",
        targetUrl: context.webhookUrl,
        name: "New Room Trigger Webhook"
      }
    );
    await context.store.put("webex_new_room_trigger_id", {
      id: response.id
    });
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(
      "webex_new_room_trigger_id"
    ));
    response != null && await makeRequest(
      context.auth.access_token,
      import_pieces_common8.HttpMethod.DELETE,
      `/webhooks/${response.id}`
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/webex/src/lib/triggers/new-meeting.ts
var import_pieces_common9 = require("@activepieces/pieces-common"), import_pieces_framework9 = require("@activepieces/pieces-framework");
var newMeeting = (0, import_pieces_framework9.createTrigger)({
  auth: webexAuth,
  name: "newMeeting",
  displayName: "New meeting",
  description: "Triggers when a new meeting is created. ",
  aiMetadata: {
    description: "Fires when a new Webex meeting is created, delivering the meeting creation event via webhook."
  },
  props: {},
  sampleData: {},
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let response = await makeRequest(
      context.auth.access_token,
      import_pieces_common9.HttpMethod.POST,
      "/webhooks",
      {
        event: "created",
        resource: "meetings",
        targetUrl: context.webhookUrl,
        name: "New Meeting Trigger Webhook"
      }
    );
    await context.store.put("webex_new_meeting_trigger_id", {
      id: response.id
    });
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(
      "webex_new_meeting_trigger_id"
    ));
    response != null && await makeRequest(
      context.auth.access_token,
      import_pieces_common9.HttpMethod.DELETE,
      `/webhooks/${response.id}`
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/webex/src/index.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var webex = (0, import_pieces_framework10.createPiece)({
  displayName: "Cisco Webex Meetings",
  auth: webexAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/webex.png",
  description: "",
  categories: [import_pieces_framework11.PieceCategory.COMMUNICATION],
  authors: ["sanket-a11y"],
  actions: [
    createMessage,
    createRoom,
    createTeam,
    findMessage,
    findRoom,
    (0, import_pieces_common10.createCustomApiCallAction)({
      auth: webexAuth,
      baseUrl: () => BASE_URL,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.access_token}`
      })
    })
  ],
  triggers: [newRoom, newMeeting]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  webex
});
