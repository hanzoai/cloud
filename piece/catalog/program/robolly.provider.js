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

// ../ap/packages/pieces/community/robolly/src/index.ts
var index_exports = {};
__export(index_exports, {
  robolly: () => robolly,
  robollyAuth: () => robollyAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/robolly/src/lib/actions/generate-image.action.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");
var generateImage = (0, import_pieces_framework.createAction)({
  description: "Generate an image using Robolly",
  audience: "both",
  aiMetadata: { description: "Renders a personalized asset (JPG, PNG, or PDF, set via the format input) from a Robolly template by filling its template fields with the supplied values, plus any extra modifications. Use when an agent needs to produce a finished image/PDF from a design template; requires a template ID and the values for that template's accepted fields. Each call produces a new render and consumes a generation, so it is not idempotent.", idempotent: !1 },
  displayName: "Generate Image",
  name: "generate_image",
  classification: "READ",
  auth: robollyAuth,
  props: {
    template_id: import_pieces_framework.Property.Dropdown({
      auth: robollyAuth,
      displayName: "Template",
      required: !0,
      description: 'Select your template. (If you want to use Template ID. Click on the "(x)" above this field. Template ID can be found by opening a template and going to \u201CRender\u201D. Being there copy the template ID from the top right.)',
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Enter your API key first",
            options: []
          };
        try {
          return {
            disabled: !1,
            options: (await import_pieces_common.httpClient.sendRequest({
              method: import_pieces_common.HttpMethod.GET,
              url: "https://api.robolly.com/v1/templates",
              headers: {
                Authorization: `Bearer ${auth.secret_text}`
              }
            })).body.templates.map((template) => ({
              label: template.name,
              value: template.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Couldn't load templates, API key is invalid"
          };
        }
      }
    }),
    format: import_pieces_framework.Property.StaticDropdown({
      displayName: "Format",
      required: !0,
      description: "The format of the image to generate.",
      defaultValue: "jpg",
      options: {
        options: [
          {
            label: "JPG",
            value: "jpg"
          },
          {
            label: "PNG",
            value: "png"
          },
          {
            label: "PDF",
            value: "pdf"
          }
        ]
      }
    }),
    fields: import_pieces_framework.Property.DynamicProperties({
      auth: robollyAuth,
      displayName: "Values",
      description: "The values to apply to the fields in the template.",
      required: !0,
      refreshers: ["template_id"],
      props: async ({ auth, template_id }) => {
        if (!auth) return {};
        if (!template_id) return {};
        let fields = {};
        return (await import_pieces_common.httpClient.sendRequest({
          method: import_pieces_common.HttpMethod.GET,
          url: `https://api.robolly.com/v1/templates/${template_id}/accepted-modifications`,
          headers: {
            Authorization: `Bearer ${auth.secret_text}`
          }
        })).body.acceptedModifications.map((field) => {
          fields[field.key] = import_pieces_framework.Property.ShortText({
            displayName: field.key,
            description: `Type: ${field.type}`,
            required: !1
          });
        }), fields;
      }
    }),
    modifications: import_pieces_framework.Property.Object({
      displayName: "Extra Modifications",
      description: 'The extra modifications to apply to the image. See "Detailed dynamic modifications" in https://robolly.com/docs/api-reference/',
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let fields = propsValue.fields, queryParams = {};
    queryParams.json = "";
    for (let key in propsValue.modifications) {
      let value = propsValue.modifications[key];
      queryParams[key] = value;
    }
    return Object.keys(fields).forEach((k) => {
      fields[k] !== "" && (queryParams[k] = fields[k]);
    }), (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      queryParams,
      url: `https://api.robolly.com/templates/${propsValue.template_id}/render/${propsValue.format}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: propsValue.modifications
    })).body;
  }
});

// ../ap/packages/pieces/community/robolly/src/index.ts
var markdownDescription = `
Follow these instructions to get your API Key:
1. Visit the following website: https://robolly.com/dashboard/account/
2. Once on the website, locate and copy your API Key.
Please, take into consideration: We don't test your API Key validity in order to save you some generations, so make sure this is the correct one.
`, robollyAuth = import_pieces_framework2.PieceAuth.SecretText({
  description: markdownDescription,
  displayName: "API Key",
  required: !0,
  validate: async () => ({
    valid: !0
  })
}), robolly = (0, import_pieces_framework2.createPiece)({
  displayName: "Robolly",
  description: "Robolly is the all\u2011in\u2011one service for personalized image, video & PDF generation with API",
  auth: robollyAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/robolly.png",
  categories: [import_pieces_framework3.PieceCategory.MARKETING],
  authors: ["pfernandez98", "kishanprmr", "MoShizzle", "abuaboud"],
  actions: [
    generateImage,
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: () => "https://api.robolly.com",
      auth: robollyAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  robolly,
  robollyAuth
});
