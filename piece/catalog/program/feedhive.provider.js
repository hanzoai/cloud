"use strict";
var __defProp = Object.defineProperty, __defProps = Object.defineProperties, __getOwnPropDesc = Object.getOwnPropertyDescriptor, __getOwnPropDescs = Object.getOwnPropertyDescriptors, __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
}, __spreadProps = (a, b) => __defProps(a, __getOwnPropDescs(b));
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

// ../ap/packages/pieces/community/feedhive/src/index.ts
var index_exports = {};
__export(index_exports, {
  feedhive: () => feedhive,
  feedhiveAuth: () => feedhiveAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common"), import_pieces_framework13 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/feedhive/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), feedhiveAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `To get your FeedHive API key:
1. Log in to your FeedHive account.
2. Go to **Settings** \u2192 **Account** (or **Workspace**).
3. Find the **API Key** section and copy your key.

Your key starts with \`fh_\`.`,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: "https://api.feedhive.com/status",
        authentication: { type: import_pieces_common.AuthenticationType.BEARER_TOKEN, token: auth }
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API key. Make sure you copied it correctly from FeedHive Settings."
      };
    }
  }
});

// ../ap/packages/pieces/community/feedhive/src/lib/triggers/post-notification.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/feedhive/src/lib/common/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var BASE_URL = "https://api.feedhive.com", socialsMultiDropdown = import_pieces_framework2.Property.MultiSelectDropdown({
  displayName: "Social Accounts",
  description: "Select the social media accounts to publish this post to.",
  refreshers: [],
  required: !1,
  auth: feedhiveAuth,
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return { disabled: !0, options: [], placeholder: "Please connect your account first" };
    try {
      return {
        disabled: !1,
        options: ((_a = (await apiCall({ token: auth.secret_text, method: import_pieces_common2.HttpMethod.GET, path: "/socials" })).body.data) != null ? _a : []).filter((a) => a.status === "active").map((a) => ({
          label: `${a.display_name} (@${a.handle}) \u2014 ${a.platform}`,
          value: a.id
        }))
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load social accounts. Check your connection."
      };
    }
  }
}), labelsMultiDropdown = import_pieces_framework2.Property.MultiSelectDropdown({
  displayName: "Labels",
  description: "Optionally assign labels to organise this post in FeedHive.",
  refreshers: [],
  required: !1,
  auth: feedhiveAuth,
  options: async ({ auth }) => {
    var _a, _b;
    if (!auth)
      return { disabled: !0, options: [], placeholder: "Please connect your account first" };
    try {
      return {
        disabled: !1,
        options: ((_b = (_a = (await apiCall({ token: auth.secret_text, method: import_pieces_common2.HttpMethod.GET, path: "/labels" })).body.data) == null ? void 0 : _a.items) != null ? _b : []).map((l) => ({
          label: l.title,
          value: l.id
        }))
      };
    } catch (e) {
      return { disabled: !0, options: [], placeholder: "Failed to load labels. Check your connection." };
    }
  }
}), postDropdown = import_pieces_framework2.Property.Dropdown({
  displayName: "Post",
  description: "Select the post you want to act on.",
  refreshers: [],
  required: !0,
  auth: feedhiveAuth,
  options: async ({ auth }) => {
    var _a, _b;
    if (!auth)
      return { disabled: !0, options: [], placeholder: "Please connect your account first" };
    try {
      return {
        disabled: !1,
        options: ((_b = (_a = (await apiCall({
          token: auth.secret_text,
          method: import_pieces_common2.HttpMethod.GET,
          path: "/posts",
          queryParams: { limit: "100" }
        })).body.data) == null ? void 0 : _a.items) != null ? _b : []).map((p) => ({ label: `${p.text ? p.text.slice(0, 60) + (p.text.length > 60 ? "\u2026" : "") : "(no text)"} [${p.status}]`, value: p.id }))
      };
    } catch (e) {
      return { disabled: !0, options: [], placeholder: "Failed to load posts. Check your connection." };
    }
  }
}), feedhiveCommon = {
  socialsMultiDropdown,
  labelsMultiDropdown,
  postDropdown,
  apiCall,
  flattenPost
};
async function apiCall({
  token,
  method,
  path,
  body,
  queryParams
}) {
  return import_pieces_common2.httpClient.sendRequest({
    method,
    url: `${BASE_URL}${path}`,
    authentication: { type: import_pieces_common2.AuthenticationType.BEARER_TOKEN, token },
    queryParams,
    body
  });
}
function flattenPost(post) {
  var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m;
  let approval = post.approval;
  return {
    id: (_a = post.id) != null ? _a : null,
    status: (_b = post.status) != null ? _b : null,
    text: (_c = post.text) != null ? _c : null,
    accounts: Array.isArray(post.accounts) && post.accounts.length > 0 ? post.accounts.join(", ") : null,
    labels: Array.isArray(post.labels) && post.labels.length > 0 ? post.labels.join(", ") : null,
    media: Array.isArray(post.media) && post.media.length > 0 ? post.media.join(", ") : null,
    notes: (_d = post.notes) != null ? _d : null,
    short_link_enabled: (_e = post.short_link_enabled) != null ? _e : null,
    scheduled_at: (_f = post.scheduled_at) != null ? _f : null,
    published_at: (_g = post.published_at) != null ? _g : null,
    slot_id: (_h = post.slot_id) != null ? _h : null,
    approval_status: (_i = approval == null ? void 0 : approval.status) != null ? _i : null,
    approval_is_approved: (_j = approval == null ? void 0 : approval.is_approved) != null ? _j : null,
    approval_approved_at: (_k = approval == null ? void 0 : approval.approved_at) != null ? _k : null,
    created_at: (_l = post.created_at) != null ? _l : null,
    updated_at: (_m = post.updated_at) != null ? _m : null
  };
}

