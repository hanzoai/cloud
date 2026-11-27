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

// ../ap/packages/pieces/community/beehiiv/src/index.ts
var index_exports = {};
__export(index_exports, {
  beehiiv: () => beehiiv
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework21 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/beehiiv/src/lib/actions/create-subscription.action.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/beehiiv/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/beehiiv/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
var BEEHIIV_API_URL = "https://api.beehiiv.com/v2";
async function beehiivApiCall({
  apiKey,
  method,
  resourceUri,
  query,
  body
}) {
  let qs = {};
  if (query)
    for (let [key, value] of Object.entries(query))
      value != null && (qs[key] = String(value));
  let request = {
    method,
    url: BEEHIIV_API_URL + resourceUri,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token: apiKey
    },
    queryParams: qs,
    body
  };
  return (await import_pieces_common.httpClient.sendRequest(request)).body;
}
async function BeehiivPaginatedApiCall({
  apiKey,
  method,
  resourceUri,
  query,
  body
}) {
  let resultData = [], page = 1, totalPages = 1;
  do {
    let response = await beehiivApiCall({
      apiKey,
      method,
      resourceUri,
      query: __spreadProps(__spreadValues({}, query), {
        limit: 100,
        page
      }),
      body
    }), { data, total_pages } = response;
    if (!data || data.length === 0) break;
    resultData.push(...data), totalPages = total_pages, page += 1;
  } while (page <= totalPages);
  return resultData;
}

// ../ap/packages/pieces/community/beehiiv/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), beehiivAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Your can obtain API key by naviating to **Settings->Workspace Settings-> API**.",
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await beehiivApiCall({
        apiKey: auth,
        method: import_pieces_common2.HttpMethod.GET,
        resourceUri: "/publications"
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API key."
      };
    }
  }
});

// ../ap/packages/pieces/community/beehiiv/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common");
var publicationId = import_pieces_framework2.Property.Dropdown({
  auth: beehiivAuth,
  displayName: "Publication",
  refreshers: [],
  required: !0,
  options: async ({ auth }) => auth ? {
    disabled: !1,
    options: (await BeehiivPaginatedApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common3.HttpMethod.GET,
      resourceUri: "/publications"
    })).map((publication) => ({
      label: publication.name,
      value: publication.id
    }))
  } : {
    disabled: !0,
    options: [],
    placeholder: "Please connect your account first."
  }
}), subscriptionId = (isRequired = !1) => import_pieces_framework2.Property.Dropdown({
  auth: beehiivAuth,
  displayName: "Subscription ID",
  refreshers: ["publicationId"],
  required: isRequired,
  options: async ({ auth, publicationId: publicationId2 }) => !auth || !publicationId2 ? {
    disabled: !0,
    options: [],
    placeholder: "Please connect your account first."
  } : {
    disabled: !1,
    options: (await BeehiivPaginatedApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common3.HttpMethod.GET,
      resourceUri: `/publications/${publicationId2}/subscriptions`
    })).map((subscription) => ({
      label: subscription.email,
      value: subscription.id
    }))
  }
}), automationId = (displayName, desc, isRequired = !1, isSingleSelect = !0) => (isSingleSelect ? import_pieces_framework2.Property.Dropdown : import_pieces_framework2.Property.MultiSelectDropdown)({
  auth: beehiivAuth,
  displayName,
  description: desc,
  refreshers: ["publicationId"],
  required: isRequired,
  options: async ({ auth, publicationId: publicationId2 }) => !auth || !publicationId2 ? {
    disabled: !0,
    options: [],
    placeholder: "Please connect your account first."
  } : {
    disabled: !1,
    options: (await BeehiivPaginatedApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common3.HttpMethod.GET,
      resourceUri: `/publications/${publicationId2}/automations`
    })).filter((automation) => automation.status !== "inactive").map((automation) => ({
      label: automation.name,
      value: automation.id
    }))
  }
}), customFields = import_pieces_framework2.Property.DynamicProperties({
  auth: beehiivAuth,
  displayName: "Custom Fields",
  refreshers: ["publicationId"],
  required: !1,
  props: async ({ auth, publicationId: publicationId2 }) => {
    if (!auth || !publicationId2) return {};
    let fields = {}, response = await BeehiivPaginatedApiCall({
      apiKey: auth,
      method: import_pieces_common3.HttpMethod.GET,
      resourceUri: `/publications/${publicationId2}/custom_fields`
    });
    for (let field of response)
      switch (field.kind) {
        case "string":
          fields[field.display] = import_pieces_framework2.Property.ShortText({
            displayName: field.display,
            required: !1
          });
          break;
        case "integer":
          fields[field.display] = import_pieces_framework2.Property.Number({
            displayName: field.display,
            required: !1
          });
          break;
        case "boolean":
          fields[field.display] = import_pieces_framework2.Property.Checkbox({
            displayName: field.display,
            required: !1
          });
          break;
        case "date":
        case "datetime":
          fields[field.display] = import_pieces_framework2.Property.DateTime({
            displayName: field.display,
            required: !1
          });
          break;
        case "list":
          fields[field.display] = import_pieces_framework2.Property.Array({
            displayName: field.display,
            required: !1
          });
          break;
        default:
          break;
      }
    return fields;
  }
});

