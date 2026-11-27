"use strict";
var __defProp = Object.defineProperty, __defProps = Object.defineProperties, __getOwnPropDesc = Object.getOwnPropertyDescriptor, __getOwnPropDescs = Object.getOwnPropertyDescriptors, __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
}, __spreadProps = (a, b) => __defProps(a, __getOwnPropDescs(b));
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

// ../ap/packages/pieces/community/capsule-crm/src/index.ts
var index_exports = {};
__export(index_exports, {
  capsuleCrm: () => capsuleCrm
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework14 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/capsule-crm/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), capsuleCrmAuth = import_pieces_framework.PieceAuth.OAuth2({
  description: `
  To authenticate with Capsule CRM:
  1. Go to your Capsule CRM user settings.
  2. Navigate to "My Preferences" > "API Authentication Tokens".
  3. Register a new application to get a Client ID and Client Secret.
  4. Add https://cloud.activepieces.com/redirect to the authorized redirect URIs.
  5. Use the OAuth2 flow below.`,
  authUrl: "https://api.capsulecrm.com/oauth/authorise",
  tokenUrl: "https://api.capsulecrm.com/oauth/token",
  required: !0,
  scope: ["read", "write"],
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: "https://api.capsulecrm.com/api/v2/site",
        authentication: {
          type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
          token: auth.access_token
        }
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid credentials"
      };
    }
  }
});

