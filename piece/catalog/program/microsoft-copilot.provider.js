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

// ../ap/packages/pieces/community/microsoft-copilot/src/index.ts
var index_exports = {};
__export(index_exports, {
  microsoft365Copilot: () => microsoft365Copilot
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/microsoft-copilot/src/lib/common/auth.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/microsoft-copilot/src/lib/common/microsoft-cloud.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), COMMERCIAL_LOGIN_HOST = "login.microsoftonline.com", GOV_LOGIN_HOST = "login.microsoftonline.us", GRAPH_BASE_URLS = {
  [COMMERCIAL_LOGIN_HOST]: "https://graph.microsoft.com",
  [GOV_LOGIN_HOST]: "https://graph.microsoft.us"
}, microsoftCloudProperty = import_pieces_framework.Property.StaticDropdown({
  displayName: "Cloud Environment",
  description: "Select your Microsoft cloud environment.",
  required: !0,
  defaultValue: COMMERCIAL_LOGIN_HOST,
  options: {
    disabled: !1,
    options: [
      { label: "Commercial (Global)", value: COMMERCIAL_LOGIN_HOST },
      { label: "US Government (GCC High)", value: GOV_LOGIN_HOST }
    ]
  }
});
function resolveCloudHost(cloudLoginHost) {
  return cloudLoginHost != null ? cloudLoginHost : COMMERCIAL_LOGIN_HOST;
}
function getMicrosoftCloudFromAuth(auth) {
  var _a;
  return resolveCloudHost((_a = auth.props) == null ? void 0 : _a.cloud);
}
function getGraphBaseUrl(cloudLoginHost) {
  var _a;
  let host = resolveCloudHost(cloudLoginHost);
  return (_a = GRAPH_BASE_URLS[host]) != null ? _a : GRAPH_BASE_URLS[COMMERCIAL_LOGIN_HOST];
}

// ../ap/packages/pieces/community/microsoft-copilot/src/lib/common/auth.ts
var authDesc = `
**Note:** This piece requires a **Microsoft 365 Copilot license** to access Copilot interaction data.

If you\u2019d like to use your own custom Azure app instead of the default app, follow the [Azure app creation guide](https://learn.microsoft.com/en-us/entra/identity-platform/quickstart-register-app#register-an-application),
 set the **Redirect URI** to {{redirectUrl}} and add the following **Microsoft Graph (Delegated) permissions** under **API permissions**:
 - Sites.Read.All
 - Mail.Read
 - Files.ReadWrite.All
 - People.Read.All
 - OnlineMeetingTranscript.Read.All
 - Chat.Read
 - ChannelMessage.Read.All
 - ExternalItem.Read.All
 - AiEnterpriseInteraction.Read
 - Files.Read.All`, microsoft365CopilotAuth = import_pieces_framework2.PieceAuth.OAuth2({
  description: authDesc,
  props: {
    cloud: microsoftCloudProperty
  },
  authUrl: "https://{cloud}/common/oauth2/v2.0/authorize",
  tokenUrl: "https://{cloud}/common/oauth2/v2.0/token",
  required: !0,
  scope: [
    "Sites.Read.All",
    "Mail.Read",
    "Files.ReadWrite.All",
    "People.Read.All",
    "OnlineMeetingTranscript.Read.All",
    "Chat.Read",
    "ChannelMessage.Read.All",
    "ExternalItem.Read.All",
    "AiEnterpriseInteraction.Read",
    "Files.Read.All",
    "openid",
    "email",
    "profile"
  ],
  prompt: "omit"
});

// ../ap/packages/pieces/community/microsoft-copilot/src/lib/actions/chat-with-copilot.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common = require("@activepieces/pieces-common");
var chatWithCopilot = (0, import_pieces_framework3.createAction)({
  audience: "both",
  auth: microsoft365CopilotAuth,
  name: "chatWithCopilot",
  displayName: "Chat with Copilot",
  description: "Send a message to an existing Copilot conversation or creating a new one and get a response",
  aiMetadata: {
    description: "Sends a prompt to Microsoft 365 Copilot and returns its reply, running in one of two modes: continuing an existing thread when a conversation ID is supplied, or silently creating a new conversation first when it is omitted. Use it when you want Copilot itself to reason over Microsoft 365 data and answer; prefer Search Copilot when you only need the matching OneDrive files back. Requires the message text and an IANA time zone, and can optionally ground the answer in web search. Not idempotent: each call posts a new message and, without a conversation ID, creates another conversation.",
    idempotent: !1
  },
  props: {
    conversationId: import_pieces_framework3.Property.ShortText({
      displayName: "Conversation ID",
      description: "The ID of an existing conversation. If not provided, a new conversation will be created.",
      required: !1
    }),
    messageText: import_pieces_framework3.Property.LongText({
      displayName: "Message Text",
      required: !0
    }),
    timeZone: import_pieces_framework3.Property.ShortText({
      displayName: "Time Zone",
      description: "User location time zone in IANA format (e.g., America/New_York, Europe/London)",
      required: !0,
      defaultValue: "UTC"
    }),
    // additionalContext: Property.Json({
    //   displayName: 'Additional Context (optional)',
    //   description:
    //     'Array of context objects with "text" and optional "description" properties',
    //   required: false,
    // }),
    // contextFiles: Property.Json({
    //   displayName: 'Context Files (optional)',
    //   description:
    //     'Array of file objects with "uri" property pointing to OneDrive/SharePoint files',
    //   required: false,
    // }),
    enableWebSearch: import_pieces_framework3.Property.Checkbox({
      displayName: "Enable Web Search Grounding",
      required: !1,
      defaultValue: !0
    })
  },
  async run(context) {
    let {
      conversationId,
      messageText,
      timeZone,
      // additionalContext,
      // contextFiles,
      enableWebSearch
    } = context.propsValue, cloud = getMicrosoftCloudFromAuth(context.auth), graphBaseUrl = getGraphBaseUrl(cloud), activeConversationId = conversationId;
    activeConversationId || (activeConversationId = (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: `${graphBaseUrl}/beta/copilot/conversations`,
      headers: {
        Authorization: `Bearer ${context.auth.access_token}`,
        "Content-Type": "application/json"
      },
      body: {}
    })).body.id);
    let body = {
      message: {
        text: messageText
      },
      locationHint: {
        timeZone
      }
    }, contextualResources = {};
    return enableWebSearch !== void 0 && (contextualResources.webContext = {
      isWebEnabled: enableWebSearch
    }), Object.keys(contextualResources).length > 0 && (body.contextualResources = contextualResources), (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: `${graphBaseUrl}/beta/copilot/conversations/${activeConversationId}/chat`,
      headers: {
        Authorization: `Bearer ${context.auth.access_token}`,
        "Content-Type": "application/json"
      },
      body: JSON.stringify(body)
    })).body.messages[1];
  }
});

