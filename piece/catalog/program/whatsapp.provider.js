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

// ../ap/packages/pieces/community/whatsapp/src/index.ts
var index_exports = {};
__export(index_exports, {
  whatsapp: () => whatsapp
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/whatsapp/src/lib/actions/send-message.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/whatsapp/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), markdown = `
To Obtain a Phone Number ID and a Permanent System User Access Token, follow these steps:

1. Go to https://developers.facebook.com/
2. Make a new app, Select Other for usecase.
3. Choose Business as the type of app.
4. Add new Product -> WhatsApp.
5. Navigate to WhatsApp Settings > API Setup.
6. Copy the Business Account ID.
7. Login to your [Meta Business Manager](https://business.facebook.com/).
8. Click on Settings.
9. Create a new System User with access over the app and copy the access token.
`, whatsappAuth = import_pieces_framework.PieceAuth.CustomAuth({
  required: !0,
  description: markdown,
  props: {
    access_token: import_pieces_framework.PieceAuth.SecretText({
      displayName: "System User Access Token",
      description: "The system user access token of your WhatsApp business account.",
      required: !0
    }),
    businessAccountId: import_pieces_framework.Property.ShortText({
      displayName: "Business Account ID",
      description: "The business account ID of your WhatsApp business account.",
      required: !0
    })
  }
});

// ../ap/packages/pieces/community/whatsapp/src/lib/common/utils.ts
var import_pieces_common = require("@activepieces/pieces-common");
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var supportedMediaTypes = ["image", "audio", "document", "sticker", "video"], capitalizeFirstLetter = (word) => word.charAt(0).toUpperCase() + word.slice(1), mediaTypeSupportsCaption = (type) => ["image", "video", "document"].includes(type), commonProps = {
  phone_number_id: import_pieces_framework2.Property.Dropdown({
    auth: whatsappAuth,
    displayName: "Phone Number ID",
    description: "Phone number ID that will be used to send the message.",
    refreshers: [],
    required: !0,
    options: async ({ auth }) => {
      if (!auth)
        return {
          placeholder: "Please connect account first",
          disabled: !0,
          options: []
        };
      let authValue = auth.props, options = [], hasMore = !1, cursor;
      do {
        let qs = {
          fields: "verified_name,id,display_phone_number",
          limit: "1"
        };
        cursor && (qs.after = cursor);
        let response = await import_pieces_common.httpClient.sendRequest({
          method: import_pieces_common.HttpMethod.GET,
          url: `https://graph.facebook.com/v20.0/${authValue.businessAccountId}/phone_numbers`,
          authentication: {
            type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
            token: authValue.access_token
          },
          queryParams: qs
        });
        for (let phoneNumber of response.body.data)
          options.push({
            label: `${phoneNumber.verified_name} : ${phoneNumber.display_phone_number}`,
            value: phoneNumber.id
          });
        response.body.paging.next ? (hasMore = !0, cursor = response.body.paging.cursors.after) : hasMore = !1;
      } while (hasMore);
      return {
        disabled: !1,
        options
      };
    }
  }),
  message_template_id: import_pieces_framework2.Property.Dropdown({
    displayName: "Message Template ID",
    refreshers: [],
    required: !0,
    auth: whatsappAuth,
    options: async ({ auth }) => {
      if (!auth)
        return {
          placeholder: "Please connect account first",
          disabled: !0,
          options: []
        };
      let authValue = auth.props, options = [], hasMore = !1, cursor;
      do {
        let qs = {
          fields: "id,name,language",
          limit: "1"
        };
        cursor && (qs.after = cursor);
        let response = await import_pieces_common.httpClient.sendRequest({
          method: import_pieces_common.HttpMethod.GET,
          url: `https://graph.facebook.com/v20.0/${authValue.businessAccountId}/message_templates`,
          authentication: {
            type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
            token: authValue.access_token
          },
          queryParams: qs
        });
        for (let template of response.body.data)
          options.push({
            label: `${template.name} (${template.language})`,
            value: template.id
          });
        response.body.paging.next ? (hasMore = !0, cursor = response.body.paging.cursors.after) : hasMore = !1;
      } while (hasMore);
      return {
        disabled: !1,
        options
      };
    }
  }),
  message_template_fields: import_pieces_framework2.Property.DynamicProperties({
    displayName: "Template Fields",
    refreshers: ["message_template_id"],
    required: !0,
    auth: whatsappAuth,
    props: async ({ auth, message_template_id }) => {
      var _a, _b, _c, _d, _e, _f;
      if (!auth) return {};
      if (!message_template_id) return {};
      let authValue = auth.props, templateId = message_template_id, response = await import_pieces_common.httpClient.sendRequest({
        url: `https://graph.facebook.com/v20.0/${templateId}`,
        method: import_pieces_common.HttpMethod.GET,
        authentication: {
          type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
          token: authValue.access_token
        }
      }), bodyComponentFields = {}, headerComponentFields = {}, buttonComponentFields = {};
      for (let component of response.body.components)
        if (component.type === "BODY") {
          bodyComponentFields.BODY_markdown = import_pieces_framework2.Property.MarkDown({
            value: `
						**Body :**
						${component.text}`
          });
          let bodyTextVariables = (_b = (_a = component.text) == null ? void 0 : _a.match(/{{(\d+)}}/g)) != null ? _b : [];
          for (let i = 0; i < bodyTextVariables.length; i++)
            bodyComponentFields[`body_{{${i + 1}}}`] = import_pieces_framework2.Property.ShortText({
              displayName: `Body {{${i + 1}}}`,
              required: !1
            });
        } else if (component.type === "HEADER" && component.format === "TEXT") {
          headerComponentFields.HEADER_markdown = import_pieces_framework2.Property.MarkDown({
            value: `
						**Header :**
						${component.text}`
          });
          let headerTextVariables = (_d = (_c = component.text) == null ? void 0 : _c.match(/{{(\d+)}}/g)) != null ? _d : [];
          for (let i = 0; i < headerTextVariables.length; i++)
            headerComponentFields[`header_{{${i + 1}}}`] = import_pieces_framework2.Property.ShortText({
              displayName: `Header {{${i + 1}}}`,
              required: !1
            });
        } else if (component.type === "BUTTONS") {
          for (let button of component.buttons)
            if (button.type === "URL") {
              let buttonURLTextVariables = (_f = (_e = button.url) == null ? void 0 : _e.match(/{{(\d+)}}/g)) != null ? _f : [];
              for (let i = 0; i < buttonURLTextVariables.length; i++)
                buttonComponentFields[`button_{{${i + 1}}}`] = import_pieces_framework2.Property.ShortText({
                  displayName: button.text,
                  required: !1
                });
            }
        }
      return __spreadValues(__spreadValues(__spreadValues({}, headerComponentFields), bodyComponentFields), buttonComponentFields);
    }
  })
};

// ../ap/packages/pieces/community/whatsapp/src/lib/actions/send-message.ts
var sendMessage = (0, import_pieces_framework3.createAction)({
  auth: whatsappAuth,
  name: "sendMessage",
  classification: "WRITE",
  displayName: "Send Message",
  description: "Send a text message through WhatsApp",
  audience: "both",
  aiMetadata: { description: "Sends a plain text WhatsApp message from a business phone number to a single recipient via the WhatsApp Cloud API. Choose this for free-form text replies or notifications when no media or pre-approved template is needed; for media use Send Media and for marketing/utility templates use Send Template Message. Requires the sender phone number ID and the recipient phone number in international format; outside the 24-hour customer service window only template messages are deliverable. Not idempotent \u2014 each call delivers a new message.", idempotent: !1 },
  props: {
    phone_number_id: commonProps.phone_number_id,
    to: import_pieces_framework3.Property.ShortText({
      displayName: "To",
      description: "The recipient of the message",
      required: !0
    }),
    text: import_pieces_framework3.Property.LongText({
      displayName: "Message",
      description: "The message to send",
      required: !0
    })
  },
  async run(context) {
    let { to, text, phone_number_id } = context.propsValue, { access_token } = context.auth.props;
    return await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: `https://graph.facebook.com/v17.0/${phone_number_id}/messages`,
      headers: {
        Authorization: "Bearer " + access_token
      },
      body: {
        messaging_product: "whatsapp",
        recipient_type: "individual",
        to,
        type: "text",
        text: {
          body: text
        }
      }
    });
  }
});