// ../ap/packages/pieces/community/capsule-crm/src/lib/actions/create-contact.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/capsule-crm/src/lib/common/client.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), capsuleCrmClient = {
  async createContact(auth, params) {
    let partyData = {
      type: params.type
    };
    params.type === "person" ? (partyData.firstName = params.firstName, partyData.lastName = params.lastName) : partyData.name = params.name, params.title && (partyData.title = params.title), params.jobTitle && (partyData.jobTitle = params.jobTitle), params.about && (partyData.about = params.about), params.organisationId && (partyData.organisation = { id: params.organisationId }), params.ownerId && (partyData.owner = { id: params.ownerId }), params.teamId && (partyData.team = { id: params.teamId }), params.tags && (partyData.tags = params.tags.map((tag) => ({ name: tag }))), params.fields && (partyData.fields = params.fields), params.emailAddresses && (partyData.emailAddresses = params.emailAddresses), params.phoneNumbers && (partyData.phoneNumbers = params.phoneNumbers), params.addresses && (partyData.addresses = params.addresses), params.websites && (partyData.websites = params.websites);
    let request = {
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://api.capsulecrm.com/api/v2/parties",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      },
      body: {
        party: partyData
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(request)).body;
  },
  async searchContacts(auth, searchTerm) {
    let isSearch = searchTerm && searchTerm.length > 0, url = isSearch ? "https://api.capsulecrm.com/api/v2/parties/search" : "https://api.capsulecrm.com/api/v2/parties", queryParams = {};
    isSearch && (queryParams.q = searchTerm);
    let request = {
      method: import_pieces_common2.HttpMethod.GET,
      url,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      },
      queryParams
    };
    return (await import_pieces_common2.httpClient.sendRequest(
      request
    )).body.parties;
  },
  async searchOrganisations(auth, searchTerm) {
    try {
      let url = searchTerm ? "https://api.capsulecrm.com/api/v2/parties/search" : "https://api.capsulecrm.com/api/v2/parties", queryParams = {
        perPage: "100"
      };
      searchTerm && (queryParams.q = searchTerm);
      let request = {
        method: import_pieces_common2.HttpMethod.GET,
        url,
        authentication: {
          type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
          token: auth.access_token
        },
        queryParams
      }, response = await import_pieces_common2.httpClient.sendRequest(
        request
      );
      if (!response.body || !response.body.parties)
        return [];
      console.log("Parties response:", response.body.parties);
      let organisations = response.body.parties.filter(
        (party) => party.type === "organisation"
      );
      return console.log("Filtered organisations:", organisations), organisations;
    } catch (error) {
      return console.error("searchOrganisations error:", error), [];
    }
  },
  async listTeams(auth) {
    let request = {
      method: import_pieces_common2.HttpMethod.GET,
      url: "https://api.capsulecrm.com/api/v2/teams",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(request)).body.teams;
  },
  async listTags(auth) {
    let request = {
      method: import_pieces_common2.HttpMethod.GET,
      url: "https://api.capsulecrm.com/api/v2/parties/tags",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(request)).body.tags;
  },
  async listCustomFields(auth) {
    let request = {
      method: import_pieces_common2.HttpMethod.GET,
      url: "https://api.capsulecrm.com/api/v2/parties/fields/definitions",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(request)).body.definitions;
  },
  async updateContact(auth, partyId, params) {
    let partyData = {};
    params.firstName && (partyData.firstName = params.firstName), params.lastName && (partyData.lastName = params.lastName), params.name && (partyData.name = params.name), params.title && (partyData.title = params.title), params.about && (partyData.about = params.about), params.ownerId && (partyData.owner = { id: params.ownerId }), params.teamId && (partyData.team = { id: params.teamId }), params.addresses && (partyData.addresses = params.addresses), params.websites && (partyData.websites = params.websites), params.emailAddresses && (partyData.emailAddresses = params.emailAddresses), params.phoneNumbers && (partyData.phoneNumbers = params.phoneNumbers);
    let request = {
      method: import_pieces_common2.HttpMethod.PUT,
      url: `https://api.capsulecrm.com/api/v2/parties/${partyId}`,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      },
      body: {
        party: partyData
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(request)).body;
  },
  async listMilestones(auth) {
    let request = {
      method: import_pieces_common2.HttpMethod.GET,
      url: "https://api.capsulecrm.com/api/v2/milestones",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(
      request
    )).body.milestones;
  },
  async searchOpportunities(auth) {
    let request = {
      method: import_pieces_common2.HttpMethod.GET,
      url: "https://api.capsulecrm.com/api/v2/opportunities",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(request)).body.opportunities;
  },
  async getOpportunity(auth, opportunityId) {
    let request = {
      method: import_pieces_common2.HttpMethod.GET,
      url: `https://api.capsulecrm.com/api/v2/opportunities/${opportunityId}`,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      },
      queryParams: {
        embed: "tags,fields"
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(
      request
    )).body.opportunity;
  },
  async createOpportunity(auth, params) {
    let request = {
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://api.capsulecrm.com/api/v2/opportunities",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      },
      body: {
        opportunity: params
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(
      request
    )).body.opportunity;
  },
  async listOpportunities(auth) {
    let request = {
      method: import_pieces_common2.HttpMethod.GET,
      url: "https://api.capsulecrm.com/api/v2/opportunities",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(request)).body.opportunities;
  },
  async updateOpportunity(auth, opportunityId, params) {
    let request = {
      method: import_pieces_common2.HttpMethod.PUT,
      url: `https://api.capsulecrm.com/api/v2/opportunities/${opportunityId}`,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      },
      body: {
        opportunity: params
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(
      request
    )).body.opportunity;
  },
  async listCategories(auth) {
    let request = {
      method: import_pieces_common2.HttpMethod.GET,
      url: "https://api.capsulecrm.com/api/v2/categories",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(
      request
    )).body.categories;
  },
  async searchProjects(auth) {
    let request = {
      method: import_pieces_common2.HttpMethod.GET,
      url: "https://api.capsulecrm.com/api/v2/kases",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(request)).body.kases;
  },
  async subscribeRestHook(auth, params) {
    let request = {
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://api.capsulecrm.com/api/v2/resthooks",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      },
      body: {
        restHook: params
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(
      request
    )).body.restHook;
  },
  async unsubscribeRestHook(auth, hookId) {
    let request = {
      method: import_pieces_common2.HttpMethod.DELETE,
      url: `https://api.capsulecrm.com/api/v2/resthooks/${hookId}`,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      }
    };
    await import_pieces_common2.httpClient.sendRequest(request);
  },
  async listActivityTypes(auth) {
    let request = {
      method: import_pieces_common2.HttpMethod.GET,
      url: "https://api.capsulecrm.com/api/v2/activitytypes",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(request)).body.activityTypes;
  },
  async findContact(auth, term) {
    let request = {
      method: import_pieces_common2.HttpMethod.GET,
      url: "https://api.capsulecrm.com/api/v2/parties/search",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      },
      queryParams: {
        q: term
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(
      request
    )).body.parties;
  },
  async filterOpportunities(auth, filter) {
    let request = {
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://api.capsulecrm.com/api/v2/opportunities/filters/results",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      },
      body: {
        filter
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(request)).body.opportunities;
  },
  async filterProjects(auth, filter) {
    let request = {
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://api.capsulecrm.com/api/v2/kases/filters/results",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      },
      body: {
        filter
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(
      request
    )).body.kases;
  },
  async listStages(auth) {
    let request = {
      method: import_pieces_common2.HttpMethod.GET,
      url: "https://api.capsulecrm.com/api/v2/stages",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(request)).body.stages;
  },
  async createProject(auth, params) {
    let request = {
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://api.capsulecrm.com/api/v2/kases",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      },
      body: {
        kase: params
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(request)).body.kase;
  },
  async listProjects(auth) {
    let request = {
      method: import_pieces_common2.HttpMethod.GET,
      url: "https://api.capsulecrm.com/api/v2/kases",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(
      request
    )).body.kases;
  },
  async listCases(auth) {
    let request = {
      method: import_pieces_common2.HttpMethod.GET,
      url: "https://api.capsulecrm.com/api/v2/kases",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(request)).body.kases;
  },
  async listUsers(auth) {
    let request = {
      method: import_pieces_common2.HttpMethod.GET,
      url: "https://api.capsulecrm.com/api/v2/users",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(request)).body.users;
  },
  async createEntry(auth, params) {
    let request = {
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://api.capsulecrm.com/api/v2/entries",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      },
      body: {
        entry: params
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(request)).body.entry;
  },
  async createTask(auth, params) {
    let request = {
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://api.capsulecrm.com/api/v2/tasks",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      },
      body: {
        task: params
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(request)).body.task;
  },
  async createNote(auth, params) {
    let entryData = {
      type: "Note",
      content: params.content
    };
    params.partyId && (entryData.party = { id: params.partyId }), params.opportunityId && (entryData.opportunity = { id: params.opportunityId }), params.caseId && (entryData.case = { id: params.caseId }), params.projectId && (entryData.project = { id: params.projectId });
    let request = {
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://api.capsulecrm.com/api/v2/entries",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      },
      body: {
        entry: entryData
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(request)).body;
  },
  async getContact(auth, contactId) {
    try {
      let request = {
        method: import_pieces_common2.HttpMethod.GET,
        url: `https://api.capsulecrm.com/api/v2/parties/${contactId}`,
        authentication: {
          type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
          token: auth.access_token
        },
        queryParams: {
          embed: "addresses,phoneNumbers,websites,emailAddresses"
        }
      };
      return (await import_pieces_common2.httpClient.sendRequest(request)).body.party;
    } catch (e) {
      return null;
    }
  },
  async findProject(auth, params) {
    let request = {
      method: import_pieces_common2.HttpMethod.GET,
      url: "https://api.capsulecrm.com/api/v2/kases",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      },
      queryParams: {
        q: params.searchTerm
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(
      request
    )).body.kases;
  },
  async getProject(auth, projectId) {
    try {
      let request = {
        method: import_pieces_common2.HttpMethod.GET,
        url: `https://api.capsulecrm.com/api/v2/kases/${projectId}`,
        authentication: {
          type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
          token: auth.access_token
        }
      };
      return (await import_pieces_common2.httpClient.sendRequest(request)).body.kase;
    } catch (e) {
      return null;
    }
  },
  async findOpportunity(auth, params) {
    let request = {
      method: import_pieces_common2.HttpMethod.GET,
      url: "https://api.capsulecrm.com/api/v2/opportunities/search",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      },
      queryParams: {
        q: params.searchTerm
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(request)).body.opportunities;
  },
  async subscribeWebhook(auth, url, event) {
    let request = {
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://api.capsulecrm.com/api/v2/webhooks",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      },
      body: {
        webhook: {
          url,
          event
        }
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(
      request
    )).body.webhook;
  },
  async unsubscribeWebhook(auth, webhookId) {
    let request = {
      method: import_pieces_common2.HttpMethod.DELETE,
      url: `https://api.capsulecrm.com/api/v2/webhooks/${webhookId}`,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      }
    };
    await import_pieces_common2.httpClient.sendRequest(request);
  }
};

// ../ap/packages/pieces/community/capsule-crm/src/lib/actions/create-contact.ts
var createContactAction = (0, import_pieces_framework2.createAction)({
  auth: capsuleCrmAuth,
  name: "create_contact",
  classification: "WRITE",
  displayName: "Create Contact",
  description: "Create a new Person or Organisation in Capsule CRM.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new contact record in Capsule CRM. Set the contact type to either a person (requires first and last name, optional linked organisation) or an organisation (requires organisation name); the type selection switches which name fields apply. Use to add a fresh contact you have confirmed does not already exist. Not idempotent: each call creates a separate record, so repeating it produces duplicate contacts.",
    idempotent: !1
  },
  props: {
    type: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Contact Type",
      description: "The type of contact to create.",
      required: !0,
      options: {
        options: [
          { label: "Person", value: "person" },
          { label: "Organisation", value: "organisation" }
        ]
      }
    }),
    contactFields: import_pieces_framework2.Property.DynamicProperties({
      auth: capsuleCrmAuth,
      displayName: "Details",
      required: !0,
      refreshers: ["type"],
      props: async ({ auth, type }) => {
        let contactType = type, fields = {};
        if (contactType === "person") {
          let organisationOptions = [];
          if (auth)
            try {
              organisationOptions = (await capsuleCrmClient.searchContacts(
                auth,
                ""
              )).filter((party) => party.type === "organisation").map((org) => ({
                label: org.name || "Unnamed Organisation",
                value: org.id
              }));
            } catch (error) {
              console.error("Failed to load organisations:", error);
            }
          fields.firstName = import_pieces_framework2.Property.ShortText({
            displayName: "First Name",
            required: !0
          }), fields.lastName = import_pieces_framework2.Property.ShortText({
            displayName: "Last Name",
            required: !0
          }), fields.title = import_pieces_framework2.Property.ShortText({
            displayName: "Title",
            required: !1
          }), fields.jobTitle = import_pieces_framework2.Property.ShortText({
            displayName: "Job Title",
            required: !1
          }), fields.organisationId = import_pieces_framework2.Property.StaticDropdown({
            displayName: "Organisation",
            required: !1,
            options: {
              options: organisationOptions
            }
          });
        } else contactType === "organisation" && (fields.organisationName = import_pieces_framework2.Property.ShortText({
          displayName: "Organisation Name",
          required: !0
        }));
        return fields;
      }
    }),
    about: import_pieces_framework2.Property.LongText({
      displayName: "About",
      required: !1
    }),
    ownerId: import_pieces_framework2.Property.Dropdown({
      auth: capsuleCrmAuth,
      displayName: "Owner",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        options: (await capsuleCrmClient.listUsers(
          auth
        )).map((user) => ({
          label: user.name,
          value: user.id
        }))
      } : { options: [] }
    }),
    teamId: import_pieces_framework2.Property.Dropdown({
      auth: capsuleCrmAuth,
      displayName: "Team",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        options: (await capsuleCrmClient.listTeams(
          auth
        )).map((team) => ({
          label: team.name,
          value: team.id
        }))
      } : { options: [] }
    }),
    tags: import_pieces_framework2.Property.MultiSelectDropdown({
      auth: capsuleCrmAuth,
      displayName: "Tags",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        options: (await capsuleCrmClient.listTags(auth)).map((tag) => ({
          label: tag.name,
          value: tag.name
        }))
      } : { options: [] }
    }),
    customFields: import_pieces_framework2.Property.DynamicProperties({
      auth: capsuleCrmAuth,
      displayName: "Custom Fields",
      required: !0,
      refreshers: [],
      props: async ({ auth }) => {
        var _a;
        let fields = {};
        if (!auth) return fields;
        let customFields = await capsuleCrmClient.listCustomFields(
          auth
        );
        for (let field of customFields)
          switch (field.type) {
            case "list":
              fields[field.id] = import_pieces_framework2.Property.StaticDropdown({
                displayName: field.name,
                required: !1,
                options: {
                  options: ((_a = field.options) == null ? void 0 : _a.map((option) => ({
                    label: option,
                    value: option
                  }))) || []
                }
              });
              break;
            case "boolean":
              fields[field.id] = import_pieces_framework2.Property.Checkbox({
                displayName: field.name,
                required: !1
              });
              break;
            case "date":
              fields[field.id] = import_pieces_framework2.Property.DateTime({
                displayName: field.name,
                required: !1
              });
              break;
            default:
              fields[field.id] = import_pieces_framework2.Property.ShortText({
                displayName: field.name,
                required: !1
              });
          }
        return fields;
      }
    }),
    emailAddresses: import_pieces_framework2.Property.Array({
      displayName: "Email Addresses",
      required: !1,
      properties: {
        type: import_pieces_framework2.Property.StaticDropdown({
          displayName: "Type",
          required: !1,
          options: {
            options: [
              { label: "Home", value: "Home" },
              { label: "Work", value: "Work" }
            ]
          }
        }),
        address: import_pieces_framework2.Property.ShortText({
          displayName: "Address",
          required: !0
        })
      }
    }),
    phoneNumbers: import_pieces_framework2.Property.Array({
      displayName: "Phone Numbers",
      required: !1,
      properties: {
        type: import_pieces_framework2.Property.StaticDropdown({
          displayName: "Type",
          required: !1,
          options: {
            options: [
              { label: "Home", value: "Home" },
              { label: "Work", value: "Work" },
              { label: "Mobile", value: "Mobile" },
              { label: "Fax", value: "Fax" },
              { label: "Direct", value: "Direct" }
            ]
          }
        }),
        number: import_pieces_framework2.Property.ShortText({
          displayName: "Number",
          required: !0
        })
      }
    }),
    addresses: import_pieces_framework2.Property.Array({
      displayName: "Addresses",
      required: !1,
      properties: {
        type: import_pieces_framework2.Property.StaticDropdown({
          displayName: "Type",
          required: !1,
          options: {
            options: [
              { label: "Home", value: "Home" },
              { label: "Postal", value: "Postal" },
              { label: "Office", value: "Office" },
              { label: "Billing", value: "Billing" },
              { label: "Shipping", value: "Shipping" }
            ]
          }
        }),
        street: import_pieces_framework2.Property.ShortText({
          displayName: "Street",
          required: !1
        }),
        city: import_pieces_framework2.Property.ShortText({
          displayName: "City",
          required: !1
        }),
        state: import_pieces_framework2.Property.ShortText({
          displayName: "State",
          required: !1
        }),
        country: import_pieces_framework2.Property.ShortText({
          displayName: "Country",
          required: !1
        }),
        zip: import_pieces_framework2.Property.ShortText({
          displayName: "Zip",
          required: !1
        })
      }
    }),
    websites: import_pieces_framework2.Property.Array({
      displayName: "Websites",
      required: !1,
      properties: {
        type: import_pieces_framework2.Property.StaticDropdown({
          displayName: "Type",
          required: !1,
          options: {
            options: [
              { label: "Home", value: "Home" },
              { label: "Work", value: "Work" }
            ]
          }
        }),
        service: import_pieces_framework2.Property.StaticDropdown({
          displayName: "Service",
          required: !0,
          options: {
            options: [
              { label: "URL", value: "URL" },
              { label: "Skype", value: "SKYPE" },
              { label: "Twitter", value: "TWITTER" },
              { label: "LinkedIn", value: "LINKED_IN" },
              { label: "Facebook", value: "FACEBOOK" },
              { label: "Xing", value: "XING" },
              { label: "Feed", value: "FEED" },
              { label: "Google+", value: "GOOGLE_PLUS" },
              { label: "Flickr", value: "FLICKR" },
              { label: "GitHub", value: "GITHUB" },
              { label: "YouTube", value: "YOUTUBE" },
              { label: "Instagram", value: "INSTAGRAM" },
              { label: "Pinterest", value: "PINTEREST" }
            ]
          }
        }),
        address: import_pieces_framework2.Property.ShortText({
          displayName: "Address",
          required: !0
        })
      }
    })
  },
  async run(context) {
    let { auth, propsValue } = context, type = propsValue.type, contactFields = propsValue.contactFields, contactData = {
      type,
      title: contactFields.title,
      jobTitle: contactFields.jobTitle,
      about: propsValue.about,
      organisationId: contactFields.organisationId,
      ownerId: propsValue.ownerId,
      teamId: propsValue.teamId,
      tags: propsValue.tags,
      emailAddresses: propsValue.emailAddresses,
      phoneNumbers: propsValue.phoneNumbers,
      addresses: propsValue.addresses,
      websites: propsValue.websites
    };
    type === "person" ? (contactData.firstName = contactFields.firstName, contactData.lastName = contactFields.lastName) : type === "organisation" && (contactData.name = contactFields.organisationName);
    let customFields = propsValue.customFields;
    return customFields && (contactData.fields = Object.entries(customFields).filter(([, value]) => value != null).map(([id, value]) => ({
      definition: { id: Number(id) },
      value
    }))), await capsuleCrmClient.createContact(
      auth,
      contactData
    );
  }
});

