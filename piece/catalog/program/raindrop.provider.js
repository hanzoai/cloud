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

// ../ap/packages/pieces/community/raindrop/src/index.ts
var index_exports = {};
__export(index_exports, {
  raindrop: () => raindrop,
  raindropAuth: () => raindropAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/raindrop/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), raindropAuth = import_pieces_framework.PieceAuth.OAuth2({
  required: !0,
  authUrl: "https://raindrop.io/oauth/authorize",
  tokenUrl: "https://raindrop.io/oauth/access_token",
  scope: [],
  extra: {}
});

// ../ap/packages/pieces/community/raindrop/src/lib/actions/create-raindrop.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/raindrop/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var BASE_URL = "https://api.raindrop.io/rest/v1", collectionDropdown = import_pieces_framework2.Property.Dropdown({
  displayName: "Collection",
  description: 'Select the collection to use. Leave empty to use "Unsorted" (default inbox).',
  refreshers: [],
  required: !1,
  auth: raindropAuth,
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first"
      };
    let accessToken = auth.access_token;
    try {
      let [rootResp, childResp] = await Promise.all([
        import_pieces_common.httpClient.sendRequest({
          method: import_pieces_common.HttpMethod.GET,
          url: `${BASE_URL}/collections`,
          headers: { Authorization: `Bearer ${accessToken}` }
        }),
        import_pieces_common.httpClient.sendRequest({
          method: import_pieces_common.HttpMethod.GET,
          url: `${BASE_URL}/collections/childrens`,
          headers: { Authorization: `Bearer ${accessToken}` }
        })
      ]), allCollections = [
        ...rootResp.body.items,
        ...childResp.body.items
      ];
      return {
        disabled: !1,
        options: [
          { label: "Unsorted", value: -1 },
          ...allCollections.map((c) => ({ label: c.title, value: c._id }))
        ]
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load collections. Check your connection."
      };
    }
  }
}), raindropCommons = {
  BASE_URL,
  collectionDropdown,
  flattenRaindrop
};
function flattenRaindrop({
  raindrop: raindrop2
}) {
  var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k;
  return {
    id: raindrop2._id,
    title: (_a = raindrop2.title) != null ? _a : null,
    excerpt: (_b = raindrop2.excerpt) != null ? _b : null,
    note: (_c = raindrop2.note) != null ? _c : null,
    link: (_d = raindrop2.link) != null ? _d : null,
    type: (_e = raindrop2.type) != null ? _e : null,
    tags: Array.isArray(raindrop2.tags) ? raindrop2.tags.join(", ") : null,
    important: (_f = raindrop2.important) != null ? _f : !1,
    collection_id: (_h = (_g = raindrop2.collection) == null ? void 0 : _g.$id) != null ? _h : null,
    cover: (_i = raindrop2.cover) != null ? _i : null,
    created_at: (_j = raindrop2.created) != null ? _j : null,
    updated_at: (_k = raindrop2.lastUpdate) != null ? _k : null
  };
}

