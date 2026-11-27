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

// ../ap/packages/pieces/community/kallabot-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  kallabotAi: () => kallabotAi,
  kallabotAuth: () => kallabotAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_framework12 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/kallabot-ai/src/lib/actions/make-call.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var makeCallAction = (0, import_pieces_framework.createAction)({
  name: "make-call",
  displayName: "Make Call",
  description: "Initiate an outbound call using Kallabot AI agent.",
  audience: "both",
  aiMetadata: {
    description: "Place a single outbound phone call where a Kallabot AI voice agent talks to the recipient. Use it for one-off calls (not bulk campaigns); requires an agent ID, a recipient number, and one of your account sender numbers, all in E.164 format. Each call placed is a new side effect, so this is not idempotent.",
    idempotent: !1
  },
  auth: kallabotAuth,
  props: {
    agent_id: import_pieces_framework.Property.Dropdown({
      displayName: "Agent",
      description: "Select the AI agent to use for the call.",
      required: !0,
      refreshers: ["auth"],
      auth: kallabotAuth,
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        try {
          let agents = (await import_pieces_common.httpClient.sendRequest({
            method: import_pieces_common.HttpMethod.GET,
            url: "https://api.kallabot.com/agents",
            headers: {
              Authorization: `Bearer ${auth.secret_text}`,
              "Content-Type": "application/json"
            }
          })).body;
          if (agents && typeof agents == "object") {
            if (agents.data && Array.isArray(agents.data))
              agents = agents.data;
            else if (agents.agents && Array.isArray(agents.agents))
              agents = agents.agents;
            else if (!Array.isArray(agents))
              return {
                disabled: !0,
                options: [],
                placeholder: "Invalid response format from API"
              };
          }
          return Array.isArray(agents) ? {
            options: agents.filter((agent) => agent.call_direction !== "inbound").map((agent) => {
              let agentId = agent.agent_id || agent.id, agentName = agent.name || "Unnamed Agent";
              return {
                label: `(${agentId ? agentId.substring(0, 8) + "..." : "xxxxxxxx..."}) ${agentName}`,
                value: agentId
              };
            })
          } : {
            disabled: !0,
            options: [],
            placeholder: "Invalid agents data format"
          };
        } catch (error) {
          return console.error("Error fetching agents:", error), {
            disabled: !0,
            options: [],
            placeholder: `Error loading agents: ${error instanceof Error ? error.message : "Network error"}`
          };
        }
      }
    }),
    recipient_phone_number: import_pieces_framework.Property.ShortText({
      displayName: "Recipient Phone Number",
      description: "The phone number to call in E.164 format (e.g., +1234567890).",
      required: !0
    }),
    sender_phone_number: import_pieces_framework.Property.Dropdown({
      displayName: "Sender Phone Number",
      description: "The phone number to make the call from.",
      required: !0,
      refreshers: ["auth"],
      auth: kallabotAuth,
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        try {
          let phoneNumbers = (await import_pieces_common.httpClient.sendRequest({
            method: import_pieces_common.HttpMethod.GET,
            url: "https://api.kallabot.com/account-phone-numbers",
            headers: {
              Authorization: `Bearer ${auth.secret_text}`,
              "Content-Type": "application/json"
            }
          })).body;
          if (phoneNumbers && typeof phoneNumbers == "object") {
            if (phoneNumbers.data && Array.isArray(phoneNumbers.data))
              phoneNumbers = phoneNumbers.data;
            else if (phoneNumbers.phone_numbers && Array.isArray(phoneNumbers.phone_numbers))
              phoneNumbers = phoneNumbers.phone_numbers;
            else if (!Array.isArray(phoneNumbers))
              return console.warn("Phone numbers response is not an array:", phoneNumbers), {
                disabled: !0,
                options: [],
                placeholder: "Invalid response format from API"
              };
          }
          return Array.isArray(phoneNumbers) ? {
            options: phoneNumbers.map((phone) => {
              let phoneNumber = phone.phone_number || phone.number || "Unknown Number", friendlyName = phone.friendly_name || phone.name || phoneNumber;
              return {
                label: `(${phoneNumber}) ${friendlyName}`,
                value: phoneNumber
              };
            })
          } : (console.warn("Phone numbers is not an array after processing:", phoneNumbers), {
            disabled: !0,
            options: [],
            placeholder: "Invalid phone numbers data format"
          });
        } catch (error) {
          return console.error("Error fetching phone numbers:", error), {
            disabled: !0,
            options: [],
            placeholder: "Error loading phone numbers"
          };
        }
      }
    }),
    template_variables: import_pieces_framework.Property.Object({
      displayName: "Template Variables",
      description: "Variables to be used in the agent template (optional).",
      required: !1
    }),
    webhook_url: import_pieces_framework.Property.ShortText({
      displayName: "Webhook URL",
      description: "URL to receive call status updates (optional).",
      required: !1
    })
  },
  async run(context) {
    let { agent_id, recipient_phone_number, sender_phone_number, template_variables, webhook_url } = context.propsValue, requestBody = {
      agent_id,
      recipient_phone_number,
      sender_phone_number
    };
    template_variables && (requestBody.template_variables = template_variables), webhook_url && (requestBody.webhook_url = webhook_url);
    try {
      let response = await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.POST,
        url: "https://api.kallabot.com/call",
        headers: {
          Authorization: `Bearer ${context.auth}`,
          "Content-Type": "application/json"
        },
        body: requestBody
      });
      return {
        success: !0,
        call_id: response.body.call_id || response.body.id,
        status: response.body.status || "initiated",
        message: "Call initiated successfully",
        response: response.body
      };
    } catch (error) {
      console.error("Error making call:", error);
      let errorMessage = "Failed to initiate call";
      if (error && typeof error == "object") {
        let err = error;
        err.response && err.response.body ? errorMessage = err.response.body.message || err.response.body.error || errorMessage : err.message && (errorMessage = err.message);
      }
      throw new Error(`Failed to initiate call: ${errorMessage}`);
    }
  }
});