// ../ap/packages/pieces/community/capsule-crm/src/lib/actions/update-contact.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/capsule-crm/src/lib/common/props.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var capsuleCrmProps = {
  contact_id: (required = !0) => import_pieces_framework3.Property.Dropdown({
    displayName: "Contact",
    description: "The contact (Person or Organisation) to select.",
    required,
    refreshers: [],
    auth: capsuleCrmAuth,
    options: async (props) => {
      var _a;
      let { auth } = props;
      if (!auth)
        return {
          disabled: !0,
          placeholder: "Please connect your account first.",
          options: []
        };
      let searchTerm = (_a = props.searchValue) != null ? _a : "";
      return {
        disabled: !1,
        options: (await capsuleCrmClient.searchContacts(
          auth,
          searchTerm
        )).map((contact) => ({
          label: (contact.type === "person" ? `${contact.firstName} ${contact.lastName}` : contact.name) || "Unnamed Contact",
          value: contact.id
        }))
      };
    }
  }),
  milestone_id: (required = !0) => import_pieces_framework3.Property.Dropdown({
    displayName: "Milestone",
    description: "The milestone to assign the opportunity to.",
    required,
    refreshers: [],
    auth: capsuleCrmAuth,
    options: async ({ auth }) => auth ? {
      disabled: !1,
      options: (await capsuleCrmClient.listMilestones(
        auth
      )).map((milestone) => ({
        label: milestone.name,
        value: milestone.id
      }))
    } : {
      disabled: !0,
      placeholder: "Please connect your account first.",
      options: []
    }
  }),
  opportunity_id: (required = !0) => import_pieces_framework3.Property.Dropdown({
    displayName: "Opportunity",
    description: "The opportunity to associate with this item.",
    required,
    refreshers: [],
    auth: capsuleCrmAuth,
    options: async ({ auth }) => auth ? {
      disabled: !1,
      options: (await capsuleCrmClient.listOpportunities(
        auth
      )).map((opportunity) => ({
        label: opportunity.name,
        value: opportunity.id
      }))
    } : {
      disabled: !0,
      placeholder: "Please connect your account first.",
      options: []
    }
  }),
  project_id: (required = !1) => import_pieces_framework3.Property.Dropdown({
    displayName: "Project",
    description: "The project to associate this item with.",
    required,
    refreshers: [],
    auth: capsuleCrmAuth,
    options: async ({ auth }) => auth ? {
      disabled: !1,
      options: (await capsuleCrmClient.listProjects(
        auth
      )).map((project) => ({
        label: project.name,
        value: project.id
      }))
    } : {
      disabled: !0,
      placeholder: "Please connect your account first.",
      options: []
    }
  }),
  case_id: (required = !1) => import_pieces_framework3.Property.Dropdown({
    displayName: "Case",
    description: "The case to associate this item with.",
    required,
    refreshers: [],
    auth: capsuleCrmAuth,
    options: async ({ auth }) => auth ? {
      disabled: !1,
      options: (await capsuleCrmClient.listCases(
        auth
      )).map((kase) => ({ label: kase.name, value: kase.id }))
    } : {
      disabled: !0,
      placeholder: "Please connect your account first.",
      options: []
    }
  }),
  owner_id: (required = !1) => import_pieces_framework3.Property.Dropdown({
    auth: capsuleCrmAuth,
    displayName: "Owner",
    description: "The user to assign the task to.",
    required,
    refreshers: [],
    options: async ({ auth }) => auth ? {
      disabled: !1,
      options: (await capsuleCrmClient.listUsers(
        auth
      )).map((user) => ({
        label: user.username,
        value: user.id
      }))
    } : {
      disabled: !0,
      placeholder: "Please connect your account first.",
      options: []
    }
  }),
  team_id: (required = !1) => import_pieces_framework3.Property.Dropdown({
    displayName: "Team",
    description: "The team to assign the contact to.",
    required,
    refreshers: [],
    auth: capsuleCrmAuth,
    options: async ({ auth }) => auth ? {
      disabled: !1,
      options: (await capsuleCrmClient.listTeams(
        auth
      )).map((team) => ({
        label: team.name,
        value: team.id
      }))
    } : {
      disabled: !0,
      placeholder: "Please connect your account first.",
      options: []
    }
  })
};

