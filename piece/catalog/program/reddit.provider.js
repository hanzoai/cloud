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

// ../ap/packages/pieces/community/reddit/src/index.ts
var index_exports = {};
__export(index_exports, {
  reddit: () => reddit
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common10 = require("@activepieces/pieces-common"), import_pieces_framework12 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/reddit/src/lib/actions/retrieve-reddit-post.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/reddit/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_framework2 = require("@activepieces/pieces-framework"), markdown = `
To obtain your Reddit API credentials:

1. Go to https://www.reddit.com/prefs/apps.
2. Click "create another app..." at the bottom.
3. Select "script" as the app type.
4. Fill in the required information:
   - name: Your app name
   - description: Brief description
   - about url: Can be left blank
   - redirect uri: as shown in Redirect URL field
5. Click "create app".
6. Note down the client ID (under the app name) and client secret.
`, redditAuth = import_pieces_framework.PieceAuth.OAuth2({
  description: markdown,
  authUrl: "https://www.reddit.com/api/v1/authorize",
  tokenUrl: "https://www.reddit.com/api/v1/access_token",
  required: !0,
  scope: ["identity", "read", "submit", "edit", "history", "flair"],
  authorizationMethod: import_pieces_framework.OAuth2AuthorizationMethod.HEADER,
  extra: {
    grantType: import_pieces_framework2.OAuth2GrantType.AUTHORIZATION_CODE,
    responseType: "code",
    duration: "permanent"
  }
});

// ../ap/packages/pieces/community/reddit/src/lib/actions/retrieve-reddit-post.ts
var retrieveRedditPost = (0, import_pieces_framework3.createAction)({
  auth: redditAuth,
  name: "retrieveRedditPost",
  classification: "SEARCH",
  displayName: "Retrieve Post",
  description: "Fetch top posts in a subreddit with optional size limit.",
  audience: "both",
  aiMetadata: { description: "Lists posts from a given subreddit, choosing the listing via a category (hot, new, top, rising, or controversial). Use it to read or monitor a subreddit's feed rather than to look up one known post. Requires the subreddit name; an optional size caps how many posts are returned (max 100). Read-only and idempotent.", idempotent: !0 },
  props: {
    post_category: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Post Category",
      description: "Select the category of posts to retrieve",
      required: !0,
      defaultValue: "hot",
      options: {
        options: [
          { label: "Hot", value: "hot" },
          { label: "New", value: "new" },
          { label: "Top", value: "top" },
          { label: "Rising", value: "rising" },
          { label: "Controversial", value: "controversial" }
        ]
      }
    }),
    subreddit: import_pieces_framework3.Property.ShortText({
      displayName: "Subreddit",
      description: "The subreddit to fetch posts from",
      required: !0
    }),
    size: import_pieces_framework3.Property.Number({
      displayName: "Number of Posts",
      description: "Number of posts to fetch (max 100)",
      required: !1,
      defaultValue: 10
    })
  },
  async run(context) {
    let baseUrl = `https://oauth.reddit.com/r/${context.propsValue.subreddit}/${context.propsValue.post_category}`, limit = context.propsValue.size || 10, response = await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: baseUrl,
      queryParams: {
        limit: limit.toString()
      },
      headers: {
        Authorization: `Bearer ${context.auth.access_token}`,
        "User-Agent": "ActivePieces Reddit Client",
        "Content-Type": "application/json"
      },
      timeout: 5e3
    });
    if (response.status !== 200)
      throw new Error(`Reddit API error: ${response.status} ${JSON.stringify(response.body)}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/reddit/src/lib/actions/get-reddit-post-details.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var getRedditPostDetails = (0, import_pieces_framework4.createAction)({
  auth: redditAuth,
  name: "getRedditPostDetails",
  classification: "READ",
  displayName: "Get Post Details",
  description: "Fetch detailed information about a specific Reddit post using its ID.",
  audience: "both",
  aiMetadata: { description: "Looks up one specific Reddit post by its ID and returns its full metadata (title, author, body, score, counts, flags). Use it when you already have a post ID and need details for that single post, not to browse a subreddit. Accepts the ID with or without the t3_ prefix. Read-only and idempotent.", idempotent: !0 },
  props: {
    post_id: import_pieces_framework4.Property.ShortText({
      displayName: "Post ID",
      description: 'The ID of the Reddit post (e.g. "t3_abc123" or "abc123")',
      required: !0
    })
  },
  async run(context) {
    var _a, _b, _c;
    let postId = context.propsValue.post_id.trim();
    postId.startsWith("t3_") && (postId = postId.slice(3));
    let url = `https://oauth.reddit.com/api/info?id=t3_${postId}`, response = await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.GET,
      url,
      headers: {
        Authorization: `Bearer ${context.auth.access_token}`,
        "User-Agent": "ActivePieces Reddit Client",
        "Content-Type": "application/json"
      },
      timeout: 5e3
    });
    if (response.status !== 200)
      return {
        error: `Failed to retrieve post details: ${response.status}`,
        details: response.body
      };
    let children = (_c = (_b = (_a = response.body) == null ? void 0 : _a.data) == null ? void 0 : _b.children) != null ? _c : [];
    if (children.length === 0)
      return { error: "No post found with the given ID" };
    let data = children[0].data, result = {
      id: data.id,
      title: data.title,
      author: data.author,
      author_fullname: data.author_fullname,
      subreddit: data.subreddit,
      subreddit_id: data.subreddit_id,
      selftext: data.selftext,
      selftext_html: data.selftext_html,
      score: data.score,
      upvote_ratio: data.upvote_ratio,
      created_utc: data.created_utc,
      permalink: data.permalink,
      url: data.url,
      domain: data.domain,
      num_comments: data.num_comments,
      is_self: data.is_self,
      is_video: data.is_video,
      is_original_content: data.is_original_content,
      over_18: data.over_18,
      spoiler: data.spoiler,
      locked: data.locked,
      stickied: data.stickied,
      post_hint: data.post_hint
    };
    return data.media && (result.media = data.media), data.gallery_data && (result.gallery_data = data.gallery_data), result;
  }
});

