"use strict";
var __defProp = Object.defineProperty, __defProps = Object.defineProperties, __getOwnPropDesc = Object.getOwnPropertyDescriptor, __getOwnPropDescs = Object.getOwnPropertyDescriptors, __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
}, __spreadProps = (a, b) => __defProps(a, __getOwnPropDescs(b));
var __objRest = (source, exclude) => {
  var target = {};
  for (var prop in source)
    __hasOwnProp.call(source, prop) && exclude.indexOf(prop) < 0 && (target[prop] = source[prop]);
  if (source != null && __getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(source))
      exclude.indexOf(prop) < 0 && __propIsEnum.call(source, prop) && (target[prop] = source[prop]);
  return target;
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

// ../ap/packages/pieces/community/parseur/src/index.ts
var index_exports = {};
__export(index_exports, {
  parseur: () => parseur
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework13 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/parseur/src/lib/actions/create-document.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/parseur/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");
var parseurAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Create a new API key in Account \u2192 API keys in the Parseur app: https://app.parseur.com/account/api-keys",
  required: !0
}), parseurCommon = {
  baseUrl: "https://api.parseur.com",
  endpoints: {
    listDocuments: (parserId) => `/parser/${parserId}/document_set`,
    getParsedDocumentById: (documentId) => `/document/${documentId}`,
    createDocument: "/email",
    createDocumentFromFile: (parserId) => `/parser/${parserId}/upload`,
    reprocessDocument: (documentId) => `/document/${documentId}/process`,
    listMailboxes: "/parser",
    createWebhook: "/webhook",
    enableWebhook: (webhookId, mailboxId) => `/parser/${mailboxId}/webhook_set/${webhookId}`,
    deleteWebhook: (webhookId) => `/webhook/${webhookId}`
  },
  getHeaders: (apiKey) => ({
    Authorization: apiKey,
    "Content-Type": "application/json"
  }),
  // API methods
  listDocuments: async (_a) => {
    var _b = _a, {
      apiKey,
      parserId
    } = _b, queryParams = __objRest(_b, [
      "apiKey",
      "parserId"
    ]);
    let _a2 = queryParams, { page, page_size, with_result } = _a2, rest = __objRest(_a2, ["page", "page_size", "with_result"]), parsedQuery = __spreadValues(__spreadValues(__spreadValues(__spreadValues({}, page != null ? { page: String(page) } : {}), page_size != null ? { page_size: String(page_size) } : {}), with_result != null ? { with_result: String(with_result) } : {}), rest);
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: parseurCommon.baseUrl + parseurCommon.endpoints.listDocuments(parserId),
      headers: parseurCommon.getHeaders(apiKey),
      queryParams: parsedQuery
    })).body;
  },
  getDocument: async ({ apiKey, documentId }) => (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: parseurCommon.baseUrl + parseurCommon.endpoints.getParsedDocumentById(documentId),
    headers: parseurCommon.getHeaders(apiKey)
  })).body,
  createDocument: async (_c) => {
    var _d = _c, {
      apiKey
    } = _d, documentParams = __objRest(_d, [
      "apiKey"
    ]);
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: parseurCommon.baseUrl + parseurCommon.endpoints.createDocument,
      headers: parseurCommon.getHeaders(apiKey),
      body: documentParams
    })).body;
  },
  createDocumentFromFile: async ({
    apiKey,
    file,
    parserId
  }) => {
    let data = new FormData(), uint8 = new Uint8Array(file.data);
    return data.append("file", new Blob([uint8]), file.filename), data.append("parserId", String(parserId)), (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: parseurCommon.baseUrl + parseurCommon.endpoints.createDocumentFromFile(parserId),
      headers: __spreadProps(__spreadValues({}, parseurCommon.getHeaders(apiKey)), {
        "Content-Type": "multipart/form-data"
      }),
      body: data
    })).body;
  },
  reprocessDocument: async ({
    apiKey,
    documentId
  }) => (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.POST,
    url: parseurCommon.baseUrl + parseurCommon.endpoints.reprocessDocument(documentId),
    headers: parseurCommon.getHeaders(apiKey)
  })).body,
  listMailboxes: async (_e) => {
    var _f = _e, { apiKey } = _f, queryParams = __objRest(_f, ["apiKey"]);
    let _a = queryParams, { page, page_size } = _a, rest = __objRest(_a, ["page", "page_size"]), parsedQuery = __spreadValues(__spreadValues(__spreadValues({}, page != null ? { page: String(page) } : {}), page_size != null ? { page_size: String(page_size) } : {}), rest);
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: parseurCommon.baseUrl + parseurCommon.endpoints.listMailboxes,
      headers: parseurCommon.getHeaders(apiKey),
      queryParams: parsedQuery
    })).body;
  },
  createWebhook: async (_g) => {
    var _h = _g, { apiKey } = _h, webhookParams = __objRest(_h, ["apiKey"]);
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: parseurCommon.baseUrl + parseurCommon.endpoints.createWebhook,
      headers: parseurCommon.getHeaders(apiKey),
      body: webhookParams
    })).body;
  },
  enableWebhook: async ({
    apiKey,
    webhookId,
    mailboxId
  }) => {
    let response = await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: parseurCommon.baseUrl + parseurCommon.endpoints.enableWebhook(webhookId, mailboxId),
      headers: parseurCommon.getHeaders(apiKey)
    });
    return response.status === 200 || response.status === 204 ? { success: !0 } : { success: !1 };
  },
  deleteWebhook: async ({ apiKey, webhookId }) => {
    let response = await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.DELETE,
      url: parseurCommon.baseUrl + parseurCommon.endpoints.deleteWebhook(webhookId),
      headers: parseurCommon.getHeaders(apiKey)
    });
    return response.status === 200 || response.status === 204 ? { success: !0 } : { success: !1 };
  }
};

