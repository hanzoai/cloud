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

// ../ap/packages/pieces/community/alai/src/index.ts
var index_exports = {};
__export(index_exports, {
  alai: () => alai
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/alai/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), markdownDescription = `
Follow these steps to get your Alai API Key:

1. Go to [**https://getalai.com/api**](https://getalai.com/api) and sign up or log in.
2. Navigate to the **API Keys** section.
3. Click **Create API Key** and give it a name (e.g., "Activepieces").
4. Copy the key and paste it below.
`, alaiAuth = import_pieces_framework.PieceAuth.CustomAuth({
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
        url: "https://slides-api.getalai.com/api/v1/ping",
        headers: {
          Authorization: `Bearer ${apiKey}`
        }
      }), { valid: !0 };
    } catch (e) {
      return e instanceof import_pieces_common.HttpError && e.response.status === 401 ? { valid: !1, error: "Invalid API Key" } : { valid: !1, error: "Failed to connect to Alai API" };
    }
  }
});

// ../ap/packages/pieces/community/alai/src/lib/actions/generate-presentation.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms)), generatePresentation = (0, import_pieces_framework2.createAction)({
  auth: alaiAuth,
  name: "generatePresentation",
  classification: "WRITE",
  displayName: "Generate Presentation",
  description: "Create a new AI-generated presentation from text.",
  audience: "both",
  aiMetadata: {
    description: "Generate a brand-new presentation from input text via the Alai AI API, with optional controls for tone, slide count, text density, theme, and AI images. Use this to turn raw text into slides. By default it returns immediately with a generation_id (poll Get Generation for status); set Wait for Completion to block until the deck finishes (up to Max Wait Time). Not idempotent: each call starts a new generation.",
    idempotent: !1
  },
  props: {
    inputText: import_pieces_framework2.Property.LongText({
      displayName: "Input Text",
      description: "The text content to generate a presentation from.",
      required: !0
    }),
    additionalInstructions: import_pieces_framework2.Property.LongText({
      displayName: "Additional Instructions",
      description: "Extra instructions for how the presentation should be generated.",
      required: !1
    }),
    exportFormats: import_pieces_framework2.Property.StaticMultiSelectDropdown({
      displayName: "Export Formats",
      description: "Formats to export the presentation in after generation.",
      required: !1,
      options: {
        options: [
          { label: "Link", value: "link" },
          { label: "PDF", value: "pdf" },
          { label: "PowerPoint (PPTX)", value: "ppt" }
        ]
      }
    }),
    title: import_pieces_framework2.Property.ShortText({
      displayName: "Title",
      description: "A title for the presentation. If not provided, one will be generated.",
      required: !1
    }),
    themeId: import_pieces_framework2.Property.ShortText({
      displayName: "Theme ID",
      description: "The ID of the presentation theme to use. If not provided, a default theme will be applied.",
      required: !1
    }),
    slideRange: import_pieces_framework2.Property.ShortText({
      displayName: "Slide Range",
      description: "Target slide count: auto, 1, 2-5, 6-10, 11-15, 16-20, 21-25, 26-50.",
      required: !1
    }),
    tone: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Tone",
      description: "The tone of voice for the presentation content.",
      required: !1,
      options: {
        options: [
          { label: "Default", value: "DEFAULT" },
          { label: "Professional", value: "PROFESSIONAL" },
          { label: "Casual", value: "CASUAL" },
          { label: "Technical", value: "TECHNICAL" },
          { label: "Educational", value: "EDUCATIONAL" },
          { label: "Inspirational", value: "INSPIRATIONAL" },
          { label: "Narrative", value: "NARRATIVE" },
          { label: "Persuasive", value: "PERSUASIVE" },
          { label: "Authoritative", value: "AUTHORITATIVE" },
          { label: "Empathetic", value: "EMPATHETIC" }
        ]
      }
    }),
    contentMode: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Content Mode",
      description: "How the input text should be handled.",
      required: !1,
      options: {
        options: [
          { label: "Preserve", value: "PRESERVE" },
          { label: "Condense", value: "CONDENSE" },
          { label: "Enhance", value: "ENHANCE" },
          { label: "Custom", value: "CUSTOM" }
        ]
      }
    }),
    amountMode: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Text Density",
      description: "The amount of text to include per slide.",
      required: !1,
      options: {
        options: [
          { label: "Minimal", value: "MINIMAL" },
          { label: "Essential", value: "ESSENTIAL" },
          { label: "Balanced", value: "BALANCED" },
          { label: "Detailed", value: "DETAILED" }
        ]
      }
    }),
    includeAiImages: import_pieces_framework2.Property.Checkbox({
      displayName: "Include AI Images",
      description: "Whether to include AI-generated images in the presentation.",
      required: !1,
      defaultValue: !0
    }),
    imageStyle: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Image Style",
      description: "The style for AI-generated images.",
      required: !1,
      options: {
        options: [
          { label: "Auto", value: "auto" },
          { label: "Realistic", value: "realistic" },
          { label: "Artistic", value: "artistic" },
          { label: "Cartoon", value: "cartoon" },
          { label: "3D", value: "three_d" }
        ]
      }
    }),
    waitForCompletion: import_pieces_framework2.Property.Checkbox({
      displayName: "Wait for Completion",
      description: "Wait for the presentation generation to complete before returning.",
      required: !1,
      defaultValue: !1
    }),
    maxWaitTime: import_pieces_framework2.Property.Number({
      displayName: "Max Wait Time (seconds)",
      description: "Maximum time to wait for completion (default 300 seconds / 5 minutes).",
      required: !1,
      defaultValue: 300
    })
  },
  async run(context) {
    var _a, _b, _c;
    let {
      inputText,
      additionalInstructions,
      exportFormats,
      title,
      themeId,
      slideRange,
      tone,
      contentMode,
      amountMode,
      includeAiImages,
      imageStyle,
      waitForCompletion,
      maxWaitTime
    } = context.propsValue, body = {
      input_text: inputText
    };
    additionalInstructions && (body.additional_instructions = additionalInstructions), exportFormats && exportFormats.length > 0 ? body.export_formats = exportFormats : body.export_formats = ["link"];
    let presentationOptions = {};
    title && (presentationOptions.title = title), themeId && (presentationOptions.theme_id = themeId), slideRange && (presentationOptions.slide_range = slideRange), Object.keys(presentationOptions).length > 0 && (body.presentation_options = presentationOptions);
    let textOptions = {};
    tone && (textOptions.tone = tone), contentMode && (textOptions.content_mode = contentMode), amountMode && (textOptions.amount_mode = amountMode), Object.keys(textOptions).length > 0 && (body.text_options = textOptions);
    let imageOptions = {};
    includeAiImages != null && (imageOptions.include_ai_images = includeAiImages), imageStyle && (imageOptions.style = imageStyle), Object.keys(imageOptions).length > 0 && (body.image_options = imageOptions);
    let response = await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://slides-api.getalai.com/api/v1/generations",
      headers: {
        Authorization: `Bearer ${context.auth.props.apiKey}`,
        "Content-Type": "application/json"
      },
      body
    }), generationId = (_a = response.body) == null ? void 0 : _a.generation_id;
    if (!generationId)
      throw new Error(
        `Failed to get generation_id from response: ${JSON.stringify(
          response.body
        )}`
      );
    if (!waitForCompletion)
      return response.body;
    await sleep(1e3);
    let maxWait = (maxWaitTime || 300) * 1e3, startTime = Date.now(), pollInterval = 5e3;
    for (; Date.now() - startTime < maxWait; ) {
      let statusResponse = await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.GET,
        url: `https://slides-api.getalai.com/api/v1/generations/${generationId}`,
        headers: {
          Authorization: `Bearer ${context.auth.props.apiKey}`
        }
      }), status = (_b = statusResponse.body) == null ? void 0 : _b.status;
      if (status === "completed")
        return statusResponse.body;
      if (status === "failed")
        throw new Error(
          `Generation failed: ${((_c = statusResponse.body) == null ? void 0 : _c.error) || "Unknown error"}`
        );
      await sleep(pollInterval);
    }
    throw new Error(`Generation timed out after ${maxWaitTime || 300} seconds`);
  }
});

