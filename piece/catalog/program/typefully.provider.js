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

// ../ap/packages/pieces/community/typefully/src/index.ts
var index_exports = {};
__export(index_exports, {
  typefully: () => typefully
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/typefully/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/typefully/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), TYPEFULLY_API_URL = "https://api.typefully.com/v2";
async function typefullyApiCall({
  apiKey,
  method,
  resourceUri,
  query,
  body
}) {
  let qs = {};
  if (query)
    for (let [key, value] of Object.entries(query))
      value != null && (qs[key] = String(value));
  let request = {
    method,
    url: TYPEFULLY_API_URL + resourceUri,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token: apiKey
    },
    queryParams: qs,
    body
  };
  return (await import_pieces_common.httpClient.sendRequest(request)).body;
}

// ../ap/packages/pieces/community/typefully/src/lib/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
function isHttpError(e) {
  return typeof e == "object" && e !== null && "response" in e && typeof e.response == "object" && e.response !== null && "status" in e.response;
}
var typefullyAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "You can obtain your API key from **Typefully Settings \u2192 API**.",
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await typefullyApiCall({
        apiKey: auth,
        method: import_pieces_common2.HttpMethod.GET,
        resourceUri: "/me"
      }), {
        valid: !0
      };
    } catch (e) {
      return isHttpError(e) && e.response.status === 401 ? {
        valid: !1,
        error: "Invalid API key."
      } : {
        valid: !1,
        error: `Could not validate API key: ${e instanceof Error ? e.message : String(e)}`
      };
    }
  }
});

// ../ap/packages/pieces/community/typefully/src/lib/actions/create-draft.action.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/typefully/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var import_pieces_framework3 = require("@activepieces/pieces-framework");
async function fetchAllPages(apiKey, resourceUri) {
  let allResults = [], offset = 0, limit = 50;
  for (; ; ) {
    let response = await typefullyApiCall({
      apiKey,
      method: import_pieces_common3.HttpMethod.GET,
      resourceUri,
      query: { limit, offset }
    });
    if (allResults.push(...response.results), response.results.length < limit || response.next === null) break;
    offset += limit;
  }
  return allResults;
}
var socialSetDropdown = import_pieces_framework2.Property.Dropdown({
  auth: typefullyAuth,
  displayName: "Social Set",
  description: "The social set (account) to use.",
  refreshers: [],
  required: !0,
  options: async ({ auth }) => auth ? {
    disabled: !1,
    options: (await fetchAllPages(
      auth.secret_text,
      "/social-sets"
    )).map((socialSet) => ({
      label: socialSet.name,
      value: socialSet.id
    }))
  } : {
    disabled: !0,
    options: [],
    placeholder: "Please connect your account first."
  }
}), tagsMultiSelectDropdown = import_pieces_framework2.Property.MultiSelectDropdown({
  auth: typefullyAuth,
  displayName: "Tags",
  description: "Tags to apply to the draft.",
  refreshers: ["social_set_id"],
  required: !1,
  options: async ({ auth, social_set_id }) => !auth || !social_set_id ? {
    disabled: !0,
    options: [],
    placeholder: "Please connect your account and select a social set first."
  } : {
    disabled: !1,
    options: (await fetchAllPages(
      auth.secret_text,
      `/social-sets/${social_set_id}/tags`
    )).map((tag) => ({
      label: tag.name,
      value: tag.id
    }))
  }
}), instructionsMarkdown = import_pieces_framework2.Property.MarkDown({
  value: `
1. In Typefully, go to **Settings \u2192 API**.
2. Go to the **Webhooks** section and click **Add webhook**.
3. Paste the following URL into the webhook endpoint field:
\`\`\`text
{{webhookUrl}}
\`\`\`
4. Select the events you want to receive and click **Save**.`,
  variant: import_pieces_framework3.MarkdownVariant.INFO
}), draftSampleData = {
  id: 12345,
  social_set_id: 67890,
  status: "draft",
  created_at: "2025-01-15T10:30:00Z",
  updated_at: "2025-01-16T09:15:00Z",
  scheduled_date: "2025-01-20T14:00:00Z",
  published_at: "2025-01-20T14:00:05Z",
  draft_title: "Weekly Newsletter",
  tags: ["marketing", "product"],
  preview: "Excited to announce our new feature!",
  share_url: "https://typefully.com/share/abc123",
  private_url: "https://typefully.com/?d=12345&a=67890",
  platforms: {
    x: {
      enabled: !0,
      posts: [{ text: "Hello world!" }]
    },
    linkedin: {
      enabled: !0,
      posts: [{ text: "Hello LinkedIn!" }]
    }
  }
};