// ../ap/packages/pieces/community/beehiiv/src/lib/actions/create-subscription.action.ts
var createSubscriptionAction = (0, import_pieces_framework3.createAction)({
  auth: beehiivAuth,
  name: "create_subscription",
  classification: "WRITE",
  displayName: "Create Subscription",
  description: "Creates a new subscription.",
  audience: "both",
  aiMetadata: { description: "Adds a new subscriber (by email) to a beehiiv publication, optionally setting tier, UTM attribution, custom fields, and enrolling them into automations on creation. Use to subscribe someone to a newsletter. Requires the publication ID and email; not idempotent \u2014 each call creates a subscription, and re-running may produce duplicate or reactivated subscribers.", idempotent: !1 },
  props: {
    publicationId,
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email",
      description: "The email address of the new subscription.",
      required: !0
    }),
    reactivate_existing: import_pieces_framework3.Property.Checkbox({
      displayName: "Reactivate Existing",
      description: "Whether to reactivate the subscription if they have already unsubscribed. Use only if the subscription is knowingly resubscribing.",
      required: !1,
      defaultValue: !1
    }),
    send_welcome_email: import_pieces_framework3.Property.Checkbox({
      displayName: "Send Welcome Email",
      description: "Whether to send the default welcome email to the subscription.",
      required: !1,
      defaultValue: !1
    }),
    utm_source: import_pieces_framework3.Property.ShortText({
      displayName: "UTM Source",
      description: "The source of the subscription.",
      required: !1
    }),
    utm_medium: import_pieces_framework3.Property.ShortText({
      displayName: "UTM Medium",
      description: "The medium of the subscription.",
      required: !1
    }),
    utm_campaign: import_pieces_framework3.Property.ShortText({
      displayName: "UTM Campaign",
      description: "The acquisition campaign of the subscription.",
      required: !1
    }),
    referring_site: import_pieces_framework3.Property.ShortText({
      displayName: "Referring Site",
      description: "The website that the subscription was referred from.",
      required: !1
    }),
    referral_code: import_pieces_framework3.Property.ShortText({
      displayName: "Referral Code",
      description: "A subscription's referral_code to give them credit for the new subscription.",
      required: !1
    }),
    tier: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Subscription Tier",
      description: "The tier for this subscription.",
      required: !1,
      options: {
        options: [
          { label: "Free", value: "free" },
          { label: "Premium", value: "premium" }
        ]
      }
    }),
    custom_fields: customFields,
    stripe_customer_id: import_pieces_framework3.Property.ShortText({
      displayName: "Stripe Customer ID",
      description: "The Stripe customer ID for this subscription.",
      required: !1
    }),
    double_opt_override: import_pieces_framework3.Property.ShortText({
      displayName: "Double Opt-in Override",
      description: "Override publication double-opt settings for this subscription.",
      required: !1
    }),
    premium_tier_ids: import_pieces_framework3.Property.Array({
      displayName: "Premium Tier IDs",
      description: "The IDs of the premium tiers this subscription is associated with.",
      required: !1
    }),
    automation_ids: automationId("Automation IDs", "Enroll the subscription into automations after their subscription has been created.", !1)
  },
  async run(context) {
    var _a, _b, _c;
    let {
      publicationId: publicationId2,
      email,
      utm_campaign,
      utm_medium,
      tier,
      utm_source,
      reactivate_existing,
      referral_code,
      referring_site,
      send_welcome_email,
      stripe_customer_id,
      double_opt_override
    } = context.propsValue, customFields2 = (_a = context.propsValue.custom_fields) != null ? _a : {}, automationIds = (_b = context.propsValue.automation_ids) != null ? _b : [], premiumTierIds = (_c = context.propsValue.premium_tier_ids) != null ? _c : [];
    return await beehiivApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common4.HttpMethod.POST,
      resourceUri: `/publications/${publicationId2}/subscriptions`,
      body: {
        email,
        reactivate_existing,
        send_welcome_email,
        utm_source,
        utm_campaign,
        utm_medium,
        referring_site,
        referral_code,
        double_opt_override,
        tier,
        premium_tier_ids: premiumTierIds.length > 0 ? premiumTierIds : void 0,
        stripe_customer_id,
        automation_ids: automationIds.length > 0 ? automationIds : void 0,
        custom_fields: Object.entries(customFields2).map(([key, value]) => ({
          name: key,
          value
        }))
      }
    });
  }
});