// ../ap/packages/pieces/community/feedhive/src/lib/triggers/post-notification.ts
var EVENT_TYPE_STATUS_MAP = {
  post_published: "published",
  post_scheduled: "scheduled",
  post_failed: "failed"
}, postNotificationTrigger = (0, import_pieces_framework3.createTrigger)({
  auth: feedhiveAuth,
  name: "post_notification",
  displayName: "Post Notification",
  description: "Triggers when a post is published, scheduled, or fails.",
  aiMetadata: {
    description: "Fires via webhook when a FeedHive post changes state \u2014 published, scheduled, or failed \u2014 depending on which notification event type is configured in FeedHive. Represents a lifecycle event for a single post, carrying its ID, excerpt, and target social account."
  },
  props: {
    event_type: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Event Type",
      description: "Choose the event type you will configure in FeedHive. This is used to load sample data when testing the trigger \u2014 it does not filter incoming webhooks.",
      required: !0,
      defaultValue: "post_published",
      options: {
        options: [
          { label: "Post Published", value: "post_published" },
          { label: "Post Scheduled", value: "post_scheduled" },
          { label: "Post Failed", value: "post_failed" }
        ]
      }
    }),
    instructions: import_pieces_framework3.Property.MarkDown({
      value: `### Setup Instructions

1. Enable this flow and copy the **Webhook URL** shown below.
 \`\`\`text
  {{webhookUrl}}
  \`\`\`
2. In FeedHive, go to **Settings \u2192 Automation \u2192 Notifications**.
3. Click **Create New Notification**.
4. Choose the matching event type and paste the Webhook URL, then click **Save**.`
    })
  },
  sampleData: {
    post_id: "xxxx-abcd-1234",
    post_excerpt: "This is the first part of the post caption...",
    social_type: "Facebook",
    social_name: "My Facebook Page",
    public_id: "123456789101112_1314151617181920",
    public_url: "https://www.facebook.com/xxxx/posts/yyyy",
    post: {
      content: [{ text: "Full post caption.", media: [] }],
      labels: ["Marketing"],
      postNotes: "Internal notes"
    }
  },
  type: import_pieces_framework3.TriggerStrategy.WEBHOOK,
  async onEnable(_context) {
  },
  async onDisable(_context) {
  },
  async run(context) {
    return [context.payload.body];
  },
  async test(context) {
    var _a, _b, _c, _d;
    let eventType = (_a = context.propsValue.event_type) != null ? _a : "post_published", status = (_b = EVENT_TYPE_STATUS_MAP[eventType]) != null ? _b : "published";
    return ((_d = (_c = (await feedhiveCommon.apiCall({
      token: context.auth.secret_text,
      method: import_pieces_common3.HttpMethod.GET,
      path: "/posts",
      queryParams: { limit: "5", status }
    })).body.data) == null ? void 0 : _c.items) != null ? _d : []).map((post) => buildSamplePayload(eventType, post));
  }
});
function buildSamplePayload(eventType, post) {
  var _a, _b, _c, _d, _e, _f, _g;
  let postExcerpt = typeof post.text == "string" ? post.text.slice(0, 100) + (post.text.length > 100 ? "..." : "") : "(no text)", notificationPost = {
    content: [{ text: (_a = post.text) != null ? _a : "", media: (_b = post.media) != null ? _b : [] }],
    labels: (_c = post.labels) != null ? _c : [],
    postNotes: (_d = post.notes) != null ? _d : ""
  }, base = {
    post_id: (_e = post.id) != null ? _e : null,
    post_excerpt: postExcerpt,
    post: notificationPost
  };
  switch (eventType) {
    case "post_published":
      return __spreadProps(__spreadValues({}, base), {
        social_type: "(see FeedHive notification)",
        social_name: "(see FeedHive notification)",
        public_id: null,
        public_url: null
      });
    case "post_scheduled":
      return __spreadProps(__spreadValues({}, base), {
        schedule_at: (_f = post.scheduled_at) != null ? _f : null,
        pending_approval: !1,
        social_types: "(see FeedHive notification)"
      });
    case "post_failed":
      return __spreadProps(__spreadValues({}, base), {
        failed_at: (_g = post.updated_at) != null ? _g : null,
        message: "(see FeedHive notification)",
        social_type: "(see FeedHive notification)",
        social_name: "(see FeedHive notification)"
      });
    default:
      return base;
  }
}

