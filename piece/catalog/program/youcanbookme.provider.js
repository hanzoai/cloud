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

// ../ap/packages/pieces/community/youcanbookme/src/index.ts
var index_exports = {};
__export(index_exports, {
  youcanbookme: () => youcanbookme
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/youcanbookme/src/lib/triggers/new-booking.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/youcanbookme/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), youcanbookmeAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "YouCanBookMe API Key",
  description: `
 Go to [app.youcanbookme.com](https://app.youcanbook.me/#/account/security/)
`,
  required: !0
});

// ../ap/packages/pieces/community/youcanbookme/src/lib/triggers/new-booking.ts
var newBooking = (0, import_pieces_framework2.createTrigger)({
  auth: youcanbookmeAuth,
  name: "newBooking",
  classification: "READ",
  displayName: "New Booking",
  description: "Trigger when a new booking is made",
  aiMetadata: {
    description: "Fires when a YouCanBookMe webhook notification is received for a booking event. Configured against a booking page notification (e.g. after a new booking is made), it represents an incoming booking with details such as start/end times, time zone, and the booker's name and email."
  },
  props: {
    webhook_setup: import_pieces_framework2.Property.MarkDown({
      value: `
## Setting up a Webhook

1. Select the booking page you want to add a webhook to and click **Edit Settings**
2. Select **Additional options** from the left menu
3. Click **Notifications** under the sub-menu
4. Click into the notification you wish to have the webhook fire (After new booking made, rescheduled, cancelled, reminder, after appointment ends, etc)
5. Click the **+** to add a new notification
6. Click **Webhook**
7. Enter the URL provided by Activepieces (copy from the input field below)
	\`\`\`text
			{{webhookUrl}}
			\`\`\`
8. Choose method:
   - **POST** 
9. Add payload (optional)
10. Click **Save changes**
            `
    })
  },
  sampleData: {
    startsAt: "2025-12-17",
    endsAt: "08:30",
    timeZone: "UTC",
    firstName: "sa",
    email: "test@test.com"
  },
  type: import_pieces_framework2.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/youcanbookme/src/index.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/youcanbookme/src/lib/actions/create-profile.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common = require("@activepieces/pieces-common"), createprofile = (0, import_pieces_framework3.createAction)({
  auth: youcanbookmeAuth,
  name: "create-profile",
  classification: "WRITE",
  displayName: "Create Profile",
  description: "Create a new profile in YouCanBookMe",
  audience: "both",
  aiMetadata: {
    description: "Creates a new YouCanBookMe booking profile under a given account, setting properties such as title, subdomain, time zone, locale, and logo. Use when an agent needs to provision a fresh scheduling page. Requires the account ID and a profile title; not idempotent \u2014 each call creates a new profile.",
    idempotent: !1
  },
  props: {
    accountId: import_pieces_framework3.Property.ShortText({
      displayName: "Account ID",
      required: !0
    }),
    title: import_pieces_framework3.Property.ShortText({
      displayName: "Profile Title",
      description: "The title of the profile",
      required: !0
    }),
    description: import_pieces_framework3.Property.LongText({
      displayName: "Description",
      description: "A description of the profile",
      required: !1
    }),
    subdomain: import_pieces_framework3.Property.ShortText({
      displayName: "Subdomain",
      description: "The subdomain for the profile",
      required: !1
    }),
    timeZone: import_pieces_framework3.Property.ShortText({
      displayName: "Time Zone",
      description: "The time zone for the profile (e.g., America/New_York)",
      required: !1
    }),
    locale: import_pieces_framework3.Property.ShortText({
      displayName: "Locale",
      description: "The locale for the profile (e.g., en-US)",
      required: !1
    }),
    logo: import_pieces_framework3.Property.ShortText({
      displayName: "Logo URL",
      description: "URL to the profile logo",
      required: !1
    }),
    accessCode: import_pieces_framework3.Property.ShortText({
      displayName: "Access Code",
      description: "An access code for the profile",
      required: !1
    })
  },
  async run({ propsValue, auth }) {
    let {
      accountId,
      title,
      description,
      subdomain,
      timeZone,
      locale,
      logo,
      accessCode
    } = propsValue, body = {
      accountId,
      title
    };
    return description && (body.description = description), subdomain && (body.subdomain = subdomain), timeZone && (body.timeZone = timeZone), locale && (body.locale = locale), logo && (body.logo = logo), accessCode && (body.accessCode = accessCode), (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://api.youcanbook.me/v1/profiles",
      headers: {
        Authorization: `Bearer ${auth}`,
        "Content-Type": "application/json"
      },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/youcanbookme/src/lib/actions/retrieve-booking-by-id.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common"), retrieveBookingById = (0, import_pieces_framework4.createAction)({
  auth: youcanbookmeAuth,
  name: "retrieveBookingById",
  classification: "READ",
  displayName: "Retrieve Booking by ID",
  description: "Retrieve a booking by its ID from YouCanBookMe",
  audience: "both",
  aiMetadata: {
    description: "Looks up a single YouCanBookMe booking by its booking ID, optionally restricting the returned fields and the time zone times are displayed in. Use when an agent already has a booking ID and needs its details. Read-only and idempotent \u2014 repeating the call returns the same booking without side effects.",
    idempotent: !0
  },
  props: {
    bookingId: import_pieces_framework4.Property.ShortText({
      displayName: "Booking ID",
      description: "The ID of the booking to retrieve",
      required: !0
    }),
    displayTimeZone: import_pieces_framework4.Property.ShortText({
      displayName: "Display Time Zone",
      description: "The time zone to display times in (e.g., America/New_York)",
      required: !1
    }),
    fields: import_pieces_framework4.Property.ShortText({
      displayName: "Fields",
      description: "Comma-separated list of fields to return. Default: id,title,accountId,profileId,createdAt,startsAt,endsAt,location,tentative,timeZone,cancelled,numberOfSlots",
      required: !1
    })
  },
  async run({ propsValue, auth }) {
    let { bookingId, displayTimeZone, fields } = propsValue, queryParams = {};
    return displayTimeZone && (queryParams.displayTimeZone = displayTimeZone), fields && (queryParams.fields = fields), (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.GET,
      url: `https://api.youcanbook.me/v1/bookings/${bookingId}`,
      headers: {
        Authorization: `Bearer ${auth}`,
        "Content-Type": "application/json"
      },
      queryParams
    })).body;
  }
});

// ../ap/packages/pieces/community/youcanbookme/src/index.ts
var import_pieces_common3 = require("@activepieces/pieces-common");
var youcanbookme = (0, import_pieces_framework5.createPiece)({
  displayName: "YouCanBookMe",
  auth: youcanbookmeAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/youcanbookme.png",
  categories: [import_pieces_framework6.PieceCategory.SALES_AND_CRM],
  description: "YouCanBookMe is an online scheduling tool that helps you manage appointments and bookings efficiently.",
  authors: ["sanket-a11y"],
  actions: [
    createprofile,
    retrieveBookingById,
    (0, import_pieces_common3.createCustomApiCallAction)({
      auth: youcanbookmeAuth,
      baseUrl: () => "https://api.youcanbook.me/v1",
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [newBooking]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  youcanbookme
});