// ../ap/packages/pieces/community/kallabot-ai/src/lib/actions/get-call-details.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var getCallDetailsAction = (0, import_pieces_framework2.createAction)({
  name: "get-call-details",
  displayName: "Get Call Details",
  description: "Retrieve details of a specific call by call SID.",
  audience: "both",
  aiMetadata: {
    description: "Look up the full record of one past or in-progress call by its call SID, including status, duration, cost, recording URL, and transcription. Use it to inspect or report on a known call. Read-only and idempotent.",
    idempotent: !0
  },
  auth: kallabotAuth,
  props: {
    call_sid: import_pieces_framework2.Property.ShortText({
      displayName: "Call SID",
      description: "The unique identifier of the call.",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.GET,
      url: `https://api.kallabot.com/call-details/${context.propsValue.call_sid}`,
      headers: {
        Authorization: `Bearer ${context.auth.secret_text}`,
        "Content-Type": "application/json"
      }
    }), mockResponse = {
      call_sid: context.propsValue.call_sid,
      agent_id: "agent-123e4567-e89b-12d3-a456-426614174000",
      account_id: "account-123e4567-e89b-12d3-a456-426614174000",
      from_number: "+1234567890",
      to_number: "+0987654321",
      duration: 120.5,
      recording_url: "https://api.twilio.com/2010-04-01/Accounts/AC123/Recordings/RE123.wav",
      transcription: {
        conversation: [
          {
            speaker: "agent",
            message: "Hello! This is regarding your recent order cancellation. How can I help you today?",
            timestamp: "2024-01-15T10:30:05Z"
          },
          {
            speaker: "customer",
            message: "Hi, yes I cancelled my order because I found a better price elsewhere.",
            timestamp: "2024-01-15T10:30:15Z"
          },
          {
            speaker: "agent",
            message: "I understand. Would you be interested if I could match that price for you?",
            timestamp: "2024-01-15T10:30:25Z"
          }
        ],
        sentiment: "neutral",
        summary: "Customer cancelled order due to price. Interested in price matching discussion."
      },
      status: "completed",
      call_type: "outbound",
      cost: 0.25,
      created_at: "2024-01-15T10:30:00Z",
      transferred: !1,
      transfer_info: null
    };
    return process.env.NODE_ENV === "development" ? mockResponse : response.body;
  }
});

