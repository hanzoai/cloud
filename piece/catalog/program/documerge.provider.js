"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __hasOwnProp = Object.prototype.hasOwnProperty;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value;
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
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key != "symbol" ? key + "" : key, value);

// ../ap/packages/pieces/community/documerge/src/index.ts
var index_exports = {};
__export(index_exports, {
  documerge: () => documerge
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_framework10 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/documerge/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/documerge/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
var BASE_URL = "https://app.documerge.ai", DocuMergeClient = class {
  constructor(apiKey) {
    __publicField(this, "apiKey", apiKey);
  }
  async makeRequest(method, endpoint, body, queryParams) {
    let url = endpoint.startsWith("/") ? `${BASE_URL}${endpoint}` : `${BASE_URL}/${endpoint}`, request = {
      method,
      url,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: this.apiKey
      },
      headers: {
        "Content-Type": "application/json",
        Accept: "application/json"
      },
      body,
      queryParams
    };
    return (await import_pieces_common.httpClient.sendRequest(request)).body;
  }
  async get(endpoint, queryParams) {
    return this.makeRequest(import_pieces_common.HttpMethod.GET, endpoint, void 0, queryParams);
  }
  async post(endpoint, body, queryParams) {
    return this.makeRequest(import_pieces_common.HttpMethod.POST, endpoint, body, queryParams);
  }
  async patch(endpoint, body, queryParams) {
    return this.makeRequest(import_pieces_common.HttpMethod.PATCH, endpoint, body, queryParams);
  }
  async put(endpoint, body, queryParams) {
    return this.makeRequest(import_pieces_common.HttpMethod.PUT, endpoint, body, queryParams);
  }
  async delete(endpoint, queryParams) {
    return this.makeRequest(import_pieces_common.HttpMethod.DELETE, endpoint, void 0, queryParams);
  }
  async makeBinaryRequest(method, endpoint, body, queryParams) {
    let url = endpoint.startsWith("/") ? `${BASE_URL}${endpoint}` : `${BASE_URL}/${endpoint}`, request = {
      method,
      url,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: this.apiKey
      },
      headers: {
        "Content-Type": "application/json",
        Accept: "application/json"
      },
      body,
      queryParams,
      responseType: "arraybuffer"
    }, responseBody = (await import_pieces_common.httpClient.sendRequest(request)).body;
    if (typeof responseBody == "string")
      return Buffer.from(responseBody, "binary").buffer;
    if (responseBody instanceof ArrayBuffer)
      return responseBody;
    if (Buffer.isBuffer(responseBody)) {
      let buf = responseBody;
      return buf.buffer.slice(buf.byteOffset, buf.byteOffset + buf.byteLength);
    }
    return Buffer.from(responseBody).buffer;
  }
};

// ../ap/packages/pieces/community/documerge/src/lib/common/auth.ts
var documergeAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Get your API token from your dashboard by clicking API Tokens in the top right corner of your profile.",
  required: !0,
  validate: async ({ auth }) => {
    var _a;
    if (!auth || typeof auth != "string")
      return {
        valid: !1,
        error: "Please provide a valid API key."
      };
    let client = new DocuMergeClient(auth);
    try {
      return await client.get("/api/documents"), {
        valid: !0
      };
    } catch (error) {
      let status = (_a = error == null ? void 0 : error.response) == null ? void 0 : _a.status, message = (error == null ? void 0 : error.message) || "Unknown error occurred";
      return status === 401 || status === 403 ? {
        valid: !1,
        error: "Invalid API key. Please verify the key in your DocuMerge dashboard and try again."
      } : {
        valid: !1,
        error: `Authentication failed: ${message}`
      };
    }
  }
});

