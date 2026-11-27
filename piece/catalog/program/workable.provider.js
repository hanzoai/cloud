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

// ../ap/packages/pieces/community/workable/src/index.ts
var index_exports = {};
__export(index_exports, {
  workable: () => workable
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/workable/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), workableAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Access Token",
  description: `
    1. Click your profile icon in the upper right and navigate to Settings > Integrations > Apps.
    2. Locate the API Access Tokens section near the top of the page.
    3. Click the button **+ Generate API token**.
    4. Select the following scopes:
      - r_jobs
      - r_candidates
      - w_candidates
    5. Click Generate token to complete the process.
    `,
  required: !0
});

// ../ap/packages/pieces/community/workable/src/lib/actions/get-candidate.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/workable/src/lib/common/get-subdomain.ts
var import_pieces_common = require("@activepieces/pieces-common");
async function getAccountSubdomain(accessToken) {
  var _a;
  let response = await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: "https://workable.com/spi/v3/accounts",
    headers: {
      Authorization: `Bearer ${accessToken}`,
      Accept: "application/json"
    }
  });
  return console.log("this is response" + response.body.accounts[0].subdomain), (_a = response.body.accounts[0]) == null ? void 0 : _a.subdomain;
}

// ../ap/packages/pieces/community/workable/src/lib/actions/get-candidate.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), getCandidate = (0, import_pieces_framework2.createAction)({
  auth: workableAuth,
  name: "getCandidate",
  displayName: "Get Candidate",
  description: "Gets candidate's information.",
  audience: "both",
  aiMetadata: { description: "Fetch a single Workable candidate record by its candidate ID. Use to look up a known candidate before rating, moving, or referencing them. Requires the exact candidate ID; this is a read-only lookup and is idempotent.", idempotent: !0 },
  props: {
    id: import_pieces_framework2.Property.ShortText({
      displayName: "Candidate's Id",
      required: !0
    })
  },
  async run(context) {
    let candidateId = context.propsValue.id, accessToken = context.auth.secret_text, account = await getAccountSubdomain(accessToken);
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.GET,
      url: `https://${account}.workable.com/spi/v3/candidates/${candidateId}`,
      headers: {
        Authorization: `Bearer ${accessToken}`,
        Accept: "application/json"
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/workable/src/lib/actions/get-members.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), getMembers = (0, import_pieces_framework3.createAction)({
  auth: workableAuth,
  name: "getMembers",
  displayName: "Get Members",
  description: "Gets members of hiring team.",
  audience: "both",
  aiMetadata: { description: "Lists Workable account members (hiring team users). With no filters it returns all members; supply role, job shortcode, email, or exact name to narrow results. Use to resolve a member ID needed by actions like rating or moving a candidate. Read-only and idempotent.", idempotent: !0 },
  props: {
    limit: import_pieces_framework3.Property.Number({
      displayName: "Limit",
      description: "Default is 50",
      required: !1
    }),
    role: import_pieces_framework3.Property.ShortText({
      displayName: "Role",
      description: "Filter for member of specified role",
      required: !1
    }),
    shortcode: import_pieces_framework3.Property.ShortText({
      displayName: "Shortcode",
      description: "Shortcode of specific job",
      required: !1
    }),
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Member's email",
      description: "Filter for specific member by email",
      required: !1
    }),
    name: import_pieces_framework3.Property.ShortText({
      displayName: "Member's name",
      description: "Filter for members of specified name (Exact Match)",
      required: !1
    })
  },
  async run(context) {
    let limit = context.propsValue.limit, role = context.propsValue.role, shortcode = context.propsValue.shortcode, email = context.propsValue.email, name = context.propsValue.name, accessToken = context.auth.secret_text, queryParams = {};
    limit != null && (queryParams.limit = limit), role && role.trim() !== "" && (queryParams.role = role), shortcode && shortcode.trim() !== "" && (queryParams.shortcode = shortcode), email && email.trim() !== "" && (queryParams.email = email), name && name.trim() !== "" && (queryParams.name = name);
    let subdomain = await getAccountSubdomain(accessToken);
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: `https://${subdomain}.workable.com/spi/v3/members`,
      headers: {
        Authorization: `Bearer ${accessToken}`,
        Accept: "application/json"
      },
      queryParams
    })).body;
  }
});

