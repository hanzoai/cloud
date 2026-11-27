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

// ../ap/packages/pieces/community/produktly/src/index.ts
var index_exports = {};
__export(index_exports, {
  produktly: () => produktly,
  produktlyAuth: () => produktlyAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common22 = require("@activepieces/pieces-common"), import_pieces_framework21 = require("@activepieces/pieces-framework"), import_shared = require("@activepieces/shared");

// ../ap/packages/pieces/community/produktly/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), produktlyAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `**How to get your Produktly API key:**

1. Sign in to your [Produktly dashboard](https://produktly.com).
2. Open **Settings \u2192 [API keys](https://produktly.com/app/settings/private-keys)**.
3. Click **Generate API Key**, give it a name (e.g. "Activepieces") and copy the key.
4. Paste the key below.

**Important:** Treat the key like a password \u2014 anyone with it can read and write your Produktly data. The same key works for both REST and MCP.`,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: "https://api.produktly.com/api/v1/changelogs",
        authentication: {
          type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
          token: auth
        }
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API key. Double-check the key in Produktly Settings \u2192 API keys."
      };
    }
  }
});

// ../ap/packages/pieces/community/produktly/src/lib/common/client.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
async function produktlyApiCall({
  auth,
  method,
  path,
  body,
  queryParams
}) {
  return await import_pieces_common2.httpClient.sendRequest({
    method,
    url: `${PRODUKTLY_BASE_URL}${path}`,
    authentication: {
      type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
      token: auth.secret_text
    },
    queryParams,
    body
  });
}
var PRODUKTLY_BASE_URL = "https://api.produktly.com/api/v1";

// ../ap/packages/pieces/community/produktly/src/lib/actions/list-changelogs.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var listChangelogs = (0, import_pieces_framework2.createAction)({
  auth: produktlyAuth,
  name: "list_changelogs",
  displayName: "List Changelogs",
  description: "List all changelogs in your Produktly account.",
  audience: "both",
  aiMetadata: { description: "List every changelog in the Produktly account with its name, active state, and creation date, paginated with limit and offset. Pick this to discover changelog IDs before reading or writing posts; use List Changelog Posts for the announcements inside one changelog. Read-only and idempotent.", idempotent: !0 },
  props: {
    limit: import_pieces_framework2.Property.Number({
      displayName: "Limit",
      description: "Maximum number of changelogs to return (1-100, default 50).",
      required: !1,
      defaultValue: 50
    }),
    offset: import_pieces_framework2.Property.Number({
      displayName: "Offset",
      description: "Number of changelogs to skip for pagination (default 0).",
      required: !1,
      defaultValue: 0
    })
  },
  async run({ auth, propsValue }) {
    var _a, _b;
    return (await produktlyApiCall({
      auth,
      method: import_pieces_common3.HttpMethod.GET,
      path: "/changelogs",
      queryParams: {
        limit: String((_a = propsValue.limit) != null ? _a : 50),
        offset: String((_b = propsValue.offset) != null ? _b : 0)
      }
    })).body.data.map((changelog) => ({
      changelog_id: changelog.id,
      changelog_name: changelog.name,
      changelog_active: changelog.active,
      changelog_created_at: changelog.createdAt
    }));
  }
});

// ../ap/packages/pieces/community/produktly/src/lib/actions/list-changelog-posts.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/produktly/src/lib/common/props.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var produktlyProps = {
  changelog: import_pieces_framework3.Property.Dropdown({
    auth: produktlyAuth,
    displayName: "Changelog",
    description: "The changelog the post belongs to.",
    required: !0,
    refreshers: [],
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
          options: (await produktlyApiCall({
            auth,
            method: import_pieces_common4.HttpMethod.GET,
            path: "/changelogs"
          })).body.data.map((changelog) => ({
            label: changelog.name + (changelog.active ? "" : " (inactive)"),
            value: String(changelog.id)
          }))
        };
      } catch (e) {
        return {
          disabled: !0,
          options: [],
          placeholder: "Failed to load changelogs. Check your connection."
        };
      }
    }
  }),
  feedbackWidget: import_pieces_framework3.Property.Dropdown({
    auth: produktlyAuth,
    displayName: "Feedback Widget",
    description: "The feedback widget to read responses from.",
    required: !0,
    refreshers: [],
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
          options: (await produktlyApiCall({
            auth,
            method: import_pieces_common4.HttpMethod.GET,
            path: "/feedback-widgets"
          })).body.data.map((widget) => ({
            label: widget.name + (widget.active ? "" : " (inactive)"),
            value: String(widget.id)
          }))
        };
      } catch (e) {
        return {
          disabled: !0,
          options: [],
          placeholder: "Failed to load feedback widgets. Check your connection."
        };
      }
    }
  }),
  npsWidget: import_pieces_framework3.Property.Dropdown({
    auth: produktlyAuth,
    displayName: "NPS Widget",
    description: "The NPS widget to read responses from.",
    required: !0,
    refreshers: [],
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
          options: (await produktlyApiCall({
            auth,
            method: import_pieces_common4.HttpMethod.GET,
            path: "/nps-widgets"
          })).body.data.map((widget) => ({
            label: widget.name + (widget.active ? "" : " (inactive)"),
            value: String(widget.id)
          }))
        };
      } catch (e) {
        return {
          disabled: !0,
          options: [],
          placeholder: "Failed to load NPS widgets. Check your connection."
        };
      }
    }
  }),
  roadmap: import_pieces_framework3.Property.Dropdown({
    auth: produktlyAuth,
    displayName: "Roadmap",
    description: "The public roadmap to inspect.",
    required: !0,
    refreshers: [],
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
          options: (await produktlyApiCall({
            auth,
            method: import_pieces_common4.HttpMethod.GET,
            path: "/roadmaps"
          })).body.data.map((roadmap) => ({
            label: roadmap.publicName || roadmap.name,
            value: String(roadmap.id)
          }))
        };
      } catch (e) {
        return {
          disabled: !0,
          options: [],
          placeholder: "Failed to load roadmaps. Check your connection."
        };
      }
    }
  }),
  tag: import_pieces_framework3.Property.Dropdown({
    auth: produktlyAuth,
    displayName: "Tag",
    description: "A Produktly tag used to categorise changelog posts.",
    required: !0,
    refreshers: [],
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
          options: (await produktlyApiCall({
            auth,
            method: import_pieces_common4.HttpMethod.GET,
            path: "/tags"
          })).body.data.map((tag) => ({
            label: tag.name,
            value: tag.name
          }))
        };
      } catch (e) {
        return {
          disabled: !0,
          options: [],
          placeholder: "Failed to load tags. Check your connection."
        };
      }
    }
  })
};

// ../ap/packages/pieces/community/produktly/src/lib/actions/list-changelog-posts.ts
var listChangelogPosts = (0, import_pieces_framework4.createAction)({
  auth: produktlyAuth,
  name: "list_changelog_posts",
  displayName: "List Changelog Posts",
  description: "List all posts within a specific changelog.",
  audience: "both",
  aiMetadata: { description: "List the posts inside one Produktly changelog, paginated with limit and offset and optionally narrowed to a release-date range. Pick this to read announcements in a known changelog or to resolve a post ID for Update Changelog Post; use List Changelogs first to find the changelog itself. Requires a changelog ID. Read-only and idempotent.", idempotent: !0 },
  props: {
    changelog: produktlyProps.changelog,
    limit: import_pieces_framework4.Property.Number({
      displayName: "Limit",
      description: "Maximum number of posts to return (1-100, default 50).",
      required: !1,
      defaultValue: 50
    }),
    offset: import_pieces_framework4.Property.Number({
      displayName: "Offset",
      description: "Number of posts to skip for pagination (default 0).",
      required: !1,
      defaultValue: 0
    }),
    start_date: import_pieces_framework4.Property.ShortText({
      displayName: "Start Date",
      description: "Optional ISO 8601 date (e.g. 2024-01-01) to filter posts from.",
      required: !1
    }),
    end_date: import_pieces_framework4.Property.ShortText({
      displayName: "End Date",
      description: "Optional ISO 8601 date (e.g. 2024-12-31) to filter posts up to.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    var _a, _b;
    let queryParams = {
      limit: String((_a = propsValue.limit) != null ? _a : 50),
      offset: String((_b = propsValue.offset) != null ? _b : 0)
    };
    return propsValue.start_date && (queryParams.startDate = propsValue.start_date), propsValue.end_date && (queryParams.endDate = propsValue.end_date), (await produktlyApiCall({
      auth,
      method: import_pieces_common5.HttpMethod.GET,
      path: `/changelogs/${propsValue.changelog}/posts`,
      queryParams
    })).body.data.map((post) => ({
      post_id: post.id,
      changelog_id: propsValue.changelog,
      post_title: post.title,
      post_description: post.description,
      post_date: post.date,
      post_active: post.active,
      post_tags: post.tags.map((t) => t.name).join(", ")
    }));
  }
});

// ../ap/packages/pieces/community/produktly/src/lib/actions/create-changelog-post.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var createChangelogPost = (0, import_pieces_framework5.createAction)({
  auth: produktlyAuth,
  name: "create_changelog_post",
  displayName: "Create Changelog Post",
  description: "Publish a new post inside a changelog. Visible to your users once active.",
  audience: "both",
  aiMetadata: { description: "Create a post inside a specific Produktly changelog, with an optional body, release date, active/hidden state, and a list of tag names that must already exist (resolve them with List Tags). Pick this to announce a release to end users; use Update Changelog Post to revise an announcement that already exists. Requires a changelog ID and a title. Not idempotent: each call creates a new post, even with identical content.", idempotent: !1 },
  props: {
    changelog: produktlyProps.changelog,
    title: import_pieces_framework5.Property.ShortText({
      displayName: "Title",
      description: 'The headline shown to users (e.g. "New: Dark mode is here").',
      required: !0
    }),
    description: import_pieces_framework5.Property.LongText({
      displayName: "Description",
      description: "The body of the post. Supports plain text or HTML.",
      required: !1
    }),
    date: import_pieces_framework5.Property.ShortText({
      displayName: "Date",
      description: "Optional release date in ISO 8601 format (e.g. 2024-12-15). Defaults to today.",
      required: !1
    }),
    active: import_pieces_framework5.Property.Checkbox({
      displayName: "Active",
      description: "If enabled, the post is visible to your users immediately.",
      required: !1,
      defaultValue: !0
    }),
    tag_names: import_pieces_framework5.Property.Array({
      displayName: "Tag Names",
      description: 'Optional list of tag names to attach. Must match existing tags (use "List Tags" to find them).',
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    var _a;
    let body = {
      title: propsValue.title,
      active: (_a = propsValue.active) != null ? _a : !0
    };
    propsValue.description && (body.description = propsValue.description), propsValue.date && (body.date = propsValue.date), Array.isArray(propsValue.tag_names) && propsValue.tag_names.length > 0 && (body.tagNames = propsValue.tag_names);
    let response = await produktlyApiCall({
      auth,
      method: import_pieces_common6.HttpMethod.POST,
      path: `/changelogs/${propsValue.changelog}/posts`,
      body
    });
    return {
      post_id: response.body.id,
      post_title: response.body.title,
      post_description: response.body.description,
      post_date: response.body.date,
      post_active: response.body.active,
      post_tags: response.body.tags.map((t) => t.name).join(", ")
    };
  }
});

// ../ap/packages/pieces/community/produktly/src/lib/actions/update-changelog-post.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var updateChangelogPost = (0, import_pieces_framework6.createAction)({
  auth: produktlyAuth,
  name: "update_changelog_post",
  displayName: "Update Changelog Post",
  description: "Edit an existing changelog post \u2014 change its title, body, tags or visibility.",
  audience: "both",
  aiMetadata: { description: "Partially update one existing post in a Produktly changelog: any of title, body, release date, active/hidden visibility, or the full tag list, leaving omitted fields untouched. Pick this to revise, publish, or unpublish an announcement that already exists; use Create Changelog Post for a new one. Requires both the changelog ID and the post ID (resolve it with List Changelog Posts), and any supplied tag names must already exist. Idempotent: repeating the same call converges on the same post state.", idempotent: !0 },
  props: {
    changelog: produktlyProps.changelog,
    post_id: import_pieces_framework6.Property.ShortText({
      displayName: "Post ID",
      description: 'The ID of the post to update. Use "List Changelog Posts" to find it.',
      required: !0
    }),
    title: import_pieces_framework6.Property.ShortText({
      displayName: "Title",
      description: "New title for the post. Leave empty to keep the current one.",
      required: !1
    }),
    description: import_pieces_framework6.Property.LongText({
      displayName: "Description",
      description: "New body for the post. Leave empty to keep the current one.",
      required: !1
    }),
    date: import_pieces_framework6.Property.ShortText({
      displayName: "Date",
      description: "New release date in ISO 8601 format. Leave empty to keep the current one.",
      required: !1
    }),
    active: import_pieces_framework6.Property.Checkbox({
      displayName: "Active",
      description: "Toggle whether the post is visible to users.",
      required: !1
    }),
    tag_names: import_pieces_framework6.Property.Array({
      displayName: "Tag Names",
      description: 'Replace the list of tags on the post. Must match existing tags (use "List Tags" to find them).',
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let body = {};
    propsValue.title !== void 0 && (body.title = propsValue.title), propsValue.description !== void 0 && (body.description = propsValue.description), propsValue.date !== void 0 && (body.date = propsValue.date), propsValue.active !== void 0 && (body.active = propsValue.active), Array.isArray(propsValue.tag_names) && (body.tagNames = propsValue.tag_names);
    let response = await produktlyApiCall({
      auth,
      method: import_pieces_common7.HttpMethod.PATCH,
      path: `/changelogs/${propsValue.changelog}/posts/${propsValue.post_id}`,
      body
    });
    return {
      post_id: response.body.id,
      post_title: response.body.title,
      post_description: response.body.description,
      post_date: response.body.date,
      post_active: response.body.active,
      post_tags: response.body.tags.map((t) => t.name).join(", ")
    };
  }
});

// ../ap/packages/pieces/community/produktly/src/lib/actions/list-feedback-widgets.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var listFeedbackWidgets = (0, import_pieces_framework7.createAction)({
  auth: produktlyAuth,
  name: "list_feedback_widgets",
  displayName: "List Feedback Widgets",
  description: "List all feedback widgets configured in your Produktly account.",
  audience: "both",
  aiMetadata: { description: "List every feedback widget configured in the Produktly account with its name, active state, and creation date, paginated with limit and offset. Pick this to discover widget IDs before pulling submissions with List Feedback Responses; use List NPS Widgets for the rating-based NPS surveys instead. Read-only and idempotent.", idempotent: !0 },
  props: {
    limit: import_pieces_framework7.Property.Number({
      displayName: "Limit",
      description: "Maximum number of widgets to return (1-100, default 50).",
      required: !1,
      defaultValue: 50
    }),
    offset: import_pieces_framework7.Property.Number({
      displayName: "Offset",
      description: "Number of widgets to skip for pagination (default 0).",
      required: !1,
      defaultValue: 0
    })
  },
  async run({ auth, propsValue }) {
    var _a, _b;
    return (await produktlyApiCall({
      auth,
      method: import_pieces_common8.HttpMethod.GET,
      path: "/feedback-widgets",
      queryParams: {
        limit: String((_a = propsValue.limit) != null ? _a : 50),
        offset: String((_b = propsValue.offset) != null ? _b : 0)
      }
    })).body.data.map((widget) => ({
      widget_id: widget.id,
      widget_name: widget.name,
      widget_active: widget.active,
      widget_created_at: widget.createdAt
    }));
  }
});

// ../ap/packages/pieces/community/produktly/src/lib/actions/list-feedback-responses.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var listFeedbackResponses = (0, import_pieces_framework8.createAction)({
  auth: produktlyAuth,
  name: "list_feedback_responses",
  displayName: "List Feedback Responses",
  description: "List the user responses submitted through a feedback widget.",
  audience: "both",
  aiMetadata: { description: "List the responses submitted to one Produktly feedback widget, each with its reaction type, chosen option, free-text message, and submitter name, email, and originating page; paginated with limit and offset and optionally narrowed to a submission-date range. Pick this to pull collected feedback on demand; use the New Feedback Response trigger to act as submissions arrive. Requires a feedback widget ID (discover one with List Feedback Widgets). Read-only and idempotent.", idempotent: !0 },
  props: {
    widget: produktlyProps.feedbackWidget,
    limit: import_pieces_framework8.Property.Number({
      displayName: "Limit",
      description: "Maximum number of responses to return (1-100, default 50).",
      required: !1,
      defaultValue: 50
    }),
    offset: import_pieces_framework8.Property.Number({
      displayName: "Offset",
      description: "Number of responses to skip for pagination (default 0).",
      required: !1,
      defaultValue: 0
    }),
    start_date: import_pieces_framework8.Property.ShortText({
      displayName: "Start Date",
      description: "Optional ISO 8601 date to filter responses from (e.g. 2024-01-01).",
      required: !1
    }),
    end_date: import_pieces_framework8.Property.ShortText({
      displayName: "End Date",
      description: "Optional ISO 8601 date to filter responses up to (e.g. 2024-12-31).",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    var _a, _b;
    let queryParams = {
      limit: String((_a = propsValue.limit) != null ? _a : 50),
      offset: String((_b = propsValue.offset) != null ? _b : 0)
    };
    return propsValue.start_date && (queryParams.startDate = propsValue.start_date), propsValue.end_date && (queryParams.endDate = propsValue.end_date), (await produktlyApiCall({
      auth,
      method: import_pieces_common9.HttpMethod.GET,
      path: `/feedback-widgets/${propsValue.widget}/responses`,
      queryParams
    })).body.data.map((resp) => ({
      response_id: resp.id,
      response_type: resp.type,
      response_option: resp.optionName,
      response_emoji: resp.emoji,
      response_message: resp.message,
      response_email: resp.email,
      response_user_name: resp.name,
      response_from_url: resp.fromUrl,
      response_created_at: resp.createdAt
    }));
  }
});

// ../ap/packages/pieces/community/produktly/src/lib/actions/list-roadmaps.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var listRoadmaps = (0, import_pieces_framework9.createAction)({
  auth: produktlyAuth,
  name: "list_roadmaps",
  displayName: "List Roadmaps",
  description: "List all public roadmaps in your Produktly account.",
  audience: "both",
  aiMetadata: { description: "List every public roadmap in the Produktly account with its internal and public name, active state, custom domain, and section and item counts, paginated with limit and offset. Pick this to discover roadmap IDs or compare roadmaps at a glance; use Get Roadmap when you need the actual sections, items, tags, and vote counts. Read-only and idempotent.", idempotent: !0 },
  props: {
    limit: import_pieces_framework9.Property.Number({
      displayName: "Limit",
      description: "Maximum number of roadmaps to return (1-100, default 50).",
      required: !1,
      defaultValue: 50
    }),
    offset: import_pieces_framework9.Property.Number({
      displayName: "Offset",
      description: "Number of roadmaps to skip for pagination (default 0).",
      required: !1,
      defaultValue: 0
    })
  },
  async run({ auth, propsValue }) {
    var _a, _b;
    return (await produktlyApiCall({
      auth,
      method: import_pieces_common10.HttpMethod.GET,
      path: "/roadmaps",
      queryParams: {
        limit: String((_a = propsValue.limit) != null ? _a : 50),
        offset: String((_b = propsValue.offset) != null ? _b : 0)
      }
    })).body.data.map((roadmap) => ({
      roadmap_id: roadmap.id,
      roadmap_name: roadmap.name,
      roadmap_public_name: roadmap.publicName,
      roadmap_active: roadmap.active,
      roadmap_custom_domain: roadmap.customDomain,
      roadmap_public_id: roadmap.publicId,
      roadmap_section_count: roadmap.sections.length,
      roadmap_item_count: roadmap.sections.reduce((acc, s) => acc + s.items.length, 0)
    }));
  }
});

// ../ap/packages/pieces/community/produktly/src/lib/actions/get-roadmap.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var getRoadmap = (0, import_pieces_framework10.createAction)({
  auth: produktlyAuth,
  name: "get_roadmap",
  displayName: "Get Roadmap",
  description: "Get a single roadmap with its sections, items, tags and vote counts.",
  audience: "both",
  aiMetadata: { description: "Fetch one Produktly public roadmap by ID, expanded into its sections and their individual items with vote counts, tags, and last-updated timestamps. Pick this when you need the contents of a known roadmap; use List Roadmaps first to discover roadmap IDs or when section and item counts alone are enough. Requires a roadmap ID. Read-only and idempotent.", idempotent: !0 },
  props: {
    roadmap: produktlyProps.roadmap
  },
  async run({ auth, propsValue }) {
    var _a, _b;
    let response = await produktlyApiCall({
      auth,
      method: import_pieces_common11.HttpMethod.GET,
      path: `/roadmaps/${propsValue.roadmap}`
    }), items = [];
    for (let section of response.body.sections)
      for (let item of section.items)
        items.push({
          roadmap_id: response.body.id,
          section_id: section.id,
          section_name: section.name,
          item_id: item.id,
          item_name: item.name,
          item_description: item.description,
          item_votes: item.votesCount,
          item_updated_at: item.updatedAt,
          item_tag: (_b = (_a = item.tag) == null ? void 0 : _a.name) != null ? _b : null
        });
    return {
      roadmap_id: response.body.id,
      roadmap_name: response.body.name,
      roadmap_public_name: response.body.publicName,
      roadmap_active: response.body.active,
      roadmap_custom_domain: response.body.customDomain,
      roadmap_public_id: response.body.publicId,
      section_count: response.body.sections.length,
      item_count: items.length,
      items
    };
  }
});

// ../ap/packages/pieces/community/produktly/src/lib/actions/list-nps-widgets.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var listNpsWidgets = (0, import_pieces_framework11.createAction)({
  auth: produktlyAuth,
  name: "list_nps_widgets",
  displayName: "List NPS Widgets",
  description: "List all NPS widgets configured in your Produktly account.",
  audience: "both",
  aiMetadata: { description: "List every NPS widget in the Produktly account with its name, active state, and creation date, paginated with limit and offset. Pick this to discover widget IDs before calling Get NPS Score or List NPS Responses; use List Feedback Widgets for the non-NPS feedback surveys instead. Read-only and idempotent.", idempotent: !0 },
  props: {
    limit: import_pieces_framework11.Property.Number({
      displayName: "Limit",
      description: "Maximum number of widgets to return (1-100, default 50).",
      required: !1,
      defaultValue: 50
    }),
    offset: import_pieces_framework11.Property.Number({
      displayName: "Offset",
      description: "Number of widgets to skip for pagination (default 0).",
      required: !1,
      defaultValue: 0
    })
  },
  async run({ auth, propsValue }) {
    var _a, _b;
    return (await produktlyApiCall({
      auth,
      method: import_pieces_common12.HttpMethod.GET,
      path: "/nps-widgets",
      queryParams: {
        limit: String((_a = propsValue.limit) != null ? _a : 50),
        offset: String((_b = propsValue.offset) != null ? _b : 0)
      }
    })).body.data.map((widget) => ({
      widget_id: widget.id,
      widget_name: widget.name,
      widget_active: widget.active,
      widget_created_at: widget.createdAt
    }));
  }
});

// ../ap/packages/pieces/community/produktly/src/lib/actions/get-nps-score.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common");
var getNpsScore = (0, import_pieces_framework12.createAction)({
  auth: produktlyAuth,
  name: "get_nps_score",
  displayName: "Get NPS Score",
  description: "Get the current Net Promoter Score (NPS) for a widget, broken down into promoters, passives and detractors.",
  audience: "both",
  aiMetadata: { description: "Fetch the aggregated Net Promoter Score for one Produktly NPS widget, with promoter, passive, and detractor counts and percentages, optionally restricted to a start/end date window. Pick this when you want the computed score; use List NPS Responses when you need the individual ratings and free-text comments behind it. Requires an NPS widget ID (discover one with List NPS Widgets). Read-only and idempotent.", idempotent: !0 },
  props: {
    widget: produktlyProps.npsWidget,
    start_date: import_pieces_framework12.Property.ShortText({
      displayName: "Start Date",
      description: "Optional ISO 8601 date to limit the score to responses from this date onward (e.g. 2024-01-01).",
      required: !1
    }),
    end_date: import_pieces_framework12.Property.ShortText({
      displayName: "End Date",
      description: "Optional ISO 8601 date to limit the score to responses up to this date (e.g. 2024-12-31).",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let queryParams = {};
    propsValue.start_date && (queryParams.startDate = propsValue.start_date), propsValue.end_date && (queryParams.endDate = propsValue.end_date);
    let response = await produktlyApiCall({
      auth,
      method: import_pieces_common13.HttpMethod.GET,
      path: `/nps-widgets/${propsValue.widget}/score`,
      queryParams
    });
    return {
      widget_id: propsValue.widget,
      nps_score: response.body.npsScore,
      total_responses: response.body.totalResponses,
      promoters_count: response.body.promoters.count,
      promoters_percentage: response.body.promoters.percentage,
      passives_count: response.body.passives.count,
      passives_percentage: response.body.passives.percentage,
      detractors_count: response.body.detractors.count,
      detractors_percentage: response.body.detractors.percentage
    };
  }
});

// ../ap/packages/pieces/community/produktly/src/lib/actions/list-nps-responses.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common14 = require("@activepieces/pieces-common");
var listNpsResponses = (0, import_pieces_framework13.createAction)({
  auth: produktlyAuth,
  name: "list_nps_responses",
  displayName: "List NPS Responses",
  description: "List individual NPS responses (rating + comment + user info) for a widget.",
  audience: "both",
  aiMetadata: { description: "List the individual NPS submissions for one Produktly NPS widget, each with its rating, free-text comment, and submitter name, email, user ID, and page URL; paginated with limit and offset and optionally narrowed to a submission-date range. Pick this when you need the raw responses; use Get NPS Score for the aggregated score and promoter/detractor breakdown. Requires an NPS widget ID. Read-only and idempotent.", idempotent: !0 },
  props: {
    widget: produktlyProps.npsWidget,
    limit: import_pieces_framework13.Property.Number({
      displayName: "Limit",
      description: "Maximum number of responses to return (1-100, default 50).",
      required: !1,
      defaultValue: 50
    }),
    offset: import_pieces_framework13.Property.Number({
      displayName: "Offset",
      description: "Number of responses to skip for pagination (default 0).",
      required: !1,
      defaultValue: 0
    }),
    start_date: import_pieces_framework13.Property.ShortText({
      displayName: "Start Date",
      description: "Optional ISO 8601 date to filter responses from (e.g. 2024-01-01).",
      required: !1
    }),
    end_date: import_pieces_framework13.Property.ShortText({
      displayName: "End Date",
      description: "Optional ISO 8601 date to filter responses up to (e.g. 2024-12-31).",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    var _a, _b;
    let queryParams = {
      limit: String((_a = propsValue.limit) != null ? _a : 50),
      offset: String((_b = propsValue.offset) != null ? _b : 0)
    };
    return propsValue.start_date && (queryParams.startDate = propsValue.start_date), propsValue.end_date && (queryParams.endDate = propsValue.end_date), (await produktlyApiCall({
      auth,
      method: import_pieces_common14.HttpMethod.GET,
      path: `/nps-widgets/${propsValue.widget}/responses`,
      queryParams
    })).body.data.map((resp) => ({
      response_id: resp.id,
      response_rating: resp.rating,
      response_comment: resp.comment,
      response_email: resp.email,
      response_user_name: resp.name,
      response_user_id: resp.userId,
      response_url: resp.url,
      response_language: resp.language,
      response_created_at: resp.createdAt
    }));
  }
});

// ../ap/packages/pieces/community/produktly/src/lib/actions/list-tags.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_common15 = require("@activepieces/pieces-common");
var listTags = (0, import_pieces_framework14.createAction)({
  auth: produktlyAuth,
  name: "list_tags",
  displayName: "List Tags",
  description: "List all tags that can be attached to changelog posts.",
  audience: "both",
  aiMetadata: { description: "List the tags defined in the Produktly account that can label changelog posts, with their display colours, paginated with limit and offset. Pick this to resolve valid tag names before Create Changelog Post or Update Changelog Post, which only accept tags that already exist. Read-only and idempotent.", idempotent: !0 },
  props: {
    limit: import_pieces_framework14.Property.Number({
      displayName: "Limit",
      description: "Maximum number of tags to return (1-100, default 50).",
      required: !1,
      defaultValue: 50
    }),
    offset: import_pieces_framework14.Property.Number({
      displayName: "Offset",
      description: "Number of tags to skip for pagination (default 0).",
      required: !1,
      defaultValue: 0
    })
  },
  async run({ auth, propsValue }) {
    var _a, _b;
    return (await produktlyApiCall({
      auth,
      method: import_pieces_common15.HttpMethod.GET,
      path: "/tags",
      queryParams: {
        limit: String((_a = propsValue.limit) != null ? _a : 50),
        offset: String((_b = propsValue.offset) != null ? _b : 0)
      }
    })).body.data.map((tag) => ({
      tag_id: tag.id,
      tag_name: tag.name,
      tag_background_color: tag.backgroundColor,
      tag_text_color: tag.textColor
    }));
  }
});

// ../ap/packages/pieces/community/produktly/src/lib/actions/get-company-stats.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_common16 = require("@activepieces/pieces-common");
var getCompanyStats = (0, import_pieces_framework15.createAction)({
  auth: produktlyAuth,
  name: "get_company_stats",
  description: "Get a company-wide statistics summary across all your Produktly features.",
  audience: "both",
  aiMetadata: { description: "Fetch one account-wide statistics summary covering every Produktly feature (tours, checklists, smart tips, announcements, changelogs, NPS widgets, roadmaps), for an optional start/end date window; omit both dates to use the API default period, which also reports the preceding comparison period. Pick this for a cross-feature rollup; use Get Widget Stats when you need per-entity event counts for a single feature type. Read-only and idempotent.", idempotent: !0 },
  displayName: "Get Company Stats",
  props: {
    start_date: import_pieces_framework15.Property.ShortText({
      displayName: "Start Date",
      description: "Optional ISO 8601 date for the start of the period (e.g. 2024-01-01).",
      required: !1
    }),
    end_date: import_pieces_framework15.Property.ShortText({
      displayName: "End Date",
      description: "Optional ISO 8601 date for the end of the period (e.g. 2024-12-31).",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let queryParams = {};
    propsValue.start_date && (queryParams.startDate = propsValue.start_date), propsValue.end_date && (queryParams.endDate = propsValue.end_date);
    let response = await produktlyApiCall({
      auth,
      method: import_pieces_common16.HttpMethod.GET,
      path: "/stats/summary",
      queryParams
    });
    return {
      period_start: response.body.period.start,
      period_end: response.body.period.end,
      period_days: response.body.period.days,
      period_granularity: response.body.period.granularity,
      previous_period_start: response.body.previousPeriod.start,
      previous_period_end: response.body.previousPeriod.end,
      features: response.body.features.map((feature) => ({
        feature_type: feature.type,
        feature_label: feature.label,
        primary_label: feature.primary.label,
        primary_count: feature.primary.count,
        primary_previous_count: feature.primary.previousCount,
        primary_change_pct: feature.primary.changePct,
        secondary_label: feature.secondary.label,
        secondary_value: feature.secondary.value,
        secondary_previous_value: feature.secondary.previousValue,
        secondary_change_pp: feature.secondary.changePp
      }))
    };
  }
});

// ../ap/packages/pieces/community/produktly/src/lib/actions/get-widget-stats.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_common17 = require("@activepieces/pieces-common");
var getWidgetStats = (0, import_pieces_framework16.createAction)({
  auth: produktlyAuth,
  name: "get_widget_stats",
  displayName: "Get Widget Stats",
  description: "Get event and unique-user statistics for a specific entity type (e.g. all changelogs, all NPS widgets).",
  audience: "both",
  aiMetadata: { description: "Fetch event and unique-user statistics for one Produktly feature type (product tour, checklist, smart tip, announcement, changelog, NPS widget, or roadmap), either across every entity of that type or narrowed to a single entity ID, over an optional start/end date window. Pick this for per-entity engagement numbers; use Get Company Stats for the account-wide rollup across all features. Requires an entity type. Read-only and idempotent.", idempotent: !0 },
  props: {
    entity_type: import_pieces_framework16.Property.StaticDropdown({
      displayName: "Entity Type",
      description: "The Produktly feature to get stats for.",
      required: !0,
      options: {
        options: [
          { label: "Product Tour", value: "tour" },
          { label: "Checklist", value: "checklist" },
          { label: "Smart Tip", value: "smartTip" },
          { label: "Announcement", value: "announcement" },
          { label: "Changelog", value: "changelog" },
          { label: "NPS Widget", value: "npsWidget" },
          { label: "Roadmap", value: "roadmap" }
        ]
      }
    }),
    entity_id: import_pieces_framework16.Property.Number({
      displayName: "Entity ID",
      description: "Optional. Filter stats to a single entity of the chosen type (e.g. a specific changelog ID).",
      required: !1
    }),
    start_date: import_pieces_framework16.Property.ShortText({
      displayName: "Start Date",
      description: "Optional ISO 8601 date for the start of the period (e.g. 2024-01-01).",
      required: !1
    }),
    end_date: import_pieces_framework16.Property.ShortText({
      displayName: "End Date",
      description: "Optional ISO 8601 date for the end of the period (e.g. 2024-12-31).",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let queryParams = {};
    propsValue.entity_id !== void 0 && (queryParams.entityId = String(propsValue.entity_id)), propsValue.start_date && (queryParams.startDate = propsValue.start_date), propsValue.end_date && (queryParams.endDate = propsValue.end_date);
    let response = await produktlyApiCall({
      auth,
      method: import_pieces_common17.HttpMethod.GET,
      path: `/stats/widgets/${propsValue.entity_type}`,
      queryParams
    });
    return {
      entity_type: response.body.entityType,
      period_start: response.body.period.start,
      period_end: response.body.period.end,
      widget_count: response.body.widgets.length,
      widgets: response.body.widgets.map((widget) => ({
        entity_id: widget.entityId,
        name: widget.name,
        archived_at: widget.archivedAt,
        total_events: widget.totalEvents,
        total_unique_users: widget.totalUniqueUsers,
        event_breakdown: Object.entries(widget.events).map(([eventName, stats]) => `${eventName}: ${stats.count} (${stats.uniqueUsers} unique)`).join(" | ")
      }))
    };
  }
});

// ../ap/packages/pieces/community/produktly/src/lib/triggers/new-changelog-post.ts
var import_pieces_common18 = require("@activepieces/pieces-common"), import_pieces_framework17 = require("@activepieces/pieces-framework");
var triggerProps = {
  changelog: produktlyProps.changelog
}, polling = {
  strategy: import_pieces_common18.DedupeStrategy.LAST_ITEM,
  items: async ({ auth, propsValue }) => (await produktlyApiCall({
    auth,
    method: import_pieces_common18.HttpMethod.GET,
    path: `/changelogs/${propsValue.changelog}/posts`,
    queryParams: { limit: "100" }
  })).body.data.map((post) => ({
    id: String(post.id),
    data: {
      post_id: post.id,
      changelog_id: propsValue.changelog,
      post_title: post.title,
      post_description: post.description,
      post_date: post.date,
      post_active: post.active,
      post_tags: post.tags.map((t) => t.name).join(", ")
    }
  }))
}, newChangelogPost = (0, import_pieces_framework17.createTrigger)({
  auth: produktlyAuth,
  name: "new_changelog_post",
  displayName: "New Changelog Post",
  description: "Fires when a new post is published in the selected changelog.",
  aiMetadata: {
    description: "Fires when a new post appears in one selected Produktly changelog, representing a product announcement or release note. Polls that changelog on a schedule and emits each post it has not seen before, including hidden (inactive) posts alongside published ones."
  },
  props: triggerProps,
  sampleData: {
    post_id: 123,
    changelog_id: 1,
    post_title: "Welcome to the new dashboard",
    post_description: "We rebuilt the dashboard from scratch...",
    post_date: "2024-12-15T10:00:00Z",
    post_active: !0,
    post_tags: "Product Update, UI"
  },
  type: import_pieces_framework17.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common18.pollingHelper.test(polling, context);
  },
  async onEnable(context) {
    await import_pieces_common18.pollingHelper.onEnable(polling, context);
  },
  async onDisable(context) {
    await import_pieces_common18.pollingHelper.onDisable(polling, context);
  },
  async run(context) {
    return await import_pieces_common18.pollingHelper.poll(polling, context);
  }
});

// ../ap/packages/pieces/community/produktly/src/lib/triggers/new-feedback-response.ts
var import_pieces_common19 = require("@activepieces/pieces-common"), import_pieces_framework18 = require("@activepieces/pieces-framework");
var triggerProps2 = {
  widget: produktlyProps.feedbackWidget
}, polling2 = {
  strategy: import_pieces_common19.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue }) => (await produktlyApiCall({
    auth,
    method: import_pieces_common19.HttpMethod.GET,
    path: `/feedback-widgets/${propsValue.widget}/responses`,
    queryParams: { limit: "100" }
  })).body.data.map((resp) => ({
    epochMilliSeconds: Date.parse(resp.createdAt),
    data: {
      response_id: resp.id,
      widget_id: propsValue.widget,
      response_type: resp.type,
      response_option: resp.optionName,
      response_emoji: resp.emoji,
      response_message: resp.message,
      response_email: resp.email,
      response_user_name: resp.name,
      response_from_url: resp.fromUrl,
      response_created_at: resp.createdAt
    }
  }))
}, newFeedbackResponse = (0, import_pieces_framework18.createTrigger)({
  auth: produktlyAuth,
  name: "new_feedback_response",
  displayName: "New Feedback Response",
  description: "Fires when a user submits a new response to the selected feedback widget.",
  aiMetadata: {
    description: "Fires when an end user submits feedback through one selected Produktly feedback widget. Polls that widget on a schedule and emits each new submission, carrying the reaction type and option, any free-text message, the submitter name and email, and the page the feedback came from."
  },
  props: triggerProps2,
  sampleData: {
    response_id: 456,
    widget_id: 1,
    response_type: "positive",
    response_option: "Love it",
    response_emoji: "\u{1F60D}",
    response_message: "This is exactly what I needed!",
    response_email: "jane@example.com",
    response_user_name: "Jane Doe",
    response_from_url: "https://app.example.com/dashboard",
    response_created_at: "2024-12-15T10:00:00Z"
  },
  type: import_pieces_framework18.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common19.pollingHelper.test(polling2, context);
  },
  async onEnable(context) {
    await import_pieces_common19.pollingHelper.onEnable(polling2, context);
  },
  async onDisable(context) {
    await import_pieces_common19.pollingHelper.onDisable(polling2, context);
  },
  async run(context) {
    return await import_pieces_common19.pollingHelper.poll(polling2, context);
  }
});

// ../ap/packages/pieces/community/produktly/src/lib/triggers/new-nps-response.ts
var import_pieces_common20 = require("@activepieces/pieces-common"), import_pieces_framework19 = require("@activepieces/pieces-framework");
var triggerProps3 = {
  widget: produktlyProps.npsWidget
}, polling3 = {
  strategy: import_pieces_common20.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue }) => (await produktlyApiCall({
    auth,
    method: import_pieces_common20.HttpMethod.GET,
    path: `/nps-widgets/${propsValue.widget}/responses`,
    queryParams: { limit: "100" }
  })).body.data.map((resp) => ({
    epochMilliSeconds: Date.parse(resp.createdAt),
    data: {
      response_id: resp.id,
      widget_id: propsValue.widget,
      response_rating: resp.rating,
      response_comment: resp.comment,
      response_email: resp.email,
      response_user_name: resp.name,
      response_user_id: resp.userId,
      response_url: resp.url,
      response_language: resp.language,
      response_created_at: resp.createdAt
    }
  }))
}, newNpsResponse = (0, import_pieces_framework19.createTrigger)({
  auth: produktlyAuth,
  name: "new_nps_response",
  displayName: "New NPS Response",
  description: "Fires when a user submits a new NPS rating for the selected widget.",
  aiMetadata: {
    description: "Fires when an end user submits an NPS rating through one selected Produktly NPS widget. Polls that widget on a schedule and emits each new response with its score, any free-text comment, and the submitter name, email, user ID, and page URL, so detractors can be routed for follow-up as they arrive."
  },
  props: triggerProps3,
  sampleData: {
    response_id: 789,
    widget_id: 1,
    response_rating: "9",
    response_comment: "Great product, would love more integrations.",
    response_email: "alex@example.com",
    response_user_name: "Alex",
    response_user_id: "user_42",
    response_url: "https://app.example.com/settings",
    response_language: "en",
    response_created_at: "2024-12-15T10:00:00Z"
  },
  type: import_pieces_framework19.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common20.pollingHelper.test(polling3, context);
  },
  async onEnable(context) {
    await import_pieces_common20.pollingHelper.onEnable(polling3, context);
  },
  async onDisable(context) {
    await import_pieces_common20.pollingHelper.onDisable(polling3, context);
  },
  async run(context) {
    return await import_pieces_common20.pollingHelper.poll(polling3, context);
  }
});

// ../ap/packages/pieces/community/produktly/src/lib/triggers/new-tag.ts
var import_pieces_common21 = require("@activepieces/pieces-common"), import_pieces_framework20 = require("@activepieces/pieces-framework");
var triggerProps4 = {}, polling4 = {
  strategy: import_pieces_common21.DedupeStrategy.LAST_ITEM,
  items: async ({ auth }) => (await produktlyApiCall({
    auth,
    method: import_pieces_common21.HttpMethod.GET,
    path: "/tags",
    queryParams: { limit: "100" }
  })).body.data.map((tag) => ({
    id: String(tag.id),
    data: {
      tag_id: tag.id,
      tag_name: tag.name,
      tag_background_color: tag.backgroundColor,
      tag_text_color: tag.textColor
    }
  }))
}, newTag = (0, import_pieces_framework20.createTrigger)({
  auth: produktlyAuth,
  name: "new_tag",
  displayName: "New Tag",
  description: "Fires when a new tag is created in your Produktly account.",
  aiMetadata: {
    description: "Fires when a new tag is created anywhere in the Produktly account. Polls the account-wide tag list on a schedule and emits each tag it has not seen before, with its name and display colours; tags are the labels that changelog posts can be categorised with."
  },
  props: triggerProps4,
  sampleData: {
    tag_id: 1,
    tag_name: "Bug fix",
    tag_background_color: "#FF0000",
    tag_text_color: "#FFFFFF"
  },
  type: import_pieces_framework20.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common21.pollingHelper.test(polling4, context);
  },
  async onEnable(context) {
    await import_pieces_common21.pollingHelper.onEnable(polling4, context);
  },
  async onDisable(context) {
    await import_pieces_common21.pollingHelper.onDisable(polling4, context);
  },
  async run(context) {
    return await import_pieces_common21.pollingHelper.poll(polling4, context);
  }
});

// ../ap/packages/pieces/community/produktly/src/index.ts
var produktly = (0, import_pieces_framework21.createPiece)({
  displayName: "Produktly",
  description: "Onboarding, in-app announcements, feedback collection and NPS for SaaS products.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/produktly.png",
  categories: [import_shared.PieceCategory.PRODUCTIVITY, import_shared.PieceCategory.MARKETING],
  auth: produktlyAuth,
  authors: ["cumonvip1"],
  actions: [
    listChangelogs,
    listChangelogPosts,
    createChangelogPost,
    updateChangelogPost,
    listFeedbackWidgets,
    listFeedbackResponses,
    listRoadmaps,
    getRoadmap,
    listNpsWidgets,
    getNpsScore,
    listNpsResponses,
    listTags,
    getCompanyStats,
    getWidgetStats,
    (0, import_pieces_common22.createCustomApiCallAction)({
      baseUrl: () => PRODUKTLY_BASE_URL,
      auth: produktlyAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [newChangelogPost, newFeedbackResponse, newNpsResponse, newTag]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  produktly,
  produktlyAuth
});
