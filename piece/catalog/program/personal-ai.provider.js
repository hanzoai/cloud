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

// ../ap/packages/pieces/community/personal-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  BASE_URL: () => BASE_URL,
  aiAssistant: () => aiAssistant
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_framework13 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/personal-ai/src/lib/actions/memory/create-memory.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/personal-ai/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), personalAiAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "API Key for authentication",
  required: !0
});

// ../ap/packages/pieces/community/personal-ai/src/lib/actions/memory/create-memory.ts
var createMemory = (0, import_pieces_framework2.createAction)({
  auth: personalAiAuth,
  name: "create_memory",
  displayName: "Create Memory",
  description: "Upload memories to your AI assistant stack.",
  audience: "both",
  aiMetadata: { description: "Append a plain-text memory to the Personal AI memory stack so the assistant can recall it later, tagging it with a required source name and optional tags/domain/timestamp. Use when you want to persist a fact or note for future retrieval. Each call adds a new memory entry, so it is not idempotent.", idempotent: !1 },
  // category: 'Memory',
  props: {
    text: import_pieces_framework2.Property.LongText({
      displayName: "Memory Text",
      description: "Plain text memories to upload to your stack",
      required: !0
    }),
    sourceName: import_pieces_framework2.Property.ShortText({
      displayName: "Source Name",
      description: "The source or application of memory to help you recall where it is from",
      required: !0
    }),
    createdTime: import_pieces_framework2.Property.ShortText({
      displayName: "Created Time",
      description: "Time (including timezone) of the memory (e.g., Wed, 19 Sep 2023 13:31:00 PDT)",
      required: !1
    }),
    rawFeedText: import_pieces_framework2.Property.LongText({
      displayName: "Raw Feed Text",
      description: "The formatted text that can be stored as it is",
      required: !1
    }),
    domainName: import_pieces_framework2.Property.ShortText({
      displayName: "Domain Name",
      description: "The domain identifier for the AI persona",
      required: !1
    }),
    tags: import_pieces_framework2.Property.ShortText({
      displayName: "Tags",
      description: "Comma delimited list of tags for the memory",
      required: !1
    })
  },
  async run(context) {
    let { auth, propsValue: { text, sourceName, createdTime, rawFeedText, domainName, tags } } = context;
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: `${BASE_URL}/v1/memory`,
      headers: {
        "Content-Type": "application/json",
        "x-api-key": auth.secret_text
      },
      body: __spreadValues(__spreadValues(__spreadValues(__spreadValues({
        Text: text,
        SourceName: sourceName
      }, createdTime && { CreatedTime: createdTime }), rawFeedText && { RawFeedText: rawFeedText }), domainName && { DomainName: domainName }), tags && { Tags: tags })
    })).body;
  }
});

// ../ap/packages/pieces/community/personal-ai/src/lib/actions/messaging/create-message.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var createMessage = (0, import_pieces_framework3.createAction)({
  auth: personalAiAuth,
  name: "create_message",
  displayName: "Send Message",
  description: "Send a message to the AI assistant for a response.",
  audience: "both",
  aiMetadata: { description: "Send a message to the Personal AI assistant and get its generated reply, optionally continuing an existing conversation by reusing the same session ID. Use when you want a conversational answer from the assistant; set the add-to-memory flag to also persist the message. Each call produces a fresh response, so it is not idempotent.", idempotent: !1 },
  // category: 'Messaging',
  props: {
    text: import_pieces_framework3.Property.LongText({
      displayName: "Message Text",
      description: "Message to send to your AI for a response",
      required: !0
    }),
    context: import_pieces_framework3.Property.LongText({
      displayName: "Context",
      description: "Additional context for the AI response (Similar to Reply function)",
      required: !1
    }),
    domainName: import_pieces_framework3.Property.ShortText({
      displayName: "Domain Name",
      description: "The domain identifier for the AI profile",
      required: !1
    }),
    userName: import_pieces_framework3.Property.ShortText({
      displayName: "User Name",
      description: "Name of the user sending the request",
      required: !1
    }),
    sessionId: import_pieces_framework3.Property.ShortText({
      displayName: "Session ID",
      description: "Use the same sessionId to continue conversation on that session",
      required: !1
    }),
    sourceName: import_pieces_framework3.Property.ShortText({
      displayName: "Source Name",
      description: "Name of the source app of the inbound message",
      required: !1
    }),
    isStack: import_pieces_framework3.Property.Checkbox({
      displayName: "Add to Memory",
      description: "Flag to also add the user message to memory",
      required: !1,
      defaultValue: !1
    }),
    isDraft: import_pieces_framework3.Property.Checkbox({
      displayName: "Create Draft",
      description: "Flag to create a copilot message for the AI",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { auth, propsValue: { text, context: messageContext, domainName, userName, sessionId, sourceName, isStack, isDraft } } = context;
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: `${BASE_URL}/v1/message`,
      headers: {
        "Content-Type": "application/json",
        "x-api-key": auth.secret_text
      },
      body: __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({
        Text: text
      }, messageContext && { Context: messageContext }), domainName && { DomainName: domainName }), userName && { UserName: userName }), sessionId && { SessionId: sessionId }), sourceName && { SourceName: sourceName }), isStack !== void 0 && { is_stack: isStack }), isDraft !== void 0 && { is_draft: isDraft })
    })).body;
  }
});