// ../ap/packages/pieces/community/typefully/src/lib/actions/create-draft.action.ts
var createDraftAction = (0, import_pieces_framework4.createAction)({
  auth: typefullyAuth,
  name: "typefully_create_draft",
  displayName: "Create Draft Simple",
  description: 'Create cross-platform, single-post drafts with text and optional media URLs. For threads (multiple posts) or advanced control, use "Create Draft Advanced" instead.',
  audience: "both",
  aiMetadata: {
    description: "Creates a single-post draft in Typefully with the same text fanned out to one or more selected platforms (X, LinkedIn, Threads, Bluesky, Mastodon). Choose this for simple one-post content; use Create Draft Advanced for multi-post threads or per-platform content. Optionally schedule it via Publish At, otherwise it is saved as an unpublished draft. Each call creates a new draft, so it is not idempotent.",
    idempotent: !1
  },
  props: {
    social_set_id: socialSetDropdown,
    text: import_pieces_framework4.Property.LongText({
      displayName: "Post Text",
      description: "The text content of the post.",
      required: !0
    }),
    platforms: import_pieces_framework4.Property.StaticMultiSelectDropdown({
      displayName: "Platforms",
      description: "Which platforms to publish to. At least one platform must be selected.",
      required: !0,
      options: {
        disabled: !1,
        options: [
          { label: "X (Twitter)", value: "x" },
          { label: "LinkedIn", value: "linkedin" },
          { label: "Threads", value: "threads" },
          { label: "Bluesky", value: "bluesky" },
          { label: "Mastodon", value: "mastodon" }
        ]
      }
    }),
    draft_title: import_pieces_framework4.Property.ShortText({
      displayName: "Draft Title",
      description: "An optional title for the draft (visible only in Typefully).",
      required: !1
    }),
    tags: tagsMultiSelectDropdown,
    share: import_pieces_framework4.Property.Checkbox({
      displayName: "Generate Share URL",
      description: "Generate a share URL for the draft.",
      required: !1,
      defaultValue: !1
    }),
    publish_at: import_pieces_framework4.Property.DateTime({
      displayName: "Publish At",
      description: "Schedule the draft for a specific date/time. Leave empty to save as draft.",
      required: !1
    }),
    reply_to_url: import_pieces_framework4.Property.ShortText({
      displayName: "X Reply To URL",
      description: "URL of an X post to reply to.",
      required: !1
    })
  },
  async run(context) {
    let {
      social_set_id,
      text,
      platforms,
      draft_title,
      tags,
      share,
      publish_at,
      reply_to_url
    } = context.propsValue, platformsBody = {};
    for (let platform of platforms) {
      let platformConfig = {
        enabled: !0,
        posts: [{ text }]
      };
      platform === "x" && reply_to_url && (platformConfig.settings = { reply_to_url }), platformsBody[platform] = platformConfig;
    }
    let body = { platforms: platformsBody };
    return draft_title && (body.draft_title = draft_title), tags && tags.length > 0 && (body.tags = tags), share && (body.share = !0), publish_at && (body.publish_at = publish_at), await typefullyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common4.HttpMethod.POST,
      resourceUri: `/social-sets/${social_set_id}/drafts`,
      body
    });
  }
});

