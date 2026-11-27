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

// ../ap/packages/pieces/community/pdf4me/src/index.ts
var index_exports = {};
__export(index_exports, {
  pdf4me: () => pdf4me,
  pdf4meAuth: () => pdf4meAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework10 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/pdf4me/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), pdf4meAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `To get your PDF4me API key:
1. Go to your [API Keys dashboard](https://dev.pdf4me.com/dashboard/#/api-keys/)
2. Sign in or create a free account if you don't have one
3. Copy your API key and paste it here`,
  required: !0
});

// ../ap/packages/pieces/community/pdf4me/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.pdf4me.com";
async function callFileApi({
  apiKey,
  endpoint,
  body
}) {
  return import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.POST,
    url: `${BASE_URL}${endpoint}`,
    headers: {
      Authorization: apiKey,
      "Content-Type": "application/json"
    },
    body,
    responseType: "arraybuffer"
  });
}
async function callJsonApi({
  apiKey,
  endpoint,
  body
}) {
  return import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.POST,
    url: `${BASE_URL}${endpoint}`,
    headers: {
      Authorization: apiKey,
      "Content-Type": "application/json"
    },
    body
  });
}
function fileNameFromHeaders(headers, fallback) {
  var _a;
  let raw = (_a = headers == null ? void 0 : headers["content-disposition"]) != null ? _a : headers == null ? void 0 : headers["Content-Disposition"], cd = Array.isArray(raw) ? raw[0] : raw != null ? raw : "", match = /filename[^;=\n]*=((['"]).*?\2|[^;\n]*)/.exec(cd);
  return match && match[1].replace(/['"]/g, "").trim() || fallback;
}
var pdf4meCommon = { BASE_URL, callFileApi, callJsonApi, fileNameFromHeaders };

// ../ap/packages/pieces/community/pdf4me/src/lib/actions/convert-to-pdf.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var convertToPdfAction = (0, import_pieces_framework2.createAction)({
  auth: pdf4meAuth,
  name: "convert_to_pdf",
  displayName: "Convert File to PDF",
  description: "Converts a Word, Excel, PowerPoint, or image file to PDF format.",
  audience: "both",
  aiMetadata: {
    description: "Converts a single office or image file (DOCX/DOC, XLSX/XLS, PPTX/PPT, PNG/JPG/GIF/BMP/TIFF) into a PDF via the PDF4me API. Use when an agent needs a non-PDF document rendered as PDF; the input file is required. A pure conversion that is idempotent \u2014 the same input always yields the same PDF with no stored side effect.",
    idempotent: !0
  },
  props: {
    file: import_pieces_framework2.Property.File({
      displayName: "File",
      description: "The file to convert. Supported formats: DOCX, DOC, XLSX, XLS, PPTX, PPT, PNG, JPG, JPEG, GIF, BMP, TIFF.",
      required: !0
    }),
    filename: import_pieces_framework2.Property.ShortText({
      displayName: "Output Filename",
      description: "Name for the resulting PDF file (without the .pdf extension). Defaults to the original filename.",
      required: !1
    })
  },
  async run(context) {
    let { file, filename } = context.propsValue, outputName = filename != null ? filename : file.filename.replace(/\.[^.]+$/, ""), response = await pdf4meCommon.callFileApi({
      apiKey: context.auth.secret_text,
      endpoint: "/api/v2/ConvertToPdf",
      body: {
        docContent: file.data.toString("base64"),
        docName: file.filename
      }
    });
    return {
      file_name: pdf4meCommon.fileNameFromHeaders(
        response.headers,
        `${outputName}.pdf`
      ),
      file_data_base64: Buffer.from(response.body).toString("base64"),
      success: !0
    };
  }
});

// ../ap/packages/pieces/community/pdf4me/src/lib/actions/merge-pdfs.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var mergePdfsAction = (0, import_pieces_framework3.createAction)({
  auth: pdf4meAuth,
  name: "merge_pdfs",
  displayName: "Merge PDF Files",
  description: "Combines two or more PDF files into a single PDF document.",
  audience: "both",
  aiMetadata: {
    description: "Merges two PDF files into one combined PDF via the PDF4me API, with the first file placed before the second. Use when an agent needs to concatenate two PDFs into a single document; both PDF files are required. A pure transformation that is idempotent \u2014 the same inputs always produce the same merged PDF with no stored side effect.",
    idempotent: !0
  },
  props: {
    file1: import_pieces_framework3.Property.File({
      displayName: "First PDF File",
      description: "The first PDF file to merge. It will appear first in the merged result.",
      required: !0
    }),
    file2: import_pieces_framework3.Property.File({
      displayName: "Second PDF File",
      description: "The second PDF file to merge.",
      required: !0
    }),
    filename: import_pieces_framework3.Property.ShortText({
      displayName: "Output Filename",
      description: "Name for the merged PDF file (without the .pdf extension).",
      required: !1,
      defaultValue: "merged"
    })
  },
  async run(context) {
    let { file1, file2, filename } = context.propsValue, outputName = filename != null ? filename : "merged", response = await pdf4meCommon.callFileApi({
      apiKey: context.auth.secret_text,
      endpoint: "/api/v2/Merge",
      body: {
        docContent: [
          file1.data.toString("base64"),
          file2.data.toString("base64")
        ],
        docName: `${outputName}.pdf`
      }
    });
    return {
      file_name: pdf4meCommon.fileNameFromHeaders(
        response.headers,
        `${outputName}.pdf`
      ),
      file_data_base64: Buffer.from(response.body).toString("base64"),
      success: !0
    };
  }
});

// ../ap/packages/pieces/community/pdf4me/src/lib/actions/split-pdf.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var SPLIT_ACTION_OPTIONS = [
  {
    label: "Split After Page \u2014 split into two parts at a given page",
    value: "SplitAfterPage"
  },
  {
    label: "Recurring Split \u2014 split into equal chunks every N pages",
    value: "RecurringSplitAfterPage"
  },
  {
    label: "Split Sequence \u2014 split by a sequence of page counts",
    value: "SplitSequence"
  },
  {
    label: "Split Ranges \u2014 split using custom page ranges",
    value: "SplitRanges"
  }
], SPLIT_ACTION_NUMBER_DESCRIPTIONS = {
  SplitAfterPage: "Page number after which the PDF is split. E.g. 3 \u2192 produces pages 1\u20133 and the rest.",
  RecurringSplitAfterPage: "Number of pages per chunk. E.g. 5 \u2192 splits into parts of 5 pages each.",
  SplitSequence: "Number of pages in the first sequence chunk.",
  SplitRanges: "Starting page number of the custom range."
}, splitPdfAction = (0, import_pieces_framework4.createAction)({
  auth: pdf4meAuth,
  name: "split_pdf",
  displayName: "Split PDF",
  description: "Splits a PDF file into multiple documents using one of four split strategies.",
  audience: "both",
  aiMetadata: {
    description: "Splits a PDF into multiple documents via the PDF4me API using one of four strategies selected by the Split Strategy prop: split after a given page, recurring split every N pages, split by a sequence of page counts, or split by custom ranges; the numeric prop controls the chosen strategy. Use when an agent needs to break one PDF into parts. The PDF file is required. A pure transformation that is idempotent \u2014 the same input and settings always produce the same parts with no stored side effect.",
    idempotent: !0
  },
  props: {
    file: import_pieces_framework4.Property.File({
      displayName: "PDF File",
      description: "The PDF file to split.",
      required: !0
    }),
    splitAction: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Split Strategy",
      description: "How the PDF should be split.",
      required: !0,
      defaultValue: "SplitAfterPage",
      options: { options: SPLIT_ACTION_OPTIONS }
    }),
    splitSettings: import_pieces_framework4.Property.DynamicProperties({
      displayName: "Split Settings",
      refreshers: ["splitAction"],
      auth: pdf4meAuth,
      required: !0,
      props: async (propsValue) => {
        var _a, _b;
        let action = String((_a = propsValue.splitAction) != null ? _a : "SplitAfterPage"), description = (_b = SPLIT_ACTION_NUMBER_DESCRIPTIONS[action]) != null ? _b : "Number controlling the split behaviour for the chosen strategy.";
        return {
          splitActionNumber: import_pieces_framework4.Property.Number({
            displayName: "Split Number",
            description,
            required: !0,
            defaultValue: 1
          })
        };
      }
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n, _o, _p, _q;
    let { file, splitAction, splitSettings } = context.propsValue, splitActionNumber = splitSettings == null ? void 0 : splitSettings.splitActionNumber, docs = (_a = (await pdf4meCommon.callJsonApi({
      apiKey: context.auth.secret_text,
      endpoint: "/api/v2/SplitPDF",
      body: {
        docContent: file.data.toString("base64"),
        docName: file.filename,
        splitAction: splitAction != null ? splitAction : "SplitAfterPage",
        splitActionNumber: splitActionNumber != null ? splitActionNumber : 1,
        fileNaming: "NameAsPerOrder"
      }
    })).body["splited Documents"]) != null ? _a : [];
    return {
      parts_count: docs.length,
      part_1_file_name: (_e = (_d = (_b = docs[0]) == null ? void 0 : _b.docName) != null ? _d : (_c = docs[0]) == null ? void 0 : _c.name) != null ? _e : null,
      part_1_file_data_base64: (_i = (_h = (_f = docs[0]) == null ? void 0 : _f.docContent) != null ? _h : (_g = docs[0]) == null ? void 0 : _g.docData) != null ? _i : null,
      part_2_file_name: (_m = (_l = (_j = docs[1]) == null ? void 0 : _j.docName) != null ? _l : (_k = docs[1]) == null ? void 0 : _k.name) != null ? _m : null,
      part_2_file_data_base64: (_q = (_p = (_n = docs[1]) == null ? void 0 : _n.docContent) != null ? _p : (_o = docs[1]) == null ? void 0 : _o.docData) != null ? _q : null
    };
  }
});