// ../ap/packages/pieces/community/beehiiv/src/lib/actions/update-subscription.action.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var updateSubscriptionAction = (0, import_pieces_framework4.createAction)({
  auth: beehiivAuth,
  name: "update_subscription",
  classification: "WRITE",
  displayName: "Update Subscription",
  description: "Update an existing subscription.",
  audience: "both",
  aiMetadata: { description: "Updates an existing beehiiv subscription identified by publication ID and subscription ID \u2014 change its tier, Stripe customer ID, custom fields, or unsubscribe it from the publication. Use to modify or unsubscribe a known subscriber. Idempotent: repeating with the same fields yields the same end state.", idempotent: !0 },
  props: {
    publicationId,
    subscriptionId: subscriptionId(!0),
    tier: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Subscription Tier",
      description: "Set the tier for this subscription.",
      required: !1,
      options: {
        options: [
          { label: "Free", value: "free" },
          { label: "Premium", value: "premium" }
        ]
      }
    }),
    stripe_customer_id: import_pieces_framework4.Property.ShortText({
      displayName: "Stripe Customer ID",
      description: "The Stripe Customer ID of the subscription.",
      required: !1
    }),
    unsubscribe: import_pieces_framework4.Property.Checkbox({
      displayName: "Unsubscribe",
      description: "Whether to unsubscribe this subscription from the publication.",
      required: !1
    }),
    custom_fields: customFields
  },
  async run(context) {
    var _a;
    let { publicationId: publicationId2, subscriptionId: subscriptionId2, tier, stripe_customer_id, unsubscribe } = context.propsValue, custom_fields = (_a = context.propsValue.custom_fields) != null ? _a : {}, body = {};
    tier !== void 0 && (body.tier = tier), stripe_customer_id !== void 0 && (body.stripe_customer_id = stripe_customer_id), unsubscribe !== void 0 && (body.unsubscribe = unsubscribe);
    let transformedCustomFields = Object.entries(custom_fields).filter(([_, value]) => Array.isArray(value) ? value.length > 0 : value != null && value !== "").map(([key, value]) => ({
      name: key,
      value
    }));
    return transformedCustomFields.length > 0 && (body.custom_fields = transformedCustomFields), console.log(JSON.stringify(body)), await beehiivApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common5.HttpMethod.PUT,
      resourceUri: `/publications/${publicationId2}/subscriptions/${subscriptionId2}`,
      body
    });
  }
});

// ../ap/packages/pieces/community/beehiiv/src/lib/actions/delete-subscription.action.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var deleteSubscriptionAction = (0, import_pieces_framework5.createAction)({
  auth: beehiivAuth,
  name: "delete_subscription",
  classification: "DESTRUCTIVE",
  displayName: "Delete Subscription",
  description: "Permanently deletes a subscription from a Beehiiv publication.",
  audience: "both",
  aiMetadata: { description: "Permanently deletes a subscription from a beehiiv publication, identified by publication ID and subscription ID. Use to remove a subscriber entirely (distinct from unsubscribing, which Update Subscription handles). Idempotent in effect \u2014 once deleted, repeating the call leaves the subscriber absent, though a subsequent call may error on the missing ID.", idempotent: !0 },
  props: {
    publicationId,
    subscriptionId: subscriptionId(!0)
  },
  async run(context) {
    let { publicationId: publicationId2, subscriptionId: subscriptionId2 } = context.propsValue;
    return await beehiivApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common6.HttpMethod.DELETE,
      resourceUri: `/publications/${publicationId2}/subscriptions/${subscriptionId2}`
    });
  }
});