// ../ap/packages/pieces/community/personal-ai/src/lib/actions/ai_interaction/create-chatgpt-instruction.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var createChatGPTInstruction = (0, import_pieces_framework4.createAction)({
  auth: personalAiAuth,
  name: "create_chatgpt_instruction",
  displayName: "Send ChatGPT Instruction",
  description: "Send an instruction to AI assistant using ChatGPT integration.",
  audience: "both",
  aiMetadata: { description: "Send an instruction or prompt to the Personal AI assistant routed through its ChatGPT integration and get the generated response, optionally continuing a session by reusing the same session ID. Use when you want a ChatGPT-backed answer rather than the standard message flow. Each call generates a fresh response, so it is not idempotent.", idempotent: !1 },
  // category: 'AI Interaction',
  props: {
    text: import_pieces_framework4.Property.LongText({
      displayName: "Instruction Text",
      description: "The instruction or prompt to send to ChatGPT",
      required: !0
    }),
    context: import_pieces_framework4.Property.LongText({
      displayName: "Context",
      description: "Additional context for the AI response",
      required: !1
    }),
    domainName: import_pieces_framework4.Property.ShortText({
      displayName: "Domain Name",
      description: "The domain identifier for the AI profile",
      required: !1
    }),
    userName: import_pieces_framework4.Property.ShortText({
      displayName: "User Name",
      description: "Name of the user sending the request",
      required: !1
    }),
    sessionId: import_pieces_framework4.Property.ShortText({
      displayName: "Session ID",
      description: "Use the same sessionId to continue conversation on that session",
      required: !1
    }),
    sourceName: import_pieces_framework4.Property.ShortText({
      displayName: "Source Name",
      description: "Name of the source app of the inbound instruction",
      required: !1
    }),
    isStack: import_pieces_framework4.Property.Checkbox({
      displayName: "Add to Memory",
      description: "Flag to also add the user instruction to memory",
      required: !1,
      defaultValue: !1
    }),
    isDraft: import_pieces_framework4.Property.Checkbox({
      displayName: "Create Draft",
      description: "Flag to create a copilot message for the AI",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { auth, propsValue: { text, context: messageContext, domainName, userName, sessionId, sourceName, isStack, isDraft } } = context;
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: `${BASE_URL}/v1/instruction?cmd=chatgpt`,
      headers: {
        "Content-Type": "application/json",
        "x-api-key": auth.secret_text
      },
      body: __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({
        Text: text
      }, messageContext && { Context: messageContext }), domainName && { DomainName: domainName }), userName && { UserName: userName }), sessionId && { SessionId: sessionId }), sourceName && { SourceName: sourceName }), isStack !== void 0 && { is_stack: isStack }), isDraft !== void 0 && { is_draft: isDraft })
    })).body;
  }
});