// ../ap/packages/pieces/community/reddit/src/lib/actions/create-reddit-post.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var createRedditPost = (0, import_pieces_framework5.createAction)({
  auth: redditAuth,
  name: "createRedditPost",
  classification: "WRITE",
  displayName: "Create Post",
  description: "Submit a new self (text) post to a subreddit.",
  audience: "both",
  aiMetadata: { description: "Submits a new self (text) post to a subreddit on behalf of the authenticated account. Use it to publish original text content; it cannot post links, images, or media. Requires the target subreddit, a title, and the text body. Not idempotent \u2014 each call creates a separate post.", idempotent: !1 },
  props: {
    subreddit: import_pieces_framework5.Property.ShortText({
      displayName: "Subreddit",
      description: "The subreddit to post in (without r/).",
      required: !0
    }),
    title: import_pieces_framework5.Property.ShortText({
      displayName: "Title",
      description: "Title of the Reddit post.",
      required: !0
    }),
    content: import_pieces_framework5.Property.LongText({
      displayName: "Content",
      description: "Text content of the post.",
      required: !0
    })
  },
  async run(context) {
    let { subreddit, title, content } = context.propsValue, url = "https://oauth.reddit.com/api/submit", payload = new URLSearchParams({
      api_type: "json",
      sr: subreddit,
      title,
      text: content,
      kind: "self"
    }), response = await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url,
      headers: {
        Authorization: `Bearer ${context.auth.access_token}`,
        "User-Agent": "ActivePieces Reddit Client",
        "Content-Type": "application/x-www-form-urlencoded"
      },
      body: payload.toString()
    });
    return response.status !== 200 ? {
      error: `Failed to create post: ${response.status}`,
      details: response.body
    } : response.body;
  }
});