// ../ap/packages/pieces/community/alai/src/lib/actions/get-generation.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var getGeneration = (0, import_pieces_framework3.createAction)({
  auth: alaiAuth,
  name: "getGeneration",
  classification: "READ",
  displayName: "Get Generation",
  description: "Fetch the status and result of a generation job.",
  audience: "both",
  aiMetadata: {
    description: "Look up the current status and result of an Alai generation job by its generation_id (returned by Generate Presentation or Export Presentation). Use this to poll for completion or retrieve the finished output and export links. Read-only and idempotent. Requires a valid generation_id.",
    idempotent: !0
  },
  props: {
    generationId: import_pieces_framework3.Property.ShortText({
      displayName: "Generation ID",
      description: 'The ID of the generation job (from the "Generate Presentation" action).',
      required: !0
    })
  },
  async run(context) {
    let { generationId } = context.propsValue;
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: `https://slides-api.getalai.com/api/v1/generations/${generationId}`,
      headers: {
        Authorization: `Bearer ${context.auth.props.apiKey}`,
        Accept: "application/json"
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/alai/src/lib/actions/export-presentation.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var sleep2 = (ms) => new Promise((resolve) => setTimeout(resolve, ms)), exportPresentation = (0, import_pieces_framework4.createAction)({
  auth: alaiAuth,
  name: "exportPresentation",
  classification: "WRITE",
  displayName: "Export Presentation",
  description: "Export a presentation in the specified formats.",
  audience: "both",
  aiMetadata: {
    description: "Export an existing Alai presentation into one or more formats (link, PDF, and/or PPTX) by its presentation ID. Use this to render a deck for download or sharing. By default it returns immediately with a generation_id (poll Get Generation for status); set Wait for Completion to block until the export finishes (up to Max Wait Time). Not idempotent: each call kicks off a new export job. Requires the presentation ID and at least one export format.",
    idempotent: !1
  },
  props: {
    presentationId: import_pieces_framework4.Property.ShortText({
      displayName: "Presentation ID",
      description: "The ID of the presentation to export.",
      required: !0
    }),
    exportFormats: import_pieces_framework4.Property.StaticMultiSelectDropdown({
      displayName: "Export Formats",
      description: "Formats to export the presentation in.",
      required: !0,
      options: {
        options: [
          { label: "Link", value: "link" },
          { label: "PDF", value: "pdf" },
          { label: "PowerPoint (PPTX)", value: "ppt" }
        ]
      }
    }),
    waitForCompletion: import_pieces_framework4.Property.Checkbox({
      displayName: "Wait for Completion",
      description: "Wait for the presentation generation to complete before returning.",
      required: !1,
      defaultValue: !1
    }),
    maxWaitTime: import_pieces_framework4.Property.Number({
      displayName: "Max Wait Time (seconds)",
      description: "Maximum time to wait for completion (default 300 seconds / 5 minutes).",
      required: !1,
      defaultValue: 300
    })
  },
  async run(context) {
    var _a, _b, _c;
    let { presentationId: presentationId2, exportFormats, waitForCompletion, maxWaitTime } = context.propsValue, response = await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: `https://slides-api.getalai.com/api/v1/presentations/${presentationId2}/exports`,
      headers: {
        Authorization: `Bearer ${context.auth.props.apiKey}`,
        "Content-Type": "application/json"
      },
      body: {
        export_formats: exportFormats
      }
    }), generationId = (_a = response.body) == null ? void 0 : _a.generation_id;
    if (!generationId)
      throw new Error(
        `Failed to get generation_id from response: ${JSON.stringify(
          response.body
        )}`
      );
    if (!waitForCompletion)
      return response.body;
    await sleep2(1e3);
    let maxWait = (maxWaitTime || 300) * 1e3, startTime = Date.now(), pollInterval = 5e3;
    for (; Date.now() - startTime < maxWait; ) {
      let statusResponse = await import_pieces_common4.httpClient.sendRequest({
        method: import_pieces_common4.HttpMethod.GET,
        url: `https://slides-api.getalai.com/api/v1/generations/${generationId}`,
        headers: {
          Authorization: `Bearer ${context.auth.props.apiKey}`
        }
      }), status = (_b = statusResponse.body) == null ? void 0 : _b.status;
      if (status === "completed")
        return statusResponse.body;
      if (status === "failed")
        throw new Error(
          `Generation failed: ${((_c = statusResponse.body) == null ? void 0 : _c.error) || "Unknown error"}`
        );
      await sleep2(pollInterval);
    }
    throw new Error(`Generation timed out after ${maxWaitTime || 300} seconds`);
  }
});

