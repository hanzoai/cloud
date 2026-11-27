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

// ../ap/packages/pieces/community/motiontools/src/index.ts
var index_exports = {};
__export(index_exports, {
  motiontools: () => motiontools
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/motiontools/src/lib/actions/create-1-stop-booking.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/motiontools/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), motiontoolsAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Motiontools API Key",
  description: "API Key for authenticating with Motiontools. Refer https://help.motiontools.io/en/articles/10476909-create-an-api-key",
  required: !0
});

// ../ap/packages/pieces/community/motiontools/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.motiontools.io/api";
async function makeRequest(apiKey, method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        "api-key": apiKey
      },
      body
    })).body;
  } catch (error) {
    throw new Error(
      `Unexpected error: ${JSON.stringify(
        error.response || error.message || error
      )}`
    );
  }
}

// ../ap/packages/pieces/community/motiontools/src/lib/actions/create-1-stop-booking.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), create1stopBooking = (0, import_pieces_framework2.createAction)({
  auth: motiontoolsAuth,
  name: "create1stopBooking",
  displayName: "Create 1-stop Booking",
  description: "Create a hailing booking (1-stop).",
  audience: "both",
  aiMetadata: { description: "Creates a MotionTools hailing booking with a single stop (e.g. a one-off pickup or dropoff). Use when an agent needs to place a ride/delivery request at one location; supply the payment method (required) and optionally a scheduled time and the stop's coordinates/address/contact details. Not idempotent \u2014 each call submits a new booking.", idempotent: !1 },
  props: {
    scheduled_at: import_pieces_framework2.Property.ShortText({
      displayName: "Scheduled At",
      description: "Scheduled datetime (ISO 8601) for the booking (optional)",
      required: !1
    }),
    payment_method: import_pieces_framework2.Property.ShortText({
      displayName: "Payment Method",
      description: "Payment method (e.g. cash, card)",
      required: !0
    }),
    stop_type: import_pieces_framework2.Property.ShortText({
      displayName: "Stop Type",
      description: "Type of stop (e.g. pickup, dropoff)",
      required: !1
    }),
    stop_lat: import_pieces_framework2.Property.Number({
      displayName: "Latitude",
      description: "Latitude for the stop (optional)",
      required: !1
    }),
    stop_lng: import_pieces_framework2.Property.Number({
      displayName: "Longitude",
      description: "Longitude for the stop (optional)",
      required: !1
    }),
    stop_street: import_pieces_framework2.Property.ShortText({
      displayName: "Street",
      description: "Street name for the stop (optional)",
      required: !1
    }),
    stop_number: import_pieces_framework2.Property.ShortText({
      displayName: "Street Number",
      description: "Street number for the stop (optional)",
      required: !1
    }),
    stop_city: import_pieces_framework2.Property.ShortText({
      displayName: "City",
      description: "City for the stop (optional)",
      required: !1
    }),
    stop_country: import_pieces_framework2.Property.ShortText({
      displayName: "Country",
      description: "Country for the stop (optional)",
      required: !1
    }),
    stop_zip_code: import_pieces_framework2.Property.ShortText({
      displayName: "Postal Code",
      description: "Postal code for the stop (optional)",
      required: !1
    }),
    stop_additional_line: import_pieces_framework2.Property.ShortText({
      displayName: "Address Additional Line",
      description: "Additional address line for the stop (optional)",
      required: !1
    }),
    stop_location_name: import_pieces_framework2.Property.ShortText({
      displayName: "Location Name",
      description: "Location name for the stop (optional)",
      required: !1
    }),
    stop_first_name: import_pieces_framework2.Property.ShortText({
      displayName: "Contact First Name",
      description: "Contact's first name at the stop (optional)",
      required: !1
    }),
    stop_last_name: import_pieces_framework2.Property.ShortText({
      displayName: "Contact Last Name",
      description: "Contact's last name at the stop (optional)",
      required: !1
    }),
    stop_phone_number: import_pieces_framework2.Property.ShortText({
      displayName: "Contact Phone Number",
      description: "Contact's phone number (optional)",
      required: !1
    }),
    stop_email: import_pieces_framework2.Property.ShortText({
      displayName: "Contact Email",
      description: "Contact's email (optional)",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let {
      scheduled_at,
      payment_method,
      stop_type,
      stop_lat,
      stop_lng,
      stop_street,
      stop_number,
      stop_city,
      stop_country,
      stop_zip_code,
      stop_additional_line,
      stop_location_name,
      stop_first_name,
      stop_last_name,
      stop_phone_number,
      stop_email
    } = propsValue, booking = {};
    scheduled_at && (booking.scheduled_at = scheduled_at), booking.payment_method = payment_method;
    let stop = {
      type: stop_type || "pickup",
      flow: "full"
    };
    typeof stop_lat == "number" && (stop.lat = stop_lat), typeof stop_lng == "number" && (stop.lng = stop_lng), stop_street && (stop.street = stop_street), stop_number && (stop.number = stop_number), stop_city && (stop.city = stop_city), stop_country && (stop.country = stop_country), stop_zip_code && (stop.zip_code = stop_zip_code), stop_additional_line && (stop.additional_line = stop_additional_line), stop_location_name && (stop.location_name = stop_location_name), stop_first_name && (stop.first_name = stop_first_name), stop_last_name && (stop.last_name = stop_last_name), stop_phone_number && (stop.phone_number = stop_phone_number), stop_email && (stop.email = stop_email), booking.stops = [stop];
    try {
      return await makeRequest(
        auth.secret_text,
        import_pieces_common2.HttpMethod.POST,
        "/hailing/bookings",
        { booking }
      );
    } catch (error) {
      throw new Error(
        `Failed to create booking: ${error instanceof Error ? error.message : String(error)}`
      );
    }
  }
});

