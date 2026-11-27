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
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key != "symbol" ? key + "" : key, value);

// ../ap/packages/pieces/community/outseta/src/index.ts
var index_exports = {};
__export(index_exports, {
  outseta: () => outseta
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework53 = require("@activepieces/pieces-framework"), import_pieces_framework54 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/outseta/src/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/outseta/src/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
var OutsetaClient = class {
  constructor(auth) {
    __publicField(this, "baseUrl");
    __publicField(this, "authHeader");
    this.baseUrl = auth.domain.replace(/\/$/, ""), this.authHeader = `Outseta ${auth.apiKey}:${auth.apiSecret}`;
  }
  async get(path) {
    return this.request(import_pieces_common.HttpMethod.GET, path);
  }
  async put(path, body) {
    return this.request(import_pieces_common.HttpMethod.PUT, path, body);
  }
  async post(path, body) {
    return this.request(import_pieces_common.HttpMethod.POST, path, body);
  }
  async delete(path) {
    return this.request(import_pieces_common.HttpMethod.DELETE, path);
  }
  async getAllPages(basePath, pageSize = 100) {
    var _a, _b;
    let allItems = [], page = 0;
    for (; ; ) {
      let separator = basePath.includes("?") ? "&" : "?", res = await this.get(
        `${basePath}${separator}limit=${pageSize}&offset=${page}`
      ), items = (_b = (_a = res == null ? void 0 : res.items) != null ? _a : res == null ? void 0 : res.Items) != null ? _b : [];
      if (allItems.push(...items), items.length < pageSize) break;
      page += 1;
    }
    return allItems;
  }
  async request(method, path, body) {
    let response = await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${this.baseUrl}${path}`,
      headers: {
        Authorization: this.authHeader,
        "Content-Type": "application/json"
      },
      body
    });
    if (response.status < 200 || response.status >= 300)
      throw new Error(
        `Outseta API error (${response.status}): ${JSON.stringify(response.body)}`
      );
    return response.body;
  }
};

// ../ap/packages/pieces/community/outseta/src/auth.ts
var outsetaAuth = import_pieces_framework.PieceAuth.CustomAuth({
  displayName: "Outseta Admin API",
  description: `Connect Activepieces to your Outseta account using the Admin API.

**To get your credentials:**
1. Log in to your Outseta account
2. Go to **Settings \u2192 Integrations \u2192 API**
3. Copy your **API Key** and **API Secret**
4. Your domain is your Outseta subdomain, e.g. \`https://yourcompany.outseta.com\`

Need help? See [Outseta API docs](https://documenter.getpostman.com/view/3613332/outseta-rest-api-v1).`,
  required: !0,
  props: {
    domain: import_pieces_framework.Property.ShortText({
      displayName: "Outseta Domain",
      description: "Your full Outseta domain URL, e.g. https://yourcompany.outseta.com",
      required: !0
    }),
    apiKey: import_pieces_framework.Property.ShortText({
      displayName: "API Key",
      description: "Found in Outseta \u2192 Settings \u2192 Integrations \u2192 API",
      required: !0
    }),
    apiSecret: import_pieces_framework.Property.ShortText({
      displayName: "API Secret",
      description: "Found alongside your API Key in Outseta \u2192 Settings \u2192 Integrations \u2192 API",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    if (auth)
      try {
        return await new OutsetaClient({
          domain: auth.domain,
          apiKey: auth.apiKey,
          apiSecret: auth.apiSecret
        }).get("/api/v1/crm/people?limit=1"), {
          valid: !0
        };
      } catch (error) {
        return {
          valid: !1,
          error: "Invalid Api Key or secret key"
        };
      }
    return {
      valid: !1,
      error: "Invalid Api Key"
    };
  }
});

// ../ap/packages/pieces/community/outseta/src/action/get-account.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var getAccountAction = (0, import_pieces_framework2.createAction)({
  name: "get_account",
  auth: outsetaAuth,
  displayName: "Retrieve Account",
  description: "Retrieve an account by its UID, or by the email of its primary contact. Returns plan, subscription, billing address, primary contact and add-on details.",
  audience: "both",
  aiMetadata: {
    description: "Fetches a single Outseta CRM account by its UID or by its primary contact email, returning core fields plus billing address, primary contact, and current subscription/plan/add-on details. Use to read an account by either identifier. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    lookupBy: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Lookup by",
      description: "How to find the account to retrieve.",
      required: !0,
      defaultValue: "uid",
      options: {
        disabled: !1,
        options: [
          { label: "Account UID", value: "uid" },
          { label: "Primary contact email", value: "email" }
        ]
      }
    }),
    accountUid: import_pieces_framework2.Property.ShortText({
      displayName: "Account UID",
      description: 'Used when "Lookup by" is set to Account UID.',
      required: !1
    }),
    primaryContactEmail: import_pieces_framework2.Property.ShortText({
      displayName: "Primary contact email",
      description: 'Used when "Lookup by" is set to Primary contact email. The action will resolve the email to the linked account.',
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n, _o, _p, _q, _r, _s, _t, _u, _v, _w, _x, _y, _z, _A, _B, _C, _D, _E, _F, _G, _H, _I, _J, _K, _L, _M, _N, _O, _P, _Q, _R, _S, _T, _U, _V, _W, _X, _Y, _Z, __, _$, _aa;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), accountUid = context.propsValue.accountUid;
    if (context.propsValue.lookupBy === "email") {
      let email = context.propsValue.primaryContactEmail;
      if (!email)
        throw new Error("Primary contact email is required when looking up by email.");
      let person = (await client.getAllPages(
        `/api/v1/crm/people?Email=${encodeURIComponent(email)}&fields=*,PersonAccount.Account.Uid`
      )).find(
        (p) => {
          var _a2;
          return ((_a2 = p.Email) == null ? void 0 : _a2.toLowerCase()) === email.toLowerCase();
        }
      );
      if (!person)
        throw new Error(`No person found with email "${email}".`);
      if (accountUid = (_g = (_f = (_e = (Array.isArray(person.PersonAccount) ? person.PersonAccount : (_d = (_c = (_a = person.PersonAccount) == null ? void 0 : _a.items) != null ? _c : (_b = person.PersonAccount) == null ? void 0 : _b.Items) != null ? _d : [])[0]) == null ? void 0 : _e.Account) == null ? void 0 : _f.Uid) != null ? _g : null, !accountUid)
        throw new Error(`Person "${email}" is not linked to any account.`);
    }
    if (!accountUid)
      throw new Error("Account UID is required.");
    let account = await client.get(
      `/api/v1/crm/accounts/${accountUid}?fields=*,BillingAddress.*,MailingAddress.*,PrimaryContact.*,CurrentSubscription.*,CurrentSubscription.Plan.*,CurrentSubscription.Plan.PlanFamily.*,CurrentSubscription.SubscriptionAddOns.*,CurrentSubscription.SubscriptionAddOns.AddOn.*`
    ), sub = account.CurrentSubscription, rawAddOns = sub == null ? void 0 : sub.SubscriptionAddOns, addOns = Array.isArray(rawAddOns) ? rawAddOns : (_i = (_h = rawAddOns == null ? void 0 : rawAddOns.items) != null ? _h : rawAddOns == null ? void 0 : rawAddOns.Items) != null ? _i : [];
    return {
      uid: (_j = account.Uid) != null ? _j : null,
      name: (_k = account.Name) != null ? _k : null,
      account_stage: (_l = account.AccountStage) != null ? _l : null,
      account_stage_label: (_m = account.AccountStageLabel) != null ? _m : null,
      client_identifier: (_n = account.ClientIdentifier) != null ? _n : null,
      invoice_notes: (_o = account.InvoiceNotes) != null ? _o : null,
      has_logged_in: (_p = account.HasLoggedIn) != null ? _p : null,
      is_demo: (_q = account.IsDemo) != null ? _q : null,
      lifetime_revenue: (_r = account.LifetimeRevenue) != null ? _r : null,
      created: (_s = account.Created) != null ? _s : null,
      updated: (_t = account.Updated) != null ? _t : null,
      billing_address_line1: (_v = (_u = account.BillingAddress) == null ? void 0 : _u.AddressLine1) != null ? _v : null,
      billing_address_line2: (_x = (_w = account.BillingAddress) == null ? void 0 : _w.AddressLine2) != null ? _x : null,
      billing_address_city: (_z = (_y = account.BillingAddress) == null ? void 0 : _y.City) != null ? _z : null,
      billing_address_state: (_B = (_A = account.BillingAddress) == null ? void 0 : _A.State) != null ? _B : null,
      billing_address_postal_code: (_D = (_C = account.BillingAddress) == null ? void 0 : _C.PostalCode) != null ? _D : null,
      billing_address_country: (_F = (_E = account.BillingAddress) == null ? void 0 : _E.Country) != null ? _F : null,
      primary_contact_uid: (_H = (_G = account.PrimaryContact) == null ? void 0 : _G.Uid) != null ? _H : null,
      primary_contact_email: (_J = (_I = account.PrimaryContact) == null ? void 0 : _I.Email) != null ? _J : null,
      primary_contact_first_name: (_L = (_K = account.PrimaryContact) == null ? void 0 : _K.FirstName) != null ? _L : null,
      primary_contact_last_name: (_N = (_M = account.PrimaryContact) == null ? void 0 : _M.LastName) != null ? _N : null,
      current_subscription_uid: (_O = sub == null ? void 0 : sub.Uid) != null ? _O : null,
      subscription_status: (_P = sub == null ? void 0 : sub.SubscriptionStatus) != null ? _P : null,
      plan_uid: (_R = (_Q = sub == null ? void 0 : sub.Plan) == null ? void 0 : _Q.Uid) != null ? _R : null,
      plan_name: (_T = (_S = sub == null ? void 0 : sub.Plan) == null ? void 0 : _S.Name) != null ? _T : null,
      plan_family_name: (_W = (_V = (_U = sub == null ? void 0 : sub.Plan) == null ? void 0 : _U.PlanFamily) == null ? void 0 : _V.Name) != null ? _W : null,
      billing_renewal_term: (_X = sub == null ? void 0 : sub.BillingRenewalTerm) != null ? _X : null,
      renewal_date: (_Y = sub == null ? void 0 : sub.RenewalDate) != null ? _Y : null,
      start_date: (_Z = sub == null ? void 0 : sub.StartDate) != null ? _Z : null,
      end_date: (__ = sub == null ? void 0 : sub.EndDate) != null ? __ : null,
      // Unified validity date: renewal_date for recurring plans,
      // end_date for one-time plans (which have no renewal).
      validity_date: (_aa = (_$ = sub == null ? void 0 : sub.RenewalDate) != null ? _$ : sub == null ? void 0 : sub.EndDate) != null ? _aa : null,
      add_ons: addOns.map((a) => {
        var _a2, _b2, _c2, _d2, _e2, _f2;
        return {
          uid: (_c2 = (_b2 = (_a2 = a.AddOn) == null ? void 0 : _a2.Uid) != null ? _b2 : a.Uid) != null ? _c2 : null,
          name: (_e2 = (_d2 = a.AddOn) == null ? void 0 : _d2.Name) != null ? _e2 : null,
          quantity: (_f2 = a.Quantity) != null ? _f2 : null
        };
      })
    };
  }
});

// ../ap/packages/pieces/community/outseta/src/action/get-person.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var getPersonAction = (0, import_pieces_framework3.createAction)({
  name: "get_person",
  auth: outsetaAuth,
  displayName: "Retrieve Person",
  description: "Retrieve a person by email or by UID, including the linked account.",
  audience: "both",
  aiMetadata: {
    description: "Fetches a single Outseta CRM person (contact) by email or by UID, returning identity, contact, and mailing-address fields plus the linked account. Use to read a person by either identifier. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    lookupBy: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Lookup by",
      description: "How to find the person to retrieve.",
      required: !0,
      defaultValue: "email",
      options: {
        disabled: !1,
        options: [
          { label: "Email", value: "email" },
          { label: "Person UID", value: "uid" }
        ]
      }
    }),
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email",
      description: 'Used when "Lookup by" is set to Email.',
      required: !1
    }),
    personUid: import_pieces_framework3.Property.ShortText({
      displayName: "Person UID",
      description: 'Used when "Lookup by" is set to Person UID.',
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n, _o, _p, _q, _r, _s, _t, _u, _v, _w, _x, _y, _z, _A, _B, _C, _D, _E, _F, _G, _H;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), person;
    if (context.propsValue.lookupBy === "uid") {
      let uid = context.propsValue.personUid;
      if (!uid)
        throw new Error("Person UID is required when looking up by UID.");
      person = await client.get(
        `/api/v1/crm/people/${uid}?fields=*,MailingAddress.*,PersonAccount.Account.Uid,PersonAccount.Account.Name,PersonAccount.Account.AccountStage`
      );
    } else {
      let email = context.propsValue.email;
      if (!email)
        throw new Error("Email is required when looking up by Email.");
      if (person = (await client.getAllPages(
        `/api/v1/crm/people?Email=${encodeURIComponent(email)}&fields=*,PersonAccount.Account.Uid,PersonAccount.Account.Name,PersonAccount.Account.AccountStage`
      )).find(
        (p) => {
          var _a2;
          return ((_a2 = p.Email) == null ? void 0 : _a2.toLowerCase()) === email.toLowerCase();
        }
      ), !person)
        throw new Error(`No person found with email "${email}".`);
    }
    return {
      uid: (_a = person.Uid) != null ? _a : null,
      email: (_b = person.Email) != null ? _b : null,
      first_name: (_c = person.FirstName) != null ? _c : null,
      last_name: (_d = person.LastName) != null ? _d : null,
      full_name: (_e = person.FullName) != null ? _e : null,
      phone_mobile: (_f = person.PhoneMobile) != null ? _f : null,
      phone_work: (_g = person.PhoneWork) != null ? _g : null,
      title: (_h = person.Title) != null ? _h : null,
      timezone: (_i = person.Timezone) != null ? _i : null,
      language: (_j = person.Language) != null ? _j : null,
      has_logged_in: (_k = person.HasLoggedIn) != null ? _k : null,
      created: (_l = person.Created) != null ? _l : null,
      updated: (_m = person.Updated) != null ? _m : null,
      mailing_address_line1: (_o = (_n = person.MailingAddress) == null ? void 0 : _n.AddressLine1) != null ? _o : null,
      mailing_address_line2: (_q = (_p = person.MailingAddress) == null ? void 0 : _p.AddressLine2) != null ? _q : null,
      mailing_address_city: (_s = (_r = person.MailingAddress) == null ? void 0 : _r.City) != null ? _s : null,
      mailing_address_state: (_u = (_t = person.MailingAddress) == null ? void 0 : _t.State) != null ? _u : null,
      mailing_address_postal_code: (_w = (_v = person.MailingAddress) == null ? void 0 : _v.PostalCode) != null ? _w : null,
      mailing_address_country: (_y = (_x = person.MailingAddress) == null ? void 0 : _x.Country) != null ? _y : null,
      account_uid: (_B = (_A = (_z = firstMembership(person)) == null ? void 0 : _z.Account) == null ? void 0 : _A.Uid) != null ? _B : null,
      account_name: (_E = (_D = (_C = firstMembership(person)) == null ? void 0 : _C.Account) == null ? void 0 : _D.Name) != null ? _E : null,
      account_stage: (_H = (_G = (_F = firstMembership(person)) == null ? void 0 : _F.Account) == null ? void 0 : _G.AccountStage) != null ? _H : null
    };
  }
});
function firstMembership(person) {
  var _a, _b, _c, _d;
  return (Array.isArray(person == null ? void 0 : person.PersonAccount) ? person.PersonAccount : (_d = (_c = (_a = person == null ? void 0 : person.PersonAccount) == null ? void 0 : _a.items) != null ? _c : (_b = person == null ? void 0 : person.PersonAccount) == null ? void 0 : _b.Items) != null ? _d : [])[0];
}

