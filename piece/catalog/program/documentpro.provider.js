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

// ../ap/packages/pieces/community/documentpro/src/index.ts
var index_exports = {};
__export(index_exports, {
  documentpro: () => documentpro
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/documentpro/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), documentproAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `
To get your DocumentPro API Key:

1. **Login** to your DocumentPro account
2. **Go to** https://app.documentpro.ai/workflows
3. **Select** a workflow
4. **Change tab** to "Workflow" from the top bar
5. **Find** the "Upload" tab
6. **Click** "Upload via API"
7. **Click** the "Generate API Key" button
8. **Copy** your API key and paste it here
  `,
  required: !0
});

// ../ap/packages/pieces/community/documentpro/src/lib/actions/upload-document.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var uploaddocument = (0, import_pieces_framework2.createAction)({
  auth: documentproAuth,
  name: "uploadDocument",
  displayName: "Upload document",
  description: "Uploads a document to a DocumentPro parser",
  audience: "both",
  aiMetadata: {
    description: "Uploads a document file (PDF, JPEG, PNG, or TIFF, max 6MB) to DocumentPro, returning a document identifier used by later extraction steps. Use this as the first step before running a parser/workflow on the document. Not idempotent: each call uploads a new document and creates a distinct record.",
    idempotent: !1
  },
  props: {
    file: import_pieces_framework2.Property.File({
      displayName: "File",
      description: "The document file to upload (PDF, JPEG, PNG, or TIFF, max 6MB)",
      required: !0
    })
  },
  async run(context) {
    let file = context.propsValue.file, formData = new FormData(), fileBuffer = Buffer.from(file.base64, "base64"), mimeType = file.extension ? `application/${file.extension}` : "application/octet-stream", blob = new Blob([fileBuffer], { type: mimeType });
    return formData.append("file", blob, file.filename), (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://api.documentpro.ai/v1/documents",
      headers: {
        "x-api-key": context.auth.secret_text,
        "Content-Type": "multipart/form-data"
      },
      body: formData
    })).body;
  }
});

