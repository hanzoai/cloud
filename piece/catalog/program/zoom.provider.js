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

// ../ap/packages/pieces/community/zoom/src/index.ts
var index_exports = {};
__export(index_exports, {
  zoom: () => zoom,
  zoomAuth: () => zoomAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/zoom/src/lib/actions/create-meeting.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var defaults = {
  agenda: "My Meeting",
  default_password: !1,
  duration: 30,
  pre_schedule: !1,
  settings: {
    allow_multiple_devices: !0,
    approval_type: 2,
    audio: "telephony",
    calendar_type: 1,
    close_registration: !1,
    email_notification: !0,
    host_video: !0,
    join_before_host: !1,
    meeting_authentication: !0,
    mute_upon_entry: !1,
    participant_video: !1,
    private_meeting: !1,
    registrants_confirmation_email: !0,
    registrants_email_notification: !0,
    registration_type: 1,
    show_share_button: !0,
    host_save_video_order: !0
  },
  timezone: "UTC",
  type: 2
}, action = () => (0, import_pieces_framework.createAction)({
  auth: zoomAuth,
  name: "zoom_create_meeting",
  // Must be a unique across the piece, this shouldn't be changed.
  classification: "WRITE",
  displayName: "Create Zoom Meeting",
  description: "Create a new Zoom Meeting",
  audience: "both",
  aiMetadata: { description: "Schedules a new Zoom meeting on the authenticated user's account, returning the meeting ID and join URL. Use to set up a video call; only the topic is required, with optional start time, duration, password, and audio/recording settings. Each call creates a distinct meeting, so it is not idempotent.", idempotent: !1 },
  props: {
    topic: import_pieces_framework.Property.ShortText({
      displayName: "Meeting's topic",
      description: "The meeting's topic",
      required: !0
    }),
    start_time: import_pieces_framework.Property.ShortText({
      displayName: "Start Time",
      description: "Meeting start date-time",
      required: !1
    }),
    duration: import_pieces_framework.Property.Number({
      displayName: "Duration (in Minutes)",
      description: "Duration of the meeting",
      required: !1
    }),
    auto_recording: import_pieces_framework.Property.StaticDropdown({
      displayName: "Auto Recording",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Local", value: "local" },
          { label: "Cloud", value: "cloud" },
          { label: "None", value: "none" }
        ]
      }
    }),
    audio: import_pieces_framework.Property.StaticDropdown({
      displayName: "Audio",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Both telephony and VoIP", value: "both" },
          { label: "Telephony only", value: "telephony" },
          { label: "VoIP only", value: "voip" },
          { label: "Third party audio conference", value: "thirdParty" }
        ]
      }
    }),
    agenda: import_pieces_framework.Property.LongText({
      displayName: "Agenda",
      description: "The meeting's agenda",
      required: !1
    }),
    password: import_pieces_framework.Property.ShortText({
      displayName: "Password",
      description: "The password required to join the meeting. By default, a password can only have a maximum length of 10 characters and only contain alphanumeric characters and the @, -, _, and * characters.",
      required: !1
    }),
    pre_schedule: import_pieces_framework.Property.Checkbox({
      displayName: "Pre Schedule",
      description: "Whether the prescheduled meeting was created via the GSuite app.",
      required: !1
    }),
    schedule_for: import_pieces_framework.Property.ShortText({
      displayName: "Schedule for",
      description: "The email address or user ID of the user to schedule a meeting for.",
      required: !1
    }),
    join_url: import_pieces_framework.Property.LongText({
      displayName: "Join URL",
      description: "URL for participants to join the meeting.",
      required: !1
    })
  },
  async run(context) {
    let body = __spreadValues(__spreadValues({}, defaults), context.propsValue);
    context.propsValue.auto_recording && (body.settings.auto_recording = context.propsValue.auto_recording), context.propsValue.audio && (body.settings.audio = context.propsValue.audio), delete body.auth;
    let request = {
      method: import_pieces_common.HttpMethod.POST,
      url: "https://api.zoom.us/v2/users/me/meetings",
      body,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: context.auth.access_token
      },
      queryParams: {}
    }, result = await import_pieces_common.httpClient.sendRequest(request);
    return result.status === 201 ? result.body : result;
  }
}), zoomCreateMeeting = action();