// ../ap/packages/pieces/community/outseta/src/action/get-deal.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/outseta/src/common/dropdowns.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
function makeClient(auth) {
  var _a;
  let a = auth;
  return (_a = a == null ? void 0 : a.props) != null && _a.domain ? new OutsetaClient({
    domain: a.props.domain,
    apiKey: a.props.apiKey,
    apiSecret: a.props.apiSecret
  }) : null;
}
function pipelineDropdown(options) {
  var _a, _b, _c;
  return import_pieces_framework4.Property.Dropdown({
    auth: outsetaAuth,
    displayName: (_a = options == null ? void 0 : options.displayName) != null ? _a : "Pipeline",
    description: (_b = options == null ? void 0 : options.description) != null ? _b : "Select the deal pipeline.",
    refreshers: [],
    required: (_c = options == null ? void 0 : options.required) != null ? _c : !0,
    options: async ({ auth }) => {
      var _a2, _b2;
      let client = makeClient(auth);
      if (!client)
        return { disabled: !0, options: [], placeholder: "Connect your Outseta account first." };
      try {
        let res = await client.get("/api/v1/crm/deals/pipelines?limit=100");
        return {
          disabled: !1,
          options: ((_b2 = (_a2 = res == null ? void 0 : res.items) != null ? _a2 : res == null ? void 0 : res.Items) != null ? _b2 : []).map((p) => {
            var _a3;
            return {
              label: (_a3 = p.Name) != null ? _a3 : p.Uid,
              value: p.Uid
            };
          })
        };
      } catch (e) {
        return { disabled: !0, options: [], placeholder: "Failed to load pipelines." };
      }
    }
  });
}
function pipelineStageDropdown(options) {
  var _a, _b, _c;
  return import_pieces_framework4.Property.Dropdown({
    auth: outsetaAuth,
    displayName: (_a = options == null ? void 0 : options.displayName) != null ? _a : "Pipeline Stage",
    description: (_b = options == null ? void 0 : options.description) != null ? _b : "Select the pipeline stage.",
    refreshers: ["pipelineUid"],
    required: (_c = options == null ? void 0 : options.required) != null ? _c : !0,
    options: async ({ auth, pipelineUid }) => {
      var _a2, _b2, _c2, _d, _e;
      let client = makeClient(auth);
      if (!client)
        return { disabled: !0, options: [], placeholder: "Connect your Outseta account first." };
      if (!pipelineUid)
        return { disabled: !0, options: [], placeholder: "Select a pipeline first." };
      try {
        let pipeline = await client.get(`/api/v1/crm/deals/pipelines/${pipelineUid}`);
        return {
          disabled: !1,
          options: ((_e = (_d = (_c2 = (_a2 = pipeline == null ? void 0 : pipeline.DealPipelineStages) == null ? void 0 : _a2.items) != null ? _c2 : (_b2 = pipeline == null ? void 0 : pipeline.DealPipelineStages) == null ? void 0 : _b2.Items) != null ? _d : pipeline == null ? void 0 : pipeline.DealPipelineStages) != null ? _e : []).map((s) => {
            var _a3;
            return {
              label: (_a3 = s.Name) != null ? _a3 : s.Uid,
              value: s.Uid
            };
          })
        };
      } catch (e) {
        return { disabled: !0, options: [], placeholder: "Failed to load pipeline stages." };
      }
    }
  });
}
function planUidDropdown(options) {
  var _a, _b, _c, _d;
  return import_pieces_framework4.Property.Dropdown({
    auth: outsetaAuth,
    displayName: (_a = options == null ? void 0 : options.displayName) != null ? _a : "Plan",
    description: (_b = options == null ? void 0 : options.description) != null ? _b : "Select the subscription plan.",
    refreshers: (_c = options == null ? void 0 : options.refreshers) != null ? _c : ["accountUid"],
    required: (_d = options == null ? void 0 : options.required) != null ? _d : !0,
    options: async ({ auth, accountUid }) => {
      var _a2, _b2, _c2, _d2, _e, _f;
      let client = makeClient(auth);
      if (!client)
        return { disabled: !0, options: [], placeholder: "Connect your Outseta account first." };
      let planFamilyUid = null;
      if (accountUid)
        try {
          let account = await client.get(
            `/api/v1/crm/accounts/${accountUid}?fields=CurrentSubscription.Plan.PlanFamily.*`
          );
          planFamilyUid = (_d2 = (_c2 = (_b2 = (_a2 = account == null ? void 0 : account.CurrentSubscription) == null ? void 0 : _a2.Plan) == null ? void 0 : _b2.PlanFamily) == null ? void 0 : _c2.Uid) != null ? _d2 : null;
        } catch (e) {
          return { disabled: !0, options: [], placeholder: "Failed to load account. Check the Account UID." };
        }
      try {
        let res = await client.get("/api/v1/billing/plans?limit=100"), items = (_f = (_e = res == null ? void 0 : res.items) != null ? _e : res == null ? void 0 : res.Items) != null ? _f : [];
        return planFamilyUid && (items = items.filter((p) => {
          var _a3;
          return ((_a3 = p.PlanFamily) == null ? void 0 : _a3.Uid) === planFamilyUid;
        })), {
          disabled: !1,
          options: items.map((p) => {
            var _a3;
            return {
              label: (_a3 = p.Name) != null ? _a3 : p.Uid,
              value: p.Uid
            };
          })
        };
      } catch (e) {
        return { disabled: !0, options: [], placeholder: "Failed to load plans." };
      }
    }
  });
}
function addOnUidDropdown(options) {
  var _a, _b;
  return import_pieces_framework4.Property.Dropdown({
    auth: outsetaAuth,
    displayName: "Add-On",
    description: "Select the metered add-on.",
    refreshers: (_a = options == null ? void 0 : options.refreshers) != null ? _a : ["accountUid"],
    required: (_b = options == null ? void 0 : options.required) != null ? _b : !0,
    options: async ({ auth, accountUid }) => {
      var _a2, _b2, _c, _d, _e;
      let client = makeClient(auth);
      if (!client)
        return { disabled: !0, options: [], placeholder: "Connect your Outseta account first." };
      if (accountUid)
        try {
          let account = await client.get(
            `/api/v1/crm/accounts/${accountUid}?fields=CurrentSubscription.SubscriptionAddOns.*,CurrentSubscription.SubscriptionAddOns.AddOn.*`
          ), rawAddOns = (_a2 = account == null ? void 0 : account.CurrentSubscription) == null ? void 0 : _a2.SubscriptionAddOns;
          return {
            disabled: !1,
            options: (Array.isArray(rawAddOns) ? rawAddOns : (_c = (_b2 = rawAddOns == null ? void 0 : rawAddOns.items) != null ? _b2 : rawAddOns == null ? void 0 : rawAddOns.Items) != null ? _c : []).filter((sa) => {
              var _a3;
              return (_a3 = sa.AddOn) == null ? void 0 : _a3.Uid;
            }).map((sa) => {
              var _a3;
              return {
                label: (_a3 = sa.AddOn.Name) != null ? _a3 : sa.AddOn.Uid,
                value: sa.AddOn.Uid
              };
            })
          };
        } catch (e) {
          return { disabled: !0, options: [], placeholder: "Failed to load account. Check the Account UID." };
        }
      try {
        let res = await client.get("/api/v1/billing/addons?limit=100");
        return {
          disabled: !1,
          options: ((_e = (_d = res == null ? void 0 : res.items) != null ? _d : res == null ? void 0 : res.Items) != null ? _e : []).map((a) => {
            var _a3;
            return {
              label: (_a3 = a.Name) != null ? _a3 : a.Uid,
              value: a.Uid
            };
          })
        };
      } catch (e) {
        return { disabled: !0, options: [], placeholder: "Failed to load add-ons." };
      }
    }
  });
}
function emailListUidDropdown(options) {
  var _a;
  return import_pieces_framework4.Property.Dropdown({
    auth: outsetaAuth,
    displayName: "Email List",
    description: "Select the email list.",
    refreshers: [],
    required: (_a = options == null ? void 0 : options.required) != null ? _a : !0,
    options: async ({ auth }) => {
      var _a2, _b;
      let client = makeClient(auth);
      if (!client)
        return { disabled: !0, options: [], placeholder: "Connect your Outseta account first." };
      try {
        let res = await client.get("/api/v1/email/lists?limit=100");
        return {
          disabled: !1,
          options: ((_b = (_a2 = res == null ? void 0 : res.items) != null ? _a2 : res == null ? void 0 : res.Items) != null ? _b : []).map((l) => {
            var _a3;
            return {
              label: (_a3 = l.Name) != null ? _a3 : l.Uid,
              value: l.Uid
            };
          })
        };
      } catch (e) {
        return { disabled: !0, options: [], placeholder: "Failed to load email lists." };
      }
    }
  });
}
function discountDropdown(options) {
  var _a;
  return import_pieces_framework4.Property.Dropdown({
    auth: outsetaAuth,
    displayName: "Discount",
    description: "Select the discount coupon to apply.",
    refreshers: [],
    required: (_a = options == null ? void 0 : options.required) != null ? _a : !0,
    options: async ({ auth }) => {
      var _a2, _b;
      let client = makeClient(auth);
      if (!client)
        return { disabled: !0, options: [], placeholder: "Connect your Outseta account first." };
      try {
        let res = await client.get("/api/v1/billing/discountcoupons?limit=100");
        return {
          disabled: !1,
          options: ((_b = (_a2 = res == null ? void 0 : res.items) != null ? _a2 : res == null ? void 0 : res.Items) != null ? _b : []).filter((d) => d.IsActive !== !1).map((d) => {
            var _a3, _b2;
            return {
              label: `${(_b2 = (_a3 = d.Name) != null ? _a3 : d.UniqueIdentifier) != null ? _b2 : d.Uid}${d.PercentOff ? ` (${d.PercentOff}% off)` : d.AmountOff ? ` ($${d.AmountOff} off)` : ""}`,
              value: d.Uid
            };
          })
        };
      } catch (e) {
        return { disabled: !0, options: [], placeholder: "Failed to load discounts." };
      }
    }
  });
}

// ../ap/packages/pieces/community/outseta/src/action/get-deal.ts
var getDealAction = (0, import_pieces_framework5.createAction)({
  name: "get_deal",
  auth: outsetaAuth,
  displayName: "Retrieve Deal",
  description: "Retrieve a deal by its UID, or by the email of the contact associated with the deal plus the pipeline.",
  audience: "both",
  aiMetadata: {
    description: "Fetches a single Outseta CRM deal by its UID, or by the contact email plus pipeline, returning amount, due date, pipeline stage, and the associated account. Use to read a deal by either identifier. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    lookupBy: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Lookup by",
      description: "How to find the deal to retrieve.",
      required: !0,
      defaultValue: "uid",
      options: {
        disabled: !1,
        options: [
          { label: "Deal UID", value: "uid" },
          { label: "Contact email + pipeline", value: "email" }
        ]
      }
    }),
    dealUid: import_pieces_framework5.Property.ShortText({
      displayName: "Deal UID",
      description: 'Used when "Lookup by" is set to Deal UID.',
      required: !1
    }),
    contactEmail: import_pieces_framework5.Property.ShortText({
      displayName: "Contact Email",
      description: 'Used when "Lookup by" is set to Contact email + pipeline.',
      required: !1
    }),
    pipelineUid: pipelineDropdown({
      required: !1,
      description: 'Used when "Lookup by" is set to Contact email + pipeline. Required in that mode.'
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n, _o, _p;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), match;
    if (context.propsValue.lookupBy === "uid") {
      let uid = context.propsValue.dealUid;
      if (!uid)
        throw new Error("Deal UID is required when looking up by UID.");
      match = await client.get(
        `/api/v1/crm/deals/${uid}?fields=Uid,Name,Amount,DueDate,Created,Updated,AssignedToPersonClientIdentifier,DealPipelineStage.Uid,DealPipelineStage.Name,DealPipelineStage.DealPipeline.Uid,DealPeople.Person.Uid,DealPeople.Person.Email,Account.Uid,Account.Name`
      );
    } else {
      let contactEmail = (_a = context.propsValue.contactEmail) == null ? void 0 : _a.toLowerCase(), pipelineUid = context.propsValue.pipelineUid;
      if (!contactEmail || !pipelineUid)
        throw new Error(
          "Contact email and pipeline are both required when looking up by email + pipeline."
        );
      if (match = (await client.getAllPages(
        `/api/v1/crm/deals?DealPipelineStage.DealPipeline.Uid=${encodeURIComponent(pipelineUid)}&fields=Uid,Name,Amount,DueDate,Created,Updated,AssignedToPersonClientIdentifier,DealPipelineStage.Uid,DealPipelineStage.Name,DealPipelineStage.DealPipeline.Uid,DealPeople.Person.Uid,DealPeople.Person.Email,Account.Uid,Account.Name`
      )).find((deal) => {
        var _a2, _b2, _c2, _d2, _e2, _f2, _g2;
        let hasContact = ((_e2 = (_d2 = (_c2 = (_a2 = deal.DealPeople) == null ? void 0 : _a2.items) != null ? _c2 : (_b2 = deal.DealPeople) == null ? void 0 : _b2.Items) != null ? _d2 : deal.DealPeople) != null ? _e2 : []).some(
          (dp) => {
            var _a3, _b3;
            return ((_b3 = (_a3 = dp.Person) == null ? void 0 : _a3.Email) == null ? void 0 : _b3.toLowerCase()) === contactEmail;
          }
        ), inPipeline = ((_g2 = (_f2 = deal.DealPipelineStage) == null ? void 0 : _f2.DealPipeline) == null ? void 0 : _g2.Uid) === pipelineUid;
        return hasContact && inPipeline;
      }), !match)
        throw new Error(
          `No deal found for contact "${context.propsValue.contactEmail}" in the selected pipeline.`
        );
    }
    return {
      uid: (_b = match.Uid) != null ? _b : null,
      name: (_c = match.Name) != null ? _c : null,
      amount: (_d = match.Amount) != null ? _d : null,
      due_date: (_e = match.DueDate) != null ? _e : null,
      pipeline_stage_uid: (_g = (_f = match.DealPipelineStage) == null ? void 0 : _f.Uid) != null ? _g : null,
      pipeline_stage_name: (_i = (_h = match.DealPipelineStage) == null ? void 0 : _h.Name) != null ? _i : null,
      account_uid: (_k = (_j = match.Account) == null ? void 0 : _j.Uid) != null ? _k : null,
      account_name: (_m = (_l = match.Account) == null ? void 0 : _l.Name) != null ? _m : null,
      assigned_to_client_identifier: (_n = match.AssignedToPersonClientIdentifier) != null ? _n : null,
      created: (_o = match.Created) != null ? _o : null,
      updated: (_p = match.Updated) != null ? _p : null
    };
  }
});

// ../ap/packages/pieces/community/outseta/src/action/get-subscription.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var getSubscriptionAction = (0, import_pieces_framework6.createAction)({
  name: "get_subscription",
  auth: outsetaAuth,
  displayName: "Retrieve Subscription",
  description: "Retrieve a subscription by its UID, or fetch the current subscription of an account by Account UID. Returns plan, billing terms, renewal dates, discount, and add-on details.",
  audience: "both",
  aiMetadata: {
    description: "Fetches a subscription by its UID, or an account's current subscription by account UID, returning plan, billing term, renewal dates, discount, and add-ons. Use to read subscription/billing state; for account-level fields use Retrieve Account. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    lookupBy: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Lookup by",
      description: "How to find the subscription to retrieve.",
      required: !0,
      defaultValue: "subscriptionUid",
      options: {
        disabled: !1,
        options: [
          { label: "Subscription UID", value: "subscriptionUid" },
          { label: "Account UID (current subscription)", value: "accountUid" }
        ]
      }
    }),
    subscriptionUid: import_pieces_framework6.Property.ShortText({
      displayName: "Subscription UID",
      required: !1,
      description: 'Used when "Lookup by" is set to Subscription UID.'
    }),
    accountUid: import_pieces_framework6.Property.ShortText({
      displayName: "Account UID",
      required: !1,
      description: `Used when "Lookup by" is set to Account UID. Resolves to the account's current subscription.`
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n, _o, _p, _q, _r, _s, _t, _u, _v, _w, _x, _y, _z, _A;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), subscriptionUid = context.propsValue.subscriptionUid;
    if (context.propsValue.lookupBy === "accountUid") {
      let accountUid = context.propsValue.accountUid;
      if (!accountUid)
        throw new Error("Account UID is required when looking up by Account UID.");
      let account = await client.get(
        `/api/v1/crm/accounts/${accountUid}?fields=Uid,CurrentSubscription.Uid`
      );
      if (subscriptionUid = (_b = (_a = account == null ? void 0 : account.CurrentSubscription) == null ? void 0 : _a.Uid) != null ? _b : null, !subscriptionUid)
        throw new Error(`Account ${accountUid} does not have an active subscription.`);
    }
    if (!subscriptionUid)
      throw new Error("Subscription UID is required.");
    let sub = await client.get(
      `/api/v1/billing/subscriptions/${subscriptionUid}?fields=*,Plan.*,Plan.PlanFamily.*,Account.*,SubscriptionAddOns.*,SubscriptionAddOns.AddOn.*`
    ), rawAddOns = sub == null ? void 0 : sub.SubscriptionAddOns, addOns = Array.isArray(rawAddOns) ? rawAddOns : (_d = (_c = rawAddOns == null ? void 0 : rawAddOns.items) != null ? _c : rawAddOns == null ? void 0 : rawAddOns.Items) != null ? _d : [];
    return {
      uid: (_e = sub.Uid) != null ? _e : null,
      account_uid: (_g = (_f = sub.Account) == null ? void 0 : _f.Uid) != null ? _g : null,
      account_name: (_i = (_h = sub.Account) == null ? void 0 : _h.Name) != null ? _i : null,
      subscription_status: (_j = sub.SubscriptionStatus) != null ? _j : null,
      plan_uid: (_l = (_k = sub.Plan) == null ? void 0 : _k.Uid) != null ? _l : null,
      plan_name: (_n = (_m = sub.Plan) == null ? void 0 : _m.Name) != null ? _n : null,
      plan_family_name: (_q = (_p = (_o = sub.Plan) == null ? void 0 : _o.PlanFamily) == null ? void 0 : _p.Name) != null ? _q : null,
      billing_renewal_term: (_r = sub.BillingRenewalTerm) != null ? _r : null,
      rate: (_s = sub.Rate) != null ? _s : null,
      discount_code: (_t = sub.DiscountCode) != null ? _t : null,
      start_date: (_u = sub.StartDate) != null ? _u : null,
      end_date: (_v = sub.EndDate) != null ? _v : null,
      renewal_date: (_w = sub.RenewalDate) != null ? _w : null,
      validity_date: (_y = (_x = sub.RenewalDate) != null ? _x : sub.EndDate) != null ? _y : null,
      created: (_z = sub.Created) != null ? _z : null,
      updated: (_A = sub.Updated) != null ? _A : null,
      add_ons: addOns.map((a) => {
        var _a2, _b2, _c2, _d2, _e2, _f2;
        return {
          uid: (_c2 = (_b2 = (_a2 = a.AddOn) == null ? void 0 : _a2.Uid) != null ? _b2 : a.Uid) != null ? _c2 : null,
          name: (_e2 = (_d2 = a.AddOn) == null ? void 0 : _d2.Name) != null ? _e2 : null,
          quantity: (_f2 = a.Quantity) != null ? _f2 : null
        };
      })
    };
  }
});

// ../ap/packages/pieces/community/outseta/src/action/get-last-payment.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var getLastPaymentAction = (0, import_pieces_framework7.createAction)({
  name: "get_last_payment",
  auth: outsetaAuth,
  displayName: "Get Last Payment for Account",
  description: "Retrieve the most recent payment transaction for an account. Returns found=false if no payment has ever been recorded.",
  audience: "both",
  aiMetadata: {
    description: "Returns the single most recent payment transaction for an account, or found=false if none exists. Use for the latest payment only; for the full payment/refund/invoice history use List Account Transactions. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    accountUid: import_pieces_framework7.Property.ShortText({
      displayName: "Account UID",
      required: !0,
      description: "The UID of the account to retrieve the last payment for."
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k;
    let res = await new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }).get(
      `/api/v1/billing/transactions/${context.propsValue.accountUid}?BillingTransactionType=${PAYMENT_TRANSACTION_TYPE}&limit=1&orderBy=Created%20DESC&fields=*,Invoice.*`
    ), items = (_b = (_a = res == null ? void 0 : res.items) != null ? _a : res == null ? void 0 : res.Items) != null ? _b : [];
    if (items.length === 0)
      return {
        found: !1,
        uid: null,
        amount: null,
        date: null,
        invoice_uid: null,
        invoice_number: null,
        created: null
      };
    let payment = items[0];
    return {
      found: !0,
      uid: (_c = payment.Uid) != null ? _c : null,
      amount: (_d = payment.Amount) != null ? _d : null,
      date: (_f = (_e = payment.TransactionDate) != null ? _e : payment.Created) != null ? _f : null,
      invoice_uid: (_h = (_g = payment.Invoice) == null ? void 0 : _g.Uid) != null ? _h : null,
      invoice_number: (_j = (_i = payment.Invoice) == null ? void 0 : _i.Number) != null ? _j : null,
      created: (_k = payment.Created) != null ? _k : null
    };
  }
}), PAYMENT_TRANSACTION_TYPE = 2;

