"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
};
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

// ../ap/packages/pieces/community/canny/src/index.ts
var index_exports = {};
__export(index_exports, {
  canny: () => canny
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_framework11 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/canny/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/canny/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
var CANNY_API_BASE = "https://canny.io/api/v1";
async function cannyRequest({
  apiKey,
  path,
  body
}) {
  return (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.POST,
    url: `${CANNY_API_BASE}${path}`,
    headers: {
      "Content-Type": "application/json"
    },
    body: __spreadValues({
      apiKey: apiKey.trim()
    }, body)
  })).body;
}
function cleanBody(obj) {
  return Object.fromEntries(
    Object.entries(obj).filter(
      ([, v]) => v != null && v !== ""
    )
  );
}

// ../ap/packages/pieces/community/canny/src/lib/auth.ts
var cannyAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Your Canny secret API key. Find it at Settings \u2192 API in your Canny admin dashboard.",
  required: !0,
  validate: async ({ auth }) => {
    try {
      let response = await cannyRequest({
        apiKey: auth,
        path: "/boards/list"
      });
      return Array.isArray(response.boards) ? { valid: !0 } : {
        valid: !1,
        error: "Unexpected response from Canny API. Check your API key."
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API key or could not reach the Canny API."
      };
    }
  }
});

// ../ap/packages/pieces/community/canny/src/lib/actions/create-post.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/canny/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var boardIdProp = import_pieces_framework2.Property.Dropdown({
  auth: cannyAuth,
  displayName: "Board",
  description: "The board to use.",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    return auth ? {
      disabled: !1,
      options: ((_a = (await cannyRequest({
        apiKey: auth.secret_text,
        path: "/boards/list"
      })).boards) != null ? _a : []).map((b) => ({
        label: b.name,
        value: b.id
      }))
    } : {
      disabled: !0,
      placeholder: "Connect your Canny account first.",
      options: []
    };
  }
});

// ../ap/packages/pieces/community/canny/src/lib/actions/create-post.ts
var createPostAction = (0, import_pieces_framework3.createAction)({
  auth: cannyAuth,
  name: "create_post",
  classification: "WRITE",
  displayName: "Create Post",
  description: "Creates a new post on a Canny board.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new feedback post (feature request, bug, etc.) on a Canny board. Use to submit feedback captured elsewhere into Canny. Requires a board ID, an author Canny user ID, a title, and details; creates a distinct post on each call (not idempotent).",
    idempotent: !1
  },
  props: {
    boardID: boardIdProp,
    authorID: import_pieces_framework3.Property.ShortText({
      displayName: "Author ID",
      description: "The unique identifier of the post author (Canny user ID).",
      required: !0
    }),
    title: import_pieces_framework3.Property.ShortText({
      displayName: "Title",
      description: "A brief title describing the post.",
      required: !0
    }),
    details: import_pieces_framework3.Property.LongText({
      displayName: "Details",
      description: "The longer description of the post.",
      required: !0
    }),
    byID: import_pieces_framework3.Property.ShortText({
      displayName: "By ID",
      description: "The unique identifier of the admin creating the post on behalf of the author.",
      required: !1
    }),
    categoryID: import_pieces_framework3.Property.ShortText({
      displayName: "Category ID",
      description: "The unique identifier of the category to assign.",
      required: !1
    }),
    ownerID: import_pieces_framework3.Property.ShortText({
      displayName: "Owner ID",
      description: "The unique identifier of the user responsible for completing the work.",
      required: !1
    }),
    eta: import_pieces_framework3.Property.ShortText({
      displayName: "ETA",
      description: 'The estimated delivery date in MM/YYYY format (e.g. "06/2026").',
      required: !1
    }),
    etaPublic: import_pieces_framework3.Property.Checkbox({
      displayName: "ETA Public",
      description: "Whether the ETA should be visible to all users.",
      required: !1
    }),
    customFields: import_pieces_framework3.Property.Object({
      displayName: "Custom Fields",
      description: "Custom fields as key-value pairs (keys max 30 chars, values max 200 chars).",
      required: !1
    }),
    createdAt: import_pieces_framework3.Property.ShortText({
      displayName: "Created At",
      description: "Original creation date in ISO 8601 format, for migrating posts from another source.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    return await cannyRequest({
      apiKey: auth.secret_text,
      path: "/posts/create",
      body: cleanBody({
        authorID: propsValue.authorID,
        boardID: propsValue.boardID,
        title: propsValue.title,
        details: propsValue.details,
        byID: propsValue.byID,
        categoryID: propsValue.categoryID,
        ownerID: propsValue.ownerID,
        eta: propsValue.eta,
        etaPublic: propsValue.etaPublic,
        customFields: propsValue.customFields,
        createdAt: propsValue.createdAt
      })
    });
  }
});

