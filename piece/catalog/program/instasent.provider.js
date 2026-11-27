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

// ../ap/packages/pieces/community/instasent/src/index.ts
var index_exports = {};
__export(index_exports, {
  getBaseUrl: () => getBaseUrl2,
  instasent: () => instasent
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/instasent/src/lib/actions/delete-contact.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/instasent/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/instasent/src/lib/common/constants.ts
var IGNORED_ATTRIBUTES = ["_email_verified", "_phone_mobile_verified"], BASE_URL = "https://api.instasent.com/v1", BOOLEAN_OPTIONS = [
  { label: "True", value: "1" },
  { label: "False", value: "0" },
  { label: "Unknown / Not set", value: null }
], LONG_TEXT_TYPES = ["text", "payload", "string"];

// ../ap/packages/pieces/community/instasent/src/lib/auth.ts
var authDescriptionMarkdown = `
## Obtain your auth data
1. Go to https://dashboard.instasent.com
2. Access to your project
3. Create an Activepieces data source
4. Copy the auth parameters and paste them in the fields below
`;
function getBaseUrl(auth) {
  return `${BASE_URL}/project/${auth.projectId}/datasource/${auth.datasourceId}`;
}
var instasentAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: authDescriptionMarkdown,
  props: {
    projectId: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Project ID",
      description: "Your Instasent Project ID",
      required: !0
    }),
    datasourceId: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Datasource ID",
      description: "Your Instasent Datasource ID",
      required: !0
    }),
    apiKey: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Key",
      description: "Your Instasent API Bearer Token",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    let authData = auth;
    try {
      let baseUrl = getBaseUrl(authData), data = (await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${baseUrl}/stream`,
        headers: {
          Authorization: `Bearer ${auth.apiKey}`
        }
      })).body;
      return !data.organization || !data.stream || !data.datasource || !data.project ? {
        valid: !1,
        error: "Invalid API response structure"
      } : {
        valid: !0
      };
    } catch (error) {
      let response = error.response, data = response == null ? void 0 : response.data;
      return {
        valid: !1,
        error: (data == null ? void 0 : data.message) || "Invalid credentials or connection error"
      };
    }
  },
  required: !0
});

// ../ap/packages/pieces/community/instasent/src/lib/actions/delete-contact.ts
var deleteContact = (0, import_pieces_framework2.createAction)({
  name: "delete_contact",
  displayName: "Delete Contact",
  description: "Delete a single contact by User ID",
  audience: "both",
  aiMetadata: { description: "Deletes one contact from an Instasent datasource by its User ID. Use to remove a contact you can identify by stable User ID. Idempotent: deleting an already-removed contact leaves the same end state.", idempotent: !0 },
  auth: instasentAuth,
  props: {
    userId: import_pieces_framework2.Property.ShortText({
      displayName: "User ID",
      description: "Unique identifier of the contact to delete",
      required: !0
    })
  },
  async run(context) {
    let { userId } = context.propsValue, auth = context.auth, baseUrl = getBaseUrl2({ projectId: auth.props.projectId, datasourceId: auth.props.datasourceId });
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.DELETE,
      url: `${baseUrl}/stream/contacts/${userId}`,
      headers: {
        Authorization: `Bearer ${auth.props.apiKey}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/instasent/src/lib/actions/add-or-update-contact.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var PROPERTY_ITERATIONS = [
  { check: (spec) => spec.requiredInWebhook },
  { check: (spec) => spec.important },
  { check: (spec) => spec.visible !== !1 },
  { check: (spec) => !spec.custom },
  { check: () => !0 }
], addOrUpdateContact = (0, import_pieces_framework3.createAction)({
  name: "add_or_update_contact",
  displayName: "Add/Update contact",
  description: "Add or update a single contact",
  audience: "both",
  aiMetadata: { description: "Upserts one contact in an Instasent datasource, keyed on the contact User ID (mandatory) \u2014 creates it if new, updates it otherwise. Use to push or sync contact attributes; the available attribute fields are loaded dynamically from the datasource. Enable the Instant option only when a following step must add an event for this contact (it processes synchronously instead of queuing). Idempotent: re-running with the same User ID and data converges to the same contact.", idempotent: !0 },
  auth: instasentAuth,
  props: {
    contact: import_pieces_framework3.Property.DynamicProperties({
      auth: instasentAuth,
      displayName: "Contact Properties",
      description: "Enter the contact properties, the User ID is mandatory",
      required: !0,
      refreshers: ["authentication"],
      props: async ({ auth }) => {
        if (!auth)
          return {};
        let authData = auth, baseUrl = getBaseUrl2({
          projectId: authData.props.projectId,
          datasourceId: authData.props.datasourceId
        });
        try {
          let response = await import_pieces_common3.httpClient.sendRequest({
            method: import_pieces_common3.HttpMethod.GET,
            url: `${baseUrl}/stream/specs/attributes`,
            headers: {
              Authorization: `Bearer ${authData.props.apiKey}`
            }
          }), properties = {};
          for (let iteration of PROPERTY_ITERATIONS)
            for (let spec of response.body.specs) {
              if (spec.readOnly || properties[spec.uid] || IGNORED_ATTRIBUTES.includes(spec.uid) || !iteration.check(spec))
                continue;
              let displayLabel = spec.displayLabel, description = spec.description;
              if (spec.dataType === "date" && (description += " (ISO 8601 format YYYY-MM-DD)"), spec.multivalue > 1)
                properties[spec.uid] = import_pieces_framework3.Property.Array({
                  displayName: displayLabel,
                  description: `${description} (Max ${spec.multivalue} values)`,
                  required: spec.requiredInWebhook
                });
              else if (spec.dataType === "bool")
                properties[spec.uid] = import_pieces_framework3.Property.StaticDropdown({
                  displayName: displayLabel,
                  description: `${description} [0=false|1=true|null=unknown]`,
                  required: spec.requiredInWebhook,
                  options: {
                    options: BOOLEAN_OPTIONS
                  }
                });
              else {
                let PropType = LONG_TEXT_TYPES.includes(spec.dataType) ? import_pieces_framework3.Property.LongText : import_pieces_framework3.Property.ShortText;
                properties[spec.uid] = PropType({
                  displayName: displayLabel,
                  description,
                  required: spec.requiredInWebhook
                });
              }
            }
          return properties;
        } catch (error) {
          throw new Error("Failed to load contact properties");
        }
      }
    }),
    instant: import_pieces_framework3.Property.Checkbox({
      displayName: "Instant",
      description: "Process contact immediately instead of queuing. Only enable this when you need to add an event for this contact in the next step. Not recommended for high-volume operations.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let contact = context.propsValue.contact, instant = context.propsValue.instant, auth = context.auth, baseUrl = getBaseUrl2({ projectId: auth.props.projectId, datasourceId: auth.props.datasourceId }), response = await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: `${baseUrl}/stream/contacts${instant ? "?_sync" : ""}`,
      headers: {
        Authorization: `Bearer ${auth.props.apiKey}`,
        "Content-Type": "application/json"
      },
      body: [contact]
    });
    if (response.body.entitiesSuccess !== 1)
      throw new Error(`Failed to add or update contact: ${JSON.stringify(response.body.errors)}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/instasent/src/lib/actions/create-event.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var createEvent = (0, import_pieces_framework4.createAction)({
  name: "add_event",
  displayName: "Add Event",
  description: "Add a contact event",
  audience: "both",
  aiMetadata: { description: "Records a contact event in Instasent for a given User ID and event type (selected from the datasource event specs), with event-type-specific parameters loaded dynamically. Use to track an activity or behavior against a contact. Requires an Event ID, which the platform uses to deduplicate \u2014 sending the same Event ID again will not create a duplicate, so the call is idempotent on Event ID.", idempotent: !0 },
  auth: instasentAuth,
  props: {
    user_id: import_pieces_framework4.Property.ShortText({
      displayName: "User ID",
      description: "Unique identifier of the user",
      required: !0
    }),
    event_id: import_pieces_framework4.Property.ShortText({
      displayName: "Event ID",
      description: "Unique identifier for this event. Used for deduplication.",
      required: !0
    }),
    event_date: import_pieces_framework4.Property.ShortText({
      displayName: "Event Date",
      description: "Date and time when the event occurred, will default to now (ISO 8601 format YYYY-MM-DDTHH:MM:SS.SSSZ)",
      required: !1
    }),
    event_type: import_pieces_framework4.Property.Dropdown({
      auth: instasentAuth,
      displayName: "Event Type",
      description: "Select the type of event to create",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let authData = auth, baseUrl = getBaseUrl2({
          projectId: authData.props.projectId,
          datasourceId: authData.props.datasourceId
        });
        return {
          options: (await import_pieces_common4.httpClient.sendRequest({
            method: import_pieces_common4.HttpMethod.GET,
            url: `${baseUrl}/stream/specs/events`,
            headers: {
              Authorization: `Bearer ${authData.props.apiKey}`
            }
          })).body.specs.map((spec) => ({
            label: `${spec.emoji} ${spec.name}`,
            value: spec.uid
          }))
        };
      }
    }),
    event_parameters: import_pieces_framework4.Property.DynamicProperties({
      auth: instasentAuth,
      displayName: "Event Parameters",
      description: "Parameters for the selected event type",
      required: !0,
      refreshers: ["event_type"],
      props: async ({ auth, event_type }) => {
        if (!auth || !event_type) return {};
        let authData = auth, baseUrl = getBaseUrl2({
          projectId: authData.props.projectId,
          datasourceId: authData.props.datasourceId
        }), response = await import_pieces_common4.httpClient.sendRequest({
          method: import_pieces_common4.HttpMethod.GET,
          url: `${baseUrl}/stream/specs/event-parameters/${event_type}`,
          headers: {
            Authorization: `Bearer ${authData.props.apiKey}`
          }
        }), props = {};
        return response.body.specs.forEach((param) => {
          if (param.multiValue > 1)
            props[param.parameter] = import_pieces_framework4.Property.Array({
              displayName: param.title,
              description: `${param.description} (Max ${param.multiValue} values)`,
              required: param.required
            });
          else
            switch (param.dataType) {
              case "bool":
                props[param.parameter] = import_pieces_framework4.Property.StaticDropdown({
                  displayName: param.title,
                  description: `${param.description} [0=false|1=true|null=unknown]`,
                  required: param.required,
                  options: {
                    options: BOOLEAN_OPTIONS
                  }
                });
                break;
              case "string":
              case "payload":
                props[param.parameter] = import_pieces_framework4.Property.LongText({
                  displayName: param.title,
                  description: param.description,
                  required: param.required
                });
                break;
              default:
                props[param.parameter] = import_pieces_framework4.Property.ShortText({
                  displayName: param.title,
                  description: param.description,
                  required: param.required
                });
                break;
            }
        }), props;
      }
    })
  },
  async run({ auth, propsValue }) {
    let authData = auth, baseUrl = getBaseUrl2({
      projectId: authData.props.projectId,
      datasourceId: authData.props.datasourceId
    }), eventData = {
      _user_id: propsValue.user_id,
      _event_id: propsValue.event_id,
      _event_type: propsValue.event_type,
      _event_date: propsValue.event_date,
      _event_parameters: propsValue.event_parameters
    }, response = await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: `${baseUrl}/stream/events`,
      headers: {
        Authorization: `Bearer ${authData.props.apiKey}`,
        "Content-Type": "application/json"
      },
      body: [eventData]
    });
    if (response.body.entitiesSuccess !== 1)
      throw new Error(`Failed to create event: ${JSON.stringify(response.body.errors)}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/instasent/src/index.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var getBaseUrl2 = (auth) => `${BASE_URL}/project/${auth.projectId}/datasource/${auth.datasourceId}`;
var instasent = (0, import_pieces_framework5.createPiece)({
  displayName: "Instasent",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/instasent.jpg",
  categories: [import_pieces_framework6.PieceCategory.MARKETING],
  authors: ["dev-instasent", "https://github.com/dev-instasent"],
  auth: instasentAuth,
  actions: [addOrUpdateContact, deleteContact, createEvent],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  getBaseUrl,
  instasent
});
