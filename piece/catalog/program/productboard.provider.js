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

// ../ap/packages/pieces/community/productboard/src/index.ts
var index_exports = {};
__export(index_exports, {
  productboard: () => productboard
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework10 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/productboard/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), productboardAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `Your Productboard API Key.

To obtain your API Key:
1. Go to https://app.productboard.com/
2. Log in to your account
3. Go to Settings \u2192 Integrations \u2192 Public API
4. Generate a new API Key
5. Copy the Key and paste it here`,
  required: !0
});

// ../ap/packages/pieces/community/productboard/src/lib/actions/create-feature.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/productboard/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), PRODUCTBOARD_API_BASE_URL = "https://api.productboard.com", productboardCommon = {
  baseUrl: PRODUCTBOARD_API_BASE_URL,
  async apiCall({
    auth,
    method,
    resourceUri,
    body = void 0,
    queryParams = void 0
  }) {
    return await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${PRODUCTBOARD_API_BASE_URL}${resourceUri}`,
      body,
      queryParams,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`,
        "X-Version": "1",
        "Content-Type": "application/json"
      }
    });
  }
};

// ../ap/packages/pieces/community/productboard/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var productboardProps = {
  /**
   * Property for selecting a feature from Productboard.
   * @param required Whether the property is mandatory.
   */
  feature_id: (required = !0) => import_pieces_framework2.Property.Dropdown({
    displayName: "Feature",
    description: "The feature to select.",
    required,
    auth: productboardAuth,
    refreshers: [],
    options: async ({ auth }) => {
      var _a;
      return auth ? {
        disabled: !1,
        options: ((_a = (await productboardCommon.apiCall({
          auth,
          method: import_pieces_common2.HttpMethod.GET,
          resourceUri: "/features"
        })).body.data) != null ? _a : []).map((feature) => ({
          label: feature.name,
          value: feature.id
        }))
      } : {
        disabled: !0,
        options: [],
        placeholder: "Please authenticate first"
      };
    }
  }),
  /**
   * Property for selecting a feature status from Productboard.
   * @param required Whether the property is mandatory.
   */
  status_id: (required = !0) => import_pieces_framework2.Property.Dropdown({
    displayName: "Status",
    description: "Current status of the feature",
    required,
    auth: productboardAuth,
    refreshers: [],
    options: async ({ auth }) => {
      var _a;
      return auth ? {
        disabled: !1,
        options: ((_a = (await productboardCommon.apiCall({
          auth,
          method: import_pieces_common2.HttpMethod.GET,
          resourceUri: "/feature-statuses"
        })).body.data) != null ? _a : []).map((status) => ({
          label: status.name,
          value: status.id
        }))
      } : {
        disabled: !0,
        options: [],
        placeholder: "Please authenticate first"
      };
    }
  })
};