// ../ap/packages/pieces/community/personal-ai/src/lib/actions/ai_interaction/create-custom-training.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var createCustomTraining = (0, import_pieces_framework5.createAction)({
  auth: personalAiAuth,
  name: "create_custom_training",
  displayName: "Send Custom Training",
  description: "Send a custom training instruction to AI assistant.",
  audience: "both",
  aiMetadata: { description: "Send a custom training instruction or prompt to the Personal AI assistant to shape its future behavior, optionally tied to a session. Use when you want to teach or fine-tune how the assistant responds rather than just chat with it. Each call submits a new training instruction, so it is not idempotent.", idempotent: !1 },
  // category: 'AI Interaction',
  props: {
    text: import_pieces_framework5.Property.LongText({
      displayName: "Training Text",
      description: "The training instruction or prompt to send",
      required: !0
    }),
    context: import_pieces_framework5.Property.LongText({
      displayName: "Context",
      description: "Additional context for the AI response",
      required: !1
    }),
    domainName: import_pieces_framework5.Property.ShortText({
      displayName: "Domain Name",
      description: "The domain identifier for the AI profile",
      required: !1
    }),
    userName: import_pieces_framework5.Property.ShortText({
      displayName: "User Name",
      description: "Name of the user sending the request",
      required: !1
    }),
    sessionId: import_pieces_framework5.Property.ShortText({
      displayName: "Session ID",
      description: "Use the same sessionId to continue conversation on that session",
      required: !1
    }),
    sourceName: import_pieces_framework5.Property.ShortText({
      displayName: "Source Name",
      description: "Name of the source app of the inbound training",
      required: !1
    }),
    isStack: import_pieces_framework5.Property.Checkbox({
      displayName: "Add to Memory",
      description: "Flag to also add the training instruction to memory",
      required: !1,
      defaultValue: !1
    }),
    isDraft: import_pieces_framework5.Property.Checkbox({
      displayName: "Create Draft",
      description: "Flag to create a copilot message for the AI",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { auth, propsValue: { text, context: messageContext, domainName, userName, sessionId, sourceName, isStack, isDraft } } = context;
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: `${BASE_URL}/v1/training?cmd=custom`,
      headers: {
        "Content-Type": "application/json",
        "x-api-key": auth.secret_text
      },
      body: __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({
        Text: text
      }, messageContext && { Context: messageContext }), domainName && { DomainName: domainName }), userName && { UserName: userName }), sessionId && { SessionId: sessionId }), sourceName && { SourceName: sourceName }), isStack !== void 0 && { is_stack: isStack }), isDraft !== void 0 && { is_draft: isDraft })
    })).body;
  }
});

// ../ap/packages/pieces/community/personal-ai/src/lib/actions/messaging/get-conversation.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var getConversation = (0, import_pieces_framework6.createAction)({
  auth: personalAiAuth,
  name: "get_conversation",
  displayName: "Get Conversation History",
  description: "Retrieve conversation history from AI assistant.",
  audience: "both",
  aiMetadata: { description: "Retrieve the message history for a conversation channel from the Personal AI assistant, with optional filtering by session/source and limit/skip pagination. Use when you need to read past messages rather than send a new one. The required channel ID identifies the conversation; this is a read-only lookup and is idempotent.", idempotent: !0 },
  // category: 'Messaging',
  props: {
    channelId: import_pieces_framework6.Property.ShortText({
      displayName: "Channel ID",
      description: "The unique identifier for the conversation channel",
      required: !0
    }),
    domainName: import_pieces_framework6.Property.ShortText({
      displayName: "Domain Name",
      description: "The domain identifier for the AI profile",
      required: !1
    }),
    userName: import_pieces_framework6.Property.ShortText({
      displayName: "User Name",
      description: "Name of the user requesting the conversation",
      required: !1
    }),
    sessionId: import_pieces_framework6.Property.ShortText({
      displayName: "Session ID",
      description: "Filter conversation by specific session ID",
      required: !1
    }),
    sourceName: import_pieces_framework6.Property.ShortText({
      displayName: "Source Name",
      description: "Filter conversation by source application",
      required: !1
    }),
    limit: import_pieces_framework6.Property.Number({
      displayName: "Limit",
      description: "Maximum number of messages to return",
      required: !1
    }),
    skip: import_pieces_framework6.Property.Number({
      displayName: "Skip",
      description: "Number of messages to skip (for pagination)",
      required: !1
    })
  },
  async run(context) {
    let { auth, propsValue: { channelId, domainName, userName, sessionId, sourceName, limit, skip } } = context;
    return (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.POST,
      url: `${BASE_URL}/v1/conversation`,
      headers: {
        "Content-Type": "application/json",
        "x-api-key": auth.secret_text
      },
      body: __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({
        ChannelId: channelId
      }, domainName && { DomainName: domainName }), userName && { UserName: userName }), sessionId && { SessionId: sessionId }), sourceName && { SourceName: sourceName }), limit !== void 0 && { Limit: limit }), skip !== void 0 && { Skip: skip })
    })).body;
  }
});