// ../ap/packages/pieces/community/microsoft-copilot/src/lib/actions/search-copilot.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common");
var searchCopilot = (0, import_pieces_framework4.createAction)({
  audience: "both",
  auth: microsoft365CopilotAuth,
  name: "searchCopilot",
  displayName: "Search Copilot",
  description: "Perform hybrid (semantic and lexical) search across OneDrive for work or school content using natural language queries",
  aiMetadata: {
    description: "Runs a hybrid semantic and lexical search over the connected user's OneDrive for work or school content from a natural-language query, optionally scoped by a KQL filter expression. Use it to find matching files in OneDrive; prefer Chat with Copilot when you want a synthesized answer rather than a list of results. Requires a query of at most 1,500 characters. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    query: import_pieces_framework4.Property.LongText({
      displayName: "Search Query",
      description: "Natural language query to search for relevant files. Maximum 1,500 characters.",
      required: !0
    }),
    filterExpression: import_pieces_framework4.Property.LongText({
      displayName: "Filter Expression (Optional)",
      description: 'KQL filter expression for OneDrive data source (https://learn.microsoft.com/en-us/sharepoint/dev/general-development/keyword-query-language-kql-syntax-reference). Example: path:"https://contoso-my.sharepoint.com/personal/user/Documents/"',
      required: !1
    }),
    resourceMetadata: import_pieces_framework4.Property.Json({
      displayName: "Resource Metadata Names (Optional)",
      description: 'Array of metadata field names to include in results. Example: ["title", "author"]',
      required: !1
    })
  },
  async run(context) {
    let { query, filterExpression, resourceMetadata } = context.propsValue, cloud = getMicrosoftCloudFromAuth(context.auth), graphBaseUrl = getGraphBaseUrl(cloud), requestBody = {
      query
    };
    if ((filterExpression || resourceMetadata) && (requestBody.dataSources = {
      oneDrive: {}
    }, filterExpression && (requestBody.dataSources.oneDrive.filterExpression = filterExpression), resourceMetadata)) {
      let metadataArray = Array.isArray(resourceMetadata) ? resourceMetadata : typeof resourceMetadata == "string" ? JSON.parse(resourceMetadata) : [];
      metadataArray.length > 0 && (requestBody.dataSources.oneDrive.resourceMetadataNames = metadataArray);
    }
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: `${graphBaseUrl}/beta/copilot/search`,
      headers: {
        Authorization: `Bearer ${context.auth.access_token}`,
        "Content-Type": "application/json"
      },
      body: JSON.stringify(requestBody)
    })).body;
  }
});

// ../ap/packages/pieces/community/microsoft-copilot/src/index.ts
var import_pieces_common3 = require("@activepieces/pieces-common");
var microsoft365Copilot = (0, import_pieces_framework5.createPiece)({
  displayName: "Microsoft 365 Copilot",
  auth: microsoft365CopilotAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/microsoft-copilot.png",
  authors: ["sanket-a11y"],
  actions: [
    chatWithCopilot,
    searchCopilot,
    // retrieveGroundingData,
    (0, import_pieces_common3.createCustomApiCallAction)({
      auth: microsoft365CopilotAuth,
      baseUrl: (auth) => {
        let cloud = getMicrosoftCloudFromAuth(auth);
        return getGraphBaseUrl(cloud) + "/";
      },
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.access_token}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  microsoft365Copilot
});