// ../ap/packages/pieces/community/raindrop/src/lib/actions/create-raindrop.ts
var createRaindropAction = (0, import_pieces_framework3.createAction)({
  auth: raindropAuth,
  name: "create_raindrop",
  displayName: "Create Bookmark",
  description: "Saves a new bookmark to your Raindrop.io account",
  audience: "both",
  aiMetadata: {
    description: "Creates a new bookmark (raindrop) in Raindrop.io from a URL, optionally setting title, excerpt, note, tags, target collection, and favourite flag. Use to save a web page to a user's account. The URL is required; if no title is given Raindrop auto-extracts it from the page. Not idempotent: each call adds another bookmark, even for a URL already saved.",
    idempotent: !1
  },
  props: {
    link: import_pieces_framework3.Property.ShortText({
      displayName: "URL",
      description: "The URL of the page to bookmark",
      required: !0
    }),
    title: import_pieces_framework3.Property.ShortText({
      displayName: "Title",
      description: "Custom title for the bookmark. If left empty, Raindrop will extract it from the URL automatically.",
      required: !1
    }),
    excerpt: import_pieces_framework3.Property.LongText({
      displayName: "Excerpt",
      description: "Short description or summary of the page",
      required: !1
    }),
    note: import_pieces_framework3.Property.LongText({
      displayName: "Note",
      description: "Personal note to attach to this bookmark",
      required: !1
    }),
    collection_id: raindropCommons.collectionDropdown,
    tags: import_pieces_framework3.Property.Array({
      displayName: "Tags",
      description: "List of tags to organize the bookmark (press Enter to add each tag)",
      required: !1
    }),
    important: import_pieces_framework3.Property.Checkbox({
      displayName: "Mark as Favourite",
      description: "Mark this bookmark as important / favourite",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { link, title, excerpt, note, collection_id, tags, important } = context.propsValue, accessToken = context.auth.access_token, body = {
      link,
      pleaseParse: {}
    };
    title && (body.title = title), excerpt && (body.excerpt = excerpt), note && (body.note = note), collection_id != null && (body.collection = { $id: collection_id }), tags && tags.length > 0 && (body.tags = tags), important && (body.important = important);
    let response = await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: `${raindropCommons.BASE_URL}/raindrop`,
      headers: {
        Authorization: `Bearer ${accessToken}`,
        "Content-Type": "application/json"
      },
      body
    });
    return flattenResponse(response.body.item);
  }
});
function flattenResponse(item) {
  var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k;
  return {
    id: item._id,
    title: (_a = item.title) != null ? _a : null,
    excerpt: (_b = item.excerpt) != null ? _b : null,
    note: (_c = item.note) != null ? _c : null,
    link: (_d = item.link) != null ? _d : null,
    type: (_e = item.type) != null ? _e : null,
    tags: Array.isArray(item.tags) ? item.tags.join(", ") : null,
    important: (_f = item.important) != null ? _f : !1,
    collection_id: (_h = (_g = item.collection) == null ? void 0 : _g.$id) != null ? _h : null,
    cover: (_i = item.cover) != null ? _i : null,
    created_at: (_j = item.created) != null ? _j : null,
    updated_at: (_k = item.lastUpdate) != null ? _k : null
  };
}

// ../ap/packages/pieces/community/raindrop/src/lib/actions/get-raindrop.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var getRaindropAction = (0, import_pieces_framework4.createAction)({
  auth: raindropAuth,
  name: "get_raindrop",
  displayName: "Get Bookmark",
  description: "Retrieves the details of a bookmark by its ID",
  audience: "both",
  aiMetadata: {
    description: "Fetches a single Raindrop.io bookmark by its numeric ID. Use when you already have a bookmark ID and need its full details (title, link, tags, collection, etc.); to locate bookmarks by keyword or collection instead, use Find Bookmarks. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    raindrop_id: import_pieces_framework4.Property.ShortText({
      displayName: "Bookmark ID",
      description: "The numeric ID of the bookmark. You can find it in the URL when viewing the bookmark in Raindrop.io (e.g. raindrop.io/app/raindrop/123456789).",
      required: !0
    })
  },
  async run(context) {
    let { raindrop_id } = context.propsValue, accessToken = context.auth.access_token, response = await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: `${raindropCommons.BASE_URL}/raindrop/${raindrop_id}`,
      headers: { Authorization: `Bearer ${accessToken}` }
    });
    return flattenResponse2(response.body.item);
  }
});
function flattenResponse2(item) {
  var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k;
  return {
    id: item._id,
    title: (_a = item.title) != null ? _a : null,
    excerpt: (_b = item.excerpt) != null ? _b : null,
    note: (_c = item.note) != null ? _c : null,
    link: (_d = item.link) != null ? _d : null,
    type: (_e = item.type) != null ? _e : null,
    tags: Array.isArray(item.tags) ? item.tags.join(", ") : null,
    important: (_f = item.important) != null ? _f : !1,
    collection_id: (_h = (_g = item.collection) == null ? void 0 : _g.$id) != null ? _h : null,
    cover: (_i = item.cover) != null ? _i : null,
    created_at: (_j = item.created) != null ? _j : null,
    updated_at: (_k = item.lastUpdate) != null ? _k : null
  };
}

