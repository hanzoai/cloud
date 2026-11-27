"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __hasOwnProp = Object.prototype.hasOwnProperty;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value;
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
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key != "symbol" ? key + "" : key, value);

// ../ap/packages/pieces/community/certopus/src/index.ts
var index_exports = {};
__export(index_exports, {
  certopus: () => certopus
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/certopus/src/lib/actions/create-credential.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/certopus/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
var CertopusClient = class {
  constructor(token) {
    __publicField(this, "token", token);
  }
  async makeRequest(method, url, query, body) {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: certopusCommon.baseUrl + url,
      queryParams: query,
      body,
      headers: {
        "x-api-key": this.token
      }
    })).body.data;
  }
  listOrganisations() {
    return this.makeRequest(import_pieces_common.HttpMethod.GET, "/organisations");
  }
  listEvents(organisationId) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      `/events/${organisationId}`
    );
  }
  listCategories(organisationId, eventId) {
    return this.makeRequest(import_pieces_common.HttpMethod.GET, "/categories", {
      organisationId,
      eventId
    });
  }
  listRecipientFields(organisationId, eventId, categoryId) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/recipient_fields",
      {
        organisationId,
        eventId,
        categoryId
      }
    );
  }
};

// ../ap/packages/pieces/community/certopus/src/lib/common/index.ts
function makeClient(apiKey) {
  return new CertopusClient(apiKey);
}
var certopusCommon = {
  baseUrl: "https://api.certopus.com/v1"
};

// ../ap/packages/pieces/community/certopus/src/lib/actions/create-credential.ts
var import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/certopus/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), certopusAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "API key acquired from your Certopus profile"
});

// ../ap/packages/pieces/community/certopus/src/lib/actions/create-credential.ts
var createCredential = (0, import_pieces_framework2.createAction)({
  auth: certopusAuth,
  name: "create_credential",
  classification: "WRITE",
  displayName: "Create Credential",
  description: "Create a credential",
  audience: "both",
  aiMetadata: {
    description: "Issues a Certopus certificate/credential for a single recipient under a given organisation, event, and category, supplying the recipient email plus the category-specific data fields. Use to grant a certificate to one person; the generate and publish flags control whether Certopus immediately produces and publishes it. Not idempotent \u2014 each call creates a new credential, so repeating it issues duplicates.",
    idempotent: !1
  },
  props: {
    organisation: import_pieces_framework2.Property.Dropdown({
      auth: certopusAuth,
      displayName: "Organisations",
      refreshers: [],
      required: !0,
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await makeClient(auth.secret_text).listOrganisations()).map((x) => ({
          label: x.name,
          value: x.id
        }))
      } : {
        disabled: !0,
        options: [],
        placeholder: "Please enter your API key first."
      }
    }),
    event: import_pieces_framework2.Property.Dropdown({
      auth: certopusAuth,
      displayName: "Event",
      refreshers: ["organisation"],
      required: !0,
      options: async ({ auth, organisation }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please enter your API key first."
          };
        if (!organisation)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please select an organisation first."
          };
        let res = await makeClient(auth.secret_text).listEvents(organisation);
        return console.log(res), {
          disabled: !1,
          options: res.map((x) => ({
            label: x.title,
            value: x.id
          }))
        };
      }
    }),
    category: import_pieces_framework2.Property.Dropdown({
      auth: certopusAuth,
      displayName: "Category",
      refreshers: ["organisation", "event"],
      required: !0,
      options: async ({ auth, organisation, event }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please enter your API key first."
          };
        if (!organisation)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please select an organisation first."
          };
        if (!event)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please select an event first."
          };
        let res = await makeClient(auth.secret_text).listCategories(
          organisation,
          event
        );
        return console.log(res), {
          disabled: !1,
          options: res.map((x) => ({
            label: x.title,
            value: x.id
          }))
        };
      }
    }),
    generate: import_pieces_framework2.Property.Checkbox({
      displayName: "Generate",
      description: "Automatically generate the crentials",
      defaultValue: !1,
      required: !0
    }),
    publish: import_pieces_framework2.Property.Checkbox({
      displayName: "Publish",
      description: "Automatically publish the crentials",
      defaultValue: !1,
      required: !0
    }),
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Recipient Email",
      description: "Email address of the recipient",
      required: !0
    }),
    fields: import_pieces_framework2.Property.DynamicProperties({
      auth: certopusAuth,
      displayName: "Recipient Data",
      required: !0,
      refreshers: ["organisation", "event", "category"],
      props: async ({ auth, organisation, event, category }) => {
        if (!auth) return {};
        if (!organisation) return {};
        if (!event) return {};
        if (!category) return {};
        let fields = {};
        try {
          (await makeClient(auth.secret_text).listRecipientFields(
            organisation.toString(),
            event.toString(),
            category.toString()
          )).forEach((field) => {
            if (field.key !== "email") {
              let params = {
                displayName: field.label,
                description: `Enter data for the ${field.label} field`,
                required: !0
              };
              fields[field.key] = import_pieces_framework2.Property.ShortText(params);
            }
          });
        } catch (e) {
          console.debug(e);
        }
        return fields;
      }
    })
  },
  async run(context) {
    let { organisation, event, category, publish, generate, email, fields } = context.propsValue, request = {
      method: import_pieces_common2.HttpMethod.POST,
      url: `${certopusCommon.baseUrl}/certificates`,
      body: {
        organisationId: organisation,
        eventId: event,
        categoryId: category,
        publish,
        generate,
        recipients: [
          {
            email,
            data: fields
          }
        ]
      },
      headers: {
        "x-api-key": context.auth.secret_text
      },
      queryParams: {}
    };
    return (await import_pieces_common2.httpClient.sendRequest(request)).body;
  }
});

// ../ap/packages/pieces/community/certopus/src/index.ts
var certopus = (0, import_pieces_framework3.createPiece)({
  displayName: "Certopus",
  description: "Your certificates, made simple",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/certopus.png",
  categories: [],
  authors: ["VrajGohil", "kishanprmr", "MoShizzle", "khaledmashaly", "abuaboud"],
  auth: certopusAuth,
  actions: [
    createCredential,
    (0, import_pieces_common3.createCustomApiCallAction)({
      baseUrl: () => certopusCommon.baseUrl,
      // Replace with the actual base URL
      auth: certopusAuth,
      authMapping: async (auth) => ({
        "x-api-key": `${auth}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  certopus
});