// ../ap/packages/pieces/community/outseta/src/action/create-account.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/outseta/src/common/custom-properties.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
function customPropertiesProp(entityType) {
  return import_pieces_framework8.Property.DynamicProperties({
    displayName: "Custom Properties",
    description: `Custom properties defined on ${entityType} in your Outseta workspace. The widget for each field is automatically inferred from the property's ControlType (text, date, single-select, multi-select).`,
    required: !1,
    auth: outsetaAuth,
    refreshers: [],
    props: async ({ auth }) => {
      var _a, _b, _c, _d, _e;
      let a = auth;
      if (!((_a = a == null ? void 0 : a.props) != null && _a.domain)) return {};
      let client = new OutsetaClient({
        domain: a.props.domain,
        apiKey: a.props.apiKey,
        apiSecret: a.props.apiSecret
      }), res;
      try {
        res = await client.get(
          `/api/v1/attributes/${entityType}/definitions?limit=100`
        );
      } catch (e) {
        return {};
      }
      let definitions = (_c = (_b = res == null ? void 0 : res.items) != null ? _b : res == null ? void 0 : res.Items) != null ? _c : [], props = {};
      for (let def of definitions) {
        if (def.Hidden) continue;
        let sysName = def.SystemName, label = (_d = def.Label) != null ? _d : sysName, description = parseControlParams(def.ControlParams).HintText, options = ((_e = parseControlParams(def.ControlParams).Options) != null ? _e : []).map((o) => ({ label: o, value: o }));
        switch (def.ControlType) {
          case "Date":
            props[sysName] = import_pieces_framework8.Property.DateTime({ displayName: label, description, required: !1 });
            break;
          case "Select":
            props[sysName] = import_pieces_framework8.Property.StaticDropdown({
              displayName: label,
              description,
              required: !1,
              options: { disabled: !1, options }
            });
            break;
          case "CheckboxList":
            props[sysName] = import_pieces_framework8.Property.StaticMultiSelectDropdown({
              displayName: label,
              description,
              required: !1,
              options: { disabled: !1, options }
            });
            break;
          default:
            props[sysName] = import_pieces_framework8.Property.ShortText({ displayName: label, description, required: !1 });
        }
      }
      return props;
    }
  });
}
function mergeCustomProperties(entity, customProps) {
  if (!customProps) return entity;
  let target = entity;
  for (let [key, value] of Object.entries(customProps))
    value == null || value === "" || (Array.isArray(value) ? target[key] = JSON.stringify(value) : target[key] = value);
  return entity;
}
function parseControlParams(raw) {
  if (!raw) return {};
  try {
    return JSON.parse(raw);
  } catch (e) {
    return {};
  }
}

// ../ap/packages/pieces/community/outseta/src/action/create-account.ts
var createAccountAction = (0, import_pieces_framework9.createAction)({
  name: "create_account",
  auth: outsetaAuth,
  displayName: "Create Account",
  description: "Create a new account in Outseta CRM. Optionally start a subscription on a plan in the same call.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new CRM account, optionally with a primary contact person and an initial subscription on a plan. Use to onboard a new customer; to attach a person to an existing account use Manage Account Membership, and to start a subscription on an existing account use Change Account Plan. Not idempotent: each call creates a new account.",
    idempotent: !1
  },
  props: {
    name: import_pieces_framework9.Property.ShortText({
      displayName: "Name",
      required: !0
    }),
    accountStage: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Account Stage",
      required: !1,
      options: {
        options: [
          { label: "Lead", value: 1 },
          { label: "Trialing", value: 2 },
          { label: "Subscribing", value: 3 },
          { label: "Delinquent", value: 4 },
          { label: "Cancelling", value: 5 },
          { label: "Expired", value: 6 },
          { label: "Demo", value: 7 }
        ]
      }
    }),
    clientIdentifier: import_pieces_framework9.Property.ShortText({
      displayName: "Client Identifier",
      required: !1,
      description: "A custom identifier you assign to this account (e.g. your internal customer ID)."
    }),
    invoiceNotes: import_pieces_framework9.Property.LongText({
      displayName: "Invoice Notes",
      required: !1,
      description: "Notes that appear on invoices sent to this account."
    }),
    addressLine1: import_pieces_framework9.Property.ShortText({
      displayName: "Address Line 1",
      required: !1
    }),
    addressLine2: import_pieces_framework9.Property.ShortText({
      displayName: "Address Line 2",
      required: !1
    }),
    city: import_pieces_framework9.Property.ShortText({
      displayName: "City",
      required: !1
    }),
    state: import_pieces_framework9.Property.ShortText({
      displayName: "State/Region",
      required: !1
    }),
    postalCode: import_pieces_framework9.Property.ShortText({
      displayName: "Postal Code",
      required: !1
    }),
    country: import_pieces_framework9.Property.ShortText({
      displayName: "Country",
      required: !1
    }),
    primaryContactPersonUid: import_pieces_framework9.Property.ShortText({
      displayName: "Primary Contact Person UID",
      required: !1,
      description: "Optional. UID of an existing person to set as primary contact on the new account. Required by Outseta when starting a subscription with a paid plan."
    }),
    planUid: planUidDropdown({
      displayName: "Starting Plan",
      description: "Optional. If set, the account is created with an active subscription on this plan.",
      refreshers: [],
      required: !1
    }),
    billingRenewalTerm: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Billing Renewal Term",
      required: !1,
      defaultValue: 1,
      description: "Used only when Starting Plan is set. Defaults to Monthly.",
      options: {
        disabled: !1,
        options: [
          { label: "Monthly", value: 1 },
          { label: "Annual", value: 2 },
          { label: "Quarterly", value: 3 },
          { label: "One-time", value: 4 }
        ]
      }
    }),
    customProperties: customPropertiesProp("Account")
  },
  async run(context) {
    var _a;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), body = {
      Name: context.propsValue.name
    };
    if (context.propsValue.accountStage != null && (body.AccountStage = context.propsValue.accountStage), context.propsValue.clientIdentifier && (body.ClientIdentifier = context.propsValue.clientIdentifier), context.propsValue.invoiceNotes && (body.InvoiceNotes = context.propsValue.invoiceNotes), context.propsValue.addressLine1 || context.propsValue.city || context.propsValue.country) {
      let address = {};
      context.propsValue.addressLine1 && (address.AddressLine1 = context.propsValue.addressLine1), context.propsValue.addressLine2 && (address.AddressLine2 = context.propsValue.addressLine2), context.propsValue.city && (address.City = context.propsValue.city), context.propsValue.state && (address.State = context.propsValue.state), context.propsValue.postalCode && (address.PostalCode = context.propsValue.postalCode), context.propsValue.country && (address.Country = context.propsValue.country), body.BillingAddress = address;
    }
    return context.propsValue.primaryContactPersonUid && (body.PersonAccount = [
      {
        Person: { Uid: context.propsValue.primaryContactPersonUid },
        IsPrimary: !0
      }
    ]), context.propsValue.planUid && (body.Subscriptions = [
      {
        Plan: { Uid: context.propsValue.planUid },
        BillingRenewalTerm: (_a = context.propsValue.billingRenewalTerm) != null ? _a : 1
      }
    ]), mergeCustomProperties(body, context.propsValue.customProperties), client.post("/api/v1/crm/accounts", body);
  }
});

// ../ap/packages/pieces/community/outseta/src/action/create-deal.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var createDealAction = (0, import_pieces_framework10.createAction)({
  name: "create_deal",
  auth: outsetaAuth,
  displayName: "Create Deal",
  description: "Create a new deal in the CRM.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new CRM deal in a pipeline stage, optionally linked to a person and account. Use when you always want a new deal; to avoid duplicates keyed on contact email use Find or Add Deal instead. Not idempotent: each call creates a new deal.",
    idempotent: !1
  },
  props: {
    name: import_pieces_framework10.Property.ShortText({
      displayName: "Deal Name",
      required: !0
    }),
    pipelineUid: pipelineDropdown(),
    pipelineStageUid: pipelineStageDropdown({
      description: "Pipeline stage for the deal."
    }),
    contactEmail: import_pieces_framework10.Property.ShortText({
      displayName: "Contact Email",
      required: !1,
      description: "Email of the person to associate with this deal."
    }),
    amount: import_pieces_framework10.Property.Number({
      displayName: "Amount",
      required: !1
    }),
    accountUid: import_pieces_framework10.Property.ShortText({
      displayName: "Account UID",
      required: !1,
      description: "UID of the account to associate with this deal."
    }),
    customProperties: customPropertiesProp("Deal")
  },
  async run(context) {
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), body = {
      Name: context.propsValue.name,
      DealPipelineStage: { Uid: context.propsValue.pipelineStageUid }
    };
    return context.propsValue.amount != null && (body.Amount = context.propsValue.amount), context.propsValue.accountUid && (body.Account = { Uid: context.propsValue.accountUid }), context.propsValue.contactEmail && (body.DealPeople = [{ Person: { Email: context.propsValue.contactEmail } }]), mergeCustomProperties(body, context.propsValue.customProperties), client.post("/api/v1/crm/deals", body);
  }
});

// ../ap/packages/pieces/community/outseta/src/action/find-or-add-person.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var findOrAddPersonAction = (0, import_pieces_framework11.createAction)({
  name: "find_or_add_person",
  auth: outsetaAuth,
  displayName: "Find or Add Person",
  description: "Search for a person by email. If not found, create a new one.",
  audience: "both",
  aiMetadata: {
    description: "Looks up an Outseta CRM person by exact (case-insensitive) email match and returns it; if no match exists, creates a new person with that email plus optional name, phone, and mailing address. Use to ensure a contact exists without creating a duplicate. Email is the dedup key, so reusing the same email is safe; supplying a different email creates a new person.",
    idempotent: !1
  },
  props: {
    email: import_pieces_framework11.Property.ShortText({
      displayName: "Email",
      required: !0,
      description: "Email to search for. If no match is found, a new person is created with this email."
    }),
    firstName: import_pieces_framework11.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    lastName: import_pieces_framework11.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    phoneMobile: import_pieces_framework11.Property.ShortText({
      displayName: "Mobile #",
      required: !1
    }),
    phoneWork: import_pieces_framework11.Property.ShortText({
      displayName: "Work #",
      required: !1
    }),
    addressLine1: import_pieces_framework11.Property.ShortText({
      displayName: "Address Line 1",
      required: !1
    }),
    addressLine2: import_pieces_framework11.Property.ShortText({
      displayName: "Address Line 2",
      required: !1
    }),
    city: import_pieces_framework11.Property.ShortText({
      displayName: "City",
      required: !1
    }),
    state: import_pieces_framework11.Property.ShortText({
      displayName: "State/Region",
      required: !1
    }),
    postalCode: import_pieces_framework11.Property.ShortText({
      displayName: "Postal Code",
      required: !1
    }),
    country: import_pieces_framework11.Property.ShortText({
      displayName: "Country",
      required: !1
    }),
    customProperties: customPropertiesProp("Person")
  },
  async run(context) {
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), exactMatch = (await client.getAllPages(
      `/api/v1/crm/people?Email=${encodeURIComponent(context.propsValue.email)}`
    )).find(
      (item) => {
        var _a;
        return ((_a = item.Email) == null ? void 0 : _a.toLowerCase()) === context.propsValue.email.toLowerCase();
      }
    );
    if (exactMatch)
      return { created: !1, person: exactMatch };
    let body = {
      Email: context.propsValue.email
    };
    if (context.propsValue.firstName && (body.FirstName = context.propsValue.firstName), context.propsValue.lastName && (body.LastName = context.propsValue.lastName), context.propsValue.phoneMobile && (body.PhoneMobile = context.propsValue.phoneMobile), context.propsValue.phoneWork && (body.PhoneWork = context.propsValue.phoneWork), context.propsValue.addressLine1 || context.propsValue.city || context.propsValue.country) {
      let address = {};
      context.propsValue.addressLine1 && (address.AddressLine1 = context.propsValue.addressLine1), context.propsValue.addressLine2 && (address.AddressLine2 = context.propsValue.addressLine2), context.propsValue.city && (address.City = context.propsValue.city), context.propsValue.state && (address.State = context.propsValue.state), context.propsValue.postalCode && (address.PostalCode = context.propsValue.postalCode), context.propsValue.country && (address.Country = context.propsValue.country), body.MailingAddress = address;
    }
    return mergeCustomProperties(body, context.propsValue.customProperties), { created: !0, person: await client.post("/api/v1/crm/people", body) };
  }
});

// ../ap/packages/pieces/community/outseta/src/action/find-or-add-deal.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var findOrAddDealAction = (0, import_pieces_framework12.createAction)({
  name: "find_or_add_deal",
  auth: outsetaAuth,
  displayName: "Find or Add Deal",
  description: "Search for a deal by contact email and pipeline. If not found, create a new one.",
  audience: "both",
  aiMetadata: {
    description: "Looks up a deal by contact email within a pipeline and returns it, creating one only if none exists. Use to upsert a deal without duplicating; to always create use Create Deal. Pipeline stage is required only on the create path. Idempotent: a repeat call returns the same deal rather than creating another.",
    idempotent: !0
  },
  props: {
    contactEmail: import_pieces_framework12.Property.ShortText({
      displayName: "Contact Email",
      required: !0,
      description: "Email of the contact associated with the deal. Used to search for existing deals."
    }),
    pipelineUid: pipelineDropdown(),
    pipelineStageUid: pipelineStageDropdown({
      required: !1,
      description: "Pipeline stage for the deal (only required when creating a new one)."
    }),
    name: import_pieces_framework12.Property.ShortText({
      displayName: "Deal Name",
      required: !0,
      description: "Name of the deal (used when creating a new deal)."
    }),
    amount: import_pieces_framework12.Property.Number({
      displayName: "Amount",
      required: !1,
      description: "Deal amount."
    }),
    accountUid: import_pieces_framework12.Property.ShortText({
      displayName: "Account UID",
      required: !1,
      description: "UID of the account to associate with the deal."
    }),
    assignedToPersonClientIdentifier: import_pieces_framework12.Property.ShortText({
      displayName: "Assigned To (Person Client Identifier)",
      required: !1,
      description: "Client identifier of the person to assign the deal to."
    }),
    customProperties: customPropertiesProp("Deal")
  },
  async run(context) {
    if (!context.propsValue.pipelineUid)
      throw new Error("Pipeline is required.");
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), person = (await client.getAllPages(
      `/api/v1/crm/people?Email=${encodeURIComponent(context.propsValue.contactEmail)}`
    )).find(
      (item) => {
        var _a;
        return ((_a = item.Email) == null ? void 0 : _a.toLowerCase()) === context.propsValue.contactEmail.toLowerCase();
      }
    );
    if (person) {
      let existingDeal = (await client.getAllPages(
        `/api/v1/crm/deals?DealPipelineStage.DealPipeline.Uid=${encodeURIComponent(context.propsValue.pipelineUid)}&fields=Uid,Name,Amount,DealPipelineStage.Uid,DealPipelineStage.DealPipeline.Uid,DealPeople.Person.Uid,DealPeople.Person.Email,Account.Uid`
      )).find(
        (deal) => {
          var _a, _b, _c, _d, _e, _f, _g;
          return ((_b = (_a = deal.DealPipelineStage) == null ? void 0 : _a.DealPipeline) == null ? void 0 : _b.Uid) === context.propsValue.pipelineUid && ((_g = (_f = (_e = (_c = deal.DealPeople) == null ? void 0 : _c.items) != null ? _e : (_d = deal.DealPeople) == null ? void 0 : _d.Items) != null ? _f : deal.DealPeople) != null ? _g : []).some(
            (dp) => {
              var _a2;
              return ((_a2 = dp.Person) == null ? void 0 : _a2.Uid) === person.Uid;
            }
          );
        }
      );
      if (existingDeal)
        return { created: !1, deal: existingDeal };
    }
    if (!context.propsValue.pipelineStageUid)
      throw new Error(
        "Pipeline Stage is required to create a new deal. Select a stage to enable the create path."
      );
    let body = {
      Name: context.propsValue.name,
      DealPipelineStage: { Uid: context.propsValue.pipelineStageUid }
    };
    return context.propsValue.amount !== void 0 && context.propsValue.amount !== null && (body.Amount = context.propsValue.amount), context.propsValue.accountUid && (body.Account = { Uid: context.propsValue.accountUid }), context.propsValue.assignedToPersonClientIdentifier && (body.AssignedToPersonClientIdentifier = context.propsValue.assignedToPersonClientIdentifier), person ? body.DealPeople = [{ Person: { Uid: person.Uid } }] : body.DealPeople = [
      { Person: { Email: context.propsValue.contactEmail } }
    ], mergeCustomProperties(body, context.propsValue.customProperties), { created: !0, deal: await client.post("/api/v1/crm/deals", body) };
  }
});

// ../ap/packages/pieces/community/outseta/src/action/update-account.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var updateAccountAction = (0, import_pieces_framework13.createAction)({
  name: "update_account",
  auth: outsetaAuth,
  displayName: "Update Account",
  description: "Update an existing account in Outseta.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing Outseta CRM account identified by UID, setting only the provided fields (name, stage, client identifier, invoice notes, billing address) via a PUT. Use to change account details when you have its UID; requires at least one field. Idempotent: re-running with the same input leaves the account in the same state.",
    idempotent: !0
  },
  props: {
    accountUid: import_pieces_framework13.Property.ShortText({
      displayName: "Account UID",
      description: "The UID of the account to update.",
      required: !0
    }),
    name: import_pieces_framework13.Property.ShortText({
      displayName: "Name",
      required: !1
    }),
    accountStage: import_pieces_framework13.Property.StaticDropdown({
      displayName: "Account Stage",
      required: !1,
      options: {
        options: [
          { label: "Lead", value: 1 },
          { label: "Trialing", value: 2 },
          { label: "Subscribing", value: 3 },
          { label: "Delinquent", value: 4 },
          { label: "Cancelling", value: 5 },
          { label: "Expired", value: 6 },
          { label: "Demo", value: 7 }
        ]
      }
    }),
    clientIdentifier: import_pieces_framework13.Property.ShortText({
      displayName: "Client Identifier",
      required: !1,
      description: "A custom identifier you assign to this account (e.g. your internal customer ID)."
    }),
    invoiceNotes: import_pieces_framework13.Property.LongText({
      displayName: "Invoice Notes",
      required: !1,
      description: "Notes that appear on invoices sent to this account."
    }),
    addressLine1: import_pieces_framework13.Property.ShortText({
      displayName: "Address Line 1",
      required: !1
    }),
    addressLine2: import_pieces_framework13.Property.ShortText({
      displayName: "Address Line 2",
      required: !1
    }),
    city: import_pieces_framework13.Property.ShortText({
      displayName: "City",
      required: !1
    }),
    state: import_pieces_framework13.Property.ShortText({
      displayName: "State/Region",
      required: !1
    }),
    postalCode: import_pieces_framework13.Property.ShortText({
      displayName: "Postal Code",
      required: !1
    }),
    country: import_pieces_framework13.Property.ShortText({
      displayName: "Country",
      required: !1
    }),
    customProperties: customPropertiesProp("Account")
  },
  async run(context) {
    var _a, _b, _c, _d, _e;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), account = await client.get(
      `/api/v1/crm/accounts/${context.propsValue.accountUid}?fields=*,BillingAddress.*,MailingAddress.*,PersonAccount.*,PersonAccount.Person.*,Subscriptions.*`
    ), changed = !1;
    if (context.propsValue.name && (account.Name = context.propsValue.name, changed = !0), context.propsValue.accountStage != null && (account.AccountStage = context.propsValue.accountStage, changed = !0), context.propsValue.clientIdentifier && (account.ClientIdentifier = context.propsValue.clientIdentifier, changed = !0), context.propsValue.invoiceNotes && (account.InvoiceNotes = context.propsValue.invoiceNotes, changed = !0), context.propsValue.addressLine1 || context.propsValue.city || context.propsValue.country) {
      let address = (_a = account.BillingAddress) != null ? _a : {};
      context.propsValue.addressLine1 && (address.AddressLine1 = context.propsValue.addressLine1), context.propsValue.addressLine2 && (address.AddressLine2 = context.propsValue.addressLine2), context.propsValue.city && (address.City = context.propsValue.city), context.propsValue.state && (address.State = context.propsValue.state), context.propsValue.postalCode && (address.PostalCode = context.propsValue.postalCode), context.propsValue.country && (address.Country = context.propsValue.country), account.BillingAddress = address, changed = !0;
    }
    if (context.propsValue.customProperties && (mergeCustomProperties(account, context.propsValue.customProperties), changed = !0), !changed)
      throw new Error("At least one field must be provided.");
    return account.PersonAccount && !Array.isArray(account.PersonAccount) && (account.PersonAccount = (_c = (_b = account.PersonAccount.items) != null ? _b : account.PersonAccount.Items) != null ? _c : []), account.Subscriptions && !Array.isArray(account.Subscriptions) && (account.Subscriptions = (_e = (_d = account.Subscriptions.items) != null ? _d : account.Subscriptions.Items) != null ? _e : []), await client.put(
      `/api/v1/crm/accounts/${context.propsValue.accountUid}`,
      account
    );
  }
});

