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

// ../ap/packages/pieces/community/readwise/src/index.ts
var index_exports = {};
__export(index_exports, {
  readwise: () => readwise,
  readwiseAuth: () => readwiseAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/readwise/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/readwise/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), READWISE_BASE_URL = "https://readwise.io/api/v2";
async function makeReadwiseRequest({
  token,
  method,
  endpoint,
  body,
  params
}) {
  let url = new URL(`${READWISE_BASE_URL}${endpoint}`);
  return params && Object.entries(params).forEach(([k, v]) => url.searchParams.set(k, v)), (await import_pieces_common.httpClient.sendRequest({
    method,
    url: url.toString(),
    headers: {
      Authorization: `Token ${token}`,
      "Content-Type": "application/json"
    },
    body
  })).body;
}

// ../ap/packages/pieces/community/readwise/src/lib/common/auth.ts
var readwiseAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Access Token",
  description: "Your Readwise access token. Get it at https://readwise.io/access_token",
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await makeReadwiseRequest({
        token: auth,
        method: import_pieces_common2.HttpMethod.GET,
        endpoint: "/auth/"
      }), { valid: !0 };
    } catch (e) {
      return { valid: !1, error: "Invalid Readwise access token." };
    }
  }
});

// ../ap/packages/pieces/community/readwise/src/lib/actions/get-highlights.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/readwise/src/lib/common/props.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var bookId = ({ required = !1 } = {}) => import_pieces_framework2.Property.Dropdown({
  displayName: "Book",
  description: "The book or article to filter highlights by.",
  auth: readwiseAuth,
  required,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your Readwise account first."
      };
    try {
      let response = await makeReadwiseRequest({
        token: auth.secret_text,
        method: import_pieces_common3.HttpMethod.GET,
        endpoint: "/books/",
        params: { page_size: "1000" }
      });
      return response.results.length === 0 ? {
        disabled: !1,
        options: [],
        placeholder: "No books found in your Readwise library."
      } : {
        disabled: !1,
        options: response.results.map((book) => ({
          label: book.author ? `${book.title} \u2014 ${book.author}` : book.title,
          value: String(book.id)
        }))
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Could not load books. Check your connection."
      };
    }
  }
}), readwiseProps = {
  bookId
};

// ../ap/packages/pieces/community/readwise/src/lib/actions/get-highlights.ts
var getHighlights = (0, import_pieces_framework3.createAction)({
  name: "get_highlights",
  displayName: "Get Highlights",
  description: "Retrieve your Readwise highlights, optionally filtered by book or date.",
  audience: "both",
  aiMetadata: {
    description: "Lists highlights from the connected Readwise account. With no filters it returns all highlights (paginated); set book_id to scope to one source or updated_after to fetch only highlights changed since a timestamp. Use when an agent needs to read or search existing highlights. Read-only and idempotent.",
    idempotent: !0
  },
  auth: readwiseAuth,
  props: {
    book_id: readwiseProps.bookId({ required: !1 }),
    updated_after: import_pieces_framework3.Property.DateTime({
      displayName: "Updated After",
      description: "Return only highlights updated after this date.",
      required: !1
    }),
    page_size: import_pieces_framework3.Property.Number({
      displayName: "Page Size",
      description: "Number of highlights to return per page (max 1000).",
      required: !1,
      defaultValue: 100
    }),
    page: import_pieces_framework3.Property.Number({
      displayName: "Page",
      description: "Which page of results to return (starts at 1).",
      required: !1
    })
  },
  async run(context) {
    let token = context.auth.secret_text, { book_id, updated_after, page_size, page } = context.propsValue, params = {
      page_size: String(page_size != null ? page_size : 100)
    };
    return book_id && (params.book_id = book_id), updated_after && (params.updated__gt = new Date(updated_after).toISOString()), page && (params.page = String(page)), makeReadwiseRequest({
      token,
      method: import_pieces_common4.HttpMethod.GET,
      endpoint: "/highlights/",
      params
    });
  }
});

