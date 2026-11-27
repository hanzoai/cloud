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

// ../ap/packages/pieces/community/qwilr/src/index.ts
var index_exports = {};
__export(index_exports, {
  qwilr: () => qwilr
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_framework11 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/qwilr/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), qwilrAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: `
    1. Go to your Qwilr account settings.
    2. Navigate to API Settings.
    3. Copy your access token.`
});

// ../ap/packages/pieces/community/qwilr/src/lib/actions/create-page.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var createPageAction = (0, import_pieces_framework2.createAction)({
  name: "create_page",
  displayName: "Create Page",
  description: "Creates a new Qwilr page from saved blocks or template.",
  audience: "both",
  aiMetadata: { description: "Creates a new Qwilr sales/proposal page from an existing template (template ID required), optionally publishing it live or leaving it as a draft, and applying token substitutions, tags, owner, and accept-block settings. Use to programmatically generate a personalized proposal or quote from a template. Not idempotent: each call creates a separate page.", idempotent: !1 },
  auth: qwilrAuth,
  props: {
    templateId: import_pieces_framework2.Property.ShortText({
      displayName: "Template ID",
      description: "ID of the template to use for the page",
      required: !0
    }),
    name: import_pieces_framework2.Property.ShortText({
      displayName: "Page Name",
      description: "Title of the page, visible as the browser page title",
      required: !1
    }),
    published: import_pieces_framework2.Property.Checkbox({
      displayName: "Publish Page",
      description: "Whether the page is publicly available; false means the page will be in Draft status",
      required: !1,
      defaultValue: !1
    }),
    ownerId: import_pieces_framework2.Property.ShortText({
      displayName: "Owner ID",
      description: "ID of the user that should own the page. If not specified, the owner of the access token will be the owner of the page.",
      required: !1
    }),
    tags: import_pieces_framework2.Property.Array({
      displayName: "Tags",
      description: "The tags for your page. Tags are case-sensitive.",
      required: !1
    }),
    metadata: import_pieces_framework2.Property.Json({
      displayName: "Metadata",
      description: "Data you provide, that will be returned as part of all Webhooks.",
      required: !1
    }),
    substitutions: import_pieces_framework2.Property.Json({
      displayName: "Substitutions",
      description: "Mapping of token API reference keys to substitution values used throughout the page.",
      required: !1
    }),
    acceptSettings: import_pieces_framework2.Property.Object({
      displayName: "Accept Settings",
      description: "Configures settings for the accept block in the template being used. Only relevant if you have an accept block.",
      required: !1
    })
  },
  async run(context) {
    let { templateId, name, published, ownerId, tags, metadata, substitutions, acceptSettings } = context.propsValue, auth = context.auth, requestBody = {
      templateId
    };
    return name !== void 0 && (requestBody.name = name), published !== void 0 && (requestBody.published = published), ownerId !== void 0 && (requestBody.ownerId = ownerId), tags && tags.length > 0 && (requestBody.tags = tags), metadata !== void 0 && (requestBody.metadata = metadata), substitutions !== void 0 && (requestBody.substitutions = substitutions), acceptSettings !== void 0 && (requestBody.acceptSettings = acceptSettings), (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://api.qwilr.com/v1/pages",
      headers: {
        Authorization: `Bearer ${auth}`,
        "Content-Type": "application/json"
      },
      body: requestBody
    })).body;
  }
});

