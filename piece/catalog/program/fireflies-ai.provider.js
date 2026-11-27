"use strict";
var __defProp = Object.defineProperty, __defProps = Object.defineProperties, __getOwnPropDesc = Object.getOwnPropertyDescriptor, __getOwnPropDescs = Object.getOwnPropertyDescriptors, __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
}, __spreadProps = (a, b) => __defProps(a, __getOwnPropDescs(b));
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

// ../ap/packages/pieces/community/fireflies-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  firefliesAi: () => firefliesAi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_framework10 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/fireflies-ai/src/lib/actions/find-meeting-by-id.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/fireflies-ai/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), markdownDescription = `
To use Fireflies.ai, you need to get an API key:
1. Login to your account at https://fireflies.ai.
2. Navigate to Settings > Developer Settings in the left sidebar.
3. Copy the API key from the API Key section.
`, firefliesAiAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: markdownDescription,
  required: !0
});

// ../ap/packages/pieces/community/fireflies-ai/src/lib/common/queries.ts
var getTranscript = `
	query Transcript($transcriptId: String!) 
    {
		transcript(id: $transcriptId) 
        {
			id
			dateString
			privacy
			speakers 
            {
                id
                name
			}
			title
			host_email
			organizer_email
			calendar_id
			user 
            {
                user_id
                email
                name
                num_transcripts
                recent_meeting
                minutes_consumed
                is_admin
                integrations
			}
			fireflies_users
			participants
			date
			transcript_url
			audio_url
			video_url
			duration
			meeting_attendees 
            {
                displayName
                email
                phoneNumber
                name
                location
			}
			summary 
            {
                keywords
                action_items
                outline
                shorthand_bullet
                overview
                bullet_gist
                gist
                short_summary
                short_overview
                meeting_type
                topics_discussed
                transcript_chapters
			}
			cal_id
			calendar_type
			meeting_info 
            {
                fred_joined
                silent_meeting
                summary_status
			}
			meeting_link
		}
	}