// ../ap/packages/pieces/community/motiontools/src/lib/actions/create-2-stop-booking.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), create2stopBooking = (0, import_pieces_framework3.createAction)({
  auth: motiontoolsAuth,
  name: "create2stopBooking",
  displayName: "Create 2-stop Booking",
  description: "Create a hailing booking with two stops (pickup + dropoff).",
  audience: "both",
  aiMetadata: { description: "Creates a MotionTools hailing booking with two stops, a pickup and a dropoff. Use when an agent needs a point-to-point ride or delivery (e.g. courier from origin to destination); supply the payment method (required) and optionally each stop's coordinates/address/contact details, a scheduled time, and metadata. Not idempotent \u2014 each call submits a new booking.", idempotent: !1 },
  props: {
    scheduled_at: import_pieces_framework3.Property.ShortText({
      displayName: "Scheduled At",
      description: "Scheduled datetime (ISO 8601) for the booking (optional)",
      required: !1
    }),
    payment_method: import_pieces_framework3.Property.ShortText({
      displayName: "Payment Method",
      description: "Payment method (e.g. cash, card)",
      required: !0
    }),
    additional_information: import_pieces_framework3.Property.LongText({
      displayName: "Additional Information",
      description: "Any extra notes for the booking (optional)",
      required: !1
    }),
    metadata: import_pieces_framework3.Property.Json({
      displayName: "Metadata",
      description: "Additional metadata for the booking (optional)",
      required: !1
    }),
    // Stop 1 (pickup)
    stop1_type: import_pieces_framework3.Property.ShortText({
      displayName: "Stop 1 Type",
      required: !1
    }),
    stop1_metadata: import_pieces_framework3.Property.Json({
      displayName: "Stop 1 Metadata",
      required: !1
    }),
    stop1_lat: import_pieces_framework3.Property.Number({
      displayName: "Stop 1 Latitude",
      required: !1
    }),
    stop1_lng: import_pieces_framework3.Property.Number({
      displayName: "Stop 1 Longitude",
      required: !1
    }),
    stop1_street: import_pieces_framework3.Property.ShortText({
      displayName: "Stop 1 Street",
      required: !1
    }),
    stop1_number: import_pieces_framework3.Property.ShortText({
      displayName: "Stop 1 Street Number",
      required: !1
    }),
    stop1_city: import_pieces_framework3.Property.ShortText({
      displayName: "Stop 1 City",
      required: !1
    }),
    stop1_country: import_pieces_framework3.Property.ShortText({
      displayName: "Stop 1 Country",
      required: !1
    }),
    stop1_zip_code: import_pieces_framework3.Property.ShortText({
      displayName: "Stop 1 Postal Code",
      required: !1
    }),
    stop1_additional_line: import_pieces_framework3.Property.ShortText({
      displayName: "Stop 1 Address Additional Line",
      required: !1
    }),
    stop1_location_name: import_pieces_framework3.Property.ShortText({
      displayName: "Stop 1 Location Name",
      required: !1
    }),
    stop1_first_name: import_pieces_framework3.Property.ShortText({
      displayName: "Stop 1 Contact First Name",
      required: !1
    }),
    stop1_last_name: import_pieces_framework3.Property.ShortText({
      displayName: "Stop 1 Contact Last Name",
      required: !1
    }),
    stop1_phone_number: import_pieces_framework3.Property.ShortText({
      displayName: "Stop 1 Contact Phone",
      required: !1
    }),
    stop1_email: import_pieces_framework3.Property.ShortText({
      displayName: "Stop 1 Contact Email",
      required: !1
    }),
    // Stop 2 (dropoff)
    stop2_type: import_pieces_framework3.Property.ShortText({
      displayName: "Stop 2 Type",
      required: !1
    }),
    stop2_metadata: import_pieces_framework3.Property.Json({
      displayName: "Stop 2 Metadata",
      required: !1
    }),
    stop2_lat: import_pieces_framework3.Property.Number({
      displayName: "Stop 2 Latitude",
      required: !1
    }),
    stop2_lng: import_pieces_framework3.Property.Number({
      displayName: "Stop 2 Longitude",
      required: !1
    }),
    stop2_street: import_pieces_framework3.Property.ShortText({
      displayName: "Stop 2 Street",
      required: !1
    }),
    stop2_number: import_pieces_framework3.Property.ShortText({
      displayName: "Stop 2 Street Number",
      required: !1
    }),
    stop2_city: import_pieces_framework3.Property.ShortText({
      displayName: "Stop 2 City",
      required: !1
    }),
    stop2_country: import_pieces_framework3.Property.ShortText({
      displayName: "Stop 2 Country",
      required: !1
    }),
    stop2_zip_code: import_pieces_framework3.Property.ShortText({
      displayName: "Stop 2 Postal Code",
      required: !1
    }),
    stop2_additional_line: import_pieces_framework3.Property.ShortText({
      displayName: "Stop 2 Address Additional Line",
      required: !1
    }),
    stop2_location_name: import_pieces_framework3.Property.ShortText({
      displayName: "Stop 2 Location Name",
      required: !1
    }),
    stop2_first_name: import_pieces_framework3.Property.ShortText({
      displayName: "Stop 2 Contact First Name",
      required: !1
    }),
    stop2_last_name: import_pieces_framework3.Property.ShortText({
      displayName: "Stop 2 Contact Last Name",
      required: !1
    }),
    stop2_phone_number: import_pieces_framework3.Property.ShortText({
      displayName: "Stop 2 Contact Phone",
      required: !1
    }),
    stop2_email: import_pieces_framework3.Property.ShortText({
      displayName: "Stop 2 Contact Email",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let {
      scheduled_at,
      payment_method,
      additional_information,
      metadata,
      stop1_type,
      stop1_metadata,
      stop1_lat,
      stop1_lng,
      stop1_street,
      stop1_number,
      stop1_city,
      stop1_country,
      stop1_zip_code,
      stop1_additional_line,
      stop1_location_name,
      stop1_first_name,
      stop1_last_name,
      stop1_phone_number,
      stop1_email,
      stop2_type,
      stop2_metadata,
      stop2_lat,
      stop2_lng,
      stop2_street,
      stop2_number,
      stop2_city,
      stop2_country,
      stop2_zip_code,
      stop2_additional_line,
      stop2_location_name,
      stop2_first_name,
      stop2_last_name,
      stop2_phone_number,
      stop2_email
    } = propsValue, booking = {};
    scheduled_at && (booking.scheduled_at = scheduled_at), booking.payment_method = payment_method, additional_information && (booking.additional_information = additional_information), metadata && (booking.metadata = metadata);
    let stop1 = {
      type: stop1_type || "pickup",
      flow: "full"
    };
    stop1_metadata && (stop1.metadata = stop1_metadata), typeof stop1_lat == "number" && (stop1.lat = stop1_lat), typeof stop1_lng == "number" && (stop1.lng = stop1_lng), stop1_street && (stop1.street = stop1_street), stop1_number && (stop1.number = stop1_number), stop1_city && (stop1.city = stop1_city), stop1_country && (stop1.country = stop1_country), stop1_zip_code && (stop1.zip_code = stop1_zip_code), stop1_additional_line && (stop1.additional_line = stop1_additional_line), stop1_location_name && (stop1.location_name = stop1_location_name), stop1_first_name && (stop1.first_name = stop1_first_name), stop1_last_name && (stop1.last_name = stop1_last_name), stop1_phone_number && (stop1.phone_number = stop1_phone_number), stop1_email && (stop1.email = stop1_email);
    let stop2 = {
      type: stop2_type || "dropoff",
      flow: "full"
    };
    return stop2_metadata && (stop2.metadata = stop2_metadata), typeof stop2_lat == "number" && (stop2.lat = stop2_lat), typeof stop2_lng == "number" && (stop2.lng = stop2_lng), stop2_street && (stop2.street = stop2_street), stop2_number && (stop2.number = stop2_number), stop2_city && (stop2.city = stop2_city), stop2_country && (stop2.country = stop2_country), stop2_zip_code && (stop2.zip_code = stop2_zip_code), stop2_additional_line && (stop2.additional_line = stop2_additional_line), stop2_location_name && (stop2.location_name = stop2_location_name), stop2_first_name && (stop2.first_name = stop2_first_name), stop2_last_name && (stop2.last_name = stop2_last_name), stop2_phone_number && (stop2.phone_number = stop2_phone_number), stop2_email && (stop2.email = stop2_email), booking.stops = [stop1, stop2], await makeRequest(
      auth.secret_text,
      import_pieces_common3.HttpMethod.POST,
      "/hailing/bookings",
      { booking }
    );
  }
});

