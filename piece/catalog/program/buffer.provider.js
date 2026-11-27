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

// ../ap/packages/pieces/community/buffer/src/index.ts
var index_exports = {};
__export(index_exports, {
  buffer: () => buffer
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/buffer/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/buffer/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BUFFER_API_URL = "https://api.buffer.com";
async function graphqlRequest({
  accessToken,
  query,
  variables
}) {
  let body = (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.POST,
    url: BUFFER_API_URL,
    headers: {
      Authorization: `Bearer ${accessToken}`,
      "Content-Type": "application/json"
    },
    body: { query, variables: variables != null ? variables : {} }
  })).body;
  if (body.errors && body.errors.length > 0)
    throw new Error(
      `Buffer API error: ${body.errors.map((e) => e.message).join("; ")}`
    );
  if (!body.data)
    throw new Error("Buffer API returned no data");
  return body.data;
}
var bufferClient = {
  graphql: graphqlRequest,
  apiUrl: BUFFER_API_URL
};

// ../ap/packages/pieces/community/buffer/src/lib/common/auth.ts
var bufferAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Access Token",
  description: `
**How to get your Buffer access token:**

1. Sign in to Buffer at https://buffer.com and open the [Developer Apps](https://publish.buffer.com/developers/apps) page.
2. Click **Create New App** and fill in the basic details (callback URL can be any placeholder).
3. After the app is created, copy the **Access Token** shown on the app's page.
4. Paste it below.

The token is sent as a Bearer token to the Buffer GraphQL API (https://api.buffer.com).`,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await bufferClient.graphql({
        accessToken: auth,
        query: "query { account { id } }"
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid Buffer access token. Make sure the token has the required scopes and try again."
      };
    }
  }
});

// ../ap/packages/pieces/community/buffer/src/lib/actions/create-post.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/buffer/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
async function fetchOrganizations(accessToken) {
  var _a, _b;
  return (_b = (_a = (await bufferClient.graphql({
    accessToken,
    query: "query { account { organizations { id name } } }"
  })).account) == null ? void 0 : _a.organizations) != null ? _b : [];
}
async function fetchChannels(accessToken, organizationId) {
  var _a;
  return (_a = (await bufferClient.graphql({
    accessToken,
    query: `query Channels($organizationId: OrganizationId!) {
      channels(input: { organizationId: $organizationId }) {
        id
        name
        service
        organizationId
        createdAt
      }
    }`,
    variables: { organizationId }
  })).channels) != null ? _a : [];
}
async function fetchPosts({
  accessToken,
  organizationId,
  channelIds,
  statusFilter,
  first = 50
}) {
  var _a, _b;
  let filter = {};
  channelIds && channelIds.length > 0 && (filter.channelIds = channelIds);
  let posts = ((_b = (_a = (await bufferClient.graphql({
    accessToken,
    query: `query Posts($input: PostsInput!, $first: Int) {
      posts(input: $input, first: $first) {
        edges {
          node {
            id
            text
            status
            createdAt
            updatedAt
            dueAt
            sentAt
            channelId
            channelService
            channel { id name service }
            tags { id name }
          }
        }
      }
    }`,
    variables: {
      input: __spreadValues({
        organizationId
      }, Object.keys(filter).length > 0 ? { filter } : {}),
      first
    }
  })).posts) == null ? void 0 : _a.edges) != null ? _b : []).map((edge) => edge.node);
  return statusFilter ? posts.filter((post) => statusFilter(post.status)) : posts;
}
var bufferProps = {
  organizationId: () => import_pieces_framework2.Property.Dropdown({
    auth: bufferAuth,
    displayName: "Organization",
    description: "The Buffer organization to use.",
    required: !0,
    refreshers: [],
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          placeholder: "Connect your Buffer account first.",
          options: []
        };
      try {
        return {
          disabled: !1,
          options: (await fetchOrganizations(auth.secret_text)).map((org) => ({
            label: org.name,
            value: org.id
          }))
        };
      } catch (e) {
        return {
          disabled: !0,
          placeholder: "Failed to load organizations.",
          options: []
        };
      }
    }
  }),
  channelIds: (required = !0) => import_pieces_framework2.Property.MultiSelectDropdown({
    auth: bufferAuth,
    displayName: "Channels",
    description: "The Buffer channels to publish to.",
    required,
    refreshers: ["organizationId"],
    options: async ({ auth, organizationId }) => {
      if (!auth || !organizationId)
        return {
          disabled: !0,
          placeholder: "Select an organization first.",
          options: []
        };
      try {
        return {
          disabled: !1,
          options: (await fetchChannels(
            auth.secret_text,
            organizationId
          )).map((channel) => ({
            label: `${channel.name} (${channel.service})`,
            value: channel.id
          }))
        };
      } catch (e) {
        return {
          disabled: !0,
          placeholder: "Failed to load channels.",
          options: []
        };
      }
    }
  }),
  channelId: () => import_pieces_framework2.Property.Dropdown({
    auth: bufferAuth,
    displayName: "Channel",
    description: "The Buffer channel.",
    required: !0,
    refreshers: ["organizationId"],
    options: async ({ auth, organizationId }) => {
      if (!auth || !organizationId)
        return {
          disabled: !0,
          placeholder: "Select an organization first.",
          options: []
        };
      try {
        return {
          disabled: !1,
          options: (await fetchChannels(
            auth.secret_text,
            organizationId
          )).map((channel) => ({
            label: `${channel.name} (${channel.service})`,
            value: channel.id
          }))
        };
      } catch (e) {
        return {
          disabled: !0,
          placeholder: "Failed to load channels.",
          options: []
        };
      }
    }
  })
}, bufferQueries = {
  fetchOrganizations,
  fetchChannels,
  fetchPosts
};