// ../ap/packages/pieces/community/parseur/src/lib/actions/create-document.ts
var createDocument = (0, import_pieces_framework2.createAction)({
  auth: parseurAuth,
  name: "createDocument",
  displayName: "Create Document",
  description: "Creates a new document.",
  audience: "both",
  aiMetadata: {
    description: "Submits a new email-style document to Parseur for parsing by sending its subject, sender/recipient addresses, and HTML or plain-text body. Use to push raw email content into a Parseur mailbox so it can be parsed against templates. Each call creates a separate document, so it is not idempotent.",
    idempotent: !1
  },
  props: {
    subject: import_pieces_framework2.Property.ShortText({
      displayName: "Subject",
      description: "The subject of the document/email.",
      required: !0
    }),
    from: import_pieces_framework2.Property.ShortText({
      displayName: "From",
      description: "The sender email address.",
      required: !0
    }),
    recipient: import_pieces_framework2.Property.ShortText({
      displayName: "Recipient",
      description: "The recipient email address.",
      required: !0
    }),
    to: import_pieces_framework2.Property.ShortText({
      displayName: "To",
      description: 'The "To" email address.',
      required: !1
    }),
    cc: import_pieces_framework2.Property.ShortText({
      displayName: "CC",
      description: 'The "CC" email address.',
      required: !1
    }),
    bcc: import_pieces_framework2.Property.ShortText({
      displayName: "BCC",
      description: 'The "BCC" email address.',
      required: !1
    }),
    body_html: import_pieces_framework2.Property.LongText({
      displayName: "Body HTML",
      description: "The HTML content of the document/email.",
      required: !1
    }),
    body_plain: import_pieces_framework2.Property.LongText({
      displayName: "Body Plain",
      description: "The plain text content of the document/email.",
      required: !1
    }),
    message_headers: import_pieces_framework2.Property.Object({
      displayName: "Message Headers",
      description: "A JSON object representing the email headers (key-value pairs).",
      required: !1
    })
  },
  async run({ auth: apiKey, propsValue }) {
    let _a = propsValue, { message_headers } = _a, rest = __objRest(_a, ["message_headers"]);
    return await parseurCommon.createDocument(__spreadProps(__spreadValues({
      apiKey: apiKey.secret_text
    }, rest), {
      message_headers: message_headers ? Object.fromEntries(
        Object.entries(message_headers).map(([k, v]) => [k, String(v)])
      ) : void 0
    }));
  }
});