// ../ap/packages/pieces/community/motiontools/src/lib/triggers/booking-created.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var bookingCreated = (0, import_pieces_framework4.createTrigger)({
  auth: motiontoolsAuth,
  name: "bookingCreated",
  displayName: "Booking Created",
  description: "Triggers when a new booking is created.  Only available if you have a MotionToolsadmin account",
  aiMetadata: {
    description: "Fires when a new booking is created in MotionTools (the booking.created webhook event). Each event carries the booking id, customer, service, and scheduling details. Requires a MotionTools admin account to register the webhook."
  },
  props: {
    instructions: import_pieces_framework4.Property.MarkDown({
      value: `# Motiontools Webhook Setup

1. Open Motiontools \u2192 Settings \u2192 Advanced \u2192 Webhooks
2. Click **Create webhook**
3. Name: e.g., Activepieces Booking Created
4. Endpoint URL: paste the Activepieces webhook URL below:

\`\`\`text
			{{webhookUrl}}
			\`\`\`

5. Enable event: **booking.created**
6. Toggle **Active** and click **Save**
`
    })
  },
  sampleData: {
    id: "db76ba31-96f8-4c95-b71d-85ade5c7a640",
    timestamp: "2020-11-09T10:19:07Z",
    resource_type: "booking",
    event: "created",
    data: {
      booking_id: "db76ba31-96f8-4c95-b71d-85ade5c7a640",
      service_id: "db76ba31-96f8-4c95-b71d-85ade5c7a640",
      customer_id: "db76ba31-96f8-4c95-b71d-85ade5c7a640",
      service_area_id: "db76ba31-96f8-4c95-b71d-85ade5c7a640",
      place_ids: "db76ba31-96f8-4c95-b71d-85ade5c7a640",
      scheduled_at: "2020-11-09T10:19:07Z",
      status: "cancelled",
      requested_capabilities: {
        property1: "string",
        property2: "string"
      },
      managing_organization_ids: "db76ba31-96f8-4c95-b71d-85ade5c7a640",
      external_id: "string"
    }
  },
  type: import_pieces_framework4.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let body = context.payload.body;
    return body.event === "created" && body.resource_type === "booking" && body.data ? body : [];
  }
});