// ../ap/packages/pieces/community/feedhive/src/lib/triggers/comment-notification.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var commentNotificationTrigger = (0, import_pieces_framework4.createTrigger)({
  auth: feedhiveAuth,
  name: "comment_notification",
  displayName: "Comment Notification",
  description: "Triggers when a comment is added to one of your posts.",
  aiMetadata: {
    description: "Fires via webhook when a new comment is added to one of your FeedHive posts. Represents a single incoming comment, carrying the comment text and author details along with the related post."
  },
  props: {
    instructions: import_pieces_framework4.Property.MarkDown({
      value: `### Setup Instructions

1. Enable this flow and copy the **Webhook URL** shown below.
 \`\`\`text
  {{webhookUrl}}
  \`\`\`
2. In FeedHive, go to **Settings \u2192 Automation \u2192 Notifications**.
3. Click **Create New Notification**.
4. Choose **Comment Added** and paste the Webhook URL, then click **Save**.`
    })
  },
  sampleData: {
    post_id: "xxxx-abcd-1234",
    comment_id: "yyyy-zxyw-4321",
    commented_at: "2024-08-01T12:00:00.000Z",
    commented_by: "zzzz-efgh-5678",
    commented_by_name: "John Doe",
    commented_by_email: "johndoe@gmail.com",
    comment_text: "Great post. Perhaps add another image?",
    post_excerpt: "This is the first part of the post caption...",
    post: {
      content: [{ text: "Full post caption.", media: [] }],
      labels: ["Marketing"],
      postNotes: "Internal notes"
    }
  },
  type: import_pieces_framework4.TriggerStrategy.WEBHOOK,
  async onEnable(_context) {
  },
  async onDisable(_context) {
  },
  async run(context) {
    return [context.payload.body];
  },
  async test(context) {
    var _a, _b;
    return ((_b = (_a = (await feedhiveCommon.apiCall({
      token: context.auth.secret_text,
      method: import_pieces_common4.HttpMethod.GET,
      path: "/posts",
      queryParams: { limit: "5", status: "published" }
    })).body.data) == null ? void 0 : _a.items) != null ? _b : []).map((post) => {
      var _a2, _b2, _c, _d, _e;
      let postExcerpt = typeof post.text == "string" ? post.text.slice(0, 100) + (post.text.length > 100 ? "..." : "") : "(no text)";
      return {
        post_id: (_a2 = post.id) != null ? _a2 : null,
        comment_id: null,
        commented_at: null,
        commented_by: null,
        commented_by_name: "(see FeedHive notification)",
        commented_by_email: "(see FeedHive notification)",
        comment_text: "(see FeedHive notification)",
        post_excerpt: postExcerpt,
        post: {
          content: [{ text: (_b2 = post.text) != null ? _b2 : "", media: (_c = post.media) != null ? _c : [] }],
          labels: (_d = post.labels) != null ? _d : [],
          postNotes: (_e = post.notes) != null ? _e : ""
        }
      };
    });
  }
});