// ../ap/packages/pieces/community/zoom/src/lib/actions/create-meeting-registrant.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/zoom/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var zoomMeetingDropdown = import_pieces_framework2.Property.Dropdown({
  displayName: "Meeting",
  description: "Select a meeting from your Zoom account.",
  required: !0,
  auth: zoomAuth,
  refreshers: ["auth"],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Zoom account first.",
        options: []
      };
    let accessToken = auth.access_token, options = [], nextPageToken = "";
    try {
      do {
        let queryParams = {
          type: "scheduled",
          page_size: "300"
        };
        nextPageToken && (queryParams.next_page_token = nextPageToken);
        let res = await import_pieces_common2.httpClient.sendRequest({
          method: import_pieces_common2.HttpMethod.GET,
          url: "https://api.zoom.us/v2/users/me/meetings",
          authentication: {
            type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
            token: accessToken
          },
          queryParams
        });
        options.push(
          ...res.body.meetings.map((m) => ({
            label: m.topic || `Meeting ${m.id}`,
            value: String(m.id)
          }))
        ), nextPageToken = res.body.next_page_token;
      } while (nextPageToken);
    } catch (e) {
      return {
        disabled: !0,
        placeholder: "Could not load meetings. Ensure your Zoom app has the meeting:read:list_meetings scope.",
        options: []
      };
    }
    return { options };
  }
}), getRegistarantProps = () => ({
  meeting_id: import_pieces_framework2.Property.ShortText({
    displayName: "Meeting ID",
    description: "The meeting ID.",
    required: !0
  }),
  first_name: import_pieces_framework2.Property.ShortText({
    displayName: "First name",
    description: "The registrant's first name.",
    required: !0
  }),
  last_name: import_pieces_framework2.Property.ShortText({
    displayName: "Last name",
    description: "The registrant's last name.",
    required: !1
  }),
  email: import_pieces_framework2.Property.ShortText({
    displayName: "Email",
    description: "The registrant's email address.",
    required: !0
  }),
  address: import_pieces_framework2.Property.ShortText({
    displayName: "Address",
    description: "The registrant's address",
    required: !1
  }),
  city: import_pieces_framework2.Property.ShortText({
    displayName: "City",
    description: "The registrant's city",
    required: !1
  }),
  state: import_pieces_framework2.Property.ShortText({
    displayName: "State",
    description: "The registrant's state or province.",
    required: !1
  }),
  zip: import_pieces_framework2.Property.ShortText({
    displayName: "Zip",
    description: "The registrant's zip or postal code.",
    required: !1
  }),
  country: import_pieces_framework2.Property.ShortText({
    displayName: "Country",
    description: "The registrant's two-letter country code.",
    required: !1
  }),
  phone: import_pieces_framework2.Property.ShortText({
    displayName: "Phone",
    description: "The registrant's phone number.",
    required: !1
  }),
  comments: import_pieces_framework2.Property.LongText({
    displayName: "Comments",
    description: "The registrant's questions and comments.",
    required: !1
  }),
  custom_questions: import_pieces_framework2.Property.Object({
    displayName: "Custom questions",
    description: "",
    required: !1
  }),
  industry: import_pieces_framework2.Property.ShortText({
    displayName: "Industry",
    description: "The registrant's industry.",
    required: !1
  }),
  job_title: import_pieces_framework2.Property.ShortText({
    displayName: "Job title",
    description: "The registrant's job title.",
    required: !1
  }),
  no_of_employees: import_pieces_framework2.Property.StaticDropdown({
    displayName: "No of employees",
    description: "The registrant's number of employees.",
    required: !1,
    options: {
      disabled: !1,
      options: [
        { label: "1-20", value: "1-20" },
        { label: "21-50", value: "21-50" },
        { label: "51-100", value: "51-100" },
        { label: "101-500", value: "101-500" },
        { label: "500-1,000", value: "500-1,000" },
        { label: "1,001-5,000", value: "1,001-5,000" },
        { label: "5,001-10,000", value: "5,001-10,000" },
        { label: "More than 10,000", value: "More than 10,000" }
      ]
    }
  }),
  org: import_pieces_framework2.Property.ShortText({
    displayName: "Organization",
    description: "The registrant's organization.",
    required: !1
  }),
  purchasing_time_frame: import_pieces_framework2.Property.StaticDropdown({
    displayName: "Purchasing time frame",
    description: "The registrant's purchasing time frame.",
    required: !1,
    options: {
      disabled: !1,
      options: [
        { label: "Within a month", value: "Within a month" },
        { label: "1-3 months", value: "1-3 months" },
        { label: "4-6 months", value: "4-6 months" },
        { label: "More than 6 months", value: "More than 6 months" },
        { label: "No timeframe", value: "No timeframe" }
      ]
    }
  }),
  role_in_purchase_process: import_pieces_framework2.Property.StaticDropdown({
    displayName: "Role in purchase process",
    description: "The registrant's role in the purchase process.",
    required: !1,
    options: {
      disabled: !1,
      options: [
        { label: "Decision Maker", value: "Decision Maker" },
        { label: "Evaluator/Recommender", value: "Evaluator/Recommender" },
        { label: "Influencer", value: "Influencer" },
        { label: "Not involved", value: "Not involved" }
      ]
    }
  })
});