// ../ap/packages/pieces/community/kallabot-ai/src/lib/actions/add-contact-to-list.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var addContactToListAction = (0, import_pieces_framework3.createAction)({
  name: "add-contact-to-list",
  displayName: "Add Contacts to List",
  description: "Add a contact or multiple contacts to an existing contact list.",
  audience: "both",
  aiMetadata: {
    description: "Append one or more contacts to an existing contact list (by list ID); each contact requires a phone_number and may include a name and template_variables. Use it to grow a list without touching existing members; use Edit Contact List to fully replace the list instead. Not idempotent \u2014 repeating the call appends the contacts again.",
    idempotent: !1
  },
  auth: kallabotAuth,
  props: {
    list_id: import_pieces_framework3.Property.Dropdown({
      auth: kallabotAuth,
      displayName: "Contact List",
      description: "Select the contact list to add contacts to.",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        try {
          let response = await import_pieces_common3.httpClient.sendRequest({
            method: import_pieces_common3.HttpMethod.GET,
            url: "https://api.kallabot.com/contacts/lists",
            headers: {
              Authorization: `Bearer ${auth.secret_text}`,
              "Content-Type": "application/json"
            }
          }), lists = [];
          if (Array.isArray(response.body))
            lists = response.body;
          else if (response.body && Array.isArray(response.body.data))
            lists = response.body.data;
          else if (response.body && Array.isArray(response.body.lists))
            lists = response.body.lists;
          else if (response.body && Array.isArray(response.body.contact_lists))
            lists = response.body.contact_lists;
          else
            return console.error("Unexpected API response structure:", response.body), {
              disabled: !0,
              options: [],
              placeholder: "Invalid response format from API"
            };
          return Array.isArray(lists) ? {
            options: lists.map((list) => {
              let listId = list.list_id || list.id, listName = list.name || "Unnamed List";
              return {
                label: `(${listId ? listId.substring(0, 8) + "..." : "xxxxxxxx..."}) ${listName} (${list.contact_count || 0} contacts)`,
                value: listId
              };
            })
          } : (console.error("Lists is not an array:", lists), {
            disabled: !0,
            options: [],
            placeholder: "Invalid data format"
          });
        } catch (error) {
          return console.error("Error fetching contact lists:", error), {
            disabled: !0,
            options: [],
            placeholder: "Error loading contact lists"
          };
        }
      }
    }),
    contacts: import_pieces_framework3.Property.Json({
      displayName: "Contacts JSON",
      description: "JSON array of contacts to add to the list. Each contact must have phone_number and can include name and template_variables.",
      required: !0,
      defaultValue: [
        {
          phone_number: "+1234567890",
          name: "John Smith",
          template_variables: {
            first_name: "John",
            last_name: "Smith",
            company: "Acme Corp",
            custom_field: "value"
          }
        },
        {
          phone_number: "+1987654321",
          name: "Jane Doe",
          template_variables: {
            first_name: "Jane",
            last_name: "Doe",
            company: "Tech Solutions",
            priority: "high"
          }
        }
      ]
    })
  },
  async run(context) {
    let contacts = context.propsValue.contacts || [];
    if (!Array.isArray(contacts) || contacts.length === 0)
      throw new Error("At least one contact must be provided");
    let payload = {
      contacts
    };
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: `https://api.kallabot.com/contacts/${context.propsValue.list_id}/add`,
      headers: {
        Authorization: `Bearer ${context.auth}`,
        "Content-Type": "application/json"
      },
      body: payload
    })).body;
  }
});

// ../ap/packages/pieces/community/kallabot-ai/src/lib/actions/create-contact-list.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var createContactListAction = (0, import_pieces_framework4.createAction)({
  name: "create-contact-list",
  displayName: "Create Contact List",
  description: "Create a new contact list.",
  audience: "both",
  aiMetadata: {
    description: "Create a new named contact list seeded with an array of contacts, each requiring a phone_number and optionally a name and template_variables; an optional description can be set. Use it to set up a fresh list for calling or campaigns. Not idempotent \u2014 each call creates a separate new list even with identical input.",
    idempotent: !1
  },
  auth: kallabotAuth,
  props: {
    name: import_pieces_framework4.Property.ShortText({
      displayName: "List Name",
      description: "The name of the contact list.",
      required: !0
    }),
    description: import_pieces_framework4.Property.LongText({
      displayName: "Description",
      description: "A description of the contact list (optional).",
      required: !1
    }),
    contacts: import_pieces_framework4.Property.Json({
      displayName: "Contacts JSON",
      description: "JSON array of contacts. Each contact must have phone_number and can include name, template_variables.",
      required: !0,
      defaultValue: [
        {
          phone_number: "+1234567890",
          name: "John Smith",
          template_variables: {
            first_name: "John",
            last_name: "Smith",
            company: "Acme Corp",
            custom_field: "value"
          }
        },
        {
          phone_number: "+1987654321",
          name: "Jane Doe",
          template_variables: {
            first_name: "Jane",
            last_name: "Doe",
            company: "Tech Solutions",
            priority: "high"
          }
        }
      ]
    })
  },
  async run(context) {
    let payload = {
      name: context.propsValue.name,
      contacts: context.propsValue.contacts || []
    };
    return context.propsValue.description && (payload.description = context.propsValue.description), (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: "https://api.kallabot.com/contacts",
      headers: {
        Authorization: `Bearer ${context.auth.secret_text}`,
        "Content-Type": "application/json"
      },
      body: payload
    })).body;
  }
});