// ../ap/packages/pieces/community/pdf4me/src/lib/actions/compress-pdf.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var compressPdfAction = (0, import_pieces_framework5.createAction)({
  auth: pdf4meAuth,
  name: "compress_pdf",
  displayName: "Compress PDF",
  description: "Reduces the file size of a PDF document while maintaining readability.",
  audience: "both",
  aiMetadata: {
    description: "Reduces the size of a PDF via the PDF4me Optimize API, with an optional compression profile that trades size against quality (web, print, grayscale, or aggressive-maximum modes). Use when an agent needs to shrink a PDF for storage or delivery; the PDF file is required. A pure transformation that is idempotent \u2014 the same input and profile always yield the same optimized PDF with no stored side effect.",
    idempotent: !0
  },
  props: {
    file: import_pieces_framework5.Property.File({
      displayName: "PDF File",
      description: "The PDF file to compress.",
      required: !0
    }),
    optimizeProfile: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Compression Profile",
      description: "Controls the trade-off between file size and output quality.",
      required: !1,
      defaultValue: "Default",
      options: {
        options: [
          { label: "Default (balanced quality and size)", value: "Default" },
          { label: "Web (optimised for online viewing)", value: "Web" },
          { label: "Web Max (aggressive web optimisation)", value: "WebMax" },
          { label: "Print (optimised for printing)", value: "Print" },
          { label: "Print Max (aggressive print optimisation)", value: "PrintMax" },
          { label: "Print Gray (grayscale for print)", value: "PrintGray" },
          { label: "Compress (strong compression)", value: "Compress" },
          { label: "Compress Max (maximum compression)", value: "CompressMax" },
          { label: "Max (maximum size reduction)", value: "Max" }
        ]
      }
    })
  },
  async run(context) {
    let { file, optimizeProfile } = context.propsValue, response = await pdf4meCommon.callFileApi({
      apiKey: context.auth.secret_text,
      endpoint: "/api/v2/Optimize",
      body: {
        docContent: file.data.toString("base64"),
        docName: file.filename,
        optimizeProfile: optimizeProfile != null ? optimizeProfile : "Default"
      }
    });
    return {
      file_name: pdf4meCommon.fileNameFromHeaders(response.headers, file.filename),
      file_data_base64: Buffer.from(response.body).toString("base64"),
      success: !0
    };
  }
});

