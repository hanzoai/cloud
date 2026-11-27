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

// ../ap/packages/pieces/community/send-it/src/index.ts
var index_exports = {};
__export(index_exports, {
  sendIt: () => sendIt
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common"), import_pieces_framework14 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/send-it/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://sendit.infiniteappsai.com/api/v1", sendItAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `To get your SendIt API key:
1. Log in to your [SendIt dashboard](https://sendit.infiniteappsai.com/dashboard)
2. Go to **Settings > API Keys**
3. Click **Create API Key** \u2014 the key is only shown once, so copy it immediately
4. Paste the key below (it starts with \`sk_live_\`)`,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${BASE_URL}/accounts`,
        headers: { Authorization: `Bearer ${auth}` }
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API key. Make sure you copied the full key starting with sk_live_."
      };
    }
  }
});

// ../ap/packages/pieces/community/send-it/src/lib/actions/publish-post.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/send-it/src/lib/common.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var API_BASE = "https://sendit.infiniteappsai.com/api/v1", options = [
  { label: "X (Twitter)", value: "x" },
  { label: "LinkedIn", value: "linkedin" },
  { label: "LinkedIn Page", value: "linkedin-page" },
  { label: "Facebook", value: "facebook" },
  { label: "Instagram", value: "instagram" },
  { label: "Instagram Standalone", value: "instagram-standalone" },
  { label: "Threads", value: "threads" },
  { label: "Bluesky", value: "bluesky" },
  { label: "Mastodon", value: "mastodon" },
  { label: "Warpcast", value: "warpcast" },
  { label: "Nostr", value: "nostr" },
  { label: "VK", value: "vk" },
  { label: "YouTube", value: "youtube" },
  { label: "TikTok", value: "tiktok" },
  { label: "Reddit", value: "reddit" },
  { label: "Lemmy", value: "lemmy" },
  { label: "Discord", value: "discord" },
  { label: "Slack", value: "slack" },
  { label: "Telegram", value: "telegram" },
  { label: "Pinterest", value: "pinterest" },
  { label: "Dribbble", value: "dribbble" },
  { label: "Medium", value: "medium" },
  { label: "DEV.to", value: "devto" },
  { label: "Hashnode", value: "hashnode" },
  { label: "WordPress", value: "wordpress" },
  { label: "Google My Business", value: "gmb" },
  { label: "Listmonk", value: "listmonk" },
  { label: "Skool", value: "skool" },
  { label: "Whop", value: "whop" },
  { label: "Kick", value: "kick" },
  { label: "Twitch", value: "twitch" },
  { label: "Product Hunt", value: "producthunt" }
];
async function sendItRequest(apiKey, method, path, body, params) {
  return (await import_pieces_common2.httpClient.sendRequest({
    method,
    url: `${API_BASE}${path}`,
    headers: {
      Authorization: `Bearer ${apiKey}`,
      "Content-Type": "application/json"
    },
    body,
    queryParams: params
  })).body;
}
var BASE_URL2 = API_BASE, platformProperty = import_pieces_framework2.Property.StaticMultiSelectDropdown({
  displayName: "Platforms",
  description: "Select one or more social media platforms to publish to. Make sure your accounts are connected in the SendIt dashboard under **Settings > Connected Accounts**.",
  required: !0,
  options: {
    options
  }
}), textProperty = import_pieces_framework2.Property.LongText({
  displayName: "Post Text",
  description: "The text content of your post. Character limits vary by platform (e.g. X/Twitter: 280, LinkedIn: 3000, Instagram: 2200).",
  required: !0
}), mediaUrlProperty = import_pieces_framework2.Property.ShortText({
  displayName: "Media URL",
  description: "A publicly accessible URL to an image (JPG, PNG, GIF) or video (MP4). **Required for Instagram and TikTok.** Images must be under 10 MB and videos under 100 MB.",
  required: !1
}), mediaUrlsProperty = import_pieces_framework2.Property.Array({
  displayName: "Media URLs (Carousel)",
  description: "Add multiple publicly accessible image URLs to create a carousel post. Supported on Instagram and Threads. Add one URL per item.",
  required: !1
}), mediaTypeProperty = import_pieces_framework2.Property.StaticDropdown({
  displayName: "Media Type",
  description: "Leave as **Auto-detect** unless SendIt misidentifies your media. Choose **Image** or **Video** to force a specific type.",
  required: !1,
  defaultValue: "auto",
  options: {
    options: [
      { label: "Auto-detect (recommended)", value: "auto" },
      { label: "Image", value: "image" },
      { label: "Video", value: "video" }
    ]
  }
}), scheduleIdProperty = import_pieces_framework2.Property.Dropdown({
  displayName: "Scheduled Post",
  description: "Select the scheduled post you want to act on. Only posts with a **Pending** status are listed.",
  refreshers: [],
  required: !0,
  auth: sendItAuth,
  options: async ({ auth }) => {
    var _a, _b;
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your SendIt account first"
      };
    try {
      let response = await sendItRequest(
        auth.secret_text,
        import_pieces_common2.HttpMethod.GET,
        "/scheduled"
      ), posts = (_b = (_a = response.posts) != null ? _a : response.data) != null ? _b : response;
      return !Array.isArray(posts) || posts.length === 0 ? {
        disabled: !1,
        options: [],
        placeholder: "No scheduled posts found. Create one first."
      } : {
        disabled: !1,
        options: posts.map((post) => {
          var _a2, _b2, _c, _d;
          let platforms = Array.isArray(post.platforms) ? post.platforms.join(", ") : String((_a2 = post.platform) != null ? _a2 : "Unknown"), content = post.content, rawText = String((_b2 = content == null ? void 0 : content.text) != null ? _b2 : ""), snippet = rawText.length > 40 ? rawText.substring(0, 40) + "\u2026" : rawText || "No text", scheduledTime = post.scheduledTime ? new Date(String(post.scheduledTime)).toLocaleString() : "", id = String((_d = (_c = post.id) != null ? _c : post.scheduleId) != null ? _d : "");
          return {
            label: `${platforms} \u2014 ${snippet}${scheduledTime ? ` (${scheduledTime})` : ""}`,
            value: id
          };
        }).filter((opt) => opt.value !== "")
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load scheduled posts. Check your API key."
      };
    }
  }
}), scheduledTimeProperty = import_pieces_framework2.Property.DateTime({
  displayName: "Scheduled Time",
  description: "When to publish the post. Must be in the future. Times are interpreted in UTC \u2014 convert your local time if needed.",
  required: !0
}), platformFilterProperty = import_pieces_framework2.Property.StaticDropdown({
  displayName: "Platform Filter",
  description: "Only return scheduled posts for a specific platform. Leave empty to see all platforms.",
  required: !1,
  options: {
    options
  }
});

// ../ap/packages/pieces/community/send-it/src/lib/actions/publish-post.ts
var publishPost = (0, import_pieces_framework3.createAction)({
  auth: sendItAuth,
  name: "publish_post",
  classification: "WRITE",
  displayName: "Publish Post",
  description: "Publish content to social media platforms immediately",
  audience: "both",
  aiMetadata: {
    description: "Immediately publishes a post (text plus optional image/video URL or a multi-image carousel) to one or more connected social platforms (X, LinkedIn, Instagram, Threads, TikTok, and others). Choose this to post right now rather than at a future time (use Schedule Post for that). Requires at least one selected platform whose account is connected in SendIt; note Instagram and TikTok require a media URL. Not idempotent: each call publishes a new post.",
    idempotent: !1
  },
  props: {
    platforms: platformProperty,
    text: textProperty,
    mediaUrl: mediaUrlProperty,
    mediaUrls: mediaUrlsProperty,
    mediaType: mediaTypeProperty
  },
  async run(context) {
    let { platforms, text, mediaUrl, mediaUrls, mediaType } = context.propsValue;
    return await sendItRequest(
      context.auth.secret_text,
      import_pieces_common3.HttpMethod.POST,
      "/publish",
      {
        platforms,
        content: {
          text,
          mediaUrl,
          mediaUrls,
          mediaType
        }
      }
    );
  }
});

// ../ap/packages/pieces/community/send-it/src/lib/actions/schedule-post.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var schedulePost = (0, import_pieces_framework4.createAction)({
  auth: sendItAuth,
  name: "schedule_post",
  classification: "WRITE",
  displayName: "Schedule Post",
  description: "Schedule content to be published at a future time",
  audience: "both",
  aiMetadata: {
    description: "Queues a post (text plus optional media URL) to be published at a specified future time on one or more connected social platforms. Choose this when publishing should be deferred; use Publish Post to send immediately. The scheduled time must be in the future and is interpreted in UTC. Not idempotent: each call creates a new scheduled post.",
    idempotent: !1
  },
  props: {
    platforms: platformProperty,
    text: textProperty,
    mediaUrl: mediaUrlProperty,
    scheduledTime: scheduledTimeProperty
  },
  async run(context) {
    let { platforms, text, mediaUrl, scheduledTime } = context.propsValue;
    return await sendItRequest(
      context.auth.secret_text,
      import_pieces_common4.HttpMethod.POST,
      "/schedule",
      {
        platforms,
        content: {
          text,
          mediaUrl
        },
        scheduledTime
      }
    );
  }
});

// ../ap/packages/pieces/community/send-it/src/lib/actions/cancel-scheduled-post.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var cancelScheduledPost = (0, import_pieces_framework5.createAction)({
  auth: sendItAuth,
  name: "cancel_scheduled_post",
  classification: "DESTRUCTIVE",
  displayName: "Cancel Scheduled Post",
  description: "Cancel a scheduled post before it is published",
  audience: "both",
  aiMetadata: {
    description: "Cancels a pending scheduled post by its schedule ID so it will not be published. Use this to call off a future post created by Schedule Post; only posts in Pending status can be cancelled. Idempotent: cancelling an already-cancelled or non-existent schedule leaves it in the same cancelled end state.",
    idempotent: !0
  },
  props: {
    scheduleId: scheduleIdProperty
  },
  async run(context) {
    let { scheduleId } = context.propsValue;
    return await sendItRequest(
      context.auth.secret_text,
      import_pieces_common5.HttpMethod.DELETE,
      `/scheduled/${scheduleId}`
    );
  }
});

// ../ap/packages/pieces/community/send-it/src/lib/actions/trigger-scheduled-post.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var triggerScheduledPost = (0, import_pieces_framework6.createAction)({
  auth: sendItAuth,
  name: "trigger_scheduled_post",
  classification: "WRITE",
  displayName: "Trigger Scheduled Post Now",
  description: "Immediately publish a scheduled post",
  audience: "both",
  aiMetadata: {
    description: "Publishes a pending scheduled post right away instead of waiting for its scheduled time, identified by its schedule ID. Use this to push out a queued post early. Not idempotent: it performs an actual publish, so repeating the call can re-publish the content.",
    idempotent: !1
  },
  props: {
    scheduleId: scheduleIdProperty
  },
  async run(context) {
    let { scheduleId } = context.propsValue;
    return await sendItRequest(
      context.auth.secret_text,
      import_pieces_common6.HttpMethod.POST,
      `/scheduled/${scheduleId}/trigger`
    );
  }
});

// ../ap/packages/pieces/community/send-it/src/lib/actions/validate-content.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var validateContent = (0, import_pieces_framework7.createAction)({
  auth: sendItAuth,
  name: "validate_content",
  classification: "READ",
  displayName: "Validate Content",
  description: "Check if content meets platform requirements before publishing",
  audience: "both",
  aiMetadata: {
    description: "Validates that a post (text plus optional media) meets the requirements of the selected social platforms, such as character limits and media rules, without publishing anything. Use this as a pre-flight check before Publish Post or Schedule Post. Idempotent: it is a read-only check with no side effects.",
    idempotent: !0
  },
  props: {
    platforms: platformProperty,
    text: textProperty,
    mediaUrl: mediaUrlProperty,
    mediaUrls: mediaUrlsProperty,
    mediaType: mediaTypeProperty
  },
  async run(context) {
    let { platforms, text, mediaUrl, mediaUrls, mediaType } = context.propsValue;
    return await sendItRequest(
      context.auth.secret_text,
      import_pieces_common7.HttpMethod.POST,
      "/validate",
      {
        platforms,
        content: {
          text,
          mediaUrl,
          mediaUrls,
          mediaType
        }
      }
    );
  }
});

// ../ap/packages/pieces/community/send-it/src/lib/actions/list-accounts.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var listAccounts = (0, import_pieces_framework8.createAction)({
  auth: sendItAuth,
  name: "list_accounts",
  classification: "SEARCH",
  displayName: "List Connected Accounts",
  description: "Get a list of connected social media accounts",
  audience: "both",
  aiMetadata: {
    description: "Retrieves the social media accounts currently connected to the SendIt account. Use this to discover which platforms are available before publishing or scheduling, or to verify a connection exists. Takes no input. Idempotent: it is a read-only lookup.",
    idempotent: !0
  },
  props: {},
  async run(context) {
    return await sendItRequest(
      context.auth.secret_text,
      import_pieces_common8.HttpMethod.GET,
      "/accounts"
    );
  }
});

// ../ap/packages/pieces/community/send-it/src/lib/actions/list-scheduled-posts.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var listScheduledPosts = (0, import_pieces_framework9.createAction)({
  auth: sendItAuth,
  name: "list_scheduled_posts",
  classification: "SEARCH",
  displayName: "List Scheduled Posts",
  description: "Get a list of pending scheduled posts",
  audience: "both",
  aiMetadata: {
    description: "Lists pending scheduled posts, optionally filtered to a single platform; leaving the platform filter empty returns scheduled posts across all platforms. Use this to find the schedule ID of a post you want to cancel or trigger early, or to review the publishing queue. Idempotent: it is a read-only list.",
    idempotent: !0
  },
  props: {
    platformFilter: platformFilterProperty
  },
  async run(context) {
    let { platformFilter } = context.propsValue, params = platformFilter ? { platform: platformFilter } : void 0;
    return await sendItRequest(
      context.auth.secret_text,
      import_pieces_common9.HttpMethod.GET,
      "/scheduled",
      void 0,
      params
    );
  }
});

// ../ap/packages/pieces/community/send-it/src/lib/triggers/post-published.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var postPublished = (0, import_pieces_framework10.createTrigger)({
  auth: sendItAuth,
  name: "post_published",
  classification: "READ",
  displayName: "Post Published",
  description: "Triggers when a post is successfully published to a platform",
  aiMetadata: {
    description: "Fires when SendIt successfully publishes a post to a connected social platform, representing a completed publication. Each event carries the platform, post ID, post URL, and content of the published post."
  },
  props: {},
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let response = await sendItRequest(
      context.auth.secret_text,
      import_pieces_common10.HttpMethod.POST,
      "/webhooks",
      {
        url: context.webhookUrl,
        events: ["post.published"]
      }
    );
    await context.store.put("webhookId", response.webhook.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get("webhookId");
    webhookId && await sendItRequest(
      context.auth.secret_text,
      import_pieces_common10.HttpMethod.DELETE,
      `/webhooks/${webhookId}`
    );
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n;
    let payload = context.payload.body;
    return [
      {
        delivery_id: (_a = payload.deliveryId) != null ? _a : null,
        event: (_b = payload.event) != null ? _b : null,
        timestamp: (_c = payload.timestamp) != null ? _c : null,
        platform: (_e = (_d = payload.data) == null ? void 0 : _d.platform) != null ? _e : null,
        post_id: (_g = (_f = payload.data) == null ? void 0 : _f.postId) != null ? _g : null,
        post_url: (_i = (_h = payload.data) == null ? void 0 : _h.postUrl) != null ? _i : null,
        content_text: (_l = (_k = (_j = payload.data) == null ? void 0 : _j.content) == null ? void 0 : _k.text) != null ? _l : null,
        published_at: (_n = (_m = payload.data) == null ? void 0 : _m.publishedAt) != null ? _n : null
      }
    ];
  },
  sampleData: {
    delivery_id: "dlv_sample123",
    event: "post.published",
    timestamp: "2025-01-14T12:00:00.000Z",
    platform: "linkedin",
    post_id: "urn:li:share:1234567890",
    post_url: "https://www.linkedin.com/feed/update/urn:li:share:1234567890",
    content_text: "Sample post content",
    published_at: "2025-01-14T12:00:00.000Z"
  }
});

// ../ap/packages/pieces/community/send-it/src/lib/triggers/post-scheduled.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var postScheduled = (0, import_pieces_framework11.createTrigger)({
  auth: sendItAuth,
  name: "post_scheduled",
  classification: "READ",
  displayName: "Post Scheduled",
  description: "Triggers when a post is scheduled for future publishing",
  aiMetadata: {
    description: "Fires when a post is queued in SendIt for future publishing, representing a newly created schedule. Each event carries the schedule ID, target platforms, content, and the scheduled publish time."
  },
  props: {},
  type: import_pieces_framework11.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let response = await sendItRequest(
      context.auth.secret_text,
      import_pieces_common11.HttpMethod.POST,
      "/webhooks",
      {
        url: context.webhookUrl,
        events: ["post.scheduled"]
      }
    );
    await context.store.put("webhookId", response.webhook.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get("webhookId");
    webhookId && await sendItRequest(
      context.auth.secret_text,
      import_pieces_common11.HttpMethod.DELETE,
      `/webhooks/${webhookId}`
    );
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k;
    let payload = context.payload.body;
    return [
      {
        delivery_id: (_a = payload.deliveryId) != null ? _a : null,
        event: (_b = payload.event) != null ? _b : null,
        timestamp: (_c = payload.timestamp) != null ? _c : null,
        schedule_id: (_e = (_d = payload.data) == null ? void 0 : _d.scheduleId) != null ? _e : null,
        platforms: Array.isArray((_f = payload.data) == null ? void 0 : _f.platforms) ? payload.data.platforms.join(", ") : null,
        content_text: (_i = (_h = (_g = payload.data) == null ? void 0 : _g.content) == null ? void 0 : _h.text) != null ? _i : null,
        scheduled_time: (_k = (_j = payload.data) == null ? void 0 : _j.scheduledTime) != null ? _k : null
      }
    ];
  },
  sampleData: {
    delivery_id: "dlv_sample456",
    event: "post.scheduled",
    timestamp: "2025-01-14T12:00:00.000Z",
    schedule_id: "sch_abc123",
    platforms: "linkedin, instagram",
    content_text: "Scheduled post content",
    scheduled_time: "2025-01-15T12:00:00.000Z"
  }
});

// ../ap/packages/pieces/community/send-it/src/lib/triggers/post-failed.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var postFailed = (0, import_pieces_framework12.createTrigger)({
  auth: sendItAuth,
  name: "post_failed",
  classification: "READ",
  displayName: "Post Failed",
  description: "Triggers when a post fails to publish to a platform",
  aiMetadata: {
    description: "Fires when SendIt fails to publish a post to a connected social platform, representing a failed publication attempt. Each event carries the platform, the post content, and an error message describing why it failed."
  },
  props: {},
  type: import_pieces_framework12.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let response = await sendItRequest(
      context.auth.secret_text,
      import_pieces_common12.HttpMethod.POST,
      "/webhooks",
      {
        url: context.webhookUrl,
        events: ["post.failed"]
      }
    );
    await context.store.put("webhookId", response.webhook.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get("webhookId");
    webhookId && await sendItRequest(
      context.auth.secret_text,
      import_pieces_common12.HttpMethod.DELETE,
      `/webhooks/${webhookId}`
    );
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l;
    let payload = context.payload.body;
    return [
      {
        delivery_id: (_a = payload.deliveryId) != null ? _a : null,
        event: (_b = payload.event) != null ? _b : null,
        timestamp: (_c = payload.timestamp) != null ? _c : null,
        platform: (_e = (_d = payload.data) == null ? void 0 : _d.platform) != null ? _e : null,
        content_text: (_h = (_g = (_f = payload.data) == null ? void 0 : _f.content) == null ? void 0 : _g.text) != null ? _h : null,
        error: (_j = (_i = payload.data) == null ? void 0 : _i.error) != null ? _j : null,
        failed_at: (_l = (_k = payload.data) == null ? void 0 : _k.failedAt) != null ? _l : null
      }
    ];
  },
  sampleData: {
    delivery_id: "dlv_sample789",
    event: "post.failed",
    timestamp: "2025-01-14T12:00:00.000Z",
    platform: "instagram",
    content_text: "Failed post content",
    error: "Instagram API rate limit exceeded",
    failed_at: "2025-01-14T12:00:00.000Z"
  }
});

// ../ap/packages/pieces/community/send-it/src/index.ts
var sendIt = (0, import_pieces_framework13.createPiece)({
  displayName: "SendIt",
  description: "Multi-platform social media publishing. Schedule and publish content to LinkedIn, Instagram, Threads, TikTok, X, and 30+ more platforms.",
  auth: sendItAuth,
  minimumSupportedRelease: "0.20.0",
  logoUrl: "https://cdn.activepieces.com/pieces/send-it.png",
  categories: [import_pieces_framework14.PieceCategory.MARKETING],
  authors: ["infiniteappsai", "sanket-a11y"],
  actions: [
    publishPost,
    schedulePost,
    cancelScheduledPost,
    triggerScheduledPost,
    validateContent,
    listAccounts,
    listScheduledPosts,
    (0, import_pieces_common13.createCustomApiCallAction)({
      baseUrl: () => BASE_URL2,
      auth: sendItAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [postPublished, postScheduled, postFailed]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  sendIt
});
