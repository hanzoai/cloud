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

// ../ap/packages/pieces/community/thankster/src/index.ts
var index_exports = {};
__export(index_exports, {
  thankster: () => thankster,
  thanksterAuth: () => thanksterAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/thankster/src/lib/actions/send-cards.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var sendCards = (0, import_pieces_framework.createAction)({
  name: "send_handwritten_cards",
  displayName: "Send Cards",
  description: "Automatically send handwritten cards.",
  audience: "both",
  aiMetadata: { description: "Sends a physical handwritten card via Thankster, using one of the account's saved card templates, to a recipient mailing address. Choose this to mail a real card (thank-you, greeting, marketing) from an automation. Requires a valid template ID plus sender and recipient name/address; the optional text and image-URL fields override the template's corresponding text/image boxes, otherwise the template's own content is used. Not idempotent \u2014 each call places a new card order.", idempotent: !1 },
  auth: thanksterAuth,
  props: {
    templateID: import_pieces_framework.Property.Dropdown({
      auth: thanksterAuth,
      displayName: "Select a Thankster Template",
      description: "If you are passing text or images from prior steps in this step into the Thankster template chosen above, be sure to select a template that has corresponding text or images boxes to pass it into. Either way, the font and style of your cards will be taken from the template you select.",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please fill in API key first"
          };
        let res = await import_pieces_common.httpClient.sendRequest({
          method: import_pieces_common.HttpMethod.GET,
          url: "https://app.thankster.com/api/v1/api_projects/listUserProjects",
          headers: {
            partner: "partner_active_pieces",
            userApiKey: auth.secret_text
          }
        }), opts = [];
        if (res.body)
          for (let template of res.body)
            opts.push({
              label: template.name,
              value: template.id
            });
        return {
          disabled: !1,
          options: opts
        };
      }
    }),
    fname: import_pieces_framework.Property.ShortText({
      displayName: "Sender Firstname",
      description: "Sender first name.",
      required: !0
    }),
    lname: import_pieces_framework.Property.ShortText({
      displayName: "Sender Lastname",
      description: "Sender last name.",
      required: !1
    }),
    company: import_pieces_framework.Property.ShortText({
      displayName: "Sender Company Name",
      description: "Enter sender company name.",
      required: !1
    }),
    address: import_pieces_framework.Property.ShortText({
      displayName: "Sender Address1",
      description: "Enter sender address1.",
      required: !0
    }),
    address2: import_pieces_framework.Property.ShortText({
      displayName: "Sender Address2",
      description: "Enter sender address2.",
      required: !1
    }),
    city: import_pieces_framework.Property.ShortText({
      displayName: "Sender City",
      description: "Enter sender city.",
      required: !0
    }),
    state: import_pieces_framework.Property.ShortText({
      displayName: "Sender State",
      description: "Enter sender state.",
      required: !1
    }),
    zip: import_pieces_framework.Property.ShortText({
      displayName: "Sender Zip",
      description: "Enter sender zip.",
      required: !0
    }),
    country: import_pieces_framework.Property.ShortText({
      displayName: "Sender Country",
      description: "If not US, type the two character country code.",
      defaultValue: "US",
      required: !1
    }),
    r_fname: import_pieces_framework.Property.ShortText({
      displayName: "Receiver Firstname",
      description: "Receiver first name.",
      required: !0
    }),
    r_lname: import_pieces_framework.Property.ShortText({
      displayName: "Receiver Lastname",
      description: "Receiver last name.",
      required: !1
    }),
    r_company: import_pieces_framework.Property.ShortText({
      displayName: "Receiver Company",
      description: "Receiver company name.",
      required: !1
    }),
    r_address: import_pieces_framework.Property.ShortText({
      displayName: "Receiver Address1",
      description: "Receiver address1.",
      required: !0
    }),
    r_address2: import_pieces_framework.Property.ShortText({
      displayName: "Receiver Address2",
      description: "Receiver address2.",
      required: !1
    }),
    r_city: import_pieces_framework.Property.ShortText({
      displayName: "Receiver City",
      description: "Receiver city.",
      required: !0
    }),
    r_state: import_pieces_framework.Property.ShortText({
      displayName: "Receiver State",
      description: "Receiver state.",
      required: !1
    }),
    r_zip: import_pieces_framework.Property.ShortText({
      displayName: "Receiver Zip",
      description: "Receiver zip.",
      required: !0
    }),
    r_country: import_pieces_framework.Property.ShortText({
      displayName: "Receiver Country",
      description: "If not US, type the two character country code.",
      defaultValue: "US",
      required: !1
    }),
    api_text_one: import_pieces_framework.Property.LongText({
      displayName: "Text One",
      description: "Optional - if left blank, it will take the message from your Thankster template. Otherwise, map this to an api text box in the Thankster template being in Thankster Template field, 200 character maximum suggested.",
      required: !1
    }),
    api_text_two: import_pieces_framework.Property.LongText({
      displayName: "Text Two",
      description: "Optional - if left blank, it will take the message from your Thankster template. Otherwise, map this to an api text box in the Thankster template being in Thankster Template field, 200 character maximum suggested.",
      required: !1
    }),
    api_text_three: import_pieces_framework.Property.LongText({
      displayName: "Text Three",
      description: "Optional - if left blank, it will take the message from your Thankster template. Otherwise, map this to an api text box in the Thankster template being in Thankster Template field, 200 character maximum suggested.",
      required: !1
    }),
    api_text_four: import_pieces_framework.Property.LongText({
      displayName: "Text Four",
      description: "Optional - if left blank, it will take the message from your Thankster template. Otherwise, map this to an api text box in the Thankster template being in Thankster Template field, 200 character maximum suggested.",
      required: !1
    }),
    api_image_one: import_pieces_framework.Property.ShortText({
      displayName: "Image One (URL)",
      description: "Optional image URL for image one. Be sure this image is at least 1500 x 1900 pixels and has that aspect ratio to avoid stretching or pixelated.",
      required: !1
    }),
    api_image_two: import_pieces_framework.Property.ShortText({
      displayName: "Image Two (URL)",
      description: "Optional image URL for image one. Be sure this image is at least 1500 x 1900 pixels and has that aspect ratio to avoid stretching or pixelated.",
      required: !1
    }),
    api_image_three: import_pieces_framework.Property.ShortText({
      displayName: "Image Three (URL)",
      description: "Optional image URL for image one. Be sure this image is at least 1500 x 1900 pixels and has that aspect ratio to avoid stretching or pixelated.",
      required: !1
    }),
    api_image_four: import_pieces_framework.Property.ShortText({
      displayName: "Image Four (URL)",
      description: "Optional image URL for image one. Be sure this image is at least 1500 x 1900 pixels and has that aspect ratio to avoid stretching or pixelated.",
      required: !1
    }),
    sender_image: import_pieces_framework.Property.ShortText({
      displayName: "Sender Image",
      description: "Optional image URL for return address (on the envelope). The recommended image size is 300x120 pixels or that aspect ratio to avoid stretching or pixelation. The image will be on top of the text and it will be downsized preserving the aspect ratio to fit the boundaries if necessary.",
      required: !1
    }),
    recipient_image: import_pieces_framework.Property.ShortText({
      displayName: "Recipient Image",
      description: "Optional image URL for return address (on the envelope). The recommended image size is 300x120 pixels or that aspect ratio to avoid stretching or pixelation. The image will be on top of the text and it will be downsized preserving the aspect ratio to fit the boundaries if necessary.",
      required: !1
    })
  },
  async run(context) {
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://app.thankster.com/api/v1/api_projects/createQuickProject",
      headers: {
        partner: "partner_active_pieces",
        userApiKey: context.auth.secret_text
      },
      body: {
        templateID: context.propsValue.templateID,
        fname: context.propsValue.fname,
        lname: context.propsValue.lname,
        company: context.propsValue.company,
        address: context.propsValue.address,
        address2: context.propsValue.address2,
        city: context.propsValue.city,
        state: context.propsValue.state,
        zip: context.propsValue.zip,
        country: context.propsValue.country,
        r_fname: context.propsValue.r_fname,
        r_lname: context.propsValue.r_lname,
        r_address: context.propsValue.r_address,
        r_address2: context.propsValue.r_address2,
        r_city: context.propsValue.r_city,
        r_state: context.propsValue.r_state,
        r_zip: context.propsValue.r_zip,
        r_country: context.propsValue.r_country,
        api_text_one: context.propsValue.api_text_one,
        api_text_two: context.propsValue.api_text_two,
        api_text_three: context.propsValue.api_text_three,
        api_text_four: context.propsValue.api_text_four,
        api_image_one: context.propsValue.api_image_one,
        api_image_two: context.propsValue.api_image_two,
        api_image_three: context.propsValue.api_image_three,
        api_image_four: context.propsValue.api_image_four,
        sender_image: context.propsValue.sender_image,
        recipient_image: context.propsValue.recipient_image
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/thankster/src/index.ts
var thanksterAuth = import_pieces_framework2.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "Go My Profile page to find your API Key at the bottom."
}), thankster = (0, import_pieces_framework2.createPiece)({
  displayName: "Thankster",
  auth: thanksterAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/thankster.png",
  authors: [],
  actions: [sendCards],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  thankster,
  thanksterAuth
});