// ../ap/packages/pieces/community/documerge/src/lib/actions/combine-files.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var combineFiles = (0, import_pieces_framework2.createAction)({
  auth: documergeAuth,
  name: "combine_files",
  displayName: "Combine Files",
  description: "Combine multiple files into a single PDF or DOCX",
  audience: "both",
  aiMetadata: { description: "Merge several source files into one output document on DocuMerge, choosing PDF or DOCX as the combined format; accepts file identifiers plus optional remote URL and inline contents. Use to concatenate documents into a single deliverable. Requires at least one file identifier, and each call produces a new combined file (not idempotent).", idempotent: !1 },
  props: {
    output: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Output Format",
      description: "The format of the combined file",
      required: !0,
      options: {
        options: [
          { label: "PDF", value: "pdf" },
          { label: "DOCX", value: "docx" }
        ]
      }
    }),
    files: import_pieces_framework2.Property.Array({
      displayName: "Files",
      description: "Array of file identifiers to combine",
      required: !0
    }),
    name: import_pieces_framework2.Property.ShortText({
      displayName: "Name",
      description: "Name for the combined file",
      required: !1
    }),
    url: import_pieces_framework2.Property.ShortText({
      displayName: "URL",
      description: "URL of a file to include (must be a valid URL)",
      required: !1
    }),
    contents: import_pieces_framework2.Property.LongText({
      displayName: "Contents",
      description: "Additional content to include",
      required: !1
    })
  },
  async run(context) {
    let { output, files, name, url, contents } = context.propsValue;
    if (!files || files.length === 0)
      throw new Error("At least one file is required");
    let client = new DocuMergeClient(context.auth.secret_text), body = {
      output,
      files: files.filter((f) => typeof f == "string")
    };
    name && (body.name = name), url && (body.url = url), contents && (body.contents = contents);
    let fileData = await client.makeBinaryRequest(
      import_pieces_common2.HttpMethod.POST,
      "/api/tools/combine",
      body
    ), fileExtension = output === "pdf" ? "pdf" : "docx", fileName = name ? `${name}.${fileExtension}` : `combined_file_${Date.now()}.${fileExtension}`, fileUrl = await context.files.write({
      fileName,
      data: Buffer.from(fileData)
    });
    return {
      success: !0,
      fileName,
      fileUrl,
      format: output,
      size: fileData.byteLength
    };
  }
});

// ../ap/packages/pieces/community/documerge/src/lib/actions/convert-file-to-pdf.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var convertFileToPdf = (0, import_pieces_framework3.createAction)({
  auth: documergeAuth,
  name: "convert_file_to_pdf",
  displayName: "Convert File to PDF",
  description: "Convert a given file to PDF",
  audience: "both",
  aiMetadata: { description: "Convert a single source file to PDF on DocuMerge, supplying the file by name plus an optional remote URL or inline contents. Use when an agent needs a PDF rendering of another document format. Requires a file name; each call generates a fresh PDF (not idempotent).", idempotent: !1 },
  props: {
    fileName: import_pieces_framework3.Property.ShortText({
      displayName: "File Name",
      description: "Name of the file to convert",
      required: !0
    }),
    fileUrl: import_pieces_framework3.Property.ShortText({
      displayName: "File URL",
      description: "URL of the file to convert (must be a valid URL)",
      required: !1
    }),
    contents: import_pieces_framework3.Property.LongText({
      displayName: "Contents",
      description: "Additional content to include",
      required: !1
    })
  },
  async run(context) {
    let { fileName, fileUrl, contents } = context.propsValue;
    if (!fileName)
      throw new Error("File name is required");
    let client = new DocuMergeClient(context.auth.secret_text), body = {
      file: {
        name: fileName
      }
    };
    fileUrl && (body.file.url = fileUrl), contents && (body.contents = contents);
    let fileData = await client.makeBinaryRequest(
      import_pieces_common3.HttpMethod.POST,
      "/api/tools/pdf/convert",
      body
    ), pdfFileName = fileName.endsWith(".pdf") ? fileName : `${fileName.replace(/\.[^/.]+$/, "")}.pdf`, fileUrlResult = await context.files.write({
      fileName: pdfFileName,
      data: Buffer.from(fileData)
    });
    return {
      success: !0,
      fileName: pdfFileName,
      fileUrl: fileUrlResult,
      format: "pdf",
      size: fileData.byteLength
    };
  }
});