// ../ap/packages/pieces/community/whatsapp/src/lib/actions/send-media.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var sendMedia = (0, import_pieces_framework4.createAction)({
  auth: whatsappAuth,
  name: "sendMedia",
  classification: "WRITE",
  displayName: "Send Media",
  description: "Send a media message through WhatsApp",
  audience: "both",
  aiMetadata: { description: "Sends an image, video, audio, document, or sticker to a WhatsApp recipient by referencing the media via a public URL. Choose this when the message payload is a file rather than plain text; captions are supported for media types that allow them and a filename can be set for documents. Requires the sender phone number ID, recipient phone number, media type, and a reachable media URL; subject to WhatsApp messaging-window rules. Not idempotent \u2014 each call delivers a new message.", idempotent: !1 },
  props: {
    phone_number_id: commonProps.phone_number_id,
    to: import_pieces_framework4.Property.ShortText({
      displayName: "To",
      description: "The recipient of the message",
      required: !0
    }),
    type: import_pieces_framework4.Property.Dropdown({
      auth: whatsappAuth,
      displayName: "Type",
      description: "The type of media to send",
      required: !0,
      options: async () => ({
        options: supportedMediaTypes.map((type) => ({
          label: capitalizeFirstLetter(type),
          value: type
        }))
      }),
      refreshers: []
    }),
    media: import_pieces_framework4.Property.ShortText({
      displayName: "Media URL",
      description: "The URL of the media to send",
      required: !0
    }),
    caption: import_pieces_framework4.Property.LongText({
      displayName: "Caption",
      description: "A caption for the media",
      required: !1
    }),
    filename: import_pieces_framework4.Property.LongText({
      displayName: "Filename",
      description: "Filename of the document to send",
      required: !1
    })
  },
  async run(context) {
    let { to, caption, media, type, filename, phone_number_id } = context.propsValue, { access_token } = context.auth.props, body = {
      messaging_product: "whatsapp",
      recipient_type: "individual",
      to,
      type,
      [type]: {
        link: media
      }
    };
    return caption && mediaTypeSupportsCaption(type) && (body[type].caption = caption), filename && type === "document" && (body[type].filename = filename), await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: `https://graph.facebook.com/v17.0/${phone_number_id}/messages`,
      headers: {
        Authorization: "Bearer " + access_token
      },
      body
    });
  }
});