// ../ap/packages/pieces/community/typefully/src/lib/actions/create-draft-advanced.action.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var createDraftAdvancedAction = (0, import_pieces_framework5.createAction)({
  auth: typefullyAuth,
  name: "typefully_create_draft_advanced",
  displayName: "Create Draft Advanced",
  description: 'Create multi-post threads with full control. Supports threads (multiple posts), different content and media per post with pre-uploaded media IDs using JSON format. For single posts with simple text and media URLs, use "Create Draft Simple" instead.',
  audience: "both",
  aiMetadata: {
    description: "Creates a Typefully draft from a raw per-platform JSON object, giving full control over multi-post threads, distinct content per platform, and attached media IDs. Choose this over Create Draft Simple when you need threads or platform-specific posts; the required Platforms JSON must be an object keyed by platform name, each with enabled and a posts array. Optionally schedule via Publish At. Each call creates a new draft, so it is not idempotent.",
    idempotent: !1
  },
  props: {
    social_set_id: socialSetDropdown,
    platforms_json: import_pieces_framework5.Property.Json({
      displayName: "Platforms (JSON)",
      description: 'A JSON object defining per-platform content. Each key is a platform name (x, linkedin, threads, bluesky, mastodon) with an object containing "enabled" (boolean) and "posts" (array of {text} objects). For threads/X, add multiple posts. X also supports "settings": {"reply_to_url": "..."}. Example: {"x": {"enabled": true, "posts": [{"text": "Tweet 1"}, {"text": "Tweet 2"}]}, "linkedin": {"enabled": true, "posts": [{"text": "LinkedIn post"}]}}',
      required: !0
    }),
    draft_title: import_pieces_framework5.Property.ShortText({
      displayName: "Draft Title",
      description: "An optional title for the draft.",
      required: !1
    }),
    tags: tagsMultiSelectDropdown,
    share: import_pieces_framework5.Property.Checkbox({
      displayName: "Generate Share URL",
      description: "Generate a share URL for the draft.",
      required: !1,
      defaultValue: !1
    }),
    publish_at: import_pieces_framework5.Property.DateTime({
      displayName: "Publish At",
      description: "Schedule the draft for a specific date/time. Leave empty to save as draft.",
      required: !1
    })
  },
  async run(context) {
    let {
      social_set_id,
      platforms_json,
      draft_title,
      tags,
      share,
      publish_at
    } = context.propsValue, platforms;
    if (typeof platforms_json == "string" ? platforms = JSON.parse(platforms_json) : platforms = platforms_json, typeof platforms != "object" || platforms === null || Array.isArray(platforms))
      throw new Error(
        'Platforms must be a JSON object. Example: {"x": {"enabled": true, "posts": [{"text": "Hello"}]}}'
      );
    let body = { platforms };
    return draft_title && (body.draft_title = draft_title), tags && tags.length > 0 && (body.tags = tags), share && (body.share = !0), publish_at && (body.publish_at = publish_at), await typefullyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common5.HttpMethod.POST,
      resourceUri: `/social-sets/${social_set_id}/drafts`,
      body
    });
  }
});

// ../ap/packages/pieces/community/typefully/src/lib/actions/get-draft.action.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var getDraftAction = (0, import_pieces_framework6.createAction)({
  auth: typefullyAuth,
  name: "typefully_get_draft",
  displayName: "Get Draft",
  description: "Retrieve a specific draft by its ID.",
  audience: "both",
  aiMetadata: {
    description: "Fetches a single Typefully draft by its draft ID within a social set. Use to read the current content, status, or schedule of a known draft. Requires both the social set ID and the draft ID. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    social_set_id: socialSetDropdown,
    draft_id: import_pieces_framework6.Property.ShortText({
      displayName: "Draft ID",
      description: "The ID of the draft to retrieve.",
      required: !0
    })
  },
  async run(context) {
    let { social_set_id, draft_id } = context.propsValue;
    return await typefullyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common6.HttpMethod.GET,
      resourceUri: `/social-sets/${social_set_id}/drafts/${draft_id}`
    });
  }
});

// ../ap/packages/pieces/community/typefully/src/lib/actions/list-drafts.action.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var listDraftsAction = (0, import_pieces_framework7.createAction)({
  auth: typefullyAuth,
  name: "typefully_list_drafts",
  displayName: "List Drafts",
  description: "List drafts with optional filters.",
  audience: "both",
  aiMetadata: {
    description: "Lists drafts within a Typefully social set, optionally filtered by status (draft, scheduled, published, publishing, error) and sorted, returning up to the given limit (1-50, default 10). Use to discover draft IDs or survey drafts before reading, scheduling, publishing, or deleting them. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    social_set_id: socialSetDropdown,
    status: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Status",
      description: "Filter by draft status.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Draft", value: "draft" },
          { label: "Scheduled", value: "scheduled" },
          { label: "Published", value: "published" },
          { label: "Publishing", value: "publishing" },
          { label: "Error", value: "error" }
        ]
      }
    }),
    order_by: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Order By",
      description: "Sort order for the results.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Created At (newest first)", value: "-created_at" },
          { label: "Created At (oldest first)", value: "created_at" },
          { label: "Updated At (newest first)", value: "-updated_at" },
          { label: "Updated At (oldest first)", value: "updated_at" },
          { label: "Scheduled Date (newest first)", value: "-scheduled_date" },
          { label: "Scheduled Date (oldest first)", value: "scheduled_date" }
        ]
      }
    }),
    limit: import_pieces_framework7.Property.Number({
      displayName: "Limit",
      description: "Maximum number of drafts to return (1-50).",
      required: !1,
      defaultValue: 10
    })
  },
  async run(context) {
    let { social_set_id, status, order_by, limit } = context.propsValue;
    return await typefullyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common7.HttpMethod.GET,
      resourceUri: `/social-sets/${social_set_id}/drafts`,
      query: {
        status,
        order_by,
        limit: limit != null ? limit : 10
      }
    });
  }
});

