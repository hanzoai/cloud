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

// ../ap/packages/pieces/community/hootsuite/src/index.ts
var index_exports = {};
__export(index_exports, {
  hootsuite: () => hootsuite,
  hootsuiteAuth: () => hootsuiteAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/hootsuite/src/lib/actions/create-message.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/hootsuite/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), hootsuiteAuth = import_pieces_framework.PieceAuth.OAuth2({
  description: "Connect your Hootsuite account. Create an OAuth2 app and get your client credentials at https://developer.hootsuite.com.",
  required: !0,
  authUrl: "https://platform.hootsuite.com/oauth2/auth",
  tokenUrl: "https://platform.hootsuite.com/oauth2/token",
  scope: ["offline", "post"]
});

// ../ap/packages/pieces/community/hootsuite/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://platform.hootsuite.com/v1";
async function hootsuiteApiCall({
  auth,
  method,
  path,
  body,
  queryParams
}) {
  return import_pieces_common.httpClient.sendRequest({
    method,
    url: `${BASE_URL}${path}`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token: auth.access_token
    },
    body,
    queryParams
  });
}

// ../ap/packages/pieces/community/hootsuite/src/lib/actions/create-message.ts
var createMessageAction = (0, import_pieces_framework2.createAction)({
  auth: hootsuiteAuth,
  name: "create_message",
  displayName: "Schedule Post",
  description: "Schedules or immediately publishes a post to one or more social media profiles.",
  audience: "both",
  aiMetadata: { description: "Publishes a post to one or more connected Hootsuite social profiles, either immediately or scheduled for a future time depending on whether a scheduled time is provided (leave it empty to post now; a future time must be at least 5 minutes ahead and in UTC). Use to create and distribute social media content across networks. Not idempotent: each call creates a new post, so repeating it produces duplicate posts. Requires the target social profile IDs and the post text.", idempotent: !1 },
  props: {
    socialProfileIds: import_pieces_framework2.Property.MultiSelectDropdown({
      displayName: "Social Profiles",
      description: "Select the social media profiles to post to.",
      refreshers: [],
      required: !0,
      auth: hootsuiteAuth,
      options: async ({ auth }) => {
        if (!auth)
          return { disabled: !0, options: [], placeholder: "Please connect your Hootsuite account first" };
        try {
          return {
            disabled: !1,
            options: (await hootsuiteApiCall({
              auth,
              method: import_pieces_common2.HttpMethod.GET,
              path: "/socialProfiles"
            })).body.data.map((p) => {
              var _a;
              return {
                label: `${(_a = p.socialNetworkUsername) != null ? _a : p.id} (${p.type})`,
                value: p.id
              };
            })
          };
        } catch (e) {
          return { disabled: !0, options: [], placeholder: "Failed to load profiles. Check your connection." };
        }
      }
    }),
    text: import_pieces_framework2.Property.LongText({
      displayName: "Post Text",
      description: "The text content of your post.",
      required: !0
    }),
    scheduledSendTime: import_pieces_framework2.Property.DateTime({
      displayName: "Scheduled Time",
      description: "When to publish the post (UTC, must be at least 5 minutes in the future). Leave empty to post immediately.",
      required: !1
    }),
    emailNotification: import_pieces_framework2.Property.Checkbox({
      displayName: "Email Notification",
      description: "Send an email notification when the post is published.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e;
    let { socialProfileIds, text, scheduledSendTime, emailNotification } = context.propsValue, body = {
      text,
      socialProfileIds,
      emailNotification
    };
    scheduledSendTime && (body.scheduledSendTime = new Date(scheduledSendTime).toISOString());
    let msg = (await hootsuiteApiCall({
      auth: context.auth,
      method: import_pieces_common2.HttpMethod.POST,
      path: "/messages",
      body
    })).body.data;
    return {
      id: msg.id,
      state: msg.state,
      scheduled_send_time: (_a = msg.scheduledSendTime) != null ? _a : null,
      text: (_b = msg.text) != null ? _b : text,
      social_profile_ids: ((_c = msg.socialProfileIds) != null ? _c : socialProfileIds).join(", "),
      post_url: (_d = msg.postUrl) != null ? _d : null,
      errors: (_e = msg.errors) != null ? _e : []
    };
  }
});

// ../ap/packages/pieces/community/hootsuite/src/lib/actions/get-message.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var getMessageAction = (0, import_pieces_framework3.createAction)({
  auth: hootsuiteAuth,
  name: "get_message",
  displayName: "Get Post Details",
  description: "Retrieves the details of a specific scheduled or published post by its ID.",
  audience: "both",
  aiMetadata: { description: "Looks up a single Hootsuite post by its ID and returns its current details and state (e.g. scheduled, sent, errored). Use to inspect or confirm a post created earlier or referenced from the dashboard. Idempotent read-only lookup. Requires the post ID, obtainable from a Schedule Post output or the Hootsuite dashboard URL.", idempotent: !0 },
  props: {
    messageId: import_pieces_framework3.Property.ShortText({
      displayName: "Post ID",
      description: 'The unique ID of the post. You can get this from a previous "Schedule Post" action output or from the Hootsuite dashboard URL.',
      required: !0
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i;
    let msg = (await hootsuiteApiCall({
      auth: context.auth,
      method: import_pieces_common3.HttpMethod.GET,
      path: `/messages/${context.propsValue.messageId}`
    })).body.data;
    return {
      id: msg.id,
      state: msg.state,
      text: (_a = msg.text) != null ? _a : null,
      scheduled_send_time: (_b = msg.scheduledSendTime) != null ? _b : null,
      send_time: (_c = msg.sendTime) != null ? _c : null,
      social_profile_ids: ((_d = msg.socialProfileIds) != null ? _d : []).join(", "),
      post_url: (_e = msg.postUrl) != null ? _e : null,
      tags: (_f = msg.tags) != null ? _f : [],
      email_notification: (_g = msg.emailNotification) != null ? _g : null,
      location: (_h = msg.location) != null ? _h : null,
      errors: (_i = msg.errors) != null ? _i : []
    };
  }
});

// ../ap/packages/pieces/community/hootsuite/src/lib/actions/delete-message.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var deleteMessageAction = (0, import_pieces_framework4.createAction)({
  auth: hootsuiteAuth,
  name: "delete_message",
  displayName: "Delete Post",
  description: "Permanently deletes a post. Only posts in a deletable state (e.g. SCHEDULED) can be removed.",
  audience: "both",
  aiMetadata: { description: "Permanently deletes a Hootsuite post by its ID. Use to cancel or remove a scheduled post before it sends. Only posts in a deletable state (e.g. SCHEDULED) can be removed; already-sent posts cannot. Not idempotent: the first call removes the post and subsequent calls with the same ID will fail since it no longer exists.", idempotent: !1 },
  props: {
    messageId: import_pieces_framework4.Property.ShortText({
      displayName: "Post ID",
      description: 'The unique ID of the post to delete. You can get this from the "Schedule Post" or "Get Post Details" action.',
      required: !0
    })
  },
  async run(context) {
    return await hootsuiteApiCall({
      auth: context.auth,
      method: import_pieces_common4.HttpMethod.DELETE,
      path: `/messages/${context.propsValue.messageId}`
    }), {
      success: !0,
      deleted_message_id: context.propsValue.messageId
    };
  }
});