// ../ap/packages/pieces/community/canny/src/lib/actions/retrieve-post.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var retrievePostAction = (0, import_pieces_framework4.createAction)({
  auth: cannyAuth,
  name: "retrieve_post",
  classification: "READ",
  displayName: "Retrieve Post",
  description: "Retrieves the details of an existing post by its ID.",
  audience: "both",
  aiMetadata: {
    description: "Looks up a single Canny post by its unique post ID and returns its full details. Use when you already have a post ID and need its current data (status, score, author, board). Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    id: import_pieces_framework4.Property.ShortText({
      displayName: "Post ID",
      description: "The unique identifier of the post.",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    return await cannyRequest({
      apiKey: auth.secret_text,
      path: "/posts/retrieve",
      body: { id: propsValue.id }
    });
  }
});

// ../ap/packages/pieces/community/canny/src/lib/actions/list-posts.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var listPostsAction = (0, import_pieces_framework5.createAction)({
  auth: cannyAuth,
  name: "list_posts",
  classification: "SEARCH",
  displayName: "List Posts",
  description: "Returns a list of posts for a board, with optional filtering and sorting.",
  audience: "both",
  aiMetadata: {
    description: "Lists Canny posts, optionally scoped to one board or queried across all boards when no board is given, with optional search text, status filter, sort order, and pagination (limit/skip). Use to discover or search posts rather than fetch one known post by ID. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    boardID: import_pieces_framework5.Property.Dropdown({
      auth: cannyAuth,
      displayName: "Board",
      description: "The board to list posts for (optional \u2014 omit to list across all boards).",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        var _a;
        return auth ? {
          disabled: !1,
          options: ((_a = (await cannyRequest({
            apiKey: auth.secret_text,
            path: "/boards/list"
          })).boards) != null ? _a : []).map((b) => ({
            label: b.name,
            value: b.id
          }))
        } : {
          disabled: !0,
          placeholder: "Connect your Canny account first.",
          options: []
        };
      }
    }),
    search: import_pieces_framework5.Property.ShortText({
      displayName: "Search",
      description: "A search query to filter posts.",
      required: !1
    }),
    sort: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Sort",
      description: "The order of returned posts.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Newest", value: "newest" },
          { label: "Oldest", value: "oldest" },
          { label: "Score", value: "score" },
          { label: "Trending", value: "trending" },
          { label: "Status Changed", value: "statusChanged" }
        ]
      }
    }),
    status: import_pieces_framework5.Property.ShortText({
      displayName: "Status",
      description: 'Comma-separated list of statuses to filter by (e.g. "open,planned").',
      required: !1
    }),
    limit: import_pieces_framework5.Property.Number({
      displayName: "Limit",
      description: "Number of posts to fetch (default 10).",
      required: !1
    }),
    skip: import_pieces_framework5.Property.Number({
      displayName: "Skip",
      description: "Number of posts to skip (for pagination).",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    return await cannyRequest({
      apiKey: auth.secret_text,
      path: "/posts/list",
      body: cleanBody({
        boardID: propsValue.boardID,
        search: propsValue.search,
        sort: propsValue.sort,
        status: propsValue.status,
        limit: propsValue.limit,
        skip: propsValue.skip
      })
    });
  }
});

// ../ap/packages/pieces/community/canny/src/lib/actions/create-vote.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var createVoteAction = (0, import_pieces_framework6.createAction)({
  auth: cannyAuth,
  name: "create_vote",
  classification: "WRITE",
  displayName: "Create Vote",
  description: "Casts a vote on a post on behalf of a user.",
  audience: "both",
  aiMetadata: {
    description: "Registers a vote on a Canny post on behalf of a specific user. Use to record support for a feature request when acting as that voter. Requires the post ID and the voter Canny user ID. Effectively idempotent: re-voting the same user on the same post does not add duplicate votes.",
    idempotent: !0
  },
  props: {
    postID: import_pieces_framework6.Property.ShortText({
      displayName: "Post ID",
      description: "The unique identifier of the post to vote on.",
      required: !0
    }),
    voterID: import_pieces_framework6.Property.ShortText({
      displayName: "Voter ID",
      description: "The unique identifier of the voter (Canny user ID).",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    return await cannyRequest({
      apiKey: auth.secret_text,
      path: "/votes/create",
      body: {
        postID: propsValue.postID,
        voterID: propsValue.voterID
      }
    });
  }
});

