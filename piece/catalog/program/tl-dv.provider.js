"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
};
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

// ../ap/packages/pieces/community/tl-dv/src/index.ts
var index_exports = {};
__export(index_exports, {
  tlDv: () => tlDv,
  tldvAuth: () => tldvAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_framework11 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/tl-dv/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/tl-dv/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
var tldvCommon = {
  baseUrl: "https://pasta.tldv.io",
  async apiCall({
    method,
    url,
    body,
    auth,
    headers
  }) {
    let apiKey = typeof auth == "string" ? auth : auth.apiKey, response = await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${this.baseUrl}${url}`,
      headers: __spreadValues({
        "x-api-key": apiKey,
        "Content-Type": "application/json"
      }, headers),
      body
    });
    if (response.status >= 400)
      throw new Error(`tl;dv API error: ${response.status} - ${JSON.stringify(response.body)}`);
    return response.body;
  }
};

// ../ap/packages/pieces/community/tl-dv/src/lib/common/auth.ts
var tldvAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Your tl;dv API key. You can find this at https://tldv.io/app/settings/personal-settings/api-keys",
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await tldvCommon.apiCall({
        method: import_pieces_common2.HttpMethod.GET,
        url: "/v1alpha1/health",
        auth: { apiKey: auth }
      }), {
        valid: !0,
        message: "API key validated successfully. Connected to tl;dv."
      };
    } catch (error) {
      return error.message.includes("401") || error.message.includes("403") ? {
        valid: !1,
        error: "Invalid API key. Please check your API key and try again."
      } : {
        valid: !1,
        error: `Authentication failed: ${error.message}. Please verify your API key is correct.`
      };
    }
  }
});

// ../ap/packages/pieces/community/tl-dv/src/lib/actions/upload-recording.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common");
var uploadRecording = (0, import_pieces_framework2.createAction)({
  auth: tldvAuth,
  name: "upload_recording",
  displayName: "Upload Recording",
  description: "Import a meeting or recording from a URL",
  audience: "both",
  aiMetadata: {
    description: "Imports an external recording into tl;dv by ingesting it from a publicly accessible media URL (.mp3/.mp4/.wav/.m4a/.mkv/.mov/.avi/.wma/.flac), queuing it for transcription and processing. Use to bring a recording hosted elsewhere into tl;dv. Each call creates a new import job (not idempotent); set dryRun to validate the import without persisting it.",
    idempotent: !1
  },
  props: {
    name: import_pieces_framework2.Property.ShortText({
      displayName: "Meeting Name",
      description: "The name of the meeting or recording",
      required: !0
    }),
    url: import_pieces_framework2.Property.ShortText({
      displayName: "Recording URL",
      description: "Publicly accessible URL of the recording. Supported formats: .mp3, .mp4, .wav, .m4a, .mkv, .mov, .avi, .wma, .flac",
      required: !0
    }),
    happenedAt: import_pieces_framework2.Property.DateTime({
      displayName: "Meeting Date",
      description: "The date and time when the meeting occurred. If not provided, the current date will be used.",
      required: !1
    }),
    dryRun: import_pieces_framework2.Property.Checkbox({
      displayName: "Dry Run",
      description: "Test the import without persisting to the database",
      required: !1,
      defaultValue: !1
    }),
    participants: import_pieces_framework2.Property.Array({
      displayName: "Participants",
      description: "Email addresses of meeting participants",
      required: !1,
      properties: {
        email: import_pieces_framework2.Property.ShortText({
          displayName: "Email",
          required: !0
        })
      }
    })
  },
  async run(context) {
    let { name, url, happenedAt, dryRun, participants } = context.propsValue;
    try {
      new URL(url);
    } catch (e) {
      throw new Error("Invalid URL format. Please provide a valid, publicly accessible URL.");
    }
    if (participants && Array.isArray(participants)) {
      let emailRegex = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
      for (let participant of participants) {
        let email = typeof participant == "string" ? participant : participant.email;
        if (email && !emailRegex.test(email))
          throw new Error(`Invalid email format: ${email}`);
      }
    }
    let body = {
      name,
      url
    };
    return happenedAt && (body.happenedAt = new Date(happenedAt).toISOString()), dryRun !== void 0 && (body.dryRun = dryRun), participants && Array.isArray(participants) && (body.participants = participants.map(
      (p) => typeof p == "string" ? p : p.email
    )), await tldvCommon.apiCall({
      method: import_pieces_common3.HttpMethod.POST,
      url: "/v1alpha1/meetings/import",
      auth: { apiKey: context.auth.secret_text },
      body
    });
  }
});

// ../ap/packages/pieces/community/tl-dv/src/lib/actions/list-meetings.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common");
var listMeetings = (0, import_pieces_framework3.createAction)({
  auth: tldvAuth,
  name: "list_meetings",
  displayName: "List Meetings",
  description: "Search and list meetings",
  audience: "both",
  aiMetadata: {
    description: "Lists meetings from the tl;dv account, paginated. With an empty search query it returns all meetings; supplying a query filters to matches by name or content, and optional date range, participation, and internal/external type filters further narrow results. Use to discover meetings or find a meeting id to feed into other actions. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    query: import_pieces_framework3.Property.ShortText({
      displayName: "Search Query",
      description: "Search for meetings by name or content",
      required: !1
    }),
    page: import_pieces_framework3.Property.Number({
      displayName: "Page",
      description: "Page number to return (default: 1)",
      required: !1,
      defaultValue: 1
    }),
    limit: import_pieces_framework3.Property.Number({
      displayName: "Results Per Page",
      description: "Number of results per page (default: 50)",
      required: !1,
      defaultValue: 50
    }),
    from: import_pieces_framework3.Property.DateTime({
      displayName: "From Date",
      description: "Search meetings from this date",
      required: !1
    }),
    to: import_pieces_framework3.Property.DateTime({
      displayName: "To Date",
      description: "Search meetings up to this date",
      required: !1
    }),
    onlyParticipated: import_pieces_framework3.Property.Checkbox({
      displayName: "Only Participated",
      description: "Only return meetings you participated in",
      required: !1,
      defaultValue: !1
    }),
    meetingType: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Meeting Type",
      description: "Filter by meeting type",
      required: !1,
      options: {
        options: [
          { label: "All", value: "" },
          { label: "Internal", value: "internal" },
          { label: "External", value: "external" }
        ]
      }
    })
  },
  async run(context) {
    let { query, page, limit, from, to, onlyParticipated, meetingType } = context.propsValue, params = new URLSearchParams();
    query && params.append("query", query), page && params.append("page", page.toString()), limit && params.append("limit", limit.toString()), from && params.append("from", new Date(from).toISOString()), to && params.append("to", new Date(to).toISOString()), onlyParticipated !== void 0 && params.append("onlyParticipated", onlyParticipated.toString()), meetingType && params.append("meetingType", meetingType);
    let queryString = params.toString(), url = `/v1alpha1/meetings${queryString ? `?${queryString}` : ""}`;
    return await tldvCommon.apiCall({
      method: import_pieces_common4.HttpMethod.GET,
      url,
      auth: { apiKey: context.auth.secret_text }
    });
  }
});

// ../ap/packages/pieces/community/tl-dv/src/lib/actions/get-meeting.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/tl-dv/src/lib/common/props.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var meetingIdProperty = import_pieces_framework4.Property.Dropdown({
  displayName: "Meeting",
  description: "Select a meeting",
  auth: tldvAuth,
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      };
    try {
      let response = await tldvCommon.apiCall({
        method: import_pieces_common5.HttpMethod.GET,
        url: "/v1alpha1/meetings?limit=100",
        auth: { apiKey: auth.secret_text }
      });
      return response.results && response.results.length > 0 ? {
        disabled: !1,
        options: response.results.map((meeting) => ({
          label: `${meeting.name}${meeting.happenedAt ? ` (${new Date(meeting.happenedAt).toLocaleDateString()})` : ""}`,
          value: meeting.id
        }))
      } : {
        disabled: !0,
        placeholder: "No meetings found",
        options: []
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Error loading meetings",
        options: []
      };
    }
  }
});

// ../ap/packages/pieces/community/tl-dv/src/lib/actions/get-meeting.ts
var getMeeting = (0, import_pieces_framework5.createAction)({
  auth: tldvAuth,
  name: "get_meeting",
  displayName: "Get Meeting",
  description: "Get meeting details by ID",
  audience: "both",
  aiMetadata: {
    description: "Retrieves the metadata for a single tl;dv meeting by its id, including name, date, duration, organizer, invitees, and the meeting URL. Use when you already have a meeting id and need its details. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    meetingId: meetingIdProperty
  },
  async run(context) {
    let { meetingId } = context.propsValue;
    return await tldvCommon.apiCall({
      method: import_pieces_common6.HttpMethod.GET,
      url: `/v1alpha1/meetings/${meetingId}`,
      auth: { apiKey: context.auth.secret_text }
    });
  }
});

// ../ap/packages/pieces/community/tl-dv/src/lib/actions/get-transcript.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common");
var getTranscript = (0, import_pieces_framework6.createAction)({
  auth: tldvAuth,
  name: "get_transcript",
  displayName: "Get Transcript",
  description: "Get meeting transcript by meeting ID",
  audience: "both",
  aiMetadata: {
    description: "Retrieves the full speaker-attributed, time-stamped transcript for a tl;dv meeting identified by its meeting id. Use when you need the spoken content of a meeting (the transcript must already be generated). Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    meetingId: meetingIdProperty
  },
  async run(context) {
    let { meetingId } = context.propsValue;
    return await tldvCommon.apiCall({
      method: import_pieces_common7.HttpMethod.GET,
      url: `/v1alpha1/meetings/${meetingId}/transcript`,
      auth: { apiKey: context.auth.secret_text }
    });
  }
});

// ../ap/packages/pieces/community/tl-dv/src/lib/actions/get-highlights.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common8 = require("@activepieces/pieces-common");
var getHighlights = (0, import_pieces_framework7.createAction)({
  auth: tldvAuth,
  name: "get_highlights",
  displayName: "Get Highlights",
  description: "Get meeting highlights (notes) by meeting ID",
  audience: "both",
  aiMetadata: {
    description: "Retrieves the highlights and notes captured for a tl;dv meeting identified by its meeting id. Use to pull the key moments or noted points from a meeting rather than the full transcript. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    meetingId: meetingIdProperty
  },
  async run(context) {
    let { meetingId } = context.propsValue;
    return await tldvCommon.apiCall({
      method: import_pieces_common8.HttpMethod.GET,
      url: `/v1alpha1/meetings/${meetingId}/highlights`,
      auth: { apiKey: context.auth.secret_text }
    });
  }
});

// ../ap/packages/pieces/community/tl-dv/src/lib/triggers/meeting-ready.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var meetingReady = (0, import_pieces_framework8.createTrigger)({
  auth: tldvAuth,
  name: "meeting_ready",
  displayName: "Meeting Ready",
  description: "Triggers when a meeting has finished processing and is ready",
  aiMetadata: {
    description: "Fires when a tl;dv meeting has finished processing and is ready, delivering the meeting metadata (id, name, date, duration, organizer, invitees, URL) via a webhook the user configures in tl;dv for the MeetingReady event."
  },
  props: {
    webhookInstructions: import_pieces_framework8.Property.MarkDown({
      value: `
## Setup Instructions

To use this trigger, configure a webhook in tl;dv:

1. Go to your tl;dv dashboard
2. Navigate to Settings > Webhooks
3. Add a new webhook
4. Set the **URL** to:
\`\`\`text
{{webhookUrl}}
\`\`\`
5. Select the **MeetingReady** event type
6. Choose the scope (User, Team, or Organization level)
7. Save the webhook

The webhook will trigger whenever a meeting finishes processing.
      `
    })
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  sampleData: {
    id: "webhook-123",
    event: "MeetingReady",
    data: {
      id: "meeting-123",
      name: "Team Standup Meeting",
      happenedAt: "2024-01-15T10:00:00Z",
      url: "https://app.tldv.io/meetings/meeting-123",
      duration: 1800,
      organizer: {
        name: "John Doe",
        email: "john@example.com"
      },
      invitees: [
        {
          name: "Jane Smith",
          email: "jane@example.com"
        }
      ],
      template: "{}",
      extraProperties: {}
    },
    executedAt: "2024-01-15T10:30:00Z"
  },
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let payload = context.payload.body;
    return payload.event === "MeetingReady" ? [payload] : [];
  }
});