// ../ap/packages/pieces/community/hootsuite/src/lib/triggers/new-scheduled-post.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var polling = {
  // LAST_ITEM is correct here: we want to fire when a post is newly created/scheduled,
  // not when its scheduled time arrives. TIMEBASED with scheduledSendTime would fire
  // based on due-time, causing posts scheduled for earlier dates to be missed.
  strategy: import_pieces_common5.DedupeStrategy.LAST_ITEM,
  items: async ({ auth }) => {
    var _a;
    return ((_a = (await hootsuiteApiCall({
      auth,
      method: import_pieces_common5.HttpMethod.GET,
      path: "/messages",
      queryParams: {
        state: "SCHEDULED",
        limit: "50"
      }
    })).body.data) != null ? _a : []).sort((a, b) => new Date(b.scheduledSendTime).getTime() - new Date(a.scheduledSendTime).getTime()).map((msg) => {
      var _a2, _b;
      return {
        id: msg.id,
        data: {
          id: msg.id,
          state: msg.state,
          scheduled_send_time: msg.scheduledSendTime,
          text: (_a2 = msg.text) != null ? _a2 : null,
          social_profile_ids: ((_b = msg.socialProfileIds) != null ? _b : []).join(", ")
        }
      };
    });
  }
}, newScheduledPostTrigger = (0, import_pieces_framework5.createTrigger)({
  auth: hootsuiteAuth,
  name: "new_scheduled_post",
  displayName: "New Scheduled Post",
  description: "Triggers when a new post is scheduled in Hootsuite.",
  aiMetadata: {
    description: "Fires when a new post enters the SCHEDULED state in Hootsuite, representing content queued for future publishing. Fires at scheduling time, not when the post actually sends."
  },
  props: {},
  sampleData: {
    id: "msg_abc123",
    state: "SCHEDULED",
    scheduled_send_time: "2024-06-01T10:00:00Z",
    text: "Check out our latest blog post!",
    social_profile_ids: "profile_1, profile_2"
  },
  type: import_pieces_framework5.TriggerStrategy.POLLING,
  async test(context) {
    return import_pieces_common5.pollingHelper.test(polling, context);
  },
  async onEnable(context) {
    await import_pieces_common5.pollingHelper.onEnable(polling, context);
  },
  async onDisable(context) {
    await import_pieces_common5.pollingHelper.onDisable(polling, context);
  },
  async run(context) {
    return import_pieces_common5.pollingHelper.poll(polling, context);
  }
});