// ../ap/packages/pieces/community/feedhive/src/lib/actions/create-post.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var createPostAction = (0, import_pieces_framework5.createAction)({
  auth: feedhiveAuth,
  name: "create_post",
  displayName: "Create Post",
  description: "Creates a new post in FeedHive (draft or scheduled).",
  audience: "both",
  aiMetadata: { description: 'Creates a new FeedHive social media post from provided text, either saved as a draft or scheduled for a chosen publish date (set Status to "scheduled" and supply a date/time). Use to author and queue content for one or more connected social accounts. Each call creates a separate post, so it is not idempotent.', idempotent: !1 },
  props: {
    text: import_pieces_framework5.Property.LongText({
      displayName: "Post Text",
      description: "The main content of your post.",
      required: !0
    }),
    status: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Status",
      description: 'Choose "Draft" to save without scheduling, or "Scheduled" to set a publish date.',
      required: !0,
      defaultValue: "draft",
      options: {
        options: [
          { label: "Draft", value: "draft" },
          { label: "Scheduled", value: "scheduled" }
        ]
      }
    }),
    scheduled_at: import_pieces_framework5.Property.DateTime({
      displayName: "Scheduled Date & Time",
      description: 'Required when Status is "Scheduled". The exact date and time the post should be published.',
      required: !1
    }),
    accounts: feedhiveCommon.socialsMultiDropdown,
    labels: feedhiveCommon.labelsMultiDropdown,
    notes: import_pieces_framework5.Property.LongText({
      displayName: "Internal Notes",
      description: "Private notes visible only to your team in FeedHive. Not published.",
      required: !1
    }),
    short_link_enabled: import_pieces_framework5.Property.Checkbox({
      displayName: "Enable Short Links",
      description: "Automatically shorten URLs in your post text.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { text, status, scheduled_at, accounts, labels, notes, short_link_enabled } = context.propsValue;
    if (status === "scheduled" && !scheduled_at)
      throw new Error('Scheduled Date & Time is required when Status is set to "Scheduled".');
    let body = { text, status };
    accounts && accounts.length > 0 && (body.accounts = accounts), labels && labels.length > 0 && (body.labels = labels), scheduled_at && (body.scheduled_at = scheduled_at), notes && (body.notes = notes), short_link_enabled !== void 0 && (body.short_link_enabled = short_link_enabled);
    let response = await feedhiveCommon.apiCall({
      token: context.auth.secret_text,
      method: import_pieces_common5.HttpMethod.POST,
      path: "/posts",
      body
    });
    return feedhiveCommon.flattenPost(response.body.data);
  }
});

