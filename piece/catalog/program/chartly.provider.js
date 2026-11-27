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

// ../ap/packages/pieces/community/chartly/src/index.ts
var index_exports = {};
__export(index_exports, {
  chartly: () => chartly
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/chartly/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), chartlyAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: `
    1. Visit chartly.dev to generate your free trial API key
    2. Create an account to manage your API keys and access higher limits
    3. Find your keys in your dashboard anytime
    4. API keys look like: trial_abc123...xyz789`,
  validate: async ({ auth }) => {
    try {
      let response = await fetch("https://api.chartly.dev/v1/status", {
        method: "GET",
        headers: {
          "X-Api-Key": auth
        }
      });
      return response.ok ? (await response.json()).status !== "ok" ? {
        valid: !1,
        error: "Chartly service is currently unhealthy"
      } : {
        valid: !0
      } : {
        valid: !1,
        error: `Invalid API key: ${response.status} ${response.statusText}`
      };
    } catch (error) {
      return {
        valid: !1,
        error: `Connection error: ${error.message}`
      };
    }
  }
});

// ../ap/packages/pieces/community/chartly/src/lib/actions/create-chart.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var createChartAction = (0, import_pieces_framework2.createAction)({
  auth: chartlyAuth,
  name: "create_chart",
  classification: "WRITE",
  displayName: "Create Chart",
  description: "Generates a chart image based on the provided data, configuration, and chart type",
  audience: "both",
  aiMetadata: { description: "Renders a Chart.js configuration into a PNG or SVG image via the Chartly API and returns it as a base64 data URL. Build the chart from the simple props (chart type, labels, dataset values, colors, dimensions) or, for full control, pass a complete Chart.js config JSON in the advanced config prop, which overrides the simple settings. Use when an agent needs to produce a chart visualization from numeric data. Each call creates a new cached chart on Chartly, so it is not idempotent.", idempotent: !1 },
  props: {
    chart_type: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Chart Type",
      description: "Type of chart to generate",
      required: !0,
      options: {
        options: [
          { label: "Bar Chart", value: "bar" },
          { label: "Line Chart", value: "line" },
          { label: "Pie Chart", value: "pie" },
          { label: "Doughnut Chart", value: "doughnut" },
          { label: "Radar Chart", value: "radar" },
          { label: "Polar Area Chart", value: "polarArea" },
          { label: "Scatter Chart", value: "scatter" },
          { label: "Bubble Chart", value: "bubble" }
        ]
      },
      defaultValue: "bar"
    }),
    chart_title: import_pieces_framework2.Property.ShortText({
      displayName: "Chart Title",
      description: "Title for the chart",
      required: !1
    }),
    labels: import_pieces_framework2.Property.Array({
      displayName: "Labels",
      description: 'Labels for the data points (e.g., ["Jan", "Feb", "Mar"])',
      required: !0
    }),
    dataset_label: import_pieces_framework2.Property.ShortText({
      displayName: "Dataset Label",
      description: "Label for the data series",
      required: !0,
      defaultValue: "Data"
    }),
    data_values: import_pieces_framework2.Property.Array({
      displayName: "Data Values",
      description: "Numeric values for the chart (e.g., [10, 20, 30])",
      required: !0
    }),
    background_color: import_pieces_framework2.Property.ShortText({
      displayName: "Background Color",
      description: 'Background color for data points (e.g., "rgba(54, 162, 235, 0.8)" or "#36A2EB")',
      required: !1,
      defaultValue: "rgba(54, 162, 235, 0.8)"
    }),
    width: import_pieces_framework2.Property.Number({
      displayName: "Width",
      description: "Image width in pixels (1-2000)",
      required: !0,
      defaultValue: 600
    }),
    height: import_pieces_framework2.Property.Number({
      displayName: "Height",
      description: "Image height in pixels (1-2000)",
      required: !0,
      defaultValue: 400
    }),
    format: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Format",
      description: "Output format for the chart image",
      required: !0,
      options: {
        options: [
          { label: "PNG", value: "png" },
          { label: "SVG", value: "svg" }
        ]
      },
      defaultValue: "png"
    }),
    background_color_image: import_pieces_framework2.Property.ShortText({
      displayName: "Image Background Color",
      description: 'Background color for the image (e.g., "white", "transparent", "#FFFFFF")',
      required: !1,
      defaultValue: "white"
    }),
    advanced_config: import_pieces_framework2.Property.LongText({
      displayName: "Advanced Chart.js Configuration",
      description: "Complete Chart.js configuration as JSON (overrides other settings)",
      required: !1
    })
  },
  async run(context) {
    let {
      chart_type,
      chart_title,
      labels,
      dataset_label,
      data_values,
      background_color,
      width,
      height,
      format,
      background_color_image,
      advanced_config
    } = context.propsValue, chartConfig;
    if (advanced_config)
      try {
        chartConfig = JSON.parse(advanced_config);
      } catch (error) {
        throw new Error("Invalid JSON in advanced configuration");
      }
    else
      chartConfig = {
        type: chart_type,
        data: {
          labels,
          datasets: [{
            label: dataset_label,
            data: data_values,
            backgroundColor: background_color
          }]
        },
        options: {
          responsive: !1
        }
      }, chart_title && (chartConfig.options.plugins = {
        title: {
          display: !0,
          text: chart_title
        }
      });
    let requestBody = {
      chart: chartConfig,
      width,
      height,
      format
    };
    background_color_image && (requestBody.backgroundColor = background_color_image);
    let response = await fetch("https://api.chartly.dev/v1/chart", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-Api-Key": context.auth.secret_text
      },
      body: JSON.stringify(requestBody)
    });
    if (!response.ok) {
      let errorText = await response.text();
      throw new Error(`Chartly API error: ${response.status} ${response.statusText} - ${errorText}`);
    }
    let imageBuffer = await response.arrayBuffer(), base64Image = Buffer.from(imageBuffer).toString("base64");
    return {
      success: !0,
      chart_url: `data:image/${format};base64,${base64Image}`,
      format,
      width,
      height,
      chart_config: chartConfig
    };
  }
});