// ../ap/packages/pieces/community/productboard/src/lib/actions/create-feature.ts
var createFeature = (0, import_pieces_framework3.createAction)({
  name: "create_feature",
  displayName: "Create Feature",
  description: "Create a new feature in Productboard",
  audience: "both",
  aiMetadata: { description: "Creates a new feature (or subfeature) in Productboard under a chosen parent (a product/component for a feature, or another feature for a subfeature). Use to add roadmap items programmatically; requires a name, description, status, and a valid parent id matching the selected parent type. Not idempotent: each call creates a distinct feature.", idempotent: !1 },
  auth: productboardAuth,
  props: {
    name: import_pieces_framework3.Property.ShortText({
      displayName: "Feature Name",
      description: "Name of the feature",
      required: !0
    }),
    description: import_pieces_framework3.Property.LongText({
      displayName: "Description",
      description: "Description of the feature",
      required: !0
    }),
    type: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Type",
      description: "Type of the feature",
      required: !0,
      options: {
        options: [
          { label: "Feature", value: "feature" },
          { label: "Subfeature", value: "subfeature" }
        ]
      }
    }),
    status: productboardProps.status_id(),
    parent_type: import_pieces_framework3.Property.DynamicProperties({
      displayName: "Parent Type",
      required: !0,
      auth: productboardAuth,
      refreshers: ["type"],
      props: async (props) => {
        let typeValue = props.type, fields = {};
        return typeValue === "subfeature" ? fields.parent_type = import_pieces_framework3.Property.StaticDropdown({
          displayName: "Parent Type",
          required: !0,
          options: {
            options: [
              { label: "Feature", value: "feature" }
            ]
          }
        }) : fields.parent_type = import_pieces_framework3.Property.StaticDropdown({
          displayName: "Parent Type",
          required: !0,
          options: {
            options: [
              { label: "Product", value: "product" },
              { label: "Component", value: "component" }
            ]
          }
        }), fields;
      }
    }),
    parent_id: import_pieces_framework3.Property.Dropdown({
      displayName: "Parent",
      required: !0,
      auth: productboardAuth,
      refreshers: ["parent_type"],
      options: async (props) => {
        let auth = props.auth, parent_type = props.parent_type;
        if (!auth) return { disabled: !0, options: [], placeholder: "Please authenticate first" };
        if (!parent_type) return { disabled: !0, options: [], placeholder: "Please select a parent type first" };
        let parentTypeValue = parent_type.parent_type, resourceUri = "";
        if (parentTypeValue === "product" ? resourceUri = "/products" : parentTypeValue === "component" ? resourceUri = "/components" : parentTypeValue === "feature" && (resourceUri = "/features"), !resourceUri) return { disabled: !0, options: [], placeholder: "Invalid parent type" };
        let items = (await productboardCommon.apiCall({
          auth,
          method: import_pieces_common3.HttpMethod.GET,
          resourceUri
        })).body.data || [], filteredItems = items;
        return parentTypeValue === "feature" && (filteredItems = items.filter((item) => item.type === "feature")), {
          disabled: !1,
          options: filteredItems.map((item) => ({
            label: item.name,
            value: item.id
          }))
        };
      }
    }),
    archived: import_pieces_framework3.Property.Checkbox({
      displayName: "Archived",
      description: "A flag denoting if the feature is archived. If null, it will default to `false`.",
      required: !1
    })
  },
  async run(context) {
    let { name, description, type, status, parent_type, parent_id, archived } = context.propsValue, featureBody = {
      data: {
        name,
        description: `<p>${description}</p>`,
        type,
        status: {
          id: status
        },
        parent: {
          [parent_type.parent_type]: {
            id: parent_id
          }
        },
        archived
      }
    };
    return (await productboardCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common3.HttpMethod.POST,
      resourceUri: "/features",
      body: featureBody
    })).body;
  }
});

// ../ap/packages/pieces/community/productboard/src/lib/actions/create-note.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var createNote = (0, import_pieces_framework4.createAction)({
  name: "create_note",
  displayName: "Create Note",
  description: "Creates a new note in Productboard",
  audience: "both",
  aiMetadata: { description: "Creates a new feedback note in Productboard from a title and HTML content, optionally attaching it to a user (by email) or a company (by domain) \u2014 these two attachments are mutually exclusive \u2014 plus optional source/display-url metadata and tags. Use to capture customer feedback or external input into the inbox. Not idempotent: each call creates a separate note.", idempotent: !1 },
  auth: productboardAuth,
  props: {
    title: import_pieces_framework4.Property.ShortText({
      displayName: "Note Title",
      description: "Title of the feedback note",
      required: !0
    }),
    content: import_pieces_framework4.Property.LongText({
      displayName: "Content",
      description: "HTML-encoded rich text content of the feedback note",
      required: !0
    }),
    user_email: import_pieces_framework4.Property.ShortText({
      displayName: "User Email",
      description: "Email address of a user to be attached to the note. This field can't be combined with `Company Domain`.",
      required: !1
    }),
    company_domain: import_pieces_framework4.Property.ShortText({
      displayName: "Company Domain",
      description: "Domain of a company the note should be linked to. This field can't be combined with `User Email`.",
      required: !1
    }),
    display_url: import_pieces_framework4.Property.ShortText({
      displayName: "Display URL",
      description: "URL where the external entity can be accessed.",
      required: !1
    }),
    source_origin: import_pieces_framework4.Property.ShortText({
      displayName: "Source Origin",
      description: "A unique string identifying the external system from which the data came.",
      required: !1
    }),
    source_record_id: import_pieces_framework4.Property.ShortText({
      displayName: "Source Record ID",
      description: "The unique id of the record in the origin system.",
      required: !1
    }),
    tags: import_pieces_framework4.Property.Array({
      displayName: "Tags",
      description: "A set of tags for categorizing the note.",
      required: !1,
      properties: {
        tag: import_pieces_framework4.Property.ShortText({
          displayName: "Tag",
          required: !0
        })
      }
    })
  },
  async run(context) {
    let {
      title,
      content,
      user_email,
      company_domain,
      display_url,
      source_origin,
      source_record_id,
      tags
    } = context.propsValue, noteBody = {
      title,
      content
    };
    return user_email && (noteBody.user = { email: user_email }), company_domain && (noteBody.company = { domain: company_domain }), display_url && (noteBody.display_url = display_url), source_origin && source_record_id && (noteBody.source = {
      origin: source_origin,
      record_id: source_record_id
    }), tags && (noteBody.tags = tags.map((t) => t.tag)), (await productboardCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common4.HttpMethod.POST,
      resourceUri: "/notes",
      body: noteBody
    })).body;
  }
});