// ../ap/packages/pieces/community/feedhive/src/lib/actions/update-post.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var updatePostAction = (0, import_pieces_framework6.createAction)({
  auth: feedhiveAuth,
  name: "update_post",
  displayName: "Update Post",
  description: "Updates an existing post in FeedHive. Only the fields you fill in will be changed.",
  audience: "both",
  aiMetadata: { description: "Updates an existing FeedHive post identified by its post ID; only the fields you supply (text, status, schedule, accounts, labels, notes) are changed, leaving the rest intact. Use to edit or reschedule a draft or scheduled post. Idempotent: applying the same field values repeatedly leaves the post in the same state.", idempotent: !0 },
  props: {
    post_id: feedhiveCommon.postDropdown,
    text: import_pieces_framework6.Property.LongText({
      displayName: "Post Text",
      description: "The new content of the post. Leave empty to keep the current text.",
      required: !1
    }),
    status: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Status",
      description: "Change the post status. Leave empty to keep the current status.",
      required: !1,
      options: {
        options: [
          { label: "Draft", value: "draft" },
          { label: "Scheduled", value: "scheduled" }
        ]
      }
    }),
    scheduled_at: import_pieces_framework6.Property.DateTime({
      displayName: "Scheduled Date & Time",
      description: "Update the scheduled publish date and time.",
      required: !1
    }),
    accounts: feedhiveCommon.socialsMultiDropdown,
    labels: feedhiveCommon.labelsMultiDropdown,
    notes: import_pieces_framework6.Property.LongText({
      displayName: "Internal Notes",
      description: "Update the private notes for this post.",
      required: !1
    }),
    short_link_enabled: import_pieces_framework6.Property.Checkbox({
      displayName: "Enable Short Links",
      description: "Automatically shorten URLs in the post text.",
      required: !1
    })
  },
  async run(context) {
    let { post_id, text, status, scheduled_at, accounts, labels, notes, short_link_enabled } = context.propsValue, body = {};
    text && (body.text = text), status && (body.status = status), scheduled_at && (body.scheduled_at = scheduled_at), accounts && accounts.length > 0 && (body.accounts = accounts), labels !== void 0 && (body.labels = labels != null ? labels : []), notes && (body.notes = notes), short_link_enabled != null && (body.short_link_enabled = short_link_enabled);
    let response = await feedhiveCommon.apiCall({
      token: context.auth.secret_text,
      method: import_pieces_common6.HttpMethod.PATCH,
      path: `/posts/${post_id}`,
      body
    });
    return feedhiveCommon.flattenPost(response.body.data);
  }
});

// ../ap/packages/pieces/community/feedhive/src/lib/actions/get-post.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var getPostAction = (0, import_pieces_framework7.createAction)({
  auth: feedhiveAuth,
  name: "get_post",
  displayName: "Get Post",
  description: "Retrieves the details of a specific post.",
  audience: "both",
  aiMetadata: { description: "Fetches the full details of a single FeedHive post by its post ID. Use to read a post's current content, status, schedule, accounts, and labels before acting on it. Read-only and idempotent.", idempotent: !0 },
  props: {
    post_id: feedhiveCommon.postDropdown
  },
  async run(context) {
    let response = await feedhiveCommon.apiCall({
      token: context.auth.secret_text,
      method: import_pieces_common7.HttpMethod.GET,
      path: `/posts/${context.propsValue.post_id}`
    });
    return feedhiveCommon.flattenPost(response.body.data);
  }
});

// ../ap/packages/pieces/community/feedhive/src/lib/actions/delete-post.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var deletePostAction = (0, import_pieces_framework8.createAction)({
  auth: feedhiveAuth,
  name: "delete_post",
  displayName: "Delete Post",
  description: "Deletes a post from FeedHive. This action cannot be undone.",
  audience: "both",
  aiMetadata: { description: "Permanently deletes a FeedHive post by its post ID; this cannot be undone. Use to remove a draft or scheduled post you no longer want. Idempotent: once the post is gone, repeating the call leaves it deleted with no further effect.", idempotent: !0 },
  props: {
    post_id: feedhiveCommon.postDropdown
  },
  async run(context) {
    return await feedhiveCommon.apiCall({
      token: context.auth.secret_text,
      method: import_pieces_common8.HttpMethod.DELETE,
      path: `/posts/${context.propsValue.post_id}`
    }), { success: !0, deleted_post_id: context.propsValue.post_id };
  }
});