// ../ap/packages/pieces/community/beehiiv/src/lib/actions/list-subscriptions.action.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var listSubscriptionsAction = (0, import_pieces_framework6.createAction)({
  auth: beehiivAuth,
  name: "list_subscriptions",
  classification: "SEARCH",
  displayName: "List Subscriptions",
  description: "Retrieves all subscriptions for a Beehiiv publication.",
  audience: "both",
  aiMetadata: { description: "Lists subscriptions for a beehiiv publication, optionally filtered by status (active, inactive, pending, etc.). Operates in two modes: when neither limit nor cursor is set it auto-paginates and returns every matching subscription; when a limit or cursor is provided it returns a single page with a next-cursor for manual pagination. Use to look up or enumerate subscribers. Read-only and idempotent.", idempotent: !0 },
  props: {
    publicationId,
    status: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Status",
      description: "Filter subscriptions by status.",
      required: !1,
      options: {
        options: [
          { label: "All", value: "all" },
          { label: "Active", value: "active" },
          { label: "Inactive", value: "inactive" },
          { label: "Pending", value: "pending" },
          { label: "Validating", value: "validating" },
          { label: "Invalid", value: "invalid" }
        ]
      }
    }),
    limit: import_pieces_framework6.Property.Number({
      displayName: "Limit",
      description: "Number of subscriptions to return per page (1-100).",
      required: !1
    }),
    cursor: import_pieces_framework6.Property.ShortText({
      displayName: "Cursor",
      description: "Cursor token for fetching the next page of results. Leave empty to start from the beginning.",
      required: !1
    })
  },
  async run(context) {
    var _a;
    let { publicationId: publicationId2, status, cursor, limit } = context.propsValue, baseParams = {
      order_by: "created",
      direction: "desc"
    };
    if (status && status !== "all" && (baseParams.status = status), !(0, import_pieces_framework7.isNil)(cursor) || !(0, import_pieces_framework7.isNil)(limit))
      return beehiivApiCall({
        apiKey: context.auth.secret_text,
        method: import_pieces_common7.HttpMethod.GET,
        resourceUri: `/publications/${publicationId2}/subscriptions`,
        query: __spreadValues(__spreadValues(__spreadValues({}, baseParams), (0, import_pieces_framework7.isNil)(limit) ? {} : { limit }), (0, import_pieces_framework7.isNil)(cursor) || cursor === "" ? {} : { cursor })
      });
    let allData = [], nextCursor;
    do {
      let response = await beehiivApiCall({
        apiKey: context.auth.secret_text,
        method: import_pieces_common7.HttpMethod.GET,
        resourceUri: `/publications/${publicationId2}/subscriptions`,
        query: __spreadValues(__spreadProps(__spreadValues({}, baseParams), {
          limit: 100
        }), nextCursor ? { cursor: nextCursor } : {})
      });
      if (!response.data || response.data.length === 0) break;
      allData.push(...response.data), nextCursor = (_a = response.next_cursor) != null ? _a : void 0;
    } while (nextCursor);
    return allData;
  }
});