// ../ap/packages/pieces/community/canny/src/lib/actions/delete-vote.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var deleteVoteAction = (0, import_pieces_framework7.createAction)({
  auth: cannyAuth,
  name: "delete_vote",
  classification: "WRITE",
  displayName: "Delete Vote",
  description: "Removes a vote from a post for a given user.",
  audience: "both",
  aiMetadata: {
    description: "Removes a specific user's vote from a Canny post. Use to retract support previously registered for a post. Requires the post ID and the voter Canny user ID. Idempotent: removing an already-absent vote leaves the post in the same state.",
    idempotent: !0
  },
  props: {
    postID: import_pieces_framework7.Property.ShortText({
      displayName: "Post ID",
      description: "The unique identifier of the post to remove the vote from.",
      required: !0
    }),
    voterID: import_pieces_framework7.Property.ShortText({
      displayName: "Voter ID",
      description: "The unique identifier of the voter (Canny user ID).",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    return await cannyRequest({
      apiKey: auth.secret_text,
      path: "/votes/delete",
      body: {
        postID: propsValue.postID,
        voterID: propsValue.voterID
      }
    });
  }
});

// ../ap/packages/pieces/community/canny/src/lib/triggers/register-trigger.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_framework9 = require("@activepieces/pieces-framework");
function createCannyTrigger({
  name,
  displayName,
  description,
  aiMetadata,
  eventType,
  sampleData
}) {
  return (0, import_pieces_framework8.createTrigger)({
    auth: cannyAuth,
    name,
    classification: "READ",
    displayName,
    description,
    aiMetadata,
    props: {},
    sampleData,
    type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
    handshakeConfiguration: { strategy: import_pieces_framework9.WebhookHandshakeStrategy.NONE },
    async onEnable(_context) {
    },
    async onDisable(_context) {
    },
    async run(context) {
      let payload = context.payload.body;
      return (payload == null ? void 0 : payload.type) !== eventType ? [] : [payload];
    }
  });
}

// ../ap/packages/pieces/community/canny/src/lib/triggers/new-post.ts
var newPostTrigger = createCannyTrigger({
  name: "new_post",
  displayName: "New Post",
  description: "Triggers when a new post is created on a Canny board.",
  aiMetadata: {
    description: "Fires when a new feedback post is created on any Canny board, delivering the post with its board and author. Use to react to incoming feature requests or feedback as they are submitted."
  },
  eventType: "post.created",
  sampleData: {
    created: "2026-04-06T10:35:59.055Z",
    objectType: "post",
    type: "post.created",
    object: {
      id: "post123",
      title: "Sample feature request",
      details: "It would be great if the app had dark mode.",
      status: "open",
      score: 1,
      commentCount: 0,
      created: "2026-04-06T10:35:58.342Z",
      statusChangedAt: "2026-04-06T10:35:58.342Z",
      url: "https://yourcompany.canny.io/admin/board/feature-requests/p/sample-feature-request",
      eta: null,
      by: null,
      category: null,
      owner: null,
      tags: [],
      imageURLs: [],
      customFields: [],
      board: {
        id: "board123",
        name: "Feature Requests",
        postCount: 1,
        created: "2026-04-06T10:35:08.905Z",
        url: "https://yourcompany.canny.io/admin/board/feature-requests"
      },
      author: {
        id: "author123",
        name: "Jane Doe",
        email: "jane@example.com",
        isAdmin: !1,
        avatarURL: null,
        created: "2026-01-08T07:26:38.452Z",
        url: "https://yourcompany.canny.io/admin/users/jane-doe",
        userID: null
      }
    }
  }
});

// ../ap/packages/pieces/community/canny/src/lib/triggers/post-status-changed.ts
var postStatusChangedTrigger = createCannyTrigger({
  name: "post_status_changed",
  displayName: "Post Status Changed",
  description: "Triggers when a post's status is changed (e.g. open \u2192 in progress \u2192 complete).",
  aiMetadata: {
    description: "Fires when a Canny post moves to a different status (for example open to in progress to complete), delivering the post in its new state. Use to react to changes in how a feature request is progressing."
  },
  eventType: "post.status_changed",
  sampleData: {
    created: "2026-04-06T11:00:00.000Z",
    objectType: "post",
    type: "post.status_changed",
    object: {
      id: "post123",
      title: "Sample feature request",
      details: "It would be great if the app had dark mode.",
      status: "in progress",
      score: 5,
      commentCount: 2,
      created: "2026-04-06T10:35:58.342Z",
      statusChangedAt: "2026-04-06T11:00:00.000Z",
      url: "https://yourcompany.canny.io/admin/board/feature-requests/p/sample-feature-request",
      eta: null,
      by: null,
      category: null,
      owner: null,
      tags: [],
      imageURLs: [],
      customFields: [],
      board: {
        id: "board123",
        name: "Feature Requests",
        postCount: 5,
        created: "2026-04-06T10:35:08.905Z",
        url: "https://yourcompany.canny.io/admin/board/feature-requests"
      },
      author: {
        id: "author123",
        name: "Jane Doe",
        email: "jane@example.com",
        isAdmin: !1,
        avatarURL: null,
        created: "2026-01-08T07:26:38.452Z",
        url: "https://yourcompany.canny.io/admin/users/jane-doe",
        userID: null
      }
    }
  }
});