// ../ap/packages/pieces/community/feedhive/src/lib/actions/list-posts.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var listPostsAction = (0, import_pieces_framework9.createAction)({
  auth: feedhiveAuth,
  name: "list_posts",
  displayName: "List Posts",
  description: "Returns a list of posts, optionally filtered by status or labels.",
  audience: "both",
  aiMetadata: { description: "Lists FeedHive posts, optionally narrowed by status (draft, scheduled, publishing, published, failed) and/or labels; leaving both filters empty returns all posts up to the result limit (max 100). Use to discover post IDs or survey the content queue. Read-only and idempotent.", idempotent: !0 },
  props: {
    status: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Filter by Status",
      description: "Only return posts with this status. Leave empty to return all posts.",
      required: !1,
      options: {
        options: [
          { label: "Draft", value: "draft" },
          { label: "Scheduled", value: "scheduled" },
          { label: "Publishing", value: "publishing" },
          { label: "Published", value: "published" },
          { label: "Failed", value: "failed" }
        ]
      }
    }),
    labels: feedhiveCommon.labelsMultiDropdown,
    limit: import_pieces_framework9.Property.Number({
      displayName: "Maximum Results",
      description: "Maximum number of posts to return. Between 1 and 100.",
      required: !1,
      defaultValue: 20
    })
  },
  async run(context) {
    var _a, _b;
    let { status, labels, limit } = context.propsValue, queryParams = {
      limit: String(Math.min(limit != null ? limit : 20, 100))
    };
    return status && (queryParams.status = status), labels && labels.length > 0 && (queryParams.labels = labels.join(",")), ((_b = (_a = (await feedhiveCommon.apiCall({
      token: context.auth.secret_text,
      method: import_pieces_common9.HttpMethod.GET,
      path: "/posts",
      queryParams
    })).body.data) == null ? void 0 : _a.items) != null ? _b : []).map((p) => feedhiveCommon.flattenPost(p));
  }
});

// ../ap/packages/pieces/community/feedhive/src/lib/actions/create-label.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var createLabelAction = (0, import_pieces_framework10.createAction)({
  auth: feedhiveAuth,
  name: "create_label",
  displayName: "Create Label",
  description: "Creates a new label in FeedHive for organising posts.",
  audience: "both",
  aiMetadata: { description: "Creates a new label in FeedHive that can later be attached to posts for organisation. Use when a label an agent needs does not yet exist. Not idempotent: each call creates a new label even if one with the same title already exists.", idempotent: !1 },
  props: {
    title: import_pieces_framework10.Property.ShortText({
      displayName: "Label Name",
      description: 'The name of the label (e.g. "Campaign Q1", "Evergreen Content").',
      required: !0
    })
  },
  async run(context) {
    var _a, _b, _c, _d;
    let response = await feedhiveCommon.apiCall({
      token: context.auth.secret_text,
      method: import_pieces_common10.HttpMethod.POST,
      path: "/labels",
      body: { title: context.propsValue.title }
    });
    return {
      id: (_b = (_a = response.body.data) == null ? void 0 : _a.id) != null ? _b : null,
      title: (_d = (_c = response.body.data) == null ? void 0 : _c.title) != null ? _d : null
    };
  }
});