// ../ap/packages/pieces/community/raindrop/src/lib/actions/update-raindrop.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var updateRaindropAction = (0, import_pieces_framework5.createAction)({
  auth: raindropAuth,
  name: "update_raindrop",
  displayName: "Update Bookmark",
  description: "Updates the details of an existing bookmark",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing Raindrop.io bookmark identified by its numeric ID, changing any of title, excerpt, note, tags, collection, or favourite flag. Only supplied fields are changed; supplying tags replaces the existing tag list. Idempotent: repeating the same update yields the same final state.",
    idempotent: !0
  },
  props: {
    raindrop_id: import_pieces_framework5.Property.ShortText({
      displayName: "Bookmark ID",
      description: "The numeric ID of the bookmark to update. You can find it in the URL when viewing the bookmark in Raindrop.io (e.g. raindrop.io/app/raindrop/123456789).",
      required: !0
    }),
    title: import_pieces_framework5.Property.ShortText({
      displayName: "New Title",
      description: "Updated title for the bookmark",
      required: !1
    }),
    excerpt: import_pieces_framework5.Property.LongText({
      displayName: "New Excerpt",
      description: "Updated short description or summary of the page",
      required: !1
    }),
    note: import_pieces_framework5.Property.LongText({
      displayName: "New Note",
      description: "Updated personal note attached to the bookmark",
      required: !1
    }),
    collection_id: raindropCommons.collectionDropdown,
    tags: import_pieces_framework5.Property.Array({
      displayName: "Tags",
      description: "New list of tags (replaces existing tags). Leave empty to keep existing tags unchanged.",
      required: !1
    }),
    important: import_pieces_framework5.Property.Checkbox({
      displayName: "Mark as Favourite",
      description: "Mark or unmark this bookmark as important / favourite",
      required: !1
    })
  },
  async run(context) {
    let { raindrop_id, title, excerpt, note, collection_id, tags, important } = context.propsValue, accessToken = context.auth.access_token, body = {};
    title != null && (body.title = title), excerpt != null && (body.excerpt = excerpt), note != null && (body.note = note), collection_id != null && (body.collection = { $id: collection_id }), tags && tags.length > 0 && (body.tags = tags), important != null && (body.important = important);
    let response = await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.PUT,
      url: `${raindropCommons.BASE_URL}/raindrop/${raindrop_id}`,
      headers: {
        Authorization: `Bearer ${accessToken}`,
        "Content-Type": "application/json"
      },
      body
    });
    return flattenResponse3(response.body.item);
  }
});
function flattenResponse3(item) {
  var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k;
  return {
    id: item._id,
    title: (_a = item.title) != null ? _a : null,
    excerpt: (_b = item.excerpt) != null ? _b : null,
    note: (_c = item.note) != null ? _c : null,
    link: (_d = item.link) != null ? _d : null,
    type: (_e = item.type) != null ? _e : null,
    tags: Array.isArray(item.tags) ? item.tags.join(", ") : null,
    important: (_f = item.important) != null ? _f : !1,
    collection_id: (_h = (_g = item.collection) == null ? void 0 : _g.$id) != null ? _h : null,
    cover: (_i = item.cover) != null ? _i : null,
    created_at: (_j = item.created) != null ? _j : null,
    updated_at: (_k = item.lastUpdate) != null ? _k : null
  };
}

// ../ap/packages/pieces/community/raindrop/src/lib/actions/delete-raindrop.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var deleteRaindropAction = (0, import_pieces_framework6.createAction)({
  auth: raindropAuth,
  name: "delete_raindrop",
  displayName: "Delete Bookmark",
  description: "Moves a bookmark to the trash. To permanently delete it, empty the trash in Raindrop.io.",
  audience: "both",
  aiMetadata: {
    description: "Moves a Raindrop.io bookmark, identified by its numeric ID, to the trash (a soft delete; emptying the trash in Raindrop.io is a separate manual step). Use to remove a bookmark from a user's active collections. Not idempotent: a repeat call on an already-trashed or missing ID errors rather than no-ops.",
    idempotent: !1
  },
  props: {
    raindrop_id: import_pieces_framework6.Property.ShortText({
      displayName: "Bookmark ID",
      description: "The numeric ID of the bookmark to delete. You can find it in the URL when viewing the bookmark in Raindrop.io (e.g. raindrop.io/app/raindrop/123456789).",
      required: !0
    })
  },
  async run(context) {
    let { raindrop_id } = context.propsValue, accessToken = context.auth.access_token;
    return await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.DELETE,
      url: `${raindropCommons.BASE_URL}/raindrop/${raindrop_id}`,
      headers: { Authorization: `Bearer ${accessToken}` }
    }), { success: !0, deleted_id: raindrop_id };
  }
});