// ../ap/packages/pieces/community/capsule-crm/src/lib/actions/update-contact.ts
var updateContactAction = (0, import_pieces_framework4.createAction)({
  auth: capsuleCrmAuth,
  name: "update_contact",
  classification: "WRITE",
  displayName: "Update Contact",
  description: "Update fields on an existing Person or Organisation.",
  audience: "both",
  aiMetadata: {
    description: "Updates fields on an existing Capsule CRM contact identified by its contact ID. Editable fields depend on whether the contact is a person or an organisation, and nested email, phone, address, and website entries can be added or removed (each supports a delete flag). Use when you already know the target contact ID; only provided fields are changed. Not idempotent because it issues a mutating update on each call.",
    idempotent: !1
  },
  props: {
    contact_id: capsuleCrmProps.contact_id(),
    contactFields: import_pieces_framework4.Property.DynamicProperties({
      auth: capsuleCrmAuth,
      displayName: "Details",
      required: !0,
      refreshers: ["contact_id"],
      props: async ({ auth, contact_id }) => {
        let fields = {};
        if (!auth || !contact_id) return fields;
        let contact = await capsuleCrmClient.getContact(
          auth,
          contact_id
        );
        return (contact == null ? void 0 : contact.type) === "person" ? (fields.firstName = import_pieces_framework4.Property.ShortText({
          displayName: "First Name",
          description: "Update the person's first name.",
          required: !1
        }), fields.lastName = import_pieces_framework4.Property.ShortText({
          displayName: "Last Name",
          description: "Update the person's last name.",
          required: !1
        }), fields.title = import_pieces_framework4.Property.ShortText({
          displayName: "Title",
          description: "Update the person's job title.",
          required: !1
        })) : (contact == null ? void 0 : contact.type) === "organisation" && (fields.organisationName = import_pieces_framework4.Property.ShortText({
          displayName: "Organisation Name",
          description: "Update the organisation's name.",
          required: !1
        })), fields;
      }
    }),
    ownerId: capsuleCrmProps.owner_id(!1),
    teamId: capsuleCrmProps.team_id(!1),
    about: import_pieces_framework4.Property.LongText({
      displayName: "About",
      description: "Update the biography or description for the contact.",
      required: !1
    }),
    addresses: import_pieces_framework4.Property.DynamicProperties({
      auth: capsuleCrmAuth,
      displayName: "Addresses",
      required: !1,
      refreshers: ["contact_id"],
      props: async ({ auth, contact_id }) => {
        var _a, _b;
        let fields = {};
        if (!auth || !contact_id) return fields;
        let contact = await capsuleCrmClient.getContact(
          auth,
          contact_id
        ), addressOptions = (_b = (_a = contact == null ? void 0 : contact.addresses) == null ? void 0 : _a.map((address) => ({
          label: `${address.street}, ${address.city}`,
          value: address.id
        }))) != null ? _b : [];
        return fields.addresses = import_pieces_framework4.Property.Array({
          displayName: "Addresses",
          required: !1,
          properties: {
            id: import_pieces_framework4.Property.StaticDropdown({
              displayName: "Address",
              required: !1,
              options: {
                options: addressOptions
              }
            }),
            type: import_pieces_framework4.Property.StaticDropdown({
              displayName: "Type",
              required: !1,
              options: {
                options: [
                  { label: "Home", value: "Home" },
                  { label: "Postal", value: "Postal" },
                  { label: "Office", value: "Office" },
                  { label: "Billing", value: "Billing" },
                  { label: "Shipping", value: "Shipping" }
                ]
              }
            }),
            street: import_pieces_framework4.Property.ShortText({
              displayName: "Street",
              required: !1
            }),
            city: import_pieces_framework4.Property.ShortText({
              displayName: "City",
              required: !1
            }),
            state: import_pieces_framework4.Property.ShortText({
              displayName: "State",
              required: !1
            }),
            country: import_pieces_framework4.Property.ShortText({
              displayName: "Country",
              required: !1
            }),
            zip: import_pieces_framework4.Property.ShortText({
              displayName: "Zip",
              required: !1
            }),
            delete: import_pieces_framework4.Property.Checkbox({
              displayName: "Delete",
              description: "Check this to delete the address.",
              required: !1
            })
          }
        }), fields;
      }
    }),
    websites: import_pieces_framework4.Property.DynamicProperties({
      auth: capsuleCrmAuth,
      displayName: "Websites",
      required: !1,
      refreshers: ["contact_id"],
      props: async ({ auth, contact_id }) => {
        var _a, _b;
        let fields = {};
        if (!auth || !contact_id) return fields;
        let contact = await capsuleCrmClient.getContact(
          auth,
          contact_id
        ), websiteOptions = (_b = (_a = contact == null ? void 0 : contact.websites) == null ? void 0 : _a.map((website) => ({
          label: website.address,
          value: website.id
        }))) != null ? _b : [];
        return fields.websites = import_pieces_framework4.Property.Array({
          displayName: "Websites",
          required: !1,
          properties: {
            id: import_pieces_framework4.Property.StaticDropdown({
              displayName: "Website",
              required: !1,
              options: {
                options: websiteOptions
              }
            }),
            type: import_pieces_framework4.Property.StaticDropdown({
              displayName: "Type",
              required: !1,
              options: {
                options: [
                  { label: "Home", value: "Home" },
                  { label: "Work", value: "Work" }
                ]
              }
            }),
            service: import_pieces_framework4.Property.StaticDropdown({
              displayName: "Service",
              required: !1,
              options: {
                options: [
                  { label: "URL", value: "URL" },
                  { label: "Skype", value: "SKYPE" },
                  { label: "Twitter", value: "TWITTER" },
                  { label: "LinkedIn", value: "LINKED_IN" },
                  { label: "Facebook", value: "FACEBOOK" },
                  { label: "Xing", value: "XING" },
                  { label: "Feed", value: "FEED" },
                  { label: "Google+", value: "GOOGLE_PLUS" },
                  { label: "Flickr", value: "FLICKR" },
                  { label: "GitHub", value: "GITHUB" },
                  { label: "YouTube", value: "YOUTUBE" },
                  { label: "Instagram", value: "INSTAGRAM" },
                  { label: "Pinterest", value: "PINTEREST" }
                ]
              }
            }),
            address: import_pieces_framework4.Property.ShortText({
              displayName: "Address",
              required: !1
            }),
            delete: import_pieces_framework4.Property.Checkbox({
              displayName: "Delete",
              description: "Check this to delete the website.",
              required: !1
            })
          }
        }), fields;
      }
    }),
    emailAddresses: import_pieces_framework4.Property.DynamicProperties({
      auth: capsuleCrmAuth,
      displayName: "Email Addresses",
      required: !1,
      refreshers: ["contact_id"],
      props: async ({ auth, contact_id }) => {
        var _a, _b;
        let fields = {};
        if (!auth || !contact_id) return fields;
        let contact = await capsuleCrmClient.getContact(
          auth,
          contact_id
        ), emailOptions = (_b = (_a = contact == null ? void 0 : contact.emailAddresses) == null ? void 0 : _a.map((email) => ({
          label: email.address,
          value: email.id
        }))) != null ? _b : [];
        return fields.emailAddresses = import_pieces_framework4.Property.Array({
          displayName: "Email Addresses",
          required: !1,
          properties: {
            id: import_pieces_framework4.Property.StaticDropdown({
              displayName: "Email Address",
              required: !1,
              options: {
                options: emailOptions
              }
            }),
            type: import_pieces_framework4.Property.StaticDropdown({
              displayName: "Type",
              required: !1,
              options: {
                options: [
                  { label: "Home", value: "Home" },
                  { label: "Work", value: "Work" }
                ]
              }
            }),
            address: import_pieces_framework4.Property.ShortText({
              displayName: "Address",
              required: !1
            }),
            delete: import_pieces_framework4.Property.Checkbox({
              displayName: "Delete",
              description: "Check this to delete the email.",
              required: !1
            })
          }
        }), fields;
      }
    }),
    phoneNumbers: import_pieces_framework4.Property.DynamicProperties({
      auth: capsuleCrmAuth,
      displayName: "Phone Numbers",
      required: !1,
      refreshers: ["contact_id"],
      props: async ({ auth, contact_id }) => {
        var _a, _b;
        let fields = {};
        if (!auth || !contact_id) return fields;
        let contact = await capsuleCrmClient.getContact(
          auth,
          contact_id
        ), phoneOptions = (_b = (_a = contact == null ? void 0 : contact.phoneNumbers) == null ? void 0 : _a.map((phone) => ({
          label: phone.number,
          value: phone.id
        }))) != null ? _b : [];
        return fields.phoneNumbers = import_pieces_framework4.Property.Array({
          displayName: "Phone Numbers",
          required: !1,
          properties: {
            id: import_pieces_framework4.Property.StaticDropdown({
              displayName: "Phone Number",
              required: !1,
              options: {
                options: phoneOptions
              }
            }),
            type: import_pieces_framework4.Property.StaticDropdown({
              displayName: "Type",
              required: !1,
              options: {
                options: [
                  { label: "Home", value: "Home" },
                  { label: "Work", value: "Work" },
                  { label: "Mobile", value: "Mobile" },
                  { label: "Fax", value: "Fax" },
                  { label: "Direct", value: "Direct" }
                ]
              }
            }),
            number: import_pieces_framework4.Property.ShortText({
              displayName: "Number",
              required: !1
            }),
            delete: import_pieces_framework4.Property.Checkbox({
              displayName: "Delete",
              description: "Check this to delete the phone number.",
              required: !1
            })
          }
        }), fields;
      }
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h;
    let { auth, propsValue } = context, contactId = propsValue.contact_id, contactFields = propsValue.contactFields;
    return await capsuleCrmClient.updateContact(auth, contactId, {
      firstName: contactFields.firstName,
      lastName: contactFields.lastName,
      name: contactFields.organisationName,
      title: contactFields.title,
      about: propsValue.about,
      ownerId: propsValue.ownerId,
      teamId: propsValue.teamId,
      addresses: (_b = (_a = propsValue.addresses) == null ? void 0 : _a.addresses) == null ? void 0 : _b.map((address) => __spreadProps(__spreadValues({}, address), {
        _delete: address.delete
      })),
      websites: (_d = (_c = propsValue.websites) == null ? void 0 : _c.websites) == null ? void 0 : _d.map((website) => __spreadProps(__spreadValues({}, website), {
        _delete: website.delete
      })),
      emailAddresses: (_f = (_e = propsValue.emailAddresses) == null ? void 0 : _e.emailAddresses) == null ? void 0 : _f.map((email) => __spreadProps(__spreadValues({}, email), {
        _delete: email.delete
      })),
      phoneNumbers: (_h = (_g = propsValue.phoneNumbers) == null ? void 0 : _g.phoneNumbers) == null ? void 0 : _h.map((phone) => __spreadProps(__spreadValues({}, phone), {
        _delete: phone.delete
      }))
    });
  }
});

// ../ap/packages/pieces/community/capsule-crm/src/lib/actions/create-opportunity.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var createOpportunityAction = (0, import_pieces_framework5.createAction)({
  auth: capsuleCrmAuth,
  name: "create_opportunity",
  classification: "WRITE",
  displayName: "Create Opportunity",
  description: "Create a new Opportunity in Capsule CRM.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new sales opportunity in Capsule CRM linked to a party (contact) and a pipeline milestone, both required. Use to log a new deal once you have the target party and milestone IDs. Not idempotent: each call creates a separate opportunity, so repeating it produces duplicates.",
    idempotent: !1
  },
  props: {
    partyId: import_pieces_framework5.Property.Dropdown({
      displayName: "Party",
      required: !0,
      refreshers: [],
      auth: capsuleCrmAuth,
      options: async ({ auth }) => auth ? {
        options: (await capsuleCrmClient.searchContacts(
          auth,
          ""
        )).map((contact) => ({
          label: contact.type === "person" ? `${contact.firstName} ${contact.lastName}` : contact.name || `Unnamed ${contact.type}`,
          value: contact.id
        }))
      } : { options: [], disabled: !0, placeholder: "Please connect your Capsule CRM account first" }
    }),
    name: import_pieces_framework5.Property.ShortText({
      displayName: "Name",
      description: "A short description of the opportunity.",
      required: !0
    }),
    description: import_pieces_framework5.Property.LongText({
      displayName: "Description",
      description: "More details about the opportunity.",
      required: !1
    }),
    milestoneId: import_pieces_framework5.Property.Dropdown({
      auth: capsuleCrmAuth,
      displayName: "Milestone",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        options: (await capsuleCrmClient.listMilestones(
          auth
        )).map((milestone) => ({
          label: milestone.name,
          value: milestone.id
        }))
      } : { options: [], disabled: !0, placeholder: "Please connect your Capsule CRM account first" }
    }),
    currency: import_pieces_framework5.Property.ShortText({
      displayName: "Currency",
      description: "The currency for the opportunity value (e.g., USD, GBP).",
      required: !1
    }),
    amount: import_pieces_framework5.Property.Number({
      displayName: "Amount",
      description: "The numerical value of the opportunity.",
      required: !1
    }),
    expectedCloseOn: import_pieces_framework5.Property.DateTime({
      displayName: "Expected Close Date",
      description: "The expected closing date for the opportunity.",
      required: !1
    }),
    probability: import_pieces_framework5.Property.Number({
      displayName: "Probability",
      description: "The probability of winning the opportunity.",
      required: !1
    }),
    durationBasis: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Duration Basis",
      required: !1,
      description: "The basis of the duration of the opportunity.",
      options: {
        options: [
          { label: "Fixed", value: "FIXED" },
          { label: "Hour", value: "HOUR" },
          { label: "Day", value: "DAY" },
          { label: "Week", value: "WEEK" },
          { label: "Month", value: "MONTH" },
          { label: "Quarter", value: "QUARTER" },
          { label: "Year", value: "YEAR" }
        ]
      }
    }),
    duration: import_pieces_framework5.Property.Number({
      displayName: "Duration",
      required: !1,
      description: "The duration of the opportunity."
    }),
    ownerId: import_pieces_framework5.Property.Dropdown({
      auth: capsuleCrmAuth,
      displayName: "Owner",
      required: !1,
      description: "The user the opportunity is assigned to.",
      refreshers: [],
      options: async ({ auth }) => auth ? {
        options: (await capsuleCrmClient.listUsers(
          auth
        )).map((user) => ({
          label: user.name,
          value: user.id
        }))
      } : { options: [], disabled: !0, placeholder: "Please connect your Capsule CRM account first" }
    }),
    teamId: import_pieces_framework5.Property.Dropdown({
      auth: capsuleCrmAuth,
      displayName: "Team",
      required: !1,
      description: "The team the opportunity is assigned to.",
      refreshers: [],
      options: async ({ auth }) => auth ? {
        options: (await capsuleCrmClient.listTeams(
          auth
        )).map((team) => ({
          label: team.name,
          value: team.id
        }))
      } : { options: [], disabled: !0, placeholder: "Please connect your Capsule CRM account first" }
    }),
    tags: import_pieces_framework5.Property.MultiSelectDropdown({
      auth: capsuleCrmAuth,
      displayName: "Tags",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        options: (await capsuleCrmClient.listTags(auth)).map((tag) => ({
          label: tag.name,
          value: tag.name
        }))
      } : { options: [], disabled: !0, placeholder: "Please connect your Capsule CRM account first" }
    }),
    customFields: import_pieces_framework5.Property.DynamicProperties({
      auth: capsuleCrmAuth,
      displayName: "Custom Fields",
      required: !0,
      refreshers: [],
      props: async ({ auth }) => {
        var _a;
        let fields = {};
        if (!auth) return { options: [], disabled: !0, placeholder: "Please connect your Capsule CRM account first" };
        let customFields = await capsuleCrmClient.listCustomFields(
          auth
        );
        for (let field of customFields)
          switch (field.type) {
            case "list":
              fields[field.id] = import_pieces_framework5.Property.StaticDropdown({
                displayName: field.name,
                required: !1,
                options: {
                  options: ((_a = field.options) == null ? void 0 : _a.map((option) => ({
                    label: option,
                    value: option
                  }))) || []
                }
              });
              break;
            case "boolean":
              fields[field.id] = import_pieces_framework5.Property.Checkbox({
                displayName: field.name,
                required: !1
              });
              break;
            case "date":
              fields[field.id] = import_pieces_framework5.Property.DateTime({
                displayName: field.name,
                required: !1
              });
              break;
            default:
              fields[field.id] = import_pieces_framework5.Property.ShortText({
                displayName: field.name,
                required: !1
              });
          }
        return fields;
      }
    })
  },
  async run(context) {
    let { auth, propsValue } = context, opportunityData = {
      party: { id: propsValue.partyId },
      name: propsValue.name,
      milestone: { id: propsValue.milestoneId },
      description: propsValue.description,
      expectedCloseOn: propsValue.expectedCloseOn,
      probability: propsValue.probability,
      durationBasis: propsValue.durationBasis,
      duration: propsValue.duration
    };
    propsValue.ownerId && (opportunityData.owner = { id: propsValue.ownerId }), propsValue.teamId && (opportunityData.team = { id: propsValue.teamId }), propsValue.currency && propsValue.amount && (opportunityData.value = {
      currency: propsValue.currency,
      amount: propsValue.amount
    }), propsValue.tags && (opportunityData.tags = propsValue.tags.map(
      (tag) => ({ name: tag })
    ));
    let customFields = propsValue.customFields;
    return customFields && (opportunityData.fields = Object.entries(customFields).filter(([, value]) => value != null).map(
      ([id, value]) => ({
        definition: { id: Number(id) },
        value
      })
    )), await capsuleCrmClient.createOpportunity(auth, opportunityData);
  }
});