// ../ap/packages/pieces/community/feedhive/src/lib/actions/fire-workflow-trigger.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var fireWorkflowTriggerAction = (0, import_pieces_framework11.createAction)({
  auth: feedhiveAuth,
  name: "fire_workflow_trigger",
  displayName: "Create Post via Trigger",
  description: "Fires a FeedHive Workflow Trigger to create a post. The workflow defines which accounts to post to, the AI model to use, and any default content.",
  audience: "both",
  aiMetadata: { description: "Fires a pre-configured FeedHive workflow trigger (identified by its Trigger ID) to create a post, letting the workflow decide accounts, AI model, and defaults; you can optionally override with explicit text, an AI prompt, schedule, media URLs, title/link, or template variables that fill [[placeholder]] tokens in the workflow template. Use when posting through an existing FeedHive workflow rather than building the post directly. Not idempotent: each call fires the workflow and creates a new post.", idempotent: !1 },
  props: {
    setup_instructions: import_pieces_framework11.Property.MarkDown({
      value: `### How to find your Trigger ID

1. In FeedHive, open the **Workflows** section in the sidebar.
2. Open the workflow you want to fire.
3. In the workflow settings, copy the **Trigger ID** (a short alphanumeric code).

**Note:** You can also pass custom template variables \u2014 if your workflow content contains placeholders like \`[[first_name]]\`, add them in the **Template Variables** field below.`
    }),
    trigger_id: import_pieces_framework11.Property.ShortText({
      displayName: "Trigger ID",
      description: "The ID of your FeedHive workflow trigger. Found in FeedHive \u2192 Workflows \u2192 your workflow settings.",
      required: !0
    }),
    text: import_pieces_framework11.Property.LongText({
      displayName: "Post Text",
      description: "The content of the post. Leave empty to let your workflow generate or use a template.",
      required: !1
    }),
    prompt: import_pieces_framework11.Property.LongText({
      displayName: "AI Prompt",
      description: "Ask FeedHive AI to generate the post content. Only used if your workflow is configured for AI generation.",
      required: !1
    }),
    scheduled: import_pieces_framework11.Property.DateTime({
      displayName: "Scheduled Date & Time",
      description: "When to publish the post. Leave empty to use the schedule defined in the workflow.",
      required: !1
    }),
    media_urls: import_pieces_framework11.Property.Array({
      displayName: "Media URLs",
      description: "Public URLs of images or videos to include in the post (e.g. https://example.com/photo.jpg).",
      required: !1
    }),
    title: import_pieces_framework11.Property.ShortText({
      displayName: "Title",
      description: "Post title \u2014 only applicable for YouTube, Google My Business, and Pinterest posts.",
      required: !1
    }),
    link: import_pieces_framework11.Property.ShortText({
      displayName: "Link URL",
      description: "A URL to attach to the post \u2014 only applicable for YouTube, Google My Business, and Pinterest posts.",
      required: !1
    }),
    template_variables: import_pieces_framework11.Property.Object({
      displayName: "Template Variables",
      description: "If your workflow template uses placeholders like [[first_name]], provide them here as key-value pairs (e.g. first_name \u2192 John). Each key maps directly to a [[placeholder]] in your template.",
      required: !1
    })
  },
  async run(context) {
    var _a;
    let { trigger_id, text, prompt, scheduled, media_urls, title, link, template_variables } = context.propsValue, body = __spreadValues({}, template_variables != null ? template_variables : {});
    return text && (body.text = text), prompt && (body.prompt = prompt), scheduled && (body.scheduled = scheduled), media_urls && media_urls.length > 0 && (body.media_urls = media_urls), title && (body.title = title), link && (body.link = link), { success: (_a = (await feedhiveCommon.apiCall({
      token: context.auth.secret_text,
      method: import_pieces_common11.HttpMethod.POST,
      path: `/triggers/${trigger_id}`,
      body
    })).body.success) != null ? _a : !0 };
  }
});

// ../ap/packages/pieces/community/feedhive/src/index.ts
var feedhive = (0, import_pieces_framework12.createPiece)({
  displayName: "FeedHive",
  description: "Schedule, manage and publish social media content with FeedHive.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/feedhive.png",
  categories: [import_pieces_framework13.PieceCategory.MARKETING],
  auth: feedhiveAuth,
  authors: ["bst1n"],
  actions: [
    createPostAction,
    updatePostAction,
    getPostAction,
    deletePostAction,
    listPostsAction,
    createLabelAction,
    fireWorkflowTriggerAction,
    (0, import_pieces_common12.createCustomApiCallAction)({
      baseUrl: () => "https://api.feedhive.com",
      auth: feedhiveAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [postNotificationTrigger, commentNotificationTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  feedhive,
  feedhiveAuth
});