// ../ap/packages/pieces/community/outseta/src/action/update-person.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var updatePersonAction = (0, import_pieces_framework14.createAction)({
  name: "update_person",
  auth: outsetaAuth,
  displayName: "Update Person",
  description: "Update an existing person in Outseta.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing Outseta CRM person identified by UID, setting only the provided fields (email, name, phone, title, mailing address) via a PUT. Use to change a contact when you have its UID; requires at least one field. Idempotent: re-running with the same input leaves the person in the same state.",
    idempotent: !0
  },
  props: {
    personUid: import_pieces_framework14.Property.ShortText({
      displayName: "Person UID",
      description: "The UID of the person to update.",
      required: !0
    }),
    email: import_pieces_framework14.Property.ShortText({
      displayName: "Email",
      required: !1
    }),
    firstName: import_pieces_framework14.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    lastName: import_pieces_framework14.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    phoneMobile: import_pieces_framework14.Property.ShortText({
      displayName: "Mobile #",
      required: !1
    }),
    phoneWork: import_pieces_framework14.Property.ShortText({
      displayName: "Work #",
      required: !1
    }),
    title: import_pieces_framework14.Property.ShortText({
      displayName: "Title",
      required: !1,
      description: "Job title (e.g. CEO, Engineer)."
    }),
    addressLine1: import_pieces_framework14.Property.ShortText({
      displayName: "Address Line 1",
      required: !1
    }),
    addressLine2: import_pieces_framework14.Property.ShortText({
      displayName: "Address Line 2",
      required: !1
    }),
    city: import_pieces_framework14.Property.ShortText({
      displayName: "City",
      required: !1
    }),
    state: import_pieces_framework14.Property.ShortText({
      displayName: "State/Region",
      required: !1
    }),
    postalCode: import_pieces_framework14.Property.ShortText({
      displayName: "Postal Code",
      required: !1
    }),
    country: import_pieces_framework14.Property.ShortText({
      displayName: "Country",
      required: !1
    }),
    customProperties: customPropertiesProp("Person")
  },
  async run(context) {
    var _a, _b, _c;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), person = await client.get(
      `/api/v1/crm/people/${context.propsValue.personUid}?fields=*,MailingAddress.*,Account.*,PersonAccount.*,PersonAccount.Account.*`
    ), changed = !1;
    if (context.propsValue.email && (person.Email = context.propsValue.email, changed = !0), context.propsValue.firstName && (person.FirstName = context.propsValue.firstName, changed = !0), context.propsValue.lastName && (person.LastName = context.propsValue.lastName, changed = !0), context.propsValue.phoneMobile && (person.PhoneMobile = context.propsValue.phoneMobile, changed = !0), context.propsValue.phoneWork && (person.PhoneWork = context.propsValue.phoneWork, changed = !0), context.propsValue.title && (person.Title = context.propsValue.title, changed = !0), context.propsValue.addressLine1 || context.propsValue.city || context.propsValue.country) {
      let address = (_a = person.MailingAddress) != null ? _a : {};
      context.propsValue.addressLine1 && (address.AddressLine1 = context.propsValue.addressLine1), context.propsValue.addressLine2 && (address.AddressLine2 = context.propsValue.addressLine2), context.propsValue.city && (address.City = context.propsValue.city), context.propsValue.state && (address.State = context.propsValue.state), context.propsValue.postalCode && (address.PostalCode = context.propsValue.postalCode), context.propsValue.country && (address.Country = context.propsValue.country), person.MailingAddress = address, changed = !0;
    }
    if (context.propsValue.customProperties && (mergeCustomProperties(person, context.propsValue.customProperties), changed = !0), !changed)
      throw new Error("At least one field must be provided.");
    return person.PersonAccount && !Array.isArray(person.PersonAccount) && (person.PersonAccount = (_c = (_b = person.PersonAccount.items) != null ? _b : person.PersonAccount.Items) != null ? _c : []), await client.put(
      `/api/v1/crm/people/${context.propsValue.personUid}`,
      person
    );
  }
});

// ../ap/packages/pieces/community/outseta/src/action/update-deal.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var updateDealAction = (0, import_pieces_framework15.createAction)({
  name: "update_deal",
  auth: outsetaAuth,
  displayName: "Update Deal",
  description: "Update an existing deal.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing Outseta CRM deal identified by UID, setting only the provided fields (name, pipeline stage, amount, assignee, due date, linked account/person) via a PUT. Use to advance a deal stage or edit its details when you have its UID. Idempotent: re-running with the same input leaves the deal in the same state.",
    idempotent: !0
  },
  props: {
    dealUid: import_pieces_framework15.Property.ShortText({
      displayName: "Deal UID",
      description: "The UID of the deal to update.",
      required: !0
    }),
    name: import_pieces_framework15.Property.ShortText({
      displayName: "Name",
      required: !1
    }),
    pipelineUid: pipelineDropdown({ required: !1 }),
    dealPipelineStageUid: pipelineStageDropdown({ required: !1 }),
    amount: import_pieces_framework15.Property.Number({
      displayName: "Amount",
      required: !1,
      description: "The monetary value of the deal."
    }),
    assignedToPersonClientIdentifier: import_pieces_framework15.Property.ShortText({
      displayName: "Assigned To (Client Identifier)",
      required: !1,
      description: "The client identifier of the team member this deal is assigned to. Found in the person's Outseta profile."
    }),
    dueDate: import_pieces_framework15.Property.DateTime({
      displayName: "Due Date",
      required: !1
    }),
    accountUid: import_pieces_framework15.Property.ShortText({
      displayName: "Account UID",
      description: "The UID of the account to associate with this deal.",
      required: !1
    }),
    personUid: import_pieces_framework15.Property.ShortText({
      displayName: "Person UID",
      description: "UID of a person to add as a contact on this deal. Existing contacts are preserved.",
      required: !1
    }),
    customProperties: customPropertiesProp("Deal")
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n, _o, _p, _q, _r, _s, _t;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), deal = await client.get(
      `/api/v1/crm/deals/${context.propsValue.dealUid}?fields=*,DealPeople.*,DealPeople.Person.*,DealPipelineStage.*,Account.*`
    ), changed = !1;
    if (context.propsValue.name && (deal.Name = context.propsValue.name, changed = !0), context.propsValue.dealPipelineStageUid && (deal.DealPipelineStage = { Uid: context.propsValue.dealPipelineStageUid }, changed = !0), context.propsValue.amount != null && (deal.Amount = context.propsValue.amount, changed = !0), context.propsValue.assignedToPersonClientIdentifier && (deal.AssignedToPersonClientIdentifier = context.propsValue.assignedToPersonClientIdentifier, changed = !0), context.propsValue.dueDate && (deal.DueDate = context.propsValue.dueDate, changed = !0), context.propsValue.accountUid && (deal.Account = { Uid: context.propsValue.accountUid }, changed = !0), context.propsValue.personUid) {
      let existing = (_e = (_d = (_c = (_a = deal.DealPeople) == null ? void 0 : _a.items) != null ? _c : (_b = deal.DealPeople) == null ? void 0 : _b.Items) != null ? _d : deal.DealPeople) != null ? _e : [];
      existing.some(
        (dp) => {
          var _a2;
          return ((_a2 = dp.Person) == null ? void 0 : _a2.Uid) === context.propsValue.personUid;
        }
      ) || (deal.DealPeople = [
        ...existing.map((dp) => {
          var _a2;
          return {
            Uid: dp.Uid,
            Person: { Uid: (_a2 = dp.Person) == null ? void 0 : _a2.Uid }
          };
        }),
        { Person: { Uid: context.propsValue.personUid } }
      ], changed = !0);
    }
    if (context.propsValue.customProperties && (mergeCustomProperties(deal, context.propsValue.customProperties), changed = !0), !changed)
      throw new Error("At least one field must be provided.");
    deal.DealPeople && !Array.isArray(deal.DealPeople) && (deal.DealPeople = (_g = (_f = deal.DealPeople.items) != null ? _f : deal.DealPeople.Items) != null ? _g : []);
    let updated = await client.put(
      `/api/v1/crm/deals/${context.propsValue.dealUid}`,
      deal
    );
    return {
      uid: (_h = updated.Uid) != null ? _h : null,
      name: (_i = updated.Name) != null ? _i : null,
      amount: (_j = updated.Amount) != null ? _j : null,
      due_date: (_k = updated.DueDate) != null ? _k : null,
      pipeline_stage_uid: (_m = (_l = updated.DealPipelineStage) == null ? void 0 : _l.Uid) != null ? _m : null,
      pipeline_stage_name: (_o = (_n = updated.DealPipelineStage) == null ? void 0 : _n.Name) != null ? _o : null,
      account_uid: (_q = (_p = updated.Account) == null ? void 0 : _p.Uid) != null ? _q : null,
      account_name: (_s = (_r = updated.Account) == null ? void 0 : _r.Name) != null ? _s : null,
      updated: (_t = updated.Updated) != null ? _t : null
    };
  }
});

// ../ap/packages/pieces/community/outseta/src/action/delete-account.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var deleteAccountAction = (0, import_pieces_framework16.createAction)({
  name: "delete_account",
  auth: outsetaAuth,
  displayName: "Delete Account",
  description: "Delete an account from Outseta CRM.",
  audience: "both",
  aiMetadata: {
    description: "Permanently deletes a CRM account by its UID. Use to remove an account entirely; to only end billing while keeping the record use Cancel Subscription. Destructive. Not idempotent: a repeat call errors because the account no longer exists.",
    idempotent: !1
  },
  props: {
    accountUid: import_pieces_framework16.Property.ShortText({
      displayName: "Account UID",
      description: "The UID of the account to delete.",
      required: !0
    })
  },
  async run(context) {
    return await new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }).delete(`/api/v1/crm/accounts/${context.propsValue.accountUid}`), { deleted: !0, account_uid: context.propsValue.accountUid };
  }
});

// ../ap/packages/pieces/community/outseta/src/action/delete-person.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework");
var deletePersonAction = (0, import_pieces_framework17.createAction)({
  name: "delete_person",
  auth: outsetaAuth,
  displayName: "Delete Person",
  description: "Delete a person from Outseta CRM.",
  audience: "both",
  aiMetadata: {
    description: "Permanently deletes a CRM person (contact) by its UID. Use to remove a contact entirely; to only detach them from an account use Manage Account Membership. Destructive. Not idempotent: a repeat call errors because the person no longer exists.",
    idempotent: !1
  },
  props: {
    personUid: import_pieces_framework17.Property.ShortText({
      displayName: "Person UID",
      description: "The UID of the person to delete.",
      required: !0
    })
  },
  async run(context) {
    return await new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }).delete(`/api/v1/crm/people/${context.propsValue.personUid}`), { deleted: !0, person_uid: context.propsValue.personUid };
  }
});

// ../ap/packages/pieces/community/outseta/src/action/delete-deal.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework");
var deleteDealAction = (0, import_pieces_framework18.createAction)({
  name: "delete_deal",
  auth: outsetaAuth,
  displayName: "Delete Deal",
  description: "Delete a deal from the CRM.",
  audience: "both",
  aiMetadata: {
    description: "Permanently deletes an Outseta CRM deal by its UID. Use to remove a deal you have the UID for. Destructive; repeating the call after the deal is gone will error rather than succeed silently.",
    idempotent: !1
  },
  props: {
    dealUid: import_pieces_framework18.Property.ShortText({
      displayName: "Deal UID",
      description: "The UID of the deal to delete.",
      required: !0
    })
  },
  async run(context) {
    return await new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }).delete(
      `/api/v1/crm/deals/${context.propsValue.dealUid}`
    ), { deleted: !0, deal_uid: context.propsValue.dealUid };
  }
});

// ../ap/packages/pieces/community/outseta/src/action/list-accounts.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework");
var listAccountsAction = (0, import_pieces_framework19.createAction)({
  name: "list_accounts",
  auth: outsetaAuth,
  displayName: "List Accounts",
  description: "Retrieve a paginated list of accounts from your Outseta CRM.",
  audience: "both",
  aiMetadata: {
    description: "Returns a paginated list of CRM accounts. Use to browse or scan accounts; to fetch one known account use Retrieve Account. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    limit: import_pieces_framework19.Property.Number({
      displayName: "Limit",
      required: !1,
      defaultValue: 100,
      description: "Maximum number of accounts to return (default 100)."
    }),
    offset: import_pieces_framework19.Property.Number({
      displayName: "Page",
      required: !1,
      defaultValue: 0,
      description: "Page number to fetch (0 = first page, 1 = second page, ...). Outseta uses page-based pagination, not record-based."
    })
  },
  async run(context) {
    var _a, _b;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), limit = (_a = context.propsValue.limit) != null ? _a : 100, offset = (_b = context.propsValue.offset) != null ? _b : 0;
    return await client.get(
      `/api/v1/crm/accounts?limit=${limit}&offset=${offset}`
    );
  }
});

// ../ap/packages/pieces/community/outseta/src/action/list-persons.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework");
var listPersonsAction = (0, import_pieces_framework20.createAction)({
  name: "list_persons",
  auth: outsetaAuth,
  displayName: "List Persons",
  description: "Retrieve a paginated list of persons from your Outseta CRM.",
  audience: "both",
  aiMetadata: {
    description: "Returns a paginated list of CRM persons (contacts). Use to browse or scan contacts; to fetch one known person use Retrieve Person. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    limit: import_pieces_framework20.Property.Number({
      displayName: "Limit",
      required: !1,
      defaultValue: 100,
      description: "Maximum number of persons to return (default 100)."
    }),
    offset: import_pieces_framework20.Property.Number({
      displayName: "Page",
      required: !1,
      defaultValue: 0,
      description: "Page number to fetch (0 = first page, 1 = second page, ...). Outseta uses page-based pagination, not record-based."
    })
  },
  async run(context) {
    var _a, _b;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), limit = (_a = context.propsValue.limit) != null ? _a : 100, offset = (_b = context.propsValue.offset) != null ? _b : 0;
    return await client.get(
      `/api/v1/crm/people?limit=${limit}&offset=${offset}`
    );
  }
});

// ../ap/packages/pieces/community/outseta/src/action/list-deals.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework");
var listDealsAction = (0, import_pieces_framework21.createAction)({
  name: "list_deals",
  auth: outsetaAuth,
  displayName: "List Deals",
  description: "Retrieve a paginated list of deals from your Outseta CRM.",
  audience: "both",
  aiMetadata: {
    description: "Returns a paginated list of CRM deals. Use to browse or scan deals; to fetch one known deal use Retrieve Deal. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    limit: import_pieces_framework21.Property.Number({
      displayName: "Limit",
      required: !1,
      defaultValue: 100,
      description: "Maximum number of deals to return (default 100)."
    }),
    offset: import_pieces_framework21.Property.Number({
      displayName: "Page",
      required: !1,
      defaultValue: 0,
      description: "Page number to fetch (0 = first page, 1 = second page, ...). Outseta uses page-based pagination, not record-based."
    })
  },
  async run(context) {
    var _a, _b;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), limit = (_a = context.propsValue.limit) != null ? _a : 100, offset = (_b = context.propsValue.offset) != null ? _b : 0;
    return client.get(`/api/v1/crm/deals?limit=${limit}&offset=${offset}`);
  }
});

// ../ap/packages/pieces/community/outseta/src/action/list-plans.ts
var import_pieces_framework22 = require("@activepieces/pieces-framework");
var listPlansAction = (0, import_pieces_framework22.createAction)({
  name: "list_plans",
  auth: outsetaAuth,
  displayName: "List Plans",
  description: "Retrieve a paginated list of subscription plans from your Outseta billing catalog.",
  audience: "both",
  aiMetadata: {
    description: "Returns a paginated list of subscription plans from the billing catalog. Use to discover available plan UIDs, e.g. before Change Account Plan or Create Account. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    limit: import_pieces_framework22.Property.Number({
      displayName: "Limit",
      required: !1,
      defaultValue: 100,
      description: "Maximum number of plans to return (default 100)."
    }),
    offset: import_pieces_framework22.Property.Number({
      displayName: "Page",
      required: !1,
      defaultValue: 0,
      description: "Page number to fetch (0 = first page, 1 = second page, ...). Outseta uses page-based pagination, not record-based."
    })
  },
  async run(context) {
    var _a, _b;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), limit = (_a = context.propsValue.limit) != null ? _a : 100, offset = (_b = context.propsValue.offset) != null ? _b : 0;
    return client.get(
      `/api/v1/billing/plans?limit=${limit}&offset=${offset}`
    );
  }
});

// ../ap/packages/pieces/community/outseta/src/action/list-addons.ts
var import_pieces_framework23 = require("@activepieces/pieces-framework");
var listAddOnsAction = (0, import_pieces_framework23.createAction)({
  name: "list_addons",
  auth: outsetaAuth,
  displayName: "List Add-Ons",
  description: "Retrieve a paginated list of add-ons from your Outseta billing catalog.",
  audience: "both",
  aiMetadata: {
    description: "Returns a paginated list of add-ons from the billing catalog. Use to discover available add-on UIDs, e.g. before Add Add-on to Subscription. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    limit: import_pieces_framework23.Property.Number({
      displayName: "Limit",
      required: !1,
      defaultValue: 100,
      description: "Maximum number of add-ons to return (default 100)."
    }),
    offset: import_pieces_framework23.Property.Number({
      displayName: "Page",
      required: !1,
      defaultValue: 0,
      description: "Page number to fetch (0 = first page, 1 = second page, ...). Outseta uses page-based pagination, not record-based."
    })
  },
  async run(context) {
    var _a, _b;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), limit = (_a = context.propsValue.limit) != null ? _a : 100, offset = (_b = context.propsValue.offset) != null ? _b : 0;
    return client.get(
      `/api/v1/billing/addons?limit=${limit}&offset=${offset}`
    );
  }
});