// ../ap/packages/pieces/community/buffer/src/lib/actions/create-post.ts
var CREATE_POST_MUTATION = `
  mutation CreatePost($input: CreatePostInput!) {
    createPost(input: $input) {
      __typename
      ... on PostActionSuccess {
        post {
          id
          text
          status
          dueAt
          sentAt
          channelId
        }
      }
      ... on MutationError {
        message
      }
    }
  }
`, createPost = (0, import_pieces_framework3.createAction)({
  auth: bufferAuth,
  name: "create_post",
  classification: "WRITE",
  displayName: "Create Post",
  description: "Create a post in Buffer and add it to the queue, share it next, share it now, or schedule it for a custom time.",
  audience: "both",
  aiMetadata: {
    description: "Creates a social media post in Buffer for one or more channels, where the share mode controls timing: add to the channel queue, share next (skip the queue), share now, or schedule for a custom time. Choose it to draft or publish content to connected social accounts. A custom-scheduled post requires the scheduled time; posting to multiple channels creates a separate post per channel. Not idempotent \u2014 each call creates new posts.",
    idempotent: !1
  },
  props: {
    organizationId: bufferProps.organizationId(),
    channelIds: bufferProps.channelIds(!0),
    text: import_pieces_framework3.Property.LongText({
      displayName: "Text",
      description: "The text content of the post.",
      required: !0
    }),
    mode: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Share Mode",
      description: "When and how the post should be published.",
      required: !0,
      defaultValue: "addToQueue",
      options: {
        disabled: !1,
        options: [
          { label: "Add to Queue", value: "addToQueue" },
          { label: "Share Next (skip the queue)", value: "shareNext" },
          { label: "Share Now", value: "shareNow" },
          { label: "Schedule for Custom Time", value: "customScheduled" }
        ]
      }
    }),
    dueAt: import_pieces_framework3.Property.DateTime({
      displayName: "Scheduled Time",
      description: 'When to publish the post. Required when Share Mode is "Schedule for Custom Time". ISO 8601 (UTC).',
      required: !1
    }),
    schedulingType: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Scheduling Type",
      description: 'Use "Automatic" for channels Buffer can publish to directly. Use "Notification" for channels that require manual publishing via the Buffer mobile app (e.g. Instagram personal accounts).',
      required: !0,
      defaultValue: "automatic",
      options: {
        disabled: !1,
        options: [
          { label: "Automatic", value: "automatic" },
          { label: "Notification (manual publish)", value: "notification" }
        ]
      }
    }),
    saveToDraft: import_pieces_framework3.Property.Checkbox({
      displayName: "Save as Draft",
      description: "Save the post as a draft instead of scheduling it. Drafts are not published until explicitly scheduled.",
      required: !1,
      defaultValue: !1
    }),
    imageUrls: import_pieces_framework3.Property.Array({
      displayName: "Image URLs",
      description: "Public URLs of images to attach to the post.",
      required: !1
    }),
    linkUrl: import_pieces_framework3.Property.ShortText({
      displayName: "Link URL",
      description: "A link to attach to the post (used as a link preview).",
      required: !1
    })
  },
  async run(context) {
    let {
      channelIds,
      text,
      mode,
      dueAt,
      schedulingType,
      saveToDraft,
      imageUrls,
      linkUrl
    } = context.propsValue;
    if (mode === "customScheduled" && !dueAt)
      throw new Error(
        'Scheduled Time is required when Share Mode is "Schedule for Custom Time".'
      );
    let assets = [];
    for (let url of imageUrls != null ? imageUrls : [])
      assets.push({ image: { url } });
    linkUrl && assets.push({ link: { url: linkUrl } });
    let results = [];
    for (let channelId of channelIds != null ? channelIds : []) {
      let input = {
        channelId,
        text,
        mode,
        schedulingType,
        assets
      };
      mode === "customScheduled" && dueAt && (input.dueAt = dueAt), saveToDraft && (input.saveToDraft = !0);
      let data = await bufferClient.graphql({
        accessToken: context.auth.secret_text,
        query: CREATE_POST_MUTATION,
        variables: { input }
      });
      if (data.createPost.message)
        throw new Error(
          `Buffer rejected the post for channel ${channelId}: ${data.createPost.message}`
        );
      results.push(data.createPost);
    }
    return { posts: results };
  }
});

