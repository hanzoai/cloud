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

// ../ap/packages/pieces/community/discourse/src/index.ts
var index_exports = {};
__export(index_exports, {
  discourse: () => discourse
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/discourse/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), markdownPropertyDescription = `
*Get your api Key: https://discourse.yourinstance.com/admin/api/keys
`, discourseAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: markdownPropertyDescription,
  required: !0,
  props: {
    api_key: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Key",
      required: !0
    }),
    api_username: import_pieces_framework.Property.ShortText({
      displayName: "API Username",
      required: !0
    }),
    website_url: import_pieces_framework.Property.ShortText({
      displayName: "Website URL",
      required: !0,
      description: "URL of the discourse url i.e https://discourse.yourinstance.com"
    })
  }
});

// ../ap/packages/pieces/community/discourse/src/lib/actions/add-users-to-group.action.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework"), addUsersToGroup = (0, import_pieces_framework2.createAction)({
  auth: discourseAuth,
  name: "add_users_to_group",
  description: "Add users to a group",
  audience: "both",
  aiMetadata: { description: "Add one or more users (by username) as members of a Discourse group, identified by its numeric group ID. Use when an agent needs to grant group membership. Re-adding users who are already members has no additional effect, so the call is effectively idempotent.", idempotent: !0 },
  displayName: "Add Users to Group",
  props: {
    group_id: import_pieces_framework2.Property.Dropdown({
      auth: discourseAuth,
      description: "Id of the group",
      displayName: "Group Id",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        options: (await import_pieces_common.httpClient.sendRequest({
          method: import_pieces_common.HttpMethod.GET,
          url: `${auth.props.website_url.trim()}/groups.json`,
          headers: {
            "Api-Key": auth.props.api_key,
            "Api-Username": auth.props.api_username
          }
        })).body.groups.map(
          (res) => ({
            label: res.display_name,
            value: res.id
          })
        ),
        disabled: !1
      } : {
        disabled: !0,
        options: [],
        placeholder: "Please connect your discourse account"
      }
    }),
    users: import_pieces_framework2.Property.Array({
      description: "List of users to add to the group",
      displayName: "Users",
      required: !0
    })
  },
  async run(context) {
    let { group_id, users } = context.propsValue;
    return users.join(","), (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.PUT,
      url: `${context.auth.props.website_url.trim()}/groups/${group_id}/members.json`,
      headers: {
        "Api-Key": context.auth.props.api_key,
        "Api-Username": context.auth.props.api_username
      },
      body: {
        usernames: users.join(",")
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/discourse/src/lib/actions/change-trust-level.action.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
var import_pieces_framework3 = require("@activepieces/pieces-framework"), changeUserTrustLevel = (0, import_pieces_framework3.createAction)({
  auth: discourseAuth,
  name: "change_user_trust_level",
  description: "Change the trust level of a user",
  audience: "both",
  aiMetadata: { description: "Set a Discourse user account to a specific trust level, identified by the numeric user ID. Use when an agent needs to promote or demote a member to control their permissions. Because it sets an absolute target level rather than incrementing, repeating the same call leaves the user at the same level.", idempotent: !0 },
  displayName: "Change User Trust Level",
  props: {
    user_id: import_pieces_framework3.Property.ShortText({
      description: "ID of the user",
      displayName: "User ID",
      required: !0
    }),
    new_trust_level: import_pieces_framework3.Property.Dropdown({
      description: "New trust level of the user",
      displayName: "New Trust Level",
      auth: discourseAuth,
      required: !0,
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your discourse account"
          };
        let response = await import_pieces_common2.httpClient.sendRequest({
          method: import_pieces_common2.HttpMethod.GET,
          url: `${auth.props.website_url.trim()}/site.json`,
          headers: {
            "Api-Key": auth.props.api_key,
            "Api-Username": auth.props.api_username
          }
        }), result = [], trust_levels = response.body.trust_levels;
        for (let key in trust_levels)
          result.push({ name: key, value: trust_levels[key] });
        return {
          options: result.map((res) => ({
            label: res.name,
            value: res.value
          })),
          disabled: !1
        };
      },
      refreshers: []
    })
  },
  async run(context) {
    let { user_id, new_trust_level } = context.propsValue;
    return await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.PUT,
      url: `${context.auth.props.website_url.trim()}/u/${user_id}.json`,
      headers: {
        "Api-Key": context.auth.props.api_key,
        "Api-Username": context.auth.props.api_username
      },
      body: {
        trust_level: new_trust_level
      }
    });
  }
});

// ../ap/packages/pieces/community/discourse/src/lib/actions/create-post.action.ts
var import_pieces_common3 = require("@activepieces/pieces-common");
var import_pieces_framework4 = require("@activepieces/pieces-framework"), createPost = (0, import_pieces_framework4.createAction)({
  auth: discourseAuth,
  name: "create_post",
  description: "Create a new post in discourse",
  audience: "both",
  aiMetadata: { description: "Add a reply post to an existing Discourse topic, identified by its numeric topic ID. Use when an agent needs to append a message to a thread that already exists (to start a new thread instead, use Create Topic). Each call creates a new post, so it is not idempotent.", idempotent: !1 },
  displayName: "Create Post",
  props: {
    raw: import_pieces_framework4.Property.LongText({
      description: "Content of the post",
      displayName: "Post Content",
      required: !0
    }),
    topic_id: import_pieces_framework4.Property.Dropdown({
      auth: discourseAuth,
      description: "ID of the topic to post in",
      displayName: "Topic ID",
      required: !0,
      options: async ({ auth }) => auth ? {
        options: (await import_pieces_common3.httpClient.sendRequest({
          method: import_pieces_common3.HttpMethod.GET,
          url: `${auth.props.website_url.trim()}/latest.json`,
          headers: {
            "Api-Key": auth.props.api_key,
            "Api-Username": auth.props.api_username
          }
        })).body.topic_list.topics.map(
          (res) => ({
            label: res.title,
            value: res.id
          })
        ),
        disabled: !1
      } : {
        disabled: !0,
        options: [],
        placeholder: "Please connect your discourse account"
      },
      refreshers: []
    })
  },
  async run(context) {
    let { raw, topic_id } = context.propsValue;
    return await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: `${context.auth.props.website_url.trim()}/posts.json`,
      headers: {
        "Api-Key": context.auth.props.api_key,
        "Api-Username": context.auth.props.api_username
      },
      body: {
        raw,
        topic_id
      }
    });
  }
});