// ../ap/packages/pieces/community/zoom/src/lib/actions/create-meeting-registrant.ts
var zoomCreateMeetingRegistrant = (0, import_pieces_framework3.createAction)({
  auth: zoomAuth,
  name: "zoom_create_meeting_registrant",
  classification: "WRITE",
  displayName: "Create Zoom Meeting Registrant",
  description: "Create and submit a user's registration to a meeting.",
  audience: "both",
  aiMetadata: { description: "Registers an attendee for an existing Zoom meeting identified by its meeting ID, capturing their name, email, and optional profile/custom-question fields. Use to add a participant to a meeting that has registration enabled. Each call submits a new registration, so it is not idempotent.", idempotent: !1 },
  props: getRegistarantProps(),
  async run(context) {
    let body = {
      first_name: context.propsValue.first_name,
      last_name: context.propsValue.last_name,
      email: context.propsValue.email,
      address: context.propsValue.address,
      city: context.propsValue.city,
      state: context.propsValue.state,
      zip: context.propsValue.zip,
      country: context.propsValue.country,
      phone: context.propsValue.phone,
      comments: context.propsValue.comments,
      industry: context.propsValue.industry,
      job_title: context.propsValue.job_title,
      no_of_employees: context.propsValue.no_of_employees,
      org: context.propsValue.org,
      purchasing_time_frame: context.propsValue.purchasing_time_frame,
      role_in_purchase_process: context.propsValue.role_in_purchase_process
    };
    context.propsValue.custom_questions && Object.keys(context.propsValue.custom_questions).length > 0 && (body.custom_questions = Object.entries(
      context.propsValue.custom_questions
    ).map(([key, value]) => ({ title: key, value })));
    let request = {
      method: import_pieces_common3.HttpMethod.POST,
      url: `https://api.zoom.us/v2/meetings/${context.propsValue.meeting_id}/registrants`,
      body,
      authentication: {
        type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
        token: context.auth.access_token
      },
      queryParams: {}
    }, result = await import_pieces_common3.httpClient.sendRequest(request);
    return result.status === 201 ? result.body : result;
  }
});

