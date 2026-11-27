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

// ../ap/packages/pieces/community/sendfox/src/index.ts
var index_exports = {};
__export(index_exports, {
  sendfox: () => sendfox
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/sendfox/src/lib/actions/create-contact.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/sendfox/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), sendfoxAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `To obtain your personal token, follow these steps:
1. Log in to your SendFox account.
2. Visit https://sendfox.com/account/oauth to create one
3. From OAuth Apps click on Create New Token.
4. Enter any name you want then click create.
5. Copy and paste your token here.`,
  required: !0
});

// ../ap/packages/pieces/community/sendfox/src/common/index.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
async function getLists(accessToken) {
  let list_of_lists = [], last_page = 1;
  for (let i = 1; i <= last_page; i++) {
    let response = (await callsendfoxApi(import_pieces_common.HttpMethod.GET, `lists?page=${i}`, accessToken, void 0)).body;
    last_page = response.last_page;
    let lists = response.data;
    list_of_lists.push(lists);
  }
  return list_of_lists;
}
var sendfoxCommon = {
  lists: import_pieces_framework2.Property.Dropdown({
    auth: sendfoxAuth,
    displayName: "Lists",
    required: !1,
    refreshers: ["auth"],
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          placeholder: "connect your account first",
          options: []
        };
      let authentication = auth.secret_text;
      return authentication ? {
        disabled: !1,
        options: (await getLists(authentication)).flat().map((list) => ({
          label: list.name,
          value: list.id
        }))
      } : {
        disabled: !0,
        placeholder: "connect your account first",
        options: []
      };
    }
  })
};
async function callsendfoxApi(method, apiUrl, accessToken, body) {
  return await import_pieces_common.httpClient.sendRequest({
    method,
    url: `https://api.sendfox.com/${apiUrl}`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token: accessToken
    },
    body
  });
}

// ../ap/packages/pieces/community/sendfox/src/lib/actions/create-contact.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), createContact = (0, import_pieces_framework3.createAction)({
  name: "create-contact",
  classification: "WRITE",
  auth: sendfoxAuth,
  displayName: "Create Contact",
  description: "Create a new contact",
  audience: "both",
  aiMetadata: { description: "Adds a new contact (subscriber) to SendFox by email, optionally setting first/last name and assigning the contact to a list. Use to add or onboard a subscriber. Not idempotent: each call posts the contact, so repeating may create or re-add the entry.", idempotent: !1 },
  props: {
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email",
      required: !0
    }),
    firstname: import_pieces_framework3.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    lastname: import_pieces_framework3.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    list: sendfoxCommon.lists
  },
  async run(context) {
    let accessToken = context.auth.secret_text, email = context.propsValue.email, firstname = context.propsValue.firstname, lastname = context.propsValue.lastname, list = context.propsValue.list, request_body = { email };
    return firstname && (request_body.first_name = firstname), lastname && (request_body.last_name = lastname), list && (request_body.lists = [list]), (await callsendfoxApi(
      import_pieces_common2.HttpMethod.POST,
      "contacts",
      accessToken,
      request_body
    )).body;
  }
});

// ../ap/packages/pieces/community/sendfox/src/lib/actions/create-list.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), createList = (0, import_pieces_framework4.createAction)({
  name: "create-list",
  classification: "WRITE",
  auth: sendfoxAuth,
  displayName: "Create List",
  description: "Create a new list",
  audience: "both",
  aiMetadata: { description: "Creates a new contact list in SendFox with the given name. Use to set up a list before adding subscribers to it. Not idempotent: each call creates a new list even if one with the same name already exists.", idempotent: !1 },
  props: {
    task_name: import_pieces_framework4.Property.ShortText({
      displayName: "List Name",
      required: !0
    })
  },
  async run(context) {
    let accessToken = context.auth.secret_text, task_name = context.propsValue.task_name;
    return [(await callsendfoxApi(import_pieces_common3.HttpMethod.POST, "lists", accessToken, {
      name: task_name
    })).body];
  }
});

// ../ap/packages/pieces/community/sendfox/src/lib/actions/unsubscribe-contact.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common"), unsubscribe = (0, import_pieces_framework5.createAction)({
  name: "unsubscribe",
  classification: "DESTRUCTIVE",
  auth: sendfoxAuth,
  displayName: "Unsubscribe Contact",
  description: "Unsubscribe a contact",
  audience: "both",
  aiMetadata: { description: "Unsubscribes a contact from SendFox by email so they stop receiving emails. Use to honor an opt-out or remove someone from sends. Effectively idempotent in outcome (the contact ends up unsubscribed), but flagged not idempotent because each call issues a state-changing request.", idempotent: !1 },
  props: {
    email: import_pieces_framework5.Property.ShortText({
      displayName: "Email",
      required: !0
    })
  },
  async run(context) {
    let accessToken = context.auth.secret_text, email = context.propsValue.email;
    return [(await callsendfoxApi(import_pieces_common4.HttpMethod.PATCH, "unsubscribe", accessToken, {
      email
    })).body];
  }
});

// ../ap/packages/pieces/community/sendfox/src/index.ts
var sendfox = (0, import_pieces_framework6.createPiece)({
  displayName: "SendFox",
  description: "Email marketing made simple",
  auth: sendfoxAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/sendfox.png",
  categories: [import_pieces_framework7.PieceCategory.MARKETING],
  authors: ["Salem-Alaa", "kishanprmr", "MoShizzle", "abuaboud"],
  actions: [
    createList,
    unsubscribe,
    createContact,
    (0, import_pieces_common5.createCustomApiCallAction)({
      baseUrl: () => "https://api.sendfox.com",
      auth: sendfoxAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  sendfox
});
