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

// ../ap/packages/pieces/community/generatebanners/src/index.ts
var index_exports = {};
__export(index_exports, {
  generatebanners: () => generatebanners
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/generatebanners/src/lib/actions/renderTemplate.action.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/generatebanners/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), markdownDescription = `
To obtain your GenerateBanners public and secret API Keys, you can follow the steps below:

1. Go to the [GenerateBanners homepage](https://www.generatebanners.com/).
2. Sign up or log in into your account.
3. Go to your [account page](https://www.generatebanners.com/app/account).
4. The public and secret API keys are now displayed, copy them one by one into the right Activepieces fields.
`, generatebannersAuth = import_pieces_framework.PieceAuth.BasicAuth({
  description: markdownDescription,
  required: !0,
  username: {
    displayName: "Public API Key"
  },
  password: {
    displayName: "Secret API Key"
  }
});

// ../ap/packages/pieces/community/generatebanners/src/lib/actions/renderTemplate.action.ts
var renderTemplate = (0, import_pieces_framework2.createAction)({
  auth: generatebannersAuth,
  name: "render_template",
  description: "Render a GenerateBanners template",
  audience: "both",
  aiMetadata: { description: "Renders a GenerateBanners design template into an image or document and returns a signed URL to the generated file. Choose a template by id, pick the output file type (jpg, png, or pdf), and supply values for the template's variable fields to fill in dynamic text. Use this to produce banners, social posts, or PDFs from a predefined template. Read-only/idempotent: the same template id, file type, and variable values always yield the same rendered output with no extra side effect.", idempotent: !0 },
  displayName: "Render Template",
  props: {
    template_id: import_pieces_framework2.Property.Dropdown({
      displayName: "Template",
      auth: generatebannersAuth,
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        let authentication = auth;
        if (!authentication)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please enter your API key first."
          };
        let response = await import_pieces_common.httpClient.sendRequest({
          method: import_pieces_common.HttpMethod.GET,
          url: `https://api.generatebanners.com/api/v1/${authentication.username}/template`,
          authentication: {
            type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
            token: authentication.password
          }
        });
        return response.status === 200 ? {
          disabled: !1,
          options: response.body.templates.map((template) => ({
            label: template.name,
            value: template.id
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Error fetching templates"
        };
      }
    }),
    filetype: import_pieces_framework2.Property.StaticDropdown({
      displayName: "File type",
      required: !0,
      defaultValue: "jpg",
      options: {
        options: [
          {
            label: "Image (jpg)",
            value: "jpg"
          },
          {
            label: "Image (png)",
            value: "png"
          },
          {
            label: "Document (pdf)",
            value: "pdf"
          }
        ]
      }
    }),
    variables: import_pieces_framework2.Property.DynamicProperties({
      displayName: "Variables",
      auth: generatebannersAuth,
      required: !0,
      refreshers: ["template_id"],
      props: async ({ auth, template_id }) => {
        if (!auth || !template_id)
          return {};
        let request = {
          method: import_pieces_common.HttpMethod.GET,
          url: `https://api.generatebanners.com/api/v1/${auth.username}/template/${template_id}`,
          authentication: {
            type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
            token: auth.password
          }
        }, result = await import_pieces_common.httpClient.sendRequest(request);
        if (result.status === 200) {
          let properties = {};
          for (let variable of result.body.template.variables)
            variable.hidden || (variable.property === "text" ? properties[variable.name] = import_pieces_framework2.Property.LongText({
              displayName: variable.name,
              required: !1
            }) : properties[variable.name] = import_pieces_framework2.Property.ShortText({
              displayName: variable.name,
              required: !1
            }));
          return properties;
        } else
          return {};
      }
    })
  },
  async run({ auth, propsValue }) {
    let query = [];
    if (propsValue.variables) {
      let props = Object.entries(propsValue.variables);
      for (let [propertyKey, propertyValue] of props)
        propertyValue && query.push(
          `${propertyKey}=${encodeURIComponent(propertyValue)}`
        );
    }
    let request = {
      method: import_pieces_common.HttpMethod.GET,
      url: `https://api.generatebanners.com/api/v1/${auth.username}/template/${propsValue.template_id}/sign-url?filetype=${encodeURIComponent(
        propsValue.filetype
      )}&${query.join("&")}`,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: auth.password
      }
    }, result = await import_pieces_common.httpClient.sendRequest(request);
    return result.status === 200 || result.status === 202 ? result.body.url : {};
  }
});

// ../ap/packages/pieces/community/generatebanners/src/index.ts
var generatebanners = (0, import_pieces_framework3.createPiece)({
  displayName: "GenerateBanners",
  description: "Image generation API for banners and social media posts",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/generatebanners.png",
  categories: [import_pieces_framework4.PieceCategory.CONTENT_AND_FILES],
  authors: ["tpatel", "kishanprmr", "khaledmashaly", "abuaboud"],
  auth: generatebannersAuth,
  actions: [renderTemplate],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  generatebanners
});