// ../ap/packages/pieces/community/typefully/src/lib/actions/delete-draft.action.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var deleteDraftAction = (0, import_pieces_framework8.createAction)({
  auth: typefullyAuth,
  name: "typefully_delete_draft",
  displayName: "Delete Draft",
  description: "Delete an existing draft.",
  audience: "both",
  aiMetadata: {
    description: "Permanently deletes a Typefully draft identified by its draft ID within a social set. Use to remove an unwanted draft; this is destructive and cannot be undone. Requires both the social set ID and the draft ID. Repeating the call on an already-deleted draft has no further effect.",
    idempotent: !1
  },
  props: {
    social_set_id: socialSetDropdown,
    draft_id: import_pieces_framework8.Property.ShortText({
      displayName: "Draft ID",
      description: "The ID of the draft to delete.",
      required: !0
    })
  },
  async run(context) {
    let { social_set_id, draft_id } = context.propsValue;
    return await typefullyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common8.HttpMethod.DELETE,
      resourceUri: `/social-sets/${social_set_id}/drafts/${draft_id}`
    }), { success: !0, deleted_draft_id: draft_id };
  }
});

// ../ap/packages/pieces/community/typefully/src/lib/actions/publish-draft-now.action.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var publishDraftNowAction = (0, import_pieces_framework9.createAction)({
  auth: typefullyAuth,
  name: "typefully_publish_draft_now",
  displayName: "Publish Draft Now",
  description: "Immediately publishes an existing draft to all configured platforms.",
  audience: "both",
  aiMetadata: {
    description: "Immediately publishes an existing Typefully draft to all of its configured platforms by setting its publish time to now. Use when content is ready to go live right away; use Schedule Draft instead to publish at a future time. Requires the social set ID and the draft ID. Publishing is a one-time side effect, so it is not idempotent.",
    idempotent: !1
  },
  props: {
    social_set_id: socialSetDropdown,
    draft_id: import_pieces_framework9.Property.ShortText({
      displayName: "Draft ID",
      description: "The ID of the draft to publish.",
      required: !0
    })
  },
  async run(context) {
    let { social_set_id, draft_id } = context.propsValue;
    return await typefullyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common9.HttpMethod.PATCH,
      resourceUri: `/social-sets/${social_set_id}/drafts/${draft_id}`,
      body: {
        publish_at: "now"
      }
    });
  }
});

// ../ap/packages/pieces/community/typefully/src/lib/actions/schedule-draft.action.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var scheduleDraftAction = (0, import_pieces_framework10.createAction)({
  auth: typefullyAuth,
  name: "typefully_schedule_draft",
  displayName: "Schedule Draft",
  description: "Schedules an existing draft to be published at a specific time.",
  audience: "both",
  aiMetadata: {
    description: "Schedules an existing Typefully draft to publish at a specific future date and time (ISO 8601 with timezone). Use to queue content for later; use Publish Draft Now to go live immediately. Requires the social set ID, the draft ID, and the schedule time. Re-running with the same time leaves the draft scheduled for that time, so it is effectively idempotent on a stable input.",
    idempotent: !0
  },
  props: {
    social_set_id: socialSetDropdown,
    draft_id: import_pieces_framework10.Property.ShortText({
      displayName: "Draft ID",
      description: "The ID of the draft to schedule.",
      required: !0
    }),
    schedule_at: import_pieces_framework10.Property.DateTime({
      displayName: "Schedule At",
      description: "The date and time to publish the draft (ISO 8601 format with timezone).",
      required: !0
    })
  },
  async run(context) {
    let { social_set_id, draft_id, schedule_at } = context.propsValue;
    return await typefullyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common10.HttpMethod.PATCH,
      resourceUri: `/social-sets/${social_set_id}/drafts/${draft_id}`,
      body: {
        publish_at: schedule_at
      }
    });
  }
});