// ../ap/packages/pieces/community/documerge/src/lib/actions/create-data-route-merge.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var createDataRouteMerge = (0, import_pieces_framework4.createAction)({
  auth: documergeAuth,
  name: "create_data_route_merge",
  displayName: "Create Data Route Merge",
  description: "Send data to your Data Route URL",
  audience: "both",
  aiMetadata: { description: "Submit field data to a configured DocuMerge data route, identified by its route key, to trigger that route's document generation/distribution. Use when populating a predefined route rather than merging a single document. Requires the route key; each submission initiates a new merge run (not idempotent).", idempotent: !1 },
  props: {
    routeKey: import_pieces_framework4.Property.ShortText({
      displayName: "Route Key",
      description: "The key of the data route to merge",
      required: !0
    }),
    fields: import_pieces_framework4.Property.Object({
      displayName: "Fields",
      description: "Field data to merge into the document",
      required: !1
    })
  },
  async run(context) {
    let { routeKey, fields } = context.propsValue;
    if (!routeKey)
      throw new Error("Route key is required");
    return await new DocuMergeClient(context.auth.secret_text).post(
      `/api/routes/merge/${encodeURIComponent(routeKey)}`,
      fields || {}
    );
  }
});

// ../ap/packages/pieces/community/documerge/src/lib/actions/create-document-merge.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var createDocumentMerge = (0, import_pieces_framework5.createAction)({
  auth: documergeAuth,
  name: "create_document_merge",
  displayName: "Create Document Merge",
  description: "Send data to your Merge URL",
  audience: "both",
  aiMetadata: { description: "Submit field data to a configured DocuMerge document, identified by its document key, to merge that data into the template and generate the populated document. Use to fill a single predefined template. Requires the document key; each submission starts a new merge run (not idempotent).", idempotent: !1 },
  props: {
    documentKey: import_pieces_framework5.Property.ShortText({
      displayName: "Document Key",
      description: "The key of the document to merge",
      required: !0
    }),
    fields: import_pieces_framework5.Property.Object({
      displayName: "Fields",
      description: "Field data to merge into the document",
      required: !1
    })
  },
  async run(context) {
    let { documentKey, fields } = context.propsValue;
    if (!documentKey)
      throw new Error("Document key is required");
    return await new DocuMergeClient(context.auth.secret_text).post(
      `/api/documents/merge/${encodeURIComponent(documentKey)}`,
      fields || {}
    );
  }
});

// ../ap/packages/pieces/community/documerge/src/lib/actions/split-pdf.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var splitPdf = (0, import_pieces_framework6.createAction)({
  auth: documergeAuth,
  name: "split_pdf",
  displayName: "Split PDF",
  description: "Extract or remove specific pages from a PDF file",
  audience: "both",
  aiMetadata: { description: 'Produce a new PDF on DocuMerge by either extracting a set of pages from a source PDF or removing a set of pages from it, with pages given as numbers or ranges (e.g. "1", "2-5"). Supply the source by name plus an optional URL or base64 contents. Use to subset or trim a PDF. Requires a file name; each call writes a new file (not idempotent).', idempotent: !1 },
  props: {
    fileName: import_pieces_framework6.Property.ShortText({
      displayName: "File Name",
      description: "Name of the PDF file",
      required: !0
    }),
    fileUrl: import_pieces_framework6.Property.ShortText({
      displayName: "File URL",
      description: "URL of the PDF file (must be a valid URL)",
      required: !1
    }),
    contents: import_pieces_framework6.Property.LongText({
      displayName: "Contents",
      description: "Base64 encoded file contents",
      required: !1
    }),
    extract: import_pieces_framework6.Property.Array({
      displayName: "Pages to Extract",
      description: 'Page numbers or ranges to extract (e.g., "1", "2-5", "1, 3-5")',
      required: !1
    }),
    remove: import_pieces_framework6.Property.Array({
      displayName: "Pages to Remove",
      description: 'Page numbers or ranges to remove (e.g., "1", "2-5", "1, 3-5")',
      required: !1
    })
  },
  async run(context) {
    let { fileName, fileUrl, contents, extract, remove } = context.propsValue;
    if (!fileName)
      throw new Error("File name is required");
    let client = new DocuMergeClient(context.auth.secret_text), body = {
      file: {
        name: fileName
      }
    };
    fileUrl && (body.file.url = fileUrl), contents && (body.file.contents = contents), extract && extract.length > 0 && (body.extract = extract.filter((e) => typeof e == "string")), remove && remove.length > 0 && (body.remove = remove.filter((r) => typeof r == "string"));
    let fileData = await client.makeBinaryRequest(
      import_pieces_common4.HttpMethod.POST,
      "/api/tools/pdf/split",
      body
    ), pdfFileName = fileName.endsWith(".pdf") ? fileName.replace(".pdf", "_split.pdf") : `${fileName}_split.pdf`, fileUrlResult = await context.files.write({
      fileName: pdfFileName,
      data: Buffer.from(fileData)
    });
    return {
      success: !0,
      fileName: pdfFileName,
      fileUrl: fileUrlResult,
      format: "pdf",
      size: fileData.byteLength
    };
  }
});