// ../ap/packages/pieces/community/raindrop/src/lib/actions/find-raindrops.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");
var findRaindropsAction = (0, import_pieces_framework7.createAction)({
  auth: raindropAuth,
  name: "find_raindrops",
  displayName: "Find Bookmarks",
  description: "Lists bookmarks from a collection, with optional keyword search",
  audience: "both",
  aiMetadata: {
    description: "Lists bookmarks from a Raindrop.io collection, sorted newest-first. Operates in two modes: with an empty search it returns all bookmarks in the collection, and with a search query it filters by keyword (supporting operators like #tag or type:article). Defaults to a collection covering all bookmarks when none is chosen; returns up to 50 per call. Use to discover bookmark IDs or browse saved content. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    collection_id: raindropCommons.collectionDropdown,
    search: import_pieces_framework7.Property.ShortText({
      displayName: "Search Query",
      description: "Filter bookmarks by keyword. Supports operators like #tag or type:article. Leave empty to list all bookmarks.",
      required: !1
    }),
    limit: import_pieces_framework7.Property.Number({
      displayName: "Limit",
      description: "Maximum number of bookmarks to return (max 50)",
      required: !1,
      defaultValue: 25
    })
  },
  async run(context) {
    let { collection_id, search, limit } = context.propsValue, accessToken = context.auth.access_token, collectionIdToUse = collection_id != null ? collection_id : 0, perpage = Math.min(limit != null ? limit : 25, 50), queryParams = {
      sort: "-created",
      perpage: String(perpage),
      page: "0"
    };
    return search && (queryParams.search = search), (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.GET,
      url: `${raindropCommons.BASE_URL}/raindrops/${collectionIdToUse}`,
      headers: { Authorization: `Bearer ${accessToken}` },
      queryParams
    })).body.items.map((item) => {
      var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k;
      return {
        id: item._id,
        title: (_a = item.title) != null ? _a : null,
        excerpt: (_b = item.excerpt) != null ? _b : null,
        note: (_c = item.note) != null ? _c : null,
        link: (_d = item.link) != null ? _d : null,
        type: (_e = item.type) != null ? _e : null,
        tags: Array.isArray(item.tags) ? item.tags.join(", ") : null,
        important: (_f = item.important) != null ? _f : !1,
        collection_id: (_h = (_g = item.collection) == null ? void 0 : _g.$id) != null ? _h : null,
        cover: (_i = item.cover) != null ? _i : null,
        created_at: (_j = item.created) != null ? _j : null,
        updated_at: (_k = item.lastUpdate) != null ? _k : null
      };
    });
  }
});

// ../ap/packages/pieces/community/raindrop/src/index.ts
var raindrop = (0, import_pieces_framework8.createPiece)({
  displayName: "Raindrop",
  description: "Bookmark manager to save, search, and organize content from the web",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/raindrop.png",
  categories: [import_pieces_framework9.PieceCategory.PRODUCTIVITY, import_pieces_framework9.PieceCategory.CONTENT_AND_FILES],
  auth: raindropAuth,
  authors: ["bs1tn"],
  actions: [
    createRaindropAction,
    getRaindropAction,
    updateRaindropAction,
    deleteRaindropAction,
    findRaindropsAction,
    (0, import_pieces_common7.createCustomApiCallAction)({
      baseUrl: () => "https://api.raindrop.io/rest/v1",
      auth: raindropAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.access_token}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  raindrop,
  raindropAuth
});
