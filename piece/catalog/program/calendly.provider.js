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

// ../ap/packages/pieces/community/calendly/src/index.ts
var index_exports = {};
__export(index_exports, {
  calendly: () => calendly
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/calendly/src/lib/common/index.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), calendlyCommon = {
  baseUrl: "https://api.calendly.com",
  scope: import_pieces_framework.Property.StaticDropdown({
    displayName: "Scope",
    required: !0,
    options: {
      options: [
        { value: "user", label: "User" },
        { value: "organization", label: "Organization" }
      ],
      disabled: !1
    }
  }),
  getUser: async (personalToken) => {
    let request = {
      method: import_pieces_common.HttpMethod.GET,
      url: `${calendlyCommon.baseUrl}/users/me`,
      headers: {
        Authorization: calendlyCommon.authorizationHeader(personalToken)
      }
    };
    return (await import_pieces_common.httpClient.sendRequest(
      request
    )).body.resource;
  },
  authorizationHeader: (personalToken) => `Bearer ${personalToken}`,
  UuidFromUri: (uri) => uri.split("/").pop()
};

// ../ap/packages/pieces/community/calendly/src/lib/trigger/invitee-canceled.trigger.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/calendly/src/lib/auth.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var markdown = `
## Obtain your Calendly Personal Token
1. Go to https://calendly.com/integrations/api_webhooks
2. Click on "Create New Token"
3. Copy the token and paste it in the field below
`, calendlyAuth = import_pieces_framework2.PieceAuth.SecretText({
  displayName: "Personal Token",
  required: !0,
  description: markdown,
  validate: async ({ auth }) => {
    try {
      return calendlyCommon.getUser(auth), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Connection failed. Please check your token and try again."
      };
    }
  }
});

// ../ap/packages/pieces/community/calendly/src/lib/trigger/invitee-canceled.trigger.ts
var triggerNameInStore = "calendly_invitee_canceled_trigger", calendlyInviteeCanceled = (0, import_pieces_framework3.createTrigger)({
  auth: calendlyAuth,
  name: "invitee_canceled",
  classification: "READ",
  displayName: "Event Canceled",
  description: "Triggers when a new Calendly event is canceled",
  aiMetadata: {
    description: 'Fires when an invitee cancels a scheduled Calendly event. Emits the canceled event including the invitee details, the cancellation reason and who canceled it (host or invitee), and the event status set to "canceled".'
  },
  props: {
    scope: calendlyCommon.scope
  },
  sampleData: {
    created_at: "2023-01-29T13:57:17.000000Z",
    created_by: "https://api.calendly.com/users/AAAAAAA",
    event: "invitee.canceled",
    payload: {
      cancel_url: "https://calendly.com/cancellations/AAAAAAAA",
      cancellation: {
        canceler_type: "host",
        canceled_by: "Ashraf Samhouri",
        reason: "testing"
      },
      created_at: "2023-01-29T13:56:46.894198Z",
      email: "test@test.com",
      event: "https://api.calendly.com/scheduled_events/AAAAAAAAA",
      first_name: null,
      last_name: null,
      name: "abdul",
      new_invitee: null,
      no_show: null,
      old_invitee: null,
      payment: null,
      questions_and_answers: [],
      reconfirmation: null,
      reschedule_url: "https://calendly.com/reschedulings/AAAAAAAA",
      rescheduled: !1,
      routing_form_submission: null,
      status: "canceled",
      text_reminder_number: null,
      timezone: "Asia/Baghdad",
      tracking: {
        utm_campaign: null,
        utm_source: null,
        utm_medium: null,
        utm_content: null,
        utm_term: null,
        salesforce_uuid: null
      },
      updated_at: "2023-01-29T13:57:17.466943Z",
      uri: "https://api.calendly.com/scheduled_events/AAAAAAAAAAAaA/invitees/AAAAAAAA"
    }
  },
  type: import_pieces_framework3.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    var _a;
    let calendlyUser = await calendlyCommon.getUser(context.auth.secret_text), request = {
      method: import_pieces_common2.HttpMethod.POST,
      url: `${calendlyCommon.baseUrl}/webhook_subscriptions`,
      body: {
        url: context.webhookUrl,
        organization: calendlyUser.current_organization,
        user: calendlyUser.uri,
        scope: context.propsValue.scope,
        events: ["invitee.canceled"]
      },
      authentication: {
        token: context.auth.secret_text,
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN
      },
      queryParams: {}
    }, { body } = await import_pieces_common2.httpClient.sendRequest(request);
    await ((_a = context.store) == null ? void 0 : _a.put(triggerNameInStore, {
      webhookId: calendlyCommon.UuidFromUri(body.resource.uri)
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(
      triggerNameInStore
    ));
    if (response != null) {
      let request = {
        method: import_pieces_common2.HttpMethod.DELETE,
        url: `${calendlyCommon.baseUrl}/webhook_subscriptions/${response.webhookId}`,
        authentication: {
          token: context.auth.secret_text,
          type: import_pieces_common2.AuthenticationType.BEARER_TOKEN
        }
      };
      await import_pieces_common2.httpClient.sendRequest(request);
    }
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/calendly/src/lib/trigger/invitee-created.trigger.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var triggerNameInStore2 = "calendly_invitee_created_trigger", calendlyInviteeCreated = (0, import_pieces_framework4.createTrigger)({
  auth: calendlyAuth,
  name: "invitee_created",
  classification: "READ",
  displayName: "Event Scheduled",
  description: "Triggers when a new Calendly event is scheduled",
  aiMetadata: {
    description: 'Fires when an invitee schedules a new Calendly event (a booking is created). Emits the new event including the invitee details (name, email), the event URI, scheduling timestamps, and the event status set to "active".'
  },
  props: {
    scope: calendlyCommon.scope
  },
  sampleData: {
    created_at: "2023-01-29T13:50:13.000000Z",
    created_by: "https://api.calendly.com/users/AAAAAAAAAAAA",
    payload: {
      cancel_url: "https://calendly.com/cancellations/AAAAAAAAAAA",
      created_at: "2023-01-29T13:50:13.072950Z",
      email: "abdulyki@activepieces.com",
      event: "https://api.calendly.com/scheduled_events/AAAAAAAAAAAA",
      first_name: null,
      last_name: null,
      name: "abdul",
      new_invitee: null,
      no_show: null,
      old_invitee: null,
      payment: null,
      questions_and_answers: [],
      reconfirmation: null,
      reschedule_url: "https://calendly.com/reschedulings/AAAAAAAAAAAA",
      rescheduled: !1,
      routing_form_submission: null,
      status: "active",
      text_reminder_number: null,
      timezone: "Asia/Baghdad",
      updated_at: "2023-01-29T13:50:13.072950Z",
      uri: "https://api.calendly.com/scheduled_events/AAAAAAAAAAAaA/invitees/AAAAAAAAAAAA"
    }
  },
  type: import_pieces_framework4.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    var _a;
    let calendlyUser = await calendlyCommon.getUser(context.auth.secret_text), request = {
      method: import_pieces_common3.HttpMethod.POST,
      url: `${calendlyCommon.baseUrl}/webhook_subscriptions`,
      body: {
        url: context.webhookUrl,
        organization: calendlyUser.current_organization,
        user: calendlyUser.uri,
        scope: context.propsValue.scope,
        events: ["invitee.created"]
      },
      authentication: {
        token: context.auth.secret_text,
        type: import_pieces_common3.AuthenticationType.BEARER_TOKEN
      },
      queryParams: {}
    }, { body } = await import_pieces_common3.httpClient.sendRequest(request);
    await ((_a = context.store) == null ? void 0 : _a.put(triggerNameInStore2, {
      webhookId: calendlyCommon.UuidFromUri(body.resource.uri)
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(
      triggerNameInStore2
    ));
    if (console.log(response || "nothing"), response != null) {
      let request = {
        method: import_pieces_common3.HttpMethod.DELETE,
        url: `${calendlyCommon.baseUrl}/webhook_subscriptions/${response.webhookId}`,
        authentication: {
          token: context.auth.secret_text,
          type: import_pieces_common3.AuthenticationType.BEARER_TOKEN
        }
      };
      await import_pieces_common3.httpClient.sendRequest(request);
    } else
      throw Error("context store was null");
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/calendly/src/index.ts
var calendly = (0, import_pieces_framework5.createPiece)({
  displayName: "Calendly",
  description: "Simple, modern scheduling",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/calendly.png",
  categories: [import_pieces_framework6.PieceCategory.PRODUCTIVITY],
  authors: ["kishanprmr", "MoShizzle", "AbdulTheActivePiecer", "khaledmashaly", "abuaboud"],
  auth: calendlyAuth,
  actions: [
    (0, import_pieces_common4.createCustomApiCallAction)({
      baseUrl: () => calendlyCommon.baseUrl,
      // Replace with the actual base URL
      auth: calendlyAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [calendlyInviteeCreated, calendlyInviteeCanceled]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  calendly
});