// ../ap/packages/pieces/community/documerge/src/lib/triggers/new-merged-document.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var newMergedDocument = (0, import_pieces_framework7.createTrigger)({
  auth: documergeAuth,
  name: "new_merged_document",
  displayName: "New Merged Document",
  description: "Triggers when a merged/populated document is created",
  aiMetadata: {
    description: "Fires when DocuMerge finishes generating a merged/populated document, delivered via a webhook the user configures on a document in their DocuMerge dashboard. The event represents a completed document merge and can carry the merged field data and a temporary download URL."
  },
  props: {
    webhookInstructions: import_pieces_framework7.Property.MarkDown({
      value: `
## Setup Instructions

To use this trigger, configure a Webhook Delivery Method in DocuMerge:

1. Go to your DocuMerge dashboard
2. Navigate to your Document or Route settings
3. Add a new **Webhook Delivery Method**
4. Set the **URL** to:
\`\`\`text
{{webhookUrl}}
\`\`\`
5. Configure the webhook options:
   - \u2705 **Send temporary download url (file_url)** - Provides a 1-hour download link
   - \u2705 **Send data using JSON** - Sends data as JSON
   - \u2705 **Send merge data** - Includes field data in the payload
6. Click **Submit** to save

The webhook will trigger whenever a document is merged.
      `
    })
  },
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  sampleData: {
    file_url: "https://app.documerge.ai/download/temp/abc123...",
    fields: {
      first_name: "John",
      last_name: "Doe",
      email: "john.doe@example.com",
      company: "Acme Inc"
    },
    document_name: "Contract_JohnDoe.pdf",
    document_id: "12345",
    merged_at: "2024-01-15T10:30:00Z"
  },
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/documerge/src/lib/triggers/new-merged-route.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var newMergedRoute = (0, import_pieces_framework8.createTrigger)({
  auth: documergeAuth,
  name: "new_merged_route",
  displayName: "New Merged Route",
  description: "Triggers when a merged/populated route is created",
  aiMetadata: {
    description: "Fires when DocuMerge completes a data route merge, delivered via a webhook the user configures on a route in their DocuMerge dashboard. The event represents a finished route merge and can carry the merged field data and a temporary download URL."
  },
  props: {
    webhookInstructions: import_pieces_framework8.Property.MarkDown({
      value: `
## Setup Instructions

To use this trigger, configure a Webhook Delivery Method in DocuMerge:

1. Go to your DocuMerge dashboard
2. Navigate to your **Route** settings
3. Add a new **Webhook Delivery Method**
4. Set the **URL** to:
\`\`\`text
{{webhookUrl}}
\`\`\`
5. Configure the webhook options:
   - \u2705 **Send temporary download url (file_url)** - Provides a 1-hour download link
   - \u2705 **Send data using JSON** - Sends data as JSON
   - \u2705 **Send merge data** - Includes field data in the payload
6. Click **Submit** to save

The webhook will trigger whenever a route merge is completed.
      `
    })
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  sampleData: {
    file_url: "https://app.documerge.ai/download/temp/xyz789...",
    fields: {
      first_name: "Jane",
      last_name: "Smith",
      email: "jane.smith@example.com",
      company: "Tech Corp"
    },
    route_name: "Customer Onboarding",
    route_id: "67890",
    merged_at: "2024-01-15T14:45:00Z"
  },
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/documerge/src/index.ts
var documerge = (0, import_pieces_framework9.createPiece)({
  displayName: "DocuMerge",
  description: "Merge and generate documents with dynamic data",
  auth: documergeAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/documerge.png",
  categories: [import_pieces_framework10.PieceCategory.CONTENT_AND_FILES],
  authors: ["onyedikachi-david"],
  actions: [combineFiles, convertFileToPdf, createDataRouteMerge, createDocumentMerge, splitPdf],
  triggers: [newMergedDocument, newMergedRoute]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  documerge
});