// ../ap/packages/pieces/community/beehiiv/src/lib/actions/add-subscription-to-automation.action.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var addSubscriptionToAutomationAction = (0, import_pieces_framework8.createAction)({
  auth: beehiivAuth,
  name: "add_subscription_to_automation",
  classification: "WRITE",
  displayName: "Add Subscription to Automation",
  description: "Adds an existing subscription to a specific automation flow.",
  audience: "both",
  aiMetadata: { description: "Enrolls an existing subscriber into a beehiiv automation journey for the given publication and automation ID. The subscriber can be identified either by email or by subscription ID \u2014 provide exactly one. Use to start a subscriber on an automation. Not idempotent: each call creates a new journey enrollment.", idempotent: !1 },
  props: {
    publicationId,
    automationId: automationId("Automation ID", "", !0, !0),
    email: import_pieces_framework8.Property.ShortText({
      displayName: "Subscription Email",
      description: "The email address of the subscription. Provide either Email or Subscription ID.",
      required: !1
    }),
    subscription_id: subscriptionId(),
    double_opt_override: import_pieces_framework8.Property.ShortText({
      displayName: "Double Opt-in Override",
      description: 'Override publication double-opt settings for this subscription (e.g., "on").',
      required: !1
    })
  },
  async run(context) {
    let { publicationId: publicationId2, automationId: automationId2, email, subscription_id, double_opt_override } = context.propsValue;
    if (!email && !subscription_id)
      throw new Error("Either Subscription Email or Subscription ID must be provided.");
    let body = {};
    return email && (body.email = email), subscription_id && (body.subscription_id = subscription_id), double_opt_override && (body.double_opt_override = double_opt_override), await beehiivApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common8.HttpMethod.POST,
      resourceUri: `/publications/${publicationId2}/automations/${automationId2}/journeys`,
      body
    });
  }
});

// ../ap/packages/pieces/community/beehiiv/src/lib/actions/list-automations.action.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var import_pieces_framework10 = require("@activepieces/pieces-framework"), listAutomationsAction = (0, import_pieces_framework9.createAction)({
  auth: beehiivAuth,
  name: "list_automations",
  classification: "SEARCH",
  displayName: "List Automations",
  description: "Retrieves a list of automations for a publication.",
  audience: "both",
  aiMetadata: { description: "Lists the automations configured for a beehiiv publication. When neither page nor limit is set it auto-paginates and returns all automations; otherwise it returns the requested page. Use to discover automation IDs before enrolling subscribers. Read-only and idempotent.", idempotent: !0 },
  props: {
    publicationId,
    limit: import_pieces_framework9.Property.Number({
      displayName: "Limit",
      description: "A limit on the number of automations to be returned (1-100, default 10).",
      required: !1
    }),
    page: import_pieces_framework9.Property.Number({
      displayName: "Page",
      description: "The page number for pagination (default 1).",
      required: !1
    })
  },
  async run(context) {
    let { publicationId: publicationId2, page, limit } = context.propsValue;
    return (0, import_pieces_framework10.isNil)(page) && (0, import_pieces_framework10.isNil)(limit) ? await BeehiivPaginatedApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common9.HttpMethod.GET,
      resourceUri: `/publications/${publicationId2}/automations`
    }) : (await beehiivApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common9.HttpMethod.GET,
      resourceUri: `/publications/${publicationId2}/automations`,
      query: {
        page,
        limit
      }
    })).data;
  }
});

