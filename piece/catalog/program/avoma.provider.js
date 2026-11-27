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

// ../ap/packages/pieces/community/avoma/src/index.ts
var index_exports = {};
__export(index_exports, {
  avoma: () => avoma
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/avoma/src/lib/triggers/new-note.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), newNote = (0, import_pieces_framework.createTrigger)({
  name: "new_note",
  classification: "READ",
  displayName: "New Note",
  description: "Triggers when notes are successfully generated for meetings or calls",
  aiMetadata: {
    description: "Fires when Avoma finishes generating AI notes for a recorded meeting or call (the AINOTE event), delivering the summary, action items, transcript and recording references for that conversation. Requires configuring the AINOTE webhook in Avoma settings."
  },
  type: import_pieces_framework.TriggerStrategy.WEBHOOK,
  props: {
    setupInstructions: import_pieces_framework.Property.MarkDown({
      value: `
**Quick Setup:**

1. In Avoma: **Settings > Integrations > Webhooks**
2. **Webhook URL:** \`{{webhookUrl}}\`
3. **Event Type:** Select **"AINOTE"**
4. **HTTP Method:** POST
5. **Content Type:** application/json

**Note:** Requires admin permissions in Avoma.
      `
    })
  },
  sampleData: {
    action_items: [
      {
        action_item: "Follow up with client on project timeline",
        company: "Avoma",
        email: "john.doe@avoma.com",
        name: "John Doe"
      }
    ],
    ai_notes: "<h2>Participants</h2><ul><li><p>Avoma: John Doe</p></li></ul>",
    ai_notes_txt: "Participants: Avoma: John Doe",
    attendees: [
      {
        email: "user@example.com",
        name: "John Doe",
        response_status: "accepted",
        uuid: "095be615-a8ad-4c33-8e9c-c7612fbf6c9f"
      }
    ],
    audio_ready: !0,
    audio_url: "http://example.com",
    created: "2019-08-24T14:15:22Z",
    duration: 3600,
    end_at: "2019-08-24T14:15:22Z",
    event_type: "AINOTE",
    is_call: !0,
    is_internal: !1,
    meeting_url: "https://app.avoma.com/meeting/aa380fc2-725d-4ef8-909c-c595c0e62bcd",
    modified: "2019-08-24T14:15:22Z",
    notes_ready: !0,
    organizer_email: "user@example.com",
    organizer_name: "John Doe",
    privacy: "private",
    processing_status: "completed",
    insights: {
      filler_wpm: [],
      longest_monologue: {},
      patience: [],
      sentiment: {},
      speaker_mapping: {},
      talk_stats: {},
      wpm: []
    },
    purpose: {
      label: "Sales Call",
      uuid: "095be615-a8ad-4c33-8e9c-c7612fbf6c9f"
    },
    recording_uuid: "3e256a03-2cdd-4fe9-823b-2b61bb25d916",
    start_at: "2019-08-24T14:15:22Z",
    state: "completed",
    subject: "Weekly Team Meeting",
    transcript_ready: !0,
    transcription_uuid: "4753c6bf-25a6-45a1-8d86-0af7c8bde615",
    transcription_vtt_url: "http://example.com",
    uuid: "095be615-a8ad-4c33-8e9c-c7612fbf6c9f",
    video_ready: !0,
    video_url: "http://example.com"
  },
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/avoma/src/lib/actions/create-call.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/avoma/src/lib/common/index.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), avomaAuth = import_pieces_framework2.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Your Avoma API Key (Bearer token). Generate it from your Avoma API Integration settings: https://help.avoma.com/api-integration-for-avoma",
  required: !0
}), avomaCommon = {
  avomaAuth,
  meetingDropdown: import_pieces_framework2.Property.Dropdown({
    auth: avomaAuth,
    displayName: "Meeting",
    description: "Select a meeting from your Avoma account",
    required: !0,
    refreshers: [],
    options: async ({ auth }) => {
      var _a, _b;
      if (!auth)
        return {
          disabled: !0,
          placeholder: "Please connect your Avoma account first",
          options: []
        };
      try {
        let toDate = (/* @__PURE__ */ new Date()).toISOString(), fromDate = new Date(Date.now() - 720 * 60 * 60 * 1e3).toISOString(), response = await import_pieces_common.httpClient.sendRequest({
          method: import_pieces_common.HttpMethod.GET,
          url: `https://api.avoma.com/v1/meetings/?page_size=100&from_date=${encodeURIComponent(fromDate)}&to_date=${encodeURIComponent(toDate)}`,
          headers: {
            Authorization: `Bearer ${auth.secret_text}`,
            "Content-Type": "application/json"
          }
        }), meetings = ((_a = response.body) == null ? void 0 : _a.results) || ((_b = response.body) == null ? void 0 : _b.data) || response.body || [];
        return Array.isArray(meetings) ? {
          disabled: !1,
          options: meetings.map((meeting) => {
            let subject = meeting.subject || meeting.title || "Untitled Meeting", startTime = meeting.start_time || meeting.startTime || meeting.created_at, dateStr = startTime ? new Date(startTime).toLocaleDateString() : "Unknown date", uuid = meeting.uuid || meeting.id;
            return {
              label: `${subject} - ${dateStr}`,
              value: uuid
            };
          })
        } : {
          disabled: !0,
          placeholder: "No meetings found or unexpected response format",
          options: []
        };
      } catch (error) {
        return console.error("Error fetching meetings:", error), {
          disabled: !0,
          placeholder: "Failed to load meetings. Please check your API key.",
          options: []
        };
      }
    }
  }),
  transcriptionDropdown: import_pieces_framework2.Property.Dropdown({
    auth: avomaAuth,
    displayName: "Transcription",
    description: "Select a transcription from your Avoma meetings",
    required: !0,
    refreshers: [],
    options: async ({ auth }) => {
      var _a;
      if (!auth)
        return {
          disabled: !0,
          placeholder: "Please connect your Avoma account first",
          options: []
        };
      try {
        let toDate = (/* @__PURE__ */ new Date()).toISOString(), fromDate = new Date(Date.now() - 720 * 60 * 60 * 1e3).toISOString(), response = await import_pieces_common.httpClient.sendRequest({
          method: import_pieces_common.HttpMethod.GET,
          url: `https://api.avoma.com/v1/transcriptions/?page_size=100&from_date=${encodeURIComponent(fromDate)}&to_date=${encodeURIComponent(toDate)}`,
          headers: {
            Authorization: `Bearer ${auth.secret_text}`,
            "Content-Type": "application/json"
          }
        }), transcriptions = [];
        return (_a = response.body) != null && _a.results ? transcriptions = response.body.results : Array.isArray(response.body) ? transcriptions = response.body : response.body && typeof response.body == "object" && (transcriptions = [response.body]), Array.isArray(transcriptions) ? {
          disabled: !1,
          options: transcriptions.map((transcription) => {
            var _a2, _b;
            let transcriptionUuid = transcription.uuid, speakersCount = ((_a2 = transcription.speakers) == null ? void 0 : _a2.length) || 0, transcriptLength = ((_b = transcription.transcript) == null ? void 0 : _b.length) || 0;
            return {
              label: `Transcription ${transcriptionUuid.substring(0, 8)}... - ${speakersCount} speakers, ${transcriptLength} paragraphs`,
              value: transcriptionUuid
            };
          })
        } : {
          disabled: !0,
          placeholder: "Unexpected response format from API",
          options: []
        };
      } catch (error) {
        return console.error("Error fetching transcriptions:", error), {
          disabled: !0,
          placeholder: "Failed to load transcriptions. Please check your API key.",
          options: []
        };
      }
    }
  }),
  meetingUuidProperty: import_pieces_framework2.Property.ShortText({
    displayName: "Meeting UUID",
    description: "Enter the meeting UUID directly, or use the dropdown below to select from your meetings",
    required: !1
  }),
  transcriptionUuidProperty: import_pieces_framework2.Property.ShortText({
    displayName: "Transcription UUID",
    description: "Enter the transcription UUID directly, or use the dropdown below to select from available transcriptions",
    required: !1
  })
};