// ../ap/packages/pieces/community/discourse/src/lib/actions/create-topic.action.ts
var import_pieces_common4 = require("@activepieces/pieces-common");
var import_pieces_framework5 = require("@activepieces/pieces-framework"), createTopic = (0, import_pieces_framework5.createAction)({
  auth: discourseAuth,
  name: "create_topic",
  description: "Create a new topic in Discourse",
  audience: "both",
  aiMetadata: { description: "Start a new Discourse topic (thread) with a title and body, optionally filed under a category ID. Use when an agent needs to open a new discussion rather than reply to an existing thread (use Create Post for replies). Each call creates a new topic, so it is not idempotent.", idempotent: !1 },
  displayName: "Create Topic",
  props: {
    title: import_pieces_framework5.Property.ShortText({
      description: "Title of the Topic",
      displayName: "Post Title",
      required: !0
    }),
    raw: import_pieces_framework5.Property.LongText({
      description: "Content of the topic",
      displayName: "Topic Content",
      required: !0
    }),
    category: import_pieces_framework5.Property.Dropdown({
      description: "ID of the category to post in",
      displayName: "Category ID",
      required: !1,
      auth: discourseAuth,
      options: async ({ auth }) => auth ? {
        options: (await import_pieces_common4.httpClient.sendRequest({
          method: import_pieces_common4.HttpMethod.GET,
          url: `${auth.props.website_url.trim()}/categories.json`,
          headers: {
            "Api-Key": auth.props.api_key,
            "Api-Username": auth.props.api_username
          }
        })).body.category_list.categories.map(
          (res) => ({
            label: res.name,
            value: res.id
          })
        ),
        disabled: !1
      } : {
        disabled: !0,
        options: [],
        placeholder: "Please connect your discourse account"
      },
      refreshers: []
    })
  },
  async run(context) {
    let { title, raw, category } = context.propsValue;
    return console.log("new post action"), await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: `${context.auth.props.website_url.trim()}/posts.json`,
      headers: {
        "Api-Key": context.auth.props.api_key,
        "Api-Username": context.auth.props.api_username
      },
      body: {
        title,
        raw,
        category
      }
    });
  }
});

// ../ap/packages/pieces/community/discourse/src/lib/actions/send-private-message.action.ts
var import_pieces_common5 = require("@activepieces/pieces-common");
var import_pieces_framework6 = require("@activepieces/pieces-framework"), sendPrivateMessage = (0, import_pieces_framework6.createAction)({
  auth: discourseAuth,
  name: "send_private_message",
  description: "Send a private message in Discourse",
  audience: "both",
  aiMetadata: { description: "Send a private message to one or more Discourse users (by username) with a title and body. Use when an agent needs to message members directly rather than post publicly. Each call delivers a new message, so it is not idempotent.", idempotent: !1 },
  displayName: "Send Private Message",
  props: {
    title: import_pieces_framework6.Property.ShortText({
      description: "Title for the PM",
      displayName: "Post Title",
      required: !0
    }),
    raw: import_pieces_framework6.Property.LongText({
      description: "Content of the post",
      displayName: "Post Content",
      required: !0
    }),
    target_recipients: import_pieces_framework6.Property.Array({
      description: "List of users to send the PM to (can be one or more)",
      displayName: "Users",
      required: !0
    })
  },
  async run(context) {
    let { title, raw, target_recipients } = context.propsValue;
    return await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.POST,
      url: `${context.auth.props.website_url.trim()}/posts.json`,
      headers: {
        "Api-Key": context.auth.props.api_key,
        "Api-Username": context.auth.props.api_username
      },
      body: {
        raw,
        title,
        target_recipients: target_recipients.join(","),
        archetype: "private_message"
      }
    });
  }
});

// ../ap/packages/pieces/community/discourse/src/index.ts
var discourse = (0, import_pieces_framework7.createPiece)({
  displayName: "Discourse",
  description: "Modern open source forum software",
  auth: discourseAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/discourse.png",
  categories: [import_pieces_framework8.PieceCategory.COMMUNICATION],
  authors: ["pfernandez98", "kishanprmr", "MoShizzle", "abuaboud"],
  actions: [
    createPost,
    createTopic,
    changeUserTrustLevel,
    addUsersToGroup,
    sendPrivateMessage,
    (0, import_pieces_common6.createCustomApiCallAction)({
      baseUrl: (auth) => auth ? auth.props.website_url.trim() : "",
      auth: discourseAuth,
      authMapping: async (auth) => ({
        "Api-Key": auth.props.api_key,
        "Api-Username": auth.props.api_username
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  discourse
});