// ../ap/packages/pieces/community/typefully/src/lib/actions/upload-media.action.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var uploadMediaAction = (0, import_pieces_framework11.createAction)({
  auth: typefullyAuth,
  name: "typefully_upload_media",
  displayName: "Upload Media",
  description: "Uploads a media file (image, video, GIF, or PDF) to Typefully. Returns a media ID that can be used when creating drafts.",
  audience: "both",
  aiMetadata: {
    description: "Uploads a media file (image, video, GIF, or PDF) to a Typefully social set and returns a media ID to reference when attaching media to drafts created via Create Draft Advanced. Use as a prerequisite step before building a draft that includes media. Requires the social set ID and the file. Each call uploads a new file, so it is not idempotent.",
    idempotent: !1
  },
  props: {
    social_set_id: socialSetDropdown,
    file: import_pieces_framework11.Property.File({
      displayName: "File",
      description: "The media file to upload (image, video, GIF, or PDF).",
      required: !0
    })
  },
  async run(context) {
    let { social_set_id, file } = context.propsValue, { media_id, upload_url } = await typefullyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common11.HttpMethod.POST,
      resourceUri: `/social-sets/${social_set_id}/media/upload`,
      body: { file_name: file.filename }
    });
    return await import_pieces_common11.httpClient.sendRequest({
      method: import_pieces_common11.HttpMethod.PUT,
      url: upload_url,
      body: file.data
    }), { media_id };
  }
});

// ../ap/packages/pieces/community/typefully/src/lib/triggers/new-event.trigger.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var STATUS_TO_EVENT = {
  draft: "draft.created",
  scheduled: "draft.scheduled",
  published: "draft.published"
};
async function fetchLatestDraft(apiKey) {
  var _a;
  let firstSocialSet = (await typefullyApiCall({
    apiKey,
    method: import_pieces_common12.HttpMethod.GET,
    resourceUri: "/social-sets",
    query: { limit: 1 }
  })).results[0];
  return firstSocialSet && (_a = (await typefullyApiCall({
    apiKey,
    method: import_pieces_common12.HttpMethod.GET,
    resourceUri: `/social-sets/${firstSocialSet.id}/drafts`,
    query: { limit: 1, order_by: "-updated_at" }
  })).results[0]) != null ? _a : null;
}
var newEventTrigger = (0, import_pieces_framework12.createTrigger)({
  auth: typefullyAuth,
  name: "typefully_new_event",
  displayName: "New Event",
  description: "Triggers when a webhook event is received from Typefully.",
  aiMetadata: {
    description: "Fires when Typefully sends a webhook event for a draft, optionally filtered to specific event types (draft created, scheduled, published, status changed, tags changed, or deleted); leave the filter empty to receive every event configured in Typefully. Each firing represents one draft lifecycle event and carries the affected draft. Webhooks must be set up manually in the Typefully dashboard."
  },
  type: import_pieces_framework12.TriggerStrategy.WEBHOOK,
  props: {
    instructions: instructionsMarkdown,
    events: import_pieces_framework12.Property.StaticMultiSelectDropdown({
      displayName: "Events",
      description: "Filter which events trigger this flow. Leave empty to receive all events configured in Typefully.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Draft Created", value: "draft.created" },
          { label: "Draft Scheduled", value: "draft.scheduled" },
          { label: "Draft Published", value: "draft.published" },
          { label: "Draft Status Changed", value: "draft.status_changed" },
          { label: "Draft Tags Changed", value: "draft.tags_changed" },
          { label: "Draft Deleted", value: "draft.deleted" }
        ]
      }
    })
  },
  sampleData: {
    event: "draft.created",
    data: draftSampleData
  },
  async onEnable() {
  },
  async onDisable() {
  },
  async test(context) {
    var _a;
    let draft = await fetchLatestDraft(context.auth.secret_text);
    if (!draft) return [];
    let selectedEvents = context.propsValue.events, inferredEvent = (_a = STATUS_TO_EVENT[draft.status]) != null ? _a : "draft.created";
    return [{ event: selectedEvents && selectedEvents.length > 0 ? selectedEvents.includes(inferredEvent) ? inferredEvent : selectedEvents[0] : inferredEvent, data: draft }];
  },
  async run(context) {
    let body = context.payload.body;
    if (!body.data) return [];
    let selectedEvents = context.propsValue.events;
    return selectedEvents && selectedEvents.length > 0 && body.event && !selectedEvents.includes(body.event) ? [] : [body];
  }
});

// ../ap/packages/pieces/community/typefully/src/index.ts
var typefully = (0, import_pieces_framework13.createPiece)({
  displayName: "Typefully",
  description: "Write, schedule, and publish social media content.",
  auth: typefullyAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/typefully.png",
  authors: ["bst1n", "sanket-a11y"],
  categories: [import_pieces_framework14.PieceCategory.MARKETING],
  actions: [
    createDraftAction,
    createDraftAdvancedAction,
    publishDraftNowAction,
    scheduleDraftAction,
    getDraftAction,
    listDraftsAction,
    deleteDraftAction,
    uploadMediaAction,
    (0, import_pieces_common13.createCustomApiCallAction)({
      auth: typefullyAuth,
      baseUrl: () => TYPEFULLY_API_URL,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [
    newEventTrigger
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  typefully
});