// ../ap/packages/pieces/community/pdf4me/src/lib/actions/protect-pdf.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var protectPdfAction = (0, import_pieces_framework6.createAction)({
  auth: pdf4meAuth,
  name: "protect_pdf",
  displayName: "Protect PDF with Password",
  description: "Adds password protection to a PDF file so only authorised users can open it.",
  audience: "both",
  aiMetadata: {
    description: "Encrypts a PDF with an open password via the PDF4me Protect API, optionally restricting permissions (copy, annotate, fill forms, print, etc.). Use when an agent needs to password-protect a PDF before sharing; the PDF file and password are required. A pure transformation that is idempotent \u2014 the same input, password, and permission setting always produce an equivalently protected PDF with no stored side effect.",
    idempotent: !0
  },
  props: {
    file: import_pieces_framework6.Property.File({
      displayName: "PDF File",
      description: "The PDF file to protect with a password.",
      required: !0
    }),
    password: import_pieces_framework6.Property.ShortText({
      displayName: "Password",
      description: "The password users will need to open the protected PDF.",
      required: !0
    }),
    pdfPermission: import_pieces_framework6.Property.StaticDropdown({
      displayName: "PDF Permissions",
      description: "Permissions granted to users who open the protected PDF.",
      required: !1,
      defaultValue: "All",
      options: {
        options: [
          { label: "All (no restrictions)", value: "All" },
          { label: "None (no permissions)", value: "None" },
          { label: "Copy", value: "Copy" },
          { label: "Annotate", value: "Annotate" },
          { label: "Fill Forms", value: "Fill Forms" },
          { label: "Support Disabilities", value: "Support Disabilities" },
          { label: "Assemble", value: "Assemble" },
          { label: "Digital Print", value: "Digital Print" }
        ]
      }
    })
  },
  async run(context) {
    let { file, password, pdfPermission } = context.propsValue, response = await pdf4meCommon.callFileApi({
      apiKey: context.auth.secret_text,
      endpoint: "/api/v2/Protect",
      body: {
        docContent: file.data.toString("base64"),
        docName: file.filename,
        password,
        pdfPermission: pdfPermission != null ? pdfPermission : "All"
      }
    });
    return {
      file_name: pdf4meCommon.fileNameFromHeaders(response.headers, file.filename),
      file_data_base64: Buffer.from(response.body).toString("base64"),
      success: !0
    };
  }
});