// ../ap/packages/pieces/community/avoma/src/lib/actions/create-call.ts
var createCall = (0, import_pieces_framework3.createAction)({
  auth: avomaCommon.avomaAuth,
  name: "create_call",
  classification: "WRITE",
  displayName: "Create Call",
  description: "Creates a new call in Avoma",
  audience: "both",
  aiMetadata: { description: "Imports an external dialer call (Zoom, Twilio, Aircall, HubSpot, etc.) into Avoma for AI recording, transcription, and analysis. Use when ingesting call data captured outside Avoma; requires a publicly accessible recording URL, an Avoma user email, and a unique external_id. Not idempotent \u2014 each call creates a new record, and reposting the same external_id and source will fail as a duplicate.", idempotent: !1 },
  props: {
    external_id: import_pieces_framework3.Property.ShortText({
      displayName: "External ID",
      description: "Unique id of the call from the dialer system like hubspot, twilio, zoom, etc.",
      required: !0
    }),
    user_email: import_pieces_framework3.Property.ShortText({
      displayName: "User Email",
      description: "Email of the user who made or received the call. This should be an Avoma user's email.",
      required: !0
    }),
    source: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Source",
      description: "Source of the call",
      required: !0,
      options: {
        options: [
          { label: "Zoom", value: "zoom" },
          { label: "Zoom Phone", value: "zoomphone" },
          { label: "Twilio", value: "twilio" },
          { label: "PhoneBurner", value: "phoneburner" },
          { label: "RingCentral", value: "ringcentral" },
          { label: "Aircall", value: "aircall" },
          { label: "HubSpot", value: "hubspot" },
          { label: "Other", value: "other" }
        ]
      }
    }),
    direction: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Direction",
      description: "Direction of the call",
      required: !0,
      options: {
        options: [
          { label: "Inbound", value: "Inbound" },
          { label: "Outbound", value: "Outbound" }
        ]
      }
    }),
    start_at: import_pieces_framework3.Property.DateTime({
      displayName: "Start Time",
      description: "Start time of the call",
      required: !0
    }),
    frm: import_pieces_framework3.Property.ShortText({
      displayName: "From Phone Number",
      description: "Phone number from which call was made (e.g., +11234567890)",
      required: !0
    }),
    to: import_pieces_framework3.Property.ShortText({
      displayName: "To Phone Number",
      description: "Phone number to which call was made (e.g., +12234567890)",
      required: !0
    }),
    recording_url: import_pieces_framework3.Property.LongText({
      displayName: "Recording URL",
      description: "URL of the recording of the call. This should be a public URL that Avoma can access.",
      required: !0
    }),
    participants: import_pieces_framework3.Property.Array({
      displayName: "Participants",
      description: "List of participants in the call. First entry should be the prospect/lead.",
      required: !0,
      properties: {
        name: import_pieces_framework3.Property.ShortText({
          displayName: "Name",
          description: "Name of the participant",
          required: !0
        }),
        email: import_pieces_framework3.Property.ShortText({
          displayName: "Email",
          description: "Email of the participant",
          required: !0
        }),
        crm_id: import_pieces_framework3.Property.ShortText({
          displayName: "CRM Record ID",
          description: "CRM record ID for this participant (optional)",
          required: !1
        }),
        crm_type: import_pieces_framework3.Property.ShortText({
          displayName: "CRM Record Type",
          description: "Type of CRM record (e.g., contact, lead, opportunity)",
          required: !1
        }),
        crm_system: import_pieces_framework3.Property.ShortText({
          displayName: "CRM System",
          description: "CRM system name (e.g., hubspot, salesforce)",
          required: !1
        })
      }
    }),
    end_at: import_pieces_framework3.Property.DateTime({
      displayName: "End Time",
      description: "End time of the call",
      required: !1
    }),
    frm_name: import_pieces_framework3.Property.ShortText({
      displayName: "From Name",
      description: "Name of the caller who made the call",
      required: !1
    }),
    to_name: import_pieces_framework3.Property.ShortText({
      displayName: "To Name",
      description: "Name of the person to whom call was made",
      required: !1
    }),
    answered: import_pieces_framework3.Property.Checkbox({
      displayName: "Answered",
      description: "Whether the call was answered",
      required: !1
    }),
    is_voicemail: import_pieces_framework3.Property.Checkbox({
      displayName: "Is Voicemail",
      description: "Indicates if the call is a voicemail",
      required: !1,
      defaultValue: !1
    }),
    additional_details: import_pieces_framework3.Property.LongText({
      displayName: "Additional Details",
      description: "Additional details of the call (JSON object as string)",
      required: !1
    })
  },
  async run(context) {
    var _a;
    let { auth, propsValue } = context, participants = ((_a = propsValue.participants) == null ? void 0 : _a.map((participant) => {
      let participantData = {
        email: participant.email,
        name: participant.name
      };
      return participant.crm_id && participant.crm_type && participant.crm_system && (participantData.associations = [{
        id: participant.crm_id,
        type: participant.crm_type,
        system: participant.crm_system
      }]), participantData;
    })) || [], requestBody = {
      external_id: propsValue.external_id,
      user_email: propsValue.user_email,
      source: propsValue.source,
      direction: propsValue.direction,
      start_at: propsValue.start_at,
      frm: propsValue.frm,
      to: propsValue.to,
      recording_url: propsValue.recording_url,
      participants
    };
    if (propsValue.end_at && (requestBody.end_at = propsValue.end_at), propsValue.frm_name && (requestBody.frm_name = propsValue.frm_name), propsValue.to_name && (requestBody.to_name = propsValue.to_name), propsValue.answered !== void 0 && (requestBody.answered = propsValue.answered), propsValue.is_voicemail !== void 0 && (requestBody.is_voicemail = propsValue.is_voicemail), propsValue.additional_details)
      try {
        requestBody.additional_details = JSON.parse(propsValue.additional_details);
      } catch (e) {
        requestBody.additional_details = propsValue.additional_details;
      }
    try {
      let response = await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.POST,
        url: "https://api.avoma.com/v1/calls/",
        headers: {
          Authorization: `Bearer ${auth.secret_text}`,
          "Content-Type": "application/json"
        },
        body: requestBody
      });
      if (response.status === 400) {
        let errorDetails = response.body, errorMessage = "Bad request: ";
        if (typeof errorDetails == "object") {
          let fieldErrors = Object.entries(errorDetails).map(([field, errors]) => `${field}: ${Array.isArray(errors) ? errors.join(", ") : errors}`).join("; ");
          errorMessage += fieldErrors;
        } else
          errorMessage += JSON.stringify(errorDetails) || "Invalid input data";
        throw errorMessage.includes("external_id") && errorMessage.includes("source") ? errorMessage += ". This may be due to a duplicate call (same external_id and source already exists)" : errorMessage.includes("user_email") && (errorMessage += ". Check that the user exists, is active, belongs to your organization, and has dialer permissions"), new Error(errorMessage);
      }
      if (response.status >= 400)
        throw new Error(`API error (${response.status}): ${JSON.stringify(response.body) || "Unknown error"}`);
      return response.body;
    } catch (error) {
      let errorMessage = error instanceof Error ? error.message : String(error);
      throw new Error(`Failed to create call: ${errorMessage}`);
    }
  }
});