// ../ap/packages/pieces/community/workable/src/lib/actions/get-job.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var getJob = (0, import_pieces_framework4.createAction)({
  auth: workableAuth,
  name: "getJob",
  displayName: "Get Job",
  description: "Gets specific job deatils.",
  audience: "both",
  aiMetadata: { description: "Fetch details of a single Workable job posting identified by its shortcode. Use when you have a job shortcode and need its full attributes. Requires the exact job shortcode; read-only and idempotent.", idempotent: !0 },
  props: {
    shortcode: import_pieces_framework4.Property.ShortText({
      displayName: "Shortcode",
      description: "Shortcode of specific job",
      required: !0
    })
  },
  async run(context) {
    var _a;
    let shortcode = (_a = context.propsValue) == null ? void 0 : _a.shortcode, accessToken = context.auth.secret_text, url = `https://${await getAccountSubdomain(accessToken)}.workable.com/spi/v3/jobs/${shortcode}`;
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.GET,
      url,
      headers: {
        Authorization: `Bearer ${accessToken}`,
        Accept: "application/json"
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/workable/src/lib/actions/get-stages.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common"), getStages = (0, import_pieces_framework5.createAction)({
  auth: workableAuth,
  name: "getStages",
  displayName: "Get Stages",
  description: "Gets stages in your recruitment pipeline stages.",
  audience: "both",
  aiMetadata: { description: "Lists the recruitment pipeline stages for a specific Workable job, identified by its shortcode. Use to discover valid stage slugs before moving a candidate. Requires the exact job shortcode; read-only and idempotent.", idempotent: !0 },
  props: {
    shortcode: import_pieces_framework5.Property.ShortText({
      displayName: "Shortcode",
      description: "Shortcode of specific job",
      required: !0
    })
  },
  async run(context) {
    let shortcode = context.propsValue.shortcode, accessToken = context.auth.secret_text, account = await getAccountSubdomain(accessToken);
    return (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.GET,
      url: `https://${account}.workable.com/spi/v3/jobs/${shortcode}/stages`,
      headers: {
        Authorization: `Bearer ${accessToken}`,
        Accept: "application/json"
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/workable/src/lib/actions/move-candidate.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common"), moveCandidate = (0, import_pieces_framework6.createAction)({
  auth: workableAuth,
  name: "moveCandidate",
  displayName: "Move Candidate",
  description: "Moves candidate to the specified stage.",
  audience: "both",
  aiMetadata: { description: "Moves a Workable candidate into a target pipeline stage, recording the move on behalf of a given hiring-team member. Use to advance or change a candidate stage in the recruitment pipeline. Requires the candidate ID, the acting member ID, and the target stage slug (resolve valid slugs via Get Stages). Not idempotent \u2014 each call posts a stage transition.", idempotent: !1 },
  props: {
    id: import_pieces_framework6.Property.ShortText({
      displayName: "Candidate's Id",
      description: "Id of candidate",
      required: !0
    }),
    member_id: import_pieces_framework6.Property.ShortText({
      displayName: "Member's Id",
      description: "This person's Id is used to move the candidate to the next stage",
      required: !0
    }),
    target_stage: import_pieces_framework6.Property.ShortText({
      displayName: "Target stage name",
      description: "Slug of stage target should be moved to",
      required: !0
    })
  },
  async run(context) {
    let id = context.propsValue.id, member_id = context.propsValue.member_id, target_stage = context.propsValue.target_stage, accessToken = context.auth.secret_text, subdomain = await getAccountSubdomain(accessToken), payload = {
      member_id,
      target_stage
    };
    return await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.POST,
      url: `https://${subdomain}.workable.com/spi/v3/candidates/${id}/move`,
      headers: {
        Authorization: `Bearer ${accessToken}`,
        Accept: "application/json"
      },
      body: payload
    });
  }
});

// ../ap/packages/pieces/community/workable/src/lib/actions/rate-candidate.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common"), rateCandidate = (0, import_pieces_framework7.createAction)({
  auth: workableAuth,
  name: "rateCandidate",
  displayName: "Rate candidate",
  description: "Rates the candidate on workable.",
  audience: "both",
  aiMetadata: { description: "Adds a rating with a comment to a Workable candidate on behalf of a hiring-team member, using one of the thumbs, stars, or numbers scales. Use to record an evaluation of a candidate. Requires the candidate ID, the acting member ID, a comment, the chosen scale, and a grade valid for that scale (thumbs 0-2, stars 0-4, numbers 0-9). Not idempotent \u2014 each call appends a new rating.", idempotent: !1 },
  props: {
    id: import_pieces_framework7.Property.ShortText({
      displayName: "Candidate's Id",
      description: "Id of candidate",
      required: !0
    }),
    member_id: import_pieces_framework7.Property.ShortText({
      displayName: "Member's Id",
      description: "Id of member that is adding the rating",
      required: !0
    }),
    comment: import_pieces_framework7.Property.LongText({
      displayName: "Comment",
      description: "Comment about the scoring of the candidate",
      required: !0
    }),
    scale: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Rating scale",
      description: "Select scale to rate candidate on",
      required: !0,
      options: {
        options: [
          { label: "Thumbs", value: "thumbs" },
          { label: "Stars", value: "stars" },
          { label: "Numbers", value: "numbers" }
        ]
      }
    }),
    grade: import_pieces_framework7.Property.Number({
      displayName: "Grade",
      description: "Thumbs scale: 0-2, Stars scale: 0-4, Numbers scale: 0-9",
      required: !0
    })
  },
  async run(context) {
    let id = context.propsValue.id, member_id = context.propsValue.member_id, comment = context.propsValue.comment, scale = context.propsValue.scale, grade = context.propsValue.grade, accessToken = context.auth.secret_text, subdomain = await getAccountSubdomain(accessToken), body = {
      comment,
      scale,
      member_id,
      grade
    };
    return await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.POST,
      url: `https://${subdomain}.workable.com/spi/v3/candidates/${id}/ratings`,
      headers: {
        Authorization: `Bearer ${accessToken}`,
        Accept: "application/json"
      },
      body
    });
  }
});

