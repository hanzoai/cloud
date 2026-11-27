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

// ../ap/packages/pieces/community/wonderchat/src/index.ts
var index_exports = {};
__export(index_exports, {
  wonderchat: () => wonderchat,
  wonderchatAuth: () => wonderchatAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/wonderchat/src/lib/actions/ask-question.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");
var askQuestion = (0, import_pieces_framework.createAction)({
  name: "askQuestion",
  displayName: "Ask Question",
  description: "Sends a question to a Wonderchat bot; returns the bot\u2019s answer.",
  audience: "both",
  aiMetadata: {
    description: "Sends a question to a specific Wonderchat chatbot (identified by chatbotId) and returns the bot\u2019s generated answer. Use to query a deployed knowledge-base chatbot programmatically. Optionally pass a chatlogId to continue an existing chat session plus custom context/contextUrl to ground the answer. Not idempotent: each call posts a new message and produces a fresh AI-generated reply.",
    idempotent: !1
  },
  auth: wonderchatAuth,
  props: {
    chatbotId: import_pieces_framework.Property.ShortText({
      displayName: "Chatbot Id",
      description: "The ID of your chatbot (can be found in the URL when viewing your bot: /bot/YOUR_BOT_ID)",
      required: !0
    }),
    question: import_pieces_framework.Property.LongText({
      displayName: "Question",
      description: "The question you wish to ask your chatbot",
      required: !0
    }),
    chatlogId: import_pieces_framework.Property.ShortText({
      displayName: "Chatlog Id",
      description: 'The ID of your chat session (can be found under Chatlog Details section, labeled as "ID")',
      required: !1
    }),
    context: import_pieces_framework.Property.LongText({
      displayName: "Custom context",
      description: "Additional custom context about the chat session (e.g., user information)",
      required: !1
    }),
    contextUrl: import_pieces_framework.Property.LongText({
      displayName: "Context URL",
      description: "URL of the page the user is on to provide additional context",
      required: !1
    })
  },
  async run({ propsValue }) {
    let { chatbotId, question, chatlogId, context, contextUrl } = propsValue, requestbody = {
      chatbotId,
      question
    };
    return chatlogId && (requestbody.chatlogId = chatlogId), context && (requestbody.context = context), contextUrl && (requestbody.contextUrl = contextUrl), (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://app.wonderchat.io/api/v1/chat",
      headers: { "Content-Type": "application/json" },
      body: requestbody
    })).body;
  }
});

// ../ap/packages/pieces/community/wonderchat/src/lib/actions/add-page.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var addPage = (0, import_pieces_framework2.createAction)({
  name: "addPage",
  displayName: "Add Page",
  description: "Add new pages to your chatbot\u2019s knowledge base.",
  audience: "both",
  aiMetadata: {
    description: "Adds one or more webpage URLs to a specific Wonderchat chatbot\u2019s knowledge base (identified by chatbotId), triggering Wonderchat to crawl and ingest them. Use when an agent needs to expand or update what a bot can answer from. Optionally provide a session cookie to crawl pages behind a login. Not idempotent: each call appends/re-ingests pages into the knowledge base.",
    idempotent: !1
  },
  auth: wonderchatAuth,
  props: {
    chatbotId: import_pieces_framework2.Property.ShortText({
      displayName: "Chatbot Id",
      description: "The ID of your chatbot (can be found in the URL when viewing your bot: /bot/YOUR_BOT_ID)",
      required: !0
    }),
    urls: import_pieces_framework2.Property.Array({
      displayName: "URLs",
      description: `List of webpage URLs to add to your chatbot's knowledge base (e.g., "https://wonderchat.io")`,
      required: !1
    }),
    sessionCookie: import_pieces_framework2.Property.LongText({
      displayName: "Session cookie",
      description: "Session cookie for crawling sites behind login",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let apiKey = auth.secret_text, { chatbotId, urls, sessionCookie } = propsValue, requestbody = {
      apiKey,
      chatbotId
    };
    return sessionCookie && (requestbody.sessionCookie = sessionCookie), urls && (requestbody.urls = urls), (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://app.wonderchat.io/api/v1/add-pages",
      headers: { "Content-Type": "application/json" },
      body: requestbody
    })).body;
  }
});