// ../ap/packages/pieces/community/personal-ai/src/lib/actions/documents/upload-document.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var uploadDocument = (0, import_pieces_framework7.createAction)({
  auth: personalAiAuth,
  name: "upload_document",
  displayName: "Upload Document",
  description: "Upload a text document to AI assistant.",
  audience: "both",
  aiMetadata: { description: "Upload a text document (raw text plus a title) to the Personal AI assistant so it can reference the content, with optional tags, source, and add-to-memory flag. Use when the source is plain text already in hand; use Upload File for binary files or Upload URL Content to fetch from a link. Each call creates a new document, so it is not idempotent.", idempotent: !1 },
  // category: 'Documents',
  props: {
    text: import_pieces_framework7.Property.LongText({
      displayName: "Document Text",
      description: "The text content of the document to upload",
      required: !0
    }),
    title: import_pieces_framework7.Property.ShortText({
      displayName: "Document Title",
      description: "Title of the document",
      required: !0
    }),
    domainName: import_pieces_framework7.Property.ShortText({
      displayName: "Domain Name",
      description: "The domain identifier for the AI profile",
      required: !1
    }),
    tags: import_pieces_framework7.Property.ShortText({
      displayName: "Tags",
      description: "Comma delimited list of tags for the document",
      required: !1
    }),
    sourceName: import_pieces_framework7.Property.ShortText({
      displayName: "Source Name",
      description: "Name of the source or application",
      required: !1
    }),
    createdTime: import_pieces_framework7.Property.ShortText({
      displayName: "Created Time",
      description: "Time (including timezone) of the document creation (e.g., Wed, 19 Sep 2023 13:31:00 PDT)",
      required: !1
    }),
    isStack: import_pieces_framework7.Property.Checkbox({
      displayName: "Add to Memory",
      description: "Flag to also add the document to memory",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { auth, propsValue: { text, title, domainName, tags, sourceName, createdTime, isStack } } = context;
    return (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.POST,
      url: `${BASE_URL}/v1/upload-text`,
      headers: {
        "Content-Type": "application/json",
        "x-api-key": auth.secret_text
      },
      body: __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({
        Text: text,
        Title: title
      }, domainName && { DomainName: domainName }), tags && { Tags: tags }), sourceName && { SourceName: sourceName }), createdTime && { CreatedTime: createdTime }), isStack !== void 0 && { is_stack: isStack })
    })).body;
  }
});

// ../ap/packages/pieces/community/personal-ai/src/lib/actions/documents/upload-file.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var uploadFile = (0, import_pieces_framework8.createAction)({
  auth: personalAiAuth,
  name: "upload_file",
  displayName: "Upload File",
  description: "Upload a file to AI assistant.",
  audience: "both",
  aiMetadata: { description: "Upload a binary file (sent as multipart form data) to the Personal AI assistant so it can reference the content, with optional tags, source, and add-to-memory flag. Use when the source is an actual file; use Upload Document for plain text or Upload URL Content to fetch from a link. Each call creates a new uploaded file, so it is not idempotent.", idempotent: !1 },
  // category: 'Documents',
  props: {
    file: import_pieces_framework8.Property.File({
      displayName: "File",
      description: "The file to upload",
      required: !0
    }),
    fileName: import_pieces_framework8.Property.ShortText({
      displayName: "File Name",
      description: "Name of the file to be uploaded",
      required: !0
    }),
    domainName: import_pieces_framework8.Property.ShortText({
      displayName: "Domain Name",
      description: "The domain identifier for the AI profile",
      required: !1
    }),
    tags: import_pieces_framework8.Property.ShortText({
      displayName: "Tags",
      description: "Comma delimited list of tags for the file",
      required: !1
    }),
    sourceName: import_pieces_framework8.Property.ShortText({
      displayName: "Source Name",
      description: "Name of the source or application",
      required: !1
    }),
    createdTime: import_pieces_framework8.Property.ShortText({
      displayName: "Created Time",
      description: "Time (including timezone) of the file creation (e.g., Wed, 19 Sep 2023 13:31:00 PDT)",
      required: !1
    }),
    isStack: import_pieces_framework8.Property.Checkbox({
      displayName: "Add to Memory",
      description: "Flag to also add the file content to memory",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { auth, propsValue: { file, fileName, domainName, tags, sourceName, createdTime, isStack } } = context, formData = new FormData(), blob = new Blob([file.data], { type: "application/octet-stream" });
    return formData.append("file", blob, fileName), domainName && formData.append("DomainName", domainName), tags && formData.append("Tags", tags), sourceName && formData.append("SourceName", sourceName), createdTime && formData.append("CreatedTime", createdTime), isStack !== void 0 && formData.append("is_stack", isStack.toString()), (await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.POST,
      url: `${BASE_URL}/upload-file`,
      headers: {
        "x-api-key": auth.secret_text
      },
      body: formData
    })).body;
  }
});