// ../ap/packages/pieces/community/zoom/src/lib/actions/find-meeting.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var zoomFindMeeting = (0, import_pieces_framework4.createAction)({
  auth: zoomAuth,
  name: "zoom_find_meeting",
  classification: "READ",
  displayName: "Find Zoom Meeting",
  description: "Retrieve the details of an existing meeting.",
  audience: "both",
  aiMetadata: { description: "Fetches the full details of an existing Zoom meeting by its meeting ID. Use to look up a meeting before acting on it; optionally target a specific occurrence of a recurring meeting or include all previous occurrences. Read-only and idempotent.", idempotent: !0 },
  props: {
    meeting_id: zoomMeetingDropdown,
    occurrence_id: import_pieces_framework4.Property.ShortText({
      displayName: "Occurrence ID",
      description: "Meeting Occurrence ID. Provide this field to view meeting details of a particular occurrence of the recurring meeting.",
      required: !1
    }),
    show_previous_occurrences: import_pieces_framework4.Property.Checkbox({
      displayName: "Show Previous Occurrences",
      description: "Set to true if you would like to view meeting details of all previous occurrences of a recurring meeting.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let cleanMeetingId = context.propsValue.meeting_id, queryParams = {};
    return context.propsValue.occurrence_id && (queryParams.occurrence_id = context.propsValue.occurrence_id), context.propsValue.show_previous_occurrences && (queryParams.show_previous_occurrences = "true"), (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.GET,
      url: `https://api.zoom.us/v2/meetings/${cleanMeetingId}`,
      authentication: {
        type: import_pieces_common4.AuthenticationType.BEARER_TOKEN,
        token: context.auth.access_token
      },
      queryParams
    })).body;
  }
});

// ../ap/packages/pieces/community/zoom/src/lib/actions/update-meeting.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var zoomUpdateMeeting = (0, import_pieces_framework5.createAction)({
  auth: zoomAuth,
  name: "zoom_update_meeting",
  classification: "WRITE",
  displayName: "Update Zoom Meeting",
  description: "Update the details of an existing meeting.",
  audience: "both",
  aiMetadata: { description: "Modifies an existing Zoom meeting identified by its meeting ID, applying only the fields you supply (topic, start time, duration, timezone, password, and audio/video/recording/waiting-room settings). Use to reschedule or reconfigure a meeting. Repeating the call with the same values produces the same end state, so it is idempotent.", idempotent: !0 },
  props: {
    meeting_id: zoomMeetingDropdown,
    topic: import_pieces_framework5.Property.ShortText({
      displayName: "Meeting's topic",
      description: "The meeting's topic",
      required: !1
    }),
    start_time: import_pieces_framework5.Property.ShortText({
      displayName: "Start Time",
      description: "Meeting start date-time (ISO 8601 format, e.g., 2023-05-01T12:00:00Z)",
      required: !1
    }),
    duration: import_pieces_framework5.Property.Number({
      displayName: "Duration (in Minutes)",
      description: "Duration of the meeting",
      required: !1
    }),
    timezone: import_pieces_framework5.Property.ShortText({
      displayName: "Timezone",
      description: "Timezone for the meeting (e.g., UTC, America/New_York)",
      required: !1
    }),
    auto_recording: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Auto Recording",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Local", value: "local" },
          { label: "Cloud", value: "cloud" },
          { label: "None", value: "none" }
        ]
      }
    }),
    audio: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Audio",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Both telephony and VoIP", value: "both" },
          { label: "Telephony only", value: "telephony" },
          { label: "VoIP only", value: "voip" },
          { label: "Third party audio conference", value: "thirdParty" }
        ]
      }
    }),
    agenda: import_pieces_framework5.Property.LongText({
      displayName: "Agenda",
      description: "The meeting's agenda",
      required: !1
    }),
    password: import_pieces_framework5.Property.ShortText({
      displayName: "Password",
      description: "The password required to join the meeting. By default, a password can only have a maximum length of 10 characters and only contain alphanumeric characters and the @, -, _, and * characters.",
      required: !1
    }),
    host_video: import_pieces_framework5.Property.Checkbox({
      displayName: "Host Video",
      description: "Whether to start video when the host joins the meeting.",
      required: !1
    }),
    participant_video: import_pieces_framework5.Property.Checkbox({
      displayName: "Participant Video",
      description: "Whether to start video when participants join the meeting.",
      required: !1
    }),
    join_before_host: import_pieces_framework5.Property.Checkbox({
      displayName: "Join Before Host",
      description: "Whether participants can join the meeting before the host starts the meeting.",
      required: !1
    }),
    mute_upon_entry: import_pieces_framework5.Property.Checkbox({
      displayName: "Mute Upon Entry",
      description: "Whether to mute participants upon entry.",
      required: !1
    }),
    waiting_room: import_pieces_framework5.Property.Checkbox({
      displayName: "Waiting Room",
      description: "Whether to enable waiting room.",
      required: !1
    })
  },
  async run(context) {
    let cleanMeetingId = context.propsValue.meeting_id, body = {};
    context.propsValue.topic && (body.topic = context.propsValue.topic), context.propsValue.start_time && (body.start_time = context.propsValue.start_time), context.propsValue.duration && (body.duration = context.propsValue.duration), context.propsValue.timezone && (body.timezone = context.propsValue.timezone), context.propsValue.agenda && (body.agenda = context.propsValue.agenda), context.propsValue.password && (body.password = context.propsValue.password);
    let settings = {}, hasSettings = !1;
    return context.propsValue.auto_recording && (settings.auto_recording = context.propsValue.auto_recording, hasSettings = !0), context.propsValue.audio && (settings.audio = context.propsValue.audio, hasSettings = !0), context.propsValue.host_video !== void 0 && (settings.host_video = context.propsValue.host_video, hasSettings = !0), context.propsValue.participant_video !== void 0 && (settings.participant_video = context.propsValue.participant_video, hasSettings = !0), context.propsValue.join_before_host !== void 0 && (settings.join_before_host = context.propsValue.join_before_host, hasSettings = !0), context.propsValue.mute_upon_entry !== void 0 && (settings.mute_upon_entry = context.propsValue.mute_upon_entry, hasSettings = !0), context.propsValue.waiting_room !== void 0 && (settings.waiting_room = context.propsValue.waiting_room, hasSettings = !0), hasSettings && (body.settings = settings), await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.PATCH,
      url: `https://api.zoom.us/v2/meetings/${cleanMeetingId}`,
      body,
      authentication: {
        type: import_pieces_common5.AuthenticationType.BEARER_TOKEN,
        token: context.auth.access_token
      }
    }), { success: !0, message: "Meeting updated successfully" };
  }
});