// ../ap/packages/pieces/community/wonderchat/src/lib/actions/add-tag.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var addTag = (0, import_pieces_framework3.createAction)({
  name: "addTag",
  displayName: "Add Tag",
  description: "Add custom tags to a specific chatlog.",
  audience: "both",
  aiMetadata: {
    description: "Adds one or more custom tags to a specific Wonderchat chatlog (chat session identified by chatlogId). Use to label or categorize a conversation. Idempotent: tags are applied as a set, so re-adding the same tags leaves the chatlog\u2019s tag set unchanged.",
    idempotent: !0
  },
  auth: wonderchatAuth,
  props: {
    chatlogId: import_pieces_framework3.Property.ShortText({
      displayName: "Chatlog Id",
      description: 'The ID of your chat session (can be found under Chatlog Details section, labeled as "ID")',
      required: !0
    }),
    tags: import_pieces_framework3.Property.Array({
      displayName: "Tags",
      description: "List of URLs to add to your chatbot",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let apiKey = auth.secret_text, { chatlogId, tags } = propsValue;
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: "https://app.wonderchat.io/api/v1/add-tags-to-chatlog",
      headers: { "Content-Type": "application/json" },
      body: {
        apiKey,
        chatlogId,
        tags
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/wonderchat/src/lib/actions/remove-tag.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var removeTag = (0, import_pieces_framework4.createAction)({
  name: "removeTag",
  displayName: "Remove Tag",
  description: "Remove specific tags from a chatlog.",
  audience: "both",
  aiMetadata: {
    description: "Removes one or more tags from a specific Wonderchat chatlog (chat session identified by chatlogId). Use to clear or correct labels on a conversation. Idempotent: re-running with the same tags leaves the chatlog\u2019s tag set unchanged once those tags are already absent.",
    idempotent: !0
  },
  auth: wonderchatAuth,
  props: {
    chatlogId: import_pieces_framework4.Property.ShortText({
      displayName: "Chatlog Id",
      description: 'The ID of your chat session (can be found under Chatlog Details section, labeled as "ID")',
      required: !0
    }),
    tags: import_pieces_framework4.Property.Array({
      displayName: "Tags",
      description: "List of tags to remove",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let apiKey = auth.secret_text, { chatlogId, tags } = propsValue;
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: "https://app.wonderchat.io/api/v1/delete-tags-from-chatlog",
      headers: { "Content-Type": "application/json" },
      body: {
        apiKey,
        chatlogId,
        tags
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/wonderchat/src/lib/triggers/new-user-message.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var polling = {
  strategy: import_pieces_common5.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => (await import_pieces_common5.httpClient.sendRequest({
    method: import_pieces_common5.HttpMethod.POST,
    url: "https://app.wonderchat.io/api/v1/messages",
    headers: { "Content-Type": "application/json" },
    body: {
      apiKey: auth.secret_text,
      chatlogId: propsValue.chatlogId
    }
  })).body.messages.filter((message) => message.type === "user").filter(
    (message) => new Date(message.createdAt).getTime() > lastFetchEpochMS
  ).map((message) => ({
    epochMilliSeconds: new Date(message.createdAt).getTime(),
    data: message
  }))
}, newUserMessage = (0, import_pieces_framework5.createTrigger)({
  auth: wonderchatAuth,
  name: "newUserMessage",
  displayName: "New User Message",
  description: "Triggers when a new message is sent by a user in a specific chatlog.",
  aiMetadata: {
    description: "Fires when an end user sends a new message in a specific Wonderchat chat session (identified by chatlogId). Polls the chatlog\u2019s messages and emits only newly arrived user-typed messages (bot replies are excluded). Use to react to incoming visitor messages in a single tracked conversation."
  },
  props: {
    chatlogId: import_pieces_framework5.Property.ShortText({
      displayName: "Chatlog ID",
      description: 'The ID of your chat session (can be found under Chatlog Details section, labeled as "ID")',
      required: !0
    })
  },
  sampleData: {
    content: "I would want to now more about the products pricing strategy",
    type: "user",
    createdAt: "2023-12-01T06:35:15.456Z"
  },
  type: import_pieces_framework5.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common5.pollingHelper.poll(polling, context);
  },
  async onEnable(context) {
    let { store, auth, propsValue } = context;
    await import_pieces_common5.pollingHelper.onEnable(polling, { store, auth, propsValue });
  },
  async onDisable(context) {
    let { store, auth, propsValue } = context;
    await import_pieces_common5.pollingHelper.onEnable(polling, { store, auth, propsValue });
  },
  async run(context) {
    return await import_pieces_common5.pollingHelper.poll(polling, context);
  }
});

// ../ap/packages/pieces/community/wonderchat/src/index.ts
var wonderchatAuth = import_pieces_framework6.PieceAuth.SecretText({
  displayName: "API Key",
  description: `
  1. Log in to your Wonderchat account.
  2. Click on your **Profile card**.
  2. Go to your **User settings**.
  3. Click on the the **API keys** tab.
  3. If you already have a key, copy it, if not generate one.
  `,
  required: !0
}), wonderchat = (0, import_pieces_framework6.createPiece)({
  displayName: "Wonderchat",
  description: "Wonderchat is a no-code chatbot platform that lets you deploy AI-powered chatbots for websites quickly.",
  auth: wonderchatAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/wonderchat.png",
  categories: [
    import_pieces_framework7.PieceCategory.ARTIFICIAL_INTELLIGENCE
  ],
  authors: ["devroy10", "sanket-a11y"],
  actions: [askQuestion, addPage, addTag, removeTag],
  triggers: [newUserMessage]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  wonderchat,
  wonderchatAuth
});
