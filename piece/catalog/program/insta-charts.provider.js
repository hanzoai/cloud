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

// ../ap/packages/pieces/community/insta-charts/src/index.ts
var index_exports = {};
__export(index_exports, {
  instaCharts: () => instaCharts
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/insta-charts/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), instaChartsAuth = import_pieces_framework.PieceAuth.OAuth2({
  description: "InstaCharts OAuth2 Authentication",
  authUrl: "https://api.instacharts.io/v1/oauth/authorize",
  tokenUrl: "https://api.instacharts.io/v1/oauth/token",
  required: !0,
  scope: ["read", "write"]
});

// ../ap/packages/pieces/community/insta-charts/src/lib/actions/generate-chart-image.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), instaChartsGenerateChartImageAction = (0, import_pieces_framework2.createAction)({
  auth: instaChartsAuth,
  name: "generate_chart_image",
  displayName: "Generate Chart Image",
  description: "Generates a temporary chart image using customizable templates and data parameters",
  audience: "both",
  aiMetadata: { description: "Renders a chart as a PNG image via the InstaCharts API and returns it as a file, given a template plus X/Y (and optional breakdown or series) data arrays. Use to turn tabular numbers into a downloadable chart for embedding in messages, reports, or documents; pick the chart shape via the chartType option (bar, stackedBar, line, scatter, area, pie, radar, and grouped variants). Requires a valid templateId from the account; each call produces a fresh temporary image, so it is not idempotent.", idempotent: !1 },
  props: {
    templateId: import_pieces_framework2.Property.Dropdown({
      displayName: "Template",
      description: "Select a chart template to use",
      required: !0,
      auth: instaChartsAuth,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please authenticate first"
          };
        try {
          let response = await fetch("https://api.instacharts.io/v1/templates", {
            headers: {
              Authorization: `Bearer ${auth.access_token}`,
              "Content-Type": "application/json"
            }
          });
          if (!response.ok)
            throw new Error(`Failed to fetch templates: ${response.statusText}`);
          return {
            options: (await response.json()).data.map((template) => ({
              label: `${template.name} (${template.type})`,
              value: template.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load templates"
          };
        }
      }
    }),
    chartType: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Chart Type",
      description: "Type of chart to generate",
      required: !1,
      defaultValue: "stackedBar",
      options: {
        options: [
          { label: "Bar Chart", value: "bar" },
          { label: "Stacked Bar Chart", value: "stackedBar" },
          { label: "Grouped Bar Chart", value: "groupedBar" },
          { label: "Line Chart", value: "line" },
          { label: "Grouped Line Chart", value: "groupedLine" },
          { label: "Scatter Chart", value: "scatter" },
          { label: "Grouped Scatter Chart", value: "groupedScatter" },
          { label: "Area Chart", value: "area" },
          { label: "Grouped Area Chart", value: "groupedArea" },
          { label: "Pie Chart", value: "pie" },
          { label: "Radar Chart", value: "radar" }
        ]
      }
    }),
    title: import_pieces_framework2.Property.ShortText({
      displayName: "Title",
      description: "Main chart title",
      required: !1
    }),
    subtitle: import_pieces_framework2.Property.ShortText({
      displayName: "Subtitle",
      description: "Secondary chart title",
      required: !1
    }),
    xTitle: import_pieces_framework2.Property.ShortText({
      displayName: "X-Axis Title",
      description: "Title for the X-axis",
      required: !1
    }),
    yTitle: import_pieces_framework2.Property.ShortText({
      displayName: "Y-Axis Title",
      description: "Title for the Y-axis",
      required: !1
    }),
    zTitle: import_pieces_framework2.Property.ShortText({
      displayName: "Legend Title",
      description: "Title for the legend",
      required: !1
    }),
    x: import_pieces_framework2.Property.Array({
      displayName: "X-Axis Data",
      description: 'X-axis data values (e.g., ["Mobile", "Desktop", "Tablet"])',
      required: !0
    }),
    y: import_pieces_framework2.Property.Array({
      displayName: "Y-Axis Data",
      description: "Y-axis data values (e.g., [100, 200, 400])",
      required: !0
    }),
    z: import_pieces_framework2.Property.Array({
      displayName: "Breakdown Data",
      description: 'Breakdown axis data for unaggregated charts (e.g., ["Site A", "Site A", "Site B"])',
      required: !1
    }),
    series: import_pieces_framework2.Property.Array({
      displayName: "Series Data",
      description: 'Series data as objects with label and data (e.g., [{"label":"Site A","data":[1,2,3]}])',
      required: !1
    }),
    dLabel: import_pieces_framework2.Property.Checkbox({
      displayName: "Show Data Labels",
      description: "Show data labels near the chart pieces",
      required: !1,
      defaultValue: !1
    }),
    overrides: import_pieces_framework2.Property.ShortText({
      displayName: "Override Parameters",
      description: "Additional query parameters (e.g., color=cccccc&width=500)",
      required: !1
    })
  },
  async run(context) {
    let { auth } = context, accessToken = auth.access_token, requestBody = {
      templateId: context.propsValue.templateId
    };
    context.propsValue.chartType && (requestBody.chartType = context.propsValue.chartType), context.propsValue.title && (requestBody.title = context.propsValue.title), context.propsValue.subtitle && (requestBody.subtitle = context.propsValue.subtitle), context.propsValue.xTitle && (requestBody.xTitle = context.propsValue.xTitle), context.propsValue.yTitle && (requestBody.yTitle = context.propsValue.yTitle), context.propsValue.zTitle && (requestBody.zTitle = context.propsValue.zTitle), context.propsValue.x && (requestBody.x = context.propsValue.x), context.propsValue.y && (requestBody.y = context.propsValue.y), context.propsValue.z && (requestBody.z = context.propsValue.z), context.propsValue.series && (requestBody.series = context.propsValue.series), context.propsValue.dLabel !== void 0 && (requestBody.dLabel = context.propsValue.dLabel), context.propsValue.overrides && (requestBody.overrides = context.propsValue.overrides);
    let response = await fetch("https://api.instacharts.io/v1/generate/image", {
      method: "POST",
      headers: {
        Authorization: `Bearer ${accessToken}`,
        "Content-Type": "application/json"
      },
      body: JSON.stringify(requestBody)
    });
    if (!response.ok) {
      let errorData = await response.json().catch(() => ({}));
      throw new Error(`Failed to generate chart: ${response.status} ${response.statusText}. ${errorData.message || ""}`);
    }
    let imageBuffer = await response.arrayBuffer();
    return context.files.write({
      data: Buffer.from(imageBuffer),
      fileName: "chart.png"
    });
  }
});

// ../ap/packages/pieces/community/insta-charts/src/index.ts
var instaCharts = (0, import_pieces_framework3.createPiece)({
  displayName: "InstaCharts",
  description: "Chart creation and visualization platform",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/insta-charts.png",
  categories: [import_pieces_framework4.PieceCategory.MARKETING, import_pieces_framework4.PieceCategory.PRODUCTIVITY],
  authors: ["onyedikachi-david"],
  auth: instaChartsAuth,
  actions: [
    instaChartsGenerateChartImageAction
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  instaCharts
});
