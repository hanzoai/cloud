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

// ../ap/packages/pieces/community/sessions-us/src/index.ts
var index_exports = {};
__export(index_exports, {
  sessionAuth: () => sessionAuth,
  sessionsUs: () => sessionsUs
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/sessions-us/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");
var baseUrl = "https://api.app.sessions.us/api", properties = {
  permission: import_pieces_framework.Property.StaticDropdown({
    displayName: "Permission",
    description: "Personal applies for the user only, organization applies to every event that is made by a user of the organization.",
    required: !0,
    defaultValue: "personal",
    options: {
      options: [
        {
          label: "Personal",
          value: "personal"
        },
        {
          label: "Organization",
          value: "organization"
        }
      ]
    }
  })
};
async function getTimezones() {
  return (await import_pieces_common.httpClient.sendRequest({
    url: "http://worldtimeapi.org/api/timezone",
    method: import_pieces_common.HttpMethod.GET
  })).body;
}
async function getEvents(auth) {
  return (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: `${baseUrl}/events`,
    headers: {
      "x-api-key": auth
    }
  })).body;
}
function slugify(string) {
  return string.trim().toLowerCase().replace(/[^\w\s-]/g, "").replace(/\s+/g, "-").replace(/-+/g, "-");
}
async function createWebhook(trigger, auth, webhookUrl, permission) {
  return (await import_pieces_common.httpClient.sendRequest({
    url: `${baseUrl}/webhooks`,
    method: import_pieces_common.HttpMethod.POST,
    headers: {
      "x-api-key": auth
    },
    body: {
      url: webhookUrl,
      trigger,
      permission,
      // If the API key used to create this webhook is deleted, deletes the webhook
      linkPublicKey: !0,
      // This needs to be ACTIVE_PIECES as set up by the Sessions.us team, makes the webhook not editable from the frontend
      integration: "ACTIVE_PIECES"
    }
  })).body;
}
async function deleteWebhook(webhookId, auth) {
  return (await import_pieces_common.httpClient.sendRequest({
    url: `${baseUrl}/webhooks/${webhookId}`,
    method: import_pieces_common.HttpMethod.DELETE,
    headers: {
      "x-api-key": auth
    }
  })).body;
}
function createSessionsUsWebhookTrigger(data) {
  var _a;
  return (0, import_pieces_framework.createTrigger)({
    auth: sessionAuth,
    name: data.name,
    displayName: data.displayName,
    description: data.description,
    aiMetadata: data.aiMetadata,
    type: import_pieces_framework.TriggerStrategy.WEBHOOK,
    sampleData: (_a = data.sampleData) != null ? _a : {},
    props: {
      permission: properties.permission
    },
    async onEnable({ auth, store, webhookUrl, propsValue }) {
      let webhookId = await createWebhook(
        data.trigger,
        auth.secret_text,
        webhookUrl,
        propsValue.permission
      );
      await store.put(data.storeKey, {
        webhookId: webhookId.id
      });
    },
    async onDisable({ auth, store }) {
      let webhookId = await store.get(data.storeKey);
      webhookId && await deleteWebhook(webhookId.webhookId, auth.secret_text);
    },
    async run({ payload }) {
      return [payload.body.data];
    }
  });
}

