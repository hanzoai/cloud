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

// ../ap/packages/pieces/community/talkable/src/index.ts
var index_exports = {};
__export(index_exports, {
  talkable: () => talkable,
  talkableAuth: () => talkableAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common14 = require("@activepieces/pieces-common"), import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_framework15 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/talkable/src/lib/actions/people/find-person.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var findPerson = (0, import_pieces_framework.createAction)({
  name: "find_person",
  // Must be a unique across the piece, this shouldn't be changed.
  auth: talkableAuth,
  displayName: "Find person",
  description: "Find person by email",
  audience: "both",
  aiMetadata: { description: "Look up a Talkable person record by email and return the requested slice of their data; the scope selector switches between general profile info, referrals as advocate, referrals as friend, rewards, shares, or personal data. Use to fetch a known customer's referral/loyalty details. Read-only; the email and chosen scope are required.", idempotent: !0 },
  props: {
    email: import_pieces_framework.Property.ShortText({
      displayName: "Person email",
      description: void 0,
      required: !0
    }),
    scope: import_pieces_framework.Property.StaticDropdown({
      displayName: "Scope",
      description: "Select scope",
      required: !0,
      options: {
        options: [
          {
            label: "General information",
            value: "/"
          },
          {
            label: "Referrals as advocate",
            value: "/referrals_as_advocate"
          },
          {
            label: "Referrals as friend",
            value: "/referrals_as_friend"
          },
          {
            label: "Rewards",
            value: "/rewards"
          },
          {
            label: "Shares",
            value: "/shares_by"
          },
          {
            label: "Personal data",
            value: "/personal_data"
          }
        ]
      }
    })
  },
  async run(context) {
    let TALKABLE_API_URL = "https://www.talkable.com/api/v2", { site, api_key } = context.auth.props;
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: `${TALKABLE_API_URL}/people/${context.propsValue.email}${context.propsValue.scope}`,
      headers: {
        Authorization: `Bearer ${api_key}`,
        "Content-Type": "application/json"
      },
      body: {
        site_slug: site
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/talkable/src/lib/actions/people/update-person.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var updatePerson = (0, import_pieces_framework2.createAction)({
  name: "update_person",
  // Must be a unique across the piece, this shouldn't be changed.
  auth: talkableAuth,
  displayName: "Update person",
  description: "Update person by email",
  audience: "both",
  aiMetadata: { description: "Update a Talkable person identified by email, setting profile fields such as name, phone, username, customer ID, custom properties, and opt-in/unsubscribe status. Use to keep an existing customer's details or subscription state current. Idempotent: it overwrites the same fields to the same values on repeat; the email is required.", idempotent: !0 },
  props: {
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Person email",
      description: void 0,
      required: !0
    }),
    first_name: import_pieces_framework2.Property.ShortText({
      displayName: "Person first name",
      description: void 0,
      required: !1
    }),
    last_name: import_pieces_framework2.Property.ShortText({
      displayName: "Person last name",
      description: void 0,
      required: !1
    }),
    phone_number: import_pieces_framework2.Property.ShortText({
      displayName: "Person phone number",
      description: void 0,
      required: !1
    }),
    username: import_pieces_framework2.Property.ShortText({
      displayName: "Person username",
      description: void 0,
      required: !1
    }),
    customer_id: import_pieces_framework2.Property.Number({
      displayName: "Customer ID",
      description: void 0,
      required: !1
    }),
    custom_properties: import_pieces_framework2.Property.Object({
      displayName: "Custom properties",
      description: void 0,
      required: !1
    }),
    gated_param_subscribed: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Opt-in status",
      description: "Opt-in status true/false",
      required: !1,
      options: {
        options: [
          {
            label: "true",
            value: "true"
          },
          {
            label: "false",
            value: "false"
          }
        ]
      }
    }),
    unsubscribed: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Unsubscribe status",
      description: "Unsubscribe status true/false",
      required: !1,
      options: {
        options: [
          {
            label: "true",
            value: "true"
          },
          {
            label: "false",
            value: "false"
          }
        ]
      }
    }),
    unsubscribed_at: import_pieces_framework2.Property.DateTime({
      displayName: "Unsubscribed date",
      description: void 0,
      required: !1
    })
  },
  async run(context) {
    let TALKABLE_API_URL = "https://www.talkable.com/api/v2", { site, api_key } = context.auth.props, {
      email,
      first_name,
      last_name,
      phone_number,
      username,
      customer_id,
      custom_properties,
      gated_param_subscribed,
      unsubscribed,
      unsubscribed_at
    } = context.propsValue;
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.PUT,
      url: `${TALKABLE_API_URL}/people/${email}`,
      headers: {
        Authorization: `Bearer ${api_key}`,
        "Content-Type": "application/json"
      },
      body: {
        site_slug: site,
        data: {
          first_name,
          last_name,
          phone_number,
          username,
          customer_id,
          custom_properties,
          gated_param_subscribed,
          unsubscribed,
          unsubscribed_at
        }
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/talkable/src/lib/actions/people/anonymize-person.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var anonymizePerson = (0, import_pieces_framework3.createAction)({
  name: "anonymize_person",
  // Must be a unique across the piece, this shouldn't be changed.
  auth: talkableAuth,
  displayName: "Anonymize person",
  description: "Anonymize person by email",
  audience: "both",
  aiMetadata: { description: "Permanently anonymize a Talkable person's personal data, identified by email, for privacy/GDPR-style erasure requests. Use only when a customer's identifying information must be scrubbed. Destructive and irreversible; repeating leaves the record anonymized (no further effect). The email is required.", idempotent: !0 },
  props: {
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Person email",
      description: void 0,
      required: !0
    })
  },
  async run(context) {
    let TALKABLE_API_URL = "https://www.talkable.com/api/v2", { site, api_key } = context.auth.props;
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: `${TALKABLE_API_URL}/people/${context.propsValue.email}/anonymize`,
      headers: {
        Authorization: `Bearer ${api_key}`,
        "Content-Type": "application/json"
      },
      body: {
        site_slug: site
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/talkable/src/lib/actions/people/unsubscribe-person.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var unsubscribePerson = (0, import_pieces_framework4.createAction)({
  name: "unsubscribe_person",
  // Must be a unique across the piece, this shouldn't be changed.
  auth: talkableAuth,
  displayName: "Unsubscribe person",
  description: "Unsubscribe person by email",
  audience: "both",
  aiMetadata: { description: "Mark a Talkable person, identified by email, as unsubscribed from referral/marketing communications. Use to honor an opt-out request. Idempotent: an already-unsubscribed person stays unsubscribed with no extra effect; the email is required.", idempotent: !0 },
  props: {
    email: import_pieces_framework4.Property.ShortText({
      displayName: "Person email",
      description: void 0,
      required: !0
    })
  },
  async run(context) {
    let TALKABLE_API_URL = "https://www.talkable.com/api/v2", { site, api_key } = context.auth.props;
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: `${TALKABLE_API_URL}/people/${context.propsValue.email}/unsubscribe`,
      headers: {
        Authorization: `Bearer ${api_key}`,
        "Content-Type": "application/json"
      },
      body: {
        site_slug: site
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/talkable/src/lib/actions/origins/create-purchase.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var createPurchase = (0, import_pieces_framework5.createAction)({
  name: "create_purchase",
  // Must be a unique across the piece, this shouldn't be changed.
  auth: talkableAuth,
  displayName: "Create purchase",
  description: "Create purchase in Talkable",
  audience: "both",
  aiMetadata: { description: "Record a single purchase origin in Talkable for a customer (by email), used to attribute referrals and trigger rewards. Use after a customer completes an order; requires email, order number, and subtotal. Not idempotent: each call posts a new purchase, so guard against duplicates (use Create batch of purchases for multiple at once).", idempotent: !1 },
  props: {
    email: import_pieces_framework5.Property.ShortText({
      displayName: "Email",
      description: void 0,
      required: !0
    }),
    order_number: import_pieces_framework5.Property.ShortText({
      displayName: "Order number",
      description: void 0,
      required: !0
    }),
    subtotal: import_pieces_framework5.Property.Number({
      displayName: "Subtotal",
      description: void 0,
      required: !0
    }),
    first_name: import_pieces_framework5.Property.ShortText({
      displayName: "First name",
      description: void 0,
      required: !1
    }),
    last_name: import_pieces_framework5.Property.ShortText({
      displayName: "Last name",
      description: void 0,
      required: !1
    }),
    username: import_pieces_framework5.Property.ShortText({
      displayName: "Username",
      description: void 0,
      required: !1
    }),
    customer_id: import_pieces_framework5.Property.ShortText({
      displayName: "Customer id",
      description: void 0,
      required: !1
    }),
    custom_properties: import_pieces_framework5.Property.Object({
      displayName: "Custom properties",
      description: void 0,
      required: !1
    }),
    phone_number: import_pieces_framework5.Property.ShortText({
      displayName: "Phone number",
      description: void 0,
      required: !1
    }),
    campaign_tags: import_pieces_framework5.Property.ShortText({
      displayName: "Campaign tags",
      description: void 0,
      required: !1
    }),
    sharing_channels: import_pieces_framework5.Property.Array({
      displayName: "Sharing channels",
      description: void 0,
      required: !1
    }),
    ip_address: import_pieces_framework5.Property.ShortText({
      displayName: "IP address",
      description: void 0,
      required: !1
    }),
    uuid: import_pieces_framework5.Property.ShortText({
      displayName: "UUID",
      description: void 0,
      required: !1
    }),
    created_at: import_pieces_framework5.Property.ShortText({
      displayName: "Created at",
      description: void 0,
      required: !1
    }),
    traffic_source: import_pieces_framework5.Property.ShortText({
      displayName: "Traffic source",
      description: void 0,
      required: !1
    }),
    coupon_codes: import_pieces_framework5.Property.Array({
      displayName: "Coupon codes",
      description: void 0,
      required: !1
    }),
    currency_iso_code: import_pieces_framework5.Property.ShortText({
      displayName: "Currency iso code",
      description: "Required for multi-currency sites",
      required: !1,
      defaultValue: "USD"
    }),
    custom_field: import_pieces_framework5.Property.ShortText({
      displayName: "Custom field",
      description: void 0,
      required: !1
    }),
    shipping_address: import_pieces_framework5.Property.ShortText({
      displayName: "Shipping address",
      description: void 0,
      required: !1
    }),
    shipping_zip: import_pieces_framework5.Property.ShortText({
      displayName: "Shipping zip",
      description: void 0,
      required: !1
    }),
    items: import_pieces_framework5.Property.Json({
      displayName: "Items",
      description: "You can pass items with purchase",
      required: !1,
      defaultValue: [
        { price: 10, quantity: 1, product_id: "SKU1" }
      ]
    })
  },
  async run(context) {
    let TALKABLE_API_URL = "https://www.talkable.com/api/v2", { site, api_key } = context.auth.props;
    return (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.POST,
      url: `${TALKABLE_API_URL}/origins/purchase`,
      headers: {
        Authorization: `Bearer ${api_key}`,
        "Content-Type": "application/json"
      },
      body: {
        site_slug: site,
        data: context.propsValue
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/talkable/src/lib/actions/origins/create-event.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var createEvent = (0, import_pieces_framework6.createAction)({
  name: "create_event",
  // Must be a unique across the piece, this shouldn't be changed.
  auth: talkableAuth,
  displayName: "Create event",
  description: "Create event in Talkable",
  audience: "both",
  aiMetadata: { description: "Record a single non-purchase event origin (e.g. a signup or newsletter subscription) in Talkable for a customer by email, used to attribute referrals and trigger rewards. Use for conversion events that are not purchases; requires email, event category, event number, and subtotal. Not idempotent: each call posts a new event (use Create batch of events for multiple at once).", idempotent: !1 },
  props: {
    email: import_pieces_framework6.Property.ShortText({
      displayName: "Email",
      description: void 0,
      required: !0
    }),
    event_category: import_pieces_framework6.Property.ShortText({
      displayName: "Event category",
      description: void 0,
      required: !0
    }),
    event_number: import_pieces_framework6.Property.ShortText({
      displayName: "Event number",
      description: void 0,
      required: !0
    }),
    subtotal: import_pieces_framework6.Property.Number({
      displayName: "Subtotal",
      description: void 0,
      required: !0
    }),
    first_name: import_pieces_framework6.Property.ShortText({
      displayName: "First name",
      description: void 0,
      required: !1
    }),
    last_name: import_pieces_framework6.Property.ShortText({
      displayName: "Last name",
      description: void 0,
      required: !1
    }),
    username: import_pieces_framework6.Property.ShortText({
      displayName: "Username",
      description: void 0,
      required: !1
    }),
    customer_id: import_pieces_framework6.Property.ShortText({
      displayName: "Customer id",
      description: void 0,
      required: !1
    }),
    custom_properties: import_pieces_framework6.Property.Object({
      displayName: "Custom properties",
      description: void 0,
      required: !1
    }),
    phone_number: import_pieces_framework6.Property.ShortText({
      displayName: "Phone number",
      description: void 0,
      required: !1
    }),
    campaign_tags: import_pieces_framework6.Property.ShortText({
      displayName: "Campaign tags",
      description: void 0,
      required: !1
    }),
    sharing_channels: import_pieces_framework6.Property.Array({
      displayName: "Sharing channels",
      description: void 0,
      required: !1
    }),
    ip_address: import_pieces_framework6.Property.ShortText({
      displayName: "IP address",
      description: void 0,
      required: !1
    }),
    uuid: import_pieces_framework6.Property.ShortText({
      displayName: "UUID",
      description: void 0,
      required: !1
    }),
    created_at: import_pieces_framework6.Property.ShortText({
      displayName: "Created at",
      description: void 0,
      required: !1
    }),
    traffic_source: import_pieces_framework6.Property.ShortText({
      displayName: "Traffic source",
      description: void 0,
      required: !1
    }),
    coupon_codes: import_pieces_framework6.Property.Array({
      displayName: "Coupon codes",
      description: void 0,
      required: !1
    }),
    currency_iso_code: import_pieces_framework6.Property.ShortText({
      displayName: "Currency iso code",
      description: "Required for multi-currency sites",
      required: !1,
      defaultValue: "USD"
    }),
    custom_field: import_pieces_framework6.Property.ShortText({
      displayName: "Custom field",
      description: void 0,
      required: !1
    }),
    shipping_address: import_pieces_framework6.Property.ShortText({
      displayName: "Shipping address",
      description: void 0,
      required: !1
    }),
    shipping_zip: import_pieces_framework6.Property.ShortText({
      displayName: "Shipping zip",
      description: void 0,
      required: !1
    }),
    items: import_pieces_framework6.Property.Json({
      displayName: "Items",
      description: "You can pass items with event",
      required: !1,
      defaultValue: [
        { price: 10, quantity: 1, product_id: "SKU1" }
      ]
    })
  },
  async run(context) {
    let TALKABLE_API_URL = "https://www.talkable.com/api/v2", { site, api_key } = context.auth.props;
    return (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.POST,
      url: `${TALKABLE_API_URL}/origins/event`,
      headers: {
        Authorization: `Bearer ${api_key}`,
        "Content-Type": "application/json"
      },
      body: {
        site_slug: site,
        data: context.propsValue
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/talkable/src/lib/actions/origins/create-events-batch.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var createEventsBatch = (0, import_pieces_framework7.createAction)({
  name: "create_events_batch",
  // Must be a unique across the piece, this shouldn't be changed.
  auth: talkableAuth,
  displayName: "Create batch of events",
  description: "Create batch of events in Talkable",
  audience: "both",
  aiMetadata: { description: "Record multiple non-purchase event origins in Talkable in one call by passing an array of event objects (each keyed to a customer email). Use to ingest events in bulk rather than one at a time; optionally create campaign offers for them. Not idempotent: each call posts the events again, so avoid resubmitting the same batch.", idempotent: !1 },
  props: {
    create_offers: import_pieces_framework7.Property.Checkbox({
      displayName: "Create offers",
      description: "Create offers for campaign",
      required: !1,
      defaultValue: !1
    }),
    events: import_pieces_framework7.Property.Json({
      displayName: "Events",
      description: void 0,
      required: !1,
      defaultValue: [
        {
          email: "user@store.com",
          event_category: "newsletter_subscription",
          event_number: "42",
          subtotal: 100.44,
          first_name: "John",
          last_name: "Doe",
          username: "johndoe1992",
          customer_id: "1024",
          custom_properties: {
            country: "US",
            eye_color: "brown",
            person_occupation: "marketing"
          },
          custom_field: "",
          phone_number: "+12025551111",
          campaign_tags: "post-purchase",
          sharing_channels: ["facebook", "custom"],
          ip_address: "192.0.2.255",
          uuid: "123e4567-e89b-32d1-a456-426614174000",
          created_at: "2023-04-27T11:30:42.769-07:00",
          traffic_source: "in-store",
          coupon_codes: ["C0001", "C0002"],
          currency_iso_code: "USD",
          shipping_address: "456 White Finch St., North Augusta, South Carolina, 29860, United States",
          shipping_zip: "29860",
          items: [
            {
              price: 100.44,
              quantity: 1,
              product_id: "SUBSCRIPTION"
            }
          ]
        }
      ]
    })
  },
  async run(context) {
    let TALKABLE_API_URL = "https://www.talkable.com/api/v2", { site, api_key } = context.auth.props;
    return (await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.POST,
      url: `${TALKABLE_API_URL}/origins/batch/events`,
      headers: {
        Authorization: `Bearer ${api_key}`,
        "Content-Type": "application/json"
      },
      body: {
        site_slug: site,
        data: context.propsValue.events,
        create_offers: context.propsValue.create_offers
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/talkable/src/lib/actions/origins/create-purchases-batch.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var createPurchasesBatch = (0, import_pieces_framework8.createAction)({
  name: "create_purchases_batch",
  // Must be a unique across the piece, this shouldn't be changed.
  auth: talkableAuth,
  displayName: "Create batch of purchases",
  description: "Create batch of purchases in Talkable",
  audience: "both",
  aiMetadata: { description: "Record multiple purchase origins in Talkable in one call by passing an array of purchase objects (each keyed to a customer email). Use to ingest orders in bulk rather than one at a time; optionally create campaign offers for them. Not idempotent: each call posts the purchases again, so avoid resubmitting the same batch.", idempotent: !1 },
  props: {
    create_offers: import_pieces_framework8.Property.Checkbox({
      displayName: "Create offers",
      description: "Create offers for campaign",
      required: !1,
      defaultValue: !1
    }),
    purchases: import_pieces_framework8.Property.Json({
      displayName: "Purchases",
      description: void 0,
      required: !1,
      defaultValue: [
        {
          email: "customer@store.com",
          order_number: "20",
          subtotal: 100.44,
          first_name: "John",
          last_name: "Doe",
          username: "johndoe1992",
          customer_id: "1024",
          custom_properties: {
            country: "US",
            eye_color: "brown",
            person_occupation: "marketing"
          },
          custom_field: "",
          phone_number: "+12025551111",
          campaign_tags: "post-purchase",
          sharing_channels: ["facebook", "custom"],
          ip_address: "192.0.2.255",
          uuid: "123e4567-e89b-32d1-a456-426614174000",
          created_at: "2023-04-27T11:30:42.769-07:00",
          traffic_source: "in-store",
          coupon_codes: ["C0001", "C0002"],
          currency_iso_code: "USD",
          shipping_address: "456 White Finch St., North Augusta, South Carolina, 29860, United States",
          shipping_zip: "29860",
          items: [
            {
              price: 25.11,
              quantity: 4,
              product_id: "TSHIRT"
            }
          ]
        }
      ]
    })
  },
  async run(context) {
    let TALKABLE_API_URL = "https://www.talkable.com/api/v2", { site, api_key } = context.auth.props;
    return (await import_pieces_common8.httpClient.sendRequest({
      method: import_pieces_common8.HttpMethod.POST,
      url: `${TALKABLE_API_URL}/origins/batch/purchases`,
      headers: {
        Authorization: `Bearer ${api_key}`,
        "Content-Type": "application/json"
      },
      body: {
        site_slug: site,
        data: context.propsValue.purchases,
        create_offers: context.propsValue.create_offers
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/talkable/src/lib/actions/origins/refund.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var refund = (0, import_pieces_framework9.createAction)({
  name: "refund",
  // Must be a unique across the piece, this shouldn't be changed.
  auth: talkableAuth,
  displayName: "Refund purchase/event",
  description: "Mark origin as refund",
  audience: "both",
  aiMetadata: { description: "Mark an existing purchase or event origin in Talkable as refunded, identified by its order/event number (origin slug), optionally with a partial refund subtotal and refund date. Use when an order is returned or canceled so referral attribution is reversed. Not idempotent: each call records a refund against the origin.", idempotent: !1 },
  props: {
    origin_slug: import_pieces_framework9.Property.ShortText({
      displayName: "Order or event number",
      description: void 0,
      required: !0
    }),
    refund_subtotal: import_pieces_framework9.Property.Number({
      displayName: "Refund subtotal",
      description: void 0,
      required: !1
    }),
    refunded_at: import_pieces_framework9.Property.DateTime({
      displayName: "Refunded date",
      description: void 0,
      required: !1
    })
  },
  async run(context) {
    let TALKABLE_API_URL = "https://www.talkable.com/api/v2", { site, api_key } = context.auth.props, { origin_slug, refund_subtotal, refunded_at } = context.propsValue;
    return (await import_pieces_common9.httpClient.sendRequest({
      method: import_pieces_common9.HttpMethod.POST,
      url: `${TALKABLE_API_URL}/origins/${origin_slug}/refund`,
      headers: {
        Authorization: `Bearer ${api_key}`,
        "Content-Type": "application/json"
      },
      body: {
        site_slug: site,
        data: {
          refunded_at,
          refund_subtotal
        }
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/talkable/src/lib/actions/loyalty/get-loyalty-redeem-actions.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var getLoyaltyRedeemActions = (0, import_pieces_framework10.createAction)({
  name: "get_loyalty_redeem_actions",
  // Must be a unique across the piece, this shouldn't be changed.
  auth: talkableAuth,
  displayName: "Get loyalty actions",
  description: "Get array of loyalty actions",
  audience: "both",
  aiMetadata: { description: "Retrieve the list of loyalty redeem actions available to a Talkable loyalty member, identified by their email. Use to see what rewards a customer can redeem. Read-only; the person's email is required.", idempotent: !0 },
  props: {
    person_email: import_pieces_framework10.Property.ShortText({
      displayName: "Person email",
      description: void 0,
      required: !0
    })
  },
  async run(context) {
    let TALKABLE_API_URL = "https://www.talkable.com/api/v2", { site, api_key } = context.auth.props;
    return (await import_pieces_common10.httpClient.sendRequest({
      method: import_pieces_common10.HttpMethod.GET,
      url: `${TALKABLE_API_URL}/loyalty/members/${context.propsValue.person_email}/redeem_actions`,
      headers: {
        Authorization: `Bearer ${api_key}`,
        "Content-Type": "application/json"
      },
      body: {
        site_slug: site
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/talkable/src/lib/actions/referrals/update-referral-status.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var updateReferralStatus = (0, import_pieces_framework11.createAction)({
  name: "update-referral-status",
  // Must be a unique across the piece, this shouldn't be changed.
  auth: talkableAuth,
  displayName: "Update referral status",
  description: "You can void or approve referral",
  audience: "both",
  aiMetadata: { description: 'Set the status of a referral tied to a purchase/event origin in Talkable to either "approved" or "voided", identified by its order/event number (origin slug). Use to manually approve a pending referral reward or void a fraudulent/invalid one. Idempotent: re-applying the same status leaves it unchanged; origin slug and a status of approved or voided are required.', idempotent: !0 },
  props: {
    origin_slug: import_pieces_framework11.Property.ShortText({
      displayName: "Order or event number",
      description: void 0,
      required: !0
    }),
    status: import_pieces_framework11.Property.StaticDropdown({
      displayName: "Status",
      description: 'Select referral status. Only "approved" or "voided" are accepted',
      required: !0,
      options: {
        options: [
          { label: "approved", value: "approved" },
          { label: "voided", value: "voided" }
        ]
      }
    })
  },
  async run(context) {
    let TALKABLE_API_URL = "https://www.talkable.com/api/v2", { site, api_key } = context.auth.props;
    return (await import_pieces_common11.httpClient.sendRequest({
      method: import_pieces_common11.HttpMethod.POST,
      url: `${TALKABLE_API_URL}/origins/${context.propsValue.origin_slug}/referral`,
      headers: {
        Authorization: `Bearer ${api_key}`,
        "Content-Type": "application/json"
      },
      body: {
        site_slug: site,
        status: context.propsValue.status
        // we have only one status so it's hardcoded
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/talkable/src/lib/actions/coupons/find-coupon.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var findCoupon = (0, import_pieces_framework12.createAction)({
  name: "find_coupon",
  // Must be a unique across the piece, this shouldn't be changed.
  auth: talkableAuth,
  displayName: "Find coupon",
  description: "Find coupon code",
  audience: "both",
  aiMetadata: { description: "Look up a Talkable coupon by its code and return its details. Use to verify a coupon exists or inspect its status/value. Read-only; the coupon code is required.", idempotent: !0 },
  props: {
    code: import_pieces_framework12.Property.ShortText({
      displayName: "Coupon code",
      description: void 0,
      required: !0
    })
  },
  async run(context) {
    let TALKABLE_API_URL = "https://www.talkable.com/api/v2", { site, api_key } = context.auth.props;
    return (await import_pieces_common12.httpClient.sendRequest({
      method: import_pieces_common12.HttpMethod.GET,
      url: `${TALKABLE_API_URL}/coupons/${context.propsValue.code}`,
      headers: {
        Authorization: `Bearer ${api_key}`,
        "Content-Type": "application/json"
      },
      body: {
        site_slug: site
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/talkable/src/lib/actions/rewards/claim-offer.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common");
var claimOffer = (0, import_pieces_framework13.createAction)({
  name: "claim-offer",
  // Must be a unique across the piece, this shouldn't be changed.
  auth: talkableAuth,
  displayName: "Share and claim offer",
  description: "Using this action, you can share and get a friend's reward",
  audience: "both",
  aiMetadata: { description: "Create an offer claim in Talkable on behalf of an advocate sharing a campaign with a friend, generating the friend's referral reward. Use to programmatically trigger a share-and-claim within a referral campaign; requires advocate email, friend email, and the campaign tag. Not idempotent: each call creates a new offer claim.", idempotent: !1 },
  props: {
    advocate_email: import_pieces_framework13.Property.ShortText({
      displayName: "Advocate email",
      description: "Example : advocate@example.com",
      required: !0
    }),
    friend_email: import_pieces_framework13.Property.ShortText({
      displayName: "Friend email",
      description: "Example : friend@example.com",
      required: !0
    }),
    campaign_tag: import_pieces_framework13.Property.ShortText({
      displayName: "Campaign tag",
      description: "Example : invite",
      required: !0
    })
  },
  async run(context) {
    let TALKABLE_API_URL = "https://www.talkable.com/api/v2", { site, api_key } = context.auth.props;
    return (await import_pieces_common13.httpClient.sendRequest({
      method: import_pieces_common13.HttpMethod.POST,
      url: `${TALKABLE_API_URL}/offer_claims`,
      headers: {
        Authorization: `Bearer ${api_key}`,
        "Content-Type": "application/json"
      },
      body: {
        site_slug: site,
        advocate_email: context.propsValue.advocate_email,
        friend_email: context.propsValue.friend_email,
        campaign_tag: context.propsValue.campaign_tag
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/talkable/src/index.ts
var markdownDescription = `
Follow these steps:

1. **Log in to your Talkable account:** Open Talkable https://www.talkable.com/login.

2. **Enter the Talkable site slug and API key:** Go to **All site Settings** > **API Settings**, and copy Site ID and API key.

`, talkableAuth = import_pieces_framework14.PieceAuth.CustomAuth({
  description: markdownDescription,
  props: {
    site: import_pieces_framework14.Property.ShortText({
      displayName: "Talkable site ID",
      required: !0
    }),
    api_key: import_pieces_framework14.Property.ShortText({
      displayName: "API key",
      required: !0
    })
  },
  required: !0
}), talkable = (0, import_pieces_framework14.createPiece)({
  displayName: "Talkable",
  description: "Referral marketing programs that drive revenue",
  auth: talkableAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://www.talkable.com/wp-content/uploads/2021/12/talkable-favicon.svg",
  authors: ["Vitalini", "kishanprmr", "MoShizzle", "abuaboud"],
  categories: [import_pieces_framework15.PieceCategory.MARKETING],
  actions: [
    findPerson,
    findCoupon,
    updatePerson,
    anonymizePerson,
    unsubscribePerson,
    createPurchase,
    createPurchasesBatch,
    createEvent,
    createEventsBatch,
    refund,
    getLoyaltyRedeemActions,
    updateReferralStatus,
    claimOffer,
    (0, import_pieces_common14.createCustomApiCallAction)({
      baseUrl: () => "https://www.talkable.com/api/v2",
      auth: talkableAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.props.api_key}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  talkable,
  talkableAuth
});