// ../ap/packages/pieces/community/buffer/src/lib/actions/create-idea.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var CREATE_IDEA_MUTATION = `
  mutation CreateIdea($input: CreateIdeaInput!) {
    createIdea(input: $input) {
      __typename
      ... on IdeaResponse {
        idea {
          id
          organizationId
          groupId
          createdAt
          updatedAt
          content {
            title
            text
            media { url type alt }
            tags { id name color }
          }
        }
        refreshIdeas
      }
      ... on MutationError {
        message
      }
    }
  }
`, createIdea = (0, import_pieces_framework4.createAction)({
  auth: bufferAuth,
  name: "create_idea",
  classification: "WRITE",
  displayName: "Create Idea",
  description: "Save a new idea to your Buffer Idea Bank.",
  audience: "both",
  aiMetadata: {
    description: "Saves a new draft idea (title, body text, and/or attached images) to a Buffer organization's Idea Bank for later use. Choose it to stash content concepts without scheduling them to a channel. The idea must include at least a title, text, or one image. Not idempotent \u2014 each call creates a new idea.",
    idempotent: !1
  },
  props: {
    organizationId: bufferProps.organizationId(),
    title: import_pieces_framework4.Property.ShortText({
      displayName: "Title",
      description: "A short title or headline for the idea.",
      required: !1
    }),
    text: import_pieces_framework4.Property.LongText({
      displayName: "Text",
      description: "The main body of the idea.",
      required: !1
    }),
    imageUrls: import_pieces_framework4.Property.Array({
      displayName: "Image URLs",
      description: "Public URLs of images to attach to the idea.",
      required: !1
    }),
    aiAssisted: import_pieces_framework4.Property.Checkbox({
      displayName: "AI Assisted",
      description: "Mark this idea as created with AI assistance.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { organizationId, title, text, imageUrls, aiAssisted } = context.propsValue;
    if (!title && !text && (!imageUrls || imageUrls.length === 0))
      throw new Error(
        "An idea must include at least a title, text, or one image."
      );
    let media = (imageUrls != null ? imageUrls : []).map((url) => ({
      url,
      type: "image"
    })), content = {};
    title && (content.title = title), text && (content.text = text), media.length > 0 && (content.media = media), aiAssisted && (content.aiAssisted = !0);
    let data = await bufferClient.graphql({
      accessToken: context.auth.secret_text,
      query: CREATE_IDEA_MUTATION,
      variables: { input: { organizationId, content } }
    });
    if (data.createIdea.message)
      throw new Error(
        `Buffer rejected the idea: ${data.createIdea.message}`
      );
    return data;
  }
});

// ../ap/packages/pieces/community/buffer/src/lib/triggers/new-channel.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var polling = {
  strategy: import_pieces_common2.DedupeStrategy.TIMEBASED,
  async items({ auth, propsValue }) {
    return (await bufferQueries.fetchChannels(
      auth.secret_text,
      propsValue.organizationId
    )).map((channel) => ({
      epochMilliSeconds: channel.createdAt ? new Date(channel.createdAt).getTime() : 0,
      data: channel
    }));
  }
}, newChannel = (0, import_pieces_framework5.createTrigger)({
  auth: bufferAuth,
  name: "new_channel",
  classification: "READ",
  displayName: "New Channel",
  description: "Triggers when a new channel is connected to your Buffer organization.",
  aiMetadata: {
    description: "Fires when a new social media channel (e.g. a Facebook page, Instagram or X account) is connected to the selected Buffer organization, representing the newly linked channel."
  },
  type: import_pieces_framework5.TriggerStrategy.POLLING,
  props: {
    organizationId: bufferProps.organizationId()
  },
  sampleData: {
    id: "channel_id_example",
    name: "My Page",
    service: "facebook",
    organizationId: "org_id_example",
    createdAt: "2026-05-25T10:00:00.000Z"
  },
  async onEnable(context) {
    await import_pieces_common2.pollingHelper.onEnable(polling, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async onDisable(context) {
    await import_pieces_common2.pollingHelper.onDisable(polling, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async test(context) {
    return await import_pieces_common2.pollingHelper.test(polling, context);
  },
  async run(context) {
    return await import_pieces_common2.pollingHelper.poll(polling, context);
  }
});

// ../ap/packages/pieces/community/buffer/src/lib/triggers/new-queue-item.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var polling2 = {
  strategy: import_pieces_common3.DedupeStrategy.TIMEBASED,
  async items({ auth, propsValue }) {
    return (await bufferQueries.fetchPosts({
      accessToken: auth.secret_text,
      organizationId: propsValue.organizationId,
      channelIds: propsValue.channelIds,
      statusFilter: (status) => {
        let normalized = status == null ? void 0 : status.toLowerCase();
        return normalized !== "sent" && normalized !== "error";
      }
    })).map((post) => ({
      epochMilliSeconds: post.createdAt ? new Date(post.createdAt).getTime() : 0,
      data: post
    }));
  }
}, newQueueItem = (0, import_pieces_framework6.createTrigger)({
  auth: bufferAuth,
  name: "new_queue_item",
  classification: "READ",
  displayName: "New Queue Item",
  description: "Triggers when a new post is queued in Buffer.",
  aiMetadata: {
    description: "Fires when a new not-yet-published post (any status except sent or error, e.g. pending, scheduled, or draft) appears in the selected Buffer organization, optionally limited to specific channels. Represents the queued post."
  },
  type: import_pieces_framework6.TriggerStrategy.POLLING,
  props: {
    organizationId: bufferProps.organizationId(),
    channelIds: import_pieces_framework6.Property.MultiSelectDropdown({
      auth: bufferAuth,
      displayName: "Channels",
      description: "Limit results to specific channels (optional).",
      required: !1,
      refreshers: ["organizationId"],
      options: async ({ auth, organizationId }) => {
        if (!auth || !organizationId)
          return {
            disabled: !0,
            placeholder: "Select an organization first.",
            options: []
          };
        try {
          return {
            disabled: !1,
            options: (await bufferQueries.fetchChannels(
              auth.secret_text,
              organizationId
            )).map((channel) => ({
              label: `${channel.name} (${channel.service})`,
              value: channel.id
            }))
          };
        } catch (e) {
          return {
            disabled: !0,
            placeholder: "Failed to load channels.",
            options: []
          };
        }
      }
    })
  },
  sampleData: {
    id: "post_id_example",
    text: "Hello from Buffer!",
    status: "pending",
    createdAt: "2026-05-25T10:00:00.000Z",
    dueAt: "2026-05-25T18:00:00.000Z",
    channelId: "channel_id",
    channelService: "facebook",
    channel: { id: "channel_id", name: "My Page", service: "facebook" },
    tags: []
  },
  async onEnable(context) {
    await import_pieces_common3.pollingHelper.onEnable(polling2, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async onDisable(context) {
    await import_pieces_common3.pollingHelper.onDisable(polling2, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async test(context) {
    return await import_pieces_common3.pollingHelper.test(polling2, context);
  },
  async run(context) {
    return await import_pieces_common3.pollingHelper.poll(polling2, context);
  }
});

// ../ap/packages/pieces/community/buffer/src/lib/triggers/new-sent-item.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var polling3 = {
  strategy: import_pieces_common4.DedupeStrategy.TIMEBASED,
  async items({ auth, propsValue }) {
    return (await bufferQueries.fetchPosts({
      accessToken: auth.secret_text,
      organizationId: propsValue.organizationId,
      channelIds: propsValue.channelIds,
      statusFilter: (status) => (status == null ? void 0 : status.toLowerCase()) === "sent"
    })).map((post) => ({
      epochMilliSeconds: post.sentAt ? new Date(post.sentAt).getTime() : post.createdAt ? new Date(post.createdAt).getTime() : 0,
      data: post
    }));
  }
}, newSentItem = (0, import_pieces_framework7.createTrigger)({
  auth: bufferAuth,
  name: "new_sent_item",
  classification: "READ",
  displayName: "New Sent Post",
  description: "Triggers when a Buffer post is successfully published.",
  aiMetadata: {
    description: "Fires when a Buffer post is successfully published (status sent) to a connected channel in the selected organization, optionally limited to specific channels. Represents the sent post."
  },
  type: import_pieces_framework7.TriggerStrategy.POLLING,
  props: {
    organizationId: bufferProps.organizationId(),
    channelIds: import_pieces_framework7.Property.MultiSelectDropdown({
      auth: bufferAuth,
      displayName: "Channels",
      description: "Limit results to specific channels (optional).",
      required: !1,
      refreshers: ["organizationId"],
      options: async ({ auth, organizationId }) => {
        if (!auth || !organizationId)
          return {
            disabled: !0,
            placeholder: "Select an organization first.",
            options: []
          };
        try {
          return {
            disabled: !1,
            options: (await bufferQueries.fetchChannels(
              auth.secret_text,
              organizationId
            )).map((channel) => ({
              label: `${channel.name} (${channel.service})`,
              value: channel.id
            }))
          };
        } catch (e) {
          return {
            disabled: !0,
            placeholder: "Failed to load channels.",
            options: []
          };
        }
      }
    })
  },
  sampleData: {
    id: "post_id_example",
    text: "Hello from Buffer!",
    status: "sent",
    createdAt: "2026-05-25T10:00:00.000Z",
    sentAt: "2026-05-25T18:00:00.000Z",
    channelId: "channel_id",
    channelService: "facebook",
    channel: { id: "channel_id", name: "My Page", service: "facebook" },
    tags: []
  },
  async onEnable(context) {
    await import_pieces_common4.pollingHelper.onEnable(polling3, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async onDisable(context) {
    await import_pieces_common4.pollingHelper.onDisable(polling3, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async test(context) {
    return await import_pieces_common4.pollingHelper.test(polling3, context);
  },
  async run(context) {
    return await import_pieces_common4.pollingHelper.poll(polling3, context);
  }
});

// ../ap/packages/pieces/community/buffer/src/index.ts
var buffer = (0, import_pieces_framework8.createPiece)({
  displayName: "Buffer",
  description: "Schedule, publish and analyze social media posts across multiple channels with Buffer.",
  auth: bufferAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/buffer.png",
  categories: [import_pieces_framework9.PieceCategory.MARKETING],
  authors: ["sanket-a11y"],
  actions: [
    createPost,
    createIdea,
    (0, import_pieces_common5.createCustomApiCallAction)({
      baseUrl: () => bufferClient.apiUrl,
      auth: bufferAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [newChannel, newQueueItem, newSentItem]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  buffer
});