// ../ap/packages/pieces/community/kallabot-ai/src/lib/actions/create-campaign.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var createCampaignAction = (0, import_pieces_framework5.createAction)({
  name: "create-campaign",
  displayName: "Create Campaign",
  description: "Create a new outbound calling campaign.",
  audience: "both",
  aiMetadata: {
    description: "Schedule a bulk outbound calling campaign that dials every contact in a list using a chosen AI agent and one or more sender numbers. Use it for mass calling rather than a single Make Call; requires an agent ID, a contact list ID, sender numbers, and a scheduled start time, with optional pacing controls (delay between calls, number rotation, timezone). Not idempotent \u2014 each call creates a separate campaign.",
    idempotent: !1
  },
  auth: kallabotAuth,
  props: {
    name: import_pieces_framework5.Property.ShortText({
      displayName: "Campaign Name",
      description: "The name of the campaign.",
      required: !0
    }),
    description: import_pieces_framework5.Property.LongText({
      displayName: "Description",
      description: "A description of the campaign (optional).",
      required: !1
    }),
    agent_id: import_pieces_framework5.Property.Dropdown({
      displayName: "Agent",
      description: "Select the AI agent to use for the campaign.",
      required: !0,
      refreshers: ["auth"],
      auth: kallabotAuth,
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        try {
          let agents = (await import_pieces_common5.httpClient.sendRequest({
            method: import_pieces_common5.HttpMethod.GET,
            url: "https://api.kallabot.com/agents",
            headers: {
              Authorization: `Bearer ${auth.secret_text}`,
              "Content-Type": "application/json"
            }
          })).body;
          if (agents && typeof agents == "object") {
            if (agents.data && Array.isArray(agents.data))
              agents = agents.data;
            else if (agents.agents && Array.isArray(agents.agents))
              agents = agents.agents;
            else if (!Array.isArray(agents))
              return console.warn("Agents response is not an array:", agents), {
                disabled: !0,
                options: [],
                placeholder: "Invalid response format from API"
              };
          }
          return Array.isArray(agents) ? {
            options: agents.filter((agent) => agent.call_direction !== "inbound").map((agent) => {
              let agentId = agent.agent_id || agent.id, agentName = agent.name || "Unnamed Agent";
              return {
                label: `(${agentId ? agentId.substring(0, 8) + "..." : "xxxxxxxx..."}) ${agentName}`,
                value: agentId
              };
            })
          } : (console.warn("Agents is not an array after processing:", agents), {
            disabled: !0,
            options: [],
            placeholder: "Invalid agents data format"
          });
        } catch (error) {
          return console.error("Error fetching agents:", error), console.error("Error details:", error instanceof Error ? error.message : String(error)), {
            disabled: !0,
            options: [],
            placeholder: `Error loading agents: ${error instanceof Error ? error.message : "Network error"}`
          };
        }
      }
    }),
    list_id: import_pieces_framework5.Property.Dropdown({
      displayName: "Contact List",
      description: "Select the contact list for the campaign.",
      required: !0,
      refreshers: ["auth"],
      auth: kallabotAuth,
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        try {
          let response = await import_pieces_common5.httpClient.sendRequest({
            method: import_pieces_common5.HttpMethod.GET,
            url: "https://api.kallabot.com/contacts/lists",
            headers: {
              Authorization: `Bearer ${auth.secret_text}`,
              "Content-Type": "application/json"
            }
          }), lists = [];
          if (Array.isArray(response.body))
            lists = response.body;
          else if (response.body && Array.isArray(response.body.data))
            lists = response.body.data;
          else if (response.body && Array.isArray(response.body.lists))
            lists = response.body.lists;
          else if (response.body && Array.isArray(response.body.contact_lists))
            lists = response.body.contact_lists;
          else
            return console.error("Unexpected API response structure:", response.body), {
              disabled: !0,
              options: [],
              placeholder: "Invalid response format from API"
            };
          return Array.isArray(lists) ? {
            options: lists.map((list) => {
              let listId = list.list_id || list.id, listName = list.name || "Unnamed List";
              return {
                label: `(${listId ? listId.substring(0, 8) + "..." : "xxxxxxxx..."}) ${listName} (${list.contact_count || 0} contacts)`,
                value: listId
              };
            })
          } : (console.error("Lists is not an array:", lists), {
            disabled: !0,
            options: [],
            placeholder: "Invalid data format"
          });
        } catch (error) {
          return console.error("Error fetching contact lists:", error), {
            disabled: !0,
            options: [],
            placeholder: "Error loading contact lists"
          };
        }
      }
    }),
    sender_phone_numbers: import_pieces_framework5.Property.MultiSelectDropdown({
      displayName: "Sender Phone Numbers",
      description: "Select phone numbers to use for making calls.",
      required: !0,
      refreshers: ["auth"],
      auth: kallabotAuth,
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        try {
          let response = await import_pieces_common5.httpClient.sendRequest({
            method: import_pieces_common5.HttpMethod.GET,
            url: "https://api.kallabot.com/account-phone-numbers",
            headers: {
              Authorization: `Bearer ${auth.secret_text}`,
              "Content-Type": "application/json"
            }
          }), phoneNumbers = [];
          if (Array.isArray(response.body))
            phoneNumbers = response.body;
          else if (response.body && Array.isArray(response.body.data))
            phoneNumbers = response.body.data;
          else if (response.body && Array.isArray(response.body.phone_numbers))
            phoneNumbers = response.body.phone_numbers;
          else
            return console.error("Unexpected API response structure:", response.body), {
              disabled: !0,
              options: [],
              placeholder: "Invalid response format from API"
            };
          return Array.isArray(phoneNumbers) ? {
            disabled: !1,
            options: phoneNumbers.map((number) => ({
              label: `${number.phone_number || number.number} (${number.friendly_name || "No name"})`,
              value: number.phone_number || number.number
            }))
          } : (console.error("Phone numbers is not an array:", phoneNumbers), {
            disabled: !0,
            options: [],
            placeholder: "Invalid data format"
          });
        } catch (error) {
          return console.error("Error fetching phone numbers:", error), {
            disabled: !0,
            options: [],
            placeholder: "Error loading phone numbers"
          };
        }
      }
    }),
    delay_between_calls: import_pieces_framework5.Property.Number({
      displayName: "Delay Between Calls (seconds)",
      description: "Time to wait between calls in seconds.",
      required: !1,
      defaultValue: 10
    }),
    scheduled_time: import_pieces_framework5.Property.DateTime({
      displayName: "Scheduled Time",
      description: "When to start the campaign (format: YYYY-MM-DDTHH:MM:SS) ISO 8601.",
      required: !0,
      defaultValue: "2026-08-01T13:15:00"
    }),
    timezone: import_pieces_framework5.Property.ShortText({
      displayName: "Timezone",
      description: "Timezone for the scheduled time (e.g., America/New_York).",
      required: !1,
      defaultValue: "UTC"
    }),
    rotate_numbers_after: import_pieces_framework5.Property.Number({
      displayName: "Rotate Numbers After",
      description: "Number of contacts to call before rotating to the next phone number.",
      required: !1,
      defaultValue: 60
    })
  },
  async run(context) {
    let payload = {
      name: context.propsValue.name,
      list_id: context.propsValue.list_id,
      agent_id: context.propsValue.agent_id,
      sender_phone_numbers: context.propsValue.sender_phone_numbers || [],
      delay_between_calls: context.propsValue.delay_between_calls || 10,
      scheduled_time: new Date(context.propsValue.scheduled_time).toISOString().slice(0, 19),
      timezone: context.propsValue.timezone || "UTC",
      rotate_numbers_after: context.propsValue.rotate_numbers_after || 10
    };
    return context.propsValue.description && (payload.description = context.propsValue.description), (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.POST,
      url: "https://api.kallabot.com/campaign",
      headers: {
        Authorization: `Bearer ${context.auth.secret_text}`,
        "Content-Type": "application/json"
      },
      body: payload
    })).body;
  }
});