// ../ap/packages/pieces/community/sessions-us/src/lib/actions/create-event.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework"), createEvent = (0, import_pieces_framework2.createAction)({
  auth: sessionAuth,
  name: "create_event",
  displayName: "Create Event",
  description: "Quickly create an event.",
  audience: "both",
  aiMetadata: { description: "Create a new event on Sessions.us with a name, start/end time, and timezone. Use when an agent needs to schedule an event (a public, registration-based gathering, as opposed to a private session). Creates a new event each call, so it is not idempotent \u2014 repeating it produces duplicate events.", idempotent: !1 },
  props: {
    name: import_pieces_framework2.Property.ShortText({
      displayName: "Event Name",
      description: "The name of the event",
      required: !0
    }),
    startAt: import_pieces_framework2.Property.DateTime({
      displayName: "Starts At",
      description: "Select a date and time",
      required: !0,
      defaultValue: (/* @__PURE__ */ new Date()).toISOString()
    }),
    plannedEnd: import_pieces_framework2.Property.DateTime({
      displayName: "Ends At",
      description: "Select a date and time",
      required: !0,
      defaultValue: (/* @__PURE__ */ new Date()).toISOString()
    }),
    timezone: import_pieces_framework2.Property.Dropdown({
      auth: sessionAuth,
      displayName: "Timezone",
      description: "The timezone which the session will take place.",
      required: !0,
      refreshers: [],
      options: async () => ({
        options: (await getTimezones()).map((timezone) => ({
          label: timezone,
          value: timezone
        }))
      })
    })
  },
  async run({ propsValue, auth }) {
    let request = {
      method: import_pieces_common2.HttpMethod.POST,
      url: `${baseUrl}/events`,
      headers: {
        "x-api-key": auth.secret_text
      },
      body: {
        name: propsValue.name,
        slug: slugify(propsValue.name),
        startAt: propsValue.startAt,
        plannedEnd: propsValue.plannedEnd,
        timeZone: propsValue.timezone
      }
    };
    return (await import_pieces_common2.httpClient.sendRequest(request)).body;
  }
});

// ../ap/packages/pieces/community/sessions-us/src/lib/actions/create-session.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework"), createSession = (0, import_pieces_framework3.createAction)({
  auth: sessionAuth,
  name: "create_session",
  displayName: "Create Session",
  description: "Quickly create a session.",
  audience: "both",
  aiMetadata: { description: "Create a new session (a private video meeting) on Sessions.us with a name, start/end time, and timezone, returning the session id and join link. Use when an agent needs to schedule a meeting rather than a public registration-based event. Creates a new session each call, so it is not idempotent \u2014 repeating it produces duplicate sessions.", idempotent: !1 },
  props: {
    name: import_pieces_framework3.Property.ShortText({
      displayName: "Session Name",
      description: "The name of the session",
      required: !0
    }),
    startAt: import_pieces_framework3.Property.DateTime({
      displayName: "Starts At",
      description: "Select a date and time",
      required: !0,
      defaultValue: (/* @__PURE__ */ new Date()).toISOString()
    }),
    plannedEnd: import_pieces_framework3.Property.DateTime({
      displayName: "Ends At",
      description: "Select a date and time",
      required: !0,
      defaultValue: (/* @__PURE__ */ new Date()).toISOString()
    }),
    timezone: import_pieces_framework3.Property.Dropdown({
      auth: sessionAuth,
      displayName: "Timezone",
      description: "The timezone which the session will take place.",
      required: !0,
      refreshers: [],
      options: async () => ({
        options: (await getTimezones()).map((timezone) => ({
          label: timezone,
          value: timezone
        }))
      })
    })
  },
  async run({ propsValue, auth }) {
    let request = {
      method: import_pieces_common3.HttpMethod.POST,
      url: `${baseUrl}/sessions`,
      headers: {
        "x-api-key": auth.secret_text
      },
      body: {
        name: propsValue.name,
        startAt: propsValue.startAt,
        plannedEnd: propsValue.plannedEnd,
        timeZone: propsValue.timezone
      }
    }, response = await import_pieces_common3.httpClient.sendRequest(request);
    return {
      id: response.body.id,
      name: response.body.name,
      link: response.body.sessionLink
    };
  }
});