// ../ap/packages/pieces/community/parseur/src/lib/actions/create-document-from-file.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/parseur/src/lib/common/properties.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var parserDropdown = ({ required = !0 }) => import_pieces_framework3.Property.Dropdown({
  auth: parseurAuth,
  displayName: "Parser",
  description: "Select the parser",
  required,
  refreshers: ["auth"],
  refreshOnSearch: !0,
  options: async ({ auth: apiKey }, { searchValue: search }) => apiKey ? search != null ? {
    disabled: !1,
    options: (await parseurCommon.listMailboxes({
      apiKey: apiKey.secret_text,
      search
    })).results.map((parser) => ({
      label: parser.name,
      value: parser.id
    }))
  } : {
    disabled: !1,
    options: (await parseurCommon.listMailboxes({
      apiKey: apiKey.secret_text
    })).results.map((parser) => ({
      label: parser.name,
      value: parser.id
    }))
  } : {
    disabled: !0,
    placeholder: "Please select an API Key first",
    options: []
  }
}), documentDropdown = ({ required = !0 }) => import_pieces_framework3.Property.Dropdown({
  auth: parseurAuth,
  displayName: "Document",
  description: "Select the document",
  required,
  refreshers: ["auth", "parserId"],
  refreshOnSearch: !0,
  options: async ({ auth: apiKey, parserId }, { searchValue: search }) => apiKey ? parserId ? search != null ? {
    disabled: !1,
    options: (await parseurCommon.listDocuments({
      apiKey: apiKey.secret_text,
      parserId,
      search
    })).results.map((document) => ({
      label: document.name,
      value: document.id.toString()
    }))
  } : {
    disabled: !1,
    options: (await parseurCommon.listDocuments({
      apiKey: apiKey.secret_text,
      parserId
    })).results.map((document) => ({
      label: document.name,
      value: document.id.toString()
    }))
  } : {
    disabled: !0,
    placeholder: "Please select a Parser first",
    options: []
  } : {
    disabled: !0,
    placeholder: "Please select an API Key first",
    options: []
  }
});

// ../ap/packages/pieces/community/parseur/src/lib/actions/create-document-from-file.ts
var createDocumentFromFile = (0, import_pieces_framework4.createAction)({
  auth: parseurAuth,
  name: "createDocumentFromFile",
  displayName: "Create Document from File",
  description: "Creates new document in mailbox from file.",
  audience: "both",
  aiMetadata: {
    description: "Uploads a file (e.g. PDF, image, attachment) into a specified Parseur parser/mailbox as a new document for parsing. Use when the source is a binary file rather than email content. Requires the target parser ID and the file; each upload creates a separate document, so it is not idempotent.",
    idempotent: !1
  },
  props: {
    parserId: parserDropdown({ required: !0 }),
    file: import_pieces_framework4.Property.File({
      displayName: "File",
      description: "Select the file to upload",
      required: !0
    })
  },
  async run({ auth: apiKey, propsValue: { parserId, file } }) {
    if (!parserId)
      throw new Error("Parser is required");
    return await parseurCommon.createDocumentFromFile({
      apiKey: apiKey.secret_text,
      parserId,
      file
    });
  }
});

// ../ap/packages/pieces/community/parseur/src/lib/actions/find-document.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var findDocument = (0, import_pieces_framework5.createAction)({
  auth: parseurAuth,
  name: "findDocument",
  displayName: "Find Document",
  description: "Finds a document based on search param.",
  audience: "both",
  aiMetadata: {
    description: "Lists documents within a given Parseur parser/mailbox, optionally filtered by a case-insensitive name search term; with an empty search it returns all documents in that mailbox. Use to look up a document ID before fetching or reprocessing it. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    parserId: parserDropdown({ required: !0 }),
    search: import_pieces_framework5.Property.ShortText({
      displayName: "Search",
      description: "The search term to filter documents by name. Case insensitive. If empty, all documents are returned.",
      required: !1
    })
  },
  async run({ auth: apiKey, propsValue: { parserId, search } }) {
    if (!parserId)
      throw new Error("Parser ID is required");
    return await parseurCommon.listDocuments({
      apiKey: apiKey.secret_text,
      parserId,
      search
    });
  }
});

// ../ap/packages/pieces/community/parseur/src/lib/actions/get-parsed-document-by-id.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var getParsedDocumentById = (0, import_pieces_framework6.createAction)({
  auth: parseurAuth,
  name: "getParsedDocumentById",
  displayName: "Get Parsed Document by ID",
  description: "Fetch parsed JSON / structured output for a given document ID",
  audience: "both",
  aiMetadata: {
    description: "Retrieves the parsed/structured data of a single Parseur document by its ID. Use to read back the extracted fields after a document has been processed. Requires the document ID (and the parser/mailbox it belongs to); read-only and idempotent.",
    idempotent: !0
  },
  props: {
    parserId: parserDropdown({ required: !0 }),
    documentId: documentDropdown({ required: !0 })
  },
  async run({ auth: apiKey, propsValue: { documentId } }) {
    if (!documentId)
      throw new Error("Document ID is required");
    return await parseurCommon.getDocument({ apiKey: apiKey.secret_text, documentId });
  }
});

