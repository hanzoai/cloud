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

// ../ap/packages/pieces/community/apitemplate-io/src/index.ts
var index_exports = {};
__export(index_exports, {
  apitemplateIo: () => apitemplateIo
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework10 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/apitemplate-io/src/lib/common/auth.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/apitemplate-io/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), APITEMPLATE_REGIONS = {
  default: "https://rest.apitemplate.io",
  europe: "https://rest-de.apitemplate.io",
  us: "https://rest-us.apitemplate.io",
  australia: "https://rest-au.apitemplate.io",
  "alt-default": "https://rest-alt.apitemplate.io",
  "alt-europe": "https://rest-alt-de.apitemplate.io",
  "alt-us": "https://rest-alt-us.apitemplate.io"
}, BASE_URL = APITEMPLATE_REGIONS.default;
function getRegionalBaseUrl(region) {
  return APITEMPLATE_REGIONS[region || "default"];
}
function getAlternativeRegions(currentRegion) {
  let alternatives = [];
  switch (currentRegion) {
    case "default":
      alternatives.push("alt-default", "us", "australia", "europe");
      break;
    case "europe":
      alternatives.push("alt-europe", "default", "us", "australia");
      break;
    case "us":
      alternatives.push("alt-us", "default", "australia", "europe");
      break;
    case "australia":
      alternatives.push("default", "us", "europe");
      break;
    case "alt-default":
      alternatives.push("default", "us", "australia");
      break;
    case "alt-europe":
      alternatives.push("europe", "default", "us");
      break;
    case "alt-us":
      alternatives.push("us", "default", "australia");
      break;
    default:
      alternatives.push("default", "us", "australia", "europe");
  }
  return alternatives;
}
async function makeRequest(apiKey, method, path, body, headers, region) {
  var _a;
  let baseUrl = getRegionalBaseUrl(region);
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${baseUrl}/v2${path}`,
      headers: {
        "X-API-KEY": apiKey,
        "Content-Type": "application/json"
      },
      body
    })).body;
  } catch (error) {
    if ((_a = error.response) != null && _a.status) {
      let statusCode = error.response.status, regionInfo = region ? ` (${region} region)` : "";
      switch (statusCode) {
        case 401:
          throw new Error(
            "APITemplate.io Error: Invalid API key or unauthorized access"
          );
        case 403:
          throw new Error(
            "APITemplate.io Error: Access forbidden - check your API permissions"
          );
        case 404:
          throw new Error(
            "APITemplate.io Error: Resource not found - check your template ID"
          );
        case 429:
          throw new Error(
            "APITemplate.io Error: Rate limit exceeded - please try again later"
          );
        case 500:
          throw new Error(
            `APITemplate.io Error: Internal server error - please try again later${regionInfo}`
          );
        case 502:
        case 503:
        case 504: {
          let alternatives = getAlternativeRegions(region), suggestionText = alternatives.length > 0 ? `

Suggested alternatives: Try switching to one of these regions: ${alternatives.slice(0, 3).join(", ")}` : "";
          throw new Error(
            `APITemplate.io Error: Service unavailable${regionInfo}. The ${region || "default"} region server is temporarily down.${suggestionText}`
          );
        }
        default:
          throw new Error(
            `APITemplate.io Error: HTTP ${statusCode}${regionInfo} - ${error.message || "Unknown error"}`
          );
      }
    }
    if (error.code === "ENOTFOUND" || error.code === "ECONNREFUSED") {
      let alternatives = getAlternativeRegions(region), suggestionText = alternatives.length > 0 ? `

Suggested alternatives: Try switching to one of these regions: ${alternatives.slice(0, 3).join(", ")}` : "";
      throw new Error(
        `APITemplate.io Error: Network connectivity issue with ${region || "default"} region.${suggestionText}`
      );
    }
  }
}

// ../ap/packages/pieces/community/apitemplate-io/src/lib/common/props.ts
var import_pieces_framework = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common");
var regionDropdown = import_pieces_framework.Property.StaticDropdown({
  displayName: "Region",
  description: "Select your preferred API region for better performance",
  required: !0,
  defaultValue: "default",
  options: {
    options: [
      {
        label: "Default (Singapore)",
        value: "default"
      },
      {
        label: "Europe (Frankfurt)",
        value: "europe"
      },
      {
        label: "US East (N. Virginia)",
        value: "us"
      },
      {
        label: "Australia (Sydney)",
        value: "australia"
      },
      {
        label: "Alternative - Default (Singapore)",
        value: "alt-default"
      },
      {
        label: "Alternative - Europe (Frankfurt)",
        value: "alt-europe"
      },
      {
        label: "Alternative - US East (N. Virginia)",
        value: "alt-us"
      }
    ]
  }
}), templateIdDropdown = import_pieces_framework.Property.Dropdown({
  auth: ApitemplateAuth,
  displayName: "Template ID",
  required: !0,
  refreshers: ["auth"],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first"
      };
    let authConfig = auth.props;
    if (!authConfig.apiKey || !authConfig.region)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please complete authentication setup."
      };
    try {
      let response = await makeRequest(
        authConfig.apiKey,
        import_pieces_common2.HttpMethod.GET,
        "/list-templates",
        void 0,
        void 0,
        authConfig.region
      ), templates = (response == null ? void 0 : response.templates) || [];
      return !Array.isArray(templates) || templates.length === 0 ? {
        disabled: !1,
        options: [],
        placeholder: "No templates found"
      } : {
        disabled: !1,
        options: templates.map((template) => ({
          label: `${template.name} (${template.format}) - ${template.status}`,
          value: template.template_id
        }))
      };
    } catch (error) {
      return console.error("Error loading templates:", error), {
        disabled: !0,
        options: [],
        placeholder: "Error loading templates"
      };
    }
  }
}), transactionRefDropdown = import_pieces_framework.Property.Dropdown({
  auth: ApitemplateAuth,
  displayName: "Transaction Reference",
  description: "Select a transaction reference to filter objects.",
  required: !1,
  refreshers: ["auth"],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first"
      };
    let authConfig = auth.props;
    if (!authConfig.apiKey || !authConfig.region)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please complete authentication setup"
      };
    try {
      let response = await makeRequest(
        authConfig.apiKey,
        import_pieces_common2.HttpMethod.GET,
        "/list-objects",
        void 0,
        void 0,
        authConfig.region
      ), objects = (response == null ? void 0 : response.objects) || [];
      return !Array.isArray(objects) || objects.length === 0 ? {
        disabled: !1,
        options: [],
        placeholder: "No objects found"
      } : {
        disabled: !1,
        options: objects.map((obj) => ({
          label: obj.transaction_ref || "Unknown Transaction Ref",
          value: obj.transaction_ref || ""
        }))
      };
    } catch (error) {
      return console.error("Error loading transaction references:", error), {
        disabled: !0,
        options: [],
        placeholder: "Error loading transaction references"
      };
    }
  }
});

// ../ap/packages/pieces/community/apitemplate-io/src/lib/common/auth.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), ApitemplateAuth = import_pieces_framework2.PieceAuth.CustomAuth({
  description: `
To obtain your API key:
1. Go to https://app.apitemplate.io/.
2. Navigate to API Integration section.
3. Copy your API key.

Select the region closest to your location for better performance.
`,
  props: {
    region: regionDropdown,
    apiKey: import_pieces_framework2.Property.ShortText({
      displayName: "API Key",
      description: "Your APITemplate.io API key",
      required: !0
    })
  },
  required: !0,
  validate: async ({ auth }) => {
    if (!(auth != null && auth.apiKey))
      return {
        valid: !1,
        error: "API Key is required"
      };
    if (!(auth != null && auth.region))
      return {
        valid: !1,
        error: "Region selection is required"
      };
    let authConfig = auth;
    try {
      let response = await makeRequest(
        authConfig.apiKey,
        import_pieces_common3.HttpMethod.GET,
        "/account-information",
        void 0,
        void 0,
        authConfig.region
      );
      return response && response.status === "success" ? {
        valid: !0
      } : {
        valid: !1,
        error: "Invalid API response. Please check your credentials."
      };
    } catch (error) {
      return {
        valid: !1,
        error: `Authentication failed: ${error.message || "Invalid API Key or region configuration"}`
      };
    }
  }
});

// ../ap/packages/pieces/community/apitemplate-io/src/lib/actions/create-image.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common");
var createImage = (0, import_pieces_framework3.createAction)({
  auth: ApitemplateAuth,
  name: "createImage",
  classification: "WRITE",
  displayName: "Create Image",
  description: "Creates an image from a template with provided data.",
  audience: "both",
  aiMetadata: { description: "Renders a new image from a saved APITemplate.io template, merging in the supplied JSON `overrides` array to fill template objects. Requires a template ID (pick from the dropdown). Not idempotent: each call generates and stores a new image with its own transaction reference.", idempotent: !1 },
  props: {
    templateId: templateIdDropdown,
    data: import_pieces_framework3.Property.Json({
      displayName: "Template Data",
      description: 'JSON data with overrides array to populate the template. Format: {"overrides": [{"name": "object_name", "property": "value"}]}.',
      required: !0
    }),
    generationDelay: import_pieces_framework3.Property.Number({
      displayName: "Generation Delay (ms)",
      description: "Delay in milliseconds before PDF generation",
      required: !1
    }),
    meta: import_pieces_framework3.Property.ShortText({
      displayName: "External Reference ID",
      description: "Specify an external reference ID for your own reference",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let authConfig = auth.props, {
      templateId,
      data,
      generationDelay,
      meta
    } = propsValue, queryParams = new URLSearchParams();
    queryParams.append("template_id", templateId), generationDelay && queryParams.append("generation_delay", generationDelay.toString()), meta && queryParams.append("meta", meta);
    let endpoint = `/create-image?${queryParams.toString()}`;
    return await makeRequest(
      authConfig.apiKey,
      import_pieces_common4.HttpMethod.POST,
      endpoint,
      data,
      void 0,
      authConfig.region
    );
  }
});

// ../ap/packages/pieces/community/apitemplate-io/src/lib/actions/create-pdf.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common");
var createPdf = (0, import_pieces_framework4.createAction)({
  auth: ApitemplateAuth,
  name: "createPdf",
  classification: "WRITE",
  displayName: "Create PDF",
  description: "Creates a PDF from a template with provided data.",
  audience: "both",
  aiMetadata: { description: "Renders a new PDF from a saved APITemplate.io template, merging in the supplied JSON `overrides` array to fill template objects. Use when the layout is already designed as a template; for ad-hoc HTML or a webpage use the from-HTML or from-URL actions instead. Requires a template ID. Not idempotent: each call generates and stores a new PDF.", idempotent: !1 },
  props: {
    templateId: templateIdDropdown,
    data: import_pieces_framework4.Property.Json({
      displayName: "Template Data",
      description: 'JSON data with overrides array to populate the template. Format: {"overrides": [{"name": "object_name", "property": "value"}]}.',
      required: !0
    }),
    expiration: import_pieces_framework4.Property.Number({
      displayName: "Expiration (minutes)",
      description: "Expiration of the generated PDF in minutes. Use 0 to store permanently, or 1-10080 minutes (7 days) to specify expiration.",
      required: !1,
      defaultValue: 0
    }),
    generationDelay: import_pieces_framework4.Property.Number({
      displayName: "Generation Delay (ms)",
      description: "Delay in milliseconds before PDF generation",
      required: !1
    }),
    meta: import_pieces_framework4.Property.ShortText({
      displayName: "External Reference ID",
      description: "Specify an external reference ID for your own reference",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let authConfig = auth.props, {
      templateId,
      data,
      expiration,
      generationDelay,
      meta
    } = propsValue, queryParams = new URLSearchParams();
    queryParams.append("template_id", templateId), expiration !== void 0 && expiration !== 0 && queryParams.append("expiration", expiration.toString()), generationDelay && queryParams.append("generation_delay", generationDelay.toString()), meta && queryParams.append("meta", meta);
    let endpoint = `/create-pdf?${queryParams.toString()}`;
    try {
      return await makeRequest(
        authConfig.apiKey,
        import_pieces_common5.HttpMethod.POST,
        endpoint,
        data,
        void 0,
        authConfig.region
      );
    } catch (error) {
      throw error.message.includes("502") && authConfig.region !== "default" ? new Error(
        `${error.message}

The ${authConfig.region} region appears to be experiencing issues. Consider switching to the 'default' region in your authentication settings or try again later.`
      ) : error;
    }
  }
});

// ../ap/packages/pieces/community/apitemplate-io/src/lib/actions/get-account-information.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common"), getAccountInformation = (0, import_pieces_framework5.createAction)({
  auth: ApitemplateAuth,
  name: "getAccountInformation",
  classification: "READ",
  displayName: "Get Account Information",
  description: "Retrieves account information including usage statistics and account details.",
  audience: "both",
  aiMetadata: { description: "Fetches the APITemplate.io account profile, including plan and generation usage/quota stats. Use to check remaining quota or account status before generating documents. Read-only and idempotent; takes no input.", idempotent: !0 },
  props: {},
  async run({ auth }) {
    let authConfig = auth.props, endpoint = "/account-information";
    try {
      return await makeRequest(
        authConfig.apiKey,
        import_pieces_common6.HttpMethod.GET,
        endpoint,
        void 0,
        void 0,
        authConfig.region
      );
    } catch (error) {
      throw error.message.includes("502") && authConfig.region !== "default" ? new Error(
        `${error.message}

The ${authConfig.region} region appears to be experiencing issues. Consider switching to the 'default' region in your authentication settings or try again later.`
      ) : error;
    }
  }
});

// ../ap/packages/pieces/community/apitemplate-io/src/lib/actions/delete-object.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common");
var deleteObject = (0, import_pieces_framework6.createAction)({
  auth: ApitemplateAuth,
  name: "deleteObject",
  classification: "DESTRUCTIVE",
  displayName: "Delete Object",
  description: "Deletes a generated PDF or image by its transaction reference or object ID.",
  audience: "both",
  aiMetadata: { description: "Permanently deletes a previously generated PDF or image, identified by its transaction reference (pick from the dropdown of recent objects). Use to clean up stored output. Idempotent in effect: once the object is gone, repeating the call leaves the same deleted state.", idempotent: !0 },
  props: {
    transactionRef: transactionRefDropdown
  },
  async run({ auth, propsValue }) {
    let authConfig = auth.props, { transactionRef } = propsValue, queryParams = new URLSearchParams();
    queryParams.append("transaction_ref", transactionRef);
    let endpoint = `/delete-object?${queryParams.toString()}`;
    try {
      return await makeRequest(
        authConfig.apiKey,
        import_pieces_common7.HttpMethod.GET,
        endpoint,
        void 0,
        void 0,
        authConfig.region
      );
    } catch (error) {
      throw error.message.includes("502") && authConfig.region !== "default" ? new Error(
        `${error.message}

The ${authConfig.region} region appears to be experiencing issues. Consider switching to the 'default' region in your authentication settings or try again later.`
      ) : error;
    }
  }
});

// ../ap/packages/pieces/community/apitemplate-io/src/lib/actions/list-objects.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common8 = require("@activepieces/pieces-common"), listObjects = (0, import_pieces_framework7.createAction)({
  auth: ApitemplateAuth,
  name: "listObjects",
  classification: "SEARCH",
  displayName: "List Objects",
  description: "Retrieves a list of generated PDFs and images with optional filtering",
  audience: "both",
  aiMetadata: { description: "Lists previously generated PDFs and images for the account, optionally narrowed by template ID, transaction reference, date range, or external reference (meta); with no filters it returns the full recent set. Supports limit/offset pagination (max 300 per call). Read-only and idempotent.", idempotent: !0 },
  props: {
    limit: import_pieces_framework7.Property.Number({
      displayName: "Limit",
      description: "Maximum number of objects to return (default: 300, max: 300)",
      required: !1,
      defaultValue: 300
    }),
    offset: import_pieces_framework7.Property.Number({
      displayName: "Offset",
      description: "Number of objects to skip for pagination (default: 0)",
      required: !1,
      defaultValue: 0
    }),
    templateId: import_pieces_framework7.Property.ShortText({
      displayName: "Template ID",
      description: "Filter objects by template ID (optional)",
      required: !1
    }),
    transactionRef: import_pieces_framework7.Property.ShortText({
      displayName: "Transaction Reference",
      description: "Filter by specific transaction reference (optional)",
      required: !1
    }),
    dateFrom: import_pieces_framework7.Property.ShortText({
      displayName: "Date From",
      description: "Start date for filtering (YYYY-MM-DD format, optional)",
      required: !1
    }),
    dateTo: import_pieces_framework7.Property.ShortText({
      displayName: "Date To",
      description: "End date for filtering (YYYY-MM-DD format, optional)",
      required: !1
    }),
    meta: import_pieces_framework7.Property.ShortText({
      displayName: "Meta Filter",
      description: "Filter by external reference ID (meta field)",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let authConfig = auth.props, {
      limit,
      offset,
      templateId,
      transactionRef,
      dateFrom,
      dateTo,
      meta
    } = propsValue, queryParams = new URLSearchParams();
    limit !== void 0 && limit !== 300 && queryParams.append("limit", Math.min(limit, 300).toString()), offset !== void 0 && offset !== 0 && queryParams.append("offset", offset.toString()), templateId && queryParams.append("template_id", templateId), transactionRef && queryParams.append("transaction_ref", transactionRef), dateFrom && queryParams.append("date_from", dateFrom), dateTo && queryParams.append("date_to", dateTo), meta && queryParams.append("meta", meta);
    let endpoint = `/list-objects${queryParams.toString() ? `?${queryParams.toString()}` : ""}`;
    try {
      return await makeRequest(
        authConfig.apiKey,
        import_pieces_common8.HttpMethod.GET,
        endpoint,
        void 0,
        void 0,
        authConfig.region
      );
    } catch (error) {
      throw error.message.includes("502") && authConfig.region !== "default" ? new Error(
        `${error.message}

The ${authConfig.region} region appears to be experiencing issues. Consider switching to the 'default' region in your authentication settings or try again later.`
      ) : error;
    }
  }
});

// ../ap/packages/pieces/community/apitemplate-io/src/lib/actions/create-pdf-from-html.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var import_pieces_common9 = require("@activepieces/pieces-common"), createPdfFromHtml = (0, import_pieces_framework8.createAction)({
  auth: ApitemplateAuth,
  name: "createPdfFromHtml",
  classification: "WRITE",
  displayName: "Create PDF From HTML",
  description: "Creates a PDF from HTML.",
  audience: "both",
  aiMetadata: { description: "Renders a new PDF directly from raw HTML (with optional CSS, templating data, and page settings such as size, orientation, margins, and headers/footers). Use when you have ad-hoc HTML markup rather than a saved template or a live URL. Requires the HTML body. Not idempotent: each call generates and stores a new PDF.", idempotent: !1 },
  props: {
    html: import_pieces_framework8.Property.LongText({
      displayName: "HTML Content",
      description: "The HTML content to convert to PDF. Can include CSS styles and external resources.",
      required: !0
    }),
    css: import_pieces_framework8.Property.LongText({
      displayName: "CSS Styles",
      description: "Optional CSS styles to apply to the HTML content. Can include inline styles or external stylesheets.",
      required: !1
    }),
    data: import_pieces_framework8.Property.Json({
      displayName: "Data for Templating",
      description: "Optional JSON data to use for templating the HTML content. Can include variables and dynamic content.",
      required: !1
    }),
    expiration: import_pieces_framework8.Property.Number({
      displayName: "Expiration (minutes)",
      description: "Expiration of the generated PDF in minutes. Use 0 to store permanently, or 1-10080 minutes (7 days) to specify expiration.",
      required: !1
    }),
    pageSize: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Page Size",
      description: "PDF page size format",
      required: !1,
      defaultValue: "A4",
      options: {
        options: [
          { label: "A4", value: "A4" },
          { label: "A3", value: "A3" },
          { label: "A5", value: "A5" },
          { label: "Letter", value: "Letter" },
          { label: "Legal", value: "Legal" },
          { label: "Tabloid", value: "Tabloid" }
        ]
      }
    }),
    orientation: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Page Orientation",
      description: "PDF page orientation",
      required: !1,
      defaultValue: "portrait",
      options: {
        options: [
          { label: "Portrait", value: "portrait" },
          { label: "Landscape", value: "landscape" }
        ]
      }
    }),
    marginTop: import_pieces_framework8.Property.Number({
      displayName: "Margin Top (mm)",
      description: "Top margin in millimeters",
      required: !1
    }),
    marginBottom: import_pieces_framework8.Property.Number({
      displayName: "Margin Bottom (mm)",
      description: "Bottom margin in millimeters",
      required: !1
    }),
    marginLeft: import_pieces_framework8.Property.Number({
      displayName: "Margin Left (mm)",
      description: "Left margin in millimeters",
      required: !1
    }),
    marginRight: import_pieces_framework8.Property.Number({
      displayName: "Margin Right (mm)",
      description: "Right margin in millimeters",
      required: !1
    }),
    printBackground: import_pieces_framework8.Property.Checkbox({
      displayName: "Print Background",
      description: "Whether to print background graphics and colors",
      required: !1,
      defaultValue: !0
    }),
    headerFontSize: import_pieces_framework8.Property.ShortText({
      displayName: "Header Font Size",
      description: 'Font size for header (e.g., "9px")',
      required: !1
    }),
    displayHeaderFooter: import_pieces_framework8.Property.Checkbox({
      displayName: "Display Header/Footer",
      description: "Whether to display header and footer",
      required: !1,
      defaultValue: !1
    }),
    customHeader: import_pieces_framework8.Property.LongText({
      displayName: "Custom Header HTML",
      description: "Custom HTML content for header",
      required: !1
    }),
    customFooter: import_pieces_framework8.Property.LongText({
      displayName: "Custom Footer HTML",
      description: "Custom HTML content for footer",
      required: !1
    }),
    scale: import_pieces_framework8.Property.Number({
      displayName: "Scale",
      description: "Scale factor for the PDF (0.1 to 2.0)",
      required: !1
    }),
    waitForTimeout: import_pieces_framework8.Property.Number({
      displayName: "Wait Timeout (ms)",
      description: "Time to wait before generating PDF (in milliseconds)",
      required: !1
    }),
    meta: import_pieces_framework8.Property.ShortText({
      displayName: "External Reference ID",
      description: "Specify an external reference ID for your own reference",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let authConfig = auth.props, {
      html,
      css,
      data,
      expiration,
      pageSize,
      orientation,
      marginTop,
      marginBottom,
      marginLeft,
      marginRight,
      printBackground,
      headerFontSize,
      displayHeaderFooter,
      customHeader,
      customFooter,
      scale,
      waitForTimeout,
      meta
    } = propsValue, queryParams = new URLSearchParams();
    expiration !== void 0 && expiration !== 0 && queryParams.append("expiration", expiration.toString()), meta && queryParams.append("meta", meta);
    let endpoint = `/create-pdf-from-html${queryParams.toString() ? `?${queryParams.toString()}` : ""}`, settings = {};
    pageSize && pageSize !== "A4" && (settings.paper_size = pageSize), orientation && (settings.orientation = orientation === "landscape" ? "1" : "0"), marginTop !== void 0 && (settings.margin_top = marginTop.toString()), marginBottom !== void 0 && (settings.margin_bottom = marginBottom.toString()), marginLeft !== void 0 && (settings.margin_left = marginLeft.toString()), marginRight !== void 0 && (settings.margin_right = marginRight.toString()), printBackground !== void 0 && (settings.print_background = printBackground ? "1" : "0"), headerFontSize && (settings.header_font_size = headerFontSize), displayHeaderFooter !== void 0 && (settings.displayHeaderFooter = displayHeaderFooter), customHeader && (settings.custom_header = customHeader), customFooter && (settings.custom_footer = customFooter), scale !== void 0 && (settings.scale = scale.toString()), waitForTimeout !== void 0 && (settings.wait_for_timeout = waitForTimeout.toString());
    let requestBody = {
      body: html
    };
    css && (requestBody.css = css), data && (requestBody.data = data), Object.keys(settings).length > 0 && (requestBody.settings = settings);
    try {
      return await makeRequest(
        authConfig.apiKey,
        import_pieces_common9.HttpMethod.POST,
        endpoint,
        requestBody,
        void 0,
        authConfig.region
      );
    } catch (error) {
      throw error.message.includes("502") && authConfig.region !== "default" ? new Error(
        `${error.message}

The ${authConfig.region} region appears to be experiencing issues. Consider switching to the 'default' region in your authentication settings or try again later.`
      ) : error;
    }
  }
});

// ../ap/packages/pieces/community/apitemplate-io/src/lib/actions/create-pdf-from-url.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var import_pieces_common10 = require("@activepieces/pieces-common"), createPdfFromUrl = (0, import_pieces_framework9.createAction)({
  auth: ApitemplateAuth,
  name: "createPdfFromUrl",
  classification: "WRITE",
  displayName: "Create PDF From URL",
  description: "Creates a PDF from a webpage URL.",
  audience: "both",
  aiMetadata: { description: "Renders a new PDF by loading and capturing a live webpage at the given URL, with optional page settings (size, orientation, margins), viewport sizing, full-page capture, and wait-for-selector/timeout controls for dynamic content. Use when the source is a public web page rather than supplied HTML or a saved template. Requires the URL. Not idempotent: each call generates and stores a new PDF.", idempotent: !1 },
  props: {
    url: import_pieces_framework9.Property.ShortText({
      displayName: "URL",
      description: "The URL of the webpage to convert to PDF",
      required: !0
    }),
    expiration: import_pieces_framework9.Property.Number({
      displayName: "Expiration (minutes)",
      description: "Expiration of the generated PDF in minutes. Use 0 to store permanently, or 1-10080 minutes (7 days) to specify expiration.",
      required: !1
    }),
    pageSize: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Page Size",
      description: "PDF page size format",
      required: !1,
      defaultValue: "A4",
      options: {
        options: [
          { label: "A4", value: "A4" },
          { label: "A3", value: "A3" },
          { label: "A5", value: "A5" },
          { label: "Letter", value: "Letter" },
          { label: "Legal", value: "Legal" },
          { label: "Tabloid", value: "Tabloid" }
        ]
      }
    }),
    orientation: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Page Orientation",
      description: "PDF page orientation",
      required: !1,
      defaultValue: "portrait",
      options: {
        options: [
          { label: "Portrait", value: "portrait" },
          { label: "Landscape", value: "landscape" }
        ]
      }
    }),
    marginTop: import_pieces_framework9.Property.Number({
      displayName: "Margin Top (mm)",
      description: "Top margin in millimeters",
      required: !1
    }),
    marginBottom: import_pieces_framework9.Property.Number({
      displayName: "Margin Bottom (mm)",
      description: "Bottom margin in millimeters",
      required: !1
    }),
    marginLeft: import_pieces_framework9.Property.Number({
      displayName: "Margin Left (mm)",
      description: "Left margin in millimeters",
      required: !1
    }),
    marginRight: import_pieces_framework9.Property.Number({
      displayName: "Margin Right (mm)",
      description: "Right margin in millimeters",
      required: !1
    }),
    printBackground: import_pieces_framework9.Property.Checkbox({
      displayName: "Print Background",
      description: "Whether to print background graphics and colors",
      required: !1,
      defaultValue: !0
    }),
    headerFontSize: import_pieces_framework9.Property.ShortText({
      displayName: "Header Font Size",
      description: 'Font size for header (e.g., "9px")',
      required: !1
    }),
    displayHeaderFooter: import_pieces_framework9.Property.Checkbox({
      displayName: "Display Header/Footer",
      description: "Whether to display header and footer",
      required: !1,
      defaultValue: !1
    }),
    customHeader: import_pieces_framework9.Property.LongText({
      displayName: "Custom Header HTML",
      description: "Custom HTML content for header",
      required: !1
    }),
    customFooter: import_pieces_framework9.Property.LongText({
      displayName: "Custom Footer HTML",
      description: "Custom HTML content for footer",
      required: !1
    }),
    scale: import_pieces_framework9.Property.Number({
      displayName: "Scale",
      description: "Scale factor for the PDF (0.1 to 2.0)",
      required: !1
    }),
    waitForTimeout: import_pieces_framework9.Property.Number({
      displayName: "Wait Timeout (ms)",
      description: "Time to wait for page to load before generating PDF (in milliseconds)",
      required: !1
    }),
    waitForSelector: import_pieces_framework9.Property.ShortText({
      displayName: "Wait for Selector",
      description: 'CSS selector to wait for before generating PDF (e.g., ".content-loaded")',
      required: !1
    }),
    viewportWidth: import_pieces_framework9.Property.Number({
      displayName: "Viewport Width",
      description: "Browser viewport width in pixels (default: 1920)",
      required: !1,
      defaultValue: 1920
    }),
    viewportHeight: import_pieces_framework9.Property.Number({
      displayName: "Viewport Height",
      description: "Browser viewport height in pixels (default: 1080)",
      required: !1,
      defaultValue: 1080
    }),
    fullPage: import_pieces_framework9.Property.Checkbox({
      displayName: "Full Page",
      description: "Capture the full scrollable page",
      required: !1,
      defaultValue: !0
    }),
    meta: import_pieces_framework9.Property.ShortText({
      displayName: "External Reference ID",
      description: "Specify an external reference ID for your own reference",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let authConfig = auth.props, {
      url,
      expiration,
      pageSize,
      orientation,
      marginTop,
      marginBottom,
      marginLeft,
      marginRight,
      printBackground,
      headerFontSize,
      displayHeaderFooter,
      customHeader,
      customFooter,
      scale,
      waitForTimeout,
      waitForSelector,
      viewportWidth,
      viewportHeight,
      fullPage,
      meta
    } = propsValue, queryParams = new URLSearchParams();
    expiration !== void 0 && expiration !== 0 && queryParams.append("expiration", expiration.toString()), meta && queryParams.append("meta", meta);
    let endpoint = `/create-pdf-from-url${queryParams.toString() ? `?${queryParams.toString()}` : ""}`, settings = {};
    pageSize && pageSize !== "A4" && (settings.paper_size = pageSize), orientation && (settings.orientation = orientation === "landscape" ? "1" : "0"), marginTop !== void 0 && (settings.margin_top = marginTop.toString()), marginBottom !== void 0 && (settings.margin_bottom = marginBottom.toString()), marginLeft !== void 0 && (settings.margin_left = marginLeft.toString()), marginRight !== void 0 && (settings.margin_right = marginRight.toString()), printBackground !== void 0 && (settings.print_background = printBackground ? "1" : "0"), headerFontSize && (settings.header_font_size = headerFontSize), displayHeaderFooter !== void 0 && (settings.displayHeaderFooter = displayHeaderFooter), customHeader && (settings.custom_header = customHeader), customFooter && (settings.custom_footer = customFooter), scale !== void 0 && (settings.scale = scale.toString()), waitForTimeout !== void 0 && (settings.wait_for_timeout = waitForTimeout.toString()), waitForSelector && (settings.wait_for_selector = waitForSelector), viewportWidth !== void 0 && viewportWidth !== 1920 && (settings.viewport_width = viewportWidth.toString()), viewportHeight !== void 0 && viewportHeight !== 1080 && (settings.viewport_height = viewportHeight.toString()), fullPage !== void 0 && (settings.full_page = fullPage ? "1" : "0");
    let requestBody = {
      url
    };
    Object.keys(settings).length > 0 && (requestBody.settings = settings);
    try {
      return await makeRequest(
        authConfig.apiKey,
        import_pieces_common10.HttpMethod.POST,
        endpoint,
        requestBody,
        void 0,
        authConfig.region
      );
    } catch (error) {
      throw error.message.includes("502") && authConfig.region !== "default" ? new Error(
        `${error.message}

The ${authConfig.region} region appears to be experiencing issues. Consider switching to the 'default' region in your authentication settings or try again later.`
      ) : error;
    }
  }
});

// ../ap/packages/pieces/community/apitemplate-io/src/index.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var apitemplateIo = (0, import_pieces_framework10.createPiece)({
  displayName: "APITemplate.io",
  auth: ApitemplateAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/apitemplate-io.png",
  categories: [import_pieces_framework11.PieceCategory.CONTENT_AND_FILES, import_pieces_framework11.PieceCategory.PRODUCTIVITY],
  authors: ["Sanket6652"],
  actions: [
    createImage,
    createPdfFromHtml,
    createPdfFromUrl,
    createPdf,
    deleteObject,
    getAccountInformation,
    listObjects,
    (0, import_pieces_common11.createCustomApiCallAction)({
      auth: ApitemplateAuth,
      baseUrl: (auth) => {
        let authConfig = auth == null ? void 0 : auth.props;
        return authConfig ? getRegionalBaseUrl(authConfig.region) : "";
      },
      authMapping: async (auth) => ({
        "X-API-KEY": auth.props.apiKey
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  apitemplateIo
});