// ../ap/packages/pieces/community/personal-ai/src/lib/actions/documents/upload-url.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var uploadUrl = (0, import_pieces_framework9.createAction)({
  auth: personalAiAuth,
  name: "upload_url",
  displayName: "Upload URL Content",
  description: "Upload content from a URL to AI assistant.",
  audience: "both",
  aiMetadata: { description: "Fetch the content at a given URL and upload it to the Personal AI assistant under a title, with optional tags, source, and add-to-memory flag. Use when the source is a web link; use Upload Document for plain text or Upload File for binary files in hand. Each call creates a new entry, so it is not idempotent.", idempotent: !1 },
  // category: 'Documents',
  props: {
    url: import_pieces_framework9.Property.ShortText({
      displayName: "URL",
      description: "The URL of the content to upload",
      required: !0
    }),
    title: import_pieces_framework9.Property.ShortText({
      displayName: "Title",
      description: "Title for the uploaded content",
      required: !0
    }),
    domainName: import_pieces_framework9.Property.ShortText({
      displayName: "Domain Name",
      description: "The domain identifier for the AI profile",
      required: !1
    }),
    tags: import_pieces_framework9.Property.ShortText({
      displayName: "Tags",
      description: "Comma delimited list of tags for the content",
      required: !1
    }),
    sourceName: import_pieces_framework9.Property.ShortText({
      displayName: "Source Name",
      description: "Name of the source or application",
      required: !1
    }),
    createdTime: import_pieces_framework9.Property.ShortText({
      displayName: "Created Time",
      description: "Time (including timezone) of the content creation (e.g., Wed, 19 Sep 2023 13:31:00 PDT)",
      required: !1
    }),
    isStack: import_pieces_framework9.Property.Checkbox({
      displayName: "Add to Memory",
      description: "Flag to also add the content to memory",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { auth, propsValue: { url, title, domainName, tags, sourceName, createdTime, isStack } } = context;
    return (await import_pieces_common8.httpClient.sendRequest({
      method: import_pieces_common8.HttpMethod.POST,
      url: `${BASE_URL}/upload-url`,
      headers: {
        "Content-Type": "application/json",
        "x-api-key": auth.secret_text
      },
      body: __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({
        Url: url,
        Title: title
      }, domainName && { DomainName: domainName }), tags && { Tags: tags }), sourceName && { SourceName: sourceName }), createdTime && { CreatedTime: createdTime }), isStack !== void 0 && { is_stack: isStack })
    })).body;
  }
});

