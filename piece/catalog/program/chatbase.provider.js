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

// ../ap/packages/pieces/community/chatbase/src/index.ts
var index_exports = {};
__export(index_exports, {
  chatbase: () => chatbase
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/chatbase/src/lib/actions/create-chatbot.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/chatbase/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), markdownDescription = "You can get your API key from your [Chatbase Account](https://www.chatbase.co/dashboard).", chatbaseAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: markdownDescription,
  required: !0
});

// ../ap/packages/pieces/community/chatbase/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common");
async function makeRequest(apiKey, method, path, body) {
  let url = `https://www.chatbase.co/api/v1${path}`;
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url,
    headers: {
      Authorization: `Bearer ${apiKey}`,
      "Content-Type": "application/json"
    },
    body
  })).body;
}

// ../ap/packages/pieces/community/chatbase/src/lib/actions/create-chatbot.ts
var createChatbotAction = (0, import_pieces_framework2.createAction)({
  auth: chatbaseAuth,
  name: "create_chatbot",
  classification: "WRITE",
  displayName: "Create Chatbot",
  description: "Creates a new chatbot.",
  audience: "both",
  aiMetadata: { description: "Creates a new Chatbase chatbot, optionally seeding it with inline training text. Use when an agent needs to provision a fresh chatbot before sending prompts to it. Not idempotent: each call creates a separate chatbot even with identical input.", idempotent: !1 },
  props: {
    chatbotName: import_pieces_framework2.Property.ShortText({
      displayName: "Chatbot Name",
      required: !0
    }),
    sourceText: import_pieces_framework2.Property.LongText({
      displayName: "Source Text",
      description: "Optional text data for training the chatbot.",
      required: !1
    })
  },
  async run(context) {
    let { chatbotName, sourceText } = context.propsValue, apiKey = context.auth.secret_text, body = {
      chatbotName
    };
    return sourceText && (body.sourceText = sourceText), await makeRequest(apiKey, import_pieces_common2.HttpMethod.POST, "/create-chatbot", body);
  }
});

// ../ap/packages/pieces/community/chatbase/src/lib/actions/list-all-chatbots.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");
var listChatbotsAction = (0, import_pieces_framework3.createAction)({
  auth: chatbaseAuth,
  name: "list_chatbots",
  classification: "SEARCH",
  displayName: "List All Chatbots",
  description: "Retrieves a list of all chatbots.",
  audience: "both",
  aiMetadata: { description: "Lists all chatbots in the authenticated Chatbase account. Use when an agent needs to discover available chatbots or resolve a chatbot ID before sending prompts or searching conversations. Takes no input; idempotent read-only listing.", idempotent: !0 },
  props: {},
  async run(context) {
    let apiKey = context.auth.secret_text;
    return await makeRequest(apiKey, import_pieces_common3.HttpMethod.GET, "/get-chatbots");
  }
});

// ../ap/packages/pieces/community/chatbase/src/lib/actions/search-conversations-by-query.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/chatbase/src/lib/common/props.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var chatbotIdDropdown = import_pieces_framework4.Property.Dropdown({
  auth: chatbaseAuth,
  displayName: "Chatbot",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please connect your account first.",
        options: []
      };
    let apiKey = auth.secret_text;
    return {
      disabled: !1,
      options: (await makeRequest(apiKey, import_pieces_common4.HttpMethod.GET, "/get-chatbots")).chatbots.map((chatbot) => ({
        label: chatbot.chatbotName,
        value: chatbot.chatbotId
      }))
    };
  }
});

// ../ap/packages/pieces/community/chatbase/src/lib/actions/search-conversations-by-query.ts
var searchConversationsAction = (0, import_pieces_framework5.createAction)({
  auth: chatbaseAuth,
  name: "search_conversations",
  classification: "SEARCH",
  displayName: "Search Conversations by Query",
  description: "Searches for conversations from a specific chatbot.",
  audience: "both",
  aiMetadata: { description: "Retrieves logged conversations for a specific Chatbase chatbot (selected by its ID), with optional filters for source channels (API, WhatsApp, Slack, etc.), a start/end date range, and pagination. Use when an agent needs to review or audit past chatbot conversations; leaving the optional filters empty returns all conversations for the chatbot. Idempotent read-only lookup.", idempotent: !0 },
  props: {
    chatbotId: chatbotIdDropdown,
    filteredSources: import_pieces_framework5.Property.StaticMultiSelectDropdown({
      displayName: "Sources",
      description: "Filter by one or more conversation sources.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "API", value: "API" },
          { label: "Chatbase site", value: "Chatbase site" },
          { label: "Instagram", value: "Instagram" },
          { label: "Messenger", value: "Messenger" },
          { label: "Slack", value: "Slack" },
          { label: "Unspecified", value: "Unspecified" },
          { label: "WhatsApp", value: "WhatsApp" },
          { label: "Widget or Iframe", value: "Widget or Iframe" }
        ]
      }
    }),
    startDate: import_pieces_framework5.Property.DateTime({
      displayName: "Start Date",
      required: !1
    }),
    endDate: import_pieces_framework5.Property.DateTime({
      displayName: "End Date",
      required: !1
    }),
    page: import_pieces_framework5.Property.Number({
      displayName: "Page",
      description: "Pagination page number (default = 1)",
      required: !1
    }),
    size: import_pieces_framework5.Property.Number({
      displayName: "Page Size",
      description: "Number of results per page (default = 10, max = 100)",
      required: !1
    })
  },
  async run(context) {
    let { chatbotId, filteredSources, startDate, endDate, page, size } = context.propsValue, apiKey = context.auth.secret_text, queryParams = new URLSearchParams({ chatbotId });
    return filteredSources != null && filteredSources.length && queryParams.append("filteredSources", filteredSources.join(",")), startDate && queryParams.append("startDate", startDate.toString().split("T")[0]), endDate && queryParams.append("endDate", endDate.toString().split("T")[0]), page && queryParams.append("page", page.toString()), size && queryParams.append("size", size.toString()), await makeRequest(
      apiKey,
      import_pieces_common5.HttpMethod.GET,
      `/get-conversations?${queryParams.toString()}`
    );
  }
});

