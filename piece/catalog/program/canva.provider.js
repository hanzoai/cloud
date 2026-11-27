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

// ../ap/packages/pieces/community/canva/src/index.ts
var index_exports = {};
__export(index_exports, {
  canva: () => canva,
  canvaAuth: () => canvaAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/canva/src/lib/common/index.ts
var canvaCommon = {
  baseUrl: "https://api.canva.com/rest/v1"
}, SUPPORTED_IMAGE_FORMATS = ["jpg", "jpeg", "png", "heic", "gif", "tiff", "webp"], SUPPORTED_VIDEO_FORMATS = ["m4v", "mkv", "mp4", "mpeg", "mov", "webm"], SUPPORTED_IMPORT_FORMATS = [
  { ext: "ai", mime: "application/illustrator", name: "Adobe Illustrator" },
  { ext: "psd", mime: "image/vnd.adobe.photoshop", name: "Adobe Photoshop" },
  { ext: "key", mime: "application/vnd.apple.keynote", name: "Apple Keynote" },
  { ext: "numbers", mime: "application/vnd.apple.numbers", name: "Apple Numbers" },
  { ext: "pages", mime: "application/vnd.apple.pages", name: "Apple Pages" },
  { ext: "xls", mime: "application/vnd.ms-excel", name: "Microsoft Excel (pre 2007)" },
  { ext: "xlsx", mime: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", name: "Microsoft Excel" },
  { ext: "ppt", mime: "application/vnd.ms-powerpoint", name: "Microsoft PowerPoint (pre 2007)" },
  { ext: "pptx", mime: "application/vnd.openxmlformats-officedocument.presentationml.presentation", name: "Microsoft PowerPoint" },
  { ext: "doc", mime: "application/msword", name: "Microsoft Word (pre 2007)" },
  { ext: "docx", mime: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", name: "Microsoft Word" },
  { ext: "odg", mime: "application/vnd.oasis.opendocument.graphics", name: "OpenOffice Draw" },
  { ext: "odp", mime: "application/vnd.oasis.opendocument.presentation", name: "OpenOffice Presentation" },
  { ext: "ods", mime: "application/vnd.oasis.opendocument.spreadsheet", name: "OpenOffice Sheets" },
  { ext: "odt", mime: "application/vnd.oasis.opendocument.text", name: "OpenOffice Text" },
  { ext: "pdf", mime: "application/pdf", name: "PDF" }
], EXPORT_FORMATS = [
  { value: "pdf", label: "PDF" },
  { value: "jpg", label: "JPEG" },
  { value: "png", label: "PNG" },
  { value: "gif", label: "GIF" },
  { value: "pptx", label: "PowerPoint (PPTX)" },
  { value: "mp4", label: "MP4 Video" }
], PDF_SIZES = [
  { value: "a4", label: "A4" },
  { value: "a3", label: "A3" },
  { value: "letter", label: "Letter" },
  { value: "legal", label: "Legal" }
], MP4_QUALITIES = [
  { value: "horizontal_480p", label: "Horizontal 480p" },
  { value: "horizontal_720p", label: "Horizontal 720p" },
  { value: "horizontal_1080p", label: "Horizontal 1080p" },
  { value: "horizontal_4k", label: "Horizontal 4K" },
  { value: "vertical_480p", label: "Vertical 480p" },
  { value: "vertical_720p", label: "Vertical 720p" },
  { value: "vertical_1080p", label: "Vertical 1080p" },
  { value: "vertical_4k", label: "Vertical 4K" }
], MAX_IMAGE_SIZE_MB = 50, MAX_VIDEO_SIZE_MB = 500, MAX_ASSET_NAME_LENGTH = 50, MAX_DESIGN_TITLE_LENGTH = 50, MIN_EXPORT_DIMENSION = 40, MAX_EXPORT_DIMENSION = 25e3;
async function fetchUserDesigns(auth, query) {
  try {
    let params = new URLSearchParams();
    query && query.trim() && params.append("query", query.trim()), params.append("sort_by", "modified_descending");
    let response = await fetch(`${canvaCommon.baseUrl}/designs?${params.toString()}`, {
      headers: {
        Authorization: `Bearer ${auth.access_token}`
      }
    });
    return response.ok ? ((await response.json()).items || []).map((design) => ({
      label: design.title || `Design ${design.id}`,
      value: design.id
    })) : [];
  } catch (error) {
    return console.error("Failed to fetch designs:", error), [];
  }
}
async function fetchUserAssets(auth, assetType) {
  try {
    let response = await fetch(`${canvaCommon.baseUrl}/folders/root/items?item_types=image`, {
      headers: {
        Authorization: `Bearer ${auth.access_token}`
      }
    });
    return response.ok ? ((await response.json()).items || []).filter((item) => {
      var _a;
      return item.type === "image" && (!assetType || ((_a = item.image) == null ? void 0 : _a.type) === assetType);
    }).map((item) => {
      var _a, _b, _c;
      return {
        label: ((_a = item.image) == null ? void 0 : _a.name) || `Asset ${(_b = item.image) == null ? void 0 : _b.id}`,
        value: ((_c = item.image) == null ? void 0 : _c.id) || ""
      };
    }).filter((item) => item.value) : [];
  } catch (error) {
    return console.error("Failed to fetch assets:", error), [];
  }
}
async function fetchUserFolders(auth) {
  try {
    let folders = [{ label: "Root (Top Level)", value: "root" }], response = await fetch(`${canvaCommon.baseUrl}/folders/root/items?item_types=folder`, {
      headers: {
        Authorization: `Bearer ${auth.access_token}`
      }
    });
    if (!response.ok) return folders;
    let subfolders = ((await response.json()).items || []).filter((item) => item.type === "folder").map((item) => {
      var _a, _b, _c;
      return {
        label: ((_a = item.folder) == null ? void 0 : _a.name) || `Folder ${(_b = item.folder) == null ? void 0 : _b.id}`,
        value: ((_c = item.folder) == null ? void 0 : _c.id) || ""
      };
    }).filter((item) => item.value);
    return [...folders, ...subfolders];
  } catch (error) {
    return console.error("Failed to fetch folders:", error), [{ label: "Root (Top Level)", value: "root" }];
  }
}

// ../ap/packages/pieces/community/canva/src/lib/actions/create-design.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var createDesign = (0, import_pieces_framework.createAction)({
  auth: canvaAuth,
  name: "create_design",
  classification: "WRITE",
  displayName: "Create Design",
  description: "Create a new Canva design using preset design types or custom dimensions. Blank designs are automatically deleted if not edited within 7 days.",
  audience: "both",
  aiMetadata: {
    description: "Creates a brand-new Canva design, either from a preset type (doc, whiteboard, presentation) or with custom pixel dimensions (40-8000px), optionally titled and seeded with an existing image asset. Pick this to start a fresh design; use Import Design instead when converting an existing file like a PDF or PPTX. Not idempotent: every call creates another design, and untouched blank designs are auto-deleted after 7 days.",
    idempotent: !1
  },
  props: {
    designTypeOption: import_pieces_framework.Property.StaticDropdown({
      displayName: "\u{1F3A8} Design Type",
      description: "Choose how to create your design - preset templates or custom dimensions",
      required: !0,
      defaultValue: "preset",
      options: {
        options: [
          { label: "\u{1F4CB} Preset Design Type (Document, Whiteboard, Presentation)", value: "preset" },
          { label: "\u{1F4D0} Custom Dimensions (Specify width & height)", value: "custom" }
        ]
      }
    }),
    presetName: import_pieces_framework.Property.StaticDropdown({
      displayName: "Preset Design Type",
      description: '\u26A0\uFE0F Only fill this if you selected "Preset Design Type" above',
      required: !1,
      options: {
        options: [
          { label: "Document (Canva Docs)", value: "doc" },
          { label: "Whiteboard", value: "whiteboard" },
          { label: "Presentation", value: "presentation" }
        ]
      }
    }),
    customWidth: import_pieces_framework.Property.Number({
      displayName: "Custom Width (pixels)",
      description: '\u26A0\uFE0F Only fill this if you selected "Custom Dimensions" above. Min: 40, Max: 8000',
      required: !1
    }),
    customHeight: import_pieces_framework.Property.Number({
      displayName: "Custom Height (pixels)",
      description: '\u26A0\uFE0F Only fill this if you selected "Custom Dimensions" above. Min: 40, Max: 8000',
      required: !1
    }),
    title: import_pieces_framework.Property.ShortText({
      displayName: "\u{1F3F7}\uFE0F Design Title",
      description: "Optional: Name for your design (1-255 characters). If empty, Canva will auto-generate a title.",
      required: !1
    }),
    assetId: import_pieces_framework.Property.Dropdown({
      displayName: "Asset",
      description: "Optional: Select an existing image asset to add to the design",
      required: !1,
      refreshers: [],
      auth: canvaAuth,
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Please authenticate with Canva first",
            options: []
          };
        try {
          return {
            disabled: !1,
            options: await fetchUserAssets(auth, "image")
          };
        } catch (error) {
          return console.error("Error fetching assets:", error), {
            disabled: !0,
            placeholder: "Error loading assets",
            options: []
          };
        }
      }
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e;
    let { designTypeOption, presetName, customWidth, customHeight, title, assetId } = context.propsValue;
    if (designTypeOption === "preset" && !presetName)
      throw new Error("Preset design type is required when using preset option");
    if (designTypeOption === "custom") {
      if (!customWidth || !customHeight)
        throw new Error("Both custom width and height are required when using custom dimensions");
      if (customWidth < 40 || customWidth > 8e3)
        throw new Error("Custom width must be between 40 and 8000 pixels");
      if (customHeight < 40 || customHeight > 8e3)
        throw new Error("Custom height must be between 40 and 8000 pixels");
    }
    if (title && (title.length < 1 || title.length > 255))
      throw new Error("Title must be between 1 and 255 characters");
    let requestBody = {};
    designTypeOption === "preset" ? requestBody.design_type = {
      type: "preset",
      name: presetName
    } : designTypeOption === "custom" && (requestBody.design_type = {
      type: "custom",
      width: customWidth,
      height: customHeight
    }), title && (requestBody.title = title), assetId && (requestBody.asset_id = assetId);
    try {
      let response = await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.POST,
        url: `${canvaCommon.baseUrl}/designs`,
        headers: {
          "Content-Type": "application/json"
        },
        authentication: {
          type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
          token: context.auth.access_token
        },
        body: requestBody
      });
      return {
        success: !0,
        design: response.body.design,
        message: `Design "${response.body.design.title || response.body.design.id}" created successfully`
      };
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 429 ? new Error("Rate limit exceeded. Please wait before making another request (20 requests per minute limit).") : ((_b = error.response) == null ? void 0 : _b.status) === 401 ? new Error("Authentication failed. Please check your Canva connection.") : ((_c = error.response) == null ? void 0 : _c.status) === 403 ? new Error("Access forbidden. Make sure your integration has the required scope: design:content:write") : (_e = (_d = error.response) == null ? void 0 : _d.data) != null && _e.message ? new Error(`Canva API error: ${error.response.data.message}`) : new Error(`Failed to create design: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/canva/src/lib/actions/get-design.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var getDesign = (0, import_pieces_framework2.createAction)({
  auth: canvaAuth,
  name: "get_design",
  classification: "READ",
  displayName: "Get Design",
  description: "Get the metadata for one of your designs, including owner information, URLs for editing and viewing, and thumbnail information.",
  audience: "both",
  aiMetadata: {
    description: "Fetches metadata for a single Canva design by design ID, including owner, edit/view URLs, and thumbnail. Pick this when you already know the design ID; use Find Design first if you only have a title or keywords. Read-only and idempotent; fails with 404 if the design does not exist or is inaccessible.",
    idempotent: !0
  },
  props: {
    designId: import_pieces_framework2.Property.Dropdown({
      displayName: "Design",
      description: "Select the design to retrieve metadata for",
      required: !0,
      refreshers: [],
      auth: canvaAuth,
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Please authenticate with Canva first",
            options: []
          };
        try {
          return {
            disabled: !1,
            options: await fetchUserDesigns(auth)
          };
        } catch (error) {
          return console.error("Error fetching designs:", error), {
            disabled: !0,
            placeholder: "Error loading designs",
            options: []
          };
        }
      }
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f;
    let { designId } = context.propsValue;
    if (!designId || designId.trim().length === 0)
      throw new Error("Design ID is required");
    try {
      let response = await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.GET,
        url: `${canvaCommon.baseUrl}/designs/${designId.trim()}`,
        authentication: {
          type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
          token: context.auth.access_token
        }
      });
      return {
        success: !0,
        design: response.body.design,
        message: `Design "${response.body.design.title || response.body.design.id}" retrieved successfully`
      };
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 429 ? new Error("Rate limit exceeded. Please wait before making another request (100 requests per minute limit).") : ((_b = error.response) == null ? void 0 : _b.status) === 401 ? new Error("Authentication failed. Please check your Canva connection.") : ((_c = error.response) == null ? void 0 : _c.status) === 403 ? new Error("Access forbidden. Make sure your integration has the required scope: design:meta:read") : ((_d = error.response) == null ? void 0 : _d.status) === 404 ? new Error(`Design with ID "${designId}" was not found or you don't have access to it.`) : (_f = (_e = error.response) == null ? void 0 : _e.data) != null && _f.message ? new Error(`Canva API error: ${error.response.data.message}`) : new Error(`Failed to retrieve design: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/canva/src/lib/actions/upload-asset.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var uploadAsset = (0, import_pieces_framework3.createAction)({
  auth: canvaAuth,
  name: "upload_asset",
  classification: "WRITE",
  displayName: "Upload Asset",
  description: "Upload an image or video asset to your Canva library. This creates an asynchronous job that you can monitor for completion.",
  audience: "both",
  aiMetadata: {
    description: "Uploads an image (max 50MB) or video (max 500MB) file into the user's Canva media library via an asynchronous job, with a required asset name of at most 50 characters. Pick this to add media for later use in designs; use Import Design instead when the file should become an editable design itself. Not idempotent: each call starts a new upload and creates a duplicate asset.",
    idempotent: !1
  },
  props: {
    file: import_pieces_framework3.Property.File({
      displayName: "Asset File",
      description: `\u{1F4C1} Supported formats:
      \u{1F5BC}\uFE0F Images (max 50MB): ${SUPPORTED_IMAGE_FORMATS.map((f) => f.toUpperCase()).join(", ")}
      \u{1F3AC} Videos (max 500MB): ${SUPPORTED_VIDEO_FORMATS.map((f) => f.toUpperCase()).join(", ")}`,
      required: !0
    }),
    assetName: import_pieces_framework3.Property.ShortText({
      displayName: "Asset Name",
      description: "\u{1F3F7}\uFE0F Name for the asset (max 50 characters, supports emojis and special characters)",
      required: !0
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h;
    let { file, assetName } = context.propsValue;
    if (!assetName || assetName.trim().length === 0)
      throw new Error("Asset name is required");
    if (assetName.length > MAX_ASSET_NAME_LENGTH)
      throw new Error(`Asset name must be ${MAX_ASSET_NAME_LENGTH} characters or less`);
    if (!file)
      throw new Error("File is required");
    let fileExtension = (_b = (_a = file.filename) == null ? void 0 : _a.split(".").pop()) == null ? void 0 : _b.toLowerCase();
    if (!fileExtension)
      throw new Error("Unable to determine file type from filename");
    let isImage = SUPPORTED_IMAGE_FORMATS.includes(fileExtension), isVideo = SUPPORTED_VIDEO_FORMATS.includes(fileExtension);
    if (!isImage && !isVideo)
      throw new Error(`Unsupported file format: ${fileExtension}. Supported formats - Images: ${SUPPORTED_IMAGE_FORMATS.join(", ")}. Videos: ${SUPPORTED_VIDEO_FORMATS.join(", ")}`);
    let fileBuffer = Buffer.from(file.base64, "base64"), fileSizeMB = fileBuffer.length / (1024 * 1024);
    if (isImage && fileSizeMB > MAX_IMAGE_SIZE_MB)
      throw new Error(`Image file size (${fileSizeMB.toFixed(2)}MB) exceeds maximum limit of ${MAX_IMAGE_SIZE_MB}MB`);
    if (isVideo && fileSizeMB > MAX_VIDEO_SIZE_MB)
      throw new Error(`Video file size (${fileSizeMB.toFixed(2)}MB) exceeds maximum limit of ${MAX_VIDEO_SIZE_MB}MB`);
    let uploadMetadata = {
      name_base64: Buffer.from(assetName.trim()).toString("base64")
    };
    try {
      let response = await import_pieces_common3.httpClient.sendRequest({
        method: import_pieces_common3.HttpMethod.POST,
        url: `${canvaCommon.baseUrl}/asset-uploads`,
        headers: {
          "Content-Type": "application/octet-stream",
          "Asset-Upload-Metadata": JSON.stringify(uploadMetadata)
        },
        authentication: {
          type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
          token: context.auth.access_token
        },
        body: fileBuffer
      });
      return {
        success: !0,
        job: response.body.job,
        file_info: {
          name: assetName.trim(),
          size_mb: Math.round(fileSizeMB * 100) / 100,
          type: isImage ? "image" : "video",
          format: fileExtension.toUpperCase()
        },
        message: `Asset upload job created successfully. Job ID: ${response.body.job.id}. Status: ${response.body.job.status}`,
        next_steps: response.body.job.status === "in_progress" ? 'Use the "Get Asset Upload Job" action to check the status and get the final asset details.' : void 0
      };
    } catch (error) {
      throw ((_c = error.response) == null ? void 0 : _c.status) === 429 ? new Error("Rate limit exceeded. Please wait before making another request (30 requests per minute limit).") : ((_d = error.response) == null ? void 0 : _d.status) === 401 ? new Error("Authentication failed. Please check your Canva connection.") : ((_e = error.response) == null ? void 0 : _e.status) === 403 ? new Error("Access forbidden. Make sure your integration has the required scope: asset:write") : ((_f = error.response) == null ? void 0 : _f.status) === 413 ? new Error("File too large. Check file size limits: Images max 50MB, Videos max 500MB.") : (_h = (_g = error.response) == null ? void 0 : _g.data) != null && _h.message ? new Error(`Canva API error: ${error.response.data.message}`) : new Error(`Failed to upload asset: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/canva/src/lib/actions/import-design.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var importDesign = (0, import_pieces_framework4.createAction)({
  auth: canvaAuth,
  name: "import_design",
  classification: "WRITE",
  displayName: "Import Design",
  description: "Import an external file (PDF, PSD, DOCX, PPTX, etc.) into Canva as a new design. This creates an asynchronous job that you can monitor for completion.",
  audience: "both",
  aiMetadata: {
    description: "Uploads an external document or design file (PDF, DOCX, PPTX, XLSX, PSD, AI, and similar) and converts it into a new Canva design via an asynchronous import job, with a required title of at most 50 characters. Pick this to bring existing files into Canva as editable designs; use Create Design for a blank design and Upload Asset for images/videos meant for the media library. Not idempotent: each call starts a new import and can create duplicate designs.",
    idempotent: !1
  },
  props: {
    file: import_pieces_framework4.Property.File({
      displayName: "Design File",
      description: `\u{1F4C4} Supported formats:
      \u{1F4CB} Documents: PDF, DOC, DOCX, ODT, Pages
      \u{1F4CA} Presentations: PPT, PPTX, ODP, Keynote  
      \u{1F4C8} Spreadsheets: XLS, XLSX, ODS, Numbers
      \u{1F3A8} Design Files: AI (Illustrator), PSD (Photoshop), ODG`,
      required: !0
    }),
    designTitle: import_pieces_framework4.Property.ShortText({
      displayName: "Design Title",
      description: "\u{1F3F7}\uFE0F Title for the imported design (max 50 characters, supports emojis and special characters)",
      required: !0
    }),
    autoDetectMimeType: import_pieces_framework4.Property.Checkbox({
      displayName: "\u{1F50D} Auto-detect File Type",
      description: "\u2705 Recommended: Let Canva automatically detect the file type. Uncheck to manually specify MIME type from file extension.",
      required: !1,
      defaultValue: !0
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i;
    let { file, designTitle, autoDetectMimeType } = context.propsValue;
    if (!designTitle || designTitle.trim().length === 0)
      throw new Error("Design title is required");
    if (designTitle.length > MAX_DESIGN_TITLE_LENGTH)
      throw new Error(`Design title must be ${MAX_DESIGN_TITLE_LENGTH} characters or less`);
    if (!file)
      throw new Error("File is required");
    let fileExtension = (_b = (_a = file.filename) == null ? void 0 : _a.split(".").pop()) == null ? void 0 : _b.toLowerCase();
    if (!fileExtension)
      throw new Error("Unable to determine file type from filename");
    let supportedFormat = SUPPORTED_IMPORT_FORMATS.find((format) => format.ext === fileExtension);
    if (!supportedFormat) {
      let supportedExts = SUPPORTED_IMPORT_FORMATS.map((f) => f.ext.toUpperCase()).join(", ");
      throw new Error(`Unsupported file format: ${fileExtension.toUpperCase()}. Supported formats: ${supportedExts}`);
    }
    let fileBuffer = Buffer.from(file.base64, "base64"), fileSizeMB = fileBuffer.length / (1024 * 1024), importMetadata = {
      title_base64: Buffer.from(designTitle.trim()).toString("base64")
    };
    autoDetectMimeType || (importMetadata.mime_type = supportedFormat.mime);
    try {
      let response = await import_pieces_common4.httpClient.sendRequest({
        method: import_pieces_common4.HttpMethod.POST,
        url: `${canvaCommon.baseUrl}/imports`,
        headers: {
          "Content-Type": "application/octet-stream",
          "Import-Metadata": JSON.stringify(importMetadata)
        },
        authentication: {
          type: import_pieces_common4.AuthenticationType.BEARER_TOKEN,
          token: context.auth.access_token
        },
        body: fileBuffer
      });
      return {
        success: !0,
        job: response.body.job,
        file_info: {
          title: designTitle.trim(),
          size_mb: Math.round(fileSizeMB * 100) / 100,
          format: supportedFormat.name,
          extension: fileExtension.toUpperCase(),
          mime_type: autoDetectMimeType ? "auto-detected" : supportedFormat.mime
        },
        message: `Design import job created successfully. Job ID: ${response.body.job.id}. Status: ${response.body.job.status}`,
        next_steps: response.body.job.status === "in_progress" ? 'Use the "Get Design Import Job" action to check the status and get the imported design details.' : response.body.job.status === "success" && response.body.job.result ? `Import completed! ${response.body.job.result.designs.length} design(s) created.` : void 0
      };
    } catch (error) {
      throw ((_c = error.response) == null ? void 0 : _c.status) === 429 ? new Error("Rate limit exceeded. Please wait before making another request (20 requests per minute limit).") : ((_d = error.response) == null ? void 0 : _d.status) === 401 ? new Error("Authentication failed. Please check your Canva connection.") : ((_e = error.response) == null ? void 0 : _e.status) === 403 ? new Error("Access forbidden. Make sure your integration has the required scope: design:content:write") : ((_f = error.response) == null ? void 0 : _f.status) === 413 ? new Error("File too large. Check the upload requirements for your file type.") : ((_g = error.response) == null ? void 0 : _g.status) === 415 ? new Error(`Unsupported media type. File format ${fileExtension.toUpperCase()} may not be supported or the file may be corrupted.`) : (_i = (_h = error.response) == null ? void 0 : _h.data) != null && _i.message ? new Error(`Canva API error: ${error.response.data.message}`) : new Error(`Failed to import design: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/canva/src/lib/actions/export-design.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var exportDesign = (0, import_pieces_framework5.createAction)({
  auth: canvaAuth,
  name: "export_design",
  classification: "READ",
  displayName: "Export Design",
  description: "Export a Canva design to various formats (PDF, JPG, PNG, GIF, PPTX, MP4). This creates an asynchronous job with download URLs valid for 24 hours.",
  audience: "both",
  aiMetadata: {
    description: "Starts an asynchronous export job that renders an existing Canva design as PDF, JPG, PNG, GIF, PPTX, or MP4, with format-specific options (quality, page selection, custom image dimensions, PDF paper size). Pick this when you need downloadable output files from a design; download URLs expire after 24 hours and the job may need to be polled for completion. Not idempotent: each call queues a new export job.",
    idempotent: !1
  },
  props: {
    designId: import_pieces_framework5.Property.Dropdown({
      displayName: "Design",
      description: "Select the design to export",
      required: !0,
      auth: canvaAuth,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Please authenticate with Canva first",
            options: []
          };
        try {
          return {
            disabled: !1,
            options: await fetchUserDesigns(auth)
          };
        } catch (error) {
          return console.error("Error fetching designs:", error), {
            disabled: !0,
            placeholder: "Error loading designs",
            options: []
          };
        }
      }
    }),
    exportFormat: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Export Format",
      description: "The format to export the design to",
      required: !0,
      options: {
        options: EXPORT_FORMATS
      }
    }),
    exportQuality: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Export Quality",
      description: "Export quality level (Pro quality may require premium elements license)",
      required: !1,
      defaultValue: "regular",
      options: {
        options: [
          { label: "Regular Quality", value: "regular" },
          { label: "Pro Quality (Premium)", value: "pro" }
        ]
      }
    }),
    pages: import_pieces_framework5.Property.Array({
      displayName: "Specific Pages",
      description: "Page numbers to export (leave empty to export all pages). First page is 1.",
      required: !1
    }),
    jpegQuality: import_pieces_framework5.Property.Number({
      displayName: "\u{1F4F8} JPEG Quality",
      description: "\u{1F3AF} JPEG ONLY: Compression quality (1-100, higher = better quality, larger file)",
      required: !1,
      defaultValue: 80
    }),
    pdfSize: import_pieces_framework5.Property.StaticDropdown({
      displayName: "\u{1F4C4} PDF Paper Size",
      description: "\u{1F3AF} PDF ONLY: Paper size for PDF export (only for Canva Docs)",
      required: !1,
      defaultValue: "a4",
      options: {
        options: PDF_SIZES
      }
    }),
    mp4Quality: import_pieces_framework5.Property.StaticDropdown({
      displayName: "\u{1F3AC} MP4 Quality",
      description: "\u{1F3AF} MP4 ONLY: Video orientation and resolution for MP4 export",
      required: !1,
      defaultValue: "horizontal_1080p",
      options: {
        options: MP4_QUALITIES
      }
    }),
    customWidth: import_pieces_framework5.Property.Number({
      displayName: "\u{1F4D0} Custom Width (pixels)",
      description: `\u{1F3AF} IMAGES ONLY (JPG/PNG/GIF): Custom width (${MIN_EXPORT_DIMENSION}-${MAX_EXPORT_DIMENSION}px). Leave empty for original size.`,
      required: !1
    }),
    customHeight: import_pieces_framework5.Property.Number({
      displayName: "\u{1F4D0} Custom Height (pixels)",
      description: `\u{1F3AF} IMAGES ONLY (JPG/PNG/GIF): Custom height (${MIN_EXPORT_DIMENSION}-${MAX_EXPORT_DIMENSION}px). Leave empty for original size.`,
      required: !1
    }),
    pngLossless: import_pieces_framework5.Property.Checkbox({
      displayName: "\u{1F527} PNG Lossless Compression",
      description: "\u{1F3AF} PNG ONLY: Export PNG without compression (default: true). Lossy compression requires Pro plan.",
      required: !1,
      defaultValue: !0
    }),
    pngTransparentBackground: import_pieces_framework5.Property.Checkbox({
      displayName: "\u{1FAE5} PNG Transparent Background",
      description: "\u{1F3AF} PNG ONLY: Export PNG with transparent background (Pro feature)",
      required: !1,
      defaultValue: !1
    }),
    pngSingleImage: import_pieces_framework5.Property.Checkbox({
      displayName: "\u{1F4D1} PNG Merge Multi-page",
      description: "\u{1F3AF} PNG ONLY: Merge multi-page designs into a single image (default: separate images per page)",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f;
    let {
      designId,
      exportFormat,
      exportQuality,
      pages,
      jpegQuality,
      pdfSize,
      mp4Quality,
      customWidth,
      customHeight,
      pngLossless,
      pngTransparentBackground,
      pngSingleImage
    } = context.propsValue;
    if (!designId || designId.trim().length === 0)
      throw new Error("Design ID is required");
    if (customWidth && (customWidth < MIN_EXPORT_DIMENSION || customWidth > MAX_EXPORT_DIMENSION))
      throw new Error(`Custom width must be between ${MIN_EXPORT_DIMENSION} and ${MAX_EXPORT_DIMENSION} pixels`);
    if (customHeight && (customHeight < MIN_EXPORT_DIMENSION || customHeight > MAX_EXPORT_DIMENSION))
      throw new Error(`Custom height must be between ${MIN_EXPORT_DIMENSION} and ${MAX_EXPORT_DIMENSION} pixels`);
    if (exportFormat === "jpg" && (!jpegQuality || jpegQuality < 1 || jpegQuality > 100))
      throw new Error("JPEG quality must be between 1 and 100");
    if (exportFormat === "mp4" && !mp4Quality)
      throw new Error("MP4 quality is required when exporting as MP4");
    let format = {
      type: exportFormat
    };
    switch (exportQuality && (format.export_quality = exportQuality), pages && Array.isArray(pages) && pages.length > 0 && (format.pages = pages.map((p) => Number(p)).filter((p) => p > 0)), exportFormat) {
      case "jpg":
        jpegQuality && (format.quality = jpegQuality), customWidth && (format.width = customWidth), customHeight && (format.height = customHeight);
        break;
      case "png":
        customWidth && (format.width = customWidth), customHeight && (format.height = customHeight), typeof pngLossless == "boolean" && (format.lossless = pngLossless), typeof pngTransparentBackground == "boolean" && (format.transparent_background = pngTransparentBackground), typeof pngSingleImage == "boolean" && (format.as_single_image = pngSingleImage);
        break;
      case "gif":
        customWidth && (format.width = customWidth), customHeight && (format.height = customHeight);
        break;
      case "pdf":
        pdfSize && (format.size = pdfSize);
        break;
      case "mp4":
        mp4Quality && (format.quality = mp4Quality);
        break;
      case "pptx":
        break;
    }
    let requestBody = {
      design_id: designId.trim(),
      format
    };
    try {
      let response = await import_pieces_common5.httpClient.sendRequest({
        method: import_pieces_common5.HttpMethod.POST,
        url: `${canvaCommon.baseUrl}/exports`,
        headers: {
          "Content-Type": "application/json"
        },
        authentication: {
          type: import_pieces_common5.AuthenticationType.BEARER_TOKEN,
          token: context.auth.access_token
        },
        body: requestBody
      });
      return {
        success: !0,
        job: response.body.job,
        export_info: {
          design_id: designId.trim(),
          format: exportFormat.toUpperCase(),
          quality: exportQuality || "regular",
          pages: format.pages ? `Pages: ${format.pages.join(", ")}` : "All pages",
          dimensions: customWidth || customHeight ? `${customWidth || "auto"}x${customHeight || "auto"} pixels` : "Original size"
        },
        message: `Export job created successfully. Job ID: ${response.body.job.id}. Status: ${response.body.job.status}`,
        next_steps: response.body.job.status === "in_progress" ? 'Use the "Get Design Export Job" action to check the status and get download URLs (valid for 24 hours).' : response.body.job.status === "success" && response.body.job.urls ? `Export completed! ${response.body.job.urls.length} file(s) ready for download.` : void 0,
        download_urls: response.body.job.urls || void 0
      };
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 429 ? new Error("Rate limit exceeded. Please wait before making another request (20 requests per minute limit).") : ((_b = error.response) == null ? void 0 : _b.status) === 401 ? new Error("Authentication failed. Please check your Canva connection.") : ((_c = error.response) == null ? void 0 : _c.status) === 403 ? new Error("Access forbidden. Make sure your integration has the required scope: design:content:read") : ((_d = error.response) == null ? void 0 : _d.status) === 404 ? new Error(`Design with ID "${designId}" was not found or you don't have access to it.`) : (_f = (_e = error.response) == null ? void 0 : _e.data) != null && _f.message ? new Error(`Canva API error: ${error.response.data.message}`) : new Error(`Failed to export design: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/canva/src/lib/actions/move-folder-item.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var moveFolderItem = (0, import_pieces_framework6.createAction)({
  auth: canvaAuth,
  name: "move_folder_item",
  classification: "WRITE",
  displayName: "Move Folder Item",
  description: "Move an item from one folder to another in Canva",
  audience: "both",
  aiMetadata: {
    description: "Moves a design or image asset into a specified destination folder in Canva, identified by item ID and folder ID. Pick this to reorganize library content; it fails for items that currently live in multiple folders (those must be moved in the Canva UI). Effectively idempotent: repeating the same move leaves the item in the same destination folder.",
    idempotent: !0
  },
  props: {
    to_folder_id: import_pieces_framework6.Property.Dropdown({
      displayName: "Destination Folder",
      description: "Select the folder to move the item to",
      required: !0,
      refreshers: [],
      auth: canvaAuth,
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Please authenticate with Canva first",
            options: []
          };
        try {
          return {
            disabled: !1,
            options: await fetchUserFolders(auth)
          };
        } catch (error) {
          return console.error("Error fetching folders:", error), {
            disabled: !0,
            placeholder: "Error loading folders",
            options: []
          };
        }
      }
    }),
    item_type: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Item Type",
      description: "Select the type of item you want to move",
      required: !0,
      defaultValue: "design",
      options: {
        options: [
          { label: "Design", value: "design" },
          { label: "Image Asset", value: "image" }
        ]
      }
    }),
    item_id: import_pieces_framework6.Property.Dropdown({
      displayName: "Item to Move",
      description: "Select the item you want to move",
      required: !0,
      auth: canvaAuth,
      refreshers: ["item_type"],
      options: async ({ auth, item_type }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Please authenticate with Canva first",
            options: []
          };
        if (!item_type)
          return {
            disabled: !0,
            placeholder: "Please select an item type first",
            options: []
          };
        try {
          let items = [];
          return item_type === "design" ? items = await fetchUserDesigns(auth) : item_type === "image" && (items = await fetchUserAssets(auth, "image")), {
            disabled: !1,
            options: items
          };
        } catch (error) {
          return console.error("Error fetching items:", error), {
            disabled: !0,
            placeholder: "Error loading items",
            options: []
          };
        }
      }
    })
  },
  async run(context) {
    let { to_folder_id, item_id } = context.propsValue, authValue = context.auth;
    try {
      let response = await fetch(`${canvaCommon.baseUrl}/folders/move`, {
        method: "POST",
        headers: {
          Authorization: `Bearer ${authValue.access_token}`,
          "Content-Type": "application/json"
        },
        body: JSON.stringify({
          to_folder_id,
          item_id
        })
      });
      if (!response.ok) {
        let errorData = await response.json().catch(() => ({}));
        throw response.status === 400 ? errorData.type === "item_in_multiple_folders" ? new Error(
          "This item exists in multiple folders. Please use the Canva UI to move the item to another folder."
        ) : new Error(`Bad Request: ${errorData.message || "Invalid request parameters"}`) : response.status === 401 ? new Error("Unauthorized: Please check your authentication credentials") : response.status === 403 ? new Error("Forbidden: Insufficient permissions or invalid folder/item access") : response.status === 404 ? new Error("Not Found: The specified folder or item does not exist") : response.status === 429 ? new Error("Rate limit exceeded: Too many requests (limit: 100 per minute)") : new Error(`HTTP ${response.status}: ${errorData.message || "Request failed"}`);
      }
      return {
        success: !0,
        message: `Item ${item_id} successfully moved to folder ${to_folder_id}`,
        moved_item_id: item_id,
        destination_folder_id: to_folder_id
      };
    } catch (error) {
      throw error instanceof Error ? error : new Error(`Failed to move folder item: ${String(error)}`);
    }
  }
});

// ../ap/packages/pieces/community/canva/src/lib/actions/find-design.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var findDesign = (0, import_pieces_framework7.createAction)({
  auth: canvaAuth,
  name: "find_design",
  classification: "SEARCH",
  displayName: "Find Design",
  description: "Search and list designs from your Canva library. Use this to find existing designs before creating new ones.",
  audience: "both",
  aiMetadata: {
    description: "Searches the user's Canva design library by keyword, with ownership (owned/shared) and sort filters plus continuation-token pagination; an empty query lists all designs. Pick this to discover or look up design IDs before getting, exporting, or moving a design, rather than creating a duplicate. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    query: import_pieces_framework7.Property.ShortText({
      displayName: "\u{1F50D} Search Query",
      description: "Enter keywords to search designs by title or content (leave empty to get all designs)",
      required: !1
    }),
    ownership: import_pieces_framework7.Property.StaticDropdown({
      displayName: "\u{1F465} Ownership Filter",
      description: "Filter designs by who owns them",
      required: !1,
      defaultValue: "any",
      options: {
        options: [
          { label: "Any (owned + shared)", value: "any" },
          { label: "Owned by me", value: "owned" },
          { label: "Shared with me", value: "shared" }
        ]
      }
    }),
    sort_by: import_pieces_framework7.Property.StaticDropdown({
      displayName: "\u{1F4CA} Sort By",
      description: "How to order the search results",
      required: !1,
      defaultValue: "modified_descending",
      options: {
        options: [
          { label: "\u{1F4C5} Recently Modified (Recommended)", value: "modified_descending" },
          { label: "\u{1F50D} Relevance", value: "relevance" },
          { label: "\u{1F4C5} Oldest Modified", value: "modified_ascending" },
          { label: "\u{1F524} Title A-Z", value: "title_ascending" },
          { label: "\u{1F524} Title Z-A", value: "title_descending" }
        ]
      }
    }),
    limit: import_pieces_framework7.Property.Number({
      displayName: "\u2699\uFE0F Result Limit (Advanced)",
      description: "Max designs to return (default: 20, max: 100). Higher numbers may be slower.",
      required: !1,
      defaultValue: 20
    }),
    continuation: import_pieces_framework7.Property.ShortText({
      displayName: "\u2699\uFE0F Continuation Token (Advanced)",
      description: "For pagination: token from previous search to get next page of results",
      required: !1
    })
  },
  async run(context) {
    let { query, ownership, sort_by, limit, continuation } = context.propsValue, authValue = context.auth;
    try {
      let params = new URLSearchParams();
      query && query.trim() && params.append("query", query.trim()), ownership && ownership !== "any" && params.append("ownership", ownership), sort_by && sort_by !== "relevance" && params.append("sort_by", sort_by), continuation && continuation.trim() && params.append("continuation", continuation.trim());
      let queryString = params.toString(), url = `${canvaCommon.baseUrl}/designs${queryString ? `?${queryString}` : ""}`, response = await fetch(url, {
        method: "GET",
        headers: {
          Authorization: `Bearer ${authValue.access_token}`
        }
      });
      if (!response.ok) {
        let errorData = await response.json().catch(() => ({}));
        throw response.status === 400 ? new Error(`Bad Request: ${errorData.message || "Invalid search parameters"}`) : response.status === 401 ? new Error("Unauthorized: Please check your authentication credentials") : response.status === 403 ? new Error("Forbidden: Insufficient permissions to access designs") : response.status === 429 ? new Error("Rate limit exceeded: Too many requests (limit: 100 per minute)") : new Error(`HTTP ${response.status}: ${errorData.message || "Request failed"}`);
      }
      let data = await response.json(), designs = data.items || [];
      return limit && limit > 0 && (designs = designs.slice(0, Math.min(limit, 100))), {
        designs,
        total_found: designs.length,
        has_more: !!data.continuation,
        continuation_token: data.continuation,
        search_query: query || null,
        ownership_filter: ownership || "any",
        sort_order: sort_by || "relevance",
        design_ids: designs.map((d) => d.id),
        design_titles: designs.map((d) => d.title).filter(Boolean)
      };
    } catch (error) {
      throw error instanceof Error ? error : new Error(`Failed to find designs: ${String(error)}`);
    }
  }
});

// ../ap/packages/pieces/community/canva/src/lib/actions/get-folder.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var getFolder = (0, import_pieces_framework8.createAction)({
  auth: canvaAuth,
  name: "get_folder",
  classification: "READ",
  displayName: "Get Folder",
  description: "Retrieve metadata for a specific folder, including name, creation date, and thumbnail.",
  audience: "both",
  aiMetadata: {
    description: "Fetches metadata for a single Canva folder by folder ID, including its name, created/updated timestamps, and thumbnail. Pick this to verify a folder exists or get its details before moving items into it; it returns folder metadata only, not the folder contents. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    folder_id: import_pieces_framework8.Property.Dropdown({
      displayName: "Folder",
      description: "Select the folder to retrieve metadata for",
      required: !0,
      auth: canvaAuth,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Please authenticate with Canva first",
            options: []
          };
        try {
          return {
            disabled: !1,
            options: await fetchUserFolders(auth)
          };
        } catch (error) {
          return console.error("Error fetching folders:", error), {
            disabled: !0,
            placeholder: "Error loading folders",
            options: []
          };
        }
      }
    })
  },
  async run(context) {
    var _a;
    let { folder_id } = context.propsValue, authValue = context.auth;
    if (!folder_id || !folder_id.trim())
      throw new Error("Folder ID is required");
    let cleanFolderId = folder_id.trim();
    try {
      let response = await fetch(`${canvaCommon.baseUrl}/folders/${cleanFolderId}`, {
        method: "GET",
        headers: {
          Authorization: `Bearer ${authValue.access_token}`
        }
      });
      if (!response.ok) {
        let errorData = await response.json().catch(() => ({}));
        throw response.status === 400 ? new Error(`Bad Request: ${errorData.message || "Invalid folder ID format"}`) : response.status === 401 ? new Error("Unauthorized: Please check your authentication credentials") : response.status === 403 ? new Error("Forbidden: You do not have permission to access this folder") : response.status === 404 ? new Error(`Folder not found: The folder with ID "${cleanFolderId}" does not exist or you do not have access to it`) : response.status === 429 ? new Error("Rate limit exceeded: Too many requests (limit: 100 per minute)") : new Error(`HTTP ${response.status}: ${errorData.message || "Request failed"}`);
      }
      let folder = (await response.json()).folder;
      return {
        folder: {
          id: folder.id,
          name: folder.name,
          created_at: folder.created_at,
          updated_at: folder.updated_at,
          thumbnail: folder.thumbnail || null
        },
        folder_id: folder.id,
        folder_name: folder.name,
        created_date: new Date(folder.created_at * 1e3).toISOString(),
        updated_date: new Date(folder.updated_at * 1e3).toISOString(),
        has_thumbnail: !!folder.thumbnail,
        thumbnail_url: ((_a = folder.thumbnail) == null ? void 0 : _a.url) || null
      };
    } catch (error) {
      throw error instanceof Error ? error : new Error(`Failed to get folder: ${String(error)}`);
    }
  }
});

// ../ap/packages/pieces/community/canva/src/lib/actions/get-asset.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var getAsset = (0, import_pieces_framework9.createAction)({
  auth: canvaAuth,
  name: "get_asset",
  classification: "READ",
  displayName: "Get Asset",
  description: "Retrieve metadata for a specific asset (image or video), including name, tags, creation date, and thumbnail.",
  audience: "both",
  aiMetadata: {
    description: "Fetches metadata for a single Canva library asset (image or video) by asset ID, including name, tags, timestamps, thumbnail, and import status. Pick this when you already have an asset ID and need its details or to confirm an upload finished importing; it is about library assets, not designs (use Get Design for those). Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    asset_id: import_pieces_framework9.Property.Dropdown({
      displayName: "Asset",
      description: "Select the asset to retrieve metadata for",
      required: !0,
      auth: canvaAuth,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Please authenticate with Canva first",
            options: []
          };
        try {
          return {
            disabled: !1,
            options: await fetchUserAssets(auth)
          };
        } catch (error) {
          return console.error("Error fetching assets:", error), {
            disabled: !0,
            placeholder: "Error loading assets",
            options: []
          };
        }
      }
    })
  },
  async run(context) {
    var _a, _b, _c, _d;
    let { asset_id } = context.propsValue, authValue = context.auth;
    if (!asset_id || !asset_id.trim())
      throw new Error("Asset ID is required");
    let cleanAssetId = asset_id.trim();
    try {
      let response = await fetch(`${canvaCommon.baseUrl}/assets/${cleanAssetId}`, {
        method: "GET",
        headers: {
          Authorization: `Bearer ${authValue.access_token}`
        }
      });
      if (!response.ok) {
        let errorData = await response.json().catch(() => ({}));
        throw response.status === 400 ? new Error(`Bad Request: ${errorData.message || "Invalid asset ID format"}`) : response.status === 401 ? new Error("Unauthorized: Please check your authentication credentials") : response.status === 403 ? new Error("Forbidden: You do not have permission to access this asset") : response.status === 404 ? new Error(`Asset not found: The asset with ID "${cleanAssetId}" does not exist or you do not have access to it`) : response.status === 429 ? new Error("Rate limit exceeded: Too many requests (limit: 100 per minute)") : new Error(`HTTP ${response.status}: ${errorData.message || "Request failed"}`);
      }
      let asset = (await response.json()).asset;
      return {
        asset: {
          type: asset.type,
          id: asset.id,
          name: asset.name,
          tags: asset.tags,
          created_at: asset.created_at,
          updated_at: asset.updated_at,
          thumbnail: asset.thumbnail || null,
          import_status: asset.import_status || null
        },
        asset_id: asset.id,
        asset_name: asset.name,
        asset_type: asset.type,
        tags_count: asset.tags.length,
        tags_list: asset.tags.join(", "),
        created_date: new Date(asset.created_at * 1e3).toISOString(),
        updated_date: new Date(asset.updated_at * 1e3).toISOString(),
        has_thumbnail: !!asset.thumbnail,
        thumbnail_url: ((_a = asset.thumbnail) == null ? void 0 : _a.url) || null,
        thumbnail_dimensions: asset.thumbnail ? `${asset.thumbnail.width}x${asset.thumbnail.height}` : null,
        is_imported_successfully: !asset.import_status || asset.import_status.state === "success",
        import_status_state: ((_b = asset.import_status) == null ? void 0 : _b.state) || "success",
        import_error_message: ((_d = (_c = asset.import_status) == null ? void 0 : _c.error) == null ? void 0 : _d.message) || null
      };
    } catch (error) {
      throw error instanceof Error ? error : new Error(`Failed to get asset: ${String(error)}`);
    }
  }
});

// ../ap/packages/pieces/community/canva/src/index.ts
var canvaAuthDescription = "\nTo connect Canva, follow these steps:\n1. Go to https://www.canva.com/developers/integrations and sign in.\n2. Click **Create an integration** and fill in the app name and description.\n3. Under **Configure** \u2192 **Authentication**, set the redirect URL to the one shown above.\n4. Under **Configure** \u2192 **Scopes**, enable: `design:content:read`, `design:content:write`, `design:meta:read`, `asset:read`, `asset:write`, `folder:read`, `folder:write`.\n5. Copy the **Client ID** and **Client secret** and paste them above.\n", canvaAuth = import_pieces_framework10.PieceAuth.OAuth2({
  description: canvaAuthDescription,
  authUrl: "https://www.canva.com/api/oauth/authorize",
  tokenUrl: "https://api.canva.com/rest/v1/oauth/token",
  required: !0,
  pkce: !0,
  pkceMethod: "S256",
  scope: [
    "design:content:read",
    "design:content:write",
    "design:meta:read",
    "asset:read",
    "asset:write",
    "folder:read",
    "folder:write"
  ],
  validate: async ({ auth }) => {
    var _a;
    try {
      let authValue = auth;
      return authValue.access_token ? (await import_pieces_common6.httpClient.sendRequest({
        method: import_pieces_common6.HttpMethod.GET,
        url: `${canvaCommon.baseUrl}/users/me`,
        headers: {
          Authorization: `Bearer ${authValue.access_token}`
        }
      })).status === 200 ? { valid: !0 } : { valid: !1, error: "Invalid or expired access token" } : { valid: !1, error: "No access token found" };
    } catch (error) {
      return ((_a = error.response) == null ? void 0 : _a.status) === 401 ? { valid: !1, error: "Access token is invalid or expired" } : { valid: !1, error: "Failed to validate authentication credentials" };
    }
  }
}), canva = (0, import_pieces_framework10.createPiece)({
  displayName: "Canva",
  description: "Create stunning designs with Canva's powerful design automation tools",
  auth: canvaAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/canva.png",
  categories: [import_pieces_framework11.PieceCategory.CONTENT_AND_FILES],
  authors: ["onyedikachi-david"],
  actions: [
    createDesign,
    getDesign,
    uploadAsset,
    importDesign,
    exportDesign,
    moveFolderItem,
    findDesign,
    getFolder,
    getAsset,
    (0, import_pieces_common6.createCustomApiCallAction)({
      baseUrl: () => "https://api.canva.com/rest/v1",
      auth: canvaAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.access_token}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  canva,
  canvaAuth
});