// ../ap/packages/pieces/community/beehiiv/src/lib/actions/list-posts.action.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var listPostsAction = (0, import_pieces_framework11.createAction)({
  auth: beehiivAuth,
  name: "list_posts",
  classification: "SEARCH",
  displayName: "List Posts",
  description: "Retrieves all posts belonging to a specific publication.",
  audience: "both",
  aiMetadata: { description: "Lists posts for a beehiiv publication, filterable by audience (free/premium), platform (web/email), status (draft/confirmed/archived), and content tags, with optional expanded fields like stats or content. When neither page nor limit is set it auto-paginates and returns all matching posts; otherwise it returns the requested page. Use to find or enumerate newsletter posts. Read-only and idempotent.", idempotent: !0 },
  props: {
    publicationId,
    expand: import_pieces_framework11.Property.StaticMultiSelectDropdown({
      displayName: "Expand Results",
      description: "Optionally expand the results by adding additional information.",
      required: !1,
      options: {
        options: [
          { label: "Stats", value: "stats" },
          { label: "Free Web Content", value: "free_web_content" },
          { label: "Free Email Content", value: "free_email_content" },
          { label: "Free RSS Content", value: "free_rss_content" },
          { label: "Premium Web Content", value: "premium_web_content" },
          { label: "Premium Email Content", value: "premium_email_content" }
        ]
      }
    }),
    audience: import_pieces_framework11.Property.StaticDropdown({
      displayName: "Audience",
      required: !0,
      options: {
        options: [
          { label: "All", value: "all" },
          { label: "Free", value: "free" },
          { label: "Premium", value: "premium" }
        ]
      }
    }),
    platform: import_pieces_framework11.Property.StaticDropdown({
      displayName: "Platform",
      required: !0,
      options: {
        options: [
          { label: "All", value: "all" },
          { label: "Web", value: "web" },
          { label: "Email", value: "email" },
          { label: "Both", value: "both" }
        ]
      }
    }),
    status: import_pieces_framework11.Property.StaticDropdown({
      displayName: "Status",
      required: !0,
      options: {
        options: [
          { label: "All", value: "all" },
          { label: "Draft", value: "draft" },
          { label: "Confirmed", value: "confirmed" },
          { label: "Archived", value: "archived" }
        ]
      }
    }),
    content_tags: import_pieces_framework11.Property.Array({
      displayName: "Content Tags",
      description: "Filter posts by content tags. Returns posts with ANY of the specified tags.",
      required: !1
    }),
    limit: import_pieces_framework11.Property.Number({
      displayName: "Limit",
      description: "Number of posts to return (1-100, default 10).",
      required: !1
    }),
    page: import_pieces_framework11.Property.Number({
      displayName: "Page",
      description: "Page number for pagination (default 1).",
      required: !1
    })
  },
  async run(context) {
    var _a, _b;
    let { publicationId: publicationId2, page, limit, status, platform, audience } = context.propsValue, expand = (_a = context.propsValue.expand) != null ? _a : [], tags = (_b = context.propsValue.content_tags) != null ? _b : [], queryParams = {
      audience,
      platform,
      status,
      order_by: "created",
      direction: "desc"
    };
    return expand && (queryParams.expand = expand.join(",")), tags.length > 0 && (queryParams.content_tags = tags), (0, import_pieces_framework12.isNil)(page) && (0, import_pieces_framework12.isNil)(limit) ? await BeehiivPaginatedApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common10.HttpMethod.GET,
      resourceUri: `/publications/${publicationId2}/automations`,
      query: queryParams
    }) : (await beehiivApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common10.HttpMethod.GET,
      resourceUri: `/publications/${publicationId2}/automations`,
      query: __spreadValues({
        page,
        limit
      }, queryParams)
    })).data;
  }
});

// ../ap/packages/pieces/community/beehiiv/src/lib/triggers/new-post-sent.trigger.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var import_pieces_common11 = require("@activepieces/pieces-common"), import_pieces_framework14 = require("@activepieces/pieces-framework"), TRIGGER_KEY = "new-post-sent-trigger", newPostSentTrigger = (0, import_pieces_framework13.createTrigger)({
  auth: beehiivAuth,
  name: "beehiiv_new_post_sent",
  classification: "READ",
  displayName: "New Post Sent",
  description: "Triggers when a new post is sent.",
  aiMetadata: {
    description: "Fires when a post is sent (published/delivered) in the selected beehiiv publication, emitting the post with its title, slug, audience, status, and URLs."
  },
  props: {
    publicationId
  },
  type: import_pieces_framework13.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let { publicationId: publicationId2 } = context.propsValue, response = await beehiivApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common11.HttpMethod.POST,
      resourceUri: `/publications/${publicationId2}/webhooks`,
      body: {
        url: context.webhookUrl,
        event_types: ["post.sent"]
      }
    });
    await context.store.put(TRIGGER_KEY, response.data.id);
  },
  async onDisable(context) {
    let { publicationId: publicationId2 } = context.propsValue, webhookId = await context.store.get(TRIGGER_KEY);
    (0, import_pieces_framework14.isNil)(webhookId) || await beehiivApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common11.HttpMethod.DELETE,
      resourceUri: `/publications/${publicationId2}/webhooks/${webhookId}`
    });
  },
  async run(context) {
    return [context.payload.body.data];
  },
  sampleData: {
    audience: "free",
    authors: ["Clark Kent"],
    content_tags: ["news"],
    created: 1666800076,
    id: "post_00000000-0000-0000-0000-000000000000",
    preview_text: "More news on the horizon",
    slug: "more_news",
    split_tested: !0,
    status: "confirmed",
    subject_line: "Check this out",
    subtitle: "New post subtitle",
    thumbnail_url: "https://example.com/pictures/thumbnail.png",
    title: "New Post Title",
    displayed_date: 1666800076,
    web_url: "https://example.com/more_news"
  }
});