// ../ap/packages/pieces/community/avoma/src/lib/actions/get-meeting-transcription.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var getMeetingTranscription = (0, import_pieces_framework4.createAction)({
  auth: avomaCommon.avomaAuth,
  name: "get_meeting_transcription",
  classification: "READ",
  displayName: "Get Meeting Transcription",
  description: "Returns transcription with speakers, timestamps, and VTT file URL",
  audience: "both",
  aiMetadata: { description: "Fetches the transcription for a single Avoma meeting by its transcription UUID, including speaker-labeled segments and a downloadable VTT subtitle URL. Use to retrieve the text of a recorded meeting or call. Read-only and idempotent.", idempotent: !0 },
  props: {
    transcription_uuid: avomaCommon.transcriptionDropdown
  },
  async run(context) {
    var _a, _b, _c, _d;
    let { auth, propsValue } = context, transcriptionUuid = propsValue.transcription_uuid;
    if (!transcriptionUuid)
      throw new Error("Please select a transcription from the dropdown");
    try {
      let response = await import_pieces_common3.httpClient.sendRequest({
        method: import_pieces_common3.HttpMethod.GET,
        url: `https://api.avoma.com/v1/transcriptions/${transcriptionUuid}`,
        headers: {
          Authorization: `Bearer ${auth}`,
          "Content-Type": "application/json"
        }
      });
      if (response.status === 404) {
        let errorDetail = ((_a = response.body) == null ? void 0 : _a.detail) || "Transcription not found";
        throw new Error(`Transcription not found: ${errorDetail}`);
      } else if (response.status >= 400) {
        let errorDetail = ((_b = response.body) == null ? void 0 : _b.detail) || JSON.stringify(response.body) || "Unknown error";
        throw new Error(`API error (${response.status}): ${errorDetail}`);
      }
      let transcription = response.body;
      return {
        success: !0,
        meeting_uuid: transcription.meeting_uuid,
        transcription_uuid: transcription.uuid,
        transcription_vtt_url: transcription.transcription_vtt_url,
        speakers: transcription.speakers || [],
        transcript: transcription.transcript || [],
        transcript_paragraphs_count: ((_c = transcription.transcript) == null ? void 0 : _c.length) || 0,
        speakers_count: ((_d = transcription.speakers) == null ? void 0 : _d.length) || 0,
        note: "Use transcription_vtt_url to download the VTT subtitle file for the meeting"
      };
    } catch (error) {
      let errorMessage = error instanceof Error ? error.message : String(error);
      throw new Error(`Failed to retrieve transcription: ${errorMessage}`);
    }
  }
});

