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

// ../ap/packages/pieces/community/gamma/src/index.ts
var index_exports = {};
__export(index_exports, {
  gamma: () => gamma
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/gamma/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), markdownDescription = `
Follow these steps to get your Gamma API Key:

1.  Log in to your Gamma account. You must have a **Pro or Ultra plan** to access the API.
2.  Navigate to **Settings & Members** (click your workspace name in the top-left).
3.  Go to the **API Key** tab.
4.  Click **Create API key** and give it a name (e.g., "Activepieces").
5.  Copy the key (it starts with \`sk-gamma-\`) and paste it below.

**Direct Link to API Settings:** [**https://gamma.app/settings/api**](https://gamma.app/settings/api)
`, gammaAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: markdownDescription,
  required: !0,
  props: {
    apiKey: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Key",
      description: "Paste your API key here.",
      required: !0
    })
  },
  async validate(auth) {
    let apiKey = auth.auth.apiKey;
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: "https://public-api.gamma.app/v1.0/generations/1",
        headers: {
          "X-API-KEY": apiKey
        }
      }), { valid: !0 };
    } catch (e) {
      if (e instanceof import_pieces_common.HttpError) {
        if (e.response.status === 404)
          return { valid: !0 };
        if (e.response.status === 401)
          return { valid: !1, error: "Invalid API Key" };
      }
      return { valid: !1, error: "Failed to connect to Gamma API" };
    }
  }
});

