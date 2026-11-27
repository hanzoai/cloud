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

// ../ap/packages/pieces/community/smaily/src/index.ts
var index_exports = {};
__export(index_exports, {
  smaily: () => smaily
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/smaily/src/lib/actions/create-or-update-subscriber.action.ts
var import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/smaily/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), smailyAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: `
  1. Click on profile pic (top right corner), navigate to **Preferences**.
  2. Go to **Integrations** tab and click on **Create a New User**.
  3. Copy generated domain, user and password.`,
  required: !0,
  props: {
    domain: import_pieces_framework.Property.ShortText({
      displayName: "Domain",
      required: !0
    }),
    username: import_pieces_framework.Property.ShortText({
      displayName: "User Name",
      required: !0
    }),
    password: import_pieces_framework.Property.ShortText({
      displayName: "Password",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `https://${auth.domain}.sendsmaily.net/api/organizations/users.php`,
        authentication: {
          type: import_pieces_common.AuthenticationType.BASIC,
          username: auth.username,
          password: auth.password
        }
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Please provide correct credentials."
      };
    }
  }
});

// ../ap/packages/pieces/community/smaily/src/lib/actions/create-or-update-subscriber.action.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var createOrUpdateSubscriberAction = (0, import_pieces_framework2.createAction)({
  auth: smailyAuth,
  name: "create-or-update-subscriber",
  displayName: "Create or Update Subscriber",
  description: "Creates a new subscriber or update an existing subscriber by email.",
  audience: "both",
  aiMetadata: {
    description: "Upsert a subscriber in a Smaily account, keyed on the email address: creates the contact if the email is new, otherwise updates the existing one (including subscription status and custom fields). Use to add or sync contact data. Idempotent \u2014 repeating the same input leaves the subscriber in the same state rather than creating duplicates.",
    idempotent: !0
  },
  props: {
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email",
      required: !0
    }),
    is_unsubscribed: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Subscription Status",
      required: !0,
      defaultValue: 0,
      options: {
        disabled: !1,
        options: [
          { label: "Unsubscribed", value: 1 },
          { label: "Subscribed", value: 0 }
        ]
      }
    }),
    custom_fields: import_pieces_framework2.Property.Object({
      displayName: "Custom Fields",
      description: "Go to **Subscribers Tab-> All Subscribers -> Manage Fields** to get custom field names.",
      required: !1
    })
  },
  async run(context) {
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: `https://${context.auth.props.domain}.sendsmaily.net/api/contact.php`,
      headers: {
        "Content-Type": "application/json"
      },
      authentication: {
        type: import_pieces_common2.AuthenticationType.BASIC,
        username: context.auth.props.username,
        password: context.auth.props.password
      },
      body: __spreadValues({
        email: context.propsValue.email,
        is_unsubscribed: context.propsValue.is_unsubscribed
      }, context.propsValue.custom_fields)
    })).body;
  }
});

// ../ap/packages/pieces/community/smaily/src/lib/actions/get-subscriber.action.ts
var import_pieces_common3 = require("@activepieces/pieces-common");
var import_pieces_framework3 = require("@activepieces/pieces-framework"), getSubscriberAction = (0, import_pieces_framework3.createAction)({
  auth: smailyAuth,
  name: "get-subscriber",
  displayName: "Get Subscriber",
  description: "retrieves detailed subscriber information for a given email address.",
  audience: "both",
  aiMetadata: {
    description: "Look up a single subscriber in a Smaily account by their email address and return their stored details (subscription status and custom fields). Use to check whether a contact exists or to read their current data before acting on it. Read-only and idempotent; requires the exact email.",
    idempotent: !0
  },
  props: {
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email",
      required: !0
    })
  },
  async run(context) {
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: `https://${context.auth.props.domain}.sendsmaily.net/api/contact.php`,
      queryParams: {
        email: context.propsValue.email
      },
      headers: {
        "Content-Type": "application/json"
      },
      authentication: {
        type: import_pieces_common3.AuthenticationType.BASIC,
        username: context.auth.props.username,
        password: context.auth.props.password
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/smaily/src/index.ts
var smaily = (0, import_pieces_framework4.createPiece)({
  displayName: "Smaily",
  auth: smailyAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/smaily.png",
  categories: [import_pieces_framework5.PieceCategory.MARKETING],
  authors: ["kishanprmr"],
  actions: [
    createOrUpdateSubscriberAction,
    getSubscriberAction,
    (0, import_pieces_common4.createCustomApiCallAction)({
      auth: smailyAuth,
      baseUrl: (auth) => auth ? `https://${auth.props.domain}.sendsmaily.net/api` : "",
      authMapping: async (auth) => ({
        Authorization: `Basic ${Buffer.from(
          `${auth.props.username}:${auth.props.password}`
        ).toString("base64")}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  smaily
});