// ../ap/packages/pieces/community/motiontools/src/lib/triggers/booking-status-update.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var bookingStatusUpdate = (0, import_pieces_framework5.createTrigger)({
  auth: motiontoolsAuth,
  name: "bookingStatusUpdate",
  displayName: "Booking Status Update",
  description: "Triggers when a booking status is updated.  Only available if you have a MotionToolsadmin account",
  aiMetadata: {
    description: "Fires when an existing booking is modified in MotionTools (the booking.modified webhook event), e.g. its status changes. Each event carries the affected booking and customer ids. Requires a MotionTools admin account to register the webhook."
  },
  props: {
    instructions: import_pieces_framework5.Property.MarkDown({
      value: `# Motiontools Webhook Setup

1. Open Motiontools \u2192 Settings \u2192 Advanced \u2192 Webhooks
2. Click **Create webhook**
3. Name: e.g., Activepieces Booking Status Update
4. Endpoint URL: paste the Activepieces webhook URL below:

\`\`\`text
            {{webhookUrl}}
            \`\`\`  
5. Enable event: **booking.modified**
6. Toggle **Active** and click **Save**
`
    })
  },
  sampleData: {
    id: "db76ba31-96f8-4c95-b71d-85ade5c7a640",
    timestamp: "2020-11-09T10:19:07Z",
    resource_type: "booking",
    event: "modified",
    data: {
      booking_id: "db76ba31-96f8-4c95-b71d-85ade5c7a640",
      customer_id: "db76ba31-96f8-4c95-b71d-85ade5c7a640",
      external_id: "string",
      service_area_id: "db76ba31-96f8-4c95-b71d-85ade5c7a640"
    }
  },
  type: import_pieces_framework5.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let body = context.payload.body;
    return body.event === "modified" && body.resource_type === "booking" && body.data ? [body] : [];
  }
});

