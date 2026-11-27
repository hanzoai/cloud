"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
};
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

// ../ap/packages/pieces/community/bannerbear/src/index.ts
var index_exports = {};
__export(index_exports, {
  bannerbear: () => bannerbear
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/bannerbear/src/lib/actions/create-image.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/bannerbear/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), bannerbearAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Bannerbear API Key",
  required: !0
});

// ../ap/packages/pieces/community/bannerbear/src/lib/actions/create-image.ts
var bannerbearCreateImageAction = (0, import_pieces_framework2.createAction)({
  auth: bannerbearAuth,
  name: "bannerbear_create_image",
  // Must be a unique across the piece, this shouldn't be changed.
  classification: "WRITE",
  displayName: "Create Image",
  description: "Create image from Bannerbear template",
  audience: "both",
  aiMetadata: {
    description: "Renders a new image (or PDF) from a Bannerbear template by applying layer modifications (text, image URLs, ratings, charts, bar codes, colors). Choose this to programmatically generate branded graphics; requires a valid template UID and modifications matching that template's available layers. Not idempotent \u2014 each call submits a new render and produces a new image.",
    idempotent: !1
  },
  props: {
    template: import_pieces_framework2.Property.Dropdown({
      auth: bannerbearAuth,
      displayName: "Template",
      description: "The template to use in image creation.",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please enter your API key first."
          };
        let response = await import_pieces_common.httpClient.sendRequest({
          method: import_pieces_common.HttpMethod.GET,
          url: "https://api.bannerbear.com/v2/templates",
          authentication: {
            type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
            token: auth.secret_text
          }
        });
        return response.status === 200 ? {
          disabled: !1,
          options: response.body.map((template) => ({
            label: template.name,
            value: template
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Error processing templates"
        };
      }
    }),
    template_version: import_pieces_framework2.Property.Number({
      displayName: "Template version",
      description: "Create image based on a specific version of the template.",
      required: !1
    }),
    modifications: import_pieces_framework2.Property.DynamicProperties({
      auth: bannerbearAuth,
      displayName: "Template modifications",
      description: "A list of modifications you want to make on the template.",
      required: !0,
      refreshers: ["template"],
      props: async ({ auth, template }) => {
        if (!auth) return {};
        if (!template) return {};
        let fields = {};
        return template.available_modifications.map(
          (modification) => {
            "text" in modification ? fields = __spreadValues(__spreadValues(__spreadValues({}, BannerbearModificationsMapping.main), BannerbearModificationsMapping.text), BannerbearModificationsMapping.common) : "rating" in modification ? fields = __spreadValues(__spreadValues(__spreadValues({}, BannerbearModificationsMapping.main), BannerbearModificationsMapping.star_rating), BannerbearModificationsMapping.common) : "image_url" in modification ? fields = __spreadValues(__spreadValues(__spreadValues({}, BannerbearModificationsMapping.main), BannerbearModificationsMapping.image), BannerbearModificationsMapping.common) : "chart_data" in modification ? fields = __spreadValues(__spreadValues(__spreadValues({}, BannerbearModificationsMapping.main), BannerbearModificationsMapping.barline_chart), BannerbearModificationsMapping.common) : "bar_code_data" in modification && (fields = __spreadValues(__spreadValues(__spreadValues({}, BannerbearModificationsMapping.main), BannerbearModificationsMapping.bar_code), BannerbearModificationsMapping.common));
          }
        ), fields;
      }
    }),
    transparent: import_pieces_framework2.Property.Checkbox({
      displayName: "Transparent background",
      description: "Render a PNG with a transparent background. Default is false.",
      required: !1
    }),
    render_pdf: import_pieces_framework2.Property.Checkbox({
      displayName: "Render a PDF",
      description: "Render a PDF instead of a PNG. Default is false.",
      required: !1
    }),
    metadata: import_pieces_framework2.Property.LongText({
      displayName: "Metadata",
      description: "Any metadata that you need to store e.g. ID of a record in your DB.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let body = {
      modifications: Object.keys(propsValue.modifications).length === 0 ? [] : [propsValue.modifications],
      template_version: propsValue.template_version,
      transparent: propsValue.transparent,
      render_pdf: propsValue.render_pdf,
      template: propsValue.template.uid,
      metadata: propsValue.metadata
    }, request = {
      method: import_pieces_common.HttpMethod.POST,
      url: "https://sync.api.bannerbear.com/v2/images",
      body,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: auth.secret_text
      }
    }, result = await import_pieces_common.httpClient.sendRequest(request);
    return result.status === 200 || result.status === 202 ? result.body : result;
  }
}), BannerbearModificationsMapping = {
  main: {
    name: import_pieces_framework2.Property.ShortText({
      displayName: "Name",
      required: !0,
      description: "The name of the layer you want to change."
    }),
    color: import_pieces_framework2.Property.ShortText({
      displayName: "Color",
      required: !1,
      description: 'Color in hex format e.g. "#FF0000".'
    })
  },
  common: {
    gradient: import_pieces_framework2.Property.ShortText({
      displayName: "Gradient",
      required: !1,
      description: 'Fill with gradient e.g. ["#000", "#FFF"]'
    }),
    border_width: import_pieces_framework2.Property.Number({
      displayName: "Border width",
      required: !1,
      description: "Width of the object border."
    }),
    border_color: import_pieces_framework2.Property.ShortText({
      displayName: "Border color",
      required: !1,
      description: 'Border color in hex format e.g. "#FF0000".'
    }),
    shift_x: import_pieces_framework2.Property.Number({
      displayName: "Shift x",
      required: !1,
      description: "Shift layer along the x axis."
    }),
    shift_y: import_pieces_framework2.Property.Number({
      displayName: "Shift y",
      required: !1,
      description: "Shift layer along the y axis."
    }),
    target: import_pieces_framework2.Property.ShortText({
      displayName: "Target",
      required: !1,
      description: "Add a clickable link to a URL on this object when rendering a PDF."
    }),
    hide: import_pieces_framework2.Property.Checkbox({
      displayName: "Hide",
      required: !1,
      description: "Set to true to hide a layer."
    })
  },
  text: {
    text: import_pieces_framework2.Property.ShortText({
      displayName: "Text",
      required: !1,
      description: "Replacement text you want to use."
    }),
    background: import_pieces_framework2.Property.ShortText({
      displayName: "Background",
      required: !1,
      description: 'Background color in hex format e.g. "#FF0000".'
    }),
    font_family: import_pieces_framework2.Property.ShortText({
      displayName: "Font family",
      required: !1,
      description: "Change the font."
    }),
    text_align_h: import_pieces_framework2.Property.ShortText({
      displayName: "Text align H",
      required: !1,
      description: "Horizontal alignment (left, center, right)"
    }),
    text_align_v: import_pieces_framework2.Property.ShortText({
      displayName: "Text align V",
      required: !1,
      description: "Vertical alignment (top, center, bottom)"
    }),
    font_family_2: import_pieces_framework2.Property.ShortText({
      displayName: "Font family 2",
      required: !1,
      description: "Change the secondary font."
    }),
    color_2: import_pieces_framework2.Property.ShortText({
      displayName: "Color 2",
      required: !1,
      description: "Change the secondary font color."
    })
  },
  image: {
    image_url: import_pieces_framework2.Property.ShortText({
      displayName: "Image url",
      required: !1,
      description: "Change the image."
    }),
    effect: import_pieces_framework2.Property.ShortText({
      displayName: "Effect",
      required: !1,
      description: "Change the effect."
    }),
    anchor_x: import_pieces_framework2.Property.ShortText({
      displayName: "Anchor x",
      required: !1,
      description: "Change the anchor point (left, center, right)"
    }),
    anchor_y: import_pieces_framework2.Property.ShortText({
      displayName: "Anchor y",
      required: !1,
      description: "Change the anchor point (top, center, bottom)."
    }),
    fill_type: import_pieces_framework2.Property.ShortText({
      displayName: "Fill type",
      required: !1,
      description: "Change the fill type (fill, fit)."
    }),
    disable_face_detect: import_pieces_framework2.Property.Checkbox({
      displayName: "Disable face detect",
      required: !1,
      description: "Set to true to disable face detect for this request (if the image container is using face detect)."
    }),
    disable_smart_crop: import_pieces_framework2.Property.Checkbox({
      displayName: "Disable smart crop",
      required: !1,
      description: "Set to true to disable smart crop for this request (if the image container is using smart crop)"
    })
  },
  barline_chart: {
    chart_data: import_pieces_framework2.Property.ShortText({
      displayName: "Chart data",
      required: !1,
      description: "Comma-delimited list of numbers to use as data."
    })
  },
  star_rating: {
    rating: import_pieces_framework2.Property.Number({
      displayName: "Rating",
      required: !1,
      description: "Number from 0 to 100 to use as the rating."
    })
  },
  qr_code: {
    target: import_pieces_framework2.Property.ShortText({
      displayName: "Target",
      required: !1,
      description: "URL or text to use as the code target."
    })
  },
  bar_code: {
    bar_code_data: import_pieces_framework2.Property.ShortText({
      displayName: "Barcode data",
      required: !1,
      description: "Text to encode as a bar code."
    })
  }
};

// ../ap/packages/pieces/community/bannerbear/src/index.ts
var bannerbear = (0, import_pieces_framework3.createPiece)({
  displayName: "Bannerbear",
  description: "Automate image generation",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/bannerbear.png",
  categories: [import_pieces_framework4.PieceCategory.MARKETING],
  authors: ["kishanprmr", "MoShizzle", "khaledmashaly", "abuaboud"],
  auth: bannerbearAuth,
  actions: [
    bannerbearCreateImageAction,
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: () => "https://sync.api.bannerbear.com/v2",
      auth: bannerbearAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  bannerbear
});