// ../ap/packages/pieces/community/gamma/src/lib/actions/generate-gamma.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var generateGamma = (0, import_pieces_framework2.createAction)({
  auth: gammaAuth,
  name: "generateGamma",
  displayName: "Generate Gamma",
  description: "Create a new Gamma generation job.",
  audience: "both",
  aiMetadata: {
    description: 'Starts an asynchronous Gamma generation job from input text, producing a presentation, document, social post, or webpage (set via the format prop). Use to kick off AI content creation; the job runs in the background, so follow up with "Get Generation" using the returned generation ID to poll for completion and outputs. Requires input text (1-400,000 chars); not idempotent \u2014 each call queues a new generation and consumes credits.',
    idempotent: !1
  },
  props: {
    folderId: import_pieces_framework2.Property.Dropdown({
      displayName: "Folder",
      description: "The folder to save the generated gamma into.",
      required: !1,
      refreshers: ["auth"],
      auth: gammaAuth,
      options: async ({ auth }) => {
        var _a;
        if (!auth) return { disabled: !0, options: [], placeholder: "Connect your account first" };
        try {
          return {
            options: ((_a = (await import_pieces_common2.httpClient.sendRequest({
              method: import_pieces_common2.HttpMethod.GET,
              url: "https://public-api.gamma.app/v1.0/folders",
              headers: { "X-API-KEY": auth.props.apiKey }
            })).body.data) != null ? _a : []).map((f) => ({ label: f.name, value: f.id }))
          };
        } catch (e) {
          return { disabled: !0, options: [], placeholder: "Failed to load folders" };
        }
      }
    }),
    inputText: import_pieces_framework2.Property.LongText({
      displayName: "Input Text",
      description: "Text used to generate your gamma (1-400,000 characters).",
      required: !0
    }),
    textMode: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Text Mode",
      description: "How you want your inputText to be modified.",
      required: !1,
      options: {
        options: [
          { label: "Generate", value: "generate" },
          { label: "Condense", value: "condense" },
          { label: "Preserve", value: "preserve" }
        ]
      }
    }),
    format: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Format",
      description: "The type of artifact you want to create.",
      required: !1,
      options: {
        options: [
          { label: "Presentation", value: "presentation" },
          { label: "Document", value: "document" },
          { label: "Social", value: "social" },
          { label: "Webpage", value: "webpage" }
        ]
      }
    }),
    themeName: import_pieces_framework2.Property.ShortText({
      displayName: "Theme Name",
      description: "The theme from Gamma to be used. Defaults to workspace default.",
      required: !1
    }),
    numCards: import_pieces_framework2.Property.Number({
      displayName: "Number of Cards",
      description: "How many cards to create (default 10). Pro: 1-50, Ultra: 1-75.",
      required: !1
    }),
    cardSplit: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Card Split",
      description: "How your content will be divided into cards. Use '---' in text for breaks.",
      required: !1,
      options: {
        options: [
          { label: "Auto", value: "auto" },
          { label: "Input Text Breaks", value: "inputTextBreaks" }
        ]
      }
    }),
    additionalInstructions: import_pieces_framework2.Property.LongText({
      displayName: "Additional Instructions",
      description: "Extra specifications about the desired content and layouts (1-500 characters).",
      required: !1
    }),
    exportAs: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Export As",
      description: "Additional file types for saving your gamma.",
      required: !1,
      options: {
        placeholder: "Don't export",
        options: [
          { label: "PDF", value: "pdf" },
          { label: "PowerPoint (PPTX)", value: "pptx" }
        ]
      }
    }),
    textOptions: import_pieces_framework2.Property.Json({
      displayName: "Text Options",
      description: "JSON object for text attributes (e.g., amount, tone, audience, language).",
      required: !1,
      defaultValue: {}
    }),
    imageOptions: import_pieces_framework2.Property.Json({
      displayName: "Image Options",
      description: "JSON object for image attributes (e.g., source, model, style).",
      required: !1,
      defaultValue: {}
    }),
    cardOptions: import_pieces_framework2.Property.Json({
      displayName: "Card Options",
      description: "JSON object for card attributes (e.g., dimensions).",
      required: !1,
      defaultValue: {}
    }),
    sharingOptions: import_pieces_framework2.Property.Json({
      displayName: "Sharing Options",
      description: "JSON object for sharing attributes (e.g., workspaceAccess, externalAccess).",
      required: !1,
      defaultValue: {}
    })
  },
  async run(context) {
    let {
      inputText,
      textMode,
      format,
      themeName,
      numCards,
      cardSplit,
      additionalInstructions,
      exportAs,
      textOptions,
      imageOptions,
      cardOptions,
      sharingOptions,
      folderId
    } = context.propsValue, body = {
      inputText
    };
    return textMode && (body.textMode = textMode), format && (body.format = format), themeName && (body.themeName = themeName), numCards && (body.numCards = numCards), cardSplit && (body.cardSplit = cardSplit), additionalInstructions && (body.additionalInstructions = additionalInstructions), exportAs && (body.exportAs = exportAs), textOptions && Object.keys(textOptions).length > 0 && (body.textOptions = textOptions), imageOptions && Object.keys(imageOptions).length > 0 && (body.imageOptions = imageOptions), cardOptions && Object.keys(cardOptions).length > 0 && (body.cardOptions = cardOptions), sharingOptions && Object.keys(sharingOptions).length > 0 && (body.sharingOptions = sharingOptions), folderId && (body.folderIds = [folderId]), (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://public-api.gamma.app/v1.0/generations",
      headers: {
        "X-API-KEY": context.auth.props.apiKey,
        "Content-Type": "application/json"
      },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/gamma/src/lib/actions/get-generation.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var getGeneration = (0, import_pieces_framework3.createAction)({
  auth: gammaAuth,
  name: "getGeneration",
  displayName: "Get Generation",
  description: "Given a Generation ID, fetch the status, outputs, metadata, etc.",
  audience: "both",
  aiMetadata: {
    description: 'Fetches the current status, outputs, and metadata of a Gamma generation job by its generation ID (returned from "Generate Gamma"). Use to poll an in-progress generation until it completes and to retrieve the resulting URLs/files. Read-only and idempotent \u2014 safe to call repeatedly while waiting for the job to finish.',
    idempotent: !0
  },
  props: {
    generationId: import_pieces_framework3.Property.ShortText({
      displayName: "Generation ID",
      description: 'The ID of the generation job (from the "Generate Gamma" action).',
      required: !0
    })
  },
  async run(context) {
    let { generationId } = context.propsValue, apiKey = context.auth.props.apiKey;
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: `https://public-api.gamma.app/v1.0/generations/${generationId}`,
      headers: {
        "X-API-KEY": apiKey,
        accept: "application/json"
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/gamma/src/index.ts
var gamma = (0, import_pieces_framework4.createPiece)({
  displayName: "Gamma",
  auth: gammaAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/gamma.png",
  authors: ["Pranith124"],
  description: "An AI-powered design partner that helps users generate presentations, documents, social media posts, cards, etc., via natural language.",
  categories: [
    import_pieces_framework5.PieceCategory.CONTENT_AND_FILES,
    import_pieces_framework5.PieceCategory.ARTIFICIAL_INTELLIGENCE
  ],
  actions: [
    generateGamma,
    getGeneration
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  gamma
});