// ../ap/packages/pieces/community/reddit/src/lib/actions/create-reddit-comment.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var createRedditComment = (0, import_pieces_framework6.createAction)({
  auth: redditAuth,
  name: "createRedditComment",
  classification: "WRITE",
  displayName: "Create Comment",
  description: "Comment on a Reddit post or reply to a comment.",
  audience: "both",
  aiMetadata: { description: "Posts a comment from the authenticated account, either as a top-level reply to a post or as a nested reply to another comment, determined by the parent ID type (t3_ for a post, t1_ for a comment). Requires the parent ID and the comment text. Not idempotent \u2014 each call creates a separate comment.", idempotent: !1 },
  props: {
    parent_id: import_pieces_framework6.Property.ShortText({
      displayName: "Parent ID",
      description: "ID of the post (t3_*) or comment (t1_*) to reply to.",
      required: !0
    }),
    content: import_pieces_framework6.Property.LongText({
      displayName: "Comment Text",
      description: "Text of the comment.",
      required: !0
    })
  },
  async run(context) {
    let parentId = context.propsValue.parent_id.trim();
    !parentId.startsWith("t1_") && !parentId.startsWith("t3_") && (parentId = `t3_${parentId}`);
    let url = "https://oauth.reddit.com/api/comment", payload = new URLSearchParams({
      thing_id: parentId,
      text: context.propsValue.content,
      api_type: "json"
    }), response = await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url,
      headers: {
        Authorization: `Bearer ${context.auth.access_token}`,
        "User-Agent": "ActivePieces Reddit Client",
        "Content-Type": "application/x-www-form-urlencoded"
      },
      body: payload.toString()
    });
    return response.status !== 200 ? {
      error: `Failed to create comment: ${response.status}`,
      details: response.body
    } : response.body;
  }
});

// ../ap/packages/pieces/community/reddit/src/lib/actions/fetch-post-comments.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var fetchPostComments = (0, import_pieces_framework7.createAction)({
  auth: redditAuth,
  name: "fetchPostComments",
  classification: "SEARCH",
  displayName: "Fetch Post Comments",
  description: "Fetch comments from a specific Reddit post.",
  audience: "both",
  aiMetadata: { description: "Retrieves the threaded comments of one Reddit post identified by its ID, including nested replies. Use it to read discussion on a known post; the sort option (new, top, hot, best, old, controversial) and a limit on top-level comments are configurable. Accepts the post ID with or without the t3_ prefix. Read-only and idempotent.", idempotent: !0 },
  props: {
    post_id: import_pieces_framework7.Property.ShortText({
      displayName: "Post ID",
      description: 'The ID of the Reddit post (e.g. "abc123" or "t3_abc123").',
      required: !0
    }),
    sort: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Sort By",
      description: "Sorting method for comments",
      defaultValue: "new",
      required: !1,
      options: {
        options: [
          { label: "New", value: "new" },
          { label: "Top", value: "top" },
          { label: "Hot", value: "hot" },
          { label: "Best", value: "best" },
          { label: "Old", value: "old" },
          { label: "Controversial", value: "controversial" }
        ]
      }
    }),
    limit: import_pieces_framework7.Property.Number({
      displayName: "Limit",
      description: "Maximum number of top-level comments to fetch",
      defaultValue: 10,
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e;
    let postId = context.propsValue.post_id.trim();
    postId.startsWith("t3_") && (postId = postId.slice(3));
    let response = await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.GET,
      url: `https://oauth.reddit.com/comments/${postId}`,
      headers: {
        Authorization: `Bearer ${context.auth.access_token}`,
        "User-Agent": "ActivePieces Reddit Client",
        "Content-Type": "application/json"
      },
      queryParams: {
        sort: (_a = context.propsValue.sort) != null ? _a : "new",
        limit: ((_b = context.propsValue.limit) != null ? _b : 10).toString()
      }
    });
    if (response.status !== 200)
      return {
        error: `Failed to retrieve comments: ${response.status}`,
        details: response.body
      };
    function processComments(comments) {
      return comments.filter((c) => c.kind === "t1").map((c) => {
        let d = c.data;
        return {
          id: d.id,
          author: d.author,
          body: d.body,
          score: d.score,
          created_utc: d.created_utc,
          permalink: d.permalink,
          edited: d.edited,
          is_submitter: d.is_submitter,
          stickied: d.stickied,
          replies: d.replies && typeof d.replies == "object" ? processComments(d.replies.data.children) : []
        };
      });
    }
    let commentsTree = (_e = (_d = (_c = response.body[1]) == null ? void 0 : _c.data) == null ? void 0 : _d.children) != null ? _e : [];
    return processComments(commentsTree);
  }
});