`, getUser = `
query User($userId: String!) 
{
    user(id: $userId) 
    {
        user_id
        recent_transcript
        recent_meeting
        num_transcripts
        name
        minutes_consumed
        is_admin
        integrations
        email
        user_groups 
        {
            name
            handle
        }
    }
}`;

// ../ap/packages/pieces/community/fireflies-ai/src/lib/common/index.ts
var BASE_URL = "https://api.fireflies.ai/graphql";

// ../ap/packages/pieces/community/fireflies-ai/src/lib/actions/find-meeting-by-id.ts
var findMeetingByIdAction = (0, import_pieces_framework2.createAction)({
  auth: firefliesAiAuth,
  name: "find-meeting-by-id",
  displayName: "Find Meeting by ID",
  description: "Finds a specific meeting by ID.",
  audience: "both",
  aiMetadata: { description: "Fetches a single Fireflies meeting transcript (with summary, speakers, attendees, and media URLs) by its exact transcript/meeting ID. Use when you already have a known meeting ID and want its full details; if you only have search criteria like title or date, use the search action instead. Read-only and idempotent.", idempotent: !0 },
  props: {
    meetingId: import_pieces_framework2.Property.ShortText({
      displayName: "Meeting ID",
      description: "The ID of the meeting to retrieve.",
      required: !0
    })
  },
  async run(context) {
    return (await import_pieces_common.httpClient.sendRequest({
      url: BASE_URL,
      method: import_pieces_common.HttpMethod.POST,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      },
      body: {
        query: getTranscript,
        variables: {
          transcriptId: context.propsValue.meetingId
        }
      }
    })).body.data.transcript;
  }
});

// ../ap/packages/pieces/community/fireflies-ai/src/lib/actions/find-recent-meeting.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var findRecentMeetingAction = (0, import_pieces_framework3.createAction)({
  auth: firefliesAiAuth,
  name: "find_recent_meeting",
  displayName: "Find Recent Meeting",
  description: "Retrieves the latest meeting for a user.",
  audience: "both",
  aiMetadata: { description: "Returns the most recently transcribed Fireflies meeting for the authenticated account, including its full transcript and summary. Use when the agent wants the latest meeting without knowing its ID; takes no inputs. Returns a found:false result when the account has no recent meeting. Read-only and idempotent.", idempotent: !0 },
  props: {},
  async run(context) {
    let userResponse = await import_pieces_common2.httpClient.sendRequest({
      url: BASE_URL,
      method: import_pieces_common2.HttpMethod.POST,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      },
      body: {
        query: `
				query User
				{
					user
					{
						user_id
						recent_transcript
						recent_meeting
						num_transcripts
						name
						minutes_consumed
						is_admin
						integrations
						email
					}
				}`,
        variables: {}
      }
    });
    return console.log(JSON.stringify(userResponse, null, 2)), (0, import_pieces_framework4.isNil)(userResponse.body.data.user.recent_meeting) ? {
      found: !1,
      meeting: {}
    } : {
      found: !0,
      meeting: (await import_pieces_common2.httpClient.sendRequest({
        url: BASE_URL,
        method: import_pieces_common2.HttpMethod.POST,
        authentication: {
          type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
          token: context.auth.secret_text
        },
        body: {
          query: getTranscript,
          variables: {
            transcriptId: userResponse.body.data.user.recent_meeting
          }
        }
      })).body.data.transcript
    };
  }
});

// ../ap/packages/pieces/community/fireflies-ai/src/lib/actions/find-meeting-by-query.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var findMeetingByQueryAction = (0, import_pieces_framework5.createAction)({
  auth: firefliesAiAuth,
  name: "find_meeting_by_query",
  displayName: "Find Meeting by Call Deatils",
  description: "Searches meetings based on provided parameters.",
  audience: "both",
  aiMetadata: { description: "Searches Fireflies meeting transcripts, optionally filtered by title, host email, participant email, and/or date, paging through all matches. Use when locating meetings by attributes rather than a known ID; leaving all filters empty returns the full meeting history. Read-only and idempotent.", idempotent: !0 },
  props: {
    title: import_pieces_framework5.Property.ShortText({
      displayName: "Meeting Title",
      required: !1
    }),
    hostEmail: import_pieces_framework5.Property.ShortText({
      displayName: "Host Email",
      description: "Filter meetings by host email.",
      required: !1
    }),
    participantEmail: import_pieces_framework5.Property.ShortText({
      displayName: "Participant Email",
      description: "Filter meetings by participant email",
      required: !1
    }),
    date: import_pieces_framework5.Property.DateTime({
      displayName: "Date",
      description: "Filter meetings on this date (YYYY-MM-DD).",
      required: !1
    })
  },
  async run({ propsValue, auth }) {
    var _a, _b;
    let filterVariables = {};
    if (propsValue.title && (filterVariables.title = propsValue.title), propsValue.hostEmail && (filterVariables.hostEmail = propsValue.hostEmail), propsValue.participantEmail && (filterVariables.participantEmail = propsValue.participantEmail), propsValue.date) {
      let dateMs = new Date(propsValue.date).getTime();
      filterVariables.date = dateMs;
    }
    let query = `
			query Transcripts(
				$title: String
				$hostEmail: String
				$participantEmail: String
				$date: Float
				$limit: Int
  				$skip: Int
			) {
				transcripts(
					title: $title
					host_email: $hostEmail
					participant_email: $participantEmail
					date: $date
					limit: $limit
					skip: $skip
				) {
					id
					dateString
					privacy
					speakers 
					{
						id
						name
					}
					title
					host_email
					organizer_email
					calendar_id
					user 
					{
						user_id
						email
						name
						num_transcripts
						recent_meeting
						minutes_consumed
						is_admin
						integrations
					}
					fireflies_users
					participants
					date
					transcript_url
					audio_url
					video_url
					duration
					meeting_attendees 
					{
						displayName
						email
						phoneNumber
						name
						location
					}
					summary 
					{
						keywords
						action_items
						outline
						shorthand_bullet
						overview
						bullet_gist
						gist
						short_summary
						short_overview
						meeting_type
						topics_discussed
						transcript_chapters
					}
					cal_id
					calendar_type
					meeting_link
				}
			}
		`, limit = 50, skip = 0, hasMore = !0, meetings = [];
    for (; hasMore; ) {
      let variables = __spreadProps(__spreadValues({}, filterVariables), {
        limit,
        skip
      }), response = await import_pieces_common3.httpClient.sendRequest({
        url: BASE_URL,
        method: import_pieces_common3.HttpMethod.POST,
        authentication: {
          type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
          token: auth.secret_text
        },
        body: {
          query,
          variables
        }
      }), transcripts = ((_b = (_a = response == null ? void 0 : response.body) == null ? void 0 : _a.data) == null ? void 0 : _b.transcripts) || [];
      transcripts.length === 0 ? hasMore = !1 : (meetings.push(...transcripts), skip += transcripts.length);
    }
    return {
      found: meetings.length !== 0,
      meetings
    };
  }
});

// ../ap/packages/pieces/community/fireflies-ai/src/lib/actions/upload-audio.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var uploadAudioAction = (0, import_pieces_framework6.createAction)({
  auth: firefliesAiAuth,
  name: "upload_audio",
  displayName: "Upload Audio",
  description: "Creates a new meeeting in Fireflies for transcription (requires a publicly accessible URL).",
  audience: "both",
  aiMetadata: { description: "Submits a publicly accessible audio/video URL (mp3, mp4, wav, m4a, ogg) to Fireflies to create a new meeting and transcribe it. Use when the agent has a recording it wants transcribed and analyzed. The API accepts only URLs, not direct file uploads. Not idempotent: each call queues a new transcription job.", idempotent: !1 },
  props: {
    audioUrl: import_pieces_framework6.Property.ShortText({
      displayName: "Audio URL",
      description: "The publicly accessible URL to your audio file (mp3, mp4, wav, m4a, ogg). Fireflies API only accepts URLs, not direct file uploads. For private files, consider using signed URLs with short expiry times.",
      required: !0
    }),
    title: import_pieces_framework6.Property.ShortText({
      displayName: "Title",
      required: !0
    })
  },
  async run({ propsValue, auth }) {
    let query = `
			mutation uploadAudio($input: AudioUploadInput) {
				uploadAudio(input: $input) {
					success
					title
					message
				}
			}
		`, input = {
      url: propsValue.audioUrl,
      title: propsValue.title
    };
    return (await import_pieces_common4.httpClient.sendRequest({
      url: BASE_URL,
      method: import_pieces_common4.HttpMethod.POST,
      authentication: {
        type: import_pieces_common4.AuthenticationType.BEARER_TOKEN,
        token: auth.secret_text
      },
      body: {
        query,
        variables: {
          input
        }
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/fireflies-ai/src/lib/actions/get-user-details.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var getUserDetailsAction = (0, import_pieces_framework7.createAction)({
  auth: firefliesAiAuth,
  name: "get-user-details",
  displayName: "Get User Details",
  description: "Retrieves profile information by ID.",
  audience: "both",
  aiMetadata: { description: "Fetches a Fireflies user profile (name, email, admin status, transcript counts, integrations) by user ID. Use when the agent needs account details for a specific user; requires the exact user ID. Read-only and idempotent.", idempotent: !0 },
  props: {
    userId: import_pieces_framework7.Property.ShortText({
      displayName: "User ID",
      required: !0
    })
  },
  async run(context) {
    return (await import_pieces_common5.httpClient.sendRequest({
      url: BASE_URL,
      method: import_pieces_common5.HttpMethod.POST,
      authentication: {
        type: import_pieces_common5.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      },
      body: {
        query: getUser,
        variables: {
          userId: context.propsValue.userId
        }
      }
    })).body.data.user;
  }
});

// ../ap/packages/pieces/community/fireflies-ai/src/lib/triggers/new-transcription-complete.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common");
var newTranscriptionCompletedTrigger = (0, import_pieces_framework8.createTrigger)({
  auth: firefliesAiAuth,
  name: "new_transcription_completed",
  displayName: "New Transcription Completed",
  description: "Triggered when a new meeting is transcribed.",
  aiMetadata: {
    description: "Fires when Fireflies finishes transcribing a meeting, emitting the completed transcript with its summary, speakers, attendees, and media URLs. Represents a freshly available, fully processed meeting record. Requires a manually configured Fireflies webhook."
  },
  props: {
    webhookInstructions: import_pieces_framework8.Property.MarkDown({
      value: `
			## Fireflies.ai Webhook Setup
			To use this trigger, you need to manually set up a webhook in your Fireflies.ai account:

			1. Login to your Fireflies.ai account.
			2. Navigate to **Settings** > **Developer Settings** in the left sidebar.
			3. Enter the following URL in the webhooks field:
			\`\`\`text
			{{webhookUrl}}
			\`\`\`
			4. Click Save to register the webhook.

			This webhook will be triggered when a meeting transcription is completed.
			`
    })
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  sampleData: void 0,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async test(context) {
    return (await import_pieces_common6.httpClient.sendRequest(
      {
        url: BASE_URL,
        method: import_pieces_common6.HttpMethod.POST,
        authentication: {
          type: import_pieces_common6.AuthenticationType.BEARER_TOKEN,
          token: context.auth.secret_text
        },
        body: {
          query: `
		query Transcripts(
		$limit: Int
		$skip: Int 
		){
			transcripts(
				limit: $limit
				skip: $skip
			){
				
				id
			dateString
			privacy
			speakers 
            {
                id
                name
			}
			title
			host_email
			organizer_email
			calendar_id
			user 
            {
                user_id
                email
                name
                num_transcripts
                recent_meeting
                minutes_consumed
                is_admin
                integrations
			}
			fireflies_users
			participants
			date
			transcript_url
			audio_url
			video_url
			duration
			meeting_attendees 
            {
                displayName
                email
                phoneNumber
                name
                location
			}
			summary 
            {
                keywords
                action_items
                outline
                shorthand_bullet
                overview
                bullet_gist
                gist
                short_summary
                short_overview
                meeting_type
                topics_discussed
                transcript_chapters
			}
			cal_id
			calendar_type
			meeting_info 
            {
                fred_joined
                silent_meeting
                summary_status
			}
			meeting_link
		}
		}`,
          variables: {
            limit: 5,
            skip: 0
          }
        }
      }
    )).body.data.transcripts;
  },
  async run(context) {
    let payload = context.payload.body;
    return payload.eventType !== "Transcription completed" ? [] : [(await import_pieces_common6.httpClient.sendRequest({
      url: BASE_URL,
      method: import_pieces_common6.HttpMethod.POST,
      authentication: {
        type: import_pieces_common6.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      },
      body: {
        query: getTranscript,
        variables: {
          transcriptId: payload.meetingId
        }
      }
    })).body.data.transcript];
  }
});

// ../ap/packages/pieces/community/fireflies-ai/src/index.ts
var firefliesAi = (0, import_pieces_framework9.createPiece)({
  displayName: "Fireflies.ai",
  description: "Meeting assistant that automatically records, transcribes, and analyzes conversations",
  logoUrl: "https://cdn.activepieces.com/pieces/fireflies-ai.png",
  authors: ["AnkitSharmaOnGithub"],
  auth: firefliesAiAuth,
  actions: [
    findMeetingByIdAction,
    findRecentMeetingAction,
    findMeetingByQueryAction,
    uploadAudioAction,
    getUserDetailsAction
  ],
  triggers: [newTranscriptionCompletedTrigger],
  categories: [import_pieces_framework10.PieceCategory.PRODUCTIVITY]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  firefliesAi
});