// ../ap/packages/pieces/community/chartly/src/lib/actions/get-chart.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var getChartAction = (0, import_pieces_framework3.createAction)({
  auth: chartlyAuth,
  name: "get_chart",
  classification: "READ",
  displayName: "Get Chart",
  description: "Retrieve a previously created chart by its ID. Returns cached image data optimized for sharing.",
  audience: "both",
  aiMetadata: { description: "Fetches a previously created chart from Chartly by its chart ID and returns the cached image as a base64 data URL, auto-detecting PNG vs SVG from the response unless a format is specified. Use when an agent already has a chart ID (e.g. from a prior Create Chart call) and needs to re-fetch the image. Requires a valid chart ID; this is a read-only lookup and is idempotent.", idempotent: !0 },
  props: {
    chart_id: import_pieces_framework3.Property.ShortText({
      displayName: "Chart ID",
      description: "The ID of the chart to retrieve (from create chart response)",
      required: !0
    }),
    format: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Expected Format",
      description: "Expected output format of the chart",
      required: !1,
      options: {
        options: [
          { label: "PNG", value: "png" },
          { label: "SVG", value: "svg" },
          { label: "Auto-detect", value: "auto" }
        ]
      },
      defaultValue: "auto"
    })
  },
  async run(context) {
    let { chart_id, format } = context.propsValue;
    if (!chart_id || chart_id.trim() === "")
      throw new Error("Chart ID is required");
    let response = await fetch(`https://api.chartly.dev/v1/chart/${chart_id.trim()}`, {
      method: "GET",
      headers: {
        "X-Api-Key": context.auth.secret_text
      }
    });
    if (!response.ok) {
      if (response.status === 404)
        throw new Error(`Chart with ID '${chart_id}' not found. Please verify the chart ID is correct.`);
      {
        let errorText = await response.text();
        throw new Error(`Chartly API error: ${response.status} ${response.statusText} - ${errorText}`);
      }
    }
    let contentType = response.headers.get("content-type") || "", detectedFormat = "png";
    contentType.includes("svg") ? detectedFormat = "svg" : contentType.includes("png") && (detectedFormat = "png");
    let imageBuffer = await response.arrayBuffer(), base64Image = Buffer.from(imageBuffer).toString("base64"), dataUrl = `data:image/${detectedFormat};base64,${base64Image}`;
    return {
      success: !0,
      chart_id,
      chart_url: dataUrl,
      format: format === "auto" ? detectedFormat : format,
      content_type: contentType,
      detected_format: detectedFormat
    };
  }
});

// ../ap/packages/pieces/community/chartly/src/index.ts
var chartly = (0, import_pieces_framework4.createPiece)({
  displayName: "Chartly",
  description: "Instant chart images. Zero servers. Transform any Chart.js configuration into cached PNG or SVG images via a simple REST API.",
  auth: chartlyAuth,
  minimumSupportedRelease: "0.36.1",
  categories: [import_pieces_framework5.PieceCategory.DEVELOPER_TOOLS, import_pieces_framework5.PieceCategory.CONTENT_AND_FILES],
  logoUrl: "https://cdn.activepieces.com/pieces/chartly.png",
  authors: ["onyedikachi-david"],
  actions: [createChartAction, getChartAction],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  chartly
});