// ../ap/packages/pieces/community/whatsapp/src/lib/actions/send-from-template.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var sendTemplateMessageAction = (0, import_pieces_framework5.createAction)({
  auth: whatsappAuth,
  name: "send-template-message",
  classification: "WRITE",
  displayName: "Send Template Message",
  description: "Sends a template message.",
  audience: "both",
  aiMetadata: { description: "Sends a pre-approved WhatsApp message template to a recipient, filling its header, body, and button placeholders from the supplied fields. Choose this to initiate conversations outside the 24-hour customer service window or for notifications/marketing where a registered template is required; the template must already be approved in the WhatsApp Business account. Requires the sender phone number ID, recipient phone number, and the template ID (its name and language are resolved automatically). Not idempotent \u2014 each call delivers a new message.", idempotent: !1 },
  props: {
    phone_number_id: commonProps.phone_number_id,
    to: import_pieces_framework5.Property.ShortText({
      displayName: "To",
      description: "Recipient phone number.",
      required: !0
    }),
    message_template_id: commonProps.message_template_id,
    message_template_fields: commonProps.message_template_fields
  },
  async run(context) {
    let phoneNumberId = context.propsValue.phone_number_id, recipientPhoneNumber = context.propsValue.to, templateId = context.propsValue.message_template_id, templateFields = context.propsValue.message_template_fields, components = [], headerParameters = [], bodyParameters = [], buttonParameters = [];
    for (let [key, value] of Object.entries(templateFields))
      key.startsWith("header_") ? headerParameters.push({ type: "text", text: value }) : key.startsWith("body_") ? bodyParameters.push({ type: "text", text: value }) : key.startsWith("button_") && buttonParameters.push({ type: "text", text: value });
    headerParameters.length && components.push({
      type: "header",
      parameters: headerParameters
    }), bodyParameters.length && components.push({
      type: "body",
      parameters: bodyParameters
    }), buttonParameters.length && components.push({
      type: "button",
      sub_type: "url",
      index: 0,
      parameters: buttonParameters
    });
    let templateData = await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.GET,
      url: `https://graph.facebook.com/v20.0/${templateId}`,
      authentication: {
        type: import_pieces_common4.AuthenticationType.BEARER_TOKEN,
        token: context.auth.props.access_token
      },
      queryParams: {
        fields: "id,name,language"
      }
    }), templateLanguage = templateData.body.language, templateName = templateData.body.name;
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: `https://graph.facebook.com/v20.0/${phoneNumberId}/messages`,
      authentication: {
        type: import_pieces_common4.AuthenticationType.BEARER_TOKEN,
        token: context.auth.props.access_token
      },
      body: {
        messaging_product: "whatsapp",
        recipient_type: "individual",
        to: recipientPhoneNumber,
        type: "template",
        template: {
          name: templateName,
          language: {
            code: templateLanguage
          },
          components
        }
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/whatsapp/src/index.ts
var whatsapp = (0, import_pieces_framework6.createPiece)({
  displayName: "WhatsApp Business",
  description: "Manage your WhatsApp business account",
  auth: whatsappAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/whatsapp.png",
  authors: ["LevwTech", "kishanprmr"],
  actions: [sendMessage, sendMedia, sendTemplateMessageAction],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  whatsapp
});