// ../ap/packages/pieces/community/hootsuite/src/lib/triggers/new-published-post.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var polling2 = {
  strategy: import_pieces_common6.DedupeStrategy.TIMEBASED,
  items: async ({ auth }) => {
    var _a;
    return ((_a = (await hootsuiteApiCall({
      auth,
      method: import_pieces_common6.HttpMethod.GET,
      path: "/messages",
      queryParams: {
        state: "SENT",
        limit: "50"
      }
    })).body.data) != null ? _a : []).filter((msg) => !!msg.sendTime).map((msg) => {
      var _a2, _b, _c;
      return {
        epochMilliSeconds: new Date(msg.sendTime).getTime(),
        data: {
          id: msg.id,
          state: msg.state,
          send_time: msg.sendTime,
          text: (_a2 = msg.text) != null ? _a2 : null,
          post_url: (_b = msg.postUrl) != null ? _b : null,
          social_profile_ids: ((_c = msg.socialProfileIds) != null ? _c : []).join(", ")
        }
      };
    });
  }
}, newPublishedPostTrigger = (0, import_pieces_framework6.createTrigger)({
  auth: hootsuiteAuth,
  name: "new_published_post",
  displayName: "New Published Post",
  description: "Triggers when a post is published (sent) in Hootsuite.",
  aiMetadata: {
    description: "Fires when a post transitions to the SENT state in Hootsuite, representing content that has actually been published to its social networks."
  },
  props: {},
  sampleData: {
    id: "msg_xyz456",
    state: "SENT",
    send_time: "2024-06-01T10:00:00Z",
    text: "Check out our latest blog post!",
    post_url: "https://twitter.com/example/status/123456",
    social_profile_ids: "profile_1"
  },
  type: import_pieces_framework6.TriggerStrategy.POLLING,
  async test(context) {
    return import_pieces_common6.pollingHelper.test(polling2, context);
  },
  async onEnable(context) {
    await import_pieces_common6.pollingHelper.onEnable(polling2, context);
  },
  async onDisable(context) {
    await import_pieces_common6.pollingHelper.onDisable(polling2, context);
  },
  async run(context) {
    return import_pieces_common6.pollingHelper.poll(polling2, context);
  }
});

// ../ap/packages/pieces/community/hootsuite/src/index.ts
var hootsuite = (0, import_pieces_framework7.createPiece)({
  displayName: "Hootsuite",
  description: "Social media management \u2014 schedule posts and manage multiple networks from one dashboard.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/hootsuite.png",
  categories: [import_pieces_framework8.PieceCategory.MARKETING],
  auth: hootsuiteAuth,
  authors: ["onyedikachi-david"],
  actions: [
    createMessageAction,
    getMessageAction,
    deleteMessageAction,
    (0, import_pieces_common7.createCustomApiCallAction)({
      baseUrl: () => "https://platform.hootsuite.com/v1",
      auth: hootsuiteAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.access_token}`
      })
    })
  ],
  triggers: [newScheduledPostTrigger, newPublishedPostTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  hootsuite,
  hootsuiteAuth
});