// ../ap/packages/pieces/community/parseur/src/lib/actions/reprocess-document.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var reprocessDocument = (0, import_pieces_framework7.createAction)({
  auth: parseurAuth,
  name: "reprocessDocument",
  displayName: "Reprocess Document",
  description: "Send an existing document back through parsing (e.g. after updating template).",
  audience: "both",
  aiMetadata: {
    description: "Re-runs Parseur parsing on an existing document by its ID, typically after a template change so the document is re-evaluated. Requires the document ID and its parser/mailbox. Each call re-triggers processing (and any downstream exports/webhooks), so it is not idempotent.",
    idempotent: !1
  },
  props: {
    parserId: parserDropdown({ required: !0 }),
    documentId: documentDropdown({ required: !0 })
  },
  async run({ auth: apiKey, propsValue: { documentId } }) {
    if (!documentId)
      throw new Error("Document ID is required");
    return await parseurCommon.reprocessDocument({
      apiKey: apiKey.secret_text,
      documentId
    });
  }
});

// ../ap/packages/pieces/community/parseur/src/lib/triggers/new-document-export-failed.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var newDocumentExportFailed = (0, import_pieces_framework8.createTrigger)({
  auth: parseurAuth,
  name: "newDocumentExportFailed",
  displayName: "New Document Export Failed",
  description: "Fires when an automated export endpoint (webhook / integration) fails for a processed document.",
  aiMetadata: {
    description: "Fires when delivering a successfully parsed document to a configured export target (webhook or downstream integration) fails for the selected Parseur mailbox. Use to detect and handle export/delivery failures."
  },
  props: { mailboxId: parserDropdown({ required: !0 }) },
  sampleData: {},
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let response = await parseurCommon.createWebhook({
      apiKey: context.auth.secret_text,
      event: "document.export_failed",
      target: context.webhookUrl,
      category: "CUSTOM"
    });
    await parseurCommon.enableWebhook({
      apiKey: context.auth.secret_text,
      webhookId: response.id,
      mailboxId: context.propsValue.mailboxId
    }), await context.store.put("_newDocumentExportFailed", {
      webhookId: response.id
    });
  },
  async onDisable(context) {
    let webhookInfo = await context.store.get(
      "_newDocumentExportFailed"
    );
    webhookInfo && (await parseurCommon.deleteWebhook({
      apiKey: context.auth.secret_text,
      webhookId: webhookInfo.webhookId
    }), await context.store.delete("_newDocumentExportFailed"));
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/parseur/src/lib/triggers/new-document-not-processed.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var newDocumentNotProcessed = (0, import_pieces_framework9.createTrigger)({
  auth: parseurAuth,
  name: "newDocumentNotProcessed",
  displayName: "New Document Not Processed",
  description: "Fires when Parseur fails to parse a document (e.g. no matching template).",
  aiMetadata: {
    description: "Fires when Parseur receives a document but cannot parse it because no matching template exists (template-needed). Use to react to documents that require a new template or manual attention. The mailbox filter is optional; without it, the event fires across mailboxes."
  },
  props: {
    mailboxId: parserDropdown({ required: !1 })
  },
  sampleData: {},
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let response = await parseurCommon.createWebhook({
      apiKey: context.auth.secret_text,
      event: "document.template_needed",
      target: context.webhookUrl,
      category: "CUSTOM"
    });
    await parseurCommon.enableWebhook({
      apiKey: context.auth.secret_text,
      webhookId: response.id,
      mailboxId: context.propsValue.mailboxId
    }), await context.store.put("_newDocumentNotProcessed", {
      webhookId: response.id
    });
  },
  async onDisable(context) {
    let webhookInfo = await context.store.get(
      "_newDocumentNotProcessed"
    );
    webhookInfo && (await parseurCommon.deleteWebhook({
      apiKey: context.auth.secret_text,
      webhookId: webhookInfo.webhookId
    }), await context.store.delete("_newDocumentNotProcessed"));
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/parseur/src/lib/triggers/new-document-processed.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var newDocumentProcessed = (0, import_pieces_framework10.createTrigger)({
  auth: parseurAuth,
  name: "newDocumentProcessed",
  displayName: "New Document Processed",
  description: "Fires when a new document is successfully processed and parsed by Parseur.",
  aiMetadata: {
    description: "Fires when a document in the selected Parseur mailbox is successfully parsed, emitting the document with its extracted/structured fields. Use to react to newly available parsed data."
  },
  props: {
    mailboxId: parserDropdown({ required: !0 })
  },
  sampleData: {},
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let webhookCreateResponse = await parseurCommon.createWebhook({
      apiKey: context.auth.secret_text,
      event: "document.processed",
      target: context.webhookUrl,
      category: "CUSTOM"
    });
    await parseurCommon.enableWebhook({
      apiKey: context.auth.secret_text,
      webhookId: webhookCreateResponse.id,
      mailboxId: context.propsValue.mailboxId
    }), await context.store.put("_newDocumentProcessed", {
      webhookId: webhookCreateResponse.id
    });
  },
  async onDisable(context) {
    let webhookInfo = await context.store.get(
      "_newDocumentProcessed"
    );
    webhookInfo && (await parseurCommon.deleteWebhook({
      apiKey: context.auth.secret_text,
      webhookId: webhookInfo.webhookId
    }), await context.store.delete("_newDocumentProcessed"));
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/parseur/src/lib/triggers/new-mailbox.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework11 = require("@activepieces/pieces-framework");
var polling = {
  strategy: import_pieces_common2.DedupeStrategy.LAST_ITEM,
  items: async ({ auth: apiKey }) => (await parseurCommon.listMailboxes({
    apiKey: apiKey.secret_text
  })).results.map((item) => ({
    id: item.id,
    data: item
  }))
}, newMailbox = (0, import_pieces_framework11.createTrigger)({
  auth: parseurAuth,
  name: "newMailbox",
  displayName: "New Mailbox",
  description: "Fires when a new mailbox is created in the Parseur account.",
  aiMetadata: {
    description: "Fires when a new mailbox (parser) is created in the Parseur account, discovered by polling the list of mailboxes. Use to react to newly provisioned parsers/mailboxes."
  },
  props: {},
  sampleData: {},
  type: import_pieces_framework11.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common2.pollingHelper.test(polling, context);
  },
  async onEnable(context) {
    let { store, auth, propsValue } = context;
    await import_pieces_common2.pollingHelper.onEnable(polling, { store, auth, propsValue });
  },
  async onDisable(context) {
    let { store, auth, propsValue } = context;
    await import_pieces_common2.pollingHelper.onDisable(polling, { store, auth, propsValue });
  },
  async run(context) {
    return await import_pieces_common2.pollingHelper.poll(polling, context);
  }
});

// ../ap/packages/pieces/community/parseur/src/lib/triggers/new-table-field-processed.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var newTableFieldProcessed = (0, import_pieces_framework12.createTrigger)({
  auth: parseurAuth,
  name: "newTableFieldProcessed",
  displayName: "New Table Field Processed",
  description: "Fires when a document with table fields is processed, and triggers for each row (table field) separately.",
  aiMetadata: {
    description: "Fires once per table row when a document containing table fields is parsed in the selected Parseur mailbox, emitting each extracted row individually. Use to process line-item or tabular data row by row rather than as a whole document."
  },
  props: {
    mailboxId: parserDropdown({ required: !0 })
  },
  sampleData: {},
  type: import_pieces_framework12.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let response = await parseurCommon.createWebhook({
      apiKey: context.auth.secret_text,
      event: "table.processed",
      target: context.webhookUrl,
      category: "CUSTOM"
    });
    await parseurCommon.enableWebhook({
      apiKey: context.auth.secret_text,
      webhookId: response.id,
      mailboxId: context.propsValue.mailboxId
    }), await context.store.put("_newTableFieldProcessed", {
      webhookId: response.id
    });
  },
  async onDisable(context) {
    let webhookInfo = await context.store.get(
      "_newTableFieldProcessed"
    );
    webhookInfo && (await parseurCommon.deleteWebhook({
      apiKey: context.auth.secret_text,
      webhookId: webhookInfo.webhookId
    }), await context.store.delete("_newTableFieldProcessed"));
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/parseur/src/index.ts
var parseur = (0, import_pieces_framework13.createPiece)({
  displayName: "Parseur",
  description: "Parseur is a document/email parsing tool that extracts structured data from emails, attachments, PDFs, invoices, forms, etc. It supports dynamic templates and table fields, and delivers parsed output to integrations (e.g. via webhook or API). This integration enables reactive workflows based on new processed documents, failed processing, mailbox changes, and more.",
  auth: parseurAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/parseur.png",
  authors: ["LuizDMM"],
  actions: [
    getParsedDocumentById,
    createDocument,
    createDocumentFromFile,
    reprocessDocument,
    findDocument,
    (0, import_pieces_common3.createCustomApiCallAction)({
      auth: parseurAuth,
      baseUrl: () => parseurCommon.baseUrl,
      authMapping: async (auth) => ({
        Authorization: auth.secret_text
      })
    })
  ],
  triggers: [
    newDocumentProcessed,
    newTableFieldProcessed,
    newDocumentNotProcessed,
    newDocumentExportFailed,
    newMailbox
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  parseur
});
