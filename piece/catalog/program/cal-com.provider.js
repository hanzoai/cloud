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

// ../ap/packages/pieces/community/cal-com/src/index.ts
var index_exports = {};
__export(index_exports, {
  calcom: () => calcom,
  calcomAuth: () => calcomAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/cal-com/src/lib/triggers/register-webhook.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var registerWebhooks = ({
  name,
  description,
  displayName,
  sampleData,
  aiMetadata
}) => (0, import_pieces_framework.createTrigger)({
  auth: calcomAuth,
  name,
  classification: "READ",
  description,
  displayName,
  aiMetadata,
  props: {},
  sampleData,
  type: import_pieces_framework.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    var _a;
    let request = {
      method: import_pieces_common.HttpMethod.POST,
      url: "https://api.cal.com/v2/webhooks",
      body: {
        triggers: [name],
        subscriberUrl: context.webhookUrl,
        active: !0
      },
      headers: {
        Authorization: `Bearer ${context.auth.secret_text}`,
        "cal-api-version": "2024-06-14"
      }
    }, response = await import_pieces_common.httpClient.sendRequest(
      request
    );
    (response.status === 200 || response.status === 201) && await ((_a = context.store) == null ? void 0 : _a.put(
      `cal_com_trigger_${name}`,
      response.body.data
    ));
  },
  async onDisable(context) {
    var _a, _b;
    let data = await ((_a = context.store) == null ? void 0 : _a.get(
      `cal_com_trigger_${name}`
    ));
    if (data != null) {
      let request = {
        method: import_pieces_common.HttpMethod.DELETE,
        url: `https://api.cal.com/v2/webhooks/${data.id}`,
        headers: {
          Authorization: `Bearer ${context.auth.secret_text}`,
          "cal-api-version": "2024-06-14"
        }
      };
      try {
        await import_pieces_common.httpClient.sendRequest(request);
      } catch (e) {
        if (((_b = e == null ? void 0 : e.response) == null ? void 0 : _b.status) !== 404) throw e;
      }
    }
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/cal-com/src/lib/triggers/index.ts
var triggers = [
  {
    type: "BOOKING_CANCELLED" /* BOOKING_CANCELLED */,
    displayName: "Booking Cancelled",
    sampleData: {
      triggerEvent: "BOOKING_CANCELLED",
      createdAt: "2023-02-18T12:11:37.975Z",
      payload: {
        title: "30 Min Meeting between Mohammad Abu Aboud and John Doe",
        type: "30 Min Meeting",
        description: "Discuss pricing",
        customInputs: {},
        startTime: "2023-02-21T11:00:00+00:00",
        endTime: "2023-02-21T11:30:00+00:00",
        organizer: {
          email: "mo@activepieces.com",
          name: "Mohammad Abu Aboud",
          timeZone: "Europe/Berlin",
          language: {
            locale: "en"
          }
        },
        attendees: [
          {
            name: "John Doe",
            email: "john@example.com",
            timeZone: "Europe/Berlin",
            language: {
              locale: "en"
            }
          }
        ],
        uid: "88wXJjC8AHAbepGYM2bwp4",
        location: "integrations:daily",
        destinationCalendar: null,
        cancellationReason: "Cancellation note",
        eventTitle: "30 Min Meeting",
        eventDescription: null,
        requiresConfirmation: null,
        price: null,
        currency: "usd",
        length: 30,
        status: "CANCELLED"
      }
    }
  },
  {
    type: "BOOKING_CREATED" /* BOOKING_CREATED */,
    displayName: "Booking Created",
    sampleData: {
      triggerEvent: "BOOKING_CREATED",
      createdAt: "2023-02-18T11:54:18.440Z",
      payload: {
        type: "15 Min Meeting",
        title: "15 Min Meeting between Ash Sam and John Doe",
        description: "",
        additionalNotes: "",
        customInputs: {},
        startTime: "2023-02-20T08:00:00Z",
        endTime: "2023-02-20T08:15:00Z",
        organizer: {
          id: 63498,
          name: "Ash Sam",
          email: "ash@sam.com",
          timeZone: "Europe/Berlin",
          language: {
            locale: "en"
          }
        },
        attendees: [
          {
            email: "johndoe@gmail.com",
            name: "John Doe",
            timeZone: "Europe/Berlin",
            language: {
              locale: "en"
            }
          }
        ],
        location: "integrations:daily",
        destinationCalendar: null,
        hideCalendarNotes: !1,
        requiresConfirmation: null,
        eventTypeId: 217779,
        seatsShowAttendees: !1,
        seatsPerTimeSlot: null,
        uid: "pssX2hWwDbuKHmdGQ9BuBz",
        conferenceData: {
          createRequest: {
            requestId: "2db644eb-37a5-581a-99fa-ebe6ce513834"
          }
        },
        videoCallData: {
          type: "daily_video",
          id: "GrhVEmlnsyhAGw3UWKjW",
          password: "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJyIjoir3JoVkVtbG5zeWhBR3czVVdLalciLCJvIjf0cnVlLCJkIjoiYmJkOThhNzEtMTljOS00YmIxLWE1YzUtY2FmMWVjNWJkMTA1IiwiaWF0IjoxNjc2NzIxMjU4fQ.FbmuGSRgCae6yfQKSldHEpUwHfLOZXS_m72rjnFu6gc",
          url: "https://meetco.daily.co/GrhVEmlnsyhAGw3UWKjW"
        },
        appsStatus: [
          {
            appName: "Cal Video",
            type: "daily_video",
            success: 1,
            failures: 0,
            errors: []
          }
        ],
        eventTitle: "15 Min Meeting",
        eventDescription: null,
        price: 0,
        currency: "usd",
        length: 15,
        bookingId: 235143,
        metadata: {
          videoCallUrl: "https://meetco.daily.co/GrhVEmlnsyhAGw3UWKjW"
        },
        status: "ACCEPTED"
      }
    }
  },
  {
    type: "BOOKING_RESCHEDULED" /* BOOKING_RESCHEDULED */,
    displayName: "Booking Rescheduled",
    sampleData: {
      triggerEvent: "BOOKING_RESCHEDULED",
      createdAt: "2023-02-18T12:11:26.909Z",
      payload: {
        type: "30 Min Meeting",
        title: "30 Min Meeting between Mohammad Abu Aboud and John Doe",
        description: "Discuss pricing",
        additionalNotes: "Discuss pricing",
        customInputs: {},
        startTime: "2023-02-21T11:00:00Z",
        endTime: "2023-02-21T11:30:00Z",
        organizer: {
          id: 63498,
          name: "Mohammad Abu Aboud",
          email: "mo@example.com",
          timeZone: "Europe/Berlin",
          language: {
            locale: "en"
          }
        },
        attendees: [
          {
            email: "john@example.com",
            name: "John Doe",
            timeZone: "Europe/Berlin",
            language: {
              locale: "en"
            }
          }
        ],
        location: "https://meetco.daily.co/9tJRDCRw9ESmb64SuEwU",
        destinationCalendar: null,
        hideCalendarNotes: !1,
        requiresConfirmation: null,
        eventTypeId: 217780,
        seatsShowAttendees: !1,
        seatsPerTimeSlot: null,
        uid: "88wXJjC8AHAbepGYM2bwp4",
        conferenceData: {
          createRequest: {
            requestId: "2db644eb-37a5-581a-99fa-ebe6ce513834"
          }
        },
        videoCallData: {
          type: "daily_video",
          id: "9tJRDCRw9ESmb64SuEwU",
          password: "PASSWORD",
          url: "https://meetco.daily.co/RANDOM"
        },
        eventTitle: "30 Min Meeting",
        eventDescription: null,
        price: 0,
        currency: "usd",
        length: 30,
        bookingId: 235153,
        rescheduleUid: "3qTJ7WiwdMR3RJmDW2KGhr",
        rescheduleStartTime: "2023-02-20T08:30:00Z",
        rescheduleEndTime: "2023-02-20T09:00:00Z",
        metadata: {},
        status: "ACCEPTED"
      }
    }
  }
].map(
  (eventTrigger) => registerWebhooks({
    name: eventTrigger.type,
    displayName: eventTrigger.displayName,
    sampleData: eventTrigger.sampleData,
    description: `Create a webhook to monitor when ${eventTrigger.displayName}`
  })
);

// ../ap/packages/pieces/community/cal-com/src/index.ts
var calcomAuth = import_pieces_framework2.PieceAuth.SecretText({
  displayName: "API Key",
  description: "API Key provided by cal.com",
  required: !0
}), calcom = (0, import_pieces_framework2.createPiece)({
  displayName: "Cal.com",
  description: "Open-source alternative to Calendly",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/cal.com.png",
  categories: [import_pieces_framework3.PieceCategory.PRODUCTIVITY],
  authors: ["kishanprmr", "AbdulTheActivePiecer", "khaledmashaly", "abuaboud"],
  auth: calcomAuth,
  actions: [],
  triggers
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  calcom,
  calcomAuth
});