// ../ap/packages/pieces/community/avoma/src/lib/actions/get-meeting-recording.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var getMeetingRecording = (0, import_pieces_framework5.createAction)({
  auth: avomaCommon.avomaAuth,
  name: "get_meeting_recording",
  classification: "READ",
  displayName: "Get Meeting Recording",
  description: "Returns video and audio recording URLs for a given meeting",
  audience: "both",
  aiMetadata: { description: "Fetches the audio and video recording URLs for a single Avoma meeting by its meeting UUID. Use to obtain downloadable media links for a recorded meeting; the returned URLs are time-limited (expire in ~5 days) and the recording may still be processing (not yet available). Read-only and idempotent.", idempotent: !0 },
  props: {
    meeting_uuid: avomaCommon.meetingDropdown
  },
  async run(context) {
    let { auth, propsValue } = context, meetingUuid = propsValue.meeting_uuid;
    if (!meetingUuid)
      throw new Error("Please select a meeting from the dropdown");
    try {
      let response = await import_pieces_common4.httpClient.sendRequest({
        method: import_pieces_common4.HttpMethod.GET,
        url: `https://api.avoma.com/v1/recordings/?meeting_uuid=${meetingUuid}`,
        headers: {
          Authorization: `Bearer ${auth.secret_text}`,
          "Content-Type": "application/json"
        }
      });
      if (response.status === 200)
        return {
          success: !0,
          status: "available",
          meeting_uuid: response.body.meeting_uuid,
          recording_uuid: response.body.uuid,
          audio_url: response.body.audio_url,
          video_url: response.body.video_url,
          valid_till: response.body.valid_till,
          urls_expire_in_days: 5
        };
      if (response.status === 202)
        return {
          success: !0,
          status: "processing",
          meeting_uuid: response.body.meeting_uuid,
          recording_uuid: response.body.uuid,
          message: response.body.message || "Recording is being processed and not yet available",
          note: "Check audio_ready and video_ready parameters in the meeting details to verify recording status"
        };
      if (response.status === 403)
        throw new Error(`Permission denied: ${response.body.detail || "You do not have permission to access this recording"}`);
      if (response.status === 404)
        throw new Error(`Recording not found: ${response.body.detail || "No recording exists for this meeting"}`);
      if (response.status === 429)
        throw new Error(`Rate limit exceeded: ${response.body.detail || "Too many requests. Please try again later"}`);
      if (response.status >= 400)
        throw new Error(`API error (${response.status}): ${JSON.stringify(response.body) || "Unknown error"}`);
      return {
        success: !1,
        status: "unknown",
        meeting_uuid: meetingUuid,
        error: "Unexpected response from API"
      };
    } catch (error) {
      let errorMessage = error instanceof Error ? error.message : String(error);
      throw new Error(`Failed to retrieve meeting recording for UUID ${meetingUuid}: ${errorMessage}`);
    }
  }
});