// ../ap/packages/pieces/community/documentpro/src/lib/actions/run-extract.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var runExtract = (0, import_pieces_framework3.createAction)({
  auth: documentproAuth,
  name: "runExtract",
  displayName: "Run Extract",
  description: "Run an extract/workflow on an uploaded document",
  audience: "both",
  aiMetadata: {
    description: "Runs a DocumentPro parser/workflow over an already-uploaded document to extract structured data, keyed by a document ID and a template/workflow ID. Optional OCR mode unlocks layout/table detection and regex-based start/end/split segmentation. Use after uploading a document; idempotent, as it reads and parses the same document with the same parameters without creating new records.",
    idempotent: !0
  },
  props: {
    document_id: import_pieces_framework3.Property.ShortText({
      displayName: "Document ID",
      description: "The unique identifier of the document to parse",
      required: !0
    }),
    template_id: import_pieces_framework3.Property.ShortText({
      displayName: "Template ID",
      description: "The unique identifier of the Workflow/template to use",
      required: !0
    }),
    use_ocr: import_pieces_framework3.Property.Checkbox({
      displayName: "Use OCR",
      description: "Enable OCR processing (required for gpt-3.5-turbo or OCR-related parameters)",
      required: !1,
      defaultValue: !1
    }),
    query_model: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Query Model",
      description: "The AI model to use for parsing",
      required: !1,
      defaultValue: "gpt-4o-mini",
      options: {
        options: [
          { label: "GPT-4o Mini", value: "gpt-4o-mini" },
          { label: "GPT-4o", value: "gpt-4o" },
          { label: "GPT-3.5 Turbo", value: "gpt-3.5-turbo" }
        ]
      }
    }),
    detect_layout: import_pieces_framework3.Property.Checkbox({
      displayName: "Detect Layout",
      description: "Detect document layout (requires use_ocr=true)",
      required: !1,
      defaultValue: !1
    }),
    detect_tables: import_pieces_framework3.Property.Checkbox({
      displayName: "Detect Tables",
      description: "Detect tables in the document (requires use_ocr=true)",
      required: !1,
      defaultValue: !1
    }),
    page_ranges: import_pieces_framework3.Property.ShortText({
      displayName: "Page Ranges",
      description: 'Specify which pages to parse (e.g., "1-3,5,7-9")',
      required: !1
    }),
    chunk_by_pages: import_pieces_framework3.Property.Number({
      displayName: "Chunk by Pages",
      description: "Number of pages per segment for method 1 segmentation",
      required: !1
    }),
    rolling_window: import_pieces_framework3.Property.Number({
      displayName: "Rolling Window",
      description: "Window size for method 2 segmentation",
      required: !1
    }),
    start_regex: import_pieces_framework3.Property.ShortText({
      displayName: "Start Regex",
      description: "Regex pattern for where parsing should begin (requires use_ocr=true)",
      required: !1
    }),
    end_regex: import_pieces_framework3.Property.ShortText({
      displayName: "End Regex",
      description: "Regex pattern for where parsing should end (requires use_ocr=true)",
      required: !1
    }),
    split_regex: import_pieces_framework3.Property.ShortText({
      displayName: "Split Regex",
      description: "Regex pattern to split the document into sections (requires use_ocr=true)",
      required: !1
    }),
    use_all_matches: import_pieces_framework3.Property.Checkbox({
      displayName: "Use All Matches",
      description: "Use all regex matches instead of just the first (requires use_ocr=true)",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let params = new URLSearchParams();
    return params.append("template_id", context.propsValue.template_id), context.propsValue.use_ocr && params.append("use_ocr", "true"), context.propsValue.query_model && params.append("query_model", context.propsValue.query_model), context.propsValue.detect_layout && params.append("detect_layout", "true"), context.propsValue.detect_tables && params.append("detect_tables", "true"), context.propsValue.page_ranges && params.append("page_ranges", context.propsValue.page_ranges), context.propsValue.chunk_by_pages && params.append(
      "chunk_by_pages",
      context.propsValue.chunk_by_pages.toString()
    ), context.propsValue.rolling_window && params.append(
      "rolling_window",
      context.propsValue.rolling_window.toString()
    ), context.propsValue.start_regex && params.append("start_regex", context.propsValue.start_regex), context.propsValue.end_regex && params.append("end_regex", context.propsValue.end_regex), context.propsValue.split_regex && params.append("split_regex", context.propsValue.split_regex), context.propsValue.use_all_matches && params.append("use_all_matches", "true"), (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.GET,
      url: `https://api.documentpro.ai/v1/documents/${context.propsValue.document_id}/run_parser?${params.toString()}`,
      headers: {
        "x-api-key": context.auth.secret_text,
        Accept: "application/json"
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/documentpro/src/lib/triggers/new-document.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var newDocument = (0, import_pieces_framework4.createTrigger)({
  auth: documentproAuth,
  name: "newDocument",
  displayName: "New Document",
  description: "Triggers when a new document is uploaded to DocumentPro",
  aiMetadata: {
    description: "Fires when a DocumentPro workflow finishes processing a document and emits a status-change webhook (e.g. parsing completed), delivering the document metadata and extracted result data. Requires the webhook URL to be configured in the DocumentPro workflow dashboard."
  },
  props: {
    webhook_info: import_pieces_framework4.Property.MarkDown({
      value: `
**In DocumentPro Dashboard:**
   - Navigate to your desired Workflow
   - Go to the "Workflow" tab
   - Find the "Webhook Notification" option in the export section
   - Set the Webhook Endpoint URL to:
            \`\`\`text
			{{webhookUrl}}
			\`\`\`
   - Click Save

 `
    })
  },
  sampleData: {
    event: "file_request_status_change",
    timestamp: "2024-07-25T14:30:29.565249",
    data: {
      request_id: "a7813466-6f9a-4c33-8128-427e7a4df755",
      request_status: "completed",
      response_body: {
        file_name: "sample_document.pdf",
        template_id: "8e9beda9-5cba-42eb-a70a-b3e5eec9120a",
        template_title: "Sample Parser",
        num_pages: 5,
        result_json_data: {
          field1: "value1",
          field2: "value2"
        }
      }
    }
  },
  type: import_pieces_framework4.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/documentpro/src/index.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common"), documentpro = (0, import_pieces_framework5.createPiece)({
  displayName: "DocumentPro",
  auth: documentproAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/documentpro.png",
  description: "DocumentPro is an AI-powered document processing platform that automates data extraction from various document types using advanced machine learning algorithms.",
  categories: [import_pieces_framework6.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["sanket-a11y"],
  actions: [
    uploaddocument,
    runExtract,
    (0, import_pieces_common3.createCustomApiCallAction)({
      auth: documentproAuth,
      baseUrl: () => "https://api.documentpro.ai/v1",
      authMapping: async (auth) => ({
        "x-api-key": `${auth.secret_text}`
      })
    })
  ],
  triggers: [newDocument]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  documentpro
});