// ../ap/packages/pieces/community/pdf4me/src/lib/actions/pdf-to-word.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var LANGUAGE_OPTIONS = [
  { label: "English", value: "English" },
  { label: "French", value: "French" },
  { label: "German", value: "German" },
  { label: "Spanish", value: "Spanish" },
  { label: "Italian", value: "Italian" },
  { label: "Portuguese", value: "Portuguese" },
  { label: "Dutch", value: "Dutch" },
  { label: "Russian", value: "Russian" },
  { label: "Chinese", value: "Chinese" },
  { label: "Japanese", value: "Japanese" },
  { label: "Korean", value: "Korean" },
  { label: "Arabic", value: "Arabic" },
  { label: "Hindi", value: "Hindi" },
  { label: "Turkish", value: "Turkish" },
  { label: "Polish", value: "Polish" },
  { label: "Swedish", value: "Swedish" },
  { label: "Danish", value: "Danish" },
  { label: "Norwegian", value: "Norwegian" },
  { label: "Finnish", value: "Finnish" },
  { label: "Czech", value: "Czech" }
], pdfToWordAction = (0, import_pieces_framework7.createAction)({
  auth: pdf4meAuth,
  name: "pdf_to_word",
  displayName: "Convert PDF to Word",
  description: "Converts a PDF file to an editable Microsoft Word (DOCX) document.",
  audience: "both",
  aiMetadata: {
    description: "Converts a PDF into an editable Word (DOCX) document via the PDF4me API, with a quality mode (Draft for one-call processing vs. High per-page processing) and optional OCR for scanned/image-based PDFs. Use when an agent needs the text of a PDF as an editable Word file; the PDF file is required, and setting the correct document language improves OCR accuracy. A pure conversion that is idempotent \u2014 the same input and settings always yield the same DOCX with no stored side effect.",
    idempotent: !0
  },
  props: {
    file: import_pieces_framework7.Property.File({
      displayName: "PDF File",
      description: "The PDF file to convert to Word format.",
      required: !0
    }),
    qualityType: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Conversion Quality",
      description: "Draft processes the entire file in one API call. High uses per-page processing \u2014 best for scanned or image-heavy PDFs.",
      required: !1,
      defaultValue: "Draft",
      options: {
        options: [
          { label: "Draft (faster, one call per file)", value: "Draft" },
          { label: "High (slower, better for scanned PDFs)", value: "High" }
        ]
      }
    }),
    language: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Document Language",
      description: "Language of the text in the PDF. Used to improve OCR accuracy.",
      required: !1,
      defaultValue: "English",
      options: { options: LANGUAGE_OPTIONS }
    }),
    mergeAllSheets: import_pieces_framework7.Property.Checkbox({
      displayName: "Merge All Sheets",
      description: "Merge all pages into a single Word document sheet.",
      required: !1,
      defaultValue: !1
    }),
    ocrWhenNeeded: import_pieces_framework7.Property.Checkbox({
      displayName: "Enable OCR for Scanned PDFs",
      description: "Automatically apply OCR when the PDF contains scanned images instead of selectable text.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { file, qualityType, language, mergeAllSheets, ocrWhenNeeded } = context.propsValue, outputName = file.filename.replace(/\.pdf$/i, ""), response = await pdf4meCommon.callFileApi({
      apiKey: context.auth.secret_text,
      endpoint: "/api/v2/ConvertPdfToWord",
      body: {
        docContent: file.data.toString("base64"),
        docName: file.filename,
        qualityType: qualityType != null ? qualityType : "Draft",
        language: language != null ? language : "English",
        mergeAllSheets: mergeAllSheets != null ? mergeAllSheets : !1,
        outputFormat: "docx",
        ocrWhenNeeded: String(ocrWhenNeeded != null ? ocrWhenNeeded : !1)
      }
    });
    return {
      file_name: pdf4meCommon.fileNameFromHeaders(
        response.headers,
        `${outputName}.docx`
      ),
      file_data_base64: Buffer.from(response.body).toString("base64"),
      success: !0
    };
  }
});