// ../ap/packages/pieces/community/qwilr/src/index.ts
var import_pieces_common9 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/qwilr/src/lib/triggers/page-accepted.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var subscribeWebhook = async (auth, event, targetUrl) => (await import_pieces_common2.httpClient.sendRequest({
  method: import_pieces_common2.HttpMethod.POST,
  url: "https://api.qwilr.com/v1/webhooks",
  headers: {
    Authorization: `Bearer ${auth}`,
    "Content-Type": "application/json"
  },
  body: {
    event,
    targetUrl
  }
})).body, unsubscribeWebhook = async (auth, subscriptionId) => {
  await import_pieces_common2.httpClient.sendRequest({
    method: import_pieces_common2.HttpMethod.DELETE,
    url: `https://api.qwilr.com/v1/webhooks/${subscriptionId}`,
    headers: {
      Authorization: `Bearer ${auth}`
    }
  });
}, pageAcceptedTrigger = (0, import_pieces_framework3.createTrigger)({
  auth: qwilrAuth,
  name: "page_accepted",
  displayName: "Page Accepted",
  description: "Triggers when a Qwilr page is accepted",
  aiMetadata: {
    description: "Fires when a recipient fully accepts a Qwilr page (all accept blocks signed off), signaling the proposal or quote has been agreed to. Use to kick off downstream deal-won workflows such as invoicing or CRM updates."
  },
  type: import_pieces_framework3.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    var _a;
    let webhookData = await subscribeWebhook(
      context.auth.secret_text,
      "pageAccepted",
      context.webhookUrl
    );
    await ((_a = context.store) == null ? void 0 : _a.put("_page_accepted_trigger", {
      subscriptionId: webhookData.id
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get("_page_accepted_trigger"));
    response != null && await unsubscribeWebhook(context.auth.secret_text, response.subscriptionId);
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    event: "pageAccepted",
    idempotencyKey: "b1c51b5b4844a81f7c3745002dbbd871fdda6cabe019961bcc79f972d924a2b0",
    page: {
      id: "6ee0f841f3cc8900090d82dc",
      metadata: {}
    }
  }
});

// ../ap/packages/pieces/community/qwilr/src/lib/triggers/page-partially-accepted.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var subscribeWebhook2 = async (auth, event, targetUrl) => (await import_pieces_common3.httpClient.sendRequest({
  method: import_pieces_common3.HttpMethod.POST,
  url: "https://api.qwilr.com/v1/webhooks",
  headers: {
    Authorization: `Bearer ${auth}`,
    "Content-Type": "application/json"
  },
  body: {
    event,
    targetUrl
  }
})).body, unsubscribeWebhook2 = async (auth, subscriptionId) => {
  await import_pieces_common3.httpClient.sendRequest({
    method: import_pieces_common3.HttpMethod.DELETE,
    url: `https://api.qwilr.com/v1/webhooks/${subscriptionId}`,
    headers: {
      Authorization: `Bearer ${auth}`
    }
  });
}, pagePartiallyAcceptedTrigger = (0, import_pieces_framework4.createTrigger)({
  auth: qwilrAuth,
  name: "page_partially_accepted",
  displayName: "Page Partially Accepted",
  description: "Triggers when a Qwilr page is partially accepted",
  aiMetadata: {
    description: "Fires when a recipient partially accepts a Qwilr page (some but not all accept blocks completed), indicating partial agreement on a multi-option proposal. Use to track in-progress deals or nudge for full acceptance."
  },
  type: import_pieces_framework4.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    var _a;
    let webhookData = await subscribeWebhook2(
      context.auth.secret_text,
      "pagePartiallyAccepted",
      context.webhookUrl
    );
    await ((_a = context.store) == null ? void 0 : _a.put("_page_partially_accepted_trigger", {
      subscriptionId: webhookData.id
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get("_page_partially_accepted_trigger"));
    response != null && await unsubscribeWebhook2(context.auth.secret_text, response.subscriptionId);
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    event: "pagePartiallyAccepted",
    idempotencyKey: "b1c51b5b4844a81f7c3745002dbbd871fdda6cabe019961bcc79f972d924a2b0",
    page: {
      id: "6ee0f841f3cc8900090d82dc",
      metadata: {}
    }
  }
});

// ../ap/packages/pieces/community/qwilr/src/lib/triggers/page-preview-accepted.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var subscribeWebhook3 = async (auth, event, targetUrl) => (await import_pieces_common4.httpClient.sendRequest({
  method: import_pieces_common4.HttpMethod.POST,
  url: "https://api.qwilr.com/v1/webhooks",
  headers: {
    Authorization: `Bearer ${auth}`,
    "Content-Type": "application/json"
  },
  body: {
    event,
    targetUrl
  }
})).body, unsubscribeWebhook3 = async (auth, subscriptionId) => {
  await import_pieces_common4.httpClient.sendRequest({
    method: import_pieces_common4.HttpMethod.DELETE,
    url: `https://api.qwilr.com/v1/webhooks/${subscriptionId}`,
    headers: {
      Authorization: `Bearer ${auth}`
    }
  });
}, pagePreviewAcceptedTrigger = (0, import_pieces_framework5.createTrigger)({
  auth: qwilrAuth,
  name: "page_preview_accepted",
  displayName: "Page Preview Accepted",
  description: "Triggers when a Qwilr page preview is accepted",
  aiMetadata: {
    description: "Fires when a page is accepted while in preview (test) mode rather than as a live recipient acceptance. Use to validate acceptance workflows during testing without reacting to a real signed proposal."
  },
  type: import_pieces_framework5.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    var _a;
    let webhookData = await subscribeWebhook3(
      context.auth.secret_text,
      "pagePreviewAccepted",
      context.webhookUrl
    );
    await ((_a = context.store) == null ? void 0 : _a.put("_page_preview_accepted_trigger", {
      subscriptionId: webhookData.id
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get("_page_preview_accepted_trigger"));
    response != null && await unsubscribeWebhook3(context.auth.secret_text, response.subscriptionId);
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    event: "pagePreviewAccepted",
    idempotencyKey: "b1c51b5b4844a81f7c3745002dbbd871fdda6cabe019961bcc79f972d924a2b0",
    page: {
      id: "6ee0f841f3cc8900090d82dc",
      metadata: {}
    }
  }
});

// ../ap/packages/pieces/community/qwilr/src/lib/triggers/page-viewed.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var subscribeWebhook4 = async (auth, event, targetUrl) => (await import_pieces_common5.httpClient.sendRequest({
  method: import_pieces_common5.HttpMethod.POST,
  url: "https://api.qwilr.com/v1/webhooks",
  headers: {
    Authorization: `Bearer ${auth}`,
    "Content-Type": "application/json"
  },
  body: {
    event,
    targetUrl
  }
})).body, unsubscribeWebhook4 = async (auth, subscriptionId) => {
  await import_pieces_common5.httpClient.sendRequest({
    method: import_pieces_common5.HttpMethod.DELETE,
    url: `https://api.qwilr.com/v1/webhooks/${subscriptionId}`,
    headers: {
      Authorization: `Bearer ${auth}`
    }
  });
}, pageViewedTrigger = (0, import_pieces_framework6.createTrigger)({
  auth: qwilrAuth,
  name: "page_viewed",
  displayName: "Page Viewed",
  description: "Triggers when a Qwilr page is viewed",
  aiMetadata: {
    description: "Fires every time a Qwilr page is opened by a viewer, including repeat views. Use to track engagement on a sent proposal; for only the very first open use the Page First Viewed trigger instead."
  },
  type: import_pieces_framework6.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    var _a;
    let webhookData = await subscribeWebhook4(
      context.auth.secret_text,
      "pageViewed",
      context.webhookUrl
    );
    await ((_a = context.store) == null ? void 0 : _a.put("_page_viewed_trigger", {
      subscriptionId: webhookData.id
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get("_page_viewed_trigger"));
    response != null && await unsubscribeWebhook4(context.auth.secret_text, response.subscriptionId);
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    event: "pageViewed",
    idempotencyKey: "b1c51b5b4844a81f7c3745002dbbd871fdda6cabe019961bcc79f972d924a2b0",
    page: {
      id: "6ee0f841f3cc8900090d82dc",
      metadata: {}
    }
  }
});

// ../ap/packages/pieces/community/qwilr/src/lib/triggers/page-first-viewed.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var subscribeWebhook5 = async (auth, event, targetUrl) => (await import_pieces_common6.httpClient.sendRequest({
  method: import_pieces_common6.HttpMethod.POST,
  url: "https://api.qwilr.com/v1/webhooks",
  headers: {
    Authorization: `Bearer ${auth}`,
    "Content-Type": "application/json"
  },
  body: {
    event,
    targetUrl
  }
})).body, unsubscribeWebhook5 = async (auth, subscriptionId) => {
  await import_pieces_common6.httpClient.sendRequest({
    method: import_pieces_common6.HttpMethod.DELETE,
    url: `https://api.qwilr.com/v1/webhooks/${subscriptionId}`,
    headers: {
      Authorization: `Bearer ${auth}`
    }
  });
}, pageFirstViewedTrigger = (0, import_pieces_framework7.createTrigger)({
  auth: qwilrAuth,
  name: "page_first_viewed",
  displayName: "Page First Viewed",
  description: "Triggers when a Qwilr page is viewed for the first time",
  aiMetadata: {
    description: "Fires only on the first time a Qwilr page is opened by a viewer, marking initial engagement with a sent proposal. Use to react once to a recipient first opening the document; for all subsequent opens use the Page Viewed trigger."
  },
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    var _a;
    let webhookData = await subscribeWebhook5(
      context.auth.secret_text,
      "pageFirstViewed",
      context.webhookUrl
    );
    await ((_a = context.store) == null ? void 0 : _a.put("_page_first_viewed_trigger", {
      subscriptionId: webhookData.id
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get("_page_first_viewed_trigger"));
    response != null && await unsubscribeWebhook5(context.auth.secret_text, response.subscriptionId);
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    event: "pageFirstViewed",
    idempotencyKey: "b1c51b5b4844a81f7c3745002dbbd871fdda6cabe019961bcc79f972d924a2b0",
    page: {
      id: "6ee0f841f3cc8900090d82dc",
      metadata: {}
    }
  }
});

// ../ap/packages/pieces/community/qwilr/src/lib/triggers/page-set-live.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var subscribeWebhook6 = async (auth, event, targetUrl) => (await import_pieces_common7.httpClient.sendRequest({
  method: import_pieces_common7.HttpMethod.POST,
  url: "https://api.qwilr.com/v1/webhooks",
  headers: {
    Authorization: `Bearer ${auth}`,
    "Content-Type": "application/json"
  },
  body: {
    event,
    targetUrl
  }
})).body, unsubscribeWebhook6 = async (auth, subscriptionId) => {
  await import_pieces_common7.httpClient.sendRequest({
    method: import_pieces_common7.HttpMethod.DELETE,
    url: `https://api.qwilr.com/v1/webhooks/${subscriptionId}`,
    headers: {
      Authorization: `Bearer ${auth}`
    }
  });
}, pageSetLiveTrigger = (0, import_pieces_framework8.createTrigger)({
  auth: qwilrAuth,
  name: "page_set_live",
  displayName: "Page Set Live",
  description: "Triggers when a Qwilr page is set to live",
  aiMetadata: {
    description: "Fires when a Qwilr page is published and set to live (made publicly accessible), transitioning out of draft status. Use to react when a proposal becomes ready to share with a recipient."
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    var _a;
    let webhookData = await subscribeWebhook6(
      context.auth.secret_text,
      "pageSetLive",
      context.webhookUrl
    );
    await ((_a = context.store) == null ? void 0 : _a.put("_page_set_live_trigger", {
      subscriptionId: webhookData.id
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get("_page_set_live_trigger"));
    response != null && await unsubscribeWebhook6(context.auth.secret_text, response.subscriptionId);
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    event: "pageSetLive",
    idempotencyKey: "b1c51b5b4844a81f7c3745002dbbd871fdda6cabe019961bcc79f972d924a2b0",
    page: {
      id: "6ee0f841f3cc8900090d82dc",
      metadata: {}
    }
  }
});

// ../ap/packages/pieces/community/qwilr/src/lib/triggers/page-revived-live.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var subscribeWebhook7 = async (auth, event, targetUrl) => (await import_pieces_common8.httpClient.sendRequest({
  method: import_pieces_common8.HttpMethod.POST,
  url: "https://api.qwilr.com/v1/webhooks",
  headers: {
    Authorization: `Bearer ${auth}`,
    "Content-Type": "application/json"
  },
  body: {
    event,
    targetUrl
  }
})).body, unsubscribeWebhook7 = async (auth, subscriptionId) => {
  await import_pieces_common8.httpClient.sendRequest({
    method: import_pieces_common8.HttpMethod.DELETE,
    url: `https://api.qwilr.com/v1/webhooks/${subscriptionId}`,
    headers: {
      Authorization: `Bearer ${auth}`
    }
  });
}, pageRevivedLiveTrigger = (0, import_pieces_framework9.createTrigger)({
  auth: qwilrAuth,
  name: "page_revived_live",
  displayName: "Page Revived Live",
  description: "Triggers when a Qwilr page is revived to live status",
  aiMetadata: {
    description: "Fires when a previously disabled or expired Qwilr page is revived back to live status, making it accessible again. Use to react when an old proposal is reactivated for a recipient."
  },
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    var _a;
    let webhookData = await subscribeWebhook7(
      context.auth.secret_text,
      "pageRevivedLive",
      context.webhookUrl
    );
    await ((_a = context.store) == null ? void 0 : _a.put("_page_revived_live_trigger", {
      subscriptionId: webhookData.id
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get("_page_revived_live_trigger"));
    response != null && await unsubscribeWebhook7(context.auth.secret_text, response.subscriptionId);
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    event: "pageRevivedLive",
    idempotencyKey: "b1c51b5b4844a81f7c3745002dbbd871fdda6cabe019961bcc79f972d924a2b0",
    page: {
      id: "6ee0f841f3cc8900090d82dc",
      metadata: {}
    }
  }
});

// ../ap/packages/pieces/community/qwilr/src/index.ts
var qwilr = (0, import_pieces_framework10.createPiece)({
  displayName: "Qwilr",
  description: "Create beautiful, interactive sales documents and proposals with Qwilr. Automate page creation, track views, and handle acceptance events.",
  auth: qwilrAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/qwilr.png",
  authors: ["onyedikachi-david"],
  categories: [import_pieces_framework11.PieceCategory.SALES_AND_CRM, import_pieces_framework11.PieceCategory.CONTENT_AND_FILES],
  actions: [
    createPageAction,
    (0, import_pieces_common9.createCustomApiCallAction)({
      auth: qwilrAuth,
      baseUrl: () => "https://api.qwilr.com/v1",
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth}`
      })
    })
  ],
  triggers: [
    pageAcceptedTrigger,
    pagePartiallyAcceptedTrigger,
    pagePreviewAcceptedTrigger,
    pageViewedTrigger,
    pageFirstViewedTrigger,
    pageSetLiveTrigger,
    pageRevivedLiveTrigger
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  qwilr
});