// ../ap/packages/pieces/community/outseta/src/action/list-discounts.ts
var import_pieces_framework24 = require("@activepieces/pieces-framework");
var listDiscountsAction = (0, import_pieces_framework24.createAction)({
  name: "list_discounts",
  auth: outsetaAuth,
  displayName: "List Discounts",
  description: "Retrieve a paginated list of discount coupons from your Outseta billing catalog.",
  audience: "both",
  aiMetadata: {
    description: "Returns a paginated list of discount coupons from the billing catalog. Use to discover existing coupon UIDs, e.g. before Apply Discount to Account. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    limit: import_pieces_framework24.Property.Number({
      displayName: "Limit",
      required: !1,
      defaultValue: 100,
      description: "Maximum number of discounts to return (default 100)."
    }),
    offset: import_pieces_framework24.Property.Number({
      displayName: "Page",
      required: !1,
      defaultValue: 0,
      description: "Page number to fetch (0 = first page, 1 = second page, ...). Outseta uses page-based pagination, not record-based."
    })
  },
  async run(context) {
    var _a, _b;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), limit = (_a = context.propsValue.limit) != null ? _a : 100, offset = (_b = context.propsValue.offset) != null ? _b : 0;
    return client.get(
      `/api/v1/billing/discountcoupons?limit=${limit}&offset=${offset}`
    );
  }
});

// ../ap/packages/pieces/community/outseta/src/action/list-cases.ts
var import_pieces_framework25 = require("@activepieces/pieces-framework");
var listCasesAction = (0, import_pieces_framework25.createAction)({
  name: "list_cases",
  auth: outsetaAuth,
  displayName: "List Cases",
  description: "Retrieve a paginated list of support tickets (cases) from Outseta. Optionally filter by the person who submitted them.",
  audience: "both",
  aiMetadata: {
    description: "Returns a paginated list of support tickets, optionally filtered by the submitting person's UID or email. Use to scan or find tickets; to open one use Create Ticket. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    fromPersonUid: import_pieces_framework25.Property.ShortText({
      displayName: "From Person UID",
      required: !1,
      description: "Optional. Only return cases submitted by this person."
    }),
    fromPersonEmail: import_pieces_framework25.Property.ShortText({
      displayName: "From Person Email",
      required: !1,
      description: "Optional. Only return cases submitted by the person with this email."
    }),
    limit: import_pieces_framework25.Property.Number({
      displayName: "Limit",
      required: !1,
      defaultValue: 100,
      description: "Maximum number of cases to return (default 100)."
    }),
    offset: import_pieces_framework25.Property.Number({
      displayName: "Page",
      required: !1,
      defaultValue: 0,
      description: "Page number to fetch (0 = first page, 1 = second page, ...). Outseta uses page-based pagination, not record-based."
    })
  },
  async run(context) {
    var _a, _b;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), params = [
      `limit=${(_a = context.propsValue.limit) != null ? _a : 100}`,
      `offset=${(_b = context.propsValue.offset) != null ? _b : 0}`
    ];
    return context.propsValue.fromPersonUid && params.push(`FromPerson.Uid=${encodeURIComponent(context.propsValue.fromPersonUid)}`), context.propsValue.fromPersonEmail && params.push(
      `FromPerson.Email=${encodeURIComponent(context.propsValue.fromPersonEmail)}`
    ), client.get(`/api/v1/support/cases?${params.join("&")}`);
  }
});

// ../ap/packages/pieces/community/outseta/src/action/list-transactions.ts
var import_pieces_framework26 = require("@activepieces/pieces-framework");
var listTransactionsAction = (0, import_pieces_framework26.createAction)({
  name: "list_transactions",
  auth: outsetaAuth,
  displayName: "List Account Transactions",
  description: "Retrieve all billing transactions (payments, refunds, invoices) for a given account.",
  audience: "both",
  aiMetadata: {
    description: "Returns a paginated list of all billing transactions (payments, refunds, invoices) for an account, by account UID. Use for full history; for just the latest payment use Get Last Payment for Account. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    accountUid: import_pieces_framework26.Property.ShortText({
      displayName: "Account UID",
      description: "The UID of the account to retrieve transactions for.",
      required: !0
    }),
    limit: import_pieces_framework26.Property.Number({
      displayName: "Limit",
      required: !1,
      defaultValue: 100,
      description: "Maximum number of transactions to return (default 100)."
    }),
    offset: import_pieces_framework26.Property.Number({
      displayName: "Page",
      required: !1,
      defaultValue: 0,
      description: "Page number to fetch (0 = first page, 1 = second page, ...). Outseta uses page-based pagination, not record-based."
    })
  },
  async run(context) {
    var _a, _b;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), limit = (_a = context.propsValue.limit) != null ? _a : 100, offset = (_b = context.propsValue.offset) != null ? _b : 0;
    return client.get(
      `/api/v1/billing/transactions/${context.propsValue.accountUid}?limit=${limit}&offset=${offset}&orderBy=Created%20DESC`
    );
  }
});

// ../ap/packages/pieces/community/outseta/src/action/change-account-plan.ts
var import_pieces_framework27 = require("@activepieces/pieces-framework");
var changeAccountPlanAction = (0, import_pieces_framework27.createAction)({
  name: "change_account_plan",
  auth: outsetaAuth,
  displayName: "Change Account Plan",
  description: "Change an account's current subscription plan (upgrade, downgrade, or switch to free).",
  audience: "both",
  aiMetadata: {
    description: "Changes the plan on an account's current subscription (upgrade, downgrade, or switch to free) and sets the billing renewal term, identified by account UID. Use to move an account between plans. Requires the account to already have an active subscription, otherwise it errors. Not idempotent: it is a billing mutation that can prorate/charge, so repeating may have further effect.",
    idempotent: !1
  },
  props: {
    accountUid: import_pieces_framework27.Property.ShortText({
      displayName: "Account UID",
      description: "The UID of the account whose plan to change.",
      required: !0
    }),
    planUid: planUidDropdown({ displayName: "New Plan", refreshers: ["accountUid"] }),
    billingRenewalTerm: import_pieces_framework27.Property.StaticDropdown({
      displayName: "Billing Renewal Term",
      required: !0,
      defaultValue: 1,
      options: {
        disabled: !1,
        options: [
          { label: "Monthly", value: 1 },
          { label: "Annual", value: 2 },
          { label: "Quarterly", value: 3 },
          { label: "One-time", value: 4 }
        ]
      }
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), account = await client.get(
      `/api/v1/crm/accounts/${context.propsValue.accountUid}?fields=Uid,CurrentSubscription.Uid`
    );
    if (!((_a = account.CurrentSubscription) != null && _a.Uid))
      throw new Error(
        "This account does not have an active subscription to change."
      );
    let subscriptionUid = account.CurrentSubscription.Uid, result = await client.put(
      `/api/v1/billing/subscriptions/${subscriptionUid}/changesubscription`,
      {
        Account: { Uid: context.propsValue.accountUid },
        Plan: { Uid: context.propsValue.planUid },
        BillingRenewalTerm: context.propsValue.billingRenewalTerm
      }
    );
    return {
      subscription_uid: (_b = result.Uid) != null ? _b : null,
      plan_uid: (_d = (_c = result.Plan) == null ? void 0 : _c.Uid) != null ? _d : null,
      plan_name: (_f = (_e = result.Plan) == null ? void 0 : _e.Name) != null ? _f : null,
      billing_renewal_term: (_g = result.BillingRenewalTerm) != null ? _g : null,
      renewal_date: (_h = result.RenewalDate) != null ? _h : null,
      updated: (_i = result.Updated) != null ? _i : null
    };
  }
});

// ../ap/packages/pieces/community/outseta/src/action/cancel-subscription.ts
var import_pieces_framework28 = require("@activepieces/pieces-framework");
var cancelSubscriptionAction = (0, import_pieces_framework28.createAction)({
  name: "cancel_subscription",
  auth: outsetaAuth,
  displayName: "Cancel Subscription",
  description: "Cancel the current subscription on an account. By default, the subscription expires at the end of the current term. You can also cancel immediately.",
  audience: "both",
  aiMetadata: {
    description: "Requests cancellation of an account's current subscription, identified by account UID. Defaults to expiring at the end of the current billing term; with cancel-immediately enabled it also forces the account to the Expired stage right away. Use to churn or downgrade an account. Optional cancellation reason and comment can be recorded. Not idempotent: it mutates subscription/account state.",
    idempotent: !1
  },
  props: {
    accountUid: import_pieces_framework28.Property.ShortText({
      displayName: "Account UID",
      description: "The UID of the account whose subscription to cancel.",
      required: !0
    }),
    cancelImmediately: import_pieces_framework28.Property.Checkbox({
      displayName: "Cancel Immediately",
      required: !1,
      defaultValue: !1,
      description: "If checked, the subscription is cancelled immediately. Otherwise it expires at the end of the current billing term."
    }),
    cancellationReason: import_pieces_framework28.Property.ShortText({
      displayName: "Cancellation Reason",
      required: !1,
      description: 'Reason for the cancellation (e.g. "Too expensive").'
    }),
    comment: import_pieces_framework28.Property.LongText({
      displayName: "Comment",
      required: !1,
      description: "Additional comment about the cancellation."
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), body = {
      Account: { Uid: context.propsValue.accountUid }
    };
    if (context.propsValue.cancellationReason && (body.CancelationReason = context.propsValue.cancellationReason), context.propsValue.comment && (body.Comment = context.propsValue.comment), await client.put(
      `/api/v1/crm/accounts/cancellation/${context.propsValue.accountUid}`,
      body
    ), context.propsValue.cancelImmediately) {
      let account = await client.get(
        `/api/v1/crm/accounts/${context.propsValue.accountUid}?fields=*,BillingAddress.*,MailingAddress.*,PrimaryContact.*,PersonAccount.*,PersonAccount.Person.*,Subscriptions.*`
      );
      account.AccountStage = 6, account.PersonAccount && !Array.isArray(account.PersonAccount) && (account.PersonAccount = (_b = (_a = account.PersonAccount.items) != null ? _a : account.PersonAccount.Items) != null ? _b : []), account.Subscriptions && !Array.isArray(account.Subscriptions) && (account.Subscriptions = (_d = (_c = account.Subscriptions.items) != null ? _c : account.Subscriptions.Items) != null ? _d : []), await client.put(
        `/api/v1/crm/accounts/${context.propsValue.accountUid}`,
        account
      );
    }
    return {
      account_uid: context.propsValue.accountUid,
      cancelled: !0,
      cancelled_immediately: (_e = context.propsValue.cancelImmediately) != null ? _e : !1
    };
  }
});

// ../ap/packages/pieces/community/outseta/src/action/remove-cancellation.ts
var import_pieces_framework29 = require("@activepieces/pieces-framework");
var removeCancellationAction = (0, import_pieces_framework29.createAction)({
  name: "remove_cancellation",
  auth: outsetaAuth,
  displayName: "Remove Cancellation",
  description: "Remove a pending cancellation request from an account, keeping the subscription active.",
  audience: "both",
  aiMetadata: {
    description: "Removes a pending cancellation on an account's subscription, keeping it active, by account UID. Use to reverse a scheduled cancellation; to schedule one use Cancel Subscription. Idempotent: calling it on a subscription that is not cancelling leaves it active.",
    idempotent: !0
  },
  props: {
    accountUid: import_pieces_framework29.Property.ShortText({
      displayName: "Account UID",
      required: !0,
      description: "The UID of the account whose cancellation should be removed."
    })
  },
  async run(context) {
    return await new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }).put(
      `/api/v1/crm/accounts/removecancellation/${context.propsValue.accountUid}`,
      {}
    ), {
      account_uid: context.propsValue.accountUid,
      cancellation_removed: !0
    };
  }
});

// ../ap/packages/pieces/community/outseta/src/action/add-discount-to-subscription.ts
var import_pieces_framework30 = require("@activepieces/pieces-framework");
var addDiscountToSubscriptionAction = (0, import_pieces_framework30.createAction)({
  name: "add_discount_to_subscription",
  auth: outsetaAuth,
  displayName: "Apply Discount to Account",
  description: "Apply a discount coupon to the current subscription of an account.",
  audience: "both",
  aiMetadata: {
    description: "Applies a discount coupon to an account's current subscription, by account UID and discount UID. Use to grant a promotion to an existing subscriber; to create the coupon itself first use Create Discount. Requires an active subscription. Not idempotent: re-applying re-adds the discount.",
    idempotent: !1
  },
  props: {
    accountUid: import_pieces_framework30.Property.ShortText({
      displayName: "Account UID",
      required: !0,
      description: "The UID of the account whose subscription will receive the discount."
    }),
    discountUid: discountDropdown()
  },
  async run(context) {
    var _a;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), account = await client.get(
      `/api/v1/crm/accounts/${context.propsValue.accountUid}?fields=CurrentSubscription.*`
    ), subscriptionUid = (_a = account == null ? void 0 : account.CurrentSubscription) == null ? void 0 : _a.Uid;
    if (!subscriptionUid)
      throw new Error(
        `Account ${context.propsValue.accountUid} does not have an active subscription.`
      );
    return await client.post(
      `/api/v1/billing/subscriptions/${subscriptionUid}/discounts/${context.propsValue.discountUid}`,
      {}
    );
  }
});

// ../ap/packages/pieces/community/outseta/src/action/add-addon-usage.ts
var import_pieces_framework31 = require("@activepieces/pieces-framework");
var addAddonUsageAction = (0, import_pieces_framework31.createAction)({
  name: "add_addon_usage",
  auth: outsetaAuth,
  displayName: "Add Usage for Add-on",
  description: "Track usage for a metered (usage-based) add-on on an account.",
  audience: "both",
  aiMetadata: {
    description: "Records a usage amount for a metered (usage-based) add-on on an account's current subscription, resolving the add-on UID against that subscription's active add-ons. Use to report consumption for billing. Requires an active subscription that already includes the chosen add-on, otherwise it errors. Not idempotent: each call appends a usage record that accumulates toward billing.",
    idempotent: !1
  },
  props: {
    accountUid: import_pieces_framework31.Property.ShortText({
      displayName: "Account UID",
      description: "The UID of the account to record usage for.",
      required: !0
    }),
    addOnUid: addOnUidDropdown({ refreshers: ["accountUid"] }),
    amount: import_pieces_framework31.Property.Number({
      displayName: "Amount",
      required: !0,
      description: "The usage amount to record."
    }),
    usageDate: import_pieces_framework31.Property.DateTime({
      displayName: "Usage Date",
      required: !1,
      description: "The date of usage. Defaults to now if not provided."
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), account = await client.get(
      `/api/v1/crm/accounts/${context.propsValue.accountUid}?fields=Uid,CurrentSubscription.*,CurrentSubscription.SubscriptionAddOns.*,CurrentSubscription.SubscriptionAddOns.AddOn.*`
    );
    if (!account.CurrentSubscription)
      throw new Error("This account does not have an active subscription.");
    let rawAddOns = account.CurrentSubscription.SubscriptionAddOns, subscriptionAddOns = Array.isArray(rawAddOns) ? rawAddOns : (_b = (_a = rawAddOns == null ? void 0 : rawAddOns.items) != null ? _a : rawAddOns == null ? void 0 : rawAddOns.Items) != null ? _b : [], matchingAddOn = subscriptionAddOns.find(
      (sa) => {
        var _a2;
        return ((_a2 = sa.AddOn) == null ? void 0 : _a2.Uid) === context.propsValue.addOnUid;
      }
    );
    if (!matchingAddOn)
      throw new Error(
        `Add-on with UID "${context.propsValue.addOnUid}" not found on this account's current subscription. Available add-ons: ${subscriptionAddOns.map((sa) => {
          var _a2, _b2;
          return `${(_a2 = sa.AddOn) == null ? void 0 : _a2.Name} (${(_b2 = sa.AddOn) == null ? void 0 : _b2.Uid})`;
        }).join(", ") || "none"}`
      );
    let result = await client.post("/api/v1/billing/usage", {
      UsageDate: (_c = context.propsValue.usageDate) != null ? _c : (/* @__PURE__ */ new Date()).toISOString(),
      Amount: context.propsValue.amount,
      SubscriptionAddOn: { Uid: matchingAddOn.Uid }
    });
    return {
      uid: (_d = result.Uid) != null ? _d : null,
      amount: (_e = result.Amount) != null ? _e : null,
      usage_date: (_f = result.UsageDate) != null ? _f : null,
      add_on_uid: (_h = (_g = result.SubscriptionAddOn) == null ? void 0 : _g.Uid) != null ? _h : null,
      created: (_i = result.Created) != null ? _i : null
    };
  }
});

// ../ap/packages/pieces/community/outseta/src/action/add-addon-to-subscription.ts
var import_pieces_framework32 = require("@activepieces/pieces-framework");
var addAddonToSubscriptionAction = (0, import_pieces_framework32.createAction)({
  name: "add_addon_to_subscription",
  auth: outsetaAuth,
  displayName: "Add Add-on to Subscription",
  description: "Attach a recurring add-on to an account's current subscription. Different from Add Usage for Add-on, which only records consumption on a metered add-on already attached.",
  audience: "both",
  aiMetadata: {
    description: "Attaches a recurring add-on to an account's current subscription, by account UID and add-on UID. Use to add an ongoing add-on charge; to report metered consumption on an add-on that is already attached use Add Usage for Add-on instead. Requires an active subscription. Not idempotent: each call adds another add-on instance.",
    idempotent: !1
  },
  props: {
    accountUid: import_pieces_framework32.Property.ShortText({
      displayName: "Account UID",
      description: "The UID of the account whose subscription will receive the add-on.",
      required: !0
    }),
    addOnUid: addOnUidDropdown({ refreshers: [] }),
    quantity: import_pieces_framework32.Property.Number({
      displayName: "Quantity",
      required: !1,
      defaultValue: 1,
      description: "Number of add-on units to attach. Defaults to 1."
    }),
    billingRenewalTerm: import_pieces_framework32.Property.StaticDropdown({
      displayName: "Billing Renewal Term",
      required: !1,
      defaultValue: 1,
      description: "How often the add-on is billed. Defaults to Monthly.",
      options: {
        disabled: !1,
        options: [
          { label: "Monthly", value: 1 },
          { label: "Annual", value: 2 },
          { label: "Quarterly", value: 3 },
          { label: "One-time", value: 4 }
        ]
      }
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), account = await client.get(
      `/api/v1/crm/accounts/${context.propsValue.accountUid}?fields=CurrentSubscription.Uid`
    ), subscriptionUid = (_a = account == null ? void 0 : account.CurrentSubscription) == null ? void 0 : _a.Uid;
    if (!subscriptionUid)
      throw new Error(
        `Account ${context.propsValue.accountUid} does not have an active subscription.`
      );
    let result = await client.post("/api/v1/billing/subscriptionaddons", {
      AddOn: { Uid: context.propsValue.addOnUid },
      Subscription: { Uid: subscriptionUid },
      Quantity: (_b = context.propsValue.quantity) != null ? _b : 1,
      BillingRenewalTerm: (_c = context.propsValue.billingRenewalTerm) != null ? _c : 1
    });
    return {
      subscription_uid: subscriptionUid,
      addon_uid: context.propsValue.addOnUid,
      quantity: (_e = (_d = result == null ? void 0 : result.Quantity) != null ? _d : context.propsValue.quantity) != null ? _e : 1,
      subscription_addon_uid: (_f = result == null ? void 0 : result.Uid) != null ? _f : null
    };
  }
});

// ../ap/packages/pieces/community/outseta/src/action/extend-trial-subscription.ts
var import_pieces_framework33 = require("@activepieces/pieces-framework");
var extendTrialSubscriptionAction = (0, import_pieces_framework33.createAction)({
  name: "extend_trial_subscription",
  auth: outsetaAuth,
  displayName: "Extend Trial Subscription",
  description: "Extend the trial period of an account by setting a new trial end date.",
  audience: "both",
  aiMetadata: {
    description: "Sets a new trial end date on an account's subscription, by account UID. Use to prolong a trial; the date must be in the future and is applied as an absolute target (yyyy-MM-dd). Idempotent: setting the same end date again has no further effect.",
    idempotent: !0
  },
  props: {
    accountUid: import_pieces_framework33.Property.ShortText({
      displayName: "Account UID",
      description: "The UID of the account whose trial to extend.",
      required: !0
    }),
    trialEndDate: import_pieces_framework33.Property.DateTime({
      displayName: "New Trial End Date",
      required: !0,
      description: "The new date when the trial should end. Must be in the future. Time-of-day is ignored \u2014 Outseta uses date only (yyyy-MM-dd)."
    })
  },
  async run(context) {
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), isoDate = new Date(context.propsValue.trialEndDate).toISOString().slice(0, 10);
    return await client.put(
      `/api/v1/crm/accounts/extendtrial/${context.propsValue.accountUid}/${isoDate}`,
      {}
    ), {
      account_uid: context.propsValue.accountUid,
      new_trial_end_date: isoDate,
      extended: !0
    };
  }
});

// ../ap/packages/pieces/community/outseta/src/action/update-payment-information.ts
var import_pieces_framework34 = require("@activepieces/pieces-framework");
var updatePaymentInformationAction = (0, import_pieces_framework34.createAction)({
  name: "update_payment_information",
  auth: outsetaAuth,
  displayName: "Update Payment Information",
  description: "Update the credit card / payment method on an account. The payment processor tokens (CustomerToken, PaymentToken) must come from your payment processor (e.g. Stripe Elements) \u2014 not from Outseta.",
  audience: "both",
  aiMetadata: {
    description: "Sets the payment method on an account from processor tokens (CustomerToken/PaymentToken), by account UID. Use to attach or replace a card; the tokens must come from your payment processor (e.g. Stripe), not Outseta. Idempotent: re-sending the same tokens yields the same stored method.",
    idempotent: !0
  },
  props: {
    accountUid: import_pieces_framework34.Property.ShortText({
      displayName: "Account UID",
      description: "The UID of the account to update payment information for.",
      required: !0
    }),
    customerToken: import_pieces_framework34.Property.ShortText({
      displayName: "Customer Token",
      required: !0,
      description: 'The customer ID issued by the payment processor (e.g. Stripe customer ID, "cust_\u2026").'
    }),
    paymentToken: import_pieces_framework34.Property.ShortText({
      displayName: "Payment Token",
      required: !0,
      description: 'The payment method ID issued by the payment processor (e.g. Stripe payment method, "pm_\u2026").'
    }),
    nameOnCard: import_pieces_framework34.Property.ShortText({
      displayName: "Name on Card",
      required: !1,
      description: "Cardholder name as it appears on the card."
    })
  },
  async run(context) {
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), body = {
      Account: { Uid: context.propsValue.accountUid },
      CustomerToken: context.propsValue.customerToken,
      PaymentToken: context.propsValue.paymentToken
    };
    return context.propsValue.nameOnCard && (body.NameOnCard = context.propsValue.nameOnCard), client.post("/api/v1/billing/paymentinformation", body);
  }
});