// ../ap/packages/pieces/community/capsule-crm/src/lib/actions/create-project.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var createProjectAction = (0, import_pieces_framework6.createAction)({
  auth: capsuleCrmAuth,
  name: "create_project",
  classification: "WRITE",
  displayName: "Create Project",
  description: "Create a new Project in Capsule CRM.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new project (case) in Capsule CRM tied to a required party (contact), with optional links to an opportunity, stage, owner, team, tags, and custom fields. Use to open a new piece of work for a contact. Not idempotent: each call creates a separate project, so repeating it produces duplicates.",
    idempotent: !1
  },
  props: {
    partyId: import_pieces_framework6.Property.Dropdown({
      auth: capsuleCrmAuth,
      displayName: "Party",
      description: "The main contact for this project.",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        options: (await capsuleCrmClient.searchContacts(
          auth,
          ""
        )).map((contact) => ({
          label: contact.type === "person" ? `${contact.firstName} ${contact.lastName}` : contact.name || `Unnamed ${contact.type}`,
          value: contact.id
        }))
      } : {
        options: [],
        disabled: !0,
        placeholder: "Please connect your Capsule CRM account first"
      }
    }),
    name: import_pieces_framework6.Property.ShortText({
      displayName: "Name",
      description: "The name of this project.",
      required: !0
    }),
    description: import_pieces_framework6.Property.LongText({
      displayName: "Description",
      description: "The description of this project.",
      required: !1
    }),
    opportunityId: import_pieces_framework6.Property.Dropdown({
      displayName: "Opportunity",
      description: "An optional link to the opportunity that this project was created to support.",
      required: !1,
      refreshers: [],
      auth: capsuleCrmAuth,
      options: async ({ auth }) => auth ? {
        options: (await capsuleCrmClient.searchOpportunities(
          auth
        )).map((opportunity) => ({
          label: opportunity.name,
          value: opportunity.id
        }))
      } : {
        options: [],
        disabled: !0,
        placeholder: "Please connect your Capsule CRM account first"
      }
    }),
    stageId: import_pieces_framework6.Property.Dropdown({
      displayName: "Stage",
      description: "The stage that this project is on.",
      required: !1,
      refreshers: [],
      auth: capsuleCrmAuth,
      options: async ({ auth }) => auth ? {
        options: (await capsuleCrmClient.listStages(
          auth
        )).map((stage) => ({
          label: stage.name,
          value: stage.id
        }))
      } : {
        options: [],
        disabled: !0,
        placeholder: "Please connect your Capsule CRM account first"
      }
    }),
    status: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Status",
      description: "The status of the project.",
      required: !1,
      options: {
        options: [
          { label: "Open", value: "OPEN" },
          { label: "Closed", value: "CLOSED" }
        ]
      }
    }),
    expectedCloseOn: import_pieces_framework6.Property.DateTime({
      displayName: "Expected Close Date",
      description: "The expected close date of this project.",
      required: !1
    }),
    ownerId: import_pieces_framework6.Property.Dropdown({
      auth: capsuleCrmAuth,
      displayName: "Owner",
      description: "The user this project is assigned to.",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        options: (await capsuleCrmClient.listUsers(
          auth
        )).map((user) => ({
          label: user.name,
          value: user.id
        }))
      } : {
        options: [],
        disabled: !0,
        placeholder: "Please connect your Capsule CRM account first"
      }
    }),
    teamId: import_pieces_framework6.Property.Dropdown({
      auth: capsuleCrmAuth,
      displayName: "Team",
      description: "The team this project is assigned to.",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        options: (await capsuleCrmClient.listTeams(
          auth
        )).map((team) => ({
          label: team.name,
          value: team.id
        }))
      } : {
        options: [],
        disabled: !0,
        placeholder: "Please connect your Capsule CRM account first"
      }
    }),
    tags: import_pieces_framework6.Property.MultiSelectDropdown({
      auth: capsuleCrmAuth,
      displayName: "Tags",
      description: "An array of tags that are added to this project.",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        options: (await capsuleCrmClient.listTags(auth)).map((tag) => ({
          label: tag.name,
          value: tag.name
        }))
      } : {
        options: [],
        disabled: !0,
        placeholder: "Please connect your Capsule CRM account first"
      }
    }),
    customFields: import_pieces_framework6.Property.DynamicProperties({
      auth: capsuleCrmAuth,
      displayName: "Custom Fields",
      description: "An array of custom fields that are defined for this project.",
      required: !1,
      refreshers: [],
      props: async ({ auth }) => {
        var _a;
        let fields = {};
        if (!auth) return fields;
        let customFields = await capsuleCrmClient.listCustomFields(
          auth
        );
        for (let field of customFields)
          switch (field.type) {
            case "list":
              fields[field.id] = import_pieces_framework6.Property.StaticDropdown({
                displayName: field.name,
                required: !1,
                options: {
                  options: ((_a = field.options) == null ? void 0 : _a.map((option) => ({
                    label: option,
                    value: option
                  }))) || []
                }
              });
              break;
            case "boolean":
              fields[field.id] = import_pieces_framework6.Property.Checkbox({
                displayName: field.name,
                required: !1
              });
              break;
            case "date":
              fields[field.id] = import_pieces_framework6.Property.DateTime({
                displayName: field.name,
                required: !1
              });
              break;
            default:
              fields[field.id] = import_pieces_framework6.Property.ShortText({
                displayName: field.name,
                required: !1
              });
          }
        return fields;
      }
    })
  },
  async run(context) {
    let { auth, propsValue } = context, projectData = {
      party: { id: propsValue.partyId },
      name: propsValue.name,
      description: propsValue.description,
      status: propsValue.status,
      expectedCloseOn: propsValue.expectedCloseOn
    };
    propsValue.opportunityId && (projectData.opportunity = { id: propsValue.opportunityId }), propsValue.stageId && (projectData.stage = { id: propsValue.stageId }), propsValue.ownerId && (projectData.owner = { id: propsValue.ownerId }), propsValue.teamId && (projectData.team = { id: propsValue.teamId }), propsValue.tags && (projectData.tags = propsValue.tags.map(
      (tag) => ({ name: tag })
    ));
    let customFields = propsValue.customFields;
    return customFields && (projectData.fields = Object.entries(customFields).filter(([, value]) => value != null).map(
      ([id, value]) => ({
        definition: { id: Number(id) },
        value
      })
    )), await capsuleCrmClient.createProject(auth, projectData);
  }
});