// ../ap/packages/pieces/community/productboard/src/lib/actions/update-feature.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var updateFeature = (0, import_pieces_framework5.createAction)({
  name: "update_feature",
  displayName: "Update Feature",
  description: "Updates an existing feature in Productboard.",
  audience: "both",
  aiMetadata: { description: "Updates an existing Productboard feature identified by its feature id, setting only the supplied fields (name, description, status, or archived flag) and leaving others unchanged. Use to edit a known feature; if no fields are provided it makes no change. Idempotent: re-applying the same field values leaves the feature in the same state.", idempotent: !0 },
  auth: productboardAuth,
  props: {
    feature_id: productboardProps.feature_id(),
    name: import_pieces_framework5.Property.ShortText({
      displayName: "Feature Name",
      description: "New name for the feature.",
      required: !1
    }),
    description: import_pieces_framework5.Property.LongText({
      displayName: "Description",
      description: "New description for the feature.",
      required: !1
    }),
    status: productboardProps.status_id(!1),
    archived: import_pieces_framework5.Property.Checkbox({
      displayName: "Archived",
      description: "Whether the feature is archived.",
      required: !1
    })
  },
  async run(context) {
    let { feature_id, name, description, status, archived } = context.propsValue, data = {};
    return name && (data.name = name), description && (data.description = description), status && (data.status = { id: status }), archived !== void 0 && (data.archived = archived), Object.keys(data).length === 0 ? { success: !0, message: "No fields to update." } : (await productboardCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common5.HttpMethod.PATCH,
      resourceUri: `/features/${feature_id}`,
      body: { data }
    })).body;
  }
});

// ../ap/packages/pieces/community/productboard/src/lib/actions/get-feature.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var getFeature = (0, import_pieces_framework6.createAction)({
  name: "get_feature",
  displayName: "Get Feature",
  description: "Get an existing feature in Productboard",
  audience: "both",
  aiMetadata: { description: "Retrieves a single Productboard feature by its feature id. Use to look up a feature's current details before acting on it or to confirm it exists. Read-only and idempotent.", idempotent: !0 },
  auth: productboardAuth,
  props: {
    feature_id: productboardProps.feature_id()
  },
  async run(context) {
    let { feature_id } = context.propsValue;
    return (await productboardCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common6.HttpMethod.GET,
      resourceUri: `/features/${feature_id}`
    })).body;
  }
});

// ../ap/packages/pieces/community/productboard/src/lib/triggers/new-feature.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");
var newFeature = (0, import_pieces_framework7.createTrigger)({
  name: "new_feature",
  displayName: "New Feature",
  description: "Triggers when a new feature is created in Productboard",
  aiMetadata: {
    description: "Fires when a new feature is created in Productboard (feature.created webhook event), representing a newly added roadmap item."
  },
  auth: productboardAuth,
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  props: {},
  sampleData: {
    data: {
      id: "c5a0b5f0-3f2e-4e1a-8c7z-5c8e4b3f2e1a",
      eventType: "feature.created",
      links: {
        target: "https://api.productboard.com/features/c5a0b5f0-3f2e-4e1a-8c7z-5c8e4b3f2e1a"
      }
    }
  },
  onEnable: async (context) => {
    let { webhookUrl, auth } = context, response = await productboardCommon.apiCall({
      auth,
      method: import_pieces_common7.HttpMethod.POST,
      resourceUri: "/webhooks",
      body: {
        data: {
          name: "Activepieces - New Feature",
          events: [
            {
              eventType: "feature.created"
            }
          ],
          notification: {
            url: webhookUrl,
            version: 1
          }
        }
      }
    });
    return await context.store.put("webhook_id", response.body.data.id), response.body;
  },
  onDisable: async (context) => {
    let { auth } = context, webhookId = await context.store.get("webhook_id");
    webhookId && await productboardCommon.apiCall({
      auth,
      method: import_pieces_common7.HttpMethod.DELETE,
      resourceUri: `/webhooks/${webhookId}`
    });
  },
  run: async (context) => [context.payload.body]
});