// ../ap/packages/pieces/community/outseta/src/action/create-discount.ts
var import_pieces_framework35 = require("@activepieces/pieces-framework");
var createDiscountAction = (0, import_pieces_framework35.createAction)({
  name: "create_discount",
  auth: outsetaAuth,
  displayName: "Create Discount",
  description: "Create a new discount coupon in the Outseta billing catalog. Either AmountOff or PercentOff must be set, never both.",
  audience: "both",
  aiMetadata: {
    description: "Creates a discount coupon in the billing catalog (fixed amount or percentage, with a duration). Use to define a new promotion code; to apply an existing coupon to a subscriber use Apply Discount to Account. Set either Amount Off or Percent Off, not both. Not idempotent: each call creates a new coupon.",
    idempotent: !1
  },
  props: {
    uniqueIdentifier: import_pieces_framework35.Property.ShortText({
      displayName: "Coupon Code",
      required: !0,
      description: 'The public code customers enter to redeem the discount (e.g. "RENTREE2026").'
    }),
    name: import_pieces_framework35.Property.ShortText({
      displayName: "Name",
      required: !0,
      description: 'Display name of the discount (e.g. "Rentr\xE9e -10%").'
    }),
    isActive: import_pieces_framework35.Property.Checkbox({
      displayName: "Is Active",
      required: !1,
      defaultValue: !0
    }),
    duration: import_pieces_framework35.Property.StaticDropdown({
      displayName: "Duration",
      required: !0,
      defaultValue: 1,
      options: {
        disabled: !1,
        options: [
          { label: "Forever", value: 1 },
          { label: "Once", value: 2 },
          { label: "Repeating (X months)", value: 3 }
        ]
      }
    }),
    durationInMonths: import_pieces_framework35.Property.Number({
      displayName: "Duration in Months",
      required: !1,
      description: "Required when Duration is set to Repeating. Ignored otherwise."
    }),
    amountOff: import_pieces_framework35.Property.Number({
      displayName: "Amount Off",
      required: !1,
      description: "Fixed amount discount (in your account currency). Set this OR Percent Off, not both."
    }),
    percentOff: import_pieces_framework35.Property.Number({
      displayName: "Percent Off",
      required: !1,
      description: "Percentage discount (0-100). Set this OR Amount Off, not both."
    }),
    maxRedemptions: import_pieces_framework35.Property.Number({
      displayName: "Max Redemptions",
      required: !1,
      description: "Maximum number of times the coupon can be redeemed across all customers."
    }),
    redeemBy: import_pieces_framework35.Property.DateTime({
      displayName: "Redeem By",
      required: !1,
      description: "Coupon expiration date."
    })
  },
  async run(context) {
    var _a;
    let { amountOff, percentOff, duration, durationInMonths } = context.propsValue;
    if (amountOff == null == (percentOff == null))
      throw new Error("Provide exactly one of Amount Off or Percent Off (not both, not neither).");
    if (duration === 3 && durationInMonths == null)
      throw new Error("Duration in Months is required when Duration is set to Repeating.");
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), body = {
      UniqueIdentifier: context.propsValue.uniqueIdentifier,
      Name: context.propsValue.name,
      IsActive: (_a = context.propsValue.isActive) != null ? _a : !0,
      Duration: duration
    };
    return amountOff != null && (body.AmountOff = amountOff), percentOff != null && (body.PercentOff = percentOff), durationInMonths != null && (body.DurationInMonths = durationInMonths), context.propsValue.maxRedemptions != null && (body.MaxRedemptions = context.propsValue.maxRedemptions), context.propsValue.redeemBy && (body.RedeemBy = context.propsValue.redeemBy), client.post("/api/v1/billing/discountcoupons", body);
  }
});

// ../ap/packages/pieces/community/outseta/src/action/add-invoice.ts
var import_pieces_framework36 = require("@activepieces/pieces-framework");
var addInvoiceAction = (0, import_pieces_framework36.createAction)({
  name: "add_invoice",
  auth: outsetaAuth,
  displayName: "Create Invoice",
  description: "Create an ad-hoc invoice for an account. The invoice is linked to the account's current subscription.",
  audience: "both",
  aiMetadata: {
    description: "Creates an ad-hoc invoice on an account's current subscription with a line item. Use to bill a one-off charge; to record a payment against it use Add Invoice Payment. Requires an active subscription. Not idempotent: each call creates a new invoice.",
    idempotent: !1
  },
  props: {
    accountUid: import_pieces_framework36.Property.ShortText({
      displayName: "Account UID",
      required: !0,
      description: "The UID of the account to invoice."
    }),
    description: import_pieces_framework36.Property.ShortText({
      displayName: "Description",
      required: !0,
      description: "Description for the invoice line item."
    }),
    amount: import_pieces_framework36.Property.Number({
      displayName: "Amount",
      required: !0,
      description: "Amount for the invoice line item."
    })
  },
  async run(context) {
    var _a;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), account = await client.get(
      `/api/v1/crm/accounts/${context.propsValue.accountUid}?fields=CurrentSubscription.*`
    ), subscriptionUid = (_a = account == null ? void 0 : account.CurrentSubscription) == null ? void 0 : _a.Uid;
    if (!subscriptionUid)
      throw new Error(
        `Account ${context.propsValue.accountUid} does not have an active subscription.`
      );
    let body = {
      Subscription: { Uid: subscriptionUid },
      InvoiceLineItems: [
        {
          Description: context.propsValue.description,
          Amount: context.propsValue.amount
        }
      ]
    };
    return await client.post("/api/v1/billing/invoices", body);
  }
});

// ../ap/packages/pieces/community/outseta/src/action/add-invoice-payment.ts
var import_pieces_framework37 = require("@activepieces/pieces-framework");
var addInvoicePaymentAction = (0, import_pieces_framework37.createAction)({
  name: "add_invoice_payment",
  auth: outsetaAuth,
  displayName: "Add Invoice Payment",
  description: "Record a payment against an invoice. The invoice is marked as paid if the total payments match the invoice amount.",
  audience: "both",
  aiMetadata: {
    description: "Records a payment transaction against a specific invoice, by account and invoice UID. Use to log a payment on an invoice; to (re)trigger automatic collection of a recorded payment use Process Payment instead. Amount must be negative per the Outseta API. Not idempotent: each call appends a payment transaction.",
    idempotent: !1
  },
  props: {
    accountUid: import_pieces_framework37.Property.ShortText({
      displayName: "Account UID",
      required: !0,
      description: "The UID of the account that owns the invoice."
    }),
    invoiceUid: import_pieces_framework37.Property.ShortText({
      displayName: "Invoice UID",
      required: !0,
      description: "The UID of the invoice to apply the payment to."
    }),
    amount: import_pieces_framework37.Property.Number({
      displayName: "Amount",
      required: !0,
      description: "Payment amount as a negative number (e.g. -49.00), as required by the Outseta API."
    })
  },
  async run(context) {
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), body = {
      Account: { Uid: context.propsValue.accountUid },
      Invoice: { Uid: context.propsValue.invoiceUid },
      Amount: context.propsValue.amount
    };
    return await client.post(
      "/api/v1/billing/transactions/payment",
      body
    );
  }
});

// ../ap/packages/pieces/community/outseta/src/action/send-invoice-email.ts
var import_pieces_framework38 = require("@activepieces/pieces-framework");
var sendInvoiceEmailAction = (0, import_pieces_framework38.createAction)({
  name: "send_invoice_email",
  auth: outsetaAuth,
  displayName: "Send Invoice Email",
  description: "Send (or resend) the invoice email to the customer associated with an invoice.",
  audience: "both",
  aiMetadata: {
    description: "Sends (or resends) the invoice email to the customer for a given invoice UID. Use to deliver a copy of an invoice. Not idempotent: each call sends another email.",
    idempotent: !1
  },
  props: {
    invoiceUid: import_pieces_framework38.Property.ShortText({
      displayName: "Invoice UID",
      description: "The UID of the invoice whose email should be sent.",
      required: !0
    })
  },
  async run(context) {
    return await new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }).post(
      `/api/v1/billing/invoices/${context.propsValue.invoiceUid}/sendinvoiceemail`,
      {}
    ), {
      invoice_uid: context.propsValue.invoiceUid,
      email_sent: !0
    };
  }
});

// ../ap/packages/pieces/community/outseta/src/action/process-payment.ts
var import_pieces_framework39 = require("@activepieces/pieces-framework");
var processPaymentAction = (0, import_pieces_framework39.createAction)({
  name: "process_payment",
  auth: outsetaAuth,
  displayName: "Process Payment",
  description: "Manually trigger the processing (retry) of a previously recorded payment. Typically used to retry a failed payment after a customer updates their card.",
  audience: "both",
  aiMetadata: {
    description: "Triggers (re)processing of an existing payment by its payment UID, e.g. to retry a declined charge after a card update. Use to collect a previously recorded payment; to log a manual payment against an invoice use Add Invoice Payment. Not idempotent: a successful retry can capture funds, so repeating may charge again.",
    idempotent: !1
  },
  props: {
    paymentUid: import_pieces_framework39.Property.ShortText({
      displayName: "Payment UID",
      description: "The UID of the payment to process. You can get it from a 'Subscription Payment Declined' webhook payload, or from the 'List Account Transactions' / 'Get Last Payment for Account' actions.",
      required: !0
    })
  },
  async run(context) {
    let result = await new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }).post(
      `/api/v1/billing/payments/${context.propsValue.paymentUid}/processpayment`,
      {}
    );
    return {
      payment_uid: context.propsValue.paymentUid,
      processed: !0,
      response: result
    };
  }
});

