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

// ../ap/packages/pieces/community/granola/src/index.ts
var index_exports = {};
__export(index_exports, {
  granola: () => granola,
  granolaAuth: () => granolaAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/granola/src/lib/actions/list-notes.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/granola/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common");
var BASE_URL = "https://public-api.granola.ai/v1";
async function granolaApiCall({
  token,
  method,
  path,
  queryParams
}) {
  return await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${BASE_URL}${path}`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token
    },
    queryParams
  });
}
function flattenNote(note) {
  var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n, _o, _p;
  return {
    id: note.id,
    title: note.title,
    owner_name: (_b = (_a = note.owner) == null ? void 0 : _a.name) != null ? _b : null,
    owner_email: (_d = (_c = note.owner) == null ? void 0 : _c.email) != null ? _d : null,
    created_at: note.created_at,
    updated_at: note.updated_at,
    calendar_event_title: (_f = (_e = note.calendar_event) == null ? void 0 : _e.event_title) != null ? _f : null,
    calendar_organiser: (_h = (_g = note.calendar_event) == null ? void 0 : _g.organiser) != null ? _h : null,
    calendar_event_id: (_j = (_i = note.calendar_event) == null ? void 0 : _i.calendar_event_id) != null ? _j : null,
    scheduled_start_time: (_l = (_k = note.calendar_event) == null ? void 0 : _k.scheduled_start_time) != null ? _l : null,
    scheduled_end_time: (_n = (_m = note.calendar_event) == null ? void 0 : _m.scheduled_end_time) != null ? _n : null,
    attendees: note.attendees ? note.attendees.map((a) => {
      var _a2;
      return (_a2 = a.name) != null ? _a2 : a.email;
    }).join(", ") : null,
    folders: note.folder_membership ? note.folder_membership.map((f) => f.name).join(", ") : null,
    summary_text: (_o = note.summary_text) != null ? _o : null,
    summary_markdown: (_p = note.summary_markdown) != null ? _p : null
  };
}
function flattenNoteWithTranscript(note) {
  let flat = flattenNote(note);
  return __spreadProps(__spreadValues({}, flat), {
    transcript: note.transcript ? note.transcript.map((t) => `[${t.speaker.source}] ${t.text}`).join(`
`) : null
  });
}

// ../ap/packages/pieces/community/granola/src/lib/actions/list-notes.ts
var listNotesAction = (0, import_pieces_framework.createAction)({
  auth: granolaAuth,
  name: "list_notes",
  displayName: "List Notes",
  description: "List your meeting notes with optional date filters.",
  audience: "both",
  aiMetadata: {
    description: "Lists the authenticated user's Granola meeting notes, returning a page of note summaries. With no date filters it returns the most recent notes; supply created-after/created-before/updated-after bounds to narrow to a time window (e.g. notes modified since a given time). Use it to discover notes and their IDs before fetching details with Get Note. Page size is capped at 30. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    created_after: import_pieces_framework.Property.DateTime({
      displayName: "Created After",
      description: "Only return notes created after this date. Leave empty to return all notes.",
      required: !1
    }),
    created_before: import_pieces_framework.Property.DateTime({
      displayName: "Created Before",
      description: "Only return notes created before this date. Leave empty for no upper limit.",
      required: !1
    }),
    updated_after: import_pieces_framework.Property.DateTime({
      displayName: "Updated After",
      description: "Only return notes updated after this date. Useful to find recently modified notes.",
      required: !1
    }),
    page_size: import_pieces_framework.Property.Number({
      displayName: "Page Size",
      description: "Number of notes to return per page (1\u201330).",
      required: !1,
      defaultValue: 10
    })
  },
  async run(context) {
    var _a, _b;
    let queryParams = {};
    context.propsValue.created_after && (queryParams.created_after = context.propsValue.created_after), context.propsValue.created_before && (queryParams.created_before = context.propsValue.created_before), context.propsValue.updated_after && (queryParams.updated_after = context.propsValue.updated_after);
    let pageSize = Math.min(Math.max((_a = context.propsValue.page_size) != null ? _a : 10, 1), 30);
    queryParams.page_size = String(pageSize);
    let response = await granolaApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common2.HttpMethod.GET,
      path: "/notes",
      queryParams
    });
    return {
      notes: response.body.notes.map(flattenNote),
      hasMore: response.body.hasMore,
      cursor: (_b = response.body.cursor) != null ? _b : null
    };
  }
});

// ../ap/packages/pieces/community/granola/src/lib/actions/get-note.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var getNoteAction = (0, import_pieces_framework2.createAction)({
  auth: granolaAuth,
  name: "get_note",
  displayName: "Get Note",
  description: "Retrieve a single meeting note by its ID, with optional transcript.",
  audience: "both",
  aiMetadata: {
    description: "Fetches one Granola meeting note by its note ID, including title, owner, calendar/attendee details, and AI summary. Enable the include-transcript option to also return the full speaker-by-speaker meeting transcript. Use it after List Notes (or a note trigger) to read a specific note in depth. Requires a valid note ID (format `not_` followed by 14 characters). Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    note_id: import_pieces_framework2.Property.ShortText({
      displayName: "Note ID",
      description: "The ID of the note to retrieve. Starts with `not_` followed by 14 characters (e.g. `not_1d3tmYTlCICgjy`). You can get this from the **List Notes** action.",
      required: !0
    }),
    include_transcript: import_pieces_framework2.Property.Checkbox({
      displayName: "Include Transcript",
      description: "Include the full meeting transcript in the response. The transcript shows who said what and when.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let queryParams = {};
    context.propsValue.include_transcript && (queryParams.include = "transcript");
    let response = await granolaApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common3.HttpMethod.GET,
      path: `/notes/${context.propsValue.note_id}`,
      queryParams
    });
    return flattenNoteWithTranscript(response.body);
  }
});

// ../ap/packages/pieces/community/granola/src/lib/triggers/new-note.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var polling = {
  strategy: import_pieces_common4.DedupeStrategy.TIMEBASED,
  items: async ({ auth, lastFetchEpochMS }) => {
    let queryParams = { page_size: "30" };
    return lastFetchEpochMS > 0 && (queryParams.created_after = new Date(lastFetchEpochMS).toISOString()), (await granolaApiCall({
      token: auth.secret_text,
      method: import_pieces_common4.HttpMethod.GET,
      path: "/notes",
      queryParams
    })).body.notes.map((note) => ({
      epochMilliSeconds: new Date(note.created_at).getTime(),
      data: flattenNote(note)
    }));
  }
}, newNoteTrigger = (0, import_pieces_framework3.createTrigger)({
  auth: granolaAuth,
  name: "new_note",
  displayName: "New Note",
  description: "Triggers when a new meeting note is created in Granola.",
  aiMetadata: {
    description: "Fires when a new meeting note is created in the authenticated user's Granola account, emitting the newly captured note with its summary and meeting metadata. Detected by polling notes ordered by creation time; use it to react to freshly recorded meetings."
  },
  props: {},
  sampleData: {
    id: "not_1d3tmYTlCICgjy",
    title: "Quarterly budget review",
    owner_name: "Jane Doe",
    owner_email: "jane@example.com",
    created_at: "2026-03-27T10:00:00Z",
    updated_at: "2026-03-27T10:30:00Z",
    calendar_event_title: "Quarterly budget review",
    calendar_organiser: "jane@example.com",
    calendar_event_id: "cal_abc123",
    scheduled_start_time: "2026-03-27T10:00:00Z",
    scheduled_end_time: "2026-03-27T11:00:00Z",
    attendees: "Jane Doe, John Smith",
    folders: "Finance",
    summary_text: "Discussed Q1 budget allocation and projections.",
    summary_markdown: null
  },
  type: import_pieces_framework3.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common4.pollingHelper.test(polling, context);
  },
  async onEnable(context) {
    await import_pieces_common4.pollingHelper.onEnable(polling, context);
  },
  async onDisable(context) {
    await import_pieces_common4.pollingHelper.onDisable(polling, context);
  },
  async run(context) {
    return await import_pieces_common4.pollingHelper.poll(polling, context);
  }
});

// ../ap/packages/pieces/community/granola/src/lib/triggers/updated-note.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var polling2 = {
  strategy: import_pieces_common5.DedupeStrategy.TIMEBASED,
  items: async ({ auth, lastFetchEpochMS }) => {
    let queryParams = { page_size: "30" };
    return lastFetchEpochMS > 0 && (queryParams.updated_after = new Date(lastFetchEpochMS).toISOString()), (await granolaApiCall({
      token: auth.secret_text,
      method: import_pieces_common5.HttpMethod.GET,
      path: "/notes",
      queryParams
    })).body.notes.filter((note) => note.updated_at !== note.created_at).map((note) => ({
      epochMilliSeconds: new Date(note.updated_at).getTime(),
      data: flattenNote(note)
    }));
  }
}, updatedNoteTrigger = (0, import_pieces_framework4.createTrigger)({
  auth: granolaAuth,
  name: "updated_note",
  displayName: "Updated Note",
  description: "Triggers when an existing meeting note is updated in Granola (e.g. summary edited, attendees changed).",
  aiMetadata: {
    description: "Fires when an already-existing Granola meeting note is modified (for example its summary is edited or attendees change), emitting the updated note. Detected by polling for notes whose updated time differs from their creation time, so brand-new notes do not fire this; use New Note for those."
  },
  props: {},
  sampleData: {
    id: "not_1d3tmYTlCICgjy",
    title: "Quarterly budget review",
    owner_name: "Jane Doe",
    owner_email: "jane@example.com",
    created_at: "2026-03-27T10:00:00Z",
    updated_at: "2026-03-27T14:00:00Z",
    calendar_event_title: "Quarterly budget review",
    calendar_organiser: "jane@example.com",
    calendar_event_id: "cal_abc123",
    scheduled_start_time: "2026-03-27T10:00:00Z",
    scheduled_end_time: "2026-03-27T11:00:00Z",
    attendees: "Jane Doe, John Smith",
    folders: "Finance",
    summary_text: "Discussed Q1 budget allocation and projections.",
    summary_markdown: null
  },
  type: import_pieces_framework4.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common5.pollingHelper.test(polling2, context);
  },
  async onEnable(context) {
    await import_pieces_common5.pollingHelper.onEnable(polling2, context);
  },
  async onDisable(context) {
    await import_pieces_common5.pollingHelper.onDisable(polling2, context);
  },
  async run(context) {
    return await import_pieces_common5.pollingHelper.poll(polling2, context);
  }
});

// ../ap/packages/pieces/community/granola/src/index.ts
var granolaAuth = import_pieces_framework5.PieceAuth.SecretText({
  displayName: "API Key",
  description: `To get your API key:
1. Open the **Granola desktop app**.
2. Go to **Settings > API**.
3. Click **Create API Key**.
4. Copy the key and paste it here.

**Note:** You need a Business or Enterprise plan to use API keys.`,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await granolaApiCall({
        method: import_pieces_common6.HttpMethod.GET,
        path: "/notes?page_size=1",
        token: auth
      }), { valid: !0 };
    } catch (e) {
      return { valid: !1, error: "Invalid API key" };
    }
  }
}), granola = (0, import_pieces_framework5.createPiece)({
  displayName: "Granola",
  description: "AI-powered meeting notes \u2014 automatically captures and summarizes your meetings.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/granola.png",
  categories: [import_pieces_framework6.PieceCategory.PRODUCTIVITY],
  auth: granolaAuth,
  authors: ["bst1n"],
  actions: [
    listNotesAction,
    getNoteAction,
    (0, import_pieces_common6.createCustomApiCallAction)({
      baseUrl: () => BASE_URL,
      auth: granolaAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [newNoteTrigger, updatedNoteTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  granola,
  granolaAuth
});