// ../ap/packages/pieces/community/zoom/src/index.ts
var zoomAuth = import_pieces_framework6.PieceAuth.OAuth2({
  description: `
  1. Go to [marketplace.zoom.us](https://marketplace.zoom.us/) and log in to your account.
  2. In the upper-right corner, click **Develop** then **Build App**.
  3. Select **General App**.
  4. Copy the Client ID and Client Secret.Add Redirect URL and press continue.
  5. Go to **Scopes** from left side bar and add **meeting:write:meeting**, **meeting:read:meeting**, **meeting:read:list_meetings**, **meeting:update:meeting**, and **meeting:write:registrant** as scopes.`,
  authUrl: "https://zoom.us/oauth/authorize",
  tokenUrl: "https://zoom.us/oauth/token",
  required: !0,
  // scope: ['meeting:write:admin', 'meeting:write'],
  scope: []
}), zoom = (0, import_pieces_framework6.createPiece)({
  displayName: "Zoom",
  description: "Video conferencing, web conferencing, webinars, screen sharing",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/zoom.png",
  categories: [import_pieces_framework7.PieceCategory.COMMUNICATION],
  actions: [
    zoomCreateMeeting,
    zoomCreateMeetingRegistrant,
    zoomFindMeeting,
    zoomUpdateMeeting,
    (0, import_pieces_common6.createCustomApiCallAction)({
      baseUrl: () => "https://api.zoom.us/v2",
      auth: zoomAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.access_token}`
      })
    })
  ],
  auth: zoomAuth,
  authors: ["kanarelo", "kishanprmr", "MoShizzle", "khaledmashaly", "abuaboud", "murex971"],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  zoom,
  zoomAuth
});