// ../ap/packages/pieces/community/outseta/src/action/manage-account-membership.ts
var import_pieces_framework40 = require("@activepieces/pieces-framework");
var manageAccountMembershipAction = (0, import_pieces_framework40.createAction)({
  name: "manage_account_membership",
  auth: outsetaAuth,
  displayName: "Manage Account Membership",
  description: "Add or remove a person from an account.",
  audience: "both",
  aiMetadata: {
    description: "Adds or removes a person on an account (membership), by account and person UID. Use to attach or detach a contact; to change an existing member's primary-contact flag use Update Account Membership. Idempotent: re-adding an existing member or removing a non-member is a no-op.",
    idempotent: !0
  },
  props: {
    action: import_pieces_framework40.Property.StaticDropdown({
      displayName: "Action",
      required: !0,
      options: {
        disabled: !1,
        options: [
          { label: "Add Person to Account", value: "add" },
          { label: "Remove Person from Account", value: "remove" }
        ]
      }
    }),
    accountUid: import_pieces_framework40.Property.ShortText({
      displayName: "Account UID",
      required: !0,
      description: "The UID of the account."
    }),
    personUid: import_pieces_framework40.Property.ShortText({
      displayName: "Person UID",
      required: !0,
      description: "The UID of the person to add or remove."
    }),
    isPrimary: import_pieces_framework40.Property.Checkbox({
      displayName: "Is Primary Contact",
      required: !1,
      defaultValue: !1,
      description: "Only used when adding: whether this person should be the primary contact for the account."
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), account = await client.get(
      `/api/v1/crm/accounts/${context.propsValue.accountUid}?fields=*,BillingAddress.*,MailingAddress.*,PrimaryContact.*,PersonAccount.*,PersonAccount.Person.*,Subscriptions.*`
    ), existingMemberships = (_d = (_c = (_a = account.PersonAccount) == null ? void 0 : _a.items) != null ? _c : (_b = account.PersonAccount) == null ? void 0 : _b.Items) != null ? _d : Array.isArray(account.PersonAccount) ? account.PersonAccount : [];
    if (context.propsValue.action === "add") {
      if (existingMemberships.some(
        (pa) => {
          var _a2;
          return ((_a2 = pa.Person) == null ? void 0 : _a2.Uid) === context.propsValue.personUid;
        }
      ))
        return {
          action: "already_member",
          account_uid: context.propsValue.accountUid,
          person_uid: context.propsValue.personUid
        };
      let updatedMemberships = [
        ...existingMemberships.map((pa) => {
          var _a2, _b2;
          return {
            Uid: pa.Uid,
            Person: { Uid: (_a2 = pa.Person) == null ? void 0 : _a2.Uid },
            IsPrimary: (_b2 = pa.IsPrimary) != null ? _b2 : !1
          };
        }),
        {
          Person: { Uid: context.propsValue.personUid },
          IsPrimary: (_e = context.propsValue.isPrimary) != null ? _e : !1
        }
      ], flatSubscriptions = account.Subscriptions && !Array.isArray(account.Subscriptions) ? (_g = (_f = account.Subscriptions.items) != null ? _f : account.Subscriptions.Items) != null ? _g : [] : account.Subscriptions, result = await client.put(
        `/api/v1/crm/accounts/${context.propsValue.accountUid}`,
        __spreadProps(__spreadValues({}, account), { PersonAccount: updatedMemberships, Subscriptions: flatSubscriptions })
      );
      return {
        action: "added",
        account_uid: (_h = result.Uid) != null ? _h : null,
        account_name: (_i = result.Name) != null ? _i : null,
        person_count: Array.isArray(result.PersonAccount) ? result.PersonAccount.length : null
      };
    }
    let membership = existingMemberships.find(
      (pa) => {
        var _a2;
        return ((_a2 = pa.Person) == null ? void 0 : _a2.Uid) === context.propsValue.personUid;
      }
    );
    return membership ? (await client.delete(
      `/api/v1/crm/accounts/${context.propsValue.accountUid}/memberships/${membership.Uid}`
    ), {
      action: "removed",
      account_uid: context.propsValue.accountUid,
      membership_uid: membership.Uid,
      person_uid: context.propsValue.personUid
    }) : {
      action: "not_a_member",
      account_uid: context.propsValue.accountUid,
      person_uid: context.propsValue.personUid
    };
  }
});

// ../ap/packages/pieces/community/outseta/src/action/update-account-membership.ts
var import_pieces_framework41 = require("@activepieces/pieces-framework");
var updateAccountMembershipAction = (0, import_pieces_framework41.createAction)({
  name: "update_account_membership",
  auth: outsetaAuth,
  displayName: "Update Account Membership",
  description: "Update a person's membership on an account \u2014 typically used to designate them as the primary contact.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing person's membership on an account \u2014 typically to set them as the primary contact \u2014 by account and person UID. Use to change a member's role; to add or remove the member entirely use Manage Account Membership. Idempotent: setting the same primary-contact state again has no further effect.",
    idempotent: !0
  },
  props: {
    accountUid: import_pieces_framework41.Property.ShortText({
      displayName: "Account UID",
      description: "The UID of the account.",
      required: !0
    }),
    personUid: import_pieces_framework41.Property.ShortText({
      displayName: "Person UID",
      description: "The UID of the person whose membership to update.",
      required: !0
    }),
    isPrimary: import_pieces_framework41.Property.Checkbox({
      displayName: "Is Primary Contact",
      required: !0,
      defaultValue: !0,
      description: "Whether this person should be the primary contact for the account."
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), account = await client.get(
      `/api/v1/crm/accounts/${context.propsValue.accountUid}?fields=Uid,PersonAccount.*,PersonAccount.Person.Uid`
    ), membership = ((_d = (_c = (_a = account.PersonAccount) == null ? void 0 : _a.items) != null ? _c : (_b = account.PersonAccount) == null ? void 0 : _b.Items) != null ? _d : Array.isArray(account.PersonAccount) ? account.PersonAccount : []).find(
      (pa) => {
        var _a2;
        return ((_a2 = pa.Person) == null ? void 0 : _a2.Uid) === context.propsValue.personUid;
      }
    );
    if (!membership)
      throw new Error(
        `Person ${context.propsValue.personUid} is not a member of account ${context.propsValue.accountUid}.`
      );
    let result = await client.put(
      `/api/v1/crm/accounts/${context.propsValue.accountUid}/memberships/${membership.Uid}`,
      {
        Account: { Uid: context.propsValue.accountUid },
        Person: { Uid: context.propsValue.personUid },
        IsPrimary: context.propsValue.isPrimary
      }
    );
    return {
      account_uid: context.propsValue.accountUid,
      membership_uid: membership.Uid,
      person_uid: context.propsValue.personUid,
      is_primary: (_e = result == null ? void 0 : result.IsPrimary) != null ? _e : context.propsValue.isPrimary
    };
  }
});

// ../ap/packages/pieces/community/outseta/src/action/add-custom-activity.ts
var import_pieces_framework42 = require("@activepieces/pieces-framework");
var addCustomActivityAction = (0, import_pieces_framework42.createAction)({
  name: "add_custom_activity",
  auth: outsetaAuth,
  displayName: "Add Custom Activity",
  description: "Record a custom activity on an account, person, or deal. Activities show on the activity feed and can trigger drip campaigns.",
  audience: "both",
  aiMetadata: {
    description: "Records a custom activity entry on an account, person, or deal (entity type selects which, then its UID), appearing in the activity feed and able to start/stop drip campaigns. Use to log a custom event or drive automation; the title must match the drip-campaign start/stop value when used for automation. Not idempotent: each call appends a new activity record.",
    idempotent: !1
  },
  props: {
    title: import_pieces_framework42.Property.ShortText({
      displayName: "Title",
      required: !0,
      description: "The activity title. Must match the start/stop value in drip campaigns if used for automation."
    }),
    entityType: import_pieces_framework42.Property.StaticDropdown({
      displayName: "Entity Type",
      required: !0,
      options: {
        options: [
          { label: "Account", value: 1 },
          { label: "Person", value: 2 },
          { label: "Deal", value: 3 }
        ]
      }
    }),
    entityUid: import_pieces_framework42.Property.Dropdown({
      auth: outsetaAuth,
      displayName: "Entity",
      description: "Select the account, person, or deal to record the activity on.",
      refreshers: ["entityType"],
      required: !0,
      options: async ({ auth, entityType }) => {
        var _a, _b;
        let client = makeClient(auth);
        if (!client)
          return { disabled: !0, options: [], placeholder: "Connect your Outseta account first." };
        if (!entityType)
          return { disabled: !0, options: [], placeholder: "Select an entity type first." };
        try {
          let path = "";
          if (entityType === 1) path = "/api/v1/crm/accounts?limit=100";
          else if (entityType === 2) path = "/api/v1/crm/people?limit=100";
          else if (entityType === 3) path = "/api/v1/crm/deals?limit=100";
          else return { disabled: !0, options: [], placeholder: "Unknown entity type." };
          let res = await client.get(path);
          return {
            disabled: !1,
            options: ((_b = (_a = res == null ? void 0 : res.items) != null ? _a : res == null ? void 0 : res.Items) != null ? _b : []).map((item) => {
              var _a2, _b2;
              return entityType === 2 ? { label: `${item.FullName} (${(_a2 = item.Email) != null ? _a2 : item.Uid})`, value: item.Uid } : { label: (_b2 = item.Name) != null ? _b2 : item.Uid, value: item.Uid };
            })
          };
        } catch (e) {
          return { disabled: !0, options: [], placeholder: "Failed to load entities." };
        }
      }
    }),
    description: import_pieces_framework42.Property.LongText({
      displayName: "Description",
      required: !1
    }),
    activityData: import_pieces_framework42.Property.LongText({
      displayName: "Data",
      required: !1,
      description: "Optional string to store serialized JSON or other metadata with this activity."
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), body = {
      Title: context.propsValue.title,
      EntityType: context.propsValue.entityType,
      EntityUid: context.propsValue.entityUid
    };
    context.propsValue.description && (body.Description = context.propsValue.description), context.propsValue.activityData && (body.ActivityData = context.propsValue.activityData);
    let result = await client.post(
      "/api/v1/activities/customactivity",
      body
    );
    return {
      uid: (_a = result.Uid) != null ? _a : null,
      title: (_b = result.Title) != null ? _b : null,
      entity_type: (_c = result.EntityType) != null ? _c : null,
      entity_uid: (_d = result.EntityUid) != null ? _d : null,
      created: (_e = result.Created) != null ? _e : null
    };
  }
});

// ../ap/packages/pieces/community/outseta/src/action/manage-email-list-subscription.ts
var import_pieces_framework43 = require("@activepieces/pieces-framework");
var manageEmailListSubscriptionAction = (0, import_pieces_framework43.createAction)({
  name: "manage_email_list_subscription",
  auth: outsetaAuth,
  displayName: "Manage Email List Subscription",
  description: "Subscribe a person to an email list, or unsubscribe them from it.",
  audience: "both",
  aiMetadata: {
    description: "Subscribes or unsubscribes a person on an email list, by email and list. Use to manage list membership in a single action. Idempotent: re-subscribing an existing subscriber or unsubscribing a non-subscriber is a no-op.",
    idempotent: !0
  },
  props: {
    action: import_pieces_framework43.Property.StaticDropdown({
      displayName: "Action",
      required: !0,
      options: {
        disabled: !1,
        options: [
          { label: "Subscribe", value: "subscribe" },
          { label: "Unsubscribe", value: "unsubscribe" }
        ]
      }
    }),
    emailListUid: emailListUidDropdown(),
    email: import_pieces_framework43.Property.ShortText({
      displayName: "Email",
      required: !0,
      description: "The email address to subscribe or unsubscribe."
    }),
    firstName: import_pieces_framework43.Property.ShortText({
      displayName: "First Name",
      required: !1,
      description: "Only used when subscribing."
    }),
    lastName: import_pieces_framework43.Property.ShortText({
      displayName: "Last Name",
      required: !1,
      description: "Only used when subscribing."
    }),
    sendWelcomeEmail: import_pieces_framework43.Property.Checkbox({
      displayName: "Send Welcome Email",
      required: !1,
      defaultValue: !1,
      description: "Only used when subscribing. If checked, Outseta will send the list welcome email."
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), subscription = (await client.getAllPages(
      `/api/v1/email/lists/${context.propsValue.emailListUid}/subscriptions?Person.Email=${encodeURIComponent(context.propsValue.email)}&fields=*,Person.*`
    )).find(
      (s) => {
        var _a2, _b2;
        return ((_b2 = (_a2 = s.Person) == null ? void 0 : _a2.Email) == null ? void 0 : _b2.toLowerCase()) === context.propsValue.email.toLowerCase();
      }
    );
    if (context.propsValue.action === "subscribe") {
      if (subscription)
        return {
          action: "already_subscribed",
          uid: (_a = subscription.Uid) != null ? _a : null,
          email_list_uid: context.propsValue.emailListUid,
          person_email: (_c = (_b = subscription.Person) == null ? void 0 : _b.Email) != null ? _c : null,
          person_uid: (_e = (_d = subscription.Person) == null ? void 0 : _d.Uid) != null ? _e : null
        };
      let person = {
        Email: context.propsValue.email
      };
      context.propsValue.firstName && (person.FirstName = context.propsValue.firstName), context.propsValue.lastName && (person.LastName = context.propsValue.lastName);
      let result = await client.post(
        `/api/v1/email/lists/${context.propsValue.emailListUid}/subscriptions`,
        {
          EmailList: { Uid: context.propsValue.emailListUid },
          Person: person,
          SendWelcomeEmail: (_f = context.propsValue.sendWelcomeEmail) != null ? _f : !1
        }
      );
      return {
        action: "subscribed",
        uid: (_g = result.Uid) != null ? _g : null,
        email_list_uid: (_i = (_h = result.EmailList) == null ? void 0 : _h.Uid) != null ? _i : null,
        person_email: (_k = (_j = result.Person) == null ? void 0 : _j.Email) != null ? _k : null,
        person_uid: (_m = (_l = result.Person) == null ? void 0 : _l.Uid) != null ? _m : null,
        created: (_n = result.Created) != null ? _n : null
      };
    }
    return subscription ? (await client.delete(
      `/api/v1/email/lists/${context.propsValue.emailListUid}/subscriptions/${subscription.Uid}`
    ), {
      action: "unsubscribed",
      email_list_uid: context.propsValue.emailListUid,
      subscription_uid: subscription.Uid,
      email: context.propsValue.email
    }) : {
      action: "not_subscribed",
      email_list_uid: context.propsValue.emailListUid,
      email: context.propsValue.email
    };
  }
});

// ../ap/packages/pieces/community/outseta/src/action/send-confirmation-email.ts
var import_pieces_framework44 = require("@activepieces/pieces-framework");
var sendConfirmationEmailAction = (0, import_pieces_framework44.createAction)({
  name: "send_confirmation_email",
  auth: outsetaAuth,
  displayName: "Send Confirmation Email",
  description: "Send a confirmation email to the primary contact of an account, a specific person, or all members.",
  audience: "both",
  aiMetadata: {
    description: "Sends the account confirmation/activation email to the primary contact, a specific person, or all members of an account. Use to (re)deliver activation emails. Not idempotent: each call sends another email.",
    idempotent: !1
  },
  props: {
    accountUid: import_pieces_framework44.Property.ShortText({
      displayName: "Account UID",
      required: !0,
      description: "The UID of the account."
    }),
    personUid: import_pieces_framework44.Property.ShortText({
      displayName: "Person UID",
      required: !1,
      description: "Leave empty for primary contact, or use * for all members."
    })
  },
  async run(context) {
    var _a;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), url = `/api/v1/crm/accounts/${context.propsValue.accountUid}/send-confirmation-email`;
    return context.propsValue.personUid && (url += `?personUid=${encodeURIComponent(context.propsValue.personUid)}`), await client.put(url, {}), {
      account_uid: context.propsValue.accountUid,
      person_uid: (_a = context.propsValue.personUid) != null ? _a : null,
      sent: !0
    };
  }
});

// ../ap/packages/pieces/community/outseta/src/action/add-case.ts
var import_pieces_framework45 = require("@activepieces/pieces-framework");
var addCaseAction = (0, import_pieces_framework45.createAction)({
  name: "add_case",
  auth: outsetaAuth,
  displayName: "Create Ticket",
  description: "Create a new support ticket (case) in Outseta.",
  audience: "both",
  aiMetadata: {
    description: "Opens a new support ticket (case) from a person, with subject and body. Use to file a customer support request; to respond within an existing ticket use Reply to Ticket. Not idempotent: each call creates a new case.",
    idempotent: !1
  },
  props: {
    personUid: import_pieces_framework45.Property.ShortText({
      displayName: "Person UID",
      required: !0,
      description: "The UID of the person submitting the ticket."
    }),
    subject: import_pieces_framework45.Property.ShortText({
      displayName: "Subject",
      required: !0,
      description: "Subject line of the ticket."
    }),
    body: import_pieces_framework45.Property.LongText({
      displayName: "Body",
      required: !0,
      description: "Body content of the ticket."
    }),
    source: import_pieces_framework45.Property.StaticDropdown({
      displayName: "Source",
      required: !0,
      description: "The source channel of the ticket.",
      options: {
        disabled: !1,
        options: [
          { label: "Website", value: 1 },
          { label: "Email", value: 2 },
          { label: "Facebook", value: 3 },
          { label: "Twitter", value: 4 }
        ]
      }
    }),
    sendAutoResponder: import_pieces_framework45.Property.Checkbox({
      displayName: "Send Auto Responder",
      required: !1,
      defaultValue: !0,
      description: "Whether to send an auto-responder email to the person."
    })
  },
  async run(context) {
    var _a;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), sendAutoResponder = (_a = context.propsValue.sendAutoResponder) != null ? _a : !0, body = {
      FromPerson: { Uid: context.propsValue.personUid },
      Subject: context.propsValue.subject,
      Body: context.propsValue.body,
      Source: context.propsValue.source
    };
    return await client.post(
      `/api/v1/support/cases?sendAutoResponder=${sendAutoResponder}`,
      body
    );
  }
});

// ../ap/packages/pieces/community/outseta/src/action/add-reply.ts
var import_pieces_framework46 = require("@activepieces/pieces-framework");
var addReplyAction = (0, import_pieces_framework46.createAction)({
  name: "add_reply",
  auth: outsetaAuth,
  displayName: "Reply to Ticket",
  description: "Add a reply to an existing support ticket (case) in Outseta.",
  audience: "both",
  aiMetadata: {
    description: "Posts an agent reply onto an existing support ticket, by case UID. Use to respond within a ticket thread; to open a new ticket use Create Ticket. Not idempotent: each call appends a new reply.",
    idempotent: !1
  },
  props: {
    caseUid: import_pieces_framework46.Property.ShortText({
      displayName: "Case UID",
      required: !0,
      description: "The UID of the support ticket to reply to."
    }),
    agentName: import_pieces_framework46.Property.ShortText({
      displayName: "Agent Name",
      required: !0,
      description: "Name of the agent sending the reply."
    }),
    comment: import_pieces_framework46.Property.LongText({
      displayName: "Comment",
      required: !0,
      description: "The reply content."
    })
  },
  async run(context) {
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), body = {
      AgentName: context.propsValue.agentName,
      Case: { Uid: context.propsValue.caseUid },
      Comment: context.propsValue.comment
    };
    return await client.post(
      `/api/v1/support/cases/${context.propsValue.caseUid}/replies`,
      body
    );
  }
});

// ../ap/packages/pieces/community/outseta/src/triggers/new-account-event.ts
var import_pieces_framework47 = require("@activepieces/pieces-framework");
var newAccountEventTrigger = (0, import_pieces_framework47.createTrigger)({
  name: "new_account_event",
  auth: outsetaAuth,
  displayName: "New Account Event",
  description: "Triggers on account-scoped events (lifecycle, stage, billing, subscription, invoice).",
  type: import_pieces_framework47.TriggerStrategy.WEBHOOK,
  aiMetadata: {
    description: "Fires on any account-scoped Outseta event (account lifecycle, stage change, billing, subscription, invoice). Each webhook delivers one Account object; the specific event's details are nested in its ActivityEventData."
  },
  props: {
    setup: import_pieces_framework47.Property.MarkDown({
      value: `
- In Outseta, go to **Settings \u2192 Notifications \u2192 Add Notification**.
- Select the account event and paste the webhook URL in the callback field:
  \`\`\`text
  {{webhookUrl}}
  \`\`\`
- Create one notification per event, all pointing to this same URL.
`
    }),
    eventSubTypes: import_pieces_framework47.Property.StaticMultiSelectDropdown({
      displayName: "Events",
      description: "Select one or more account-scoped events to listen for. Configure a matching Outseta notification for each, all pointing to this webhook URL.",
      required: !0,
      options: {
        disabled: !1,
        // Mirrors the Outseta admin dropdown verbatim (Settings → Notifications →
        // Add Notification → Activity Type), so users can match each option here
        // 1:1 with the notifications they configure on the Outseta side.
        options: [
          { label: "Account Created", value: "account_created" },
          { label: "Account Updated", value: "account_updated" },
          { label: "Account Add Person", value: "account_add_person" },
          { label: "Account Remove Person", value: "account_remove_person" },
          { label: "Account Primary Person Updated", value: "account_primary_person_updated" },
          { label: "Account Stage Updated", value: "account_stage_updated" },
          { label: "Account Deleted", value: "account_deleted" },
          { label: "Account Billing Information Removed", value: "account_billing_information_removed" },
          { label: "Account Billing Information Requested", value: "account_billing_information_requested" },
          { label: "Account Billing Information Updated", value: "account_billing_information_updated" },
          { label: "Account Billing Invoice Email Sent", value: "account_billing_invoice_email_sent" },
          { label: "Account Billing Invoice Created", value: "account_billing_invoice_created" },
          { label: "Account Billing Invoice Deleted", value: "account_billing_invoice_deleted" },
          { label: "Account Paid Subscription Created", value: "account_paid_subscription_created" },
          { label: "Account Subscription Add Ons Changed", value: "account_subscription_add_ons_changed" },
          { label: "Account Subscription Cancellation Requested", value: "account_subscription_cancellation_requested" },
          { label: "Account Subscription Plan Updated", value: "account_subscription_plan_updated" },
          { label: "Account Subscription Payment Collected", value: "account_subscription_payment_collected" },
          { label: "Account Subscription Payment Declined", value: "account_subscription_payment_declined" },
          { label: "Account Subscription Renewal Extended", value: "account_subscription_renewal_extended" },
          { label: "Account Subscription Started", value: "account_subscription_started" },
          // Manual activity events. Per Outseta support, these fire with the
          // entity they are attached to as the webhook payload — when logged
          // against an Account, the body is the Account.
          { label: "Custom", value: "custom" },
          { label: "Note", value: "note" },
          { label: "Email", value: "email" },
          { label: "Phone Call", value: "phone_call" },
          { label: "Meeting", value: "meeting" },
          { label: "Chat", value: "chat" }
        ]
      }
    })
  },
  sampleData: {
    Uid: "abc123xyz",
    Name: "Example Corp",
    AccountStage: 2,
    AccountStageLabel: "Subscribing",
    ClientIdentifier: null,
    InvoiceNotes: null,
    PersonAccount: [],
    Subscriptions: [],
    ActivityEventData: null,
    Created: "2024-01-10T00:00:00Z",
    Updated: "2024-01-10T00:00:00Z"
  },
  async onEnable() {
  },
  async onDisable() {
  },
  async run(context) {
    return [context.payload.body];
  },
  async test(context) {
    var _a, _b, _c;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), selected = (_a = context.propsValue.eventSubTypes) != null ? _a : [], orderProperty = selected.length > 0 && selected.every((s) => s.endsWith("_created")) ? "Created" : "Updated", res = await client.get(
      `/api/v1/crm/accounts?limit=5&orderBy=${orderProperty}%20DESC`
    );
    return (_c = (_b = res == null ? void 0 : res.items) != null ? _b : res == null ? void 0 : res.Items) != null ? _c : [];
  }
});

