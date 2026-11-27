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

// ../ap/packages/pieces/community/formitable/src/index.ts
var index_exports = {};
__export(index_exports, {
  formitable: () => formitable,
  formitableAuth: () => formitableAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/formitable/src/lib/triggers/register-trigger.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/formitable/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");
var formitableCommon = {
  baseUrl: "https://api.formitable.com/api/v1.2",
  restaurant: import_pieces_framework.Property.Dropdown({
    displayName: "Restaurant",
    description: "Select the restaurant to monitor",
    required: !0,
    auth: formitableAuth,
    refreshers: [],
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          placeholder: "Connect your account first",
          options: []
        };
      let apiKey = auth.secret_text;
      return {
        disabled: !1,
        options: (await formitableApiCall({
          apiKey,
          method: import_pieces_common.HttpMethod.GET,
          endpoint: "/restaurants"
        })).map((restaurant) => ({
          label: restaurant.name,
          value: restaurant.uid
        }))
      };
    }
  })
};
async function formitableApiCall({
  apiKey,
  method,
  endpoint,
  body,
  queryParams
}) {
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${formitableCommon.baseUrl}${endpoint}`,
    headers: {
      ApiKey: apiKey,
      Accept: "application/json",
      "Content-Type": "application/json"
    },
    body,
    queryParams
  })).body;
}
async function createWebhook({
  apiKey,
  restaurantUid,
  webhookUrl,
  events,
  secretKey
}) {
  return formitableApiCall({
    apiKey,
    method: import_pieces_common.HttpMethod.POST,
    endpoint: `/${restaurantUid}/webhook`,
    body: {
      events,
      url: webhookUrl,
      secretKey
    }
  });
}
async function deleteWebhook({
  apiKey,
  restaurantUid,
  webhookUid
}) {
  await formitableApiCall({
    apiKey,
    method: import_pieces_common.HttpMethod.DELETE,
    endpoint: `/${restaurantUid}/webhook/${webhookUid}`
  });
}

// ../ap/packages/pieces/community/formitable/src/lib/triggers/register-trigger.ts
var formitableRegisterTrigger = ({
  name,
  displayName,
  description,
  aiMetadata,
  event,
  sampleData
}) => (0, import_pieces_framework2.createTrigger)({
  auth: formitableAuth,
  name: `formitable_${name}`,
  displayName,
  description,
  aiMetadata,
  props: {
    restaurant: formitableCommon.restaurant
  },
  sampleData,
  type: import_pieces_framework2.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let { restaurant } = context.propsValue, apiKey = context.auth.secret_text, webhook = await createWebhook({
      apiKey,
      restaurantUid: restaurant,
      webhookUrl: context.webhookUrl,
      events: [event],
      secretKey: `ap_${context.webhookUrl.split("/").pop()}`
    });
    await context.store.put(`formitable_${name}_webhook`, webhook);
  },
  async onDisable(context) {
    let { restaurant } = context.propsValue, apiKey = context.auth.secret_text, webhook = await context.store.get(`formitable_${name}_webhook`);
    webhook && await deleteWebhook({
      apiKey,
      restaurantUid: restaurant,
      webhookUid: webhook.uid
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/formitable/src/lib/triggers/booking-created.ts
var bookingCreated = formitableRegisterTrigger({
  name: "booking_created",
  displayName: "Booking Created",
  description: "Triggers when a new booking is created.",
  event: "booking.created",
  sampleData: {
    data: {
      booking: {
        uid: "abc123",
        date: "2024-01-15",
        time: "19:00",
        partySize: 4,
        status: "created"
      }
    },
    restaurantUid: "rest123",
    event: "booking.created"
  }
});

// ../ap/packages/pieces/community/formitable/src/lib/triggers/booking-accepted.ts
var bookingAccepted = formitableRegisterTrigger({
  name: "booking_accepted",
  displayName: "Booking Accepted",
  description: "Triggers when a booking is accepted.",
  event: "booking.accepted",
  sampleData: {
    data: {
      booking: {
        uid: "abc123",
        date: "2024-01-15",
        time: "19:00",
        partySize: 4,
        status: "accepted"
      }
    },
    restaurantUid: "rest123",
    event: "booking.accepted"
  }
});

// ../ap/packages/pieces/community/formitable/src/lib/triggers/booking-changed.ts
var bookingChanged = formitableRegisterTrigger({
  name: "booking_changed",
  displayName: "Booking Changed",
  description: "Triggers when a booking is updated.",
  event: "booking.changed",
  sampleData: {
    data: {
      booking: {
        uid: "abc123",
        date: "2024-01-15",
        time: "20:00",
        partySize: 6,
        status: "changed"
      }
    },
    restaurantUid: "rest123",
    event: "booking.changed"
  }
});

// ../ap/packages/pieces/community/formitable/src/lib/triggers/booking-checkin.ts
var bookingCheckin = formitableRegisterTrigger({
  name: "booking_checkin",
  displayName: "Guest Checked In",
  description: "Triggers when a guest checks in at the restaurant.",
  event: "booking.checkin",
  sampleData: {
    data: {
      booking: {
        uid: "abc123",
        date: "2024-01-15",
        time: "19:00",
        partySize: 4,
        status: "checkin"
      }
    },
    restaurantUid: "rest123",
    event: "booking.checkin"
  }
});

// ../ap/packages/pieces/community/formitable/src/lib/triggers/booking-checkout.ts
var bookingCheckout = formitableRegisterTrigger({
  name: "booking_checkout",
  displayName: "Guest Checked Out",
  description: "Triggers when a guest checks out from the restaurant.",
  event: "booking.checkout",
  sampleData: {
    data: {
      booking: {
        uid: "abc123",
        date: "2024-01-15",
        time: "19:00",
        partySize: 4,
        status: "checkout"
      }
    },
    restaurantUid: "rest123",
    event: "booking.checkout"
  }
});

// ../ap/packages/pieces/community/formitable/src/lib/triggers/booking-canceled.ts
var bookingCanceled = formitableRegisterTrigger({
  name: "booking_canceled",
  displayName: "Booking Canceled",
  description: "Triggers when a booking is canceled.",
  event: "booking.canceled",
  sampleData: {
    data: {
      booking: {
        uid: "abc123",
        date: "2024-01-15",
        time: "19:00",
        partySize: 4,
        status: "canceled"
      }
    },
    restaurantUid: "rest123",
    event: "booking.canceled"
  }
});

// ../ap/packages/pieces/community/formitable/src/lib/triggers/booking-failed.ts
var bookingFailed = formitableRegisterTrigger({
  name: "booking_failed",
  displayName: "Booking Failed",
  description: "Triggers when a booking fails.",
  event: "booking.failed",
  sampleData: {
    data: {
      booking: {
        uid: "abc123",
        date: "2024-01-15",
        time: "19:00",
        partySize: 4,
        status: "failed"
      }
    },
    restaurantUid: "rest123",
    event: "booking.failed"
  }
});

// ../ap/packages/pieces/community/formitable/src/lib/triggers/option-created.ts
var optionCreated = formitableRegisterTrigger({
  name: "option_created",
  displayName: "Booking Option Created",
  description: "Triggers when a booking option is created.",
  event: "option.created",
  sampleData: {
    data: {
      option: {
        uid: "opt123",
        date: "2024-01-15",
        time: "19:00",
        partySize: 4,
        status: "created"
      }
    },
    restaurantUid: "rest123",
    event: "option.created"
  }
});

// ../ap/packages/pieces/community/formitable/src/lib/triggers/option-accepted.ts
var optionAccepted = formitableRegisterTrigger({
  name: "option_accepted",
  displayName: "Booking Option Accepted",
  description: "Triggers when a booking option is accepted. A booking.accepted event will also be fired.",
  event: "option.accepted",
  sampleData: {
    data: {
      option: {
        uid: "opt123",
        date: "2024-01-15",
        time: "19:00",
        partySize: 4,
        status: "accepted"
      }
    },
    restaurantUid: "rest123",
    event: "option.accepted"
  }
});

// ../ap/packages/pieces/community/formitable/src/lib/triggers/option-canceled.ts
var optionCanceled = formitableRegisterTrigger({
  name: "option_canceled",
  displayName: "Booking Option Canceled",
  description: "Triggers when a booking option is canceled.",
  event: "option.canceled",
  sampleData: {
    data: {
      option: {
        uid: "opt123",
        date: "2024-01-15",
        time: "19:00",
        partySize: 4,
        status: "canceled"
      }
    },
    restaurantUid: "rest123",
    event: "option.canceled"
  }
});

// ../ap/packages/pieces/community/formitable/src/lib/triggers/option-expired.ts
var optionExpired = formitableRegisterTrigger({
  name: "option_expired",
  displayName: "Booking Option Expired",
  description: "Triggers when a booking option expires.",
  event: "option.expired",
  sampleData: {
    data: {
      option: {
        uid: "opt123",
        date: "2024-01-15",
        time: "19:00",
        partySize: 4,
        status: "expired"
      }
    },
    restaurantUid: "rest123",
    event: "option.expired"
  }
});

// ../ap/packages/pieces/community/formitable/src/lib/triggers/order-ordered.ts
var orderOrdered = formitableRegisterTrigger({
  name: "order_ordered",
  displayName: "Order Placed",
  description: "Triggers when a takeaway order is made or a gift voucher is purchased.",
  event: "order.ordered",
  sampleData: {
    data: {
      order: {
        uid: "order123",
        type: "takeaway",
        total: 45.5,
        status: "ordered"
      }
    },
    restaurantUid: "rest123",
    event: "order.ordered"
  }
});

// ../ap/packages/pieces/community/formitable/src/lib/triggers/message-sent.ts
var messageSent = formitableRegisterTrigger({
  name: "message_sent",
  displayName: "Message Sent by Customer",
  description: "Triggers when a message is sent by the customer.",
  event: "message.sent",
  sampleData: {
    data: {
      message: {
        uid: "msg123",
        content: "Hello, I have a question about my reservation.",
        sentAt: "2024-01-15T10:30:00Z"
      }
    },
    restaurantUid: "rest123",
    event: "message.sent"
  }
});

// ../ap/packages/pieces/community/formitable/src/lib/triggers/message-received.ts
var messageReceived = formitableRegisterTrigger({
  name: "message_received",
  displayName: "Message Sent to Customer",
  description: "Triggers when a message is sent by the restaurant to the customer.",
  event: "message.received",
  sampleData: {
    data: {
      message: {
        uid: "msg123",
        content: "Thank you for your inquiry. Your reservation is confirmed.",
        sentAt: "2024-01-15T10:35:00Z"
      }
    },
    restaurantUid: "rest123",
    event: "message.received"
  }
});

// ../ap/packages/pieces/community/formitable/src/lib/triggers/review-created.ts
var reviewCreated = formitableRegisterTrigger({
  name: "review_created",
  displayName: "Review Created",
  description: "Triggers when a review is created.",
  event: "review.created",
  sampleData: {
    data: {
      review: {
        uid: "rev123",
        rating: 5,
        comment: "Excellent dining experience!",
        createdAt: "2024-01-16T12:00:00Z"
      }
    },
    restaurantUid: "rest123",
    event: "review.created"
  }
});

// ../ap/packages/pieces/community/formitable/src/lib/triggers/review-request.ts
var reviewRequest = formitableRegisterTrigger({
  name: "review_request",
  displayName: "Review Request",
  description: "Triggers when a review request can be sent to the customer (typically 1 day after reservation).",
  event: "review.request",
  sampleData: {
    data: {
      booking: {
        uid: "abc123",
        date: "2024-01-15",
        time: "19:00",
        partySize: 4
      }
    },
    restaurantUid: "rest123",
    event: "review.request"
  }
});

// ../ap/packages/pieces/community/formitable/src/lib/triggers/index.ts
var formitableTriggers = [
  bookingCreated,
  bookingAccepted,
  bookingChanged,
  bookingCheckin,
  bookingCheckout,
  bookingCanceled,
  bookingFailed,
  optionCreated,
  optionAccepted,
  optionCanceled,
  optionExpired,
  orderOrdered,
  messageSent,
  messageReceived,
  reviewCreated,
  reviewRequest
];

// ../ap/packages/pieces/community/formitable/src/index.ts
var markdown = `
To obtain your API key:

1. Log in to your Formitable account
2. Go to **Settings > Team**
3. Create an API Key for your user
4. Copy and paste the key here
`, formitableAuth = import_pieces_framework3.PieceAuth.SecretText({
  displayName: "API Key",
  description: markdown,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.GET,
        url: "https://api.formitable.com/api/v1.2/restaurants",
        headers: {
          ApiKey: auth,
          Accept: "application/json"
        }
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API key"
      };
    }
  }
}), formitable = (0, import_pieces_framework3.createPiece)({
  displayName: "Formitable",
  description: "Restaurant reservation and guest management platform",
  auth: formitableAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/formitable.png",
  authors: ["onyedikachi-david"],
  categories: [import_pieces_framework4.PieceCategory.SALES_AND_CRM],
  actions: [],
  triggers: formitableTriggers
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  formitable,
  formitableAuth
});
