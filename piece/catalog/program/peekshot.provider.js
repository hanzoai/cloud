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

// ../ap/packages/pieces/community/peekshot/src/index.ts
var index_exports = {};
__export(index_exports, {
  peekshot: () => peekshot
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/peekshot/src/lib/actions/capture-screenshot.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/peekshot/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), peekshotAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "You can obtain your API key by navigating to [API Keys](https://dashboard.peekshot.com/dashboard/api-keys) menu.",
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: "https://api.peekshot.com/api/v1/projects",
        headers: {
          "x-api-key": auth,
          "Content-Type": "application/json"
        }
      }), { valid: !0 };
    } catch (e) {
      return { valid: !1, error: "Invalid API Key." };
    }
  }
});

// ../ap/packages/pieces/community/peekshot/src/lib/common/props.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var projectId = import_pieces_framework2.Property.Dropdown({
  displayName: "Project",
  required: !0,
  refreshers: [],
  auth: peekshotAuth,
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please connect your account first.",
        options: []
      };
    try {
      let responseData = (await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.GET,
        url: "https://api.peekshot.com/api/v1/projects",
        headers: {
          "x-api-key": auth.secret_text,
          "Content-Type": "application/json"
        }
      })).body;
      return (responseData == null ? void 0 : responseData.status) === "success" && ((_a = responseData == null ? void 0 : responseData.data) != null && _a.projects) ? {
        options: responseData.data.projects.map((project) => ({
          label: project.name,
          value: project.id
        }))
      } : {
        options: []
      };
    } catch (error) {
      return {
        options: []
      };
    }
  }
});

// ../ap/packages/pieces/community/peekshot/src/lib/actions/capture-screenshot.ts
var captureScreenshot = (0, import_pieces_framework3.createAction)({
  auth: peekshotAuth,
  name: "captureScreenshot",
  classification: "WRITE",
  displayName: "Capture Screenshot",
  description: "Captures Screenshot of a URL.",
  audience: "both",
  aiMetadata: {
    description: "Captures a screenshot of a given web page URL via PeekShot and waits (polling up to ~5 minutes) for the rendered image to be ready. Use when an agent needs to render a live web page to an image; supports optional viewport width/height, PNG/JPEG output, full-page capture, and injecting custom CSS/JS before the shot. Requires a PeekShot project id and a target URL. Not idempotent: each call submits a new capture request.",
    idempotent: !1
  },
  props: {
    projectId,
    url: import_pieces_framework3.Property.ShortText({ displayName: "Target URL", required: !0 }),
    width: import_pieces_framework3.Property.ShortText({
      displayName: "Custom Screenshot Width",
      required: !1
    }),
    height: import_pieces_framework3.Property.ShortText({
      displayName: "Custom Screenshot Height",
      required: !1
    }),
    file_type: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Image Format",
      description: "Output format for the screenshot.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "PNG", value: "png" },
          { label: "JPEG", value: "jpeg" }
        ]
      }
    }),
    inject_css: import_pieces_framework3.Property.LongText({
      displayName: "Custom CSS",
      description: "Custom CSS to apply.",
      required: !1
    }),
    inject_js: import_pieces_framework3.Property.LongText({
      displayName: "Custom JavaScript",
      description: "Custom JavaScript to apply.",
      required: !1
    }),
    full_page: import_pieces_framework3.Property.Checkbox({
      displayName: "Full Page?",
      description: "To capture the entire page.",
      required: !1
    })
  },
  async run({ propsValue, auth }) {
    var _a, _b;
    let {
      projectId: projectId2,
      url,
      width,
      height,
      file_type,
      full_page,
      inject_css,
      inject_js
    } = propsValue, res = await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: "https://api.peekshot.com/api/v1/screenshots",
      headers: {
        "x-api-key": auth.secret_text,
        "Content-Type": "application/json"
      },
      body: {
        project_id: projectId2.toString(),
        url,
        width,
        height,
        inject_css,
        inject_js,
        file_type,
        full_page: full_page ? "true" : "false"
      }
    });
    if (!res.body || !res.body.data || !res.body.data.requestId)
      throw new Error("Failed to initiate screenshot request");
    let requestId = res.body.data.requestId, status = res.body.status, timeoutAt = Date.now() + 300 * 1e3;
    for (; status !== "COMPLETE" && Date.now() < timeoutAt; ) {
      await new Promise((resolve) => setTimeout(resolve, 5e3));
      let pollRes = await import_pieces_common3.httpClient.sendRequest({
        method: import_pieces_common3.HttpMethod.GET,
        url: `https://api.peekshot.com/api/v1/screenshots/${requestId}`,
        headers: {
          "x-api-key": auth.secret_text,
          "Content-Type": "application/json"
        }
      });
      if (status = (_b = (_a = pollRes.body) == null ? void 0 : _a.data) == null ? void 0 : _b.status, status === "COMPLETE")
        return pollRes.body;
    }
    throw new Error("Screenshot generation timed out or failed.");
  }
});

// ../ap/packages/pieces/community/peekshot/src/index.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var peekshot = (0, import_pieces_framework4.createPiece)({
  displayName: "PeekShot",
  auth: peekshotAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/peekshot.png",
  categories: [import_pieces_framework5.PieceCategory.PRODUCTIVITY],
  authors: ["balwant1707"],
  actions: [
    captureScreenshot,
    (0, import_pieces_common4.createCustomApiCallAction)({
      auth: peekshotAuth,
      baseUrl: () => "https://api.peekshot.com/api/v1",
      authMapping: async (auth) => ({
        "x-api-key": auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  peekshot
});
