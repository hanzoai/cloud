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

// ../ap/packages/pieces/community/klipy/src/index.ts
var index_exports = {};
__export(index_exports, {
  klipy: () => klipy,
  klipyAuth: () => klipyAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/klipy/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), klipyAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "App Key",
  description: `To get your App Key:
1. Sign in to your KLIPY account at https://klipy.com
2. Go to your dashboard and open **API Settings**
3. Copy your **App Key**

The App Key is a unique identifier issued by KLIPY for your application.`,
  required: !0
});

// ../ap/packages/pieces/community/klipy/src/lib/actions/search-gifs.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/klipy/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.klipy.com";
async function search({
  appKey,
  endpoint,
  query,
  page,
  perPage,
  customerId,
  locale,
  contentFilter,
  formatFilter
}) {
  let queryParams = {
    page: String(page),
    per_page: String(perPage)
  };
  return query && (queryParams.q = query), customerId && (queryParams.customer_id = customerId), locale && (queryParams.locale = locale), contentFilter && (queryParams.content_filter = contentFilter), formatFilter && (queryParams.format_filter = formatFilter), (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: `${BASE_URL}/api/v1/${appKey}/${endpoint}`,
    queryParams
  })).body;
}
function flattenGifSticker(item) {
  var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n, _o, _p, _q, _r, _s, _t, _u, _v, _w, _x, _y, _z, _A, _B, _C, _D;
  let hd = (_a = item.file) == null ? void 0 : _a.hd, sm = (_b = item.file) == null ? void 0 : _b.sm;
  return {
    id: item.id,
    slug: item.slug,
    title: item.title,
    type: item.type,
    tags: Array.isArray(item.tags) ? item.tags.join(", ") : "",
    hd_gif_url: (_d = (_c = hd == null ? void 0 : hd.gif) == null ? void 0 : _c.url) != null ? _d : null,
    hd_webp_url: (_f = (_e = hd == null ? void 0 : hd.webp) == null ? void 0 : _e.url) != null ? _f : null,
    hd_mp4_url: (_h = (_g = hd == null ? void 0 : hd.mp4) == null ? void 0 : _g.url) != null ? _h : null,
    hd_png_url: (_j = (_i = hd == null ? void 0 : hd.png) == null ? void 0 : _i.url) != null ? _j : null,
    hd_width: (_n = (_m = (_k = hd == null ? void 0 : hd.gif) == null ? void 0 : _k.width) != null ? _m : (_l = hd == null ? void 0 : hd.webp) == null ? void 0 : _l.width) != null ? _n : null,
    hd_height: (_r = (_q = (_o = hd == null ? void 0 : hd.gif) == null ? void 0 : _o.height) != null ? _q : (_p = hd == null ? void 0 : hd.webp) == null ? void 0 : _p.height) != null ? _r : null,
    sm_gif_url: (_t = (_s = sm == null ? void 0 : sm.gif) == null ? void 0 : _s.url) != null ? _t : null,
    sm_webp_url: (_v = (_u = sm == null ? void 0 : sm.webp) == null ? void 0 : _u.url) != null ? _v : null,
    sm_width: (_z = (_y = (_w = sm == null ? void 0 : sm.gif) == null ? void 0 : _w.width) != null ? _y : (_x = sm == null ? void 0 : sm.webp) == null ? void 0 : _x.width) != null ? _z : null,
    sm_height: (_D = (_C = (_A = sm == null ? void 0 : sm.gif) == null ? void 0 : _A.height) != null ? _C : (_B = sm == null ? void 0 : sm.webp) == null ? void 0 : _B.height) != null ? _D : null
  };
}
function flattenClip(item) {
  var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n, _o, _p, _q, _r;
  return {
    title: item.title,
    slug: item.slug,
    url: item.url,
    type: item.type,
    tags: Array.isArray(item.tags) ? item.tags.join(", ") : "",
    mp4_url: (_b = (_a = item.file) == null ? void 0 : _a.mp4) != null ? _b : null,
    gif_url: (_d = (_c = item.file) == null ? void 0 : _c.gif) != null ? _d : null,
    webp_url: (_f = (_e = item.file) == null ? void 0 : _e.webp) != null ? _f : null,
    mp4_width: (_i = (_h = (_g = item.file_meta) == null ? void 0 : _g.mp4) == null ? void 0 : _h.width) != null ? _i : null,
    mp4_height: (_l = (_k = (_j = item.file_meta) == null ? void 0 : _j.mp4) == null ? void 0 : _k.height) != null ? _l : null,
    gif_width: (_o = (_n = (_m = item.file_meta) == null ? void 0 : _m.gif) == null ? void 0 : _n.width) != null ? _o : null,
    gif_height: (_r = (_q = (_p = item.file_meta) == null ? void 0 : _p.gif) == null ? void 0 : _q.height) != null ? _r : null
  };
}
var klipyClient = { search, flattenGifSticker, flattenClip };