// ../ap/packages/pieces/community/sessions-us/src/lib/actions/publish-event.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework"), publishEvent = (0, import_pieces_framework4.createAction)({
  auth: sessionAuth,
  name: "publish_event",
  displayName: "Publish Event",
  description: "Quickly publish an event.",
  audience: "both",
  aiMetadata: { description: "Publish an existing draft event on Sessions.us so it becomes live and open for registration, identified by its event id. Use after Create Event when the event should go public. Requires a valid event id; publishing an already-published event has no further effect, so the call is effectively idempotent.", idempotent: !0 },
  props: {
    event: import_pieces_framework4.Property.Dropdown({
      auth: sessionAuth,
      displayName: "Event",
      description: "The event you want to publish.",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        options: (await getEvents(auth.secret_text)).map((event) => ({
          label: event.session.name,
          value: event.id
        }))
      } : {
        disabled: !0,
        options: []
      }
    })
  },
  async run({ propsValue, auth }) {
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: `${baseUrl}/events/${propsValue.event}/publish`,
      headers: {
        "x-api-key": auth.secret_text
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/sessions-us/src/lib/triggers/booking-created.ts
var bookingCreated = createSessionsUsWebhookTrigger({
  name: "booking_created",
  displayName: "Booking Created",
  description: "Triggered when a new booking is created.",
  aiMetadata: {
    description: "Fires when someone books a meeting through a Sessions.us booking page, creating a new scheduled session tied to that booking. The payload includes the booking details (participant name and email, guests) and the resulting session and its participants."
  },
  trigger: "BOOKING_CREATED" /* BOOKING_CREATED */,
  storeKey: "sessions_booking_created_trigger",
  sampleData: {
    session: {
      id: "2f8547a5-5c36-49ea-bc21-c61e337d89a3",
      name: "Talk with Active Pieces",
      description: "",
      quickSession: !1,
      room: null,
      createdAt: "2023-11-30T10:21:41.372Z",
      startAt: "2023-11-30T10:30:00.000Z",
      actualStart: "2023-11-30T10:34:37.799Z",
      endedAt: null,
      booking: {
        id: "3df6dbdb-9a6c-41e6-89b2-d7a38233163e",
        name: "Talk with Active Pieces",
        participantName: "Active Pieces",
        participantEmail: "example@gmail.com",
        guests: []
      },
      event: null,
      participants: [
        {
          id: "2021014e-5e28-498f-9fe3-3428ce40c9c4",
          isOwner: !1,
          guest: {
            id: "529e6bc0-af38-4365-a0b2-5530d8207ecb",
            email: "example@gmail.com",
            lastName: null,
            firstName: "Active Pieces"
          },
          user: null
        },
        {
          id: "529b967f-de3d-4dec-a865-35637c294f9c",
          isOwner: !0,
          guest: null,
          user: {
            id: "9090bd7c-0cf9-4716-837e-43f3821a65c4",
            email: "email@example.com",
            lastName: "Pieces",
            firstName: "Active"
          }
        }
      ]
    }
  }
});

// ../ap/packages/pieces/community/sessions-us/src/lib/triggers/booking-ended.ts
var bookingEnded = createSessionsUsWebhookTrigger({
  name: "booking_ended",
  displayName: "Booking Ended",
  description: "Triggered when a booking ends.",
  aiMetadata: {
    description: "Fires when a booked session finishes (the meeting ends). The payload includes the booking details, the session, and its participants."
  },
  trigger: "BOOKING_ENDED" /* BOOKING_ENDED */,
  storeKey: "sessions_booking_ended_trigger",
  sampleData: {
    session: {
      id: "2f8547a5-5c36-49ea-bc21-c61e337d89a3",
      name: "Talk with Active Pieces",
      description: "",
      quickSession: !1,
      room: null,
      createdAt: "2023-11-30T10:21:41.372Z",
      startAt: "2023-11-30T10:30:00.000Z",
      actualStart: "2023-11-30T10:34:37.799Z",
      endedAt: null,
      booking: {
        id: "3df6dbdb-9a6c-41e6-89b2-d7a38233163e",
        name: "Talk with Active Pieces",
        participantName: "Active Pieces",
        participantEmail: "example@gmail.com",
        guests: []
      },
      event: null,
      participants: [
        {
          id: "2021014e-5e28-498f-9fe3-3428ce40c9c4",
          isOwner: !1,
          guest: {
            id: "529e6bc0-af38-4365-a0b2-5530d8207ecb",
            email: "example@gmail.com",
            lastName: null,
            firstName: "Active Pieces"
          },
          user: null
        },
        {
          id: "529b967f-de3d-4dec-a865-35637c294f9c",
          isOwner: !0,
          guest: null,
          user: {
            id: "9090bd7c-0cf9-4716-837e-43f3821a65c4",
            email: "email@example.com",
            lastName: "Pieces",
            firstName: "Active"
          }
        }
      ]
    }
  }
});

// ../ap/packages/pieces/community/sessions-us/src/lib/triggers/booking-started.ts
var bookingStarted = createSessionsUsWebhookTrigger({
  name: "booking_started",
  displayName: "Booking Started",
  description: "Triggered when a booking starts.",
  aiMetadata: {
    description: "Fires when a booked session goes live (the meeting actually begins). The payload includes the booking details, the session, and its participants."
  },
  trigger: "BOOKING_STARTED" /* BOOKING_STARTED */,
  storeKey: "sessions_booking_started_trigger",
  sampleData: {
    session: {
      id: "2f8547a5-5c36-49ea-bc21-c61e337d89a3",
      name: "Talk with Active Pieces",
      description: "",
      quickSession: !1,
      room: null,
      createdAt: "2023-11-30T10:21:41.372Z",
      startAt: "2023-11-30T10:30:00.000Z",
      actualStart: "2023-11-30T10:34:37.799Z",
      endedAt: null,
      booking: {
        id: "3df6dbdb-9a6c-41e6-89b2-d7a38233163e",
        name: "Talk with Active Pieces",
        participantName: "Active Pieces",
        participantEmail: "example@gmail.com",
        guests: []
      },
      event: null,
      participants: [
        {
          id: "2021014e-5e28-498f-9fe3-3428ce40c9c4",
          isOwner: !1,
          guest: {
            id: "529e6bc0-af38-4365-a0b2-5530d8207ecb",
            email: "example@gmail.com",
            lastName: null,
            firstName: "Active Pieces"
          },
          user: null
        },
        {
          id: "529b967f-de3d-4dec-a865-35637c294f9c",
          isOwner: !0,
          guest: null,
          user: {
            id: "9090bd7c-0cf9-4716-837e-43f3821a65c4",
            email: "email@example.com",
            lastName: "Pieces",
            firstName: "Active"
          }
        }
      ]
    }
  }
});

// ../ap/packages/pieces/community/sessions-us/src/lib/triggers/event-created.ts
var eventCreated = createSessionsUsWebhookTrigger({
  name: "event_created",
  displayName: "Event Created",
  description: "Triggered when a new event is created.",
  aiMetadata: {
    description: "Fires when a new event (a public, registration-based gathering) is created on Sessions.us, while still in draft before it is published. The payload includes the underlying session, the event id and slug, and the owning participant."
  },
  trigger: "EVENT_CREATED" /* EVENT_CREATED */,
  storeKey: "sessions_event_created_trigger",
  sampleData: {
    session: {
      id: "8208f783-fba9-4045-ae6e-dea64f5ab7ea",
      name: "My Best Event",
      description: null,
      quickSession: !1,
      room: null,
      createdAt: "2023-11-30T10:46:13.675Z",
      startAt: "2023-11-30T11:00:00.000Z",
      actualStart: null,
      endedAt: null,
      booking: null,
      event: {
        id: "52259a5f-f706-41d9-8b58-1b8796bd0ffc",
        slug: "f34bd8a5-a7d9-47f2-aee9-031d10a76b75-83897/my-best-event"
      },
      participants: [
        {
          id: "d98f984f-982b-4a17-8a3d-093a4a774b49",
          isOwner: !0,
          guest: null,
          user: {
            id: "9090bd7c-0cf9-4716-837e-43f3821a65c4",
            email: "email@example.com",
            lastName: "Pieces",
            firstName: "Active"
          }
        }
      ]
    }
  }
});

// ../ap/packages/pieces/community/sessions-us/src/lib/triggers/event-ended.ts
var eventEnded = createSessionsUsWebhookTrigger({
  name: "event_ended",
  displayName: "Event Ended",
  description: "Triggered when an event ends.",
  aiMetadata: {
    description: "Fires when an event finishes (the live event concludes). The payload includes the underlying session, the event id and slug, and the owning participant."
  },
  trigger: "EVENT_ENDED" /* EVENT_ENDED */,
  storeKey: "sessions_event_ended_trigger",
  sampleData: {
    session: {
      id: "8208f783-fba9-4045-ae6e-dea64f5ab7ea",
      name: "My Best Event",
      description: null,
      quickSession: !1,
      room: null,
      createdAt: "2023-11-30T10:46:13.675Z",
      startAt: "2023-11-30T11:00:00.000Z",
      actualStart: null,
      endedAt: null,
      booking: null,
      event: {
        id: "52259a5f-f706-41d9-8b58-1b8796bd0ffc",
        slug: "f34bd8a5-a7d9-47f2-aee9-031d10a76b75-83897/my-best-event"
      },
      participants: [
        {
          id: "d98f984f-982b-4a17-8a3d-093a4a774b49",
          isOwner: !0,
          guest: null,
          user: {
            id: "9090bd7c-0cf9-4716-837e-43f3821a65c4",
            email: "email@example.com",
            lastName: "Pieces",
            firstName: "Active"
          }
        }
      ]
    }
  }
});

// ../ap/packages/pieces/community/sessions-us/src/lib/triggers/event-new-registration.ts
var eventNewRegistration = createSessionsUsWebhookTrigger({
  name: "event_new_registration",
  displayName: "Event New Registration",
  description: "Triggered when a new registration for an event occurs.",
  aiMetadata: {
    description: "Fires when a person registers for a published event. The payload includes the event and session ids plus the registered participant (name, email, and their submitted registration form answers)."
  },
  trigger: "EVENT_NEW_REGISTRATION" /* EVENT_NEW_REGISTRATION */,
  storeKey: "sessions_event_new_registration_trigger",
  sampleData: {
    eventId: "52259a5f-f706-41d9-8b58-1b8796bd0ffc",
    sessionId: "8208f783-fba9-4045-ae6e-dea64f5ab7ea",
    registeredParticipant: {
      id: "5164d6de-5f72-4990-8a42-8b5c9b7d5670",
      email: "email@example.com",
      firstName: "Active Pieces",
      form: [
        {
          question: "Name",
          answer: "Active Pieces"
        },
        {
          question: "Email",
          answer: "email@example.com"
        }
      ]
    }
  }
});

// ../ap/packages/pieces/community/sessions-us/src/lib/triggers/event-published.ts
var eventPublished = createSessionsUsWebhookTrigger({
  name: "event_published",
  displayName: "Event Published",
  description: "Triggered when an event is published.",
  aiMetadata: {
    description: "Fires when an event goes from draft to published, meaning it is now live and open for registration. The payload includes the underlying session, the event id and slug, and the owning participant."
  },
  trigger: "EVENT_PUBLISHED" /* EVENT_PUBLISHED */,
  storeKey: "sessions_event_published_trigger",
  sampleData: {
    session: {
      id: "8208f783-fba9-4045-ae6e-dea64f5ab7ea",
      name: "My Best Event",
      description: null,
      quickSession: !1,
      room: null,
      createdAt: "2023-11-30T10:46:13.675Z",
      startAt: "2023-11-30T11:00:00.000Z",
      actualStart: null,
      endedAt: null,
      booking: null,
      event: {
        id: "52259a5f-f706-41d9-8b58-1b8796bd0ffc",
        slug: "f34bd8a5-a7d9-47f2-aee9-031d10a76b75-83897/my-best-event"
      },
      participants: [
        {
          id: "d98f984f-982b-4a17-8a3d-093a4a774b49",
          isOwner: !0,
          guest: null,
          user: {
            id: "9090bd7c-0cf9-4716-837e-43f3821a65c4",
            email: "email@example.com",
            lastName: "Pieces",
            firstName: "Active"
          }
        }
      ]
    }
  }
});

// ../ap/packages/pieces/community/sessions-us/src/lib/triggers/event-started.ts
var eventStarted = createSessionsUsWebhookTrigger({
  name: "event_started",
  displayName: "Event Started",
  description: "Triggered when an event starts.",
  aiMetadata: {
    description: "Fires when a published event begins (goes live). The payload includes the underlying session, the event id and slug, and the owning participant."
  },
  trigger: "EVENT_STARTED" /* EVENT_STARTED */,
  storeKey: "sessions_event_started_trigger",
  sampleData: {
    session: {
      id: "8208f783-fba9-4045-ae6e-dea64f5ab7ea",
      name: "My Best Event",
      description: null,
      quickSession: !1,
      room: null,
      createdAt: "2023-11-30T10:46:13.675Z",
      startAt: "2023-11-30T11:00:00.000Z",
      actualStart: null,
      endedAt: null,
      booking: null,
      event: {
        id: "52259a5f-f706-41d9-8b58-1b8796bd0ffc",
        slug: "f34bd8a5-a7d9-47f2-aee9-031d10a76b75-83897/my-best-event"
      },
      participants: [
        {
          id: "d98f984f-982b-4a17-8a3d-093a4a774b49",
          isOwner: !0,
          guest: null,
          user: {
            id: "9090bd7c-0cf9-4716-837e-43f3821a65c4",
            email: "email@example.com",
            lastName: "Pieces",
            firstName: "Active"
          }
        }
      ]
    }
  }
});

// ../ap/packages/pieces/community/sessions-us/src/lib/triggers/session-created.ts
var sessionCreated = createSessionsUsWebhookTrigger({
  name: "session_created",
  displayName: "Session Created",
  description: "Triggered when a new session is created.",
  aiMetadata: {
    description: "Fires when a new session (a private video meeting) is created on Sessions.us. The payload includes the session details and its participants."
  },
  trigger: "SESSION_CREATED" /* SESSION_CREATED */,
  storeKey: "sessions_session_created_trigger",
  sampleData: {
    session: {
      id: "5645d810-4e29-4c2c-b9a5-84d71a6429dd",
      name: "My Talking Session",
      description: "",
      quickSession: !1,
      room: null,
      createdAt: "2023-11-30T10:30:16.535Z",
      startAt: "2023-11-30T10:31:00.000Z",
      actualStart: null,
      endedAt: null,
      booking: null,
      event: null,
      participants: [
        {
          id: "0f2aced6-e099-424b-9448-4dd94d9ba3a0",
          isOwner: !0,
          guest: null,
          user: {
            id: "9090bd7c-0cf9-4716-837e-43f3821a65c4",
            email: "email@example.com",
            lastName: "Pieces",
            firstName: "Active"
          }
        }
      ]
    }
  }
});

// ../ap/packages/pieces/community/sessions-us/src/lib/triggers/session-ended.ts
var sessionEnded = createSessionsUsWebhookTrigger({
  name: "session_ended",
  displayName: "Session Ended",
  description: "Triggered when a session has ended.",
  aiMetadata: {
    description: "Fires when a session finishes (the meeting ends). The payload includes the session details and its participants."
  },
  trigger: "SESSION_ENDED" /* SESSION_ENDED */,
  storeKey: "sessions_session_ended_trigger",
  sampleData: {
    session: {
      id: "5645d810-4e29-4c2c-b9a5-84d71a6429dd",
      name: "My Talking Session",
      description: "",
      quickSession: !1,
      room: null,
      createdAt: "2023-11-30T10:30:16.535Z",
      startAt: "2023-11-30T10:31:00.000Z",
      actualStart: null,
      endedAt: null,
      booking: null,
      event: null,
      participants: [
        {
          id: "0f2aced6-e099-424b-9448-4dd94d9ba3a0",
          isOwner: !0,
          guest: null,
          user: {
            id: "9090bd7c-0cf9-4716-837e-43f3821a65c4",
            email: "email@example.com",
            lastName: "Pieces",
            firstName: "Active"
          }
        }
      ]
    }
  }
});

// ../ap/packages/pieces/community/sessions-us/src/lib/triggers/session-started.ts
var sessionStarted = createSessionsUsWebhookTrigger({
  name: "session_started",
  displayName: "Session Started",
  description: "Triggered when a session starts.",
  aiMetadata: {
    description: "Fires when a session goes live (the meeting begins). The payload includes the session details and its participants."
  },
  trigger: "SESSION_STARTED" /* SESSION_STARTED */,
  storeKey: "sessions_session_started_trigger",
  sampleData: {
    session: {
      id: "5645d810-4e29-4c2c-b9a5-84d71a6429dd",
      name: "My Talking Session",
      description: "",
      quickSession: !1,
      room: null,
      createdAt: "2023-11-30T10:30:16.535Z",
      startAt: "2023-11-30T10:31:00.000Z",
      actualStart: null,
      endedAt: null,
      booking: null,
      event: null,
      participants: [
        {
          id: "0f2aced6-e099-424b-9448-4dd94d9ba3a0",
          isOwner: !0,
          guest: null,
          user: {
            id: "9090bd7c-0cf9-4716-837e-43f3821a65c4",
            email: "email@example.com",
            lastName: "Pieces",
            firstName: "Active"
          }
        }
      ]
    }
  }
});

// ../ap/packages/pieces/community/sessions-us/src/lib/triggers/takeaway-ready.ts
var takeawayReady = createSessionsUsWebhookTrigger({
  name: "takeaway_ready",
  displayName: "Takeaway Ready",
  description: "Triggered when a takeaway becomes available.",
  aiMetadata: {
    description: "Fires when the AI-generated takeaways (summary notes) for a session have finished processing and are available. The payload includes the session id and the takeaways as both plain text and rich document content."
  },
  trigger: "TAKEAWAY_READY" /* TAKEAWAY_READY */,
  storeKey: "sessions_takeaway_ready_trigger",
  sampleData: {
    session: {
      id: "e5ebb4a1-a5a5-4a10-b18b-e1a89ddac27e",
      takeawaysText: "Hello",
      takeawaysRaw: {
        type: "doc",
        content: [
          {
            type: "paragraph",
            attrs: {
              textAlign: "left",
              indent: 0
            },
            content: [
              {
                type: "text",
                text: "Hello"
              }
            ]
          }
        ]
      }
    }
  }
});

// ../ap/packages/pieces/community/sessions-us/src/lib/triggers/transcript-ready.ts
var transcriptReady = createSessionsUsWebhookTrigger({
  name: "transcript_ready",
  displayName: "Transcript Ready",
  description: "Triggered when a session has ended.",
  aiMetadata: {
    description: "Fires when the transcript for a session has finished processing and is available. The payload includes the session id and the transcript segments (text, language, speaker participant id, and timestamps)."
  },
  trigger: "TRANSCRIPT_READY" /* TRANSCRIPT_READY */,
  storeKey: "sessions_transcript_ready_trigger",
  sampleData: {
    session: {
      id: "8208f783-fba9-4045-ae6e-dea64f5ab7ea",
      transcripts: [
        {
          content: [
            {
              text: "Hello, my name is...",
              language: "en"
            }
          ],
          participantId: "d98f984f-982b-4a17-8a3d-093a4a774b49",
          sourceTimestamp: "2023-11-30T10:50:26.363Z",
          sourceLanguage: "en"
        }
      ]
    }
  }
});

// ../ap/packages/pieces/community/sessions-us/src/index.ts
var sessionAuth = import_pieces_framework5.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common5.httpClient.sendRequest({
        method: import_pieces_common5.HttpMethod.GET,
        url: "https://api.app.sessions.us/api/sessions",
        headers: {
          accept: "application/json",
          "x-api-key": `${auth}`
        }
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        error: "Invalid API Key",
        valid: !1
      };
    }
  }
}), sessionsUs = (0, import_pieces_framework5.createPiece)({
  displayName: "Sessions.us",
  description: "Video conferencing platform for businesses and professionals",
  auth: sessionAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/sessions-us.png",
  authors: ["kishanprmr", "MoShizzle", "abuaboud"],
  categories: [import_pieces_framework6.PieceCategory.BUSINESS_INTELLIGENCE],
  actions: [
    createSession,
    createEvent,
    publishEvent,
    (0, import_pieces_common5.createCustomApiCallAction)({
      baseUrl: () => "https://api.app.sessions.us/api",
      auth: sessionAuth,
      authMapping: async (auth) => ({
        "x-api-key": `${auth.secret_text}`
      })
    })
  ],
  triggers: [
    bookingCreated,
    bookingStarted,
    bookingEnded,
    eventCreated,
    eventPublished,
    eventStarted,
    eventEnded,
    eventNewRegistration,
    sessionCreated,
    sessionStarted,
    sessionEnded,
    takeawayReady,
    transcriptReady
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  sessionAuth,
  sessionsUs
});