// ../ap/packages/pieces/community/capsule-crm/src/lib/actions/create-task.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var createTaskAction = (0, import_pieces_framework7.createAction)({
  auth: capsuleCrmAuth,
  name: "create_task",
  classification: "WRITE",
  displayName: "Create Task",
  description: "Create a new Task in Capsule CRM.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new task in Capsule CRM with a description and due date, optionally linked to exactly one entity chosen via the Link To selector (a party/contact, an opportunity, or a project). Use to schedule a to-do or follow-up. Not idempotent: each call creates a separate task, so repeating it produces duplicates.",
    idempotent: !1
  },
  props: {
    description: import_pieces_framework7.Property.ShortText({
      displayName: "Description",
      description: "A short description of the task.",
      required: !0
    }),
    dueOn: import_pieces_framework7.Property.DateTime({
      displayName: "Due Date",
      description: "The date when this task is due.",
      required: !0
    }),
    detail: import_pieces_framework7.Property.LongText({
      displayName: "Details",
      description: "More details about the task.",
      required: !1
    }),
    dueTime: import_pieces_framework7.Property.ShortText({
      displayName: "Due Time",
      description: "The time when this task is due (e.g., 18:00:00). Note: The time is in the user's timezone.",
      required: !1
    }),
    linkTo: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Link To",
      description: "The entity this task is linked to. Only one can be selected.",
      required: !1,
      options: {
        options: [
          { label: "Party (Contact)", value: "party" },
          { label: "Opportunity", value: "opportunity" },
          { label: "Project", value: "project" }
        ]
      }
    }),
    linkedEntityId: import_pieces_framework7.Property.DynamicProperties({
      displayName: "Linked Entity",
      required: !1,
      refreshers: ["linkTo"],
      auth: capsuleCrmAuth,
      props: async ({ auth, linkTo }) => {
        let fields = {}, linkToType = linkTo;
        if (!auth || !linkToType) return fields;
        if (linkToType === "party") {
          let contactOptions = (await capsuleCrmClient.searchContacts(
            auth,
            ""
          )).map((contact) => ({
            label: contact.type === "person" ? `${contact.firstName} ${contact.lastName}` : contact.name || `Unnamed ${contact.type}`,
            value: contact.id
          }));
          fields.partyId = import_pieces_framework7.Property.StaticDropdown({
            displayName: "Party",
            required: !0,
            options: {
              options: contactOptions
            }
          });
        } else if (linkToType === "opportunity") {
          let opportunityOptions = (await capsuleCrmClient.searchOpportunities(
            auth
          )).map((opportunity) => ({
            label: opportunity.name,
            value: opportunity.id
          }));
          fields.opportunityId = import_pieces_framework7.Property.StaticDropdown({
            displayName: "Opportunity",
            required: !0,
            options: {
              options: opportunityOptions
            }
          });
        } else if (linkToType === "project") {
          let projectOptions = (await capsuleCrmClient.searchProjects(
            auth
          )).map((project) => ({
            label: project.name,
            value: project.id
          }));
          fields.projectId = import_pieces_framework7.Property.StaticDropdown({
            displayName: "Project",
            required: !0,
            options: {
              options: projectOptions
            }
          });
        }
        return fields;
      }
    }),
    categoryId: import_pieces_framework7.Property.Dropdown({
      displayName: "Category",
      description: "The category of this task.",
      required: !1,
      refreshers: [],
      auth: capsuleCrmAuth,
      options: async ({ auth }) => auth ? {
        options: (await capsuleCrmClient.listCategories(
          auth
        )).map((category) => ({
          label: category.name,
          value: category.id
        }))
      } : {
        options: [],
        disabled: !0,
        placeholder: "Please connect your Capsule CRM account first"
      }
    }),
    ownerId: import_pieces_framework7.Property.Dropdown({
      displayName: "Owner",
      description: "The user this task is assigned to.",
      required: !1,
      refreshers: [],
      auth: capsuleCrmAuth,
      options: async ({ auth }) => auth ? {
        options: (await capsuleCrmClient.listUsers(
          auth
        )).map((user) => ({
          label: user.name,
          value: user.id
        }))
      } : {
        options: [],
        disabled: !0,
        placeholder: "Please connect your Capsule CRM account first"
      }
    })
  },
  async run(context) {
    let { auth, propsValue } = context, taskData = {
      description: propsValue.description,
      dueOn: propsValue.dueOn,
      detail: propsValue.detail,
      dueTime: propsValue.dueTime
    };
    propsValue.categoryId && (taskData.category = { id: propsValue.categoryId }), propsValue.ownerId && (taskData.owner = { id: propsValue.ownerId });
    let linkedEntity = propsValue.linkedEntityId;
    return linkedEntity && (linkedEntity.partyId ? taskData.party = { id: linkedEntity.partyId } : linkedEntity.opportunityId ? taskData.opportunity = { id: linkedEntity.opportunityId } : linkedEntity.projectId && (taskData.kase = { id: linkedEntity.projectId })), await capsuleCrmClient.createTask(auth, taskData);
  }
});