// ../ap/packages/pieces/community/beehiiv/src/lib/triggers/user-unsubscribes.trigger.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var import_pieces_common12 = require("@activepieces/pieces-common"), import_pieces_framework16 = require("@activepieces/pieces-framework"), TRIGGER_KEY2 = "user-unsubscribes-trigger", userUnsubscribesTrigger = (0, import_pieces_framework15.createTrigger)({
  auth: beehiivAuth,
  name: "beehiiv_user_unsubscribes",
  classification: "READ",
  displayName: "User Unsubscribes",
  description: "Triggers when a user unsubscribes.",
  aiMetadata: {
    description: "Fires when a subscriber is removed/unsubscribed from the selected beehiiv publication (subscription.deleted event), emitting the affected subscription record."
  },
  props: {
    publicationId
  },
  type: import_pieces_framework15.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let { publicationId: publicationId2 } = context.propsValue, response = await beehiivApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common12.HttpMethod.POST,
      resourceUri: `/publications/${publicationId2}/webhooks`,
      body: {
        url: context.webhookUrl,
        event_types: ["subscription.deleted"]
      }
    });
    await context.store.put(TRIGGER_KEY2, response.data.id);
  },
  async onDisable(context) {
    let { publicationId: publicationId2 } = context.propsValue, webhookId = await context.store.get(TRIGGER_KEY2);
    (0, import_pieces_framework16.isNil)(webhookId) || await beehiivApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common12.HttpMethod.DELETE,
      resourceUri: `/publications/${publicationId2}/webhooks/${webhookId}`
    });
  },
  async run(context) {
    return [context.payload.body.data];
  },
  sampleData: {
    created: 1666800076,
    email: "example@example.com",
    id: "sub_00000000-0000-0000-0000-000000000000",
    referral_code: "ABC123",
    referring_site: "https://www.blog.com",
    status: "active",
    subscription_tier: "premium",
    subscription_premium_tier_names: ["Premium", "Pro"],
    stripe_customer_id: "cus_00000000000000",
    utm_campaign: "Q1 Campaign",
    utm_channel: "website",
    utm_medium: "organic",
    utm_source: "Twitter"
  }
});

// ../ap/packages/pieces/community/beehiiv/src/lib/triggers/new-subscription-confirmed.trigger.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework");
var import_pieces_common13 = require("@activepieces/pieces-common");
var import_pieces_framework18 = require("@activepieces/pieces-framework"), TRIGGER_KEY3 = "new-subscription-confirmed-trigger", newSubscriptionConfirmedTrigger = (0, import_pieces_framework17.createTrigger)({
  auth: beehiivAuth,
  name: "beehiiv_new_subscription_confirmed",
  classification: "READ",
  displayName: "New Subscription Confirmation",
  description: "Triggers when a new subscriber confirms their subscription.",
  aiMetadata: {
    description: "Fires when a subscriber confirms their subscription to the selected beehiiv publication (subscription.confirmed event), after any double opt-in step, emitting the confirmed subscription record."
  },
  props: {
    publicationId
  },
  type: import_pieces_framework17.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let { publicationId: publicationId2 } = context.propsValue, response = await beehiivApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common13.HttpMethod.POST,
      resourceUri: `/publications/${publicationId2}/webhooks`,
      body: {
        url: context.webhookUrl,
        event_types: ["subscription.confirmed"]
      }
    });
    await context.store.put(TRIGGER_KEY3, response.data.id);
  },
  async onDisable(context) {
    let { publicationId: publicationId2 } = context.propsValue, webhookId = await context.store.get(TRIGGER_KEY3);
    (0, import_pieces_framework18.isNil)(webhookId) || await beehiivApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common13.HttpMethod.DELETE,
      resourceUri: `/publications/${publicationId2}/webhooks/${webhookId}`
    });
  },
  async test(context) {
    let { publicationId: publicationId2 } = context.propsValue, response = await beehiivApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common13.HttpMethod.GET,
      resourceUri: `/publications/${publicationId2}/subscriptions`,
      query: {
        status: "active",
        limit: 5,
        page: 1,
        order_by: "created",
        direction: "desc"
      }
    });
    return (0, import_pieces_framework18.isNil)(response.data) || response.data.length == 0 ? [] : response.data;
  },
  async run(context) {
    return [context.payload.body.data];
  },
  sampleData: {
    created: 1666800076,
    email: "example@example.com",
    id: "sub_00000000-0000-0000-0000-000000000000",
    referral_code: "ABC123",
    referring_site: "https://www.blog.com",
    status: "active",
    subscription_tier: "premium",
    subscription_premium_tier_names: ["Premium", "Pro"],
    stripe_customer_id: "cus_00000000000000",
    utm_campaign: "Q1 Campaign",
    utm_channel: "website",
    utm_medium: "organic",
    utm_source: "Twitter"
  }
});