// ../ap/packages/pieces/community/tl-dv/src/lib/triggers/transcript-ready.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var transcriptReady = (0, import_pieces_framework9.createTrigger)({
  auth: tldvAuth,
  name: "transcript_ready",
  displayName: "Transcript Ready",
  description: "Triggers when a meeting transcript has been generated",
  aiMetadata: {
    description: "Fires when a tl;dv meeting transcript has been generated, delivering the speaker-attributed, time-stamped transcript for the meeting via a webhook the user configures in tl;dv for the TranscriptReady event."
  },
  props: {
    webhookInstructions: import_pieces_framework9.Property.MarkDown({
      value: `
## Setup Instructions

To use this trigger, configure a webhook in tl;dv:

1. Go to your tl;dv dashboard
2. Navigate to Settings > Webhooks
3. Add a new webhook
4. Set the **URL** to:
\`\`\`text
{{webhookUrl}}
\`\`\`
5. Select the **TranscriptReady** event type
6. Choose the scope (User, Team, or Organization level)
7. Save the webhook

The webhook will trigger whenever a meeting transcript is generated.
      `
    })
  },
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  sampleData: {
    id: "webhook-456",
    event: "TranscriptReady",
    data: {
      id: "transcript-123",
      meetingId: "meeting-123",
      data: [
        {
          speaker: "John Doe",
          text: "Hello everyone, welcome to today's meeting.",
          startTime: 0,
          endTime: 3
        },
        {
          speaker: "Jane Smith",
          text: "Thanks for having me.",
          startTime: 4,
          endTime: 6
        }
      ]
    },
    executedAt: "2024-01-15T10:35:00Z"
  },
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let payload = context.payload.body;
    return payload.event === "TranscriptReady" ? [payload] : [];
  }
});

// ../ap/packages/pieces/community/tl-dv/src/index.ts
var tlDv = (0, import_pieces_framework10.createPiece)({
  displayName: "tl;dv",
  description: "Record meetings, get transcripts, and access meeting notes automatically.",
  auth: tldvAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/tl-dv.png",
  categories: [import_pieces_framework11.PieceCategory.PRODUCTIVITY],
  authors: ["onyedikachi-david"],
  actions: [
    uploadRecording,
    listMeetings,
    getMeeting,
    getTranscript,
    getHighlights
  ],
  triggers: [
    meetingReady,
    transcriptReady
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  tlDv,
  tldvAuth
});