// ../ap/packages/pieces/community/productboard/src/lib/triggers/new-note.ts
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework");
var newNote = (0, import_pieces_framework8.createTrigger)({
  name: "new_note",
  displayName: "New Note",
  description: "Triggers when a new note is created in Productboard",
  aiMetadata: {
    description: "Fires when a new note is created in Productboard (note.created webhook event), representing newly captured customer feedback or external input."
  },
  auth: productboardAuth,
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  props: {},
  sampleData: {
    data: {
      id: "c5a0b5f0-3f2e-4e1a-8c7z-5c8e4b3f2e1a",
      eventType: "note.created",
      links: {
        target: "https://api.productboard.com/notes/c5a0b5f0-3f2e-4e1a-8c7z-5c8e4b3f2e1a"
      }
    }
  },
  onEnable: async (context) => {
    let { webhookUrl, auth } = context, response = await productboardCommon.apiCall({
      auth,
      method: import_pieces_common8.HttpMethod.POST,
      resourceUri: "/webhooks",
      body: {
        data: {
          name: "Activepieces - New Note",
          events: [
            {
              eventType: "note.created"
            }
          ],
          notification: {
            url: webhookUrl,
            version: 1
          }
        }
      }
    });
    return await context.store.put("webhook_id", response.body.data.id), response.body;
  },
  onDisable: async (context) => {
    let { auth } = context, webhookId = await context.store.get("webhook_id");
    webhookId && await productboardCommon.apiCall({
      auth,
      method: import_pieces_common8.HttpMethod.DELETE,
      resourceUri: `/webhooks/${webhookId}`
    });
  },
  run: async (context) => [context.payload.body]
});

// ../ap/packages/pieces/community/productboard/src/lib/triggers/updated-feature.ts
var import_pieces_common9 = require("@activepieces/pieces-common"), import_pieces_framework9 = require("@activepieces/pieces-framework");
var updatedFeature = (0, import_pieces_framework9.createTrigger)({
  name: "updated_feature",
  displayName: "Updated Feature",
  description: "Triggers when an existing feature is updated in Productboard",
  aiMetadata: {
    description: "Fires when an existing feature is updated in Productboard (feature.updated webhook event), representing a change to a feature's fields such as name, description, status, or archived state."
  },
  auth: productboardAuth,
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  props: {},
  sampleData: {
    data: {
      id: "c5a0b5f0-3f2e-4e1a-8c7z-5c8e4b3f2e1a",
      eventType: "feature.updated",
      links: {
        target: "https://api.productboard.com/features/c5a0b5f0-3f2e-4e1a-8c7z-5c8e4b3f2e1a"
      }
    }
  },
  onEnable: async (context) => {
    let { webhookUrl, auth } = context, response = await productboardCommon.apiCall({
      auth,
      method: import_pieces_common9.HttpMethod.POST,
      resourceUri: "/webhooks",
      body: {
        data: {
          name: "Activepieces - Updated Feature",
          events: [
            {
              eventType: "feature.updated"
            }
          ],
          notification: {
            url: webhookUrl,
            version: 1
          }
        }
      }
    });
    return await context.store.put("webhook_id", response.body.data.id), response.body;
  },
  onDisable: async (context) => {
    let { auth } = context, webhookId = await context.store.get("webhook_id");
    webhookId && await productboardCommon.apiCall({
      auth,
      method: import_pieces_common9.HttpMethod.DELETE,
      resourceUri: `/webhooks/${webhookId}`
    });
  },
  run: async (context) => [context.payload.body]
});

// ../ap/packages/pieces/community/productboard/src/index.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), productboard = (0, import_pieces_framework10.createPiece)({
  displayName: "Productboard",
  auth: productboardAuth,
  description: "Productboard is a product management tool that helps you manage your product roadmap and features.",
  categories: [import_pieces_framework11.PieceCategory.PRODUCTIVITY],
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/productboard.png",
  authors: ["owuzo", "onyedikachi-david"],
  actions: [
    createFeature,
    createNote,
    updateFeature,
    getFeature
  ],
  triggers: [
    newFeature,
    newNote,
    updatedFeature
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  productboard
});