// ../ap/packages/pieces/community/outseta/src/triggers/new-person-event.ts
var import_pieces_framework48 = require("@activepieces/pieces-framework");
var newPersonEventTrigger = (0, import_pieces_framework48.createTrigger)({
  name: "new_person_event",
  auth: outsetaAuth,
  displayName: "New Person Event",
  description: "Triggers on person-scoped events (lifecycle, login, email engagement, list, segment, support ticket).",
  type: import_pieces_framework48.TriggerStrategy.WEBHOOK,
  aiMetadata: {
    description: "Fires on any person-scoped Outseta event (lifecycle, login, email engagement, list, segment, support ticket). Each webhook delivers one Person object; the specific event's details are nested in its ActivityEventData."
  },
  props: {
    setup: import_pieces_framework48.Property.MarkDown({
      value: `
- In Outseta, go to **Settings \u2192 Notifications \u2192 Add Notification**.
- Select the person event and paste the webhook URL in the callback field:
  \`\`\`text
  {{webhookUrl}}
  \`\`\`
- Create one notification per event, all pointing to this same URL.
`
    }),
    eventSubTypes: import_pieces_framework48.Property.StaticMultiSelectDropdown({
      displayName: "Events",
      description: "Select one or more person-scoped events to listen for. Configure a matching Outseta notification for each, all pointing to this webhook URL.",
      required: !0,
      options: {
        disabled: !1,
        // Mirrors the Outseta admin dropdown verbatim (Settings → Notifications →
        // Add Notification → Activity Type), so users can match each option here
        // 1:1 with the notifications they configure on the Outseta side.
        options: [
          { label: "Person Created", value: "person_created" },
          { label: "Person Updated", value: "person_updated" },
          { label: "Person Deleted", value: "person_deleted" },
          { label: "Person Login", value: "person_login" },
          { label: "Person Temporary Password Set", value: "person_temporary_password_set" },
          { label: "Person List Subscribed", value: "person_list_subscribed" },
          { label: "Person List Confirmed", value: "person_list_confirmed" },
          { label: "Person List Unsubscribed", value: "person_list_unsubscribed" },
          { label: "Person Segment Added", value: "person_segment_added" },
          { label: "Person Segment Removed", value: "person_segment_removed" },
          { label: "Person Email Opened", value: "person_email_opened" },
          { label: "Person Email Clicked", value: "person_email_clicked" },
          { label: "Person Email Bounce", value: "person_email_bounce" },
          { label: "Person Email Spam", value: "person_email_spam" },
          { label: "Person Email Subscribed", value: "person_email_subscribed" },
          { label: "Person Email Unsubscribed", value: "person_email_unsubscribed" },
          { label: "Person Support Ticket Created", value: "person_support_ticket_created" },
          { label: "Person Support Ticket Updated", value: "person_support_ticket_updated" },
          { label: "Person Support Ticket Closed", value: "person_support_ticket_closed" },
          { label: "Person Lead Form Submitted", value: "person_lead_form_submitted" },
          // Discord events. Per Outseta support, Discord activities are logged
          // with Person as the entity, so the webhook body is a Person.
          { label: "Discord User Linked", value: "discord_user_linked" },
          { label: "Discord User Added To Server", value: "discord_user_added_to_server" },
          { label: "Discord User Removed From Server", value: "discord_user_removed_from_server" },
          { label: "Discord User Roles Updated", value: "discord_user_roles_updated" },
          // Manual activity events. Per Outseta support, these fire with the
          // entity they are attached to as the webhook payload — when logged
          // against a Person (the default for Email and Chat), the body is the
          // Person.
          { label: "Custom", value: "custom" },
          { label: "Note", value: "note" },
          { label: "Email", value: "email" },
          { label: "Phone Call", value: "phone_call" },
          { label: "Meeting", value: "meeting" },
          { label: "Chat", value: "chat" }
        ]
      }
    })
  },
  sampleData: {
    Uid: "abc123xyz",
    Email: "jane.doe@example.com",
    FirstName: "Jane",
    LastName: "Doe",
    FullName: "Jane Doe",
    MailingAddress: null,
    Account: null,
    ActivityEventData: null,
    Created: "2024-01-10T00:00:00Z",
    Updated: "2024-01-10T00:00:00Z"
  },
  async onEnable() {
  },
  async onDisable() {
  },
  async run(context) {
    return [context.payload.body];
  },
  async test(context) {
    var _a, _b, _c;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), selected = (_a = context.propsValue.eventSubTypes) != null ? _a : [], orderProperty = selected.length > 0 && selected.every((s) => s.endsWith("_created")) ? "Created" : "Updated", res = await client.get(
      `/api/v1/crm/people?limit=5&orderBy=${orderProperty}%20DESC`
    );
    return (_c = (_b = res == null ? void 0 : res.items) != null ? _b : res == null ? void 0 : res.Items) != null ? _c : [];
  }
});

// ../ap/packages/pieces/community/outseta/src/triggers/new-deal-event.ts
var import_pieces_framework49 = require("@activepieces/pieces-framework");
var newDealEventTrigger = (0, import_pieces_framework49.createTrigger)({
  name: "new_deal_event",
  auth: outsetaAuth,
  displayName: "New Deal Event",
  description: "Triggers on deal-scoped events (lifecycle, contact/account association).",
  type: import_pieces_framework49.TriggerStrategy.WEBHOOK,
  aiMetadata: {
    description: "Fires on any deal-scoped Outseta event (lifecycle, contact/account association). Each webhook delivers one Deal object; the specific event's details are nested in its ActivityEventData."
  },
  props: {
    setup: import_pieces_framework49.Property.MarkDown({
      value: `
- In Outseta, go to **Settings \u2192 Notifications \u2192 Add Notification**.
- Select the deal event and paste the webhook URL in the callback field:
  \`\`\`text
  {{webhookUrl}}
  \`\`\`
- Create one notification per event, all pointing to this same URL.
`
    }),
    eventSubTypes: import_pieces_framework49.Property.StaticMultiSelectDropdown({
      displayName: "Events",
      description: "Select one or more deal-scoped events to listen for. Configure a matching Outseta notification for each, all pointing to this webhook URL.",
      required: !0,
      options: {
        disabled: !1,
        // Mirrors the Outseta admin dropdown verbatim (Settings → Notifications →
        // Add Notification → Activity Type), so users can match each option here
        // 1:1 with the notifications they configure on the Outseta side.
        options: [
          { label: "Deal Created", value: "deal_created" },
          { label: "Deal Updated", value: "deal_updated" },
          { label: "Deal Add Person", value: "deal_add_person" },
          { label: "Deal Add Account", value: "deal_add_account" },
          { label: "Deal Deleted", value: "deal_deleted" },
          { label: "Deal Due Date", value: "deal_due_date" },
          // Manual activity events. Per Outseta support, these fire with the
          // entity they are attached to as the webhook payload — when logged
          // against a Deal, the body is the Deal.
          { label: "Custom", value: "custom" },
          { label: "Note", value: "note" },
          { label: "Email", value: "email" },
          { label: "Phone Call", value: "phone_call" },
          { label: "Meeting", value: "meeting" },
          { label: "Chat", value: "chat" }
        ]
      }
    })
  },
  sampleData: {
    Uid: "abc123xyz",
    Name: "Example Deal",
    Amount: 1e3,
    DueDate: null,
    DealPipelineStage: null,
    Account: null,
    DealPeople: [],
    ActivityEventData: null,
    Created: "2024-01-10T00:00:00Z",
    Updated: "2024-01-10T00:00:00Z"
  },
  async onEnable() {
  },
  async onDisable() {
  },
  async run(context) {
    return [context.payload.body];
  },
  async test(context) {
    var _a, _b, _c;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), selected = (_a = context.propsValue.eventSubTypes) != null ? _a : [], orderProperty = selected.length > 0 && selected.every((s) => s.endsWith("_created")) ? "Created" : "Updated", res = await client.get(
      `/api/v1/crm/deals?limit=5&orderBy=${orderProperty}%20DESC`
    );
    return (_c = (_b = res == null ? void 0 : res.items) != null ? _b : res == null ? void 0 : res.Items) != null ? _c : [];
  }
});

// ../ap/packages/pieces/community/outseta/src/triggers/new-task-event.ts
var import_pieces_framework50 = require("@activepieces/pieces-framework");
var newTaskEventTrigger = (0, import_pieces_framework50.createTrigger)({
  name: "new_task_event",
  auth: outsetaAuth,
  displayName: "New Task Event",
  description: "Triggers on task lifecycle events (created, updated).",
  type: import_pieces_framework50.TriggerStrategy.WEBHOOK,
  aiMetadata: {
    description: "Fires on Outseta task events (created, updated). Each webhook delivers one Task object; the event's details are nested in its ActivityEventData."
  },
  props: {
    setup: import_pieces_framework50.Property.MarkDown({
      value: `
- In Outseta, go to **Settings \u2192 Notifications \u2192 Add Notification**.
- Select the task event and paste the webhook URL in the callback field:
  \`\`\`text
  {{webhookUrl}}
  \`\`\`
- Create one notification per event, all pointing to this same URL.
`
    }),
    eventSubTypes: import_pieces_framework50.Property.StaticMultiSelectDropdown({
      displayName: "Events",
      description: "Select one or more Task events to listen for. Configure a matching Outseta notification for each, all pointing to this webhook URL.",
      required: !0,
      options: {
        disabled: !1,
        // Mirrors the Outseta admin dropdown verbatim (Settings → Notifications →
        // Add Notification → Activity Type), so users can match each option here
        // 1:1 with the notifications they configure on the Outseta side.
        options: [
          { label: "Task Created", value: "task_created" },
          { label: "Task Updated", value: "task_updated" }
        ]
      }
    })
  },
  sampleData: {
    Uid: "abc123xyz",
    Title: "Follow up with the customer",
    Description: "Schedule a check-in call to review onboarding progress.",
    DueDate: "2024-01-17T00:00:00Z",
    IsCompleted: !1,
    Account: null,
    Person: null,
    Deal: null,
    ActivityEventData: null,
    Created: "2024-01-10T00:00:00Z",
    Updated: "2024-01-10T00:00:00Z"
  },
  async onEnable() {
  },
  async onDisable() {
  },
  async run(context) {
    return [context.payload.body];
  },
  async test(context) {
    var _a, _b, _c;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), selected = (_a = context.propsValue.eventSubTypes) != null ? _a : [], orderProperty = selected.length > 0 && selected.every((s) => s.endsWith("_created")) ? "Created" : "Updated", res = await client.get(
      `/api/v1/crm/tasks?limit=5&orderBy=${orderProperty}%20DESC`
    );
    return (_c = (_b = res == null ? void 0 : res.items) != null ? _b : res == null ? void 0 : res.Items) != null ? _c : [];
  }
});

// ../ap/packages/pieces/community/outseta/src/triggers/new-plan-event.ts
var import_pieces_framework51 = require("@activepieces/pieces-framework");
var newPlanEventTrigger = (0, import_pieces_framework51.createTrigger)({
  name: "new_plan_event",
  auth: outsetaAuth,
  displayName: "New Plan Event",
  description: "Triggers when a billing plan is created or updated in the Outseta catalog.",
  type: import_pieces_framework51.TriggerStrategy.WEBHOOK,
  aiMetadata: {
    description: "Fires when a billing plan is created or updated in the Outseta catalog (admin configuration), not when a customer's subscription changes. Each webhook delivers one Plan object."
  },
  props: {
    setup: import_pieces_framework51.Property.MarkDown({
      value: `
- In Outseta, go to **Settings \u2192 Notifications \u2192 Add Notification**.
- Select the plan event and paste the webhook URL in the callback field:
  \`\`\`text
  {{webhookUrl}}
  \`\`\`
- Create one notification per event, all pointing to this same URL.
`
    }),
    eventSubTypes: import_pieces_framework51.Property.StaticMultiSelectDropdown({
      displayName: "Events",
      description: "Select one or more Plan catalog events to listen for. Configure a matching Outseta notification for each, all pointing to this webhook URL.",
      required: !0,
      options: {
        disabled: !1,
        // Mirrors the Outseta admin dropdown verbatim (Settings → Notifications →
        // Add Notification → Activity Type), so users can match each option here
        // 1:1 with the notifications they configure on the Outseta side.
        options: [
          { label: "Plan Created", value: "plan_created" },
          { label: "Plan Updated", value: "plan_updated" }
        ]
      }
    })
  },
  sampleData: {
    Uid: "abc123xyz",
    Name: "Pro Annual",
    Description: "Annual professional plan with all features included.",
    MonthlyRate: 0,
    AnnualRate: 348,
    QuarterlyRate: 0,
    OneTimeRate: 0,
    SetupFee: 0,
    IsTaxable: !1,
    IsActive: !0,
    IsPerUser: !1,
    TrialPeriodDays: 14,
    ExpiresAfterMonths: 0,
    PlanFamily: null,
    ActivityEventData: null,
    Created: "2024-01-10T00:00:00Z",
    Updated: "2024-01-10T00:00:00Z"
  },
  async onEnable() {
  },
  async onDisable() {
  },
  async run(context) {
    return [context.payload.body];
  },
  async test(context) {
    var _a, _b, _c;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), selected = (_a = context.propsValue.eventSubTypes) != null ? _a : [], orderProperty = selected.length > 0 && selected.every((s) => s.endsWith("_created")) ? "Created" : "Updated", res = await client.get(
      `/api/v1/billing/plans?limit=5&orderBy=${orderProperty}%20DESC`
    );
    return (_c = (_b = res == null ? void 0 : res.items) != null ? _b : res == null ? void 0 : res.Items) != null ? _c : [];
  }
});

// ../ap/packages/pieces/community/outseta/src/triggers/new-add-on-event.ts
var import_pieces_framework52 = require("@activepieces/pieces-framework");
var newAddOnEventTrigger = (0, import_pieces_framework52.createTrigger)({
  name: "new_add_on_event",
  auth: outsetaAuth,
  displayName: "New Add-On Event",
  description: "Triggers when an add-on is created or updated in the Outseta catalog.",
  type: import_pieces_framework52.TriggerStrategy.WEBHOOK,
  aiMetadata: {
    description: "Fires when an add-on is created or updated in the Outseta catalog (admin configuration), not when a customer's subscription add-ons change. Each webhook delivers one Add-On object."
  },
  props: {
    setup: import_pieces_framework52.Property.MarkDown({
      value: `
- In Outseta, go to **Settings \u2192 Notifications \u2192 Add Notification**.
- Select the add-on event and paste the webhook URL in the callback field:
  \`\`\`text
  {{webhookUrl}}
  \`\`\`
- Create one notification per event, all pointing to this same URL.
`
    }),
    eventSubTypes: import_pieces_framework52.Property.StaticMultiSelectDropdown({
      displayName: "Events",
      description: "Select one or more Add-On catalog events to listen for. Configure a matching Outseta notification for each, all pointing to this webhook URL.",
      required: !0,
      options: {
        disabled: !1,
        // Mirrors the Outseta admin dropdown verbatim (Settings → Notifications →
        // Add Notification → Activity Type), so users can match each option here
        // 1:1 with the notifications they configure on the Outseta side.
        options: [
          { label: "Add On Created", value: "add_on_created" },
          { label: "Add On Updated", value: "add_on_updated" }
        ]
      }
    })
  },
  sampleData: {
    Uid: "abc123xyz",
    Name: "Extra Hours",
    Description: "Additional usage-based hours for the Pro plan.",
    BillingAddOnType: 2,
    MonthlyRate: 0,
    AnnualRate: 0,
    QuarterlyRate: 0,
    OneTimeRate: 0,
    SetupFee: 0,
    IsTaxable: !1,
    IsActive: !0,
    ActivityEventData: null,
    Created: "2024-01-10T00:00:00Z",
    Updated: "2024-01-10T00:00:00Z"
  },
  async onEnable() {
  },
  async onDisable() {
  },
  async run(context) {
    return [context.payload.body];
  },
  async test(context) {
    var _a, _b, _c;
    let client = new OutsetaClient({
      domain: context.auth.props.domain,
      apiKey: context.auth.props.apiKey,
      apiSecret: context.auth.props.apiSecret
    }), selected = (_a = context.propsValue.eventSubTypes) != null ? _a : [], orderProperty = selected.length > 0 && selected.every((s) => s.endsWith("_created")) ? "Created" : "Updated", res = await client.get(
      `/api/v1/billing/addons?limit=5&orderBy=${orderProperty}%20DESC`
    );
    return (_c = (_b = res == null ? void 0 : res.items) != null ? _b : res == null ? void 0 : res.Items) != null ? _c : [];
  }
});

// ../ap/packages/pieces/community/outseta/src/index.ts
var outseta = (0, import_pieces_framework53.createPiece)({
  displayName: "Outseta",
  description: "Triggers and actions for Outseta CRM and Billing",
  auth: outsetaAuth,
  minimumSupportedRelease: "0.20.0",
  logoUrl: "https://cdn.activepieces.com/pieces/outseta.png",
  authors: ["bst1n", "sanket-a11y"],
  categories: [import_pieces_framework54.PieceCategory.SALES_AND_CRM],
  triggers: [
    newAccountEventTrigger,
    newPersonEventTrigger,
    newDealEventTrigger,
    newTaskEventTrigger,
    newPlanEventTrigger,
    newAddOnEventTrigger
  ],
  actions: [
    // Retrieve
    getAccountAction,
    getPersonAction,
    getDealAction,
    getSubscriptionAction,
    getLastPaymentAction,
    // Create / Find or Add
    createAccountAction,
    createDealAction,
    findOrAddPersonAction,
    findOrAddDealAction,
    // Update
    updateAccountAction,
    updatePersonAction,
    updateDealAction,
    // Delete
    deleteAccountAction,
    deletePersonAction,
    deleteDealAction,
    // List
    listAccountsAction,
    listPersonsAction,
    listDealsAction,
    listPlansAction,
    listAddOnsAction,
    listDiscountsAction,
    listCasesAction,
    listTransactionsAction,
    // Billing — Subscription
    changeAccountPlanAction,
    cancelSubscriptionAction,
    removeCancellationAction,
    addDiscountToSubscriptionAction,
    addAddonUsageAction,
    addAddonToSubscriptionAction,
    extendTrialSubscriptionAction,
    updatePaymentInformationAction,
    // Billing — Catalog
    createDiscountAction,
    // Billing — Invoice
    addInvoiceAction,
    addInvoicePaymentAction,
    sendInvoiceEmailAction,
    processPaymentAction,
    // CRM — Membership / Activity
    manageAccountMembershipAction,
    updateAccountMembershipAction,
    addCustomActivityAction,
    // Email
    manageEmailListSubscriptionAction,
    sendConfirmationEmailAction,
    // Support
    addCaseAction,
    addReplyAction,
    // Custom API call
    (0, import_pieces_common2.createCustomApiCallAction)({
      auth: outsetaAuth,
      baseUrl: (auth) => {
        var _a;
        return `${(_a = auth == null ? void 0 : auth.props) == null ? void 0 : _a.domain}/api/v1`;
      },
      authMapping: async (auth) => {
        let { apiKey, apiSecret } = auth.props;
        return {
          Authorization: `Outseta ${apiKey}:${apiSecret}`
        };
      }
    })
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  outseta
});