// ../ap/packages/pieces/community/pdf4me/src/lib/actions/pdf-to-image.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var pdfToImageAction = (0, import_pieces_framework8.createAction)({
  auth: pdf4meAuth,
  name: "pdf_to_image",
  displayName: "Convert PDF to Image",
  description: "Converts one or more pages of a PDF file to an image.",
  audience: "both",
  aiMetadata: {
    description: 'Renders selected pages of a PDF as images via the PDF4me API; the page selection accepts a single page, a comma list, a range, or "all", and the output format (PNG, JPG, TIFF, BMP) and width in pixels are configurable. Use when an agent needs a rasterized image of PDF pages rather than the PDF itself; the PDF file is required. A pure transformation that is idempotent \u2014 the same input and settings always produce the same image(s) with no stored side effect.',
    idempotent: !0
  },
  props: {
    file: import_pieces_framework8.Property.File({
      displayName: "PDF File",
      description: "The PDF file to convert to an image.",
      required: !0
    }),
    pageNrs: import_pieces_framework8.Property.ShortText({
      displayName: "Page Numbers",
      description: 'Pages to convert. Examples: "1" (first page only), "1,3,5" (specific pages), "2-5" (range), "all" (every page). Defaults to the first page.',
      required: !1,
      defaultValue: "1"
    }),
    imageFormat: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Image Format",
      description: "The output image format.",
      required: !1,
      defaultValue: "PNG",
      options: {
        options: [
          { label: "PNG (lossless, recommended)", value: "PNG" },
          { label: "JPEG (smaller file size)", value: "JPG" },
          { label: "TIFF", value: "TIF" },
          { label: "BMP", value: "BMP" }
        ]
      }
    }),
    widthPixel: import_pieces_framework8.Property.Number({
      displayName: "Width (pixels)",
      description: "Output image width in pixels. Height is calculated automatically to maintain the page aspect ratio.",
      required: !1,
      defaultValue: 1920
    })
  },
  async run(context) {
    let { file, pageNrs, imageFormat, widthPixel } = context.propsValue, pageSelection = pageNrs != null ? pageNrs : "1", format = imageFormat != null ? imageFormat : "PNG", ext = format.toLowerCase(), response = await pdf4meCommon.callFileApi({
      apiKey: context.auth.secret_text,
      endpoint: "/api/v2/CreateImages",
      body: {
        docContent: file.data.toString("base64"),
        docname: file.filename,
        imageAction: {
          WidthPixel: widthPixel != null ? widthPixel : 1920,
          ImageExtension: format
        },
        pageNrs: pageSelection
      }
    }), baseName = file.filename.replace(/\.pdf$/i, "");
    return {
      file_name: pdf4meCommon.fileNameFromHeaders(
        response.headers,
        `${baseName}.${ext}`
      ),
      file_data_base64: Buffer.from(response.body).toString("base64"),
      image_format: ext,
      page_selection: pageSelection,
      success: !0
    };
  }
});

// ../ap/packages/pieces/community/pdf4me/src/index.ts
var pdf4me = (0, import_pieces_framework9.createPiece)({
  displayName: "PDF4me",
  description: "Convert, merge, split, compress, and protect PDF documents.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/pdf4me.png",
  categories: [import_pieces_framework10.PieceCategory.CONTENT_AND_FILES],
  auth: pdf4meAuth,
  authors: ["onyedikachi-david"],
  actions: [
    convertToPdfAction,
    mergePdfsAction,
    splitPdfAction,
    compressPdfAction,
    protectPdfAction,
    pdfToWordAction,
    pdfToImageAction,
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: () => pdf4meCommon.BASE_URL,
      auth: pdf4meAuth,
      authMapping: async (auth) => ({
        Authorization: auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  pdf4me,
  pdf4meAuth
});