// ../ap/packages/pieces/community/motiontools/src/lib/triggers/booking-stop-status-update.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var bookingStopStatusUpdate = (0, import_pieces_framework6.createTrigger)({
  auth: motiontoolsAuth,
  name: "bookingStopStatusUpdate",
  displayName: "Booking Stop Status Update",
  description: "",
  aiMetadata: {
    description: "Fires when a booking's route changes in MotionTools (the booking.route_changed webhook event), reflecting an update to its stops/route. Each event carries the affected booking and customer ids."
  },
  props: {
    instructions: import_pieces_framework6.Property.MarkDown({
      value: `# Motiontools Webhook Setup

1. Open Motiontools \u2192 Settings \u2192 Advanced \u2192 Webhooks
2. Click **Create webhook**
3. Name: e.g., Activepieces Booking Stop Status Update
4. Endpoint URL: paste the Activepieces webhook URL below:

\`\`\`text
            {{webhookUrl}}
            \`\`\`  
5. Enable event: **booking.route_changed**
6. Toggle **Active** and click **Save**
`
    })
  },
  sampleData: {
    id: "db76ba31-96f8-4c95-b71d-85ade5c7a640",
    timestamp: "2020-11-09T10:19:07Z",
    resource_type: "booking",
    event: "route_changed",
    data: {
      booking_id: "db76ba31-96f8-4c95-b71d-85ade5c7a640",
      customer_id: "db76ba31-96f8-4c95-b71d-85ade5c7a640",
      external_id: "string",
      service_area_id: "db76ba31-96f8-4c95-b71d-85ade5c7a640"
    }
  },
  type: import_pieces_framework6.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let body = context.payload.body;
    return body.event === "route_changed" && body.resource_type === "booking" && body.data ? [body] : [];
  }
});