// ../ap/packages/pieces/community/reddit/src/lib/actions/edit-reddit-post.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var editRedditPost = (0, import_pieces_framework8.createAction)({
  auth: redditAuth,
  name: "editRedditPost",
  classification: "WRITE",
  displayName: "Edit Post",
  description: "Edits the content of an existing Reddit post.",
  audience: "both",
  aiMetadata: { description: "Replaces the text body of an existing self post owned by the authenticated account, identified by post ID. Use it to update a post you previously created; it only edits text posts, not link or media posts. Requires the post ID (with or without the t3_ prefix) and the new content. Idempotent \u2014 repeating with the same content leaves the post in the same state.", idempotent: !0 },
  props: {
    post_id: import_pieces_framework8.Property.ShortText({
      displayName: "Post ID",
      description: 'ID of the Reddit post to edit (e.g., "abc123" or "t3_abc123").',
      required: !0
    }),
    content: import_pieces_framework8.Property.LongText({
      displayName: "New Post Content",
      description: "Updated text content for the post.",
      required: !0
    })
  },
  async run(context) {
    let postId = context.propsValue.post_id.trim();
    postId.startsWith("t3_") && (postId = postId.slice(3));
    let url = "https://oauth.reddit.com/api/editusertext", payload = new URLSearchParams({
      api_type: "json",
      thing_id: `t3_${postId}`,
      text: context.propsValue.content
    }), response = await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.POST,
      url,
      headers: {
        Authorization: `Bearer ${context.auth.access_token}`,
        "User-Agent": "ActivePieces Reddit Client",
        "Content-Type": "application/x-www-form-urlencoded"
      },
      body: payload.toString()
    });
    return response.status !== 200 ? {
      error: `Failed to edit post: ${response.status}`,
      details: response.body
    } : response.body;
  }
});

// ../ap/packages/pieces/community/reddit/src/lib/actions/edit-reddit-comment.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var editRedditComment = (0, import_pieces_framework9.createAction)({
  auth: redditAuth,
  name: "editRedditComment",
  classification: "WRITE",
  displayName: "Edit Comment",
  description: "Edits the content of an existing Reddit comment.",
  audience: "both",
  aiMetadata: { description: "Replaces the text of an existing comment owned by the authenticated account, identified by comment ID. Use it to update a comment you previously posted. Requires the comment ID (with or without the t1_ prefix) and the new content. Idempotent \u2014 repeating with the same content leaves the comment in the same state.", idempotent: !0 },
  props: {
    comment_id: import_pieces_framework9.Property.ShortText({
      displayName: "Comment ID",
      description: 'ID of the Reddit comment to edit (e.g., "def456" or "t1_def456").',
      required: !0
    }),
    content: import_pieces_framework9.Property.LongText({
      displayName: "New Comment Content",
      description: "Updated text content for the comment.",
      required: !0
    })
  },
  async run(context) {
    let commentId = context.propsValue.comment_id.trim();
    commentId.startsWith("t1_") && (commentId = commentId.slice(3));
    let url = "https://oauth.reddit.com/api/editusertext", payload = new URLSearchParams({
      api_type: "json",
      thing_id: `t1_${commentId}`,
      text: context.propsValue.content
    }), response = await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.POST,
      url,
      headers: {
        Authorization: `Bearer ${context.auth.access_token}`,
        "User-Agent": "ActivePieces Reddit Client",
        "Content-Type": "application/x-www-form-urlencoded"
      },
      body: payload.toString()
    });
    return response.status !== 200 ? {
      error: `Failed to edit comment: ${response.status}`,
      details: response.body
    } : response.body;
  }
});