// ../ap/packages/pieces/community/capsule-crm/src/lib/actions/update-opportunity.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var updateOpportunityAction = (0, import_pieces_framework8.createAction)({
  auth: capsuleCrmAuth,
  name: "update_opportunity",
  classification: "WRITE",
  displayName: "Update Opportunity",
  description: "Update an existing Opportunity in Capsule CRM.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing Capsule CRM opportunity identified by its opportunity ID, changing fields such as name, milestone, value, probability, duration, owner, team, tags, and custom fields. Tags and custom-field entries each support a delete flag for removal. Use when you already know the target opportunity ID; only provided fields are changed. Not idempotent because it issues a mutating update on each call.",
    idempotent: !1
  },
  props: {
    opportunityId: import_pieces_framework8.Property.Dropdown({
      auth: capsuleCrmAuth,
      displayName: "Opportunity",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        options: (await capsuleCrmClient.searchOpportunities(
          auth
        )).map((opportunity) => ({
          label: opportunity.name,
          value: opportunity.id
        }))
      } : { options: [], disabled: !0, placeholder: "Please connect your Capsule CRM account first" }
    }),
    name: import_pieces_framework8.Property.ShortText({
      displayName: "Name",
      required: !1
    }),
    description: import_pieces_framework8.Property.LongText({
      displayName: "Description",
      required: !1
    }),
    milestoneId: import_pieces_framework8.Property.Dropdown({
      auth: capsuleCrmAuth,
      displayName: "Milestone",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        options: (await capsuleCrmClient.listMilestones(
          auth
        )).map((milestone) => ({
          label: milestone.name,
          value: milestone.id
        }))
      } : { options: [], disabled: !0, placeholder: "Please connect your Capsule CRM account first" }
    }),
    currency: import_pieces_framework8.Property.ShortText({
      displayName: "Currency",
      required: !1
    }),
    amount: import_pieces_framework8.Property.Number({
      displayName: "Amount",
      required: !1
    }),
    expectedCloseOn: import_pieces_framework8.Property.DateTime({
      displayName: "Expected Close Date",
      required: !1
    }),
    probability: import_pieces_framework8.Property.Number({
      displayName: "Probability",
      required: !1
    }),
    durationBasis: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Duration Basis",
      required: !1,
      options: {
        options: [
          { label: "Fixed", value: "FIXED" },
          { label: "Hour", value: "HOUR" },
          { label: "Day", value: "DAY" },
          { label: "Week", value: "WEEK" },
          { label: "Month", value: "MONTH" },
          { label: "Quarter", value: "QUARTER" },
          { label: "Year", value: "YEAR" }
        ]
      }
    }),
    duration: import_pieces_framework8.Property.Number({
      displayName: "Duration",
      required: !1
    }),
    ownerId: import_pieces_framework8.Property.Dropdown({
      auth: capsuleCrmAuth,
      displayName: "Owner",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        options: (await capsuleCrmClient.listUsers(
          auth
        )).map((user) => ({
          label: user.name,
          value: user.id
        }))
      } : { options: [], disabled: !0, placeholder: "Please connect your Capsule CRM account first" }
    }),
    teamId: import_pieces_framework8.Property.Dropdown({
      auth: capsuleCrmAuth,
      displayName: "Team",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        options: (await capsuleCrmClient.listTeams(
          auth
        )).map((team) => ({
          label: team.name,
          value: team.id
        }))
      } : { options: [], disabled: !0, placeholder: "Please connect your Capsule CRM account first" }
    }),
    tags: import_pieces_framework8.Property.DynamicProperties({
      auth: capsuleCrmAuth,
      displayName: "Tags",
      required: !1,
      refreshers: ["opportunityId"],
      props: async ({ auth, opportunityId }) => {
        var _a, _b;
        let fields = {};
        if (!auth || !opportunityId) return { options: [], disabled: !0, placeholder: "Please connect your Capsule CRM account first or select an opportunity" };
        let tagOptions = (_b = (_a = (await capsuleCrmClient.getOpportunity(
          auth,
          opportunityId
        )).tags) == null ? void 0 : _a.map((tag) => ({
          label: tag.name,
          value: tag.id
        }))) != null ? _b : [];
        return fields.tags = import_pieces_framework8.Property.Array({
          displayName: "Tags",
          required: !1,
          properties: {
            id: import_pieces_framework8.Property.StaticDropdown({
              displayName: "Tag",
              required: !1,
              options: {
                options: tagOptions
              }
            }),
            name: import_pieces_framework8.Property.ShortText({
              displayName: "New Tag Name",
              description: "Enter a name to create a new tag.",
              required: !1
            }),
            delete: import_pieces_framework8.Property.Checkbox({
              displayName: "Delete",
              description: "Check this to delete the tag.",
              required: !1
            })
          }
        }), fields;
      }
    }),
    customFields: import_pieces_framework8.Property.DynamicProperties({
      auth: capsuleCrmAuth,
      displayName: "Custom Fields",
      required: !1,
      refreshers: ["opportunityId"],
      props: async ({ auth, opportunityId }) => {
        var _a;
        let fields = {};
        if (!auth || !opportunityId) return { options: [], disabled: !0, placeholder: "Please connect your Capsule CRM account first or select an opportunity" };
        await capsuleCrmClient.getOpportunity(
          auth,
          opportunityId
        );
        let allCustomFields = await capsuleCrmClient.listCustomFields(
          auth
        ), customFieldOptions = (_a = allCustomFields == null ? void 0 : allCustomFields.map((field) => ({
          label: field.name,
          value: field.id
        }))) != null ? _a : [];
        return fields.customFields = import_pieces_framework8.Property.Array({
          displayName: "Custom Fields",
          required: !1,
          properties: {
            definitionId: import_pieces_framework8.Property.StaticDropdown({
              displayName: "Field",
              required: !0,
              options: {
                options: customFieldOptions
              }
            }),
            value: import_pieces_framework8.Property.ShortText({
              displayName: "Value",
              required: !0
            }),
            delete: import_pieces_framework8.Property.Checkbox({
              displayName: "Delete",
              description: "Check this to delete the custom field value.",
              required: !1
            })
          }
        }), fields;
      }
    })
  },
  async run(context) {
    var _a, _b;
    let { auth, propsValue } = context, { opportunityId } = propsValue, opportunityData = {
      name: propsValue.name,
      description: propsValue.description,
      expectedCloseOn: propsValue.expectedCloseOn,
      probability: propsValue.probability,
      durationBasis: propsValue.durationBasis,
      duration: propsValue.duration
    };
    propsValue.ownerId && (opportunityData.owner = { id: propsValue.ownerId }), propsValue.teamId && (opportunityData.team = { id: propsValue.teamId }), propsValue.milestoneId && (opportunityData.milestone = { id: propsValue.milestoneId }), propsValue.currency && propsValue.amount && (opportunityData.value = {
      currency: propsValue.currency,
      amount: propsValue.amount
    });
    let tags = (_a = propsValue.tags) == null ? void 0 : _a.tags;
    tags && (opportunityData.tags = tags.map((tag) => ({
      id: tag.id,
      name: tag.name,
      _delete: tag._delete
    })));
    let customFields = (_b = propsValue.customFields) == null ? void 0 : _b.customFields;
    return customFields && (opportunityData.fields = customFields.map((field) => ({
      definition: { id: field.definitionId },
      value: field.value,
      _delete: field.delete
    }))), await capsuleCrmClient.updateOpportunity(
      auth,
      opportunityId,
      opportunityData
    );
  }
});

// ../ap/packages/pieces/community/capsule-crm/src/lib/actions/add-note-to-entity.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var addNoteToEntityAction = (0, import_pieces_framework9.createAction)({
  auth: capsuleCrmAuth,
  name: "add_note_to_entity",
  classification: "WRITE",
  displayName: "Add Note to Entity",
  description: "Add a comment/note to an entity (e.g., contact, opportunity, project).",
  audience: "both",
  aiMetadata: {
    description: "Appends a note entry to one Capsule CRM record, where the entity type selector chooses whether the note attaches to a party (contact), an opportunity, or a project. Use to log a comment or activity against a known record. Not idempotent: each call appends a new note, so repeating it adds duplicate notes.",
    idempotent: !1
  },
  props: {
    content: import_pieces_framework9.Property.LongText({
      displayName: "Note Content",
      description: "The body of the note.",
      required: !0
    }),
    entityType: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Entity Type",
      description: "The type of entity to add the note to.",
      required: !0,
      options: {
        options: [
          { label: "Party (Contact)", value: "party" },
          { label: "Opportunity", value: "opportunity" },
          { label: "Project", value: "project" }
        ]
      }
    }),
    entityId: import_pieces_framework9.Property.DynamicProperties({
      auth: capsuleCrmAuth,
      displayName: "Entity",
      required: !0,
      refreshers: ["entityType"],
      props: async ({ auth, entityType }) => {
        let fields = {}, entityTypeStr = entityType;
        if (!auth || !entityTypeStr) return fields;
        if (entityTypeStr === "party") {
          let contacts = await capsuleCrmClient.searchContacts(
            auth,
            ""
          );
          fields.partyId = import_pieces_framework9.Property.StaticDropdown({
            displayName: "Party",
            required: !0,
            options: {
              options: contacts.map((contact) => ({
                label: contact.type === "person" ? `${contact.firstName} ${contact.lastName}` : contact.name || `Unnamed ${contact.type}`,
                value: contact.id
              }))
            }
          });
        } else if (entityTypeStr === "opportunity") {
          let opportunities = await capsuleCrmClient.searchOpportunities(
            auth
          );
          fields.opportunityId = import_pieces_framework9.Property.StaticDropdown({
            displayName: "Opportunity",
            required: !0,
            options: {
              options: opportunities.map((opportunity) => ({
                label: opportunity.name,
                value: opportunity.id
              }))
            }
          });
        } else if (entityTypeStr === "project") {
          let projects = await capsuleCrmClient.searchProjects(
            auth
          );
          fields.projectId = import_pieces_framework9.Property.StaticDropdown({
            displayName: "Project",
            required: !0,
            options: {
              options: projects.map((project) => ({
                label: project.name,
                value: project.id
              }))
            }
          });
        }
        return fields;
      }
    }),
    activityTypeId: import_pieces_framework9.Property.Dropdown({
      displayName: "Activity Type",
      description: 'The activity type for this entry. Defaults to "Note".',
      required: !1,
      refreshers: [],
      auth: capsuleCrmAuth,
      options: async ({ auth }) => auth ? {
        options: (await capsuleCrmClient.listActivityTypes(
          auth
        )).map((activityType) => ({
          label: activityType.name,
          value: activityType.id
        }))
      } : {
        options: [],
        disabled: !0,
        placeholder: "Please connect your Capsule CRM account first"
      }
    })
  },
  async run(context) {
    let { auth, propsValue } = context, entryData = {
      type: "note",
      content: propsValue.content
    };
    propsValue.activityTypeId && (entryData.activityType = { id: propsValue.activityTypeId });
    let entityId = propsValue.entityId;
    return entityId && (entityId.partyId ? entryData.party = { id: entityId.partyId } : entityId.opportunityId ? entryData.opportunity = { id: entityId.opportunityId } : entityId.projectId && (entryData.kase = { id: entityId.projectId })), await capsuleCrmClient.createEntry(auth, entryData);
  }
});