// ../ap/packages/pieces/community/klipy/src/lib/actions/search-gifs.ts
var searchGifsAction = (0, import_pieces_framework2.createAction)({
  auth: klipyAuth,
  name: "search_gifs",
  displayName: "Search GIFs",
  description: "Search and retrieve GIFs from KLIPY's database by keyword or phrase.",
  audience: "both",
  aiMetadata: { description: "Searches KLIPY's GIF library and returns a paginated set of matching GIFs. Use when an agent needs to find GIFs for a topic; leave the search keyword empty to browse trending GIFs instead of filtering to a query. Read-only and idempotent (same input returns the same page). Tune results with paging, locale, content-safety filter, and format filter.", idempotent: !0 },
  props: {
    q: import_pieces_framework2.Property.ShortText({
      displayName: "Search Keyword",
      description: 'The keyword or phrase to search for (e.g. "hello", "happy birthday"). Leave empty to browse trending GIFs.',
      required: !1
    }),
    page: import_pieces_framework2.Property.Number({
      displayName: "Page Number",
      description: "Which page of results to return. Starts at 1.",
      required: !1,
      defaultValue: 1
    }),
    per_page: import_pieces_framework2.Property.Number({
      displayName: "Results Per Page",
      description: "Number of GIFs to return per page. Must be between 8 and 50.",
      required: !1,
      defaultValue: 24
    }),
    locale: import_pieces_framework2.Property.ShortText({
      displayName: "Locale",
      description: 'ISO 3166-1 Alpha-2 country code to boost results for a specific region (e.g. "us", "uk", "de"). Leave empty for global results.',
      required: !1
    }),
    content_filter: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Content Filter",
      description: "Set the content safety level for the results.",
      required: !1,
      defaultValue: "medium",
      options: {
        options: [
          { label: "High (most strict)", value: "high" },
          { label: "Medium (recommended)", value: "medium" },
          { label: "Low", value: "low" },
          { label: "Off (no filtering)", value: "off" }
        ]
      }
    }),
    format_filter: import_pieces_framework2.Property.ShortText({
      displayName: "Format Filter",
      description: "Comma-separated list of formats to include in results. Possible values: gif, webp, jpg, mp4, webm. Leave empty to include all formats.",
      required: !1
    }),
    customer_id: import_pieces_framework2.Property.ShortText({
      displayName: "User ID",
      description: "A unique identifier for the end user in your system. Used to personalize results. Keep this consistent for the same user.",
      required: !1
    })
  },
  async run(context) {
    let { q, page, per_page, locale, content_filter, format_filter, customer_id } = context.propsValue, response = await klipyClient.search({
      appKey: context.auth.secret_text,
      endpoint: "gifs/search",
      query: q != null ? q : void 0,
      page: page != null ? page : 1,
      perPage: per_page != null ? per_page : 24,
      locale: locale != null ? locale : void 0,
      contentFilter: content_filter != null ? content_filter : void 0,
      formatFilter: format_filter != null ? format_filter : void 0,
      customerId: customer_id != null ? customer_id : void 0
    }), items = response.data.data.map(klipyClient.flattenGifSticker);
    return {
      current_page: response.data.current_page,
      per_page: response.data.per_page,
      has_next: response.data.has_next,
      items
    };
  }
});

// ../ap/packages/pieces/community/klipy/src/lib/actions/search-stickers.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var searchStickersAction = (0, import_pieces_framework3.createAction)({
  auth: klipyAuth,
  name: "search_stickers",
  displayName: "Search Stickers",
  description: "Search and retrieve stickers from KLIPY's database by keyword or phrase.",
  audience: "both",
  aiMetadata: { description: "Searches KLIPY's sticker library and returns a paginated set of matching stickers. Use when an agent needs to find stickers for a topic; leave the search keyword empty to browse trending stickers instead of filtering to a query. Read-only and idempotent (same input returns the same page). Tune results with paging, locale, content-safety filter, and format filter.", idempotent: !0 },
  props: {
    q: import_pieces_framework3.Property.ShortText({
      displayName: "Search Keyword",
      description: 'The keyword or phrase to search for (e.g. "love", "thumbs up"). Leave empty to browse trending stickers.',
      required: !1
    }),
    page: import_pieces_framework3.Property.Number({
      displayName: "Page Number",
      description: "Which page of results to return. Starts at 1.",
      required: !1,
      defaultValue: 1
    }),
    per_page: import_pieces_framework3.Property.Number({
      displayName: "Results Per Page",
      description: "Number of stickers to return per page. Must be between 8 and 50.",
      required: !1,
      defaultValue: 24
    }),
    locale: import_pieces_framework3.Property.ShortText({
      displayName: "Locale",
      description: 'ISO 3166-1 Alpha-2 country code to boost results for a specific region (e.g. "us", "uk", "de"). Leave empty for global results.',
      required: !1
    }),
    content_filter: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Content Filter",
      description: "Set the content safety level for the results.",
      required: !1,
      defaultValue: "medium",
      options: {
        options: [
          { label: "High (most strict)", value: "high" },
          { label: "Medium (recommended)", value: "medium" },
          { label: "Low", value: "low" },
          { label: "Off (no filtering)", value: "off" }
        ]
      }
    }),
    format_filter: import_pieces_framework3.Property.ShortText({
      displayName: "Format Filter",
      description: "Comma-separated list of formats to include in results. Possible values: gif, webp, png, webm. Leave empty to include all formats.",
      required: !1
    }),
    customer_id: import_pieces_framework3.Property.ShortText({
      displayName: "User ID",
      description: "A unique identifier for the end user in your system. Used to personalize results. Keep this consistent for the same user.",
      required: !1
    })
  },
  async run(context) {
    let { q, page, per_page, locale, content_filter, format_filter, customer_id } = context.propsValue, response = await klipyClient.search({
      appKey: context.auth.secret_text,
      endpoint: "stickers/search",
      query: q != null ? q : void 0,
      page: page != null ? page : 1,
      perPage: per_page != null ? per_page : 24,
      locale: locale != null ? locale : void 0,
      contentFilter: content_filter != null ? content_filter : void 0,
      formatFilter: format_filter != null ? format_filter : void 0,
      customerId: customer_id != null ? customer_id : void 0
    }), items = response.data.data.map(klipyClient.flattenGifSticker);
    return {
      current_page: response.data.current_page,
      per_page: response.data.per_page,
      has_next: response.data.has_next,
      items
    };
  }
});

