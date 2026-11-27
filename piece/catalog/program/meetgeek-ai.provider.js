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

// ../ap/packages/pieces/community/meetgeek-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  meetgeekAi: () => meetgeekAi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework10 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/meetgeek-ai/src/lib/actions/get-highlights.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/meetgeek-ai/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), meetgeekaiAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `Enter your MeetGeek API key to authenticate.

## Acquiring a Token

For service accounts and enterprise company-wide integrations, please use [this form](https://meetgeek.ai/contact) to talk to our team and request a custom quote.

To access the public API, you will need an API key. Follow these steps to obtain your key:

1. Sign up for an account at [app.meetgeek.ai](https://app.meetgeek.ai)
2. Generate your API key by going to **Integrations \u2192 Public API Card**
3. Copy and paste the API key into the field below.`,
  required: !0
});

// ../ap/packages/pieces/community/meetgeek-ai/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/meetgeek-ai/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), MEETGEEKAI_BASE_URL = "https://api.meetgeek.ai/v1";
async function makeRequest(api_key, method, url, body) {
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${MEETGEEKAI_BASE_URL}${url}`,
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${api_key}`
    },
    body
  })).body;
}

// ../ap/packages/pieces/community/meetgeek-ai/src/lib/common/props.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), meetingIdDropdwon = import_pieces_framework2.Property.Dropdown({
  auth: meetgeekaiAuth,
  displayName: "Meeting",
  description: "Select the meeting",
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
      let response = await makeRequest(
        auth.secret_text,
        import_pieces_common2.HttpMethod.GET,
        "/meetings"
      );
      return !response.meetings || response.meetings.length === 0 ? {
        disabled: !0,
        placeholder: "No meetings found",
        options: []
      } : {
        disabled: !1,
        options: response.meetings.map((meeting) => ({
          label: meeting.title || meeting.meeting_id + meeting.timestamp_start_utc,
          value: meeting.meeting_id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load meetings",
        options: []
      };
    }
  }
}), teamIdDropdown = import_pieces_framework2.Property.Dropdown({
  auth: meetgeekaiAuth,
  displayName: "Team",
  description: "Select the team",
  required: !0,
  refreshers: ["auth"],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first"
      };
    try {
      let response = await makeRequest(
        auth.secret_text,
        import_pieces_common2.HttpMethod.GET,
        "/teams"
      ), allTeams = /* @__PURE__ */ new Map();
      return response.share_access && response.share_access.forEach((team) => {
        allTeams.set(team.id, {
          label: `${team.name} (Share Access)`,
          value: team.id
        });
      }), response.view_access && response.view_access.forEach((team) => {
        allTeams.has(team.id) ? allTeams.set(team.id, {
          label: `${team.name} (Share & View Access)`,
          value: team.id
        }) : allTeams.set(team.id, {
          label: `${team.name} (View Access)`,
          value: team.id
        });
      }), {
        disabled: !1,
        options: Array.from(allTeams.values())
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Error loading teams. Please reconnect your account."
      };
    }
  }
});

// ../ap/packages/pieces/community/meetgeek-ai/src/lib/actions/get-highlights.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), getHighlights = (0, import_pieces_framework3.createAction)({
  auth: meetgeekaiAuth,
  name: "getHighlights",
  displayName: "Get Highlights",
  description: "Retrieves all highlights for a meeting by meeting ID",
  audience: "both",
  aiMetadata: {
    description: "Fetch the AI-generated highlights for a single MeetGeek meeting, identified by its meeting ID. Use to pull key moments after a meeting has been analyzed. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    meetingId: meetingIdDropdwon
  },
  async run(context) {
    let { meetingId } = context.propsValue;
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common3.HttpMethod.GET,
      `/meetings/${meetingId}/highlights`
    );
  }
});

// ../ap/packages/pieces/community/meetgeek-ai/src/lib/actions/get-meeting-details.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common"), getMeetingDetails = (0, import_pieces_framework4.createAction)({
  auth: meetgeekaiAuth,
  name: "getMeetingDetails",
  displayName: "Get Meeting Details",
  description: "Retrieves meeting details including host, participants, timestamps, and source information",
  audience: "both",
  aiMetadata: {
    description: "Look up the metadata for one MeetGeek meeting by its meeting ID (host, participants, timestamps, source). Use when an agent needs a meeting's context rather than its content. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    meetingId: meetingIdDropdwon
  },
  async run(context) {
    let { meetingId } = context.propsValue;
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common4.HttpMethod.GET,
      `/meetings/${meetingId}`
    );
  }
});

// ../ap/packages/pieces/community/meetgeek-ai/src/lib/actions/get-meetings-summary-insights.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common"), getMeetingsSummaryInsights = (0, import_pieces_framework5.createAction)({
  auth: meetgeekaiAuth,
  name: "getMeetingsSummaryInsights",
  displayName: "Get Meeting Summary & AI Insights",
  description: "Retrieves the meeting summary and AI-generated insights",
  audience: "both",
  aiMetadata: {
    description: "Fetch the AI-generated summary and insights for a single MeetGeek meeting by its meeting ID. Use to get a condensed overview rather than the full transcript. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    meetingId: meetingIdDropdwon
  },
  async run(context) {
    let { meetingId } = context.propsValue;
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common5.HttpMethod.GET,
      `/meetings/${meetingId}/summary`
    );
  }
});

// ../ap/packages/pieces/community/meetgeek-ai/src/lib/actions/get-team-meetings.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common"), getTeamMeetings = (0, import_pieces_framework6.createAction)({
  auth: meetgeekaiAuth,
  name: "getTeamMeetings",
  displayName: "Get Team Meetings",
  description: "Retrieves paginated past meetings of a team",
  audience: "both",
  aiMetadata: {
    description: "List the past meetings belonging to a given MeetGeek team, identified by team ID. Use to discover meeting IDs to feed into the per-meeting lookup actions. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    teamId: teamIdDropdown
  },
  async run(context) {
    let { teamId } = context.propsValue, url = `/teams/${teamId}/meetings`;
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common6.HttpMethod.GET,
      url
    );
  }
});

// ../ap/packages/pieces/community/meetgeek-ai/src/lib/actions/get-transcript.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common"), getTranscript = (0, import_pieces_framework7.createAction)({
  auth: meetgeekaiAuth,
  name: "getTranscript",
  displayName: "Get Transcript",
  description: "Retrieves all transcript sentences for a meeting with pagination support",
  audience: "both",
  aiMetadata: {
    description: "Fetch the full transcript (all sentences) for a single MeetGeek meeting by its meeting ID. Use when an agent needs the verbatim spoken content rather than a summary or highlights. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    meetingId: meetingIdDropdwon
  },
  async run(context) {
    let { meetingId } = context.propsValue;
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common7.HttpMethod.GET,
      `/meetings/${meetingId}/transcript`
    );
  }
});

// ../ap/packages/pieces/community/meetgeek-ai/src/lib/actions/upload-recording.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var import_pieces_common8 = require("@activepieces/pieces-common"), uploadRecording = (0, import_pieces_framework8.createAction)({
  auth: meetgeekaiAuth,
  name: "uploadRecording",
  displayName: "Upload Recording",
  description: "Upload a video or audio file for analysis and receive a notification upon completion via webhook",
  audience: "both",
  aiMetadata: {
    description: "Submit a video or audio recording to MeetGeek for transcription and AI analysis by passing a publicly accessible download URL (e.g. an S3 signed URL); analysis runs asynchronously and completion is signaled via the New Meeting webhook. Use to ingest an external recording into MeetGeek. Not idempotent: each call queues a new analysis job.",
    idempotent: !1
  },
  props: {
    download_url: import_pieces_framework8.Property.ShortText({
      displayName: "Download URL",
      description: "A publicly accessible URL that initiates a direct download when accessed (e.g., S3 signed URL)",
      required: !0
    }),
    language_code: import_pieces_framework8.Property.ShortText({
      displayName: "Language Code",
      description: "Language code for the recording (e.g., en-US, es-ES, fr-FR). See MeetGeek documentation for all available options.",
      required: !1,
      defaultValue: "en-US"
    }),
    template_name: import_pieces_framework8.Property.ShortText({
      displayName: "Template Name",
      description: 'Meeting template to use for analysis (e.g., "General meeting", "Sales call", "Interview"). See MeetGeek documentation for all available templates.',
      required: !1,
      defaultValue: "General meeting"
    })
  },
  async run(context) {
    let { download_url, language_code, template_name } = context.propsValue, body = {
      download_url
    };
    return language_code && (body.language_code = language_code), template_name && (body.template_name = template_name), await makeRequest(
      context.auth.secret_text,
      import_pieces_common8.HttpMethod.POST,
      "/upload",
      body
    );
  }
});

// ../ap/packages/pieces/community/meetgeek-ai/src/lib/triggers/new-meeting.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var newMeeting = (0, import_pieces_framework9.createTrigger)({
  auth: meetgeekaiAuth,
  name: "newMeeting",
  displayName: "New Meeting",
  description: `Triggers when a meeting analysis is completed and highlights are available.
`,
  aiMetadata: {
    description: "Fires when MeetGeek finishes analyzing a meeting (or uploaded recording) and its highlights, transcript, and summary become available. The payload carries the meeting ID, which can be passed to the get-meeting / transcript / summary / highlights actions. Requires manually configuring the webhook URL in the MeetGeek Public API integrations settings."
  },
  props: {
    instruction: import_pieces_framework9.Property.MarkDown({
      value: `## Configuration

To set up this webhook trigger:

1. **Copy the Webhook URL** displayed below:
\`\`\`text
{{webhookUrl}}
\`\`\`

2. Navigate to [MeetGeek Integrations Page](https://app.meetgeek.ai/integrations)
3. Open the **Public API** section
4. Paste the webhook URL into the **Webhook URL** field
5. Click **Save** to activate the webhook`
    })
  },
  sampleData: {
    meeting_id: "99e727a1-fd4b-4526-9cfb-3a8a0bdffeb7",
    message: "File analyzed successfully"
  },
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/meetgeek-ai/src/index.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), meetgeekAi = (0, import_pieces_framework10.createPiece)({
  displayName: "Meetgeek",
  auth: meetgeekaiAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/meetgeek-ai.png",
  categories: [
    import_pieces_framework11.PieceCategory.ARTIFICIAL_INTELLIGENCE,
    import_pieces_framework11.PieceCategory.COMMUNICATION
  ],
  description: "AI-powered meeting assistant that automates note-taking, summarization, and insights generation for your meetings.",
  authors: ["sanket-a11y"],
  actions: [
    getHighlights,
    getMeetingDetails,
    getMeetingsSummaryInsights,
    getTeamMeetings,
    getTranscript,
    uploadRecording
  ],
  triggers: [newMeeting]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  meetgeekAi
});
