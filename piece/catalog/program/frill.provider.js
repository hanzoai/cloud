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

// ../ap/packages/pieces/community/frill/src/index.ts
var index_exports = {};
__export(index_exports, {
  frill: () => frill,
  frillAuth: () => frillAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common"), import_pieces_framework14 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/frill/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), frillAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `To get your Frill API key:
1. Log in to your Frill dashboard at https://app.frill.co
2. Go to **Settings > API**
3. Click **Create API Key**
4. Copy the key and paste it here

The key is used in the Authorization header as a Bearer token.`,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: "https://api.frill.co/v1/ideas",
        authentication: {
          type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
          token: auth
        },
        queryParams: { limit: "1" }
      }), { valid: !0 };
    } catch (e) {
      return { valid: !1, error: "Invalid API Key or unable to connect to Frill." };
    }
  }
});

// ../ap/packages/pieces/community/frill/src/lib/actions/create-idea.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/frill/src/lib/common/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var BASE_URL = "https://api.frill.co/v1";
async function frillApiCall({
  token,
  method,
  path,
  body,
  queryParams
}) {
  let sanitizedQuery = {};
  if (queryParams)
    for (let [key, value] of Object.entries(queryParams))
      value != null && (sanitizedQuery[key] = String(value));
  return await import_pieces_common2.httpClient.sendRequest({
    method,
    url: `${BASE_URL}${path}`,
    authentication: {
      type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
      token
    },
    queryParams: sanitizedQuery,
    body
  });
}
async function frillPaginatedApiCall({
  token,
  path,
  queryParams,
  limit
}) {
  let results = [], targetLimit = limit && limit > 0 ? limit : 100, after, hasNextPage = !0;
  for (; hasNextPage && results.length < targetLimit; ) {
    let pageLimit = Math.min(100, targetLimit - results.length), response = await frillApiCall({
      token,
      method: import_pieces_common2.HttpMethod.GET,
      path,
      queryParams: __spreadProps(__spreadValues({}, queryParams), {
        limit: pageLimit,
        after
      })
    });
    if (response.body.data.length === 0) break;
    results.push(...response.body.data), hasNextPage = response.body.pagination.hasNextPage, after = response.body.pagination.endCursor;
  }
  return results;
}
function flattenObject(obj, prefix = "") {
  let result = {};
  for (let [key, value] of Object.entries(obj)) {
    let flatKey = prefix ? `${prefix}_${key}` : key;
    value == null ? result[flatKey] = null : Array.isArray(value) ? result[flatKey] = value.map((v) => typeof v == "object" ? JSON.stringify(v) : String(v)).join(", ") : typeof value == "object" ? Object.assign(result, flattenObject(value, flatKey)) : result[flatKey] = value;
  }
  return result;
}
var frillDropdowns = {
  ideaDropdown: import_pieces_framework2.Property.Dropdown({
    displayName: "Idea",
    description: "Select the feedback or idea to work with",
    auth: frillAuth,
    refreshers: [],
    required: !0,
    options: async ({ auth }) => {
      if (!auth)
        return { disabled: !0, options: [], placeholder: "Please connect your account first" };
      try {
        return {
          disabled: !1,
          options: (await frillApiCall({
            token: auth.secret_text,
            method: import_pieces_common2.HttpMethod.GET,
            path: "/ideas",
            queryParams: { limit: 100 }
          })).body.data.map((idea) => ({
            label: `${idea.name} (${idea.slug})`,
            value: idea.idx
          }))
        };
      } catch (error) {
        return { disabled: !0, options: [], placeholder: "Failed to load ideas. Check your connection." };
      }
    }
  }),
  ideaDropdownOptional: import_pieces_framework2.Property.Dropdown({
    displayName: "Idea",
    description: "Optionally filter comments by a specific idea",
    auth: frillAuth,
    refreshers: [],
    required: !1,
    options: async ({ auth }) => {
      if (!auth)
        return { disabled: !0, options: [], placeholder: "Please connect your account first" };
      try {
        return {
          disabled: !1,
          options: (await frillApiCall({
            token: auth.secret_text,
            method: import_pieces_common2.HttpMethod.GET,
            path: "/ideas",
            queryParams: { limit: 100 }
          })).body.data.map((idea) => ({
            label: `${idea.name} (${idea.slug})`,
            value: idea.idx
          }))
        };
      } catch (error) {
        return { disabled: !0, options: [], placeholder: "Failed to load ideas. Check your connection." };
      }
    }
  }),
  statusDropdown: import_pieces_framework2.Property.Dropdown({
    displayName: "Status",
    description: "Select the status for the idea",
    auth: frillAuth,
    refreshers: [],
    required: !1,
    options: async ({ auth }) => {
      if (!auth)
        return { disabled: !0, options: [], placeholder: "Please connect your account first" };
      try {
        return {
          disabled: !1,
          options: (await frillApiCall({
            token: auth.secret_text,
            method: import_pieces_common2.HttpMethod.GET,
            path: "/statuses"
          })).body.data.map((status) => ({
            label: status.name,
            value: status.idx
          }))
        };
      } catch (error) {
        return { disabled: !0, options: [], placeholder: "Failed to load statuses. Check your connection." };
      }
    }
  }),
  topicDropdown: import_pieces_framework2.Property.MultiSelectDropdown({
    displayName: "Topics",
    description: "Select one or more topics to tag the idea with",
    auth: frillAuth,
    refreshers: [],
    required: !1,
    options: async ({ auth }) => {
      if (!auth)
        return { disabled: !0, options: [], placeholder: "Please connect your account first" };
      try {
        return {
          disabled: !1,
          options: (await frillApiCall({
            token: auth.secret_text,
            method: import_pieces_common2.HttpMethod.GET,
            path: "/topics"
          })).body.data.map((topic) => ({
            label: topic.name,
            value: topic.idx
          }))
        };
      } catch (error) {
        return { disabled: !0, options: [], placeholder: "Failed to load topics. Check your connection." };
      }
    }
  }),
  announcementCategoryDropdown: import_pieces_framework2.Property.Dropdown({
    displayName: "Category",
    description: "Select the announcement category",
    auth: frillAuth,
    refreshers: [],
    required: !1,
    options: async ({ auth }) => {
      if (!auth)
        return { disabled: !0, options: [], placeholder: "Please connect your account first" };
      try {
        return {
          disabled: !1,
          options: (await frillApiCall({
            token: auth.secret_text,
            method: import_pieces_common2.HttpMethod.GET,
            path: "/announcement-categories"
          })).body.data.map((cat) => ({
            label: cat.name,
            value: cat.idx
          }))
        };
      } catch (error) {
        return { disabled: !0, options: [], placeholder: "Failed to load categories. Check your connection." };
      }
    }
  }),
  followerDropdown: import_pieces_framework2.Property.Dropdown({
    displayName: "Follower (User)",
    description: "Select a follower to update",
    auth: frillAuth,
    refreshers: [],
    required: !0,
    options: async ({ auth }) => {
      if (!auth)
        return { disabled: !0, options: [], placeholder: "Please connect your account first" };
      try {
        return {
          disabled: !1,
          options: (await frillApiCall({
            token: auth.secret_text,
            method: import_pieces_common2.HttpMethod.GET,
            path: "/followers",
            queryParams: { limit: 100 }
          })).body.data.map((follower) => ({
            label: `${follower.name} (${follower.email})`,
            value: follower.idx
          }))
        };
      } catch (error) {
        return { disabled: !0, options: [], placeholder: "Failed to load followers. Check your connection." };
      }
    }
  })
};

// ../ap/packages/pieces/community/frill/src/lib/actions/create-idea.ts
var createIdea = (0, import_pieces_framework3.createAction)({
  auth: frillAuth,
  name: "create_idea",
  displayName: "Create Idea",
  description: "Create a new feedback idea or feature request in Frill.",
  audience: "both",
  aiMetadata: { description: "Creates a new feedback idea or feature request in Frill, optionally tagged with topics, a status, a bug flag, and roadmap visibility. Use to capture incoming feedback or seed roadmap items. Not idempotent: each call creates a distinct idea even with identical input.", idempotent: !1 },
  props: {
    name: import_pieces_framework3.Property.ShortText({
      displayName: "Title",
      description: "The title of the idea or feedback.",
      required: !0
    }),
    description: import_pieces_framework3.Property.LongText({
      displayName: "Description",
      description: "The description of the idea in Markdown format.",
      required: !1
    }),
    topics: frillDropdowns.topicDropdown,
    status: frillDropdowns.statusDropdown,
    is_bug: import_pieces_framework3.Property.Checkbox({
      displayName: "Mark as Bug",
      description: "Check if this idea represents a bug.",
      required: !1,
      defaultValue: !1
    }),
    show_in_roadmap: import_pieces_framework3.Property.Checkbox({
      displayName: "Show in Roadmap",
      description: "Display this idea on the public roadmap.",
      required: !1,
      defaultValue: !0
    }),
    cover_image: import_pieces_framework3.Property.ShortText({
      displayName: "Cover Image URL",
      description: "Optional URL for a cover image shown on the roadmap.",
      required: !1
    })
  },
  async run(context) {
    let body = {
      name: context.propsValue.name
    };
    context.propsValue.description && (body.description = context.propsValue.description), context.propsValue.topics && context.propsValue.topics.length > 0 && (body.topics = context.propsValue.topics), context.propsValue.status && (body.status = context.propsValue.status), context.propsValue.is_bug !== void 0 && (body.is_bug = context.propsValue.is_bug), context.propsValue.show_in_roadmap !== void 0 && (body.show_in_roadmap = context.propsValue.show_in_roadmap), context.propsValue.cover_image && (body.cover_image = context.propsValue.cover_image);
    let response = await frillApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common3.HttpMethod.POST,
      path: "/ideas",
      body
    });
    return flattenObject(response.body.data);
  }
});

// ../ap/packages/pieces/community/frill/src/lib/actions/update-idea.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var updateIdea = (0, import_pieces_framework4.createAction)({
  auth: frillAuth,
  name: "update_idea",
  displayName: "Update Idea",
  description: "Update an existing idea, including status, topics, or archive flag.",
  audience: "both",
  aiMetadata: { description: "Updates fields of an existing Frill idea identified by its idea ID, such as title, description, status, topics, bug/archive/completed flags, and roadmap visibility; only the fields you set are changed. Use to modify or transition an idea you already know the ID of. Idempotent: re-running with the same input leaves the idea in the same state.", idempotent: !0 },
  props: {
    idea: frillDropdowns.ideaDropdown,
    name: import_pieces_framework4.Property.ShortText({
      displayName: "Title",
      description: "New title for the idea. Leave empty to keep unchanged.",
      required: !1
    }),
    description: import_pieces_framework4.Property.LongText({
      displayName: "Description",
      description: "New description in Markdown. Leave empty to keep unchanged.",
      required: !1
    }),
    status: frillDropdowns.statusDropdown,
    topics: frillDropdowns.topicDropdown,
    is_bug: import_pieces_framework4.Property.Checkbox({
      displayName: "Mark as Bug",
      description: "Check to mark this idea as a bug. Leave empty to keep unchanged.",
      required: !1
    }),
    is_archived: import_pieces_framework4.Property.Checkbox({
      displayName: "Archive",
      description: "Check to archive this idea. Leave empty to keep unchanged.",
      required: !1
    }),
    is_completed: import_pieces_framework4.Property.Checkbox({
      displayName: "Mark Completed",
      description: "Check to mark this idea as completed. Leave empty to keep unchanged.",
      required: !1
    }),
    show_in_roadmap: import_pieces_framework4.Property.Checkbox({
      displayName: "Show in Roadmap",
      description: "Display this idea on the public roadmap. Leave empty to keep unchanged.",
      required: !1
    })
  },
  async run(context) {
    let body = {};
    context.propsValue.name && (body.name = context.propsValue.name), context.propsValue.description && (body.description = context.propsValue.description), context.propsValue.status && (body.status = context.propsValue.status), context.propsValue.topics && context.propsValue.topics.length > 0 && (body.topics = context.propsValue.topics), context.propsValue.is_bug !== void 0 && (body.is_bug = context.propsValue.is_bug), context.propsValue.is_archived !== void 0 && (body.is_archived = context.propsValue.is_archived), context.propsValue.is_completed !== void 0 && (body.is_completed = context.propsValue.is_completed), context.propsValue.show_in_roadmap !== void 0 && (body.show_in_roadmap = context.propsValue.show_in_roadmap);
    let response = await frillApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common4.HttpMethod.POST,
      path: `/ideas/${context.propsValue.idea}`,
      body
    });
    return flattenObject(response.body.data);
  }
});

// ../ap/packages/pieces/community/frill/src/lib/actions/create-comment.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var createComment = (0, import_pieces_framework5.createAction)({
  auth: frillAuth,
  name: "create_comment",
  displayName: "Create Comment",
  description: "Add a public comment or internal note to an existing idea.",
  audience: "both",
  aiMetadata: { description: "Posts a comment on an existing Frill idea (identified by idea ID), either as a public comment or, when the internal-note flag is set, as a private internal note. Use to reply to or annotate feedback. Not idempotent: each call appends a new comment.", idempotent: !1 },
  props: {
    idea: frillDropdowns.ideaDropdown,
    message: import_pieces_framework5.Property.LongText({
      displayName: "Message",
      description: "The comment text in Markdown format.",
      required: !0
    }),
    is_private: import_pieces_framework5.Property.Checkbox({
      displayName: "Internal Note",
      description: "Check to post this as a private internal note instead of a public comment.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let response = await frillApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common5.HttpMethod.POST,
      path: "/comments",
      body: {
        idea_id: context.propsValue.idea,
        message: context.propsValue.message,
        is_private: context.propsValue.is_private
      }
    });
    return flattenObject(response.body.data);
  }
});

// ../ap/packages/pieces/community/frill/src/lib/actions/create-announcement.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var createAnnouncement = (0, import_pieces_framework6.createAction)({
  auth: frillAuth,
  name: "create_announcement",
  displayName: "Create Announcement",
  description: "Publish a changelog or product update announcement.",
  audience: "both",
  aiMetadata: { description: "Creates a changelog or product-update announcement in Frill, either published immediately or saved as a draft depending on the publish flag, optionally filed under a category. Use to broadcast product updates. Not idempotent: each call creates a separate announcement.", idempotent: !1 },
  props: {
    name: import_pieces_framework6.Property.ShortText({
      displayName: "Title",
      description: "The title of the announcement.",
      required: !0
    }),
    content: import_pieces_framework6.Property.LongText({
      displayName: "Content",
      description: "The announcement body in Markdown format.",
      required: !0
    }),
    category: frillDropdowns.announcementCategoryDropdown,
    is_published: import_pieces_framework6.Property.Checkbox({
      displayName: "Publish Immediately",
      description: "Check to publish immediately. Uncheck to save as draft.",
      required: !1,
      defaultValue: !0
    })
  },
  async run(context) {
    let body = {
      name: context.propsValue.name,
      content: context.propsValue.content,
      is_published: context.propsValue.is_published
    };
    context.propsValue.category && (body.categories = [context.propsValue.category]);
    let response = await frillApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common6.HttpMethod.POST,
      path: "/announcements",
      body
    });
    return flattenObject(response.body.data);
  }
});

// ../ap/packages/pieces/community/frill/src/lib/actions/create-follower.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var createFollower = (0, import_pieces_framework7.createAction)({
  auth: frillAuth,
  name: "create_follower",
  displayName: "Create Follower",
  description: "Add a new user (follower) to Frill, optionally with custom attributes and company data.",
  audience: "both",
  aiMetadata: { description: "Adds a new follower (end user) to Frill by name and email, optionally with custom attributes and associated companies (each company needs an id and name). Use to register a user so they can be linked to feedback. Not idempotent: each call creates a follower, so use Update Follower to change an existing one.", idempotent: !1 },
  props: {
    name: import_pieces_framework7.Property.ShortText({
      displayName: "Name",
      description: "Full name of the follower.",
      required: !0
    }),
    email: import_pieces_framework7.Property.ShortText({
      displayName: "Email",
      description: "Email address of the follower.",
      required: !0
    }),
    attributes: import_pieces_framework7.Property.Object({
      displayName: "Custom Attributes",
      description: "Optional custom attributes as key-value pairs (requires custom attributes feature).",
      required: !1
    }),
    companies: import_pieces_framework7.Property.Array({
      displayName: "Companies",
      description: "Companies to associate with this follower. Find company IDs in your Frill dashboard under Settings > Companies.",
      required: !1,
      properties: {
        id: import_pieces_framework7.Property.ShortText({ displayName: "Company ID", description: "The unique identifier for the company in Frill (found in the company URL or via the API).", required: !0 }),
        name: import_pieces_framework7.Property.ShortText({ displayName: "Company Name", description: "The display name for the company.", required: !0 })
      }
    })
  },
  async run(context) {
    let body = {
      name: context.propsValue.name,
      email: context.propsValue.email
    };
    context.propsValue.attributes && (body.attributes = context.propsValue.attributes), context.propsValue.companies && context.propsValue.companies.length > 0 && (body.companies = context.propsValue.companies);
    let response = await frillApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common7.HttpMethod.POST,
      path: "/followers",
      body
    });
    return flattenObject(response.body.data);
  }
});

// ../ap/packages/pieces/community/frill/src/lib/actions/update-follower.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var updateFollower = (0, import_pieces_framework8.createAction)({
  auth: frillAuth,
  name: "update_follower",
  displayName: "Update Follower",
  description: "Update an existing user (follower), including custom attributes and company associations.",
  audience: "both",
  aiMetadata: { description: "Updates an existing Frill follower identified by follower ID, changing name or email and merging in custom attributes and company associations; only the fields you set are applied. Use to enrich or correct a known follower. Idempotent: re-running with the same input converges to the same state.", idempotent: !0 },
  props: {
    follower: frillDropdowns.followerDropdown,
    name: import_pieces_framework8.Property.ShortText({
      displayName: "Name",
      description: "New name for the follower. Leave empty to keep unchanged.",
      required: !1
    }),
    email: import_pieces_framework8.Property.ShortText({
      displayName: "Email",
      description: "New email address. Leave empty to keep unchanged.",
      required: !1
    }),
    attributes: import_pieces_framework8.Property.Object({
      displayName: "Custom Attributes",
      description: "Custom attributes to merge with existing ones (requires custom attributes feature).",
      required: !1
    }),
    companies: import_pieces_framework8.Property.Array({
      displayName: "Companies",
      description: "Companies to merge with this follower's existing associations. Find company IDs in your Frill dashboard under Settings > Companies.",
      required: !1,
      properties: {
        id: import_pieces_framework8.Property.ShortText({ displayName: "Company ID", description: "The unique identifier for the company in Frill (found in the company URL or via the API).", required: !0 }),
        name: import_pieces_framework8.Property.ShortText({ displayName: "Company Name", description: "The display name for the company.", required: !0 })
      }
    })
  },
  async run(context) {
    let body = {};
    context.propsValue.name && (body.name = context.propsValue.name), context.propsValue.email && (body.email = context.propsValue.email), context.propsValue.attributes && (body.attributes = context.propsValue.attributes), context.propsValue.companies && context.propsValue.companies.length > 0 && (body.companies = context.propsValue.companies);
    let response = await frillApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common8.HttpMethod.POST,
      path: `/followers/${context.propsValue.follower}`,
      body
    });
    return flattenObject(response.body.data);
  }
});

// ../ap/packages/pieces/community/frill/src/lib/actions/get-ideas.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var getIdeas = (0, import_pieces_framework9.createAction)({
  auth: frillAuth,
  name: "get_ideas",
  displayName: "Get Ideas",
  description: "Fetch a list of ideas/feedback entries from Frill.",
  audience: "both",
  aiMetadata: { description: "Retrieves a list of ideas/feedback entries from Frill, optionally filtered by status, with a result limit (defaults to 20). Use to browse or search existing feedback, e.g. to find an idea ID before commenting or updating. Idempotent: read-only.", idempotent: !0 },
  props: {
    limit: import_pieces_framework9.Property.Number({
      displayName: "Limit",
      description: "Maximum number of ideas to return. Defaults to 20.",
      required: !1,
      defaultValue: 20
    }),
    status: frillDropdowns.statusDropdown
  },
  async run(context) {
    var _a, _b;
    let queryParams = {
      limit: (_a = context.propsValue.limit) != null ? _a : 20
    };
    return context.propsValue.status && (queryParams.status = context.propsValue.status), (await frillPaginatedApiCall({
      token: context.auth.secret_text,
      path: "/ideas",
      queryParams,
      limit: (_b = context.propsValue.limit) != null ? _b : 20
    })).map((idea) => flattenObject(idea));
  }
});

// ../ap/packages/pieces/community/frill/src/lib/actions/get-comments.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var getComments = (0, import_pieces_framework10.createAction)({
  auth: frillAuth,
  name: "get_comments",
  displayName: "Get Comments",
  description: "Fetch comments for a specific idea or all recent comments.",
  audience: "both",
  aiMetadata: { description: "Retrieves comments from Frill: scoped to a single idea when an idea ID is supplied, or all recent comments across ideas when left empty, up to the result limit (defaults to 20). Use to read discussion on feedback. Idempotent: read-only.", idempotent: !0 },
  props: {
    idea: frillDropdowns.ideaDropdownOptional,
    limit: import_pieces_framework10.Property.Number({
      displayName: "Limit",
      description: "Maximum number of comments to return. Defaults to 20.",
      required: !1,
      defaultValue: 20
    })
  },
  async run(context) {
    var _a, _b;
    let queryParams = {
      limit: (_a = context.propsValue.limit) != null ? _a : 20
    };
    return context.propsValue.idea && (queryParams.idea_id = context.propsValue.idea), (await frillPaginatedApiCall({
      token: context.auth.secret_text,
      path: "/comments",
      queryParams,
      limit: (_b = context.propsValue.limit) != null ? _b : 20
    })).map((comment) => flattenObject(comment));
  }
});

// ../ap/packages/pieces/community/frill/src/lib/triggers/new-idea.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var polling = {
  strategy: import_pieces_common9.DedupeStrategy.TIMEBASED,
  items: async ({ auth }) => (await frillApiCall({
    token: auth.secret_text,
    method: import_pieces_common9.HttpMethod.GET,
    path: "/ideas",
    queryParams: { limit: 100 }
  })).body.data.map((item) => {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n, _o, _p, _q, _r, _s, _t;
    return {
      epochMilliSeconds: new Date(item.created_at).getTime(),
      data: {
        idx: item.idx,
        name: item.name,
        slug: item.slug,
        excerpt: (_a = item.excerpt) != null ? _a : null,
        content_html: (_b = item.content_html) != null ? _b : null,
        content_markdown: (_c = item.content_markdown) != null ? _c : null,
        vote_count: (_d = item.vote_count) != null ? _d : 0,
        comment_count: (_e = item.comment_count) != null ? _e : 0,
        is_pined: (_f = item.is_pined) != null ? _f : !1,
        is_bug: (_g = item.is_bug) != null ? _g : !1,
        is_archived: (_h = item.is_archived) != null ? _h : !1,
        is_completed: (_i = item.is_completed) != null ? _i : !1,
        show_in_roadmap: (_j = item.show_in_roadmap) != null ? _j : !1,
        approval_status: (_k = item.approval_status) != null ? _k : null,
        created_at: item.created_at,
        updated_at: (_l = item.updated_at) != null ? _l : null,
        author_name: (_n = (_m = item.author) == null ? void 0 : _m.name) != null ? _n : null,
        author_email: (_p = (_o = item.author) == null ? void 0 : _o.email) != null ? _p : null,
        status_name: (_r = (_q = item.status) == null ? void 0 : _q.name) != null ? _r : null,
        status_color: (_t = (_s = item.status) == null ? void 0 : _s.color) != null ? _t : null,
        topics: Array.isArray(item.topics) ? item.topics.map((t) => t.name).join(", ") : null
      }
    };
  })
}, newIdeaTrigger = (0, import_pieces_framework11.createTrigger)({
  auth: frillAuth,
  name: "new_idea",
  displayName: "New Idea",
  description: "Triggers when a new idea or feedback entry is created in Frill.",
  aiMetadata: {
    description: "Fires when a new idea or feedback entry is created in Frill (polled). Represents fresh user-submitted feedback or feature requests entering the board."
  },
  props: {},
  sampleData: {
    idx: "idea_abc123",
    name: "Dark Mode Support",
    slug: "dark-mode-support",
    excerpt: "Please add a dark mode to the dashboard.",
    content_html: "<p>Please add a dark mode to the dashboard.</p>",
    content_markdown: "Please add a dark mode to the dashboard.",
    vote_count: 42,
    comment_count: 5,
    is_pined: !1,
    is_bug: !1,
    is_archived: !1,
    is_completed: !1,
    show_in_roadmap: !0,
    approval_status: "approved",
    created_at: "2024-01-15T10:30:00Z",
    updated_at: "2024-01-15T10:30:00Z",
    author_name: "Jane Doe",
    author_email: "jane@example.com",
    status_name: "Planned",
    status_color: "#3b82f6",
    topics: "UI, Feature Request"
  },
  type: import_pieces_framework11.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common9.pollingHelper.test(polling, context);
  },
  async onEnable(context) {
    await import_pieces_common9.pollingHelper.onEnable(polling, context);
  },
  async onDisable(context) {
    await import_pieces_common9.pollingHelper.onDisable(polling, context);
  },
  async run(context) {
    return await import_pieces_common9.pollingHelper.poll(polling, context);
  }
});

// ../ap/packages/pieces/community/frill/src/lib/triggers/new-comment.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var polling2 = {
  strategy: import_pieces_common10.DedupeStrategy.TIMEBASED,
  items: async ({ auth }) => (await frillApiCall({
    token: auth.secret_text,
    method: import_pieces_common10.HttpMethod.GET,
    path: "/comments",
    queryParams: { limit: 100 }
  })).body.data.map((item) => {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k;
    return {
      epochMilliSeconds: new Date(item.created_at).getTime(),
      data: {
        idx: item.idx,
        message: (_a = item.message) != null ? _a : null,
        content_html: (_b = item.content_html) != null ? _b : null,
        content_markdown: (_c = item.content_markdown) != null ? _c : null,
        is_private: (_d = item.is_private) != null ? _d : !1,
        type: (_e = item.type) != null ? _e : null,
        created_at: item.created_at,
        updated_at: (_f = item.updated_at) != null ? _f : null,
        follower_name: (_h = (_g = item.follower) == null ? void 0 : _g.name) != null ? _h : null,
        follower_email: (_j = (_i = item.follower) == null ? void 0 : _i.email) != null ? _j : null,
        idea_id: (_k = item.idea_id) != null ? _k : null
      }
    };
  })
}, newCommentTrigger = (0, import_pieces_framework12.createTrigger)({
  auth: frillAuth,
  name: "new_comment",
  displayName: "New Comment",
  description: "Triggers when a new public comment or internal note is added in Frill.",
  aiMetadata: {
    description: "Fires when a new comment is added on any Frill idea (polled), covering both public comments and private internal notes. Represents new discussion activity on feedback."
  },
  props: {},
  sampleData: {
    idx: "cmt_xyz789",
    message: "Thanks for the suggestion, we are looking into it!",
    content_html: "<p>Thanks for the suggestion, we are looking into it!</p>",
    content_markdown: "Thanks for the suggestion, we are looking into it!",
    is_private: !1,
    type: "comment",
    created_at: "2024-01-15T12:00:00Z",
    updated_at: "2024-01-15T12:00:00Z",
    follower_name: "John Smith",
    follower_email: "john@example.com",
    idea_id: 12345
  },
  type: import_pieces_framework12.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common10.pollingHelper.test(polling2, context);
  },
  async onEnable(context) {
    await import_pieces_common10.pollingHelper.onEnable(polling2, context);
  },
  async onDisable(context) {
    await import_pieces_common10.pollingHelper.onDisable(polling2, context);
  },
  async run(context) {
    return await import_pieces_common10.pollingHelper.poll(polling2, context);
  }
});

// ../ap/packages/pieces/community/frill/src/index.ts
var frill = (0, import_pieces_framework13.createPiece)({
  displayName: "Frill",
  description: "Collect feedback, manage ideas, and publish announcements with Frill.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/frill.png",
  categories: [import_pieces_framework14.PieceCategory.CUSTOMER_SUPPORT],
  auth: frillAuth,
  authors: ["simonmann2", "onyedikachi-david"],
  actions: [
    createIdea,
    updateIdea,
    createComment,
    createAnnouncement,
    createFollower,
    updateFollower,
    getIdeas,
    getComments,
    (0, import_pieces_common11.createCustomApiCallAction)({
      baseUrl: () => "https://api.frill.co/v1",
      auth: frillAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [newIdeaTrigger, newCommentTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  frill,
  frillAuth
});