// ../ap/packages/pieces/community/klipy/src/lib/actions/search-clips.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var searchClipsAction = (0, import_pieces_framework4.createAction)({
  auth: klipyAuth,
  name: "search_clips",
  displayName: "Search Clips",
  description: "Search and retrieve video clips from KLIPY's database by keyword or phrase.",
  audience: "both",
  aiMetadata: { description: "Searches KLIPY's video clip library and returns a paginated set of matching clips. Use when an agent needs to find short video clips for a topic; leave the search keyword empty to browse trending clips instead of filtering to a query. Read-only and idempotent (same input returns the same page). Tune results with paging, locale, and content-safety filter.", idempotent: !0 },
  props: {
    q: import_pieces_framework4.Property.ShortText({
      displayName: "Search Keyword",
      description: 'The keyword or phrase to search for (e.g. "funny cat", "celebration"). Leave empty to browse trending clips.',
      required: !1
    }),
    page: import_pieces_framework4.Property.Number({
      displayName: "Page Number",
      description: "Which page of results to return. Starts at 1.",
      required: !1,
      defaultValue: 1
    }),
    per_page: import_pieces_framework4.Property.Number({
      displayName: "Results Per Page",
      description: "Number of clips to return per page. Must be between 8 and 50.",
      required: !1,
      defaultValue: 24
    }),
    locale: import_pieces_framework4.Property.ShortText({
      displayName: "Locale",
      description: 'ISO 3166-1 Alpha-2 country code to boost results for a specific region (e.g. "us", "uk", "de"). Leave empty for global results.',
      required: !1
    }),
    content_filter: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Content Filter",
      description: "Set the content safety level for the results.",
      required: !1,
      defaultValue: "medium",
      options: {
        options: [
          { label: "High (most strict)", value: "high" },
          { label: "Medium (recommended)", value: "medium" },
          { label: "Low", value: "low" },
          { label: "Off (no filtering)", value: "off" }
        ]
      }
    }),
    customer_id: import_pieces_framework4.Property.ShortText({
      displayName: "User ID",
      description: "A unique identifier for the end user in your system. Used to personalize results. Keep this consistent for the same user.",
      required: !1
    })
  },
  async run(context) {
    let { q, page, per_page, locale, content_filter, customer_id } = context.propsValue, response = await klipyClient.search({
      appKey: context.auth.secret_text,
      endpoint: "clips/search",
      query: q != null ? q : void 0,
      page: page != null ? page : 1,
      perPage: per_page != null ? per_page : 24,
      locale: locale != null ? locale : void 0,
      contentFilter: content_filter != null ? content_filter : void 0,
      customerId: customer_id != null ? customer_id : void 0
    }), items = response.data.data.map(klipyClient.flattenClip);
    return {
      current_page: response.data.current_page,
      per_page: response.data.per_page,
      has_next: response.data.has_next,
      items
    };
  }
});

// ../ap/packages/pieces/community/klipy/src/index.ts
var klipy = (0, import_pieces_framework5.createPiece)({
  displayName: "Klipy",
  description: "Search and retrieve GIFs, stickers, and video clips from KLIPY's media library.",
  auth: klipyAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/klipy.png",
  categories: [import_pieces_framework6.PieceCategory.CONTENT_AND_FILES],
  authors: ["sanket-a11y"],
  actions: [
    searchGifsAction,
    searchStickersAction,
    searchClipsAction,
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: (auth) => `https://api.klipy.com/api/v1/${auth == null ? void 0 : auth.secret_text}`,
      auth: klipyAuth
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  klipy,
  klipyAuth
});