// ../ap/packages/pieces/community/workable/src/lib/triggers/new-candidate.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/workable/src/lib/common/webhooks.ts
var import_pieces_common8 = require("@activepieces/pieces-common"), workableCommon = {
  subscribeWebhook: async (subdomain, accessToken, webhookUrl, event, args = {}) => {
    let request = {
      method: import_pieces_common8.HttpMethod.POST,
      url: `https://${subdomain}.workable.com/spi/v3/subscriptions`,
      headers: {
        Authorization: `Bearer ${accessToken}`,
        Accept: "application/json"
      },
      body: {
        target: webhookUrl,
        event,
        args
      }
    };
    return (await import_pieces_common8.httpClient.sendRequest(request)).body;
  },
  unsubscribeWebhook: async (subdomain, accessToken, subscriptionId) => {
    let request = {
      method: import_pieces_common8.HttpMethod.DELETE,
      url: `https://${subdomain}.workable.com/spi/v3/subscriptions/${subscriptionId}`,
      headers: {
        Authorization: `Bearer ${accessToken}`,
        Accept: "application/json"
      }
    };
    return await import_pieces_common8.httpClient.sendRequest(request);
  }
};

// ../ap/packages/pieces/community/workable/src/lib/triggers/new-candidate.ts
var newCandidate = (0, import_pieces_framework8.createTrigger)({
  auth: workableAuth,
  name: "newCandidate",
  displayName: "New Candidate",
  description: "Triggers when new candidate submits application. Can be filtered by specific job and/or hiring pipeline stage.",
  aiMetadata: {
    description: "Fires when a new candidate is created in Workable (a candidate_created event), representing a newly submitted application. Can be scoped to a single job shortcode and/or a single pipeline stage slug, or left unfiltered to fire for all jobs and stages."
  },
  props: {
    shortcode: import_pieces_framework8.Property.ShortText({
      displayName: "Shortcode",
      description: "Shortcode of specific job. Leave empty to trigger for all jobs.",
      required: !1
    }),
    stage_slug: import_pieces_framework8.Property.ShortText({
      displayName: "Stage Slug",
      description: "Stage slug to filter by specific hiring pipeline stage (e.g., 'applied', 'phone_screen', 'interview', 'offer'). Leave empty to trigger for all stages.",
      required: !1
    })
  },
  sampleData: {
    id: "123",
    name: "John Doe",
    firstname: "John",
    lastname: "Doe",
    headline: "Software Engineer",
    account: {
      subdomain: "example",
      name: "Example Inc."
    },
    job: {
      shortcode: "ENG123",
      title: "Software Engineer"
    },
    stage: "applied",
    disqualified: !1,
    disqualification_reason: "",
    sourced: !1,
    profile_url: "https://example.workable.com/candidates/123",
    email: "john.doe@example.com",
    domain: "example.com",
    created_at: (/* @__PURE__ */ new Date()).toISOString(),
    updated_at: (/* @__PURE__ */ new Date()).toISOString()
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    var _a;
    let accessToken = context.auth.secret_text, subdomain = await getAccountSubdomain(accessToken), shortcode = context.propsValue.shortcode || "", stageSlug = context.propsValue.stage_slug || "", webhookUrl = context.webhookUrl, subscription = await workableCommon.subscribeWebhook(
      subdomain,
      accessToken,
      webhookUrl,
      "candidate_created",
      { job_shortcode: shortcode, stage_slug: stageSlug }
    );
    await ((_a = context.store) == null ? void 0 : _a.put("_new_candidate_created", {
      webhookId: subscription.id
    }));
  },
  async onDisable(context) {
    let accessToken = context.auth.secret_text, webhookInfo = await context.store.get("_new_candidate_created");
    if (webhookInfo != null && webhookInfo.webhookId) {
      let subdomain = await getAccountSubdomain(accessToken);
      await workableCommon.unsubscribeWebhook(subdomain, accessToken, webhookInfo.webhookId);
    }
  },
  async test(context) {
    var _a, _b;
    let accessToken = context.auth.secret_text, subdomain = await getAccountSubdomain(accessToken), shortcode = context.propsValue.shortcode || "", stageSlug = context.propsValue.stage_slug || "", queryParams = {};
    return shortcode && (queryParams.shortcode = shortcode), stageSlug && (queryParams.stage = stageSlug), (_b = (_a = (await import_pieces_common9.httpClient.sendRequest({
      method: import_pieces_common9.HttpMethod.GET,
      url: `https://${subdomain}.workable.com/spi/v3/candidates/`,
      headers: {
        Authorization: `Bearer ${accessToken}`,
        Accept: "application/json"
      },
      queryParams
    })).body.candidates) == null ? void 0 : _a.slice(0, 3)) != null ? _b : [];
  },
  async run(context) {
    return [context.payload.body.data];
  }
});

// ../ap/packages/pieces/community/workable/src/index.ts
var import_pieces_common10 = require("@activepieces/pieces-common"), import_pieces_framework10 = require("@activepieces/pieces-framework");
var workable = (0, import_pieces_framework9.createPiece)({
  displayName: "Workable",
  auth: workableAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/workable.png",
  categories: [import_pieces_framework10.PieceCategory.HUMAN_RESOURCES],
  authors: ["Cloudieunnie"],
  actions: [
    getCandidate,
    getMembers,
    getJob,
    getStages,
    moveCandidate,
    rateCandidate,
    (0, import_pieces_common10.createCustomApiCallAction)({
      baseUrl: () => "https://workable.com/spi/v3/",
      auth: workableAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`,
        Accept: "application/json"
      })
    })
  ],
  triggers: [
    newCandidate
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  workable
});