// ../ap/packages/pieces/community/alai/src/lib/actions/add-slide.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/alai/src/lib/common/props.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var presentationId = import_pieces_framework5.Property.Dropdown({
  auth: alaiAuth,
  displayName: "Presentation",
  description: "Select a presentation",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    if (!auth) return { options: [] };
    let presentations = ((_a = (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.GET,
      url: "https://slides-api.getalai.com/api/v1/presentations",
      headers: {
        Authorization: `Bearer ${auth.props.apiKey}`,
        Accept: "application/json"
      }
    })).body) == null ? void 0 : _a.presentations) || [];
    return {
      disabled: presentations.length === 0,
      options: presentations.map(
        (presentation) => ({
          label: presentation.title,
          value: presentation.presentation_id
        })
      )
    };
  }
});

// ../ap/packages/pieces/community/alai/src/lib/actions/add-slide.ts
var addSlide = (0, import_pieces_framework6.createAction)({
  auth: alaiAuth,
  name: "addSlide",
  classification: "WRITE",
  displayName: "Add Slide",
  description: "Add a new slide to an existing presentation.",
  audience: "both",
  aiMetadata: {
    description: 'Generate and insert a new AI-authored slide into an existing Alai presentation, given the presentation ID and the slide content/topic; optional slide order controls where it lands (a numeric index, or "end" to append). Use this to extend a deck after it has been generated. Not idempotent: each call appends another slide.',
    idempotent: !1
  },
  props: {
    presentationId,
    slide_context: import_pieces_framework6.Property.LongText({
      displayName: "Slide Context",
      description: "The content or topic for the slide to be generated.",
      required: !0
    }),
    additionalInstructions: import_pieces_framework6.Property.LongText({
      displayName: "Additional Instructions",
      description: "Extra instructions for how the slide should be generated.",
      required: !1
    }),
    slide_order: import_pieces_framework6.Property.ShortText({
      displayName: "Slide Order",
      description: 'The position to insert the new slide (e.g., "0" for the first slide, "end" to add at the end).',
      required: !1
    }),
    includeAiImages: import_pieces_framework6.Property.Checkbox({
      displayName: "Include AI Images",
      description: "Whether to include AI-generated images in the slide.",
      required: !1,
      defaultValue: !0
    }),
    imageStyle: import_pieces_framework6.Property.ShortText({
      displayName: "Image Style",
      description: "The style for AI-generated images.",
      required: !1
    })
  },
  async run(context) {
    let { presentationId: presentationId2, slide_context, additionalInstructions, slide_order, includeAiImages, imageStyle } = context.propsValue, options = {};
    additionalInstructions && (options.additional_instructions = additionalInstructions), slide_order && (options.slide_order = isNaN(Number(slide_order)) ? slide_order : Number(slide_order)), includeAiImages ? options.num_image_variants = imageStyle ? 2 : 1 : options.num_image_variants = 0;
    let body = {
      slide_context,
      options,
      export_formats: ["link"]
    };
    return (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.POST,
      url: `https://slides-api.getalai.com/api/v1/presentations/${presentationId2}/slides`,
      headers: {
        Authorization: `Bearer ${context.auth.props.apiKey}`,
        "Content-Type": "application/json"
      },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/alai/src/lib/actions/delete-presentation.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var deletePresentation = (0, import_pieces_framework7.createAction)({
  auth: alaiAuth,
  name: "deletePresentation",
  classification: "DESTRUCTIVE",
  displayName: "Delete Presentation",
  description: "Delete a presentation by its ID.",
  audience: "both",
  aiMetadata: {
    description: "Permanently delete an Alai presentation by its ID. Use this to remove a deck the user no longer needs. Destructive but idempotent: once deleted, repeating the call with the same ID leaves the same end state. Requires the presentation ID.",
    idempotent: !0
  },
  props: {
    presentationId: import_pieces_framework7.Property.ShortText({
      displayName: "Presentation ID",
      description: "The ID of the presentation to delete.",
      required: !0
    })
  },
  async run(context) {
    let { presentationId: presentationId2 } = context.propsValue;
    return (await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.DELETE,
      url: `https://slides-api.getalai.com/api/v1/presentations/${presentationId2}`,
      headers: {
        Authorization: `Bearer ${context.auth.props.apiKey}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/alai/src/index.ts
var import_pieces_common8 = require("@activepieces/pieces-common"), alai = (0, import_pieces_framework8.createPiece)({
  displayName: "Alai",
  auth: alaiAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/alai.png",
  authors: ["getalai", "sanket-a11y"],
  description: "AI-powered presentation generation API. Create, export, and manage professional presentations from text.",
  categories: [
    import_pieces_framework9.PieceCategory.CONTENT_AND_FILES,
    import_pieces_framework9.PieceCategory.ARTIFICIAL_INTELLIGENCE
  ],
  actions: [
    generatePresentation,
    getGeneration,
    exportPresentation,
    addSlide,
    deletePresentation,
    (0, import_pieces_common8.createCustomApiCallAction)({
      auth: alaiAuth,
      baseUrl: () => "https://slides-api.getalai.com/api/v1",
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.props.apiKey}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  alai
});
