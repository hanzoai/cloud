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

// ../ap/packages/pieces/community/postiz/src/index.ts
var index_exports = {};
__export(index_exports, {
  postiz: () => postiz,
  postizAuth: () => postizAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common"), import_pieces_framework13 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/postiz/src/lib/actions/create-post.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/postiz/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), postizAuth = import_pieces_framework.PieceAuth.CustomAuth({
  displayName: "Postiz Connection",
  required: !0,
  props: {
    base_url: import_pieces_framework.Property.ShortText({
      displayName: "Base URL",
      description: "The API base URL. Use `https://api.postiz.com/public/v1` for Postiz Cloud, or `https://your-domain.com/api/public/v1` for self-hosted instances.",
      required: !0,
      defaultValue: "https://api.postiz.com/public/v1"
    }),
    api_key: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Key",
      description: `To get your API key:
1. Log in to your Postiz dashboard
2. Go to **Settings > Developers > Public API**
3. Generate a new API key and copy it`,
      required: !0
    })
  },
  validate: async ({ auth }) => {
    var _a;
    try {
      let baseUrl = (_a = auth.base_url) == null ? void 0 : _a.trim().replace(/\/+$/, "");
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${baseUrl}/is-connected`,
        headers: {
          Authorization: auth.api_key
        }
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API key or base URL. Please check your credentials."
      };
    }
  }
});

// ../ap/packages/pieces/community/postiz/src/lib/common/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var DEFAULT_BASE_URL = "https://api.postiz.com/public/v1";
function buildBaseUrl(auth) {
  var _a;
  let url = (_a = auth.props.base_url) == null ? void 0 : _a.trim();
  return url ? url.replace(/\/+$/, "") : DEFAULT_BASE_URL;
}
async function postizApiCall({
  auth,
  method,
  path,
  body,
  queryParams
}) {
  return await import_pieces_common2.httpClient.sendRequest({
    method,
    url: `${buildBaseUrl(auth)}${path}`,
    headers: {
      Authorization: auth.props.api_key
    },
    queryParams,
    body
  });
}
var postizCommon = {
  integrationDropdown: import_pieces_framework2.Property.Dropdown({
    displayName: "Channel",
    description: "Select the connected social media channel",
    refreshers: ["auth"],
    required: !0,
    auth: postizAuth,
    options: async ({ auth }) => auth ? {
      disabled: !1,
      options: (await postizApiCall({
        auth,
        method: import_pieces_common2.HttpMethod.GET,
        path: "/integrations"
      })).body.map((integration) => ({
        label: `${integration.name} (${integration.identifier})`,
        value: integration.id
      }))
    } : {
      disabled: !0,
      options: [],
      placeholder: "Please connect your Postiz account first"
    }
  })
};

// ../ap/packages/pieces/community/postiz/src/lib/actions/create-post.ts
var createPost = (0, import_pieces_framework3.createAction)({
  auth: postizAuth,
  name: "create_post",
  displayName: "Create Post",
  description: "Create or schedule a post on one or more connected channels",
  audience: "both",
  aiMetadata: {
    description: "Publishes content to a connected social channel in one of three modes selectable via the post type: publish immediately, schedule for a future date, or save as a draft. Use to push a message out through Postiz; a publish date is required when scheduling, and media must be a path returned by the Upload File from URL action. Not idempotent \u2014 each call creates a new post.",
    idempotent: !1
  },
  props: {
    type: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Post Type",
      description: "Choose whether to publish immediately, schedule for later, or save as draft",
      required: !0,
      defaultValue: "schedule",
      options: {
        options: [
          { label: "Schedule for later", value: "schedule" },
          { label: "Publish now", value: "now" },
          { label: "Save as draft", value: "draft" }
        ]
      }
    }),
    date: import_pieces_framework3.Property.DateTime({
      displayName: "Publish Date",
      description: "The date and time to publish the post (ISO 8601 format). Required for scheduled posts.",
      required: !1
    }),
    integration: postizCommon.integrationDropdown,
    content: import_pieces_framework3.Property.LongText({
      displayName: "Content",
      description: "The text content of the post",
      required: !0
    }),
    media: import_pieces_framework3.Property.Array({
      displayName: "Media Paths",
      description: 'File paths of images or videos to attach. Upload media first using the "Upload File from URL" action, then paste the returned path here.',
      required: !1
    }),
    shortLink: import_pieces_framework3.Property.Checkbox({
      displayName: "Shorten Links",
      description: "Automatically shorten URLs in the post content",
      required: !1,
      defaultValue: !1
    }),
    settings: import_pieces_framework3.Property.Json({
      displayName: "Settings",
      description: "Optional platform-specific settings (e.g. approval workflow overrides)",
      required: !1
    })
  },
  async run(context) {
    let { type, date, integration, content, media, shortLink, settings } = context.propsValue, auth = context.auth, value = { content };
    media && media.length > 0 && (value.image = media.map((path) => ({ path })));
    let postItem = {
      integration: { id: integration },
      value: [value]
    };
    settings && (postItem.settings = settings);
    let body = {
      type,
      shortLink: shortLink != null ? shortLink : !1,
      posts: [postItem]
    };
    if (type === "schedule") {
      if (!date)
        throw new Error(
          'A Publish Date is required when Post Type is "Schedule for later".'
        );
      body.date = date;
    }
    let results = (await postizApiCall({
      auth,
      method: import_pieces_common3.HttpMethod.POST,
      path: "/posts",
      body
    })).body;
    return Array.isArray(results) && results.length === 1 ? results[0] : results;
  }
});

// ../ap/packages/pieces/community/postiz/src/lib/actions/list-posts.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var listPosts = (0, import_pieces_framework4.createAction)({
  auth: postizAuth,
  name: "list_posts",
  displayName: "List Posts",
  description: "Retrieve posts within a date range",
  audience: "both",
  aiMetadata: {
    description: "Retrieves posts scheduled or published within a given date range across all connected channels. Use to inspect what is on the calendar or to obtain post IDs for analytics or deletion. Both start and end dates are required. Idempotent \u2014 a read-only lookup.",
    idempotent: !0
  },
  props: {
    startDate: import_pieces_framework4.Property.DateTime({
      displayName: "Start Date",
      description: "Start of the date range (ISO 8601 format)",
      required: !0
    }),
    endDate: import_pieces_framework4.Property.DateTime({
      displayName: "End Date",
      description: "End of the date range (ISO 8601 format)",
      required: !0
    })
  },
  async run(context) {
    let auth = context.auth;
    return (await postizApiCall({
      auth,
      method: import_pieces_common4.HttpMethod.GET,
      path: "/posts",
      queryParams: {
        startDate: context.propsValue.startDate,
        endDate: context.propsValue.endDate
      }
    })).body.posts.map((post) => {
      var _a, _b, _c, _d, _e, _f, _g;
      return {
        id: post.id,
        content: post.content,
        publish_date: post.publishDate,
        release_url: (_a = post.releaseURL) != null ? _a : null,
        state: post.state,
        integration_id: (_c = (_b = post.integration) == null ? void 0 : _b.id) != null ? _c : null,
        integration_provider: (_e = (_d = post.integration) == null ? void 0 : _d.providerIdentifier) != null ? _e : null,
        integration_name: (_g = (_f = post.integration) == null ? void 0 : _f.name) != null ? _g : null
      };
    });
  }
});

// ../ap/packages/pieces/community/postiz/src/lib/actions/delete-post.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var deletePost = (0, import_pieces_framework5.createAction)({
  auth: postizAuth,
  name: "delete_post",
  displayName: "Delete Post",
  description: "Delete a post and all other posts in the same group",
  audience: "both",
  aiMetadata: {
    description: "Deletes a post by its ID, along with every other post in the same group (a post fanned out to multiple channels). Use to cancel or remove a scheduled or published post. Requires a post ID, obtainable from List Posts or the New Published Post trigger. Effectively idempotent \u2014 re-deleting an already-removed post has no further effect.",
    idempotent: !0
  },
  props: {
    postId: import_pieces_framework5.Property.ShortText({
      displayName: "Post ID",
      description: 'The ID of the post to delete. You can get this from the "List Posts" or "New Published Post" trigger output.',
      required: !0
    })
  },
  async run(context) {
    let auth = context.auth;
    return (await postizApiCall({
      auth,
      method: import_pieces_common5.HttpMethod.DELETE,
      path: `/posts/${context.propsValue.postId}`
    })).body;
  }
});

// ../ap/packages/pieces/community/postiz/src/lib/actions/list-integrations.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var listIntegrations = (0, import_pieces_framework6.createAction)({
  auth: postizAuth,
  name: "list_integrations",
  displayName: "List Channels",
  description: "List all connected social media channels in your organization",
  audience: "both",
  aiMetadata: {
    description: "Lists every social media channel connected to the organization, returning each channel id, name, and provider. Use to discover which channels are available and to resolve the integration id required by Create Post, Find Available Slot, and Get Platform Analytics. Idempotent \u2014 a read-only lookup.",
    idempotent: !0
  },
  props: {},
  async run(context) {
    let auth = context.auth;
    return (await postizApiCall({
      auth,
      method: import_pieces_common6.HttpMethod.GET,
      path: "/integrations"
    })).body.map((integration) => {
      var _a, _b, _c, _d, _e, _f;
      return {
        id: integration.id,
        name: integration.name,
        provider: integration.identifier,
        picture: (_a = integration.picture) != null ? _a : null,
        disabled: integration.disabled,
        profile: (_b = integration.profile) != null ? _b : null,
        customer_id: (_d = (_c = integration.customer) == null ? void 0 : _c.id) != null ? _d : null,
        customer_name: (_f = (_e = integration.customer) == null ? void 0 : _e.name) != null ? _f : null
      };
    });
  }
});

// ../ap/packages/pieces/community/postiz/src/lib/actions/find-available-slot.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var findAvailableSlot = (0, import_pieces_framework7.createAction)({
  auth: postizAuth,
  name: "find_available_slot",
  displayName: "Find Available Slot",
  description: "Find the next available posting time slot for a connected channel",
  audience: "both",
  aiMetadata: {
    description: "Returns the next free posting time slot for a connected channel based on its posting schedule. Use before scheduling to pick a non-conflicting publish date for Create Post. Requires the channel integration id (resolve via List Channels). Idempotent \u2014 a read-only lookup.",
    idempotent: !0
  },
  props: {
    integration: postizCommon.integrationDropdown
  },
  async run(context) {
    let auth = context.auth;
    return {
      next_available_date: (await postizApiCall({
        auth,
        method: import_pieces_common7.HttpMethod.GET,
        path: `/find-slot/${context.propsValue.integration}`
      })).body.date
    };
  }
});

// ../ap/packages/pieces/community/postiz/src/lib/actions/get-platform-analytics.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var getPlatformAnalytics = (0, import_pieces_framework8.createAction)({
  auth: postizAuth,
  name: "get_platform_analytics",
  displayName: "Get Platform Analytics",
  description: "Retrieve analytics (followers, impressions, engagement) for a connected channel",
  audience: "both",
  aiMetadata: {
    description: "Retrieves channel-level analytics (followers, impressions, engagement and their change) for a connected channel over a configurable lookback window in days. Use to report on overall account performance rather than a single post. Requires the channel integration id (resolve via List Channels). Idempotent \u2014 a read-only lookup.",
    idempotent: !0
  },
  props: {
    integration: postizCommon.integrationDropdown,
    days: import_pieces_framework8.Property.Number({
      displayName: "Lookback Days",
      description: "Number of days to look back for analytics (e.g. 7, 30, 90)",
      required: !0,
      defaultValue: 30
    })
  },
  async run(context) {
    let auth = context.auth;
    return (await postizApiCall({
      auth,
      method: import_pieces_common8.HttpMethod.GET,
      path: `/analytics/${context.propsValue.integration}`,
      queryParams: {
        date: String(context.propsValue.days)
      }
    })).body.map((metric) => {
      var _a, _b;
      return {
        metric: metric.label,
        total: metric.total,
        percentage_change: metric.percentageChange,
        daily_data: (_b = (_a = metric.data) == null ? void 0 : _a.map((d) => `${d.date}:${d.total}`).join(", ")) != null ? _b : null
      };
    });
  }
});

// ../ap/packages/pieces/community/postiz/src/lib/actions/get-post-analytics.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var getPostAnalytics = (0, import_pieces_framework9.createAction)({
  auth: postizAuth,
  name: "get_post_analytics",
  displayName: "Get Post Analytics",
  description: "Retrieve analytics (likes, comments, shares, impressions) for a specific published post",
  audience: "both",
  aiMetadata: {
    description: "Retrieves engagement analytics (likes, comments, shares, impressions and their change) for a single published post over a configurable lookback window in days. Use to measure how one specific post performed. Requires the post ID (from List Posts or the New Published Post trigger). Idempotent \u2014 a read-only lookup.",
    idempotent: !0
  },
  props: {
    postId: import_pieces_framework9.Property.ShortText({
      displayName: "Post ID",
      description: 'The ID of the published post. You can get this from the "List Posts" or "New Published Post" trigger output.',
      required: !0
    }),
    days: import_pieces_framework9.Property.Number({
      displayName: "Lookback Days",
      description: "Number of days to look back for analytics (e.g. 7, 30, 90)",
      required: !0,
      defaultValue: 30
    })
  },
  async run(context) {
    let auth = context.auth;
    return (await postizApiCall({
      auth,
      method: import_pieces_common9.HttpMethod.GET,
      path: `/analytics/post/${context.propsValue.postId}`,
      queryParams: {
        date: String(context.propsValue.days)
      }
    })).body.map((metric) => {
      var _a, _b;
      return {
        metric: metric.label,
        total: metric.total,
        percentage_change: metric.percentageChange,
        daily_data: (_b = (_a = metric.data) == null ? void 0 : _a.map((d) => `${d.date}:${d.total}`).join(", ")) != null ? _b : null
      };
    });
  }
});

// ../ap/packages/pieces/community/postiz/src/lib/actions/upload-file-from-url.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var uploadFileFromUrl = (0, import_pieces_framework10.createAction)({
  auth: postizAuth,
  name: "upload_file_from_url",
  displayName: "Upload File from URL",
  description: "Upload a media file from a URL to Postiz. Returns the file path to use when creating posts.",
  audience: "both",
  aiMetadata: {
    description: "Downloads an image or video from a public URL and uploads it into Postiz media storage, returning a file path to attach to Create Post. Use this first whenever a post needs media. Requires a publicly reachable file URL. Not idempotent \u2014 each call uploads a new media file.",
    idempotent: !1
  },
  props: {
    url: import_pieces_framework10.Property.ShortText({
      displayName: "File URL",
      description: "The public URL of the image or video to upload",
      required: !0
    })
  },
  async run(context) {
    let auth = context.auth, file = (await postizApiCall({
      auth,
      method: import_pieces_common10.HttpMethod.POST,
      path: "/upload-from-url",
      body: {
        url: context.propsValue.url
      }
    })).body;
    return {
      id: file.id,
      name: file.name,
      path: file.path,
      organization_id: file.organizationId,
      created_at: file.createdAt,
      updated_at: file.updatedAt
    };
  }
});

// ../ap/packages/pieces/community/postiz/src/lib/triggers/new-post.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var polling = {
  strategy: import_pieces_common11.DedupeStrategy.TIMEBASED,
  items: async ({ auth }) => {
    let now = /* @__PURE__ */ new Date(), thirtyDaysAgo = new Date(now.getTime() - 720 * 60 * 60 * 1e3);
    return (await postizApiCall({
      auth,
      method: import_pieces_common11.HttpMethod.GET,
      path: "/posts",
      queryParams: {
        startDate: thirtyDaysAgo.toISOString(),
        endDate: now.toISOString()
      }
    })).body.posts.filter((post) => post.state === "PUBLISHED").map((post) => {
      var _a, _b, _c, _d, _e, _f, _g;
      return {
        epochMilliSeconds: new Date(post.publishDate).getTime(),
        data: {
          id: post.id,
          content: post.content,
          publish_date: post.publishDate,
          release_url: (_a = post.releaseURL) != null ? _a : null,
          state: post.state,
          integration_id: (_c = (_b = post.integration) == null ? void 0 : _b.id) != null ? _c : null,
          integration_provider: (_e = (_d = post.integration) == null ? void 0 : _d.providerIdentifier) != null ? _e : null,
          integration_name: (_g = (_f = post.integration) == null ? void 0 : _f.name) != null ? _g : null
        }
      };
    });
  }
}, newPost = (0, import_pieces_framework11.createTrigger)({
  auth: postizAuth,
  name: "new_post",
  displayName: "New Published Post",
  description: "Triggers when a post is published in Postiz",
  aiMetadata: {
    description: "Fires when a post transitions to the published state in Postiz, emitting the published post with its id, content, publish date, release URL, and the channel it went out on. Use to react to content going live (e.g. to log, notify, or fetch post analytics). Polls recent posts and emits each newly published one."
  },
  props: {},
  sampleData: {
    id: "abc123",
    content: "Hello world! Check out our latest update.",
    publish_date: "2024-12-15T10:00:00.000Z",
    release_url: "https://x.com/user/status/123456",
    state: "PUBLISHED",
    integration_id: "int_123",
    integration_provider: "x",
    integration_name: "My X Account"
  },
  type: import_pieces_framework11.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common11.pollingHelper.test(polling, context);
  },
  async onEnable(context) {
    await import_pieces_common11.pollingHelper.onEnable(polling, context);
  },
  async onDisable(context) {
    await import_pieces_common11.pollingHelper.onDisable(polling, context);
  },
  async run(context) {
    return await import_pieces_common11.pollingHelper.poll(polling, context);
  }
});

// ../ap/packages/pieces/community/postiz/src/index.ts
var postiz = (0, import_pieces_framework12.createPiece)({
  displayName: "Postiz",
  description: "Open-source social media scheduling tool supporting 30+ platforms",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/postiz.png",
  categories: [import_pieces_framework13.PieceCategory.MARKETING],
  auth: postizAuth,
  authors: ["bst1n", "onyedikachi-david"],
  actions: [
    createPost,
    listPosts,
    deletePost,
    listIntegrations,
    findAvailableSlot,
    getPlatformAnalytics,
    getPostAnalytics,
    uploadFileFromUrl,
    (0, import_pieces_common12.createCustomApiCallAction)({
      baseUrl: (auth) => {
        let { base_url } = auth.props;
        return base_url == null ? void 0 : base_url.trim().replace(/\/+$/, "");
      },
      auth: postizAuth,
      authMapping: async (auth) => ({
        Authorization: auth.props.api_key
      })
    })
  ],
  triggers: [newPost]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  postiz,
  postizAuth
});