// ../ap/packages/pieces/community/readwise/src/lib/actions/create-highlight.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var createHighlight = (0, import_pieces_framework4.createAction)({
  name: "create_highlight",
  displayName: "Create Highlight",
  description: "Save a new highlight to Readwise.",
  audience: "both",
  aiMetadata: {
    description: "Saves a new highlight to the connected Readwise account, optionally attaching source metadata (title, author, URL, category) and a note. Use when an agent needs to capture a passage or quote into Readwise. Only the highlight text is required; not idempotent \u2014 each call appends a new highlight.",
    idempotent: !1
  },
  auth: readwiseAuth,
  props: {
    text: import_pieces_framework4.Property.LongText({
      displayName: "Highlight Text",
      description: "The text you want to save as a highlight.",
      required: !0
    }),
    title: import_pieces_framework4.Property.ShortText({
      displayName: "Source Title",
      description: "Title of the book, article, or source.",
      required: !1
    }),
    author: import_pieces_framework4.Property.ShortText({
      displayName: "Author",
      description: "Author of the source.",
      required: !1
    }),
    source_url: import_pieces_framework4.Property.ShortText({
      displayName: "Source URL",
      description: "URL of the article or webpage.",
      required: !1
    }),
    source_type: import_pieces_framework4.Property.ShortText({
      displayName: "Source Type",
      description: 'An identifier for your app/source, e.g. "my_app" (3\u201364 chars, no spaces).',
      required: !1
    }),
    category: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Category",
      description: "Type of source.",
      required: !1,
      defaultValue: "articles",
      options: {
        options: [
          { label: "Articles", value: "articles" },
          { label: "Books", value: "books" },
          { label: "Tweets", value: "tweets" },
          { label: "Podcasts", value: "podcasts" }
        ]
      }
    }),
    note: import_pieces_framework4.Property.LongText({
      displayName: "Note",
      description: "Optional note to attach to the highlight.",
      required: !1
    }),
    location: import_pieces_framework4.Property.Number({
      displayName: "Location",
      description: "Position of the highlight in the source.",
      required: !1
    }),
    location_type: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Location Type",
      description: "How the location above is measured.",
      required: !1,
      options: {
        options: [
          { label: "Page", value: "page" },
          { label: "Location", value: "location" },
          { label: "Order", value: "order" },
          { label: "Offset", value: "offset" },
          { label: "Time Offset", value: "time_offset" },
          { label: "None", value: "none" }
        ]
      }
    }),
    image_url: import_pieces_framework4.Property.ShortText({
      displayName: "Image URL",
      description: "Cover image for the source.",
      required: !1
    }),
    highlight_url: import_pieces_framework4.Property.ShortText({
      displayName: "Highlight URL",
      description: "A unique URL that links directly to this highlight.",
      required: !1
    }),
    highlighted_at: import_pieces_framework4.Property.DateTime({
      displayName: "Highlighted At",
      description: "Date when the highlight was made (defaults to now).",
      required: !1
    })
  },
  async run(context) {
    let p = context.propsValue, highlight = { text: p.text };
    return p.title && (highlight.title = p.title), p.author && (highlight.author = p.author), p.source_url && (highlight.source_url = p.source_url), p.source_type && (highlight.source_type = p.source_type), p.category && (highlight.category = p.category), p.note && (highlight.note = p.note), p.location !== void 0 && p.location !== null && (highlight.location = p.location), p.location_type && (highlight.location_type = p.location_type), p.image_url && (highlight.image_url = p.image_url), p.highlight_url && (highlight.highlight_url = p.highlight_url), p.highlighted_at && (highlight.highlighted_at = new Date(p.highlighted_at).toISOString()), makeReadwiseRequest({
      token: context.auth.secret_text,
      method: import_pieces_common5.HttpMethod.POST,
      endpoint: "/highlights/",
      body: { highlights: [highlight] }
    });
  }
});

// ../ap/packages/pieces/community/readwise/src/lib/triggers/new-highlight.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var polling = {
  strategy: import_pieces_common6.DedupeStrategy.TIMEBASED,
  items: async ({ auth, lastFetchEpochMS }) => {
    let params = { page_size: "1000" };
    return lastFetchEpochMS && (params.updated__gt = new Date(lastFetchEpochMS).toISOString()), (await makeReadwiseRequest({
      token: auth.secret_text,
      method: import_pieces_common6.HttpMethod.GET,
      endpoint: "/highlights/",
      params
    })).results.map((highlight) => ({
      epochMilliSeconds: new Date(highlight.created_at).getTime(),
      data: highlight
    }));
  }
}, newHighlight = (0, import_pieces_framework5.createTrigger)({
  name: "new_highlight",
  displayName: "New Highlight",
  description: "Fires when a new highlight is saved to Readwise.",
  aiMetadata: {
    description: "Fires when a new highlight is created in the connected Readwise account, emitting that highlight. Polls Readwise on an interval and surfaces each newly added highlight as a separate event."
  },
  auth: readwiseAuth,
  props: {},
  type: import_pieces_framework5.TriggerStrategy.POLLING,
  sampleData: {
    id: 12345678,
    text: "The best investment you can make is in yourself.",
    note: "",
    location: 1,
    location_type: "order",
    color: "yellow",
    highlighted_at: "2026-04-17T00:00:00.000Z",
    created_at: "2026-04-17T00:00:00.000Z",
    updated: "2026-04-17T00:00:00.000Z",
    external_id: null,
    end_location: null,
    url: null,
    book_id: 9876543,
    tags: [],
    readwise_url: "https://readwise.io/open/12345678"
  },
  async onEnable(context) {
    await import_pieces_common6.pollingHelper.onEnable(polling, context);
  },
  async onDisable(context) {
    await import_pieces_common6.pollingHelper.onDisable(polling, context);
  },
  async test(context) {
    return await import_pieces_common6.pollingHelper.test(polling, context);
  },
  async run(context) {
    return await import_pieces_common6.pollingHelper.poll(polling, context);
  }
});

// ../ap/packages/pieces/community/readwise/src/index.ts
var readwise = (0, import_pieces_framework6.createPiece)({
  displayName: "Readwise",
  description: "Save and retrieve your highlights from Readwise \u2014 your reading notes, all in one place.",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/readwise.png",
  categories: [import_pieces_framework7.PieceCategory.PRODUCTIVITY],
  authors: ["tosh2308"],
  auth: readwiseAuth,
  actions: [
    getHighlights,
    createHighlight,
    (0, import_pieces_common7.createCustomApiCallAction)({
      baseUrl: () => READWISE_BASE_URL,
      auth: readwiseAuth,
      authMapping: async (auth) => ({
        Authorization: `Token ${auth.secret_text}`
      })
    })
  ],
  triggers: [newHighlight]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  readwise,
  readwiseAuth
});