// ../ap/packages/pieces/community/chatbase/src/lib/actions/send-prompt-to-chatbot.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var sendPromptToChatbotAction = (0, import_pieces_framework6.createAction)({
  auth: chatbaseAuth,
  name: "message_chatbot",
  classification: "READ",
  displayName: "Send Prompt to Chatbot",
  description: "Sends a prompt to the chatbot to generate a response.",
  audience: "both",
  aiMetadata: { description: "Sends a user prompt to a specific Chatbase chatbot (selected by its ID) and returns the generated reply, optionally overriding the model and temperature. Use when an agent needs an answer from a trained chatbot; pass a stable conversationId to keep messages threaded in one logged conversation. Not idempotent: each call appends a turn and produces a fresh, possibly different completion.", idempotent: !1 },
  props: {
    chatbotId: chatbotIdDropdown,
    message: import_pieces_framework6.Property.LongText({
      displayName: "Prompt",
      required: !0
    }),
    temperature: import_pieces_framework6.Property.Number({
      displayName: "Temperature",
      description: "Higher values = more random output. Between 0 and 1.",
      required: !1,
      defaultValue: 0
    }),
    conversationId: import_pieces_framework6.Property.ShortText({
      displayName: "Conversation ID",
      description: "Unique ID for saving this conversation in Chatbase dashboard.",
      required: !1
    }),
    model: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Model (Optional)",
      required: !1,
      options: {
        options: [
          { label: "o4-mini", value: "o4-mini" },
          { label: "o3", value: "o3" },
          { label: "gpt-4", value: "gpt-4" },
          { label: "gpt-4o", value: "gpt-4o" },
          { label: "gpt-4o-mini", value: "gpt-4o-mini" },
          { label: "gpt-4.1-mini", value: "gpt-4.1-mini" },
          { label: "gpt-4.1-nano", value: "gpt-4.1-nano" },
          { label: "gpt-4-turbo", value: "gpt-4-turbo" },
          { label: "o3-mini", value: "o3-mini" },
          { label: "gpt-4.1", value: "gpt-4.1" },
          { label: "gpt-4.5", value: "gpt-4.5" },
          { label: "claude-sonnet-4", value: "claude-sonnet-4" },
          { label: "claude-3-7-sonnet", value: "claude-3-7-sonnet" },
          { label: "claude-3-5-sonnet", value: "claude-3-5-sonnet" },
          { label: "claude-3-opus", value: "claude-3-opus" },
          { label: "claude-opus-4", value: "claude-opus-4" },
          { label: "claude-3-haiku", value: "claude-3-haiku" },
          { label: "gemini-2.0-flash", value: "gemini-2.0-flash" },
          { label: "gemini-1.5-flash", value: "gemini-1.5-flash" },
          { label: "gemini-1.5-pro", value: "gemini-1.5-pro" },
          { label: "gemini-2.0-pro", value: "gemini-2.0-pro" },
          { label: "command-r", value: "command-r" },
          { label: "command-r-plus", value: "command-r-plus" },
          { label: "DeepSeek-V3", value: "DeepSeek-V3" },
          { label: "DeepSeek-R1", value: "DeepSeek-R1" },
          {
            label: "Llama-4-Scout-17B-16E-Instruct",
            value: "Llama-4-Scout-17B-16E-Instruct"
          },
          {
            label: "Llama-4-Maverick-17B-128E-Instruct-FP8",
            value: "Llama-4-Maverick-17B-128E-Instruct-FP8"
          },
          { label: "grok-3", value: "grok-3" },
          { label: "grok-3-mini", value: "grok-3-mini" }
        ]
      }
    })
  },
  async run(context) {
    let { chatbotId, message, temperature, model, conversationId } = context.propsValue, apiKey = context.auth.secret_text, payload = {
      chatbotId,
      messages: [{ content: message, role: "user" }]
    };
    return temperature !== void 0 && (payload.temperature = temperature), conversationId && (payload.conversationId = conversationId), model && (payload.model = model), await makeRequest(apiKey, import_pieces_common6.HttpMethod.POST, "/chat", payload);
  }
});

// ../ap/packages/pieces/community/chatbase/src/index.ts
var chatbase = (0, import_pieces_framework7.createPiece)({
  displayName: "Chatbase",
  description: "Build and manage AI chatbots with custom sources.",
  auth: chatbaseAuth,
  logoUrl: "https://cdn.activepieces.com/pieces/chatbase.png",
  categories: [import_pieces_framework8.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["krushnarout"],
  actions: [
    createChatbotAction,
    sendPromptToChatbotAction,
    searchConversationsAction,
    listChatbotsAction,
    (0, import_pieces_common7.createCustomApiCallAction)({
      auth: chatbaseAuth,
      baseUrl: () => "https://www.chatbase.co/api/v1",
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  chatbase
});