// ../ap/packages/pieces/community/kallabot-ai/src/lib/actions/delete-campaign.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var deleteCampaignAction = (0, import_pieces_framework6.createAction)({
  name: "delete-campaign",
  displayName: "Delete Campaign",
  description: "Delete a campaign with optional contact list deletion.",
  audience: "both",
  aiMetadata: {
    description: "Permanently delete an outbound campaign by its campaign ID, optionally also deleting its associated contact list. Use it to clean up a campaign that is no longer needed; this is destructive. Idempotent in effect: once the campaign is gone, repeating the call leaves nothing further to delete.",
    idempotent: !0
  },
  auth: kallabotAuth,
  props: {
    campaign_id: import_pieces_framework6.Property.ShortText({
      displayName: "Campaign ID",
      description: "The ID of the campaign to delete.",
      required: !0
    }),
    delete_contact_list: import_pieces_framework6.Property.Checkbox({
      displayName: "Delete Contact List",
      description: "Also delete the associated contact list (optional).",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let params = new URLSearchParams();
    context.propsValue.delete_contact_list && params.append("delete_contact_list", "true");
    let url = `https://api.kallabot.com/campaign/${context.propsValue.campaign_id}${params.toString() ? "?" + params.toString() : ""}`;
    return (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.DELETE,
      url,
      headers: {
        Authorization: `Bearer ${context.auth.secret_text}`,
        "Content-Type": "application/json"
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/kallabot-ai/src/lib/triggers/call-events.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_framework8 = require("@activepieces/pieces-framework"), webhookInstructions = `**Setup Instructions:**
1. Copy the webhook URL below.
2. In your Kallabot AI agent configuration, go to Actions/Webhooks settings  .
3. Enable webhooks and paste the webhook URL.
4. Your agent will now send call data to this webhook when a call ends.`, testInstructions = `
**Test URL:**

If you want to generate sample data without triggering the flow, append \`/test\` to your webhook URL.

`, callEventsTrigger = (0, import_pieces_framework7.createTrigger)({
  name: "catch_webhook",
  displayName: "Call Events Webhook",
  description: "Receives webhook data when call events occur from Kallabot AI agents.",
  aiMetadata: {
    description: "Fires when a Kallabot AI agent reports a call event via webhook, typically when a call ends, delivering the call details (status, duration, cost, recording, transcription, and transfer info) for that call. Requires the webhook URL to be configured in the Kallabot agent settings."
  },
  requireAuth: !1,
  props: {
    webhookUrl: import_pieces_framework7.Property.MarkDown({
      value: "**Webhook URL:**\n```text\n{{webhookUrl}}\n```",
      variant: import_pieces_framework8.MarkdownVariant.BORDERLESS
    }),
    instructions: import_pieces_framework7.Property.MarkDown({
      value: webhookInstructions,
      variant: import_pieces_framework8.MarkdownVariant.INFO
    }),
    testInstructions: import_pieces_framework7.Property.MarkDown({
      value: testInstructions,
      variant: import_pieces_framework8.MarkdownVariant.INFO
    })
  },
  sampleData: {
    event_type: "call_completed",
    call_sid: "CAxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
    status: "completed",
    call_type: "inbound",
    from_number: "+1234567890",
    to_number: "+1987654321",
    duration: 125.5,
    cost: 1.25,
    created_at: "2024-01-15T10:30:00Z",
    agent: {
      agent_id: "agent_123456789",
      name: "Customer Service Agent"
    },
    recording: {
      url: "https://kallabot-s3-amazon.com/recordings/recording_123.wav",
      sid: "RExxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
    },
    transcription: {
      conversation: [
        {
          speaker: "agent",
          text: "Hello, thank you for calling. How can I help you today?",
          timestamp: "2024-01-15T10:30:05Z"
        },
        {
          speaker: "customer",
          text: "Hi, I need help with my account.",
          timestamp: "2024-01-15T10:30:10Z"
        }
      ]
    },
    transfer: {
      transferred: !1,
      department: null,
      number: null,
      time: null
    }
  },
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  async onEnable() {
  },
  async onDisable() {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/kallabot-ai/src/lib/actions/get-contacts-from-list.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var getContactsFromListAction = (0, import_pieces_framework9.createAction)({
  name: "get-contacts-from-list",
  displayName: "Get Contacts from List",
  description: "Retrieve all contacts from a specific contact list.",
  audience: "both",
  aiMetadata: {
    description: "List every contact stored in one Kallabot contact list, identified by its list ID. Use it to read or enumerate the members of a list before calling, editing, or campaigning. Read-only and idempotent.",
    idempotent: !0
  },
  auth: kallabotAuth,
  props: {
    list_id: import_pieces_framework9.Property.Dropdown({
      displayName: "Contact List",
      description: "Select the contact list to retrieve contacts from.",
      required: !0,
      auth: kallabotAuth,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        try {
          let response = await import_pieces_common7.httpClient.sendRequest({
            method: import_pieces_common7.HttpMethod.GET,
            url: "https://api.kallabot.com/contacts/lists",
            headers: {
              Authorization: `Bearer ${auth.secret_text}`,
              "Content-Type": "application/json"
            }
          }), lists = [];
          if (Array.isArray(response.body))
            lists = response.body;
          else if (response.body && Array.isArray(response.body.data))
            lists = response.body.data;
          else if (response.body && Array.isArray(response.body.lists))
            lists = response.body.lists;
          else if (response.body && Array.isArray(response.body.contact_lists))
            lists = response.body.contact_lists;
          else
            return console.error("Unexpected API response structure:", response.body), {
              disabled: !0,
              options: [],
              placeholder: "Invalid response format from API"
            };
          return Array.isArray(lists) ? {
            options: lists.map((list) => {
              let listId = list.list_id || list.id, listName = list.name || "Unnamed List";
              return {
                label: `(${listId ? listId.substring(0, 8) + "..." : "xxxxxxxx..."}) ${listName} (${list.contact_count || 0} contacts)`,
                value: listId
              };
            })
          } : (console.error("Lists is not an array:", lists), {
            disabled: !0,
            options: [],
            placeholder: "Invalid data format"
          });
        } catch (error) {
          return console.error("Error fetching contact lists:", error), {
            disabled: !0,
            options: [],
            placeholder: "Error loading contact lists"
          };
        }
      }
    })
  },
  async run(context) {
    return (await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.GET,
      url: `https://api.kallabot.com/contacts?list_id=${context.propsValue.list_id}`,
      headers: {
        Authorization: `Bearer ${context.auth}`,
        "Content-Type": "application/json"
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/kallabot-ai/src/lib/actions/edit-contact-list.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var editContactListAction = (0, import_pieces_framework10.createAction)({
  name: "edit-contact-list",
  displayName: "Edit Contact List",
  description: "Update/replace all contacts in an existing contact list.",
  audience: "both",
  aiMetadata: {
    description: "Replace the entire contents of an existing contact list (by list ID) with a supplied array of contacts, each requiring a phone_number and optionally a name and template_variables. This is a full overwrite, not an append \u2014 contacts not in the array are removed; use Add Contacts to List to append instead. Idempotent: re-sending the same array yields the same resulting list.",
    idempotent: !0
  },
  auth: kallabotAuth,
  props: {
    list_id: import_pieces_framework10.Property.Dropdown({
      auth: kallabotAuth,
      displayName: "Contact List",
      description: "Select the contact list to edit.",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        try {
          let response = await import_pieces_common8.httpClient.sendRequest({
            method: import_pieces_common8.HttpMethod.GET,
            url: "https://api.kallabot.com/contacts/lists",
            headers: {
              Authorization: `Bearer ${auth.secret_text}`,
              "Content-Type": "application/json"
            }
          }), lists = [];
          if (Array.isArray(response.body))
            lists = response.body;
          else if (response.body && Array.isArray(response.body.data))
            lists = response.body.data;
          else if (response.body && Array.isArray(response.body.lists))
            lists = response.body.lists;
          else if (response.body && Array.isArray(response.body.contact_lists))
            lists = response.body.contact_lists;
          else
            return {
              disabled: !0,
              options: [],
              placeholder: "Invalid response format from API"
            };
          return {
            options: lists.map((list) => {
              let listId = list.list_id || list.id, listName = list.name || "Unnamed List";
              return {
                label: `(${listId ? listId.substring(0, 8) + "..." : "xxxxxxxx..."}) ${listName} (${list.contact_count || 0} contacts)`,
                value: listId
              };
            })
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading contact lists"
          };
        }
      }
    }),
    contacts: import_pieces_framework10.Property.DynamicProperties({
      auth: kallabotAuth,
      displayName: "Contacts",
      description: "JSON array of contacts to replace the entire list",
      required: !0,
      refreshers: ["list_id"],
      props: async ({ list_id, auth }) => {
        if (!list_id || !auth)
          return {
            contacts: import_pieces_framework10.Property.Json({
              displayName: "Contacts JSON",
              description: "JSON array of contacts to replace the entire list. Each contact must have phone_number and can include name and template_variables.",
              required: !0,
              defaultValue: [
                {
                  phone_number: "+1234567890",
                  name: "Sample Contact",
                  template_variables: {}
                }
              ]
            })
          };
        try {
          let response = await import_pieces_common8.httpClient.sendRequest({
            method: import_pieces_common8.HttpMethod.GET,
            url: `https://api.kallabot.com/contacts?list_id=${list_id}`,
            headers: {
              Authorization: `Bearer ${auth}`,
              "Content-Type": "application/json"
            }
          }), contacts = [];
          Array.isArray(response.body) ? contacts = response.body : response.body && Array.isArray(response.body.contacts) ? contacts = response.body.contacts : response.body && Array.isArray(response.body.data) ? contacts = response.body.data : contacts = [];
          let formattedContacts = contacts.map((contact) => ({
            phone_number: contact.phone_number || contact.phone || "",
            name: contact.name || "",
            template_variables: contact.template_variables || contact.custom_fields || {}
          })).filter((contact) => contact.phone_number);
          return {
            contacts: import_pieces_framework10.Property.Json({
              displayName: "Contacts JSON",
              description: "Current contacts from list. Edit this JSON to update the entire list.",
              required: !0,
              defaultValue: formattedContacts.length > 0 ? formattedContacts : [
                {
                  phone_number: "+1234567890",
                  name: "New Contact",
                  template_variables: {}
                }
              ]
            })
          };
        } catch (error) {
          return {
            contacts: import_pieces_framework10.Property.Json({
              displayName: "Contacts JSON",
              description: "Could not load contacts. Please enter manually.",
              required: !0,
              defaultValue: [
                {
                  phone_number: "+1234567890",
                  name: "New Contact",
                  template_variables: {}
                }
              ]
            })
          };
        }
      }
    })
  },
  async run(context) {
    var _a;
    let contacts = ((_a = context.propsValue.contacts) == null ? void 0 : _a.contacts) || [];
    if (!Array.isArray(contacts) || contacts.length === 0)
      throw new Error("At least one contact must be provided");
    let payload = {
      contacts
    };
    return (await import_pieces_common8.httpClient.sendRequest({
      method: import_pieces_common8.HttpMethod.PUT,
      url: `https://api.kallabot.com/contacts/${context.propsValue.list_id}`,
      headers: {
        Authorization: `Bearer ${context.auth}`,
        "Content-Type": "application/json"
      },
      body: payload
    })).body;
  }
});

// ../ap/packages/pieces/community/kallabot-ai/src/index.ts
var authDescription = `
Follow these steps to obtain your Kallbot API Key:

1. Go to [Kallabot](https://kallabot.com/) and log in to your account.
2. Click on your profile picture in the top right corner.
3. Select **Settings** from the dropdown menu.
4. Navigate to the **API & Integrations** tab.
5. Copy the generated API key and paste it here.
`, kallabotAuth = import_pieces_framework11.PieceAuth.SecretText({
  description: authDescription,
  displayName: "API Key",
  required: !0
}), kallabotAi = (0, import_pieces_framework11.createPiece)({
  displayName: "Kallabot",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/kallabot-ai.png",
  categories: [import_pieces_framework12.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["abdulrahmanmajid"],
  auth: kallabotAuth,
  actions: [
    makeCallAction,
    getCallDetailsAction,
    getContactsFromListAction,
    editContactListAction,
    addContactToListAction,
    createContactListAction,
    createCampaignAction,
    deleteCampaignAction
  ],
  triggers: [callEventsTrigger],
  description: "AI-powered voice agents and conversational interfaces."
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  kallabotAi,
  kallabotAuth
});
