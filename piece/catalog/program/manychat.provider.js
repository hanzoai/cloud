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

// ../ap/packages/pieces/community/manychat/src/index.ts
var index_exports = {};
__export(index_exports, {
  manychat: () => manychat
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework11 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/manychat/src/lib/actions/find-user-by-custom-field.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/manychat/src/lib/common/props.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/manychat/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var manychatAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "You can create an API key by navigating to **Setting -> Extensions -> API**.",
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${BASE_URL}/page/getInfo`,
        authentication: {
          type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
          token: auth
        }
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API Key"
      };
    }
  }
});

// ../ap/packages/pieces/community/manychat/src/lib/common/props.ts
var BASE_URL = "https://api.manychat.com/fb", subscriberId = import_pieces_framework2.Property.Number({
  displayName: "User ID",
  description: "Please refer [Manychat Guide](https://help.manychat.com/hc/en-us/articles/14959510331420-How-to-generate-a-token-for-the-Manychat-API-and-where-to-get-parameters#h_01JCR55RJ0B0PQW6HDW6RW0A42) to obtain user/contact ID.",
  required: !0
}), tagIdDropdown = import_pieces_framework2.Property.Dropdown({
  auth: manychatAuth,
  displayName: "Tag",
  refreshers: [],
  required: !0,
  options: async ({ auth }) => auth ? {
    disabled: !1,
    options: (await import_pieces_common2.httpClient.sendRequest({
      url: `${BASE_URL}/page/getTags`,
      method: import_pieces_common2.HttpMethod.GET,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.secret_text
      }
    })).body.data.map((tag) => ({
      label: tag.name,
      value: tag.id
    }))
  } : {
    placeholder: "Please connect your account.",
    disabled: !0,
    options: []
  }
});

// ../ap/packages/pieces/community/manychat/src/lib/actions/find-user-by-custom-field.ts
var findUserByCustomFieldAction = (0, import_pieces_framework3.createAction)({
  auth: manychatAuth,
  name: "findUserByCustomField",
  displayName: "Find User by Custom Field",
  description: "Finds a user by custom field.",
  audience: "both",
  aiMetadata: { description: "Searches Manychat subscribers by a custom field (text or number type) matching a given value, returning whether any matched and the list of matches. Use to look up a subscriber by a stored attribute when you do not have their subscriber ID. Read-only and idempotent.", idempotent: !0 },
  props: {
    field: import_pieces_framework3.Property.Dropdown({
      auth: manychatAuth,
      displayName: "Custom Field",
      refreshers: [],
      required: !0,
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await import_pieces_common3.httpClient.sendRequest({
          url: `${BASE_URL}/page/getCustomFields`,
          method: import_pieces_common3.HttpMethod.GET,
          authentication: {
            type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
            token: auth.secret_text
          }
        })).body.data.filter((field) => ["text", "number"].includes(field.type)).map((field) => ({
          label: field.name,
          value: field.id
        }))
      } : {
        placeholder: "Please connect your account.",
        disabled: !0,
        options: []
      }
    }),
    value: import_pieces_framework3.Property.ShortText({
      displayName: "Value",
      description: "The value to search for.",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let { field, value } = propsValue, response = await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: `${BASE_URL}/subscriber/findByCustomField`,
      queryParams: {
        field_id: field.toString(),
        field_value: value
      },
      authentication: {
        type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
        token: auth.secret_text
      }
    });
    return {
      found: response.body.status === "success" && response.body.data.length > 0,
      result: response.body.data
    };
  }
});

// ../ap/packages/pieces/community/manychat/src/lib/actions/create-subscriber.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var import_pieces_framework5 = require("@activepieces/pieces-framework"), createSubscriberAction = (0, import_pieces_framework4.createAction)({
  auth: manychatAuth,
  name: "createSubscriber",
  displayName: "Create Subscriber",
  description: "Creates a Unified or a Whatsapp subscriber.",
  audience: "both",
  aiMetadata: { description: "Creates a new Manychat subscriber from contact details such as name, email, phone, or WhatsApp phone. Use when onboarding a new contact into Manychat. At least one of phone, email, or WhatsApp phone must be provided. Not idempotent: each call creates another subscriber.", idempotent: !1 },
  props: {
    first_name: import_pieces_framework4.Property.ShortText({
      displayName: "First Name",
      required: !0
    }),
    last_name: import_pieces_framework4.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    phone: import_pieces_framework4.Property.ShortText({
      displayName: "Phone Number",
      required: !1,
      description: "Fill in at least one field: phone or email or whatsapp phone."
    }),
    whatsapp_phone: import_pieces_framework4.Property.ShortText({
      displayName: "WhatsApp Phone",
      required: !1,
      description: "Fill in at least one field: phone or email or whatsapp phone."
    }),
    email: import_pieces_framework4.Property.ShortText({
      displayName: "Email",
      required: !1,
      description: "Fill in at least one field: phone or email or whatsapp phone."
    }),
    gender: import_pieces_framework4.Property.ShortText({
      displayName: "Gender",
      required: !1
    }),
    has_opt_in_sms: import_pieces_framework4.Property.Checkbox({
      displayName: "SMS Opt-in",
      required: !1
    }),
    has_opt_in_email: import_pieces_framework4.Property.Checkbox({
      displayName: "Email Opt-in",
      required: !1
    }),
    consent_phrase: import_pieces_framework4.Property.ShortText({
      displayName: "Consent Phrase",
      description: "The consent phrase provided by the subscriber.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let {
      first_name,
      last_name,
      phone,
      whatsapp_phone,
      email,
      gender,
      has_opt_in_email,
      has_opt_in_sms,
      consent_phrase
    } = propsValue;
    if ((0, import_pieces_framework5.isNil)(phone) && (0, import_pieces_framework5.isNil)(whatsapp_phone) && (0, import_pieces_framework5.isNil)(email))
      throw Error(
        "To create a subscriber you must fill in at least one field: phone or email or whatsapp_phone."
      );
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: `${BASE_URL}/subscriber/createSubscriber`,
      authentication: {
        type: import_pieces_common4.AuthenticationType.BEARER_TOKEN,
        token: auth.secret_text
      },
      body: {
        first_name,
        last_name,
        phone,
        whatsapp_phone,
        email,
        gender,
        has_opt_in_sms,
        has_opt_in_email,
        consent_phrase
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/manychat/src/lib/actions/send-content-to-user.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var sendContentToUserAction = (0, import_pieces_framework6.createAction)({
  auth: manychatAuth,
  name: "sendContentToUser",
  displayName: "Send Content to User",
  description: "Sends a content to a user.",
  audience: "both",
  aiMetadata: { description: "Sends a message to a Manychat subscriber, identified by subscriber ID, where the content type selects the mode: a text message, or a media message (image, video, audio, or file) referenced by URL. Use to deliver an outbound message to a known contact, optionally targeting a specific platform and applying a message tag. Text content is required for text mode and a media URL for media modes. Not idempotent: each call sends another message.", idempotent: !1 },
  props: {
    subscriber_id: subscriberId,
    platform: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Platform",
      description: "The platform to send the content to.",
      required: !1,
      options: {
        options: [
          { label: "Facebook", value: "facebook" },
          { label: "Instagram", value: "instagram" },
          { label: "WhatsApp", value: "whatsapp" },
          { label: "Telegram", value: "telegram" }
        ]
      }
    }),
    content_type: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Content Type",
      description: "The type of content to send.",
      required: !0,
      options: {
        options: [
          { label: "Text", value: "text" },
          { label: "Image", value: "image" },
          { label: "Video", value: "video" },
          { label: "Audio", value: "audio" },
          { label: "File", value: "file" }
        ]
      }
    }),
    text_content: import_pieces_framework6.Property.LongText({
      displayName: "Text Content",
      description: "The text content to send. Required when content type is Text.",
      required: !1,
      defaultValue: ""
    }),
    media_url: import_pieces_framework6.Property.ShortText({
      displayName: "Media URL",
      description: "URL of the media to send (image, video, audio, or file). Required for media content types.",
      required: !1
    }),
    message_tag: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Message Tag",
      description: "The tag to use for the message.",
      required: !1,
      options: {
        options: [
          { label: "Account Update", value: "ACCOUNT_UPDATE" },
          { label: "Confirmed Event Update", value: "CONFIRMED_EVENT_UPDATE" },
          { label: "Human Agent", value: "HUMAN_AGENT" },
          { label: "Post Purchase Update", value: "POST_PURCHASE_UPDATE" },
          { label: "Business Productivity", value: "BUSINESS_PRODUCTIVITY" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    let { subscriber_id, platform, content_type, text_content, media_url, message_tag } = propsValue;
    if (content_type === "text" && !text_content)
      throw new Error("Text content is required when content type is Text");
    if (["image", "video", "audio", "file"].includes(content_type) && !media_url)
      throw new Error(`Media URL is required when content type is ${content_type}`);
    let messages = [];
    switch (content_type) {
      case "text":
        messages.push({
          type: "text",
          text: text_content
        });
        break;
      case "image":
        messages.push({
          type: "image",
          url: media_url
        });
        break;
      case "video":
        messages.push({
          type: "video",
          url: media_url
        });
        break;
      case "audio":
        messages.push({
          type: "audio",
          url: media_url
        });
        break;
      case "file":
        messages.push({
          type: "file",
          url: media_url
        });
        break;
    }
    let content = {
      version: "v2",
      content: {
        messages,
        actions: [],
        quick_replies: []
      }
    };
    platform && platform !== "facebook" && (content.content.type = platform);
    let requestBody = {
      subscriber_id,
      data: content
    };
    return message_tag && (requestBody.message_tag = message_tag), (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.POST,
      url: "https://api.manychat.com/fb/sending/sendContent",
      headers: {
        accept: "application/json",
        Authorization: `Bearer ${auth}`,
        "Content-Type": "application/json"
      },
      body: requestBody
    })).body;
  }
});

// ../ap/packages/pieces/community/manychat/src/lib/actions/set-custom-fields.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var setCustomFieldAction = (0, import_pieces_framework7.createAction)({
  auth: manychatAuth,
  name: "setCustomField",
  displayName: "Set Custom Field",
  description: "Ass or Updates a custom field value for a user.",
  audience: "both",
  aiMetadata: { description: "Sets or updates a custom field value on a Manychat subscriber, identified by subscriber ID and the chosen custom field, then returns the updated subscriber info. Use to store or change a single attribute on a known contact; the input type adapts to the field (text, number, date, or boolean). Idempotent: setting the same value again yields the same state.", idempotent: !0 },
  props: {
    subscriber_id: subscriberId,
    field_id: import_pieces_framework7.Property.Dropdown({
      auth: manychatAuth,
      displayName: "Custom Field",
      refreshers: [],
      required: !0,
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await import_pieces_common6.httpClient.sendRequest({
          url: `${BASE_URL}/page/getCustomFields`,
          method: import_pieces_common6.HttpMethod.GET,
          authentication: {
            type: import_pieces_common6.AuthenticationType.BEARER_TOKEN,
            token: auth.secret_text
          }
        })).body.data.filter((field) => field.type !== "array").map((field) => ({
          label: field.name,
          value: `${field.id}:::${field.type}`
        }))
      } : {
        placeholder: "Please connect your account.",
        disabled: !0,
        options: []
      }
    }),
    field_value: import_pieces_framework7.Property.DynamicProperties({
      auth: manychatAuth,
      displayName: "Field Value",
      required: !0,
      refreshers: ["field_id"],
      props: async ({ auth, field_id }) => {
        if (!auth || !field_id) return {};
        let fields = {};
        switch (field_id.split(":::")[1]) {
          case "text":
            fields.value = import_pieces_framework7.Property.ShortText({
              displayName: "Value",
              required: !0
            });
            break;
          case "number":
            fields.value = import_pieces_framework7.Property.Number({
              displayName: "Value",
              required: !0
            });
            break;
          case "date":
          case "datetime":
            fields.value = import_pieces_framework7.Property.DateTime({
              displayName: "Value",
              required: !0
            });
            break;
          case "boolean":
            fields.value = import_pieces_framework7.Property.Checkbox({
              displayName: "Value",
              required: !0
            });
            break;
          default:
            break;
        }
        return fields;
      }
    })
  },
  async run({ auth, propsValue }) {
    let { subscriber_id, field_id, field_value } = propsValue, setCustomFieldResponse = await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.POST,
      url: `${BASE_URL}/subscriber/setCustomField`,
      authentication: {
        type: import_pieces_common6.AuthenticationType.BEARER_TOKEN,
        token: auth.secret_text
      },
      body: {
        subscriber_id,
        field_id: field_id.split(":::")[0],
        field_value: field_value.value
      }
    });
    if (setCustomFieldResponse.body.status !== "success")
      throw Error(`Unexpected Error occured : ${JSON.stringify(setCustomFieldResponse.body)}`);
    return (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.GET,
      url: `${BASE_URL}/subscriber/getInfo`,
      authentication: {
        type: import_pieces_common6.AuthenticationType.BEARER_TOKEN,
        token: auth.secret_text
      },
      queryParams: {
        subscriber_id: `${subscriber_id}`
      }
    })).body.data;
  }
});

// ../ap/packages/pieces/community/manychat/src/lib/actions/remove-tag-from-user.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var removeTagFromUserAction = (0, import_pieces_framework8.createAction)({
  name: "removeTagFromUser",
  displayName: "Remove Tag from User",
  description: "Remove a tag from a user.",
  audience: "both",
  aiMetadata: { description: "Removes a tag from a Manychat subscriber, identified by subscriber ID and tag ID, then returns the updated subscriber info. Use to unlabel or de-segment a known contact. Idempotent: removing a tag the subscriber no longer has leaves the same state.", idempotent: !0 },
  auth: manychatAuth,
  props: {
    subscriberId,
    tagId: tagIdDropdown
  },
  async run({ auth, propsValue }) {
    let { subscriberId: subscriberId2, tagId } = propsValue, removeTagResponse = await import_pieces_common7.httpClient.sendRequest({
      url: `${BASE_URL}/subscriber/removeTag`,
      method: import_pieces_common7.HttpMethod.POST,
      authentication: {
        type: import_pieces_common7.AuthenticationType.BEARER_TOKEN,
        token: auth.secret_text
      },
      body: {
        subscriber_id: subscriberId2,
        tag_id: tagId
      }
    });
    if (removeTagResponse.body.status !== "success")
      throw Error(`Unexpected Error occured : ${JSON.stringify(removeTagResponse.body)}`);
    return (await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.GET,
      url: `${BASE_URL}/subscriber/getInfo`,
      authentication: {
        type: import_pieces_common7.AuthenticationType.BEARER_TOKEN,
        token: auth.secret_text
      },
      queryParams: {
        subscriber_id: subscriberId2.toString()
      }
    })).body.data;
  }
});

// ../ap/packages/pieces/community/manychat/src/lib/actions/add-tag-to-user.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var addTagToUserAction = (0, import_pieces_framework9.createAction)({
  auth: manychatAuth,
  name: "addTagToUser",
  displayName: "Add Tag to User",
  description: "Adds a tag to a user.",
  audience: "both",
  aiMetadata: { description: "Assigns an existing tag to a Manychat subscriber, identified by subscriber ID and tag ID, then returns the updated subscriber info. Use to label or segment a known contact (e.g. mark them as a lead). Idempotent: re-adding the same tag leaves the subscriber in the same state.", idempotent: !0 },
  props: {
    subscriberId,
    tagId: tagIdDropdown
  },
  async run({ auth, propsValue }) {
    let { subscriberId: subscriberId2, tagId } = propsValue, addTagResponse = await import_pieces_common8.httpClient.sendRequest({
      url: `${BASE_URL}/subscriber/addTag`,
      method: import_pieces_common8.HttpMethod.POST,
      authentication: {
        type: import_pieces_common8.AuthenticationType.BEARER_TOKEN,
        token: auth.secret_text
      },
      body: {
        subscriber_id: subscriberId2,
        tag_id: tagId
      }
    });
    if (addTagResponse.body.status !== "success")
      throw Error(`Unexpected Error occured : ${JSON.stringify(addTagResponse.body)}`);
    return (await import_pieces_common8.httpClient.sendRequest({
      method: import_pieces_common8.HttpMethod.GET,
      url: `${BASE_URL}/subscriber/getInfo`,
      authentication: {
        type: import_pieces_common8.AuthenticationType.BEARER_TOKEN,
        token: auth.secret_text
      },
      queryParams: {
        subscriber_id: `${subscriberId2}`
      }
    })).body.data;
  }
});

// ../ap/packages/pieces/community/manychat/src/lib/actions/find-user-by-name.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var findUserByNameAction = (0, import_pieces_framework10.createAction)({
  auth: manychatAuth,
  name: "findUserByName",
  displayName: "Find User by Name",
  description: "Finds a user by name.",
  audience: "both",
  aiMetadata: { description: "Searches Manychat subscribers by full name, returning whether any matched and the list of matches. Use to resolve a contact to their subscriber record when you only know their name. Read-only and idempotent.", idempotent: !0 },
  props: {
    name: import_pieces_framework10.Property.ShortText({
      displayName: "Name",
      description: "Provide user's full name to search.",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let { name } = propsValue, response = await import_pieces_common9.httpClient.sendRequest({
      method: import_pieces_common9.HttpMethod.GET,
      url: `${BASE_URL}/subscriber/findByName`,
      queryParams: {
        name
      },
      authentication: {
        type: import_pieces_common9.AuthenticationType.BEARER_TOKEN,
        token: auth.secret_text
      }
    });
    return {
      found: response.body.status === "success" && response.body.data.length > 0,
      result: response.body.data
    };
  }
});

// ../ap/packages/pieces/community/manychat/src/index.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var manychat = (0, import_pieces_framework11.createPiece)({
  displayName: "Manychat",
  description: "Automations for Instagram, WhatsApp, TikTok, and Messenger marketing.",
  categories: [import_pieces_framework12.PieceCategory.MARKETING],
  auth: manychatAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/manychat.png",
  authors: ["neo773", "kishanprmr"],
  actions: [
    addTagToUserAction,
    createSubscriberAction,
    findUserByCustomFieldAction,
    findUserByNameAction,
    removeTagFromUserAction,
    sendContentToUserAction,
    setCustomFieldAction
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  manychat
});