// ../ap/packages/pieces/community/reddit/src/lib/actions/delete-reddit-post.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var deleteRedditPost = (0, import_pieces_framework10.createAction)({
  auth: redditAuth,
  name: "deleteRedditPost",
  classification: "DESTRUCTIVE",
  displayName: "Delete Post",
  description: "Delete a specific Reddit post by ID.",
  audience: "both",
  aiMetadata: { description: "Deletes a post owned by the authenticated account, identified by post ID. Use it to permanently remove a post you previously created. Requires the post ID (with or without the t3_ prefix). Idempotent \u2014 once deleted, repeating the call leaves the same end state.", idempotent: !0 },
  props: {
    post_id: import_pieces_framework10.Property.ShortText({
      displayName: "Post ID",
      description: 'ID of the Reddit post to delete (e.g., "abc123" or "t3_abc123").',
      required: !0
    })
  },
  async run(context) {
    let postId = context.propsValue.post_id.trim();
    postId.startsWith("t3_") && (postId = postId.slice(3));
    let url = "https://oauth.reddit.com/api/del", payload = new URLSearchParams({
      api_type: "json",
      id: `t3_${postId}`
    }), response = await import_pieces_common8.httpClient.sendRequest({
      method: import_pieces_common8.HttpMethod.POST,
      url,
      headers: {
        Authorization: `Bearer ${context.auth.access_token}`,
        "User-Agent": "ActivePieces Reddit Client",
        "Content-Type": "application/x-www-form-urlencoded"
      },
      body: payload.toString()
    });
    return response.status !== 200 ? {
      error: `Failed to delete post: ${response.status}`,
      details: response.body
    } : { success: !0, response: response.body };
  }
});

// ../ap/packages/pieces/community/reddit/src/lib/actions/delete-reddit-comment.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var deleteRedditComment = (0, import_pieces_framework11.createAction)({
  auth: redditAuth,
  name: "deleteRedditComment",
  classification: "DESTRUCTIVE",
  displayName: "Delete Comment",
  description: "Delete a specific Reddit comment by ID.",
  audience: "both",
  aiMetadata: { description: "Deletes a comment owned by the authenticated account, identified by comment ID. Use it to permanently remove a comment you previously posted. Requires the comment ID (with or without the t1_ prefix). Idempotent \u2014 once deleted, repeating the call leaves the same end state.", idempotent: !0 },
  props: {
    comment_id: import_pieces_framework11.Property.ShortText({
      displayName: "Comment ID",
      description: 'ID of the Reddit comment to delete (e.g., "def456" or "t1_def456").',
      required: !0
    })
  },
  async run(context) {
    let commentId = context.propsValue.comment_id.trim();
    commentId.startsWith("t1_") && (commentId = commentId.slice(3));
    let url = "https://oauth.reddit.com/api/del", payload = new URLSearchParams({
      api_type: "json",
      id: `t1_${commentId}`
    }), response = await import_pieces_common9.httpClient.sendRequest({
      method: import_pieces_common9.HttpMethod.POST,
      url,
      headers: {
        Authorization: `Bearer ${context.auth.access_token}`,
        "User-Agent": "ActivePieces Reddit Client",
        "Content-Type": "application/x-www-form-urlencoded"
      },
      body: payload.toString()
    });
    return response.status !== 200 ? {
      error: `Failed to delete comment: ${response.status}`,
      details: response.body
    } : { success: !0, response: response.body };
  }
});

// ../ap/packages/pieces/community/reddit/src/index.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var reddit = (0, import_pieces_framework12.createPiece)({
  displayName: "Reddit",
  description: "Interact with Reddit - fetch and submit posts.",
  logoUrl: "https://cdn.activepieces.com/pieces/reddit.png",
  minimumSupportedRelease: "0.36.1",
  categories: [import_pieces_framework13.PieceCategory.COMMUNICATION],
  authors: ["bhaviksingla1403"],
  auth: redditAuth,
  actions: [
    retrieveRedditPost,
    getRedditPostDetails,
    createRedditPost,
    createRedditComment,
    fetchPostComments,
    editRedditPost,
    editRedditComment,
    deleteRedditPost,
    deleteRedditComment,
    (0, import_pieces_common10.createCustomApiCallAction)({
      auth: redditAuth,
      baseUrl: () => "https://oauth.reddit.com",
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.access_token}`,
        "User-Agent": "ActivePieces/1.0.0"
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  reddit
});
