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

// ../ap/packages/pieces/community/aidbase/src/index.ts
var index_exports = {};
__export(index_exports, {
  aidbase: () => aidbase
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework17 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/aidbase/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), aidbaseAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `
    To get your API Key:
    1. Log in to your Aidbase dashboard.
    2. Navigate to **Settings** in the sidebar.
    3. Click on the **API** section.
    4. Generate and copy your new API Key.
    `,
  required: !0,
  validate: async ({ auth }) => {
    let baseUrl = "https://api.aidbase.ai/v1";
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${baseUrl}/knowledge`,
        headers: {
          Authorization: `Bearer ${auth}`
        }
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API Key. Please check the key and try again."
      };
    }
  }
});

// ../ap/packages/pieces/community/aidbase/src/lib/triggers/email-received.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/aidbase/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/aidbase/src/lib/common/client.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), API_BASE_URL = "https://api.aidbase.ai/v1";
function getHeaders(apiKey) {
  return {
    Authorization: `Bearer ${apiKey}`,
    "Content-Type": "application/json"
  };
}
async function addVideo(apiKey, videoUrl) {
  let response = await import_pieces_common2.httpClient.sendRequest({
    method: import_pieces_common2.HttpMethod.POST,
    url: `${API_BASE_URL}/knowledge/video`,
    headers: getHeaders(apiKey),
    body: {
      video_url: videoUrl,
      video_type: "YOUTUBE"
      // This is hardcoded as per the API documentation
    }
  });
  if (response.body.success)
    return response.body.data;
  throw new Error(response.body.message || "Failed to add video to Aidbase.");
}
async function addWebsite(apiKey, websiteUrl) {
  let response = await import_pieces_common2.httpClient.sendRequest({
    method: import_pieces_common2.HttpMethod.POST,
    url: `${API_BASE_URL}/knowledge/website`,
    headers: getHeaders(apiKey),
    body: {
      website_url: websiteUrl
    }
  });
  if (response.body.success)
    return response.body.data;
  throw new Error(
    response.body.message || "Failed to add website to Aidbase."
  );
}
async function listKnowledgeItems(apiKey) {
  let response = await import_pieces_common2.httpClient.sendRequest({
    method: import_pieces_common2.HttpMethod.GET,
    url: `${API_BASE_URL}/knowledge`,
    headers: getHeaders(apiKey)
  });
  if (response.body.success && response.body.data)
    return response.body.data;
  throw new Error(
    response.body.message || "Failed to list knowledge items from Aidbase."
  );
}
async function addFaqItem(apiKey, faqId, params) {
  let response = await import_pieces_common2.httpClient.sendRequest({
    method: import_pieces_common2.HttpMethod.POST,
    url: `${API_BASE_URL}/knowledge/${faqId}/faq-item`,
    headers: getHeaders(apiKey),
    body: params
  });
  if (response.body.success)
    return response.body.data;
  throw new Error(
    response.body.message || "Failed to add FAQ item to Aidbase."
  );
}
async function createFaq(apiKey, params) {
  let response = await import_pieces_common2.httpClient.sendRequest({
    method: import_pieces_common2.HttpMethod.POST,
    url: `${API_BASE_URL}/knowledge/faq`,
    headers: getHeaders(apiKey),
    body: params
  });
  if (response.body.success)
    return response.body.data;
  throw new Error(
    response.body.message || "Failed to create FAQ in Aidbase."
  );
}
async function listChatbots(apiKey) {
  var _a;
  let response = await import_pieces_common2.httpClient.sendRequest({
    method: import_pieces_common2.HttpMethod.GET,
    url: `${API_BASE_URL}/chatbots`,
    headers: getHeaders(apiKey)
  });
  if (response.body.success && ((_a = response.body.data) != null && _a.items))
    return { items: response.body.data.items };
  throw new Error(
    response.body.message || "Failed to list chatbots from Aidbase."
  );
}
async function createChatbotReply(apiKey, chatbotId, params) {
  let response = await import_pieces_common2.httpClient.sendRequest({
    method: import_pieces_common2.HttpMethod.POST,
    url: `${API_BASE_URL}/chatbot/${chatbotId}/reply`,
    headers: getHeaders(apiKey),
    body: params
  });
  if (response.body.success)
    return response.body.data;
  throw new Error(
    response.body.message || "Failed to create chatbot reply in Aidbase."
  );
}
async function startTraining(apiKey, knowledgeId) {
  let response = await import_pieces_common2.httpClient.sendRequest({
    method: import_pieces_common2.HttpMethod.PUT,
    url: `${API_BASE_URL}/knowledge/${knowledgeId}/train`,
    headers: getHeaders(apiKey)
  });
  if (response.body.success)
    return response.body.data;
  throw new Error(
    response.body.message || "Failed to start training in Aidbase."
  );
}
var aidbaseClient = {
  addVideo,
  addWebsite,
  listKnowledgeItems,
  addFaqItem,
  createFaq,
  listChatbots,
  createChatbotReply,
  startTraining
};

// ../ap/packages/pieces/community/aidbase/src/lib/common/props.ts
var emailInboxDropdown = import_pieces_framework2.Property.Dropdown({
  auth: aidbaseAuth,
  displayName: "Email Inbox",
  description: "Select the email inbox to watch. Leave blank to trigger for all inboxes.",
  required: !1,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please connect your Aidbase account first.",
        options: []
      };
    let response = await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: "https://api.aidbase.ai/v1/email-inboxes",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    });
    return response.body.success ? {
      disabled: !1,
      options: response.body.data.items.map((inbox) => ({
        label: inbox.title,
        value: inbox.id
      }))
    } : {
      disabled: !0,
      placeholder: "Could not load inboxes.",
      options: []
    };
  }
}), ticketFormDropdown = import_pieces_framework2.Property.Dropdown({
  auth: aidbaseAuth,
  displayName: "Ticket Form",
  description: "Select the ticket form to watch. Leave blank to trigger for all forms.",
  required: !1,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please connect your Aidbase account first.",
        options: []
      };
    let response = await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: "https://api.aidbase.ai/v1/ticket-forms",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    });
    return response.body.success ? {
      disabled: !1,
      options: response.body.data.items.map((form) => ({
        label: form.title,
        value: form.public_id
      }))
    } : {
      disabled: !0,
      placeholder: "Could not load ticket forms.",
      options: []
    };
  }
}), faqDropdown = import_pieces_framework2.Property.Dropdown({
  auth: aidbaseAuth,
  displayName: "FAQ",
  description: "Select the FAQ to which the item will be added.",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => auth ? {
    disabled: !1,
    options: (await aidbaseClient.listKnowledgeItems(auth.secret_text)).items.filter((item) => item.type === "faq").map((item) => ({
      label: item.title || item.id,
      value: item.id
    }))
  } : {
    disabled: !0,
    placeholder: "Please connect your Aidbase account first.",
    options: []
  }
}), chatbotDropdown = import_pieces_framework2.Property.Dropdown({
  auth: aidbaseAuth,
  displayName: "Chatbot",
  description: "Select the chatbot that will generate the reply.",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => auth ? {
    disabled: !1,
    options: (await aidbaseClient.listChatbots(auth.secret_text)).items.map((chatbot) => ({
      label: chatbot.title,
      value: chatbot.id
    }))
  } : {
    disabled: !0,
    placeholder: "Please connect your Aidbase account first.",
    options: []
  }
}), knowledgeItemDropdown = import_pieces_framework2.Property.Dropdown({
  auth: aidbaseAuth,
  displayName: "Knowledge Item",
  description: "Select the knowledge item to train.",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please connect your Aidbase account first.",
        options: []
      };
    let response = await aidbaseClient.listKnowledgeItems(auth.secret_text), getLabel = (item) => {
      switch (item.type) {
        case "faq":
          return `[FAQ] ${item.title || item.id}`;
        case "website":
          return `[Website] ${item.base_url || item.id}`;
        case "video":
          return `[Video] ${item.title || item.id}`;
        case "document":
          return `[Document] ${item.title || item.id}`;
        default:
          return `[${item.type}] ${item.id}`;
      }
    };
    return {
      disabled: !1,
      options: response.items.map((item) => ({
        label: getLabel(item),
        value: item.id
      }))
    };
  }
});

// ../ap/packages/pieces/community/aidbase/src/lib/triggers/email-received.ts
var emailReceived = (0, import_pieces_framework3.createTrigger)({
  auth: aidbaseAuth,
  name: "email_received",
  classification: "READ",
  displayName: "Email Received",
  description: "Fires when a new email is received in an Aidbase inbox.",
  aiMetadata: {
    description: "Fires when a customer email arrives in an Aidbase email inbox, representing a new inbound support message. Can be scoped to a specific inbox or left open to fire for any inbox."
  },
  props: {
    inbox_id: emailInboxDropdown
  },
  sampleData: {
    id: "evt_1Hc2a8f36qsa23e",
    type: "email.received",
    data: {
      id: "7c3c5609-b838-4643-8e8c-2a2da0a14c4a",
      email_inbox_id: "286d123e-5961-457c-8fd5-56f192ec315d",
      topic: "Question about something",
      session_data: {
        username: "John Doe",
        email: "john@doe.com"
      }
    }
  },
  type: import_pieces_framework3.TriggerStrategy.WEBHOOK,
  // onEnable is not needed because the user manually creates the webhook in the Aidbase UI.
  async onEnable(context) {
  },
  // onDisable is not needed because the webhook is managed outside of Activepieces.
  async onDisable(context) {
  },
  async run(context) {
    let payloadBody = context.payload.body, selectedInboxId = context.propsValue.inbox_id;
    return payloadBody.type !== "email.received" ? [] : selectedInboxId && payloadBody.data.email_inbox_id !== selectedInboxId ? [] : [payloadBody];
  }
});

// ../ap/packages/pieces/community/aidbase/src/lib/triggers/email-status-changed.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var emailStatusChanged = (0, import_pieces_framework4.createTrigger)({
  auth: aidbaseAuth,
  name: "email_status_changed",
  classification: "READ",
  displayName: "Email Status Changed",
  description: "Fires when the status of an email changes.",
  aiMetadata: {
    description: "Fires when an Aidbase email transitions to a new status (e.g. Opened, Assigned, Need More Info, Resolved, Closed), representing a workflow state change on a support email. Can be filtered to a specific target status and/or inbox, or left open to fire on any status change."
  },
  props: {
    inbox_id: emailInboxDropdown,
    status: import_pieces_framework4.Property.StaticDropdown({
      displayName: "New Status",
      description: "Select the status to trigger on. Leave blank to trigger for any status change.",
      required: !1,
      options: {
        options: [
          { label: "Opened", value: "OPEN" },
          { label: "Assigned", value: "ASSIGNED" },
          { label: "Need More Info", value: "NEED_MORE_INFO" },
          { label: "Resolved", value: "RESOLVED" },
          { label: "Closed", value: "CLOSED" }
        ]
      }
    })
  },
  sampleData: {
    id: "evt_1Hc2a8f36qsa23e",
    webhook_id: "c1042e89-fea5-415c-8bd2-e557abf25307",
    object: "webhook_event",
    created: "2023-10-31T17:36:56.491Z",
    type: "email.status.changed",
    changes: {
      status: {
        previous: "ASSIGNED",
        new: "RESOLVED"
      }
    },
    data: {
      id: "7c3c5609-b838-4643-8e8c-2a2da0a14c4a",
      email_inbox_id: "286d123e-5961-457c-8fd5-56f192ec315d",
      status: "RESOLVED"
    }
  },
  type: import_pieces_framework4.TriggerStrategy.WEBHOOK,
  // onEnable is not needed because the user manually creates the webhook in the Aidbase UI.
  async onEnable(context) {
  },
  // onDisable is not needed because the webhook is managed outside of Activepieces.
  async onDisable(context) {
  },
  async run(context) {
    let payloadBody = context.payload.body, { inbox_id, status } = context.propsValue;
    return payloadBody.type !== "email.status.changed" ? [] : inbox_id && payloadBody.data.email_inbox_id !== inbox_id ? [] : status && payloadBody.changes.status.new !== status ? [] : [payloadBody];
  }
});

// ../ap/packages/pieces/community/aidbase/src/lib/triggers/email-priority-changed.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var emailPriorityChanged = (0, import_pieces_framework5.createTrigger)({
  auth: aidbaseAuth,
  name: "email_priority_changed",
  classification: "READ",
  displayName: "Email Priority Changed",
  description: "Fires when an email\u2019s priority is changed.",
  aiMetadata: {
    description: "Fires when an Aidbase email\u2019s priority changes (Low, Medium, High), representing a re-prioritization of a support email. Can be filtered to a specific target priority and/or inbox, or left open to fire on any priority change."
  },
  props: {
    inbox_id: emailInboxDropdown,
    priority: import_pieces_framework5.Property.StaticDropdown({
      displayName: "New Priority",
      description: "Select the priority to trigger on. Leave blank to trigger for any priority change.",
      required: !1,
      options: {
        options: [
          { label: "Low", value: "LOW" },
          { label: "Medium", value: "MEDIUM" },
          { label: "High", value: "HIGH" }
        ]
      }
    })
  },
  sampleData: {
    id: "evt_1Hc2a8f36qsa23e",
    type: "email.priority.changed",
    changes: {
      priority: {
        previous: "MEDIUM",
        new: "HIGH"
      }
    },
    data: {
      id: "7c3c5609-b838-4643-8e8c-2a2da0a14c4a",
      email_inbox_id: "286d123e-5961-457c-8fd5-56f192ec315d",
      priority: "HIGH"
    }
  },
  type: import_pieces_framework5.TriggerStrategy.WEBHOOK,
  // onEnable is not needed due to manual webhook setup in the Aidbase Dashboard.
  async onEnable(context) {
  },
  // onDisable is not needed as Activepieces does not manage the webhook lifecycle.
  async onDisable(context) {
  },
  async run(context) {
    let payloadBody = context.payload.body, { inbox_id, priority } = context.propsValue;
    return payloadBody.type !== "email.priority.changed" ? [] : inbox_id && payloadBody.data.email_inbox_id !== inbox_id ? [] : priority && payloadBody.changes.priority.new !== priority ? [] : [payloadBody];
  }
});

// ../ap/packages/pieces/community/aidbase/src/lib/triggers/email-sent.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var emailSent = (0, import_pieces_framework6.createTrigger)({
  auth: aidbaseAuth,
  name: "email_sent",
  classification: "READ",
  displayName: "Email Sent",
  description: "Fires when an email is sent from Aidbase.",
  aiMetadata: {
    description: "Fires when an outbound email is sent from Aidbase (e.g. an agent reply on a support thread), representing a new outgoing message. Can be scoped to a specific inbox or left open to fire for any inbox."
  },
  props: {
    inbox_id: emailInboxDropdown
  },
  sampleData: {
    id: "evt_1Hc2a8f36qsa23e",
    webhook_id: "c1042e89-fea5-415c-8bd2-e557abf25307",
    object: "webhook_event",
    created: "2023-10-31T17:36:56.491Z",
    type: "email.sent",
    changes: {
      message: {
        id: "00bcde85-2b4d-4682-ac8b-b280dc819559",
        type: "AGENT",
        created: "2023-10-31T17:39:56.491Z",
        message: "Hi John, than you for your message...",
        session_data: {
          id: "2b06ada3f5bb",
          username: "jamesdoe",
          email: "james@doe.com"
        }
      }
    },
    data: {
      id: "7c3c5609-b838-4643-8e8c-2a2da0a14c4a",
      email_inbox_id: "286d123e-5961-457c-8fd5-56f192ec315d",
      status: "OPEN"
    }
  },
  type: import_pieces_framework6.TriggerStrategy.WEBHOOK,
  // onEnable is not needed due to manual webhook setup in the Aidbase Dashboard.
  async onEnable(context) {
  },
  // onDisable is not needed as Activepieces does not manage the webhook lifecycle.
  async onDisable(context) {
  },
  async run(context) {
    let payloadBody = context.payload.body, { inbox_id } = context.propsValue;
    return payloadBody.type !== "email.sent" ? [] : inbox_id && payloadBody.data.email_inbox_id !== inbox_id ? [] : [payloadBody];
  }
});

// ../ap/packages/pieces/community/aidbase/src/lib/triggers/ticket-created.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var ticketCreated = (0, import_pieces_framework7.createTrigger)({
  auth: aidbaseAuth,
  name: "ticket_created",
  classification: "READ",
  displayName: "Ticket Created",
  description: "Fires when a new ticket is created in Aidbase.",
  aiMetadata: {
    description: "Fires when a new support ticket is submitted in Aidbase, representing a fresh customer request. Can be scoped to a specific ticket form or left open to fire for any form."
  },
  props: {
    ticket_form_id: ticketFormDropdown
  },
  sampleData: {
    id: "evt_1Hc2X2JZ6qsJ5XQ",
    type: "ticket.created",
    data: {
      id: "a36306fa-0cc4-4497-8c2a-c7798cfdc720",
      ticket_form_id: "I2o6Ii_4U6m48bmKTTMoR",
      status: "OPEN",
      priority: "MEDIUM",
      field_values: [
        { id: "uv9xA", name: "Your name", type: "TEXT", value: "John Doe" }
      ]
    }
  },
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  // onEnable is not needed due to manual webhook setup in the Aidbase Dashboard.
  async onEnable(context) {
  },
  // onDisable is not needed as Activepieces does not manage the webhook lifecycle.
  async onDisable(context) {
  },
  async run(context) {
    let payloadBody = context.payload.body, { ticket_form_id } = context.propsValue;
    return payloadBody.type !== "ticket.created" ? [] : ticket_form_id && payloadBody.data.ticket_form_id !== ticket_form_id ? [] : [payloadBody];
  }
});

// ../ap/packages/pieces/community/aidbase/src/lib/triggers/ticket-priority-changed.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var ticketPriorityChanged = (0, import_pieces_framework8.createTrigger)({
  auth: aidbaseAuth,
  name: "ticket_priority_changed",
  classification: "READ",
  displayName: "Ticket Priority Changed",
  description: "Fires when the priority of an existing ticket changes.",
  aiMetadata: {
    description: "Fires when an existing Aidbase ticket\u2019s priority changes (Low, Medium, High), representing a re-prioritization of a support ticket. Can be filtered to a specific target priority and/or ticket form, or left open to fire on any priority change."
  },
  props: {
    ticket_form_id: ticketFormDropdown,
    priority: import_pieces_framework8.Property.StaticDropdown({
      displayName: "New Priority",
      description: "Select the priority to trigger on. Leave blank to trigger for any priority change.",
      required: !1,
      options: {
        options: [
          { label: "Low", value: "LOW" },
          { label: "Medium", value: "MEDIUM" },
          { label: "High", value: "HIGH" }
        ]
      }
    })
  },
  sampleData: {
    id: "evt_1Hc2X2JZ6qsJ5XQ",
    type: "ticket.priority.changed",
    changes: {
      priority: {
        previous: "MEDIUM",
        new: "HIGH"
      }
    },
    data: {
      id: "a36306fa-0cc4-4497-8c2a-c7798cfdc720",
      ticket_form_id: "I2o6Ii_4U6m48bmKTTMoR",
      status: "OPEN",
      priority: "HIGH"
    }
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  // onEnable is not needed due to manual webhook setup in the Aidbase Dashboard.
  async onEnable(context) {
  },
  // onDisable is not needed as Activepieces does not manage the webhook lifecycle.
  async onDisable(context) {
  },
  async run(context) {
    let payloadBody = context.payload.body, { ticket_form_id, priority } = context.propsValue;
    return payloadBody.type !== "ticket.priority.changed" ? [] : ticket_form_id && payloadBody.data.ticket_form_id !== ticket_form_id ? [] : priority && payloadBody.changes.priority.new !== priority ? [] : [payloadBody];
  }
});

// ../ap/packages/pieces/community/aidbase/src/lib/triggers/ticket-status-changed.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var ticketStatusChanged = (0, import_pieces_framework9.createTrigger)({
  auth: aidbaseAuth,
  name: "ticket_status_changed",
  classification: "READ",
  displayName: "Ticket Status Changed",
  description: "Fires when a ticket\u2019s overall status changes.",
  aiMetadata: {
    description: "Fires when an Aidbase ticket transitions to a new status (e.g. Open, Assigned, Need More Info, Resolved, Closed), representing a workflow state change on a support ticket. Can be filtered to a specific target status and/or ticket form, or left open to fire on any status change."
  },
  props: {
    ticket_form_id: ticketFormDropdown,
    status: import_pieces_framework9.Property.StaticDropdown({
      displayName: "New Status",
      description: "Select the status to trigger on. Leave blank to trigger for any status change.",
      required: !1,
      options: {
        options: [
          { label: "Open", value: "OPEN" },
          { label: "Assigned", value: "ASSIGNED" },
          { label: "Need More Info", value: "NEED_MORE_INFO" },
          { label: "Resolved", value: "RESOLVED" },
          { label: "Closed", value: "CLOSED" }
        ]
      }
    })
  },
  sampleData: {
    id: "evt_1Hc2X2JZ6qsJ5XQ",
    type: "ticket.status.changed",
    changes: {
      status: {
        previous: "ASSIGNED",
        new: "RESOLVED"
      }
    },
    data: {
      id: "a36306fa-0cc4-4497-8c2a-c7798cfdc720",
      ticket_form_id: "I2o6Ii_4U6m48bmKTTMoR",
      status: "RESOLVED"
    }
  },
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  // onEnable is not needed due to manual webhook setup in the Aidbase Dashboard.
  async onEnable(context) {
  },
  // onDisable is not needed as Activepieces does not manage the webhook lifecycle.
  async onDisable(context) {
  },
  async run(context) {
    let payloadBody = context.payload.body, { ticket_form_id, status } = context.propsValue;
    return console.log(payloadBody), payloadBody.type !== "ticket.status.changed" ? [] : ticket_form_id && payloadBody.data.ticket_form_id !== ticket_form_id ? [] : status && payloadBody.changes.status.new !== status ? [] : [payloadBody];
  }
});

// ../ap/packages/pieces/community/aidbase/src/lib/triggers/ticket-new-comment.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var ticketNewComment = (0, import_pieces_framework10.createTrigger)({
  auth: aidbaseAuth,
  name: "ticket_new_comment",
  classification: "READ",
  displayName: "Ticket New Comment",
  description: "Fires when a new comment is added to an existing ticket.",
  aiMetadata: {
    description: "Fires when a new comment is posted on an existing Aidbase ticket, representing an added reply or note on a support thread. Can be filtered by commenter type (customer User vs. support Agent) and/or ticket form, or left open to fire for all comments."
  },
  props: {
    ticket_form_id: ticketFormDropdown,
    commenter_type: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Commenter Type",
      description: "Select the type of commenter to trigger on. Leave blank for all comments.",
      required: !1,
      options: {
        options: [
          { label: "User (Customer)", value: "USER" },
          { label: "Agent (Support Staff)", value: "AGENT" }
        ]
      }
    })
  },
  sampleData: {
    id: "evt_1Hc2X2JZ6qsJ5XQ",
    type: "ticket.new.comment",
    changes: {
      comment: {
        id: "443968de-64d8-4018-9185-ffb9afb4751d",
        type: "AGENT",
        comment: "Great question! Here is the answer..."
      }
    },
    data: {
      id: "a36306fa-0cc4-4497-8c2a-c7798cfdc720",
      ticket_form_id: "I2o6Ii_4U6m48bmKTTMoR",
      status: "OPEN"
    }
  },
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  // onEnable is not needed due to manual webhook setup in the Aidbase Dashboard.
  async onEnable(context) {
  },
  // onDisable is not needed as Activepieces does not manage the webhook lifecycle.
  async onDisable(context) {
  },
  async run(context) {
    let payloadBody = context.payload.body, { ticket_form_id, commenter_type } = context.propsValue;
    return payloadBody.type !== "ticket.new.comment" ? [] : ticket_form_id && payloadBody.data.ticket_form_id !== ticket_form_id ? [] : commenter_type && payloadBody.changes.comment.type !== commenter_type ? [] : [payloadBody];
  }
});

// ../ap/packages/pieces/community/aidbase/src/lib/actions/add-video.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var addVideo2 = (0, import_pieces_framework11.createAction)({
  auth: aidbaseAuth,
  name: "add_video",
  classification: "WRITE",
  displayName: "Add Video",
  description: "Adds a YouTube video URL as knowledge to the Aidbase knowledge base.",
  audience: "both",
  aiMetadata: {
    description: "Registers a YouTube video as a new knowledge source in Aidbase so its content can later be trained on and answered from. Use when ingesting video content into the knowledge base; requires a YouTube video URL. Not idempotent: each call creates a new knowledge source even for the same URL.",
    idempotent: !1
  },
  props: {
    video_url: import_pieces_framework11.Property.ShortText({
      displayName: "YouTube Video URL",
      description: "The URL of the YouTube video to add as a knowledge source.",
      required: !0
    })
  },
  async run(context) {
    let { auth: apiKey, propsValue } = context, { video_url } = propsValue;
    return await aidbaseClient.addVideo(apiKey.secret_text, video_url);
  }
});

// ../ap/packages/pieces/community/aidbase/src/lib/actions/add-website.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var addWebsite2 = (0, import_pieces_framework12.createAction)({
  auth: aidbaseAuth,
  name: "add_website",
  classification: "WRITE",
  displayName: "Add Website",
  description: "Adds a website URL as a knowledge source for Aidbase.",
  audience: "both",
  aiMetadata: {
    description: "Registers a website URL as a new knowledge source in Aidbase so its content can later be trained on and answered from. Use when ingesting a web page or site into the knowledge base; requires the website URL. Not idempotent: each call creates a new knowledge source even for the same URL.",
    idempotent: !1
  },
  props: {
    website_url: import_pieces_framework12.Property.ShortText({
      displayName: "Website URL",
      description: "The URL of the website to add as a knowledge source (e.g., https://www.example.com).",
      required: !0
    })
  },
  async run(context) {
    let { auth: apiKey, propsValue } = context, { website_url } = propsValue;
    return await aidbaseClient.addWebsite(apiKey.secret_text, website_url);
  }
});

// ../ap/packages/pieces/community/aidbase/src/lib/actions/add-faq-item.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var addFaqItem2 = (0, import_pieces_framework13.createAction)({
  auth: aidbaseAuth,
  name: "add_faq_item",
  classification: "WRITE",
  displayName: "Add FAQ Item",
  description: "Adds a new question/answer item to an existing FAQ; supports categories.",
  audience: "both",
  aiMetadata: {
    description: "Appends a question/answer entry to an existing FAQ knowledge base in Aidbase, optionally tagging it with category names (new categories are created on the fly). Use to populate an FAQ already created via Create FAQ; requires the target FAQ id plus question and answer text. Not idempotent: each call appends a new item even if the question/answer is identical.",
    idempotent: !1
  },
  props: {
    faq_id: faqDropdown,
    question: import_pieces_framework13.Property.ShortText({
      displayName: "Question",
      required: !0
    }),
    answer: import_pieces_framework13.Property.LongText({
      displayName: "Answer",
      required: !0
    }),
    source_url: import_pieces_framework13.Property.ShortText({
      displayName: "Source URL",
      description: "An optional URL for the source of the information.",
      required: !1
    }),
    categories: import_pieces_framework13.Property.Array({
      displayName: "Categories",
      description: "A list of category names. New categories will be created if they do not exist.",
      required: !1
    })
  },
  async run(context) {
    let { auth: apiKey, propsValue } = context, { faq_id, question, answer, source_url, categories } = propsValue;
    return await aidbaseClient.addFaqItem(apiKey.secret_text, faq_id, {
      question,
      answer,
      source_url,
      categories
    });
  }
});

// ../ap/packages/pieces/community/aidbase/src/lib/actions/create-faq.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var createFaq2 = (0, import_pieces_framework14.createAction)({
  auth: aidbaseAuth,
  name: "create_faq",
  classification: "WRITE",
  displayName: "Create FAQ",
  description: "Creates a new FAQ entry with title and description.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new, empty FAQ knowledge base in Aidbase identified by a title (description optional). Use this first to obtain an FAQ id before adding entries with Add FAQ Item. Not idempotent: each call creates a separate FAQ even with the same title.",
    idempotent: !1
  },
  props: {
    title: import_pieces_framework14.Property.ShortText({
      displayName: "Title",
      description: "The title of the new FAQ knowledge base.",
      required: !0
    }),
    description: import_pieces_framework14.Property.LongText({
      displayName: "Description",
      description: "An optional description for the FAQ.",
      required: !1
    })
  },
  async run(context) {
    let { auth: apiKey, propsValue } = context, { title, description } = propsValue;
    return await aidbaseClient.createFaq(apiKey.secret_text, {
      title,
      description
    });
  }
});

// ../ap/packages/pieces/community/aidbase/src/lib/actions/create-chatbot-reply.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var createChatbotReply2 = (0, import_pieces_framework15.createAction)({
  auth: aidbaseAuth,
  name: "create_chatbot_reply",
  classification: "WRITE",
  displayName: "Create Chatbot Reply",
  description: "Generates an AI chatbot reply given a message and optional session context.",
  audience: "both",
  aiMetadata: {
    description: "Sends a message to a specified Aidbase chatbot and returns its AI-generated reply. Pass a session id to continue an existing conversation, or omit it to start a new one. Use to get an automated support answer for a user message; requires the chatbot id and the message text. Not idempotent: each call generates a fresh reply and advances the conversation.",
    idempotent: !1
  },
  props: {
    chatbot_id: chatbotDropdown,
    message: import_pieces_framework15.Property.LongText({
      displayName: "Message",
      description: "The message or question to send to the chatbot.",
      required: !0
    }),
    session_id: import_pieces_framework15.Property.ShortText({
      displayName: "Session ID",
      description: "An optional session ID to maintain conversation context. If omitted, a new conversation will start.",
      required: !1
    })
  },
  async run(context) {
    let { auth: apiKey, propsValue } = context, { chatbot_id, message, session_id } = propsValue;
    return await aidbaseClient.createChatbotReply(apiKey.secret_text, chatbot_id, {
      message,
      session_id
    });
  }
});

// ../ap/packages/pieces/community/aidbase/src/lib/actions/start-training.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var startTraining2 = (0, import_pieces_framework16.createAction)({
  auth: aidbaseAuth,
  name: "start_training",
  classification: "WRITE",
  displayName: "Start Training",
  description: "Starts a training job on an existing knowledge base item (FAQ, website, video, etc.).",
  audience: "both",
  aiMetadata: {
    description: "Triggers a training job for a single existing Aidbase knowledge base item (FAQ, website, video, etc.) so its content becomes answerable by the chatbot. Use after adding a knowledge source; requires the knowledge item id. Not idempotent: each call enqueues another training run.",
    idempotent: !1
  },
  props: {
    knowledge_id: knowledgeItemDropdown
  },
  async run({ auth: apiKey, propsValue }) {
    let { knowledge_id } = propsValue;
    return await aidbaseClient.startTraining(apiKey.secret_text, knowledge_id);
  }
});

// ../ap/packages/pieces/community/aidbase/src/index.ts
var import_pieces_common4 = require("@activepieces/pieces-common");
var import_pieces_framework18 = require("@activepieces/pieces-framework"), aidbase = (0, import_pieces_framework17.createPiece)({
  displayName: "Aidbase",
  auth: aidbaseAuth,
  minimumSupportedRelease: "0.36.1",
  categories: [import_pieces_framework18.PieceCategory.CUSTOMER_SUPPORT, import_pieces_framework18.PieceCategory.COMMUNICATION],
  logoUrl: "https://cdn.activepieces.com/pieces/aidbase.png",
  authors: ["Prabhukiran161", "sanket-a11y"],
  actions: [
    addVideo2,
    addWebsite2,
    addFaqItem2,
    createFaq2,
    createChatbotReply2,
    startTraining2,
    (0, import_pieces_common4.createCustomApiCallAction)({
      auth: aidbaseAuth,
      baseUrl: () => API_BASE_URL,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [
    emailReceived,
    emailStatusChanged,
    emailPriorityChanged,
    emailSent,
    ticketCreated,
    ticketPriorityChanged,
    ticketStatusChanged,
    ticketNewComment
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  aidbase
});