// ../ap/packages/pieces/community/motiontools/src/lib/triggers/package-created.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var packageCreated = (0, import_pieces_framework7.createTrigger)({
  auth: motiontoolsAuth,
  name: "packageCreated",
  displayName: "Package Created",
  description: "Triggers when a package is created",
  aiMetadata: {
    description: "Fires when a new hailing package is created in MotionTools (the hailing_package.created webhook event). Each event carries the package id, customer, and its initial status/customs status."
  },
  props: {
    instructions: import_pieces_framework7.Property.MarkDown({
      value: `# Motiontools Webhook Setup
        
        1. Open Motiontools \u2192 Settings \u2192 Advanced \u2192 Webhooks
        2. Click **Create webhook**
        3. Name: e.g., Activepieces Booking Stop Status Update
        4. Endpoint URL: paste the Activepieces webhook URL below:
        
        \`\`\`text
                    {{webhookUrl}}
                    \`\`\`  
        5. Enable event: **hailing_package.created**
        6. Toggle **Active** and click **Save**
        `
    })
  },
  sampleData: {
    id: "db76ba31-96f8-4c95-b71d-85ade5c7a640",
    timestamp: "2020-11-09T10:19:07Z",
    resource_type: "hailing_package",
    event: "created",
    data: {
      package_id: "db76ba31-96f8-4c95-b71d-85ade5c7a640",
      customer_id: "db76ba31-96f8-4c95-b71d-85ade5c7a640",
      status: "at_customs",
      customs_status: "documents_needed"
    }
  },
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let body = context.payload.body;
    return body.event === "created" && body.resource_type === "hailing_package" && body.data ? [body] : [];
  }
});

// ../ap/packages/pieces/community/motiontools/src/lib/triggers/package-status-update.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var packageStatusUpdate = (0, import_pieces_framework8.createTrigger)({
  auth: motiontoolsAuth,
  name: "packageStatusUpdate",
  displayName: "Package Status Update",
  description: "",
  aiMetadata: {
    description: "Fires when a hailing package transitions customs status in MotionTools (the hailing_package.customs_transition webhook event). Each event carries the package and customer ids plus the from/to customs status of the transition."
  },
  props: {
    instructions: import_pieces_framework8.Property.MarkDown({
      value: `# Motiontools Webhook Setup
                
                1. Open Motiontools \u2192 Settings \u2192 Advanced \u2192 Webhooks
                2. Click **Create webhook**
                3. Name: e.g., Activepieces Booking Stop Status Update
                4. Endpoint URL: paste the Activepieces webhook URL below:
                
                \`\`\`text
                            {{webhookUrl}}
                            \`\`\`  
                5. Enable event: **hailing_package.customs_transition**
                6. Toggle **Active** and click **Save**
                `
    })
  },
  sampleData: {
    id: "db76ba31-96f8-4c95-b71d-85ade5c7a640",
    timestamp: "2020-11-09T10:19:07Z",
    resource_type: "hailing_package",
    event: "customs_transition",
    data: {
      package_id: "db76ba31-96f8-4c95-b71d-85ade5c7a640",
      customer_id: "db76ba31-96f8-4c95-b71d-85ade5c7a640",
      from: "documents_needed",
      to: "documents_needed",
      event: "announce"
    }
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let body = context.payload.body;
    return body.event === "customs_transition" && body.resource_type === "hailing_package" && body.data ? [body] : [];
  }
});

// ../ap/packages/pieces/community/motiontools/src/index.ts
var motiontools = (0, import_pieces_framework9.createPiece)({
  displayName: "MotionTools",
  auth: motiontoolsAuth,
  description: "Digitize processes, boost efficiency and excite users with MotionTools, the operating system for fleet-based service providers.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/motiontools.png",
  authors: ["sanket-a11y"],
  actions: [create1stopBooking, create2stopBooking],
  triggers: [
    bookingCreated,
    bookingStatusUpdate,
    bookingStopStatusUpdate,
    packageCreated,
    packageStatusUpdate
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  motiontools
});