// ../ap/packages/pieces/community/personal-ai/src/lib/actions/documents/update-document.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var updateDocument = (0, import_pieces_framework10.createAction)({
  auth: personalAiAuth,
  name: "update_document",
  displayName: "Update Document",
  description: "Update an existing document in AI assistant.",
  audience: "both",
  aiMetadata: { description: "Overwrite an existing Personal AI document, identified by its document ID, with new text and optional title/tags/source. Use when you need to revise a known document rather than create a new one; the document ID must already exist. Because it replaces the document keyed on a stable ID, repeating the same call yields the same result and is idempotent.", idempotent: !0 },
  // category: 'Documents',
  props: {
    documentId: import_pieces_framework10.Property.ShortText({
      displayName: "Document ID",
      description: "The unique identifier of the document to update",
      required: !0
    }),
    text: import_pieces_framework10.Property.LongText({
      displayName: "Document Text",
      description: "The updated text content of the document",
      required: !0
    }),
    title: import_pieces_framework10.Property.ShortText({
      displayName: "Document Title",
      description: "Updated title of the document",
      required: !1
    }),
    domainName: import_pieces_framework10.Property.ShortText({
      displayName: "Domain Name",
      description: "The domain identifier for the AI profile",
      required: !1
    }),
    tags: import_pieces_framework10.Property.ShortText({
      displayName: "Tags",
      description: "Comma delimited list of tags for the document",
      required: !1
    }),
    sourceName: import_pieces_framework10.Property.ShortText({
      displayName: "Source Name",
      description: "Name of the source or application",
      required: !1
    }),
    createdTime: import_pieces_framework10.Property.ShortText({
      displayName: "Created Time",
      description: "Time (including timezone) of the document creation (e.g., Wed, 19 Sep 2023 13:31:00 PDT)",
      required: !1
    }),
    isStack: import_pieces_framework10.Property.Checkbox({
      displayName: "Add to Memory",
      description: "Flag to also add the document to memory",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { auth, propsValue: { documentId, text, title, domainName, tags, sourceName, createdTime, isStack } } = context;
    return (await import_pieces_common9.httpClient.sendRequest({
      method: import_pieces_common9.HttpMethod.PUT,
      url: `${BASE_URL}/update-document`,
      headers: {
        "Content-Type": "application/json",
        "x-api-key": auth.secret_text
      },
      body: __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({
        DocumentId: documentId,
        Text: text
      }, title && { Title: title }), domainName && { DomainName: domainName }), tags && { Tags: tags }), sourceName && { SourceName: sourceName }), createdTime && { CreatedTime: createdTime }), isStack !== void 0 && { is_stack: isStack })
    })).body;
  }
});

// ../ap/packages/pieces/community/personal-ai/src/lib/actions/documents/get-document.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var getDocument = (0, import_pieces_framework11.createAction)({
  auth: personalAiAuth,
  name: "get_document",
  displayName: "Get Document",
  description: "Retrieve a document from AI assistant.",
  audience: "both",
  aiMetadata: { description: "Fetch a single Personal AI document by its document ID, optionally including the full content. Use when you need to read back a previously uploaded document. The document ID is required; this is a read-only lookup and is idempotent.", idempotent: !0 },
  // category: 'Documents',
  props: {
    documentId: import_pieces_framework11.Property.ShortText({
      displayName: "Document ID",
      description: "The unique identifier of the document to retrieve",
      required: !0
    }),
    domainName: import_pieces_framework11.Property.ShortText({
      displayName: "Domain Name",
      description: "The domain identifier for the AI profile",
      required: !1
    }),
    includeContent: import_pieces_framework11.Property.Checkbox({
      displayName: "Include Content",
      description: "Flag to include the document content in the response",
      required: !1,
      defaultValue: !0
    })
  },
  async run(context) {
    let { auth, propsValue: { documentId, domainName, includeContent } } = context;
    return (await import_pieces_common10.httpClient.sendRequest({
      method: import_pieces_common10.HttpMethod.GET,
      url: `${BASE_URL}/get-document`,
      headers: {
        "Content-Type": "application/json",
        "x-api-key": auth.secret_text
      },
      queryParams: __spreadValues(__spreadValues({
        DocumentId: documentId
      }, domainName && { DomainName: domainName }), includeContent !== void 0 && { IncludeContent: includeContent.toString() })
    })).body;
  }
});

// ../ap/packages/pieces/community/personal-ai/src/index.ts
var BASE_URL = "https://api.personal.ai", aiAssistant = (0, import_pieces_framework12.createPiece)({
  displayName: "Personal AI",
  description: "Manage memory storage, messaging, and documents through AI integration.",
  logoUrl: "https://cdn.activepieces.com/pieces/personal-ai.png",
  auth: personalAiAuth,
  categories: [import_pieces_framework13.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  minimumSupportedRelease: "0.20.0",
  authors: ["reemayoush"],
  actions: [
    createMemory,
    createMessage,
    createChatGPTInstruction,
    createCustomTraining,
    getConversation,
    uploadDocument,
    uploadFile,
    uploadUrl,
    updateDocument,
    getDocument
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  BASE_URL,
  aiAssistant
});