// ../ap/packages/pieces/community/beehiiv/src/lib/triggers/new-subscriber-created.trigger.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework");
var import_pieces_common14 = require("@activepieces/pieces-common");
var import_pieces_framework20 = require("@activepieces/pieces-framework"), TRIGGER_KEY4 = "new-subscriber-created-trigger", newSubscriberCreatedTrigger = (0, import_pieces_framework19.createTrigger)({
  auth: beehiivAuth,
  name: "beehiiv_new_subscriber_created",
  classification: "READ",
  displayName: "New Subscriber Created",
  description: "Triggers when a new subscriber is created (before email confirmation).",
  aiMetadata: {
    description: "Fires when a new subscriber is created in the selected beehiiv publication (subscription.created event), before any email confirmation step, emitting the newly created subscription record."
  },
  props: {
    publicationId
  },
  type: import_pieces_framework19.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let { publicationId: publicationId2 } = context.propsValue, response = await beehiivApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common14.HttpMethod.POST,
      resourceUri: `/publications/${publicationId2}/webhooks`,
      body: {
        url: context.webhookUrl,
        event_types: ["subscription.created"]
      }
    });
    await context.store.put(TRIGGER_KEY4, response.data.id);
  },
  async onDisable(context) {
    let { publicationId: publicationId2 } = context.propsValue, webhookId = await context.store.get(TRIGGER_KEY4);
    (0, import_pieces_framework20.isNil)(webhookId) || await beehiivApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common14.HttpMethod.DELETE,
      resourceUri: `/publications/${publicationId2}/webhooks/${webhookId}`
    });
  },
  async test(context) {
    let { publicationId: publicationId2 } = context.propsValue, response = await beehiivApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common14.HttpMethod.GET,
      resourceUri: `/publications/${publicationId2}/subscriptions`,
      query: {
        status: "active",
        limit: 5,
        page: 1,
        order_by: "created",
        direction: "desc"
      }
    });
    return (0, import_pieces_framework20.isNil)(response.data) || response.data.length === 0 ? [] : response.data;
  },
  async run(context) {
    return [context.payload.body.data];
  },
  sampleData: {
    created: 1666800076,
    email: "example@example.com",
    id: "sub_00000000-0000-0000-0000-000000000000",
    referral_code: "ABC123",
    referring_site: "https://www.blog.com",
    status: "validating",
    subscription_tier: "free",
    subscription_premium_tier_names: [],
    utm_campaign: "",
    utm_channel: "",
    utm_medium: "",
    utm_source: ""
  }
});

// ../ap/packages/pieces/community/beehiiv/src/index.ts
var import_pieces_framework22 = require("@activepieces/pieces-framework"), import_pieces_common15 = require("@activepieces/pieces-common");
var beehiiv = (0, import_pieces_framework21.createPiece)({
  displayName: "Beehiiv",
  auth: beehiivAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/beehiiv.png",
  authors: ["onyedikachi-david", "kishanprmr", "tosh2308"],
  categories: [import_pieces_framework22.PieceCategory.MARKETING],
  actions: [
    createSubscriptionAction,
    updateSubscriptionAction,
    deleteSubscriptionAction,
    listSubscriptionsAction,
    addSubscriptionToAutomationAction,
    listAutomationsAction,
    listPostsAction,
    (0, import_pieces_common15.createCustomApiCallAction)({
      auth: beehiivAuth,
      baseUrl: () => BEEHIIV_API_URL,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [newPostSentTrigger, userUnsubscribesTrigger, newSubscriptionConfirmedTrigger, newSubscriberCreatedTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  beehiiv
});
