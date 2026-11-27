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

// ../ap/packages/pieces/community/imeetify/src/index.ts
var index_exports = {};
__export(index_exports, {
  imeetify: () => imeetify
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/imeetify/src/lib/triggers/appointment-event.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_framework2 = require("@activepieces/pieces-framework"), setupInstructions = `
**Setup Instructions**

1. In iMeetify, open **Integrations > More > Webhook** and click **Config**.
2. Fill in the form:
   - **Webhook Name:** any label (e.g. \`Activepieces\`)
   - **URL:** \`{{webhookUrl}}\`
   - **Authentication Key:** click **Generate Key** to create one (kept inside iMeetify)
   - **Events:** select **Appointment Confirmation** and/or **Appointment Cancellation**
3. Click **Save**. iMeetify requires an HTTPS URL \u2014 the URL above is already secure.

This trigger will only fire when the incoming event matches the **Event Type** selected below.
`, appointmentEvent = (0, import_pieces_framework.createTrigger)({
  name: "appointment_event",
  displayName: "Appointment Event",
  description: "Fires when an appointment is confirmed or cancelled in iMeetify.",
  aiMetadata: {
    description: "Fires when an iMeetify appointment matching the selected event type occurs: an appointment confirmation (newly scheduled/booked) or an appointment cancellation. Delivered via webhook, each event represents a single appointment lifecycle change and includes the appointment details (id, start/end times with timezone, type) and the invitee details (name, email, organization). Events whose type does not match the selected Event Type are ignored."
  },
  type: import_pieces_framework.TriggerStrategy.WEBHOOK,
  props: {
    instructions: import_pieces_framework.Property.MarkDown({
      value: setupInstructions,
      variant: import_pieces_framework2.MarkdownVariant.BORDERLESS
    }),
    eventType: import_pieces_framework.Property.StaticDropdown({
      displayName: "Event Type",
      description: "Choose which event to listen for. Other event types received on this URL will be ignored.",
      required: !0,
      defaultValue: "appointment_schedule",
      options: {
        disabled: !1,
        options: [
          { label: "Appointment Confirmation", value: "appointment_schedule" },
          { label: "Appointment Cancellation", value: "appointment_cancel" }
        ]
      }
    })
  },
  sampleData: {
    id: "et_6a0ed723239a9",
    occurred_at: "2026-05-21T09:57:55.145850Z",
    object: "event",
    appointment: {
      id: 37777,
      access_code: "BSLWT8sjafZKAOR54SsCdxs5Vkv5H6Ber0HaqKMR",
      StartTime: {
        date: "2026-05-22 10:30:00.000000",
        timezone_type: 3,
        timezone: "Asia/Kolkata"
      },
      EndTime: {
        date: "2026-05-22 11:00:00.000000",
        timezone_type: 3,
        timezone: "Asia/Kolkata"
      },
      Type: { type: "In-Person" }
    },
    invitee: {
      id: 36338,
      StartTime: {
        date: "2026-05-22 10:30:00.000000",
        timezone_type: 3,
        timezone: "Asia/Kolkata"
      },
      EndTime: {
        date: "2026-05-22 11:00:00.000000",
        timezone_type: 3,
        timezone: "Asia/Kolkata"
      },
      Email: "sanket@gmail.com",
      FirstName: "Sanket",
      LastName: "Nannaware",
      MobileNumber: null,
      OrganizationName: null,
      TermsConditions: 0,
      PromotionalMessages: 0,
      Guests: null,
      Remarks: null
    },
    qustions: [],
    event_type: "appointment_schedule"
  },
  async onEnable() {
  },
  async onDisable() {
  },
  async run(context) {
    let payload = context.payload.body;
    return payload.data.event_type !== context.propsValue.eventType ? [] : [payload.data];
  }
});

// ../ap/packages/pieces/community/imeetify/src/index.ts
var imeetify = (0, import_pieces_framework3.createPiece)({
  displayName: "iMeetify",
  description: "Online appointment scheduling: receive appointment confirmation and cancellation events via webhook.",
  auth: import_pieces_framework3.PieceAuth.None(),
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/imeetify.png",
  categories: [import_pieces_framework4.PieceCategory.PRODUCTIVITY],
  authors: ["sanket-a11y"],
  actions: [],
  triggers: [appointmentEvent]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  imeetify
});