// ../ap/packages/pieces/community/canny/src/lib/triggers/new-comment.ts
var newCommentTrigger = createCannyTrigger({
  name: "new_comment",
  displayName: "New Comment",
  description: "Triggers when a new comment is created on a post.",
  aiMetadata: {
    description: "Fires when a new comment is added to any Canny post, delivering the comment along with its author and parent post. Use to react to discussion activity on feedback."
  },
  eventType: "comment.created",
  sampleData: {
    created: "2026-04-06T11:05:00.000Z",
    objectType: "comment",
    type: "comment.created",
    object: {
      id: "comment123",
      value: "This is a great idea! We really need this feature.",
      created: "2026-04-06T11:05:00.000Z",
      by: null,
      author: {
        id: "author456",
        name: "John Smith",
        email: "john@example.com",
        isAdmin: !1,
        avatarURL: null,
        created: "2026-01-08T07:26:38.452Z",
        url: "https://yourcompany.canny.io/admin/users/john-smith",
        userID: null
      },
      post: {
        id: "post123",
        title: "Sample feature request",
        status: "open",
        score: 5,
        created: "2026-04-06T10:35:58.342Z",
        url: "https://yourcompany.canny.io/admin/board/feature-requests/p/sample-feature-request",
        board: {
          id: "board123",
          name: "Feature Requests",
          postCount: 5,
          created: "2026-04-06T10:35:08.905Z",
          url: "https://yourcompany.canny.io/admin/board/feature-requests"
        }
      }
    }
  }
});

// ../ap/packages/pieces/community/canny/src/lib/triggers/new-vote.ts
var newVoteTrigger = createCannyTrigger({
  name: "new_vote",
  displayName: "New Vote",
  description: "Triggers when a user votes on a post.",
  aiMetadata: {
    description: "Fires when a user casts a vote on a Canny post, delivering the vote with its voter and the parent post. Use to react to changes in demand or support for feedback."
  },
  eventType: "vote.created",
  sampleData: {
    created: "2026-04-06T10:47:47.056Z",
    objectType: "vote",
    type: "vote.created",
    object: {
      id: "vote123",
      created: "2026-04-06T10:47:46.858Z",
      score: 1,
      votePriority: "No priority",
      by: null,
      voter: {
        id: "user123",
        name: "Jane Doe",
        email: "jane@example.com",
        isAdmin: !1,
        avatarURL: null,
        created: "2026-01-08T07:26:38.452Z",
        url: "https://yourcompany.canny.io/admin/users/jane-doe",
        userID: null
      },
      post: {
        id: "post123",
        title: "Sample feature request",
        details: "It would be great if the app had dark mode.",
        status: "open",
        score: 1,
        created: "2026-04-06T10:35:58.342Z",
        url: "https://yourcompany.canny.io/admin/board/feature-requests/p/sample-feature-request",
        board: {
          id: "board123",
          name: "Feature Requests",
          postCount: 1,
          created: "2026-04-06T10:35:08.905Z",
          url: "https://yourcompany.canny.io/admin/board/feature-requests"
        },
        author: {
          id: "author123",
          name: "John Smith",
          email: "john@example.com",
          isAdmin: !1,
          avatarURL: null,
          created: "2026-01-08T07:26:38.452Z",
          url: "https://yourcompany.canny.io/admin/users/john-smith",
          userID: null
        }
      }
    }
  }
});

// ../ap/packages/pieces/community/canny/src/index.ts
var canny = (0, import_pieces_framework10.createPiece)({
  displayName: "Canny",
  description: "Product feedback management platform. Collect, organize, and prioritize feature requests from your users.",
  auth: cannyAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/canny.png",
  authors: ["Harmatta", "sanket-a11y"],
  categories: [import_pieces_framework11.PieceCategory.PRODUCTIVITY],
  actions: [
    createPostAction,
    retrievePostAction,
    listPostsAction,
    createVoteAction,
    deleteVoteAction
    // createCustomApiCallAction({
    //   baseUrl: () => CANNY_API_BASE,
    //   auth: cannyAuth,
    //   authMapping: async (_auth) => ({
    //     'Content-Type': 'application/json',
    //   }),
    // }),
  ],
  triggers: [newPostTrigger, postStatusChangedTrigger, newCommentTrigger, newVoteTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  canny
});
