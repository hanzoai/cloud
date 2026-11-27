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

// ../ap/packages/pieces/community/beamer/src/index.ts
var index_exports = {};
__export(index_exports, {
  beamer: () => beamer
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/beamer/src/lib/actions/create-comment.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/beamer/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/beamer/src/lib/common/index.ts
var beamerCommon = {
  baseUrl: "https://api.getbeamer.com/v0"
};

// ../ap/packages/pieces/community/beamer/src/lib/auth.ts
var beamerAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "API key acquired from your Beamer settings",
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${beamerCommon.baseUrl}/ping`,
        headers: {
          "Beamer-Api-Key": `${auth}`
        }
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API key."
      };
    }
  }
});

// ../ap/packages/pieces/community/beamer/src/lib/actions/create-comment.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), createComment = (0, import_pieces_framework2.createAction)({
  auth: beamerAuth,
  name: "create_new_comment",
  classification: "WRITE",
  displayName: "Create a new comment",
  description: "Create a new comment for a Feature request",
  audience: "both",
  aiMetadata: {
    description: "Posts a comment on an existing Beamer feature request, identified by its numeric feature request ID. Optionally attributes the comment to a user via user ID or email. Use to add discussion or replies to a feedback item. Appends a new comment on each call (not idempotent).",
    idempotent: !1
  },
  props: {
    featureRequestId: import_pieces_framework2.Property.Number({
      displayName: "ID",
      description: "ID of the feature request",
      required: !0
    }),
    userId: import_pieces_framework2.Property.Number({
      displayName: "User ID",
      description: "id of the user making the comment",
      required: !1
    }),
    text: import_pieces_framework2.Property.LongText({
      displayName: "Text",
      description: "Content of your comment",
      required: !0
    }),
    userEmail: import_pieces_framework2.Property.ShortText({
      displayName: "User Email",
      description: "Feature requested by..",
      required: !1
    })
  },
  async run(context) {
    let apiKey = context.auth.secret_text, request = {
      method: import_pieces_common2.HttpMethod.POST,
      url: `${beamerCommon.baseUrl}/requests/${context.propsValue.featureRequestId}/comments`,
      headers: {
        "Beamer-Api-Key": `${apiKey}`,
        "Content-Type": "application/json"
      },
      body: {
        text: context.propsValue.text,
        userId: context.propsValue.userId,
        userEmail: context.propsValue.userEmail,
        visible: "true"
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(request)).body;
  }
});

// ../ap/packages/pieces/community/beamer/src/lib/actions/create-feature-request.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");
var createNewFeatureRequest = (0, import_pieces_framework3.createAction)({
  auth: beamerAuth,
  name: "create_new_feature_request",
  classification: "WRITE",
  displayName: "Create New Feature Request ",
  description: "Create New Feature Request",
  audience: "both",
  aiMetadata: {
    description: "Creates a feature request on a Beamer feedback board, attributed to a user by email, with a category (bug, feature request, or improvement), a status (under review, planned, in progress, or completed), and public or internal visibility. Use to log product feedback or roadmap items. Creates a new request on each call (not idempotent).",
    idempotent: !1
  },
  props: {
    title: import_pieces_framework3.Property.ShortText({
      displayName: "Title",
      description: "Enter the Title of the post",
      required: !0
    }),
    content: import_pieces_framework3.Property.LongText({
      displayName: "Description",
      description: "Enter the description of the post",
      required: !0
    }),
    visible: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Visibility",
      description: "Set visiblisty",
      required: !0,
      options: {
        options: [
          {
            label: "Public",
            value: !0
          },
          {
            label: "Internal",
            value: !1
          }
        ]
      }
    }),
    category: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Category",
      description: "Set Category",
      required: !0,
      options: {
        options: [
          {
            label: "Bug",
            value: "bug"
          },
          {
            label: "Feature Request",
            value: "feature_request"
          },
          {
            label: "Improvement",
            value: "improvement"
          }
        ]
      }
    }),
    status: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Status",
      description: "Set status",
      required: !0,
      options: {
        options: [
          {
            label: "Under Review",
            value: "under_review"
          },
          {
            label: "Planned",
            value: "planned"
          },
          {
            label: "In Progress",
            value: "in_progress"
          },
          {
            label: "Completed",
            value: "completed"
          }
        ]
      }
    }),
    requestedby: import_pieces_framework3.Property.ShortText({
      displayName: "User Email",
      description: "Feature requested by..",
      required: !0
    })
  },
  async run(context) {
    let apiKey = context.auth.secret_text, request = {
      method: import_pieces_common3.HttpMethod.POST,
      url: `${beamerCommon.baseUrl}/requests`,
      headers: {
        "Beamer-Api-Key": `${apiKey}`,
        "Content-Type": "application/json"
      },
      body: {
        title: [context.propsValue.title],
        content: [context.propsValue.content],
        visible: context.propsValue.visible,
        category: context.propsValue.category,
        status: context.propsValue.status,
        userEmail: context.propsValue.requestedby
      }
    };
    return (await import_pieces_common3.httpClient.sendRequest(request)).body;
  }
});

// ../ap/packages/pieces/community/beamer/src/lib/actions/create-posts.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var createBeamerPost = (0, import_pieces_framework4.createAction)({
  auth: beamerAuth,
  name: "create_beamer_post",
  classification: "WRITE",
  displayName: "Create Beamer Post ",
  description: "Create a new post in Beamer",
  audience: "both",
  aiMetadata: {
    description: "Publishes a new announcement post to a Beamer account, attributed to a user by email and tagged with a category (new, fix, coming soon, announcement, or improvement). Use to broadcast product news or changelog entries to users. Creates a distinct post on each call (not idempotent).",
    idempotent: !1
  },
  props: {
    title: import_pieces_framework4.Property.ShortText({
      displayName: "Title",
      description: "Enter the Title of the post",
      required: !0,
      defaultValue: "Post name"
    }),
    description: import_pieces_framework4.Property.LongText({
      displayName: "Description",
      description: "Enter the description of the post",
      required: !0,
      defaultValue: "desc..."
    }),
    category: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Category",
      description: "Set Category",
      required: !0,
      options: {
        options: [
          {
            label: "New",
            value: "new"
          },
          {
            label: "Fix",
            value: "fix"
          },
          {
            label: "Coming Soon ",
            value: "coming_soon"
          },
          {
            label: "Announcement",
            value: "announcement"
          },
          {
            label: "Improvement",
            value: "improvement"
          }
        ]
      }
    }),
    showInWidget: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Show In Widget ",
      description: "Check to enable widget",
      required: !1,
      options: {
        options: [
          {
            label: "True",
            value: !0
          },
          {
            label: "False",
            value: !1
          }
        ]
      }
    }),
    showInStandalone: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Show In Standalone  ",
      description: "Check to enable Standalone",
      required: !1,
      options: {
        options: [
          {
            label: "True",
            value: !0
          },
          {
            label: "False",
            value: !1
          }
        ]
      }
    }),
    enableFeedback: import_pieces_framework4.Property.Checkbox({
      displayName: "Enable Feedback",
      description: "Check to enable feedback",
      required: !1,
      defaultValue: !1
    }),
    enableReactions: import_pieces_framework4.Property.Checkbox({
      displayName: "Enable Reactions",
      description: "Check to enable feedback",
      required: !1,
      defaultValue: !1
    }),
    enableSocialShare: import_pieces_framework4.Property.Checkbox({
      displayName: "Enable Social share",
      description: "Check to enable social share",
      required: !1,
      defaultValue: !1
    }),
    autoOpen: import_pieces_framework4.Property.Checkbox({
      displayName: "Enable Auto open",
      description: "Check to enable auto open",
      required: !1,
      defaultValue: !1
    }),
    sendPushNotification: import_pieces_framework4.Property.Checkbox({
      displayName: "Enable Push Notifications",
      description: "Check to enable Push Notifications",
      required: !1,
      defaultValue: !1
    }),
    userEmail: import_pieces_framework4.Property.ShortText({
      displayName: "User Email",
      description: "Enter user email",
      required: !0
    })
  },
  async run(context) {
    let apiKey = context.auth.secret_text, request = {
      method: import_pieces_common4.HttpMethod.POST,
      url: `${beamerCommon.baseUrl}/posts`,
      headers: {
        "Beamer-Api-Key": `${apiKey}`,
        "Content-Type": "application/json"
      },
      body: {
        title: [context.propsValue.title],
        content: [context.propsValue.description],
        category: context.propsValue.category,
        publish: !0,
        archive: !1,
        pinned: !1,
        showInWidget: context.propsValue.showInWidget,
        showInStandalone: context.propsValue.showInStandalone,
        enableFeedback: context.propsValue.enableFeedback,
        enableReactions: context.propsValue.enableReactions,
        enableSocialShare: context.propsValue.enableSocialShare,
        autoOpen: context.propsValue.autoOpen,
        sendPushNotification: context.propsValue.sendPushNotification,
        userEmail: context.propsValue.userEmail,
        fixedBoostedAnnouncement: !0
      }
    };
    return (await import_pieces_common4.httpClient.sendRequest(request)).body;
  }
});

// ../ap/packages/pieces/community/beamer/src/lib/actions/create-vote.ts
var import_pieces_common5 = require("@activepieces/pieces-common");
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var createVote = (0, import_pieces_framework5.createAction)({
  auth: beamerAuth,
  name: "create_vote",
  classification: "WRITE",
  displayName: "Create a new vote ",
  description: "Create a new vote on a feature request",
  audience: "both",
  aiMetadata: {
    description: "Casts a vote on an existing Beamer feature request, identified by its numeric feature request ID, optionally attributed to a voter by first name and email. Use to register user support for a feedback item. Records a vote on each call (not idempotent).",
    idempotent: !1
  },
  props: {
    featureRequestId: import_pieces_framework5.Property.Number({
      displayName: "Feature ID",
      description: "ID of the feature request to create a comment for",
      required: !0
    }),
    userFirstname: import_pieces_framework5.Property.ShortText({
      displayName: "User Firstname",
      description: "Feature requested by..",
      required: !1
    }),
    userEmail: import_pieces_framework5.Property.ShortText({
      displayName: "User Email",
      description: "Feature requested by..",
      required: !1
    })
  },
  async run(context) {
    let apiKey = context.auth.secret_text, request = {
      method: import_pieces_common5.HttpMethod.POST,
      url: `${beamerCommon.baseUrl}/requests/${context.propsValue.featureRequestId}/votes`,
      headers: {
        "Beamer-Api-Key": `${apiKey}`,
        "Content-Type": "application/json"
      },
      body: {
        userFirstname: context.propsValue.userFirstname,
        email: context.propsValue.userEmail
      }
    };
    return (await import_pieces_common5.httpClient.sendRequest(request)).body;
  }
});

// ../ap/packages/pieces/community/beamer/src/lib/trigger/new-post.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), markdown = `
- Go to the "Integration" section.
- Find and click on the "Webhook" plugin to activate it.
- Add a webhook to that form.
- In the webhook settings, paste this URL: 
  \`{{webhookUrl}}\`
`, newPost = (0, import_pieces_framework6.createTrigger)({
  name: "new_post_on_beamer",
  classification: "READ",
  displayName: "New Beamer Post",
  description: "Triggers when new post is found in your beamer account",
  aiMetadata: {
    description: "Fires when a new post is published in the connected Beamer account, delivered via a Beamer webhook integration. Represents a freshly created announcement or changelog entry."
  },
  props: {
    md: import_pieces_framework6.Property.MarkDown({
      value: markdown
    })
  },
  type: import_pieces_framework6.TriggerStrategy.WEBHOOK,
  sampleData: {},
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/beamer/src/index.ts
var beamer = (0, import_pieces_framework7.createPiece)({
  displayName: "Beamer",
  description: "Engage users with targeted announcements",
  logoUrl: "https://cdn.activepieces.com/pieces/beamer.png",
  categories: [import_pieces_framework8.PieceCategory.PRODUCTIVITY],
  auth: beamerAuth,
  authors: ["i-nithin", "kishanprmr", "MoShizzle", "abuaboud"],
  actions: [
    createBeamerPost,
    createNewFeatureRequest,
    createComment,
    createVote,
    (0, import_pieces_common6.createCustomApiCallAction)({
      baseUrl: () => beamerCommon.baseUrl,
      auth: beamerAuth,
      authMapping: async (auth) => ({
        "Beamer-Api-Key": `${auth}`
      })
    })
  ],
  triggers: [newPost]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  beamer
});