// ../ap/packages/pieces/community/capsule-crm/src/lib/actions/find-contact.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var findContactAction = (0, import_pieces_framework10.createAction)({
  auth: capsuleCrmAuth,
  name: "find_contact",
  classification: "SEARCH",
  displayName: "Find Contact",
  description: "Find a Person by search criteria.",
  audience: "both",
  aiMetadata: {
    description: "Searches Capsule CRM contacts by a free-text term (such as a name or email) and returns only matches of type person, filtering out organisations. Use to look up an existing person before creating or referencing one. Idempotent: it is a read-only search that does not modify any data.",
    idempotent: !0
  },
  props: {
    term: import_pieces_framework10.Property.ShortText({
      displayName: "Search Term",
      description: "The value to search for (e.g., a name or email).",
      required: !0
    })
  },
  async run(context) {
    let { auth, propsValue } = context;
    return (await capsuleCrmClient.findContact(auth, propsValue.term)).filter((party) => party.type === "person");
  }
});

// ../ap/packages/pieces/community/capsule-crm/src/lib/actions/find-project.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var findProjectAction = (0, import_pieces_framework11.createAction)({
  auth: capsuleCrmAuth,
  name: "find_project",
  classification: "SEARCH",
  displayName: "Find Project",
  description: "Find a Project by search criteria.",
  audience: "both",
  aiMetadata: {
    description: "Finds Capsule CRM projects (cases) matching a structured filter query (a conditions array of field/operator/value clauses, per Capsule's filter API). Use to locate projects by field criteria rather than a free-text term. Idempotent: it is a read-only filter that does not modify any data.",
    idempotent: !0
  },
  props: {
    filter: import_pieces_framework11.Property.Json({
      displayName: "Filter",
      description: "The structured filter query. See the [documentation](https://capsulecrm.com/developer/api-v2/filters/) for examples.",
      required: !0,
      defaultValue: {
        conditions: [
          {
            field: "name",
            operator: "is",
            value: "example"
          }
        ]
      }
    })
  },
  async run(context) {
    let { auth, propsValue } = context;
    return await capsuleCrmClient.filterProjects(
      auth,
      propsValue.filter
    );
  }
});

// ../ap/packages/pieces/community/capsule-crm/src/lib/actions/find-opportunity.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var findOpportunityAction = (0, import_pieces_framework12.createAction)({
  auth: capsuleCrmAuth,
  name: "find_opportunity",
  classification: "SEARCH",
  displayName: "Find Opportunity",
  description: "Find an Opportunity by search criteria.",
  audience: "both",
  aiMetadata: {
    description: "Finds Capsule CRM opportunities matching a structured filter query (a conditions array of field/operator/value clauses, per Capsule's filter API). Use to locate opportunities by field criteria rather than a free-text term. Idempotent: it is a read-only filter that does not modify any data.",
    idempotent: !0
  },
  props: {
    filter: import_pieces_framework12.Property.Json({
      displayName: "Filter",
      description: "The structured filter query. See the [documentation](https://capsulecrm.com/developer/api-v2/filters/) for examples.",
      required: !0,
      defaultValue: {
        conditions: [
          {
            field: "name",
            operator: "is",
            value: "example"
          }
        ]
      }
    })
  },
  async run(context) {
    let { auth, propsValue } = context;
    return await capsuleCrmClient.filterOpportunities(
      auth,
      propsValue.filter
    );
  }
});

// ../ap/packages/pieces/community/capsule-crm/src/lib/common/trigger.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var capsuleCrmCreateTrigger = ({
  name,
  displayName,
  description,
  aiMetadata,
  event,
  sampleData
}) => (0, import_pieces_framework13.createTrigger)({
  auth: capsuleCrmAuth,
  name,
  classification: "READ",
  displayName,
  description,
  aiMetadata,
  props: {},
  type: import_pieces_framework13.TriggerStrategy.WEBHOOK,
  sampleData,
  async onEnable(context) {
    let hook = await capsuleCrmClient.subscribeRestHook(context.auth, {
      targetUrl: context.webhookUrl,
      event,
      description: `Activepieces - ${displayName}`
    });
    await context.store.put(`capsule_crm_trigger_${name}`, {
      hookId: hook.id
    });
  },
  async onDisable(context) {
    let storedData = await context.store.get(
      `capsule_crm_trigger_${name}`
    );
    storedData && await capsuleCrmClient.unsubscribeRestHook(context.auth, storedData.hookId);
  },
  async run(context) {
    return context.payload.body.payload;
  }
});

// ../ap/packages/pieces/community/capsule-crm/src/lib/triggers/new-case.ts
var newCaseTrigger = capsuleCrmCreateTrigger({
  name: "new_case",
  displayName: "New Case",
  description: "Fires when a new case (project) is created in Capsule CRM.",
  aiMetadata: {
    description: "Fires when a new case (project) is created in Capsule CRM, delivering the created case record. Use to react to newly opened cases."
  },
  event: "kase/created",
  sampleData: {
    event: "kase/created",
    payload: [
      {
        id: 12,
        party: {
          id: 892,
          type: "organisation",
          name: "Zestia",
          pictureURL: "https://capsulecrm.com/theme/default/images/org_avatar_70.png"
        },
        owner: {
          id: 61,
          username: "ted",
          name: "Ted Danson"
        },
        status: "OPEN",
        stage: {
          name: "Project Brief",
          id: 149
        },
        createdAt: "2015-12-07T16:54:27Z",
        updatedAt: "2015-12-07T16:54:27Z",
        expectedCloseOn: "2015-12-09",
        description: "Scope and design web site shopping cart",
        name: "Consulting"
      }
    ]
  }
});

// ../ap/packages/pieces/community/capsule-crm/src/lib/triggers/new-opportunity.ts
var newOpportunityTrigger = capsuleCrmCreateTrigger({
  name: "new_opportunity",
  displayName: "New Opportunity",
  description: "Fires when a new opportunity is created.",
  aiMetadata: {
    description: "Fires when a new opportunity (deal) is created in Capsule CRM, delivering the created opportunity record. Use to react to newly added deals entering the pipeline."
  },
  event: "opportunity/created",
  sampleData: {
    event: "opportunity/created",
    payload: [
      {
        id: 83948362,
        updatedAt: "2015-10-29T12:55:12Z",
        description: "Scope and design web site shopping cart",
        owner: {
          id: 6,
          username: "scottspacey",
          name: "Scott Spacey"
        },
        party: {
          id: 581,
          pictureURL: "https://capsulecrm.com/theme/default/images/person_avatar_70.png",
          type: "organisation",
          name: "Capsule"
        },
        lostReason: null,
        milestone: {
          id: 14,
          name: "Bid"
        },
        value: {
          amount: 500,
          currency: "GBP"
        },
        expectedCloseOn: "2015-10-31",
        probability: 50,
        durationBasis: "FIXED",
        duration: null,
        closedOn: null,
        createdAt: "2015-10-29T12:55:12Z",
        name: "Consulting"
      }
    ]
  }
});

// ../ap/packages/pieces/community/capsule-crm/src/lib/triggers/new-task.ts
var newTaskTrigger = capsuleCrmCreateTrigger({
  name: "new_task",
  displayName: "New Task",
  description: "Fires when a new task is created.",
  aiMetadata: {
    description: "Fires when a new task is created in Capsule CRM, delivering the created task record. Use to react to newly scheduled to-dos or follow-ups."
  },
  event: "task/created",
  sampleData: {
    event: "task/created",
    payload: [
      {
        id: 530,
        description: "Email product details",
        dueTime: "18:00:00",
        status: "OPEN",
        party: {
          id: 11587,
          type: "person",
          firstName: "Scott",
          lastName: "Spacey",
          pictureURL: "https://capsulecrm.com/theme/default/images/person_avatar_70.png"
        },
        owner: {
          id: 1,
          username: "john",
          name: "John Spacey"
        },
        createdAt: "2015-12-21T13:51:38Z",
        updatedAt: "2015-12-21T13:51:38Z",
        dueOn: "2014-05-20",
        hasTrack: !1
      }
    ]
  }
});

// ../ap/packages/pieces/community/capsule-crm/src/lib/triggers/new-project.ts
var newProjectTrigger = capsuleCrmCreateTrigger({
  name: "new_project",
  displayName: "New Project",
  description: "Fires when a project is created.",
  aiMetadata: {
    description: "Fires when a new project (case) is created in Capsule CRM, delivering the created project record. Use to react to newly opened projects."
  },
  event: "kase/created",
  sampleData: {
    event: "kase/created",
    payload: [
      {
        id: 12,
        party: {
          id: 892,
          type: "organisation",
          name: "Zestia",
          pictureURL: "https://capsulecrm.com/theme/default/images/org_avatar_70.png"
        },
        owner: {
          id: 61,
          username: "ted",
          name: "Ted Danson"
        },
        status: "OPEN",
        stage: {
          name: "Project Brief",
          id: 149
        },
        createdAt: "2015-12-07T16:54:27Z",
        updatedAt: "2015-12-07T16:54:27Z",
        expectedCloseOn: "2015-12-09",
        description: "Scope and design web site shopping cart",
        name: "Consulting"
      }
    ]
  }
});

// ../ap/packages/pieces/community/capsule-crm/src/index.ts
var capsuleCrm = (0, import_pieces_framework14.createPiece)({
  displayName: "Capsule CRM",
  auth: capsuleCrmAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/capsule-crm.png",
  authors: ["Prabhukiran161", "onyedikachi-david"],
  actions: [
    createContactAction,
    updateContactAction,
    createOpportunityAction,
    createProjectAction,
    createTaskAction,
    updateOpportunityAction,
    addNoteToEntityAction,
    findContactAction,
    findProjectAction,
    findOpportunityAction
  ],
  triggers: [
    newCaseTrigger,
    newOpportunityTrigger,
    newTaskTrigger,
    newProjectTrigger
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  capsuleCrm
});