// ../ap/packages/pieces/community/avoma/src/lib/triggers/new-meeting-scheduled.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), newMeetingScheduled = (0, import_pieces_framework6.createTrigger)({
  name: "new_meeting_scheduled",
  classification: "READ",
  displayName: "New Meeting Scheduled",
  description: "Triggers when a meeting is booked via one of your Avoma scheduling pages",
  aiMetadata: {
    description: "Fires when an invitee books a new meeting through one of your Avoma scheduling pages (the MEETING_BOOKED_VIA_SCHEDULER event), providing the booker, attendees, start/end times, and scheduling intake responses. Requires configuring the corresponding webhook in Avoma settings."
  },
  type: import_pieces_framework6.TriggerStrategy.WEBHOOK,
  props: {
    setupInstructions: import_pieces_framework6.Property.MarkDown({
      value: `
**Quick Setup:**

1. In Avoma: **Settings > Integrations > Webhooks**
2. **Webhook URL:** \`{{webhookUrl}}\`
3. **Event Type:** Select **"MEETING_BOOKED_VIA_SCHEDULER"**
4. **HTTP Method:** POST
5. **Content Type:** application/json

**Note:** Requires admin permissions in Avoma.
      `
    })
  },
  sampleData: {
    booker_email: "client@example.com",
    cancel_reason: null,
    conference_link: "https://zoom.us/j/123456789",
    created: "2019-08-24T14:15:22Z",
    event_end_time: "2019-08-24T15:15:22Z",
    event_start_time: "2019-08-24T14:15:22Z",
    event_type: "MEETING_BOOKED_VIA_SCHEDULER",
    invitee_details: {
      email: "client@example.com",
      locale: "en-US",
      name: "John Client",
      tz: "America/New_York"
    },
    invitee_responses: [
      {
        question: "What would you like to discuss?",
        response: "Product demo and pricing discussion"
      },
      {
        question: "Company size?",
        response: "50-100 employees"
      }
    ],
    meeting_uuid: "65fb768c-30b9-4a9a-999f-5dab85e66635",
    modified: "2019-08-24T14:15:22Z",
    organizer_email: "sales@company.com",
    organizer_timezone: "America/New_York",
    purpose: "Sales Demo",
    scheduling_page_link: "https://meet.avoma.com/sales-demo",
    subject: "Product Demo - John Client",
    uuid: "095be615-a8ad-4c33-8e9c-c7612fbf6c9f"
  },
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/avoma/src/lib/triggers/meeting-rescheduled.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), meetingRescheduled = (0, import_pieces_framework7.createTrigger)({
  name: "meeting_rescheduled",
  classification: "READ",
  displayName: "Meeting Rescheduled",
  description: "Triggers when a scheduled meeting is rescheduled",
  aiMetadata: {
    description: "Fires when a meeting previously booked through an Avoma scheduling page is rescheduled to new times (the MEETING_BOOKED_VIA_SCHEDULER_RESCHEDULED event), providing the updated start/end times along with the booker and meeting details. Requires configuring the corresponding webhook in Avoma settings."
  },
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  props: {
    setupInstructions: import_pieces_framework7.Property.MarkDown({
      value: `
**Quick Setup:**

1. In Avoma: **Settings > Integrations > Webhooks**
2. **Webhook URL:** \`{{webhookUrl}}\`
3. **Event Type:** Select **"MEETING_BOOKED_VIA_SCHEDULER_RESCHEDULED"**
4. **HTTP Method:** POST
5. **Content Type:** application/json

**Note:** Requires admin permissions in Avoma.
      `
    })
  },
  sampleData: {
    booker_email: "client@example.com",
    cancel_reason: null,
    conference_link: "https://zoom.us/j/123456789",
    created: "2019-08-24T14:15:22Z",
    event_end_time: "2019-08-25T15:15:22Z",
    event_start_time: "2019-08-25T14:15:22Z",
    event_type: "MEETING_BOOKED_VIA_SCHEDULER_RESCHEDULED",
    invitee_details: {
      email: "client@example.com",
      locale: "en-US",
      name: "John Client",
      tz: "America/New_York"
    },
    invitee_responses: [
      {
        question: "What would you like to discuss?",
        response: "Product demo and pricing discussion"
      },
      {
        question: "Company size?",
        response: "50-100 employees"
      }
    ],
    meeting_uuid: "65fb768c-30b9-4a9a-999f-5dab85e66635",
    modified: "2019-08-24T16:30:22Z",
    organizer_email: "sales@company.com",
    organizer_timezone: "America/New_York",
    purpose: "Sales Demo",
    scheduling_page_link: "https://meet.avoma.com/sales-demo",
    subject: "Product Demo - John Client (Rescheduled)",
    uuid: "095be615-a8ad-4c33-8e9c-c7612fbf6c9f"
  },
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/avoma/src/lib/triggers/meeting-cancelled.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), meetingCancelled = (0, import_pieces_framework8.createTrigger)({
  name: "meeting_cancelled",
  classification: "READ",
  displayName: "Meeting Cancelled",
  description: "Triggers when a meeting booked via the scheduling page is cancelled",
  aiMetadata: {
    description: "Fires when a meeting booked through an Avoma scheduling page is cancelled (the MEETING_BOOKED_VIA_SCHEDULER_CANCELED event), providing the cancellation reason along with the booker and original meeting details. Requires configuring the corresponding webhook in Avoma settings."
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  props: {
    setupInstructions: import_pieces_framework8.Property.MarkDown({
      value: `
**Quick Setup:**

1. In Avoma: **Settings > Integrations > Webhooks**
2. **Webhook URL:** \`{{webhookUrl}}\`
3. **Event Type:** Select **"MEETING_BOOKED_VIA_SCHEDULER_CANCELED"**
4. **HTTP Method:** POST
5. **Content Type:** application/json

**Note:** Requires admin permissions in Avoma.
      `
    })
  },
  sampleData: {
    booker_email: "client@example.com",
    cancel_reason: "Schedule conflict - need to reschedule for next week",
    conference_link: "https://zoom.us/j/123456789",
    created: "2019-08-24T14:15:22Z",
    event_end_time: "2019-08-24T15:15:22Z",
    event_start_time: "2019-08-24T14:15:22Z",
    event_type: "MEETING_BOOKED_VIA_SCHEDULER_CANCELED",
    invitee_details: {
      email: "client@example.com",
      locale: "en-US",
      name: "John Client",
      tz: "America/New_York"
    },
    invitee_responses: [
      {
        question: "What would you like to discuss?",
        response: "Product demo and pricing discussion"
      },
      {
        question: "Company size?",
        response: "50-100 employees"
      }
    ],
    meeting_uuid: "65fb768c-30b9-4a9a-999f-5dab85e66635",
    modified: "2019-08-24T16:30:22Z",
    organizer_email: "sales@company.com",
    organizer_timezone: "America/New_York",
    purpose: "Sales Demo",
    scheduling_page_link: "https://meet.avoma.com/sales-demo",
    subject: "Product Demo - John Client (Cancelled)",
    uuid: "095be615-a8ad-4c33-8e9c-c7612fbf6c9f"
  },
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/avoma/src/index.ts
var avoma = (0, import_pieces_framework9.createPiece)({
  displayName: "Avoma",
  description: "Avoma is an AI Meeting Assistant that automatically records, transcribes, and summarizes your meetings.",
  auth: avomaCommon.avomaAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/avoma.png",
  authors: ["fortunamide", "onyedikachi-david"],
  actions: [createCall, getMeetingRecording, getMeetingTranscription],
  triggers: [newNote, newMeetingScheduled, meetingRescheduled, meetingCancelled]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  avoma
});
