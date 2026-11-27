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

// ../ap/packages/pieces/community/greenhouse/src/index.ts
var index_exports = {};
__export(index_exports, {
  greenhouse: () => greenhouse
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_common15 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/greenhouse/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), greenhouseAuth = import_pieces_framework.PieceAuth.OAuth2({
  displayName: "Greenhouse",
  description: "To connect Greenhouse, you need OAuth2 credentials from a registered Greenhouse partner application.\n\n**Steps to get credentials:**\n1. Contact **partner-support@greenhouse.io** to register your integration.\n2. Provide: integration name, required scopes, and your redirect URI.\n3. Greenhouse will issue a **Client ID** and **Client Secret**.\n\n**Required scopes:** `harvest:candidates:list`, `harvest:candidates:create`, `harvest:candidates:update`, `harvest:applications:list`, `harvest:jobs:list`, `harvest:job_posts:list`, `harvest:interviews:list`, `harvest:scorecards:list`, `harvest:notes:create`",
  authUrl: "https://auth.greenhouse.io/authorize",
  tokenUrl: "https://auth.greenhouse.io/token",
  scope: [
    "harvest:candidates:list",
    "harvest:candidates:create",
    "harvest:candidates:update",
    "harvest:applications:list",
    "harvest:jobs:list",
    "harvest:job_posts:list",
    "harvest:interviews:list",
    "harvest:scorecards:list",
    "harvest:notes:create"
  ],
  required: !0
});

// ../ap/packages/pieces/community/greenhouse/src/lib/actions/create-candidate.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/greenhouse/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://harvest.greenhouse.io/v3";
async function greenhouseApiCall({
  accessToken,
  method,
  endpoint,
  body,
  queryParams
}) {
  return import_pieces_common.httpClient.sendRequest({
    method,
    url: `${BASE_URL}${endpoint}`,
    headers: {
      Authorization: `Bearer ${accessToken}`
    },
    body,
    queryParams
  });
}
function shapeCandidate(c) {
  var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n, _o, _p, _q, _r, _s, _t;
  return {
    id: c.id,
    first_name: c.first_name,
    last_name: c.last_name,
    preferred_name: (_a = c.preferred_name) != null ? _a : null,
    company: c.company,
    title: c.title,
    created_at: c.created_at,
    updated_at: c.updated_at,
    last_activity_at: c.last_activity_at,
    private: c.private,
    can_email: c.can_email,
    primary_email: (_d = (_c = (_b = c.email_addresses) == null ? void 0 : _b[0]) == null ? void 0 : _c.value) != null ? _d : null,
    primary_email_type: (_g = (_f = (_e = c.email_addresses) == null ? void 0 : _e[0]) == null ? void 0 : _f.type) != null ? _g : null,
    primary_phone: (_j = (_i = (_h = c.phone_numbers) == null ? void 0 : _h[0]) == null ? void 0 : _i.value) != null ? _j : null,
    primary_phone_type: (_m = (_l = (_k = c.phone_numbers) == null ? void 0 : _k[0]) == null ? void 0 : _l.type) != null ? _m : null,
    website_url: (_p = (_o = (_n = c.website_addresses) == null ? void 0 : _n.find((w) => w.type === "personal")) == null ? void 0 : _o.value) != null ? _p : null,
    linkedin_url: (_s = (_r = (_q = c.social_media_addresses) == null ? void 0 : _q[0]) == null ? void 0 : _r.value) != null ? _s : null,
    tags: ((_t = c.tags) != null ? _t : []).join(", ")
  };
}

// ../ap/packages/pieces/community/greenhouse/src/lib/actions/create-candidate.ts
var createCandidateAction = (0, import_pieces_framework2.createAction)({
  name: "create_candidate",
  displayName: "Create Candidate",
  description: "Creates a new candidate profile in Greenhouse.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new candidate record in Greenhouse from name plus optional contact details, current role, links, and tags. Use when adding someone to the candidate database; first and last name are required. Not idempotent \u2014 each call creates a separate candidate even with identical input, so to avoid duplicates use Find or Create Candidate when the person may already exist.",
    idempotent: !1
  },
  auth: greenhouseAuth,
  props: {
    first_name: import_pieces_framework2.Property.ShortText({
      displayName: "First Name",
      description: "The candidate's first name.",
      required: !0
    }),
    last_name: import_pieces_framework2.Property.ShortText({
      displayName: "Last Name",
      description: "The candidate's last name.",
      required: !0
    }),
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email Address",
      description: "The candidate's email address.",
      required: !1
    }),
    email_type: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Email Type",
      description: "The type of email address provided.",
      required: !1,
      defaultValue: "personal",
      options: {
        options: [
          { label: "Personal", value: "personal" },
          { label: "Work", value: "work" },
          { label: "Other", value: "other" }
        ]
      }
    }),
    phone: import_pieces_framework2.Property.ShortText({
      displayName: "Phone Number",
      description: "The candidate's phone number.",
      required: !1
    }),
    phone_type: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Phone Type",
      description: "The type of phone number provided.",
      required: !1,
      defaultValue: "mobile",
      options: {
        options: [
          { label: "Mobile", value: "mobile" },
          { label: "Home", value: "home" },
          { label: "Work", value: "work" },
          { label: "Skype", value: "skype" },
          { label: "Other", value: "other" }
        ]
      }
    }),
    company: import_pieces_framework2.Property.ShortText({
      displayName: "Current Company",
      description: "The candidate's current employer.",
      required: !1
    }),
    title: import_pieces_framework2.Property.ShortText({
      displayName: "Current Job Title",
      description: "The candidate's current job title.",
      required: !1
    }),
    linkedin_url: import_pieces_framework2.Property.ShortText({
      displayName: "LinkedIn URL",
      description: "URL to the candidate's LinkedIn profile (e.g. `https://linkedin.com/in/johndoe`).",
      required: !1
    }),
    website_url: import_pieces_framework2.Property.ShortText({
      displayName: "Website URL",
      description: "URL to the candidate's personal website or portfolio.",
      required: !1
    }),
    tags: import_pieces_framework2.Property.Array({
      displayName: "Tags",
      description: "Labels to attach to this candidate (e.g. `referral`, `engineering`). Each item in the list becomes one tag.",
      required: !1
    })
  },
  async run(context) {
    let {
      first_name,
      last_name,
      email,
      email_type,
      phone,
      phone_type,
      company,
      title,
      linkedin_url,
      website_url,
      tags
    } = context.propsValue, body = { first_name, last_name };
    company && (body.company = company), title && (body.title = title), tags && tags.length > 0 && (body.tags = tags), email && (body.email_addresses = [{ value: email, type: email_type != null ? email_type : "personal" }]), phone && (body.phone_numbers = [{ value: phone, type: phone_type != null ? phone_type : "mobile" }]), website_url && (body.website_addresses = [{ value: website_url, type: "personal" }]), linkedin_url && (body.social_media_addresses = [{ value: linkedin_url }]);
    let response = await greenhouseApiCall({
      accessToken: context.auth.access_token,
      method: import_pieces_common2.HttpMethod.POST,
      endpoint: "/candidates",
      body
    });
    return shapeCandidate(response.body.candidate);
  }
});

// ../ap/packages/pieces/community/greenhouse/src/lib/actions/create-candidate-note.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var createCandidateNoteAction = (0, import_pieces_framework3.createAction)({
  name: "create_candidate_note",
  displayName: "Create Candidate Note",
  description: "Adds a note to an existing candidate profile in Greenhouse.",
  audience: "both",
  aiMetadata: {
    description: "Appends a note (or activity entry) to an existing candidate, with a chosen visibility level (public, private, or admin-only) and optional author attribution. Use to log commentary or context against a candidate; requires the numeric candidate ID. Not idempotent \u2014 each call adds a new note, so repeating it appends duplicates.",
    idempotent: !1
  },
  auth: greenhouseAuth,
  props: {
    candidate_id: import_pieces_framework3.Property.ShortText({
      displayName: "Candidate ID",
      description: "The numeric ID of the candidate to add the note to. Found in the candidate URL in Greenhouse (e.g. `https://app.greenhouse.io/people/**123456**`), or mapped from a previous **Create Candidate** step.",
      required: !0
    }),
    note_type: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Note Type",
      description: "The type of note to create.",
      required: !0,
      defaultValue: "NOTE",
      options: {
        options: [
          { label: "Note", value: "NOTE" },
          { label: "Activity", value: "ACTIVITY" }
        ]
      }
    }),
    body: import_pieces_framework3.Property.LongText({
      displayName: "Note",
      description: "The content of the note.",
      required: !0
    }),
    visibility: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Visibility",
      description: "Who can see this note. **Admin Only** \u2014 only admins; **Private** \u2014 only the author; **Public** \u2014 all Greenhouse users.",
      required: !0,
      defaultValue: "public",
      options: {
        options: [
          { label: "Public (all users)", value: "public" },
          { label: "Private (author only)", value: "private" },
          { label: "Admin Only", value: "admin_only" }
        ]
      }
    }),
    user_id: import_pieces_framework3.Property.ShortText({
      displayName: "Author User ID",
      description: "Optional. The Greenhouse user ID to attribute this note to. To find it: go to **Configure \u2192 Users**, click a user, and copy the number from the URL (e.g. `https://app.greenhouse.io/people/**12345**`).",
      required: !1
    })
  },
  async run(context) {
    let { candidate_id, note_type, body, visibility, user_id } = context.propsValue, noteBody = {
      candidate_id: Number(candidate_id),
      note_type,
      body,
      visibility
    };
    user_id && (noteBody.user_id = Number(user_id));
    let note = (await greenhouseApiCall({
      accessToken: context.auth.access_token,
      method: import_pieces_common3.HttpMethod.POST,
      endpoint: "/notes",
      body: noteBody
    })).body;
    return {
      id: note.id,
      candidate_id: note.candidate_id,
      application_id: note.application_id,
      body: note.body,
      subject: note.subject,
      type: note.type,
      visibility: note.visibility,
      created_at: note.created_at,
      updated_at: note.updated_at,
      user_id: note.user_id
    };
  }
});

// ../ap/packages/pieces/community/greenhouse/src/lib/actions/create-prospect.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var createProspectAction = (0, import_pieces_framework4.createAction)({
  name: "create_prospect",
  displayName: "Create Prospect",
  description: "Creates a new prospect in Greenhouse \u2014 a person who has not yet applied to a specific job.",
  audience: "both",
  aiMetadata: {
    description: "Creates a prospect in Greenhouse \u2014 a sourced person flagged as not yet applied to a specific job \u2014 from name plus optional contact details, current role, links, and tags. Use for proactive sourcing rather than active applicants (use Create Candidate for those); first and last name are required. Not idempotent \u2014 each call creates a separate prospect even with identical input.",
    idempotent: !1
  },
  auth: greenhouseAuth,
  props: {
    first_name: import_pieces_framework4.Property.ShortText({
      displayName: "First Name",
      description: "The prospect's first name.",
      required: !0
    }),
    last_name: import_pieces_framework4.Property.ShortText({
      displayName: "Last Name",
      description: "The prospect's last name.",
      required: !0
    }),
    email: import_pieces_framework4.Property.ShortText({
      displayName: "Email Address",
      description: "The prospect's email address.",
      required: !1
    }),
    email_type: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Email Type",
      description: "The type of email address provided.",
      required: !1,
      defaultValue: "personal",
      options: {
        options: [
          { label: "Personal", value: "personal" },
          { label: "Work", value: "work" },
          { label: "Other", value: "other" }
        ]
      }
    }),
    phone: import_pieces_framework4.Property.ShortText({
      displayName: "Phone Number",
      description: "The prospect's phone number.",
      required: !1
    }),
    phone_type: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Phone Type",
      description: "The type of phone number provided.",
      required: !1,
      defaultValue: "mobile",
      options: {
        options: [
          { label: "Mobile", value: "mobile" },
          { label: "Home", value: "home" },
          { label: "Work", value: "work" },
          { label: "Skype", value: "skype" },
          { label: "Other", value: "other" }
        ]
      }
    }),
    company: import_pieces_framework4.Property.ShortText({
      displayName: "Current Company",
      description: "The prospect's current employer.",
      required: !1
    }),
    title: import_pieces_framework4.Property.ShortText({
      displayName: "Current Job Title",
      description: "The prospect's current job title.",
      required: !1
    }),
    linkedin_url: import_pieces_framework4.Property.ShortText({
      displayName: "LinkedIn URL",
      description: "URL to the prospect's LinkedIn profile (e.g. `https://linkedin.com/in/johndoe`).",
      required: !1
    }),
    website_url: import_pieces_framework4.Property.ShortText({
      displayName: "Website URL",
      description: "URL to the prospect's personal website or portfolio.",
      required: !1
    }),
    tags: import_pieces_framework4.Property.Array({
      displayName: "Tags",
      description: "Labels to attach to this prospect (e.g. `referral`, `engineering`). Each item in the list becomes one tag.",
      required: !1
    })
  },
  async run(context) {
    let {
      first_name,
      last_name,
      email,
      email_type,
      phone,
      phone_type,
      company,
      title,
      linkedin_url,
      website_url,
      tags
    } = context.propsValue, body = {
      first_name,
      last_name,
      application: { prospect: !0 }
    };
    company && (body.company = company), title && (body.title = title), tags && tags.length > 0 && (body.tags = tags), email && (body.email_addresses = [{ value: email, type: email_type != null ? email_type : "personal" }]), phone && (body.phone_numbers = [{ value: phone, type: phone_type != null ? phone_type : "mobile" }]), website_url && (body.website_addresses = [{ value: website_url, type: "personal" }]), linkedin_url && (body.social_media_addresses = [{ value: linkedin_url }]);
    let response = await greenhouseApiCall({
      accessToken: context.auth.access_token,
      method: import_pieces_common4.HttpMethod.POST,
      endpoint: "/candidates",
      body
    });
    return shapeCandidate(response.body.candidate);
  }
});

// ../ap/packages/pieces/community/greenhouse/src/lib/actions/update-candidate.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var updateCandidateAction = (0, import_pieces_framework5.createAction)({
  name: "update_candidate",
  displayName: "Update Candidate",
  description: "Updates an existing candidate profile in Greenhouse. Only fields you provide will be changed.",
  audience: "both",
  aiMetadata: {
    description: "Updates fields on an existing candidate identified by numeric candidate ID; only the fields you supply are changed, and the rest are left untouched. Use to amend a candidate name, contact details, current role, links, or tags. Idempotent \u2014 re-sending the same field values leaves the record in the same state; note that supplying email/phone replaces the primary value and supplying tags replaces all existing tags.",
    idempotent: !0
  },
  auth: greenhouseAuth,
  props: {
    candidate_id: import_pieces_framework5.Property.ShortText({
      displayName: "Candidate ID",
      description: "The numeric ID of the candidate to update. Found in the candidate URL in Greenhouse (e.g. `https://app.greenhouse.io/people/**123456**`), or mapped from a previous **Create Candidate** step.",
      required: !0
    }),
    first_name: import_pieces_framework5.Property.ShortText({
      displayName: "First Name",
      description: "Updated first name for the candidate.",
      required: !1
    }),
    last_name: import_pieces_framework5.Property.ShortText({
      displayName: "Last Name",
      description: "Updated last name for the candidate.",
      required: !1
    }),
    email: import_pieces_framework5.Property.ShortText({
      displayName: "Email Address",
      description: "Updated email address. This replaces the candidate's primary email.",
      required: !1
    }),
    email_type: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Email Type",
      description: "The type of the updated email address.",
      required: !1,
      defaultValue: "personal",
      options: {
        options: [
          { label: "Personal", value: "personal" },
          { label: "Work", value: "work" },
          { label: "Other", value: "other" }
        ]
      }
    }),
    phone: import_pieces_framework5.Property.ShortText({
      displayName: "Phone Number",
      description: "Updated phone number. This replaces the candidate's primary phone.",
      required: !1
    }),
    phone_type: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Phone Type",
      description: "The type of the updated phone number.",
      required: !1,
      defaultValue: "mobile",
      options: {
        options: [
          { label: "Mobile", value: "mobile" },
          { label: "Home", value: "home" },
          { label: "Work", value: "work" },
          { label: "Skype", value: "skype" },
          { label: "Other", value: "other" }
        ]
      }
    }),
    company: import_pieces_framework5.Property.ShortText({
      displayName: "Current Company",
      description: "Updated current employer.",
      required: !1
    }),
    title: import_pieces_framework5.Property.ShortText({
      displayName: "Current Job Title",
      description: "Updated current job title.",
      required: !1
    }),
    linkedin_url: import_pieces_framework5.Property.ShortText({
      displayName: "LinkedIn URL",
      description: "Updated LinkedIn profile URL (e.g. `https://linkedin.com/in/johndoe`).",
      required: !1
    }),
    website_url: import_pieces_framework5.Property.ShortText({
      displayName: "Website URL",
      description: "Updated personal website or portfolio URL.",
      required: !1
    }),
    tags: import_pieces_framework5.Property.Array({
      displayName: "Tags",
      description: "Replaces all existing tags on the candidate with this list. Leave blank to keep existing tags unchanged.",
      required: !1
    })
  },
  async run(context) {
    let {
      candidate_id,
      first_name,
      last_name,
      email,
      email_type,
      phone,
      phone_type,
      company,
      title,
      linkedin_url,
      website_url,
      tags
    } = context.propsValue, body = {};
    first_name && (body.first_name = first_name), last_name && (body.last_name = last_name), company && (body.company = company), title && (body.title = title), tags && tags.length > 0 && (body.tags = tags), email && (body.email_addresses = [{ value: email, type: email_type != null ? email_type : "personal" }]), phone && (body.phone_numbers = [{ value: phone, type: phone_type != null ? phone_type : "mobile" }]), website_url && (body.website_addresses = [{ value: website_url, type: "personal" }]), linkedin_url && (body.social_media_addresses = [{ value: linkedin_url }]);
    let response = await greenhouseApiCall({
      accessToken: context.auth.access_token,
      method: import_pieces_common5.HttpMethod.PATCH,
      endpoint: `/candidates/${candidate_id}`,
      body
    });
    return shapeCandidate(response.body.candidate);
  }
});

// ../ap/packages/pieces/community/greenhouse/src/lib/actions/find-candidate.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var findCandidateAction = (0, import_pieces_framework6.createAction)({
  name: "find_candidate",
  displayName: "Find Candidate",
  description: "Searches for a candidate in Greenhouse by email address. Returns the first match.",
  audience: "both",
  aiMetadata: {
    description: "Looks up a candidate by exact email address and returns the first match, or a not-found result if none exists. Use to resolve a candidate ID or check whether someone already exists before creating them. Idempotent read-only lookup; matches only on a full email, not partial names.",
    idempotent: !0
  },
  auth: greenhouseAuth,
  props: {
    email: import_pieces_framework6.Property.ShortText({
      displayName: "Email Address",
      description: "The email address to search for. Returns the first candidate whose profile contains this address.",
      required: !0
    })
  },
  async run(context) {
    let { email } = context.propsValue, response = await greenhouseApiCall({
      accessToken: context.auth.access_token,
      method: import_pieces_common6.HttpMethod.GET,
      endpoint: "/candidates",
      queryParams: { email }
    }), candidates = Array.isArray(response.body) ? response.body : [];
    return candidates.length === 0 ? { found: !1 } : __spreadValues({ found: !0 }, shapeCandidate(candidates[0]));
  }
});

// ../ap/packages/pieces/community/greenhouse/src/lib/actions/find-or-create-candidate.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var findOrCreateCandidateAction = (0, import_pieces_framework7.createAction)({
  name: "find_or_create_candidate",
  displayName: "Find or Create Candidate",
  description: "Searches for a candidate by email address. If no match is found, creates a new candidate profile instead.",
  audience: "both",
  aiMetadata: {
    description: "Looks up a candidate by exact email and returns the existing match if found, otherwise creates a new candidate from the supplied name and details. Prefer this over Create Candidate when the person may already exist, to avoid duplicates. Idempotent \u2014 keyed on email, so repeating with the same email returns the same candidate rather than creating another; first and last name are required for the create path.",
    idempotent: !0
  },
  auth: greenhouseAuth,
  props: {
    email: import_pieces_framework7.Property.ShortText({
      displayName: "Email Address",
      description: "The email address to search for. If a candidate with this address already exists, they will be returned as-is.",
      required: !0
    }),
    first_name: import_pieces_framework7.Property.ShortText({
      displayName: "First Name",
      description: "Required when creating a new candidate. Ignored if a match is found.",
      required: !0
    }),
    last_name: import_pieces_framework7.Property.ShortText({
      displayName: "Last Name",
      description: "Required when creating a new candidate. Ignored if a match is found.",
      required: !0
    }),
    email_type: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Email Type",
      description: "The type of email address. Used only when creating a new candidate.",
      required: !1,
      defaultValue: "personal",
      options: {
        options: [
          { label: "Personal", value: "personal" },
          { label: "Work", value: "work" },
          { label: "Other", value: "other" }
        ]
      }
    }),
    phone: import_pieces_framework7.Property.ShortText({
      displayName: "Phone Number",
      description: "Phone number to set on the new candidate. Ignored if a match is found.",
      required: !1
    }),
    phone_type: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Phone Type",
      description: "The type of phone number. Used only when creating a new candidate.",
      required: !1,
      defaultValue: "mobile",
      options: {
        options: [
          { label: "Mobile", value: "mobile" },
          { label: "Home", value: "home" },
          { label: "Work", value: "work" },
          { label: "Skype", value: "skype" },
          { label: "Other", value: "other" }
        ]
      }
    }),
    company: import_pieces_framework7.Property.ShortText({
      displayName: "Current Company",
      description: "Current employer to set on the new candidate. Ignored if a match is found.",
      required: !1
    }),
    title: import_pieces_framework7.Property.ShortText({
      displayName: "Current Job Title",
      description: "Current job title to set on the new candidate. Ignored if a match is found.",
      required: !1
    }),
    linkedin_url: import_pieces_framework7.Property.ShortText({
      displayName: "LinkedIn URL",
      description: "LinkedIn profile URL to set on the new candidate (e.g. `https://linkedin.com/in/johndoe`). Ignored if a match is found.",
      required: !1
    }),
    website_url: import_pieces_framework7.Property.ShortText({
      displayName: "Website URL",
      description: "Personal website or portfolio URL to set on the new candidate. Ignored if a match is found.",
      required: !1
    }),
    tags: import_pieces_framework7.Property.Array({
      displayName: "Tags",
      description: "Tags to attach to the new candidate. Ignored if a match is found.",
      required: !1
    })
  },
  async run(context) {
    let {
      email,
      first_name,
      last_name,
      email_type,
      phone,
      phone_type,
      company,
      title,
      linkedin_url,
      website_url,
      tags
    } = context.propsValue, searchResponse = await greenhouseApiCall({
      accessToken: context.auth.access_token,
      method: import_pieces_common7.HttpMethod.GET,
      endpoint: "/candidates",
      queryParams: { email }
    }), existing = Array.isArray(searchResponse.body) ? searchResponse.body : [];
    if (existing.length > 0)
      return __spreadValues({ created: !1 }, shapeCandidate(existing[0]));
    let body = { first_name, last_name };
    body.email_addresses = [{ value: email, type: email_type != null ? email_type : "personal" }], company && (body.company = company), title && (body.title = title), tags && tags.length > 0 && (body.tags = tags), phone && (body.phone_numbers = [{ value: phone, type: phone_type != null ? phone_type : "mobile" }]), website_url && (body.website_addresses = [{ value: website_url, type: "personal" }]), linkedin_url && (body.social_media_addresses = [{ value: linkedin_url }]);
    let createResponse = await greenhouseApiCall({
      accessToken: context.auth.access_token,
      method: import_pieces_common7.HttpMethod.POST,
      endpoint: "/candidates",
      body
    });
    return __spreadValues({ created: !0 }, shapeCandidate(createResponse.body.candidate));
  }
});

// ../ap/packages/pieces/community/greenhouse/src/lib/actions/find-due-scorecard.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var findDueScorecardAction = (0, import_pieces_framework8.createAction)({
  name: "find_due_scorecard",
  displayName: "Find Due Scorecard",
  description: "Retrieves the full details of a scorecard by its ID. Use with the New Scorecard Due trigger to look up the scorecard that fired the flow.",
  audience: "both",
  aiMetadata: {
    description: "Fetches the full details of a single scorecard by its numeric ID, returning null if no scorecard matches. Use to enrich a New Scorecard Due trigger event with the complete scorecard record. Idempotent read-only lookup.",
    idempotent: !0
  },
  auth: greenhouseAuth,
  props: {
    scorecard_id: import_pieces_framework8.Property.ShortText({
      displayName: "Scorecard ID",
      description: "The numeric ID of the scorecard to retrieve. Typically mapped from a **New Scorecard Due** trigger step.",
      required: !0
    })
  },
  async run(context) {
    let { scorecard_id } = context.propsValue, response = await greenhouseApiCall({
      accessToken: context.auth.access_token,
      method: import_pieces_common8.HttpMethod.GET,
      endpoint: "/scorecards",
      queryParams: { ids: scorecard_id }
    }), s = (Array.isArray(response.body) ? response.body : [])[0];
    return s ? {
      id: s.id,
      application_id: s.application_id,
      interview_kit_id: s.interview_kit_id,
      interviewer_id: s.interviewer_id,
      submitter_id: s.submitter_id,
      status: s.status,
      candidate_rating: s.candidate_rating,
      notes: s.notes,
      public_notes: s.public_notes,
      private_notes: s.private_notes,
      interviewed_at: s.interviewed_at,
      submitted_at: s.submitted_at,
      created_at: s.created_at,
      updated_at: s.updated_at
    } : null;
  }
});

// ../ap/packages/pieces/community/greenhouse/src/lib/triggers/new-candidate-application.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var polling = {
  strategy: import_pieces_common9.DedupeStrategy.TIMEBASED,
  items: async ({ auth, lastFetchEpochMS }) => {
    let createdAfter = lastFetchEpochMS > 0 ? new Date(lastFetchEpochMS).toISOString() : new Date(Date.now() - 864e5).toISOString(), response = await greenhouseApiCall({
      accessToken: auth.access_token,
      method: import_pieces_common9.HttpMethod.GET,
      endpoint: "/applications",
      queryParams: {
        "created_at[gte]": createdAfter,
        per_page: "500"
      }
    });
    return (Array.isArray(response.body) ? response.body : []).map((app) => {
      var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j;
      return {
        epochMilliSeconds: new Date(app.created_at).getTime(),
        data: {
          id: app.id,
          candidate_id: app.candidate_id,
          job_id: app.job_id,
          job_post_id: app.job_post_id,
          prospect: app.prospect,
          status: app.status,
          created_at: app.created_at,
          rejected_at: app.rejected_at,
          last_activity_at: app.last_activity_at,
          location_address: app.location_address,
          source_id: app.source_id,
          referrer_id: app.referrer_id,
          rejection_reason_id: app.rejection_reason_id,
          stage_id: app.stage_id,
          prospect_office_id: (_b = (_a = app.prospect_details) == null ? void 0 : _a.office_id) != null ? _b : null,
          prospect_department_id: (_d = (_c = app.prospect_details) == null ? void 0 : _c.department_id) != null ? _d : null,
          prospect_pool_id: (_f = (_e = app.prospect_details) == null ? void 0 : _e.pool_id) != null ? _f : null,
          prospect_pool_stage_id: (_h = (_g = app.prospect_details) == null ? void 0 : _g.pool_stage_id) != null ? _h : null,
          prospect_owner_id: (_j = (_i = app.prospect_details) == null ? void 0 : _i.prospect_owner_id) != null ? _j : null
        }
      };
    });
  }
}, newCandidateApplicationTrigger = (0, import_pieces_framework9.createTrigger)({
  auth: greenhouseAuth,
  name: "new_candidate_application",
  displayName: "New Candidate Application",
  description: "Triggers when a new application is submitted by a candidate.",
  aiMetadata: {
    description: "Fires when a new candidate application is created in Greenhouse, representing a candidate applying to (or being added against) a job or prospect pool. Polls for newly created applications and emits each one once."
  },
  props: {},
  sampleData: {
    id: 985314,
    candidate_id: 123456,
    job_id: 144371,
    job_post_id: null,
    prospect: !1,
    status: "active",
    created_at: "2024-03-15T09:30:00.000Z",
    rejected_at: null,
    last_activity_at: "2024-03-15T09:30:00.000Z",
    location_address: "New York, New York, USA",
    source_id: 12,
    referrer_id: null,
    rejection_reason_id: null,
    stage_id: 77,
    prospect_office_id: null,
    prospect_department_id: null,
    prospect_pool_id: null,
    prospect_pool_stage_id: null,
    prospect_owner_id: null
  },
  type: import_pieces_framework9.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common9.pollingHelper.test(polling, context);
  },
  async onEnable(context) {
    await import_pieces_common9.pollingHelper.onEnable(polling, context);
  },
  async onDisable(context) {
    await import_pieces_common9.pollingHelper.onDisable(polling, context);
  },
  async run(context) {
    return await import_pieces_common9.pollingHelper.poll(polling, context);
  }
});

// ../ap/packages/pieces/community/greenhouse/src/lib/triggers/candidate-hired.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var polling2 = {
  strategy: import_pieces_common10.DedupeStrategy.TIMEBASED,
  items: async ({ auth, lastFetchEpochMS }) => {
    let createdAfter = lastFetchEpochMS > 0 ? new Date(lastFetchEpochMS).toISOString() : new Date(Date.now() - 864e5).toISOString(), response = await greenhouseApiCall({
      accessToken: auth.access_token,
      method: import_pieces_common10.HttpMethod.GET,
      endpoint: "/applications",
      queryParams: {
        status: "hired",
        "created_at[gte]": createdAfter,
        per_page: "500"
      }
    });
    return (Array.isArray(response.body) ? response.body : []).map((app) => {
      var _a;
      return {
        epochMilliSeconds: new Date((_a = app.last_activity_at) != null ? _a : app.created_at).getTime(),
        data: {
          id: app.id,
          candidate_id: app.candidate_id,
          job_id: app.job_id,
          job_post_id: app.job_post_id,
          prospect: app.prospect,
          status: app.status,
          created_at: app.created_at,
          hired_at: app.last_activity_at,
          location_address: app.location_address,
          source_id: app.source_id,
          referrer_id: app.referrer_id,
          stage_id: app.stage_id
        }
      };
    });
  }
}, candidateHiredTrigger = (0, import_pieces_framework10.createTrigger)({
  auth: greenhouseAuth,
  name: "candidate_hired",
  displayName: "Candidate Hired",
  description: "Triggers when a candidate is hired.",
  aiMetadata: {
    description: "Fires when a candidate application reaches the hired status in Greenhouse, representing a completed hire. Use to kick off onboarding or downstream HR steps; polls applications filtered to hired status."
  },
  props: {},
  sampleData: {
    id: 48206478,
    candidate_id: 36952451,
    job_id: 211706,
    job_post_id: 123,
    prospect: !1,
    status: "hired",
    created_at: "2024-02-01T14:26:02.000Z",
    hired_at: "2024-03-15T10:00:00.000Z",
    location_address: "New York, New York, USA",
    source_id: 33,
    referrer_id: null,
    stage_id: null
  },
  type: import_pieces_framework10.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common10.pollingHelper.test(polling2, context);
  },
  async onEnable(context) {
    await import_pieces_common10.pollingHelper.onEnable(polling2, context);
  },
  async onDisable(context) {
    await import_pieces_common10.pollingHelper.onDisable(polling2, context);
  },
  async run(context) {
    return await import_pieces_common10.pollingHelper.poll(polling2, context);
  }
});

// ../ap/packages/pieces/community/greenhouse/src/lib/triggers/new-job-post.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var polling3 = {
  strategy: import_pieces_common11.DedupeStrategy.TIMEBASED,
  items: async ({ auth, lastFetchEpochMS }) => {
    let createdAfter = lastFetchEpochMS > 0 ? new Date(lastFetchEpochMS).toISOString() : new Date(Date.now() - 864e5).toISOString(), response = await greenhouseApiCall({
      accessToken: auth.access_token,
      method: import_pieces_common11.HttpMethod.GET,
      endpoint: "/job_posts",
      queryParams: {
        "created_at[gte]": createdAfter,
        per_page: "500"
      }
    });
    return (Array.isArray(response.body) ? response.body : []).map((post) => {
      var _a, _b, _c, _d, _e, _f;
      return {
        epochMilliSeconds: new Date(post.created_at).getTime(),
        data: {
          id: post.id,
          title: post.title,
          job_id: post.job_id,
          job_board_id: post.job_board_id,
          location_name: (_b = (_a = post.location) == null ? void 0 : _a.name) != null ? _b : null,
          location_type: (_d = (_c = post.location) == null ? void 0 : _c.type) != null ? _d : null,
          location_id: (_f = (_e = post.location) == null ? void 0 : _e.id) != null ? _f : null,
          internal: post.internal,
          active: post.active,
          live: post.live,
          featured: post.featured,
          public_url: post.public_url,
          language: post.language,
          first_published_at: post.first_published_at,
          demographic_question_set_id: post.demographic_question_set_id,
          created_at: post.created_at,
          updated_at: post.updated_at
        }
      };
    });
  }
}, newJobPostTrigger = (0, import_pieces_framework11.createTrigger)({
  auth: greenhouseAuth,
  name: "new_job_post",
  displayName: "New Job Post",
  description: "Triggers when a new job post is created.",
  aiMetadata: {
    description: "Fires when a new job post is created in Greenhouse \u2014 a public or internal posting tied to a job, including its location, board, and live/active state. Polls for newly created job posts."
  },
  props: {},
  sampleData: {
    id: 123,
    title: "Software Engineer",
    job_id: 1234,
    job_board_id: 56,
    location_name: "New York, NY",
    location_type: "free_text",
    location_id: null,
    internal: !1,
    active: !0,
    live: !0,
    featured: !1,
    public_url: "https://boards.greenhouse.io/acme/jobs/123",
    language: "en",
    first_published_at: "2024-03-15T10:00:00.000Z",
    demographic_question_set_id: null,
    created_at: "2024-03-15T09:00:00.000Z",
    updated_at: "2024-03-15T10:00:00.000Z"
  },
  type: import_pieces_framework11.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common11.pollingHelper.test(polling3, context);
  },
  async onEnable(context) {
    await import_pieces_common11.pollingHelper.onEnable(polling3, context);
  },
  async onDisable(context) {
    await import_pieces_common11.pollingHelper.onDisable(polling3, context);
  },
  async run(context) {
    return await import_pieces_common11.pollingHelper.poll(polling3, context);
  }
});

// ../ap/packages/pieces/community/greenhouse/src/lib/triggers/job-updated.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var polling4 = {
  strategy: import_pieces_common12.DedupeStrategy.TIMEBASED,
  items: async ({ auth, lastFetchEpochMS }) => {
    let updatedAfter = lastFetchEpochMS > 0 ? new Date(lastFetchEpochMS).toISOString() : new Date(Date.now() - 864e5).toISOString(), response = await greenhouseApiCall({
      accessToken: auth.access_token,
      method: import_pieces_common12.HttpMethod.GET,
      endpoint: "/jobs",
      queryParams: {
        "updated_at[gte]": updatedAfter,
        per_page: "500"
      }
    });
    return (Array.isArray(response.body) ? response.body : []).map((job) => {
      var _a, _b, _c;
      return {
        epochMilliSeconds: new Date(job.updated_at).getTime(),
        data: {
          id: job.id,
          name: job.name,
          requisition_id: job.requisition_id,
          status: job.status,
          confidential: job.confidential,
          is_template: job.is_template,
          created_at: job.created_at,
          opened_at: job.opened_at,
          closed_at: job.closed_at,
          updated_at: job.updated_at,
          department_id: job.department_id,
          office_ids: ((_a = job.office_ids) != null ? _a : []).join(", "),
          open_openings: ((_b = job.openings) != null ? _b : []).filter((o) => o.open).length,
          total_openings: ((_c = job.openings) != null ? _c : []).length
        }
      };
    });
  }
}, jobUpdatedTrigger = (0, import_pieces_framework12.createTrigger)({
  auth: greenhouseAuth,
  name: "job_updated",
  displayName: "Job Updated",
  description: "Triggers when an existing job is updated.",
  aiMetadata: {
    description: "Fires when an existing job in Greenhouse is modified, representing any change to its details, status, or openings. Polls jobs by their updated timestamp, so the same job can fire again each time it changes."
  },
  props: {},
  sampleData: {
    id: 6404,
    name: "Senior Software Engineer",
    requisition_id: "ENG-2024-001",
    status: "open",
    confidential: !1,
    is_template: !1,
    created_at: "2024-01-15T14:42:58.000Z",
    opened_at: "2024-01-20T14:42:58.000Z",
    closed_at: null,
    updated_at: "2024-03-10T16:22:15.000Z",
    department_id: 12345,
    office_ids: "47012",
    open_openings: 2,
    total_openings: 3
  },
  type: import_pieces_framework12.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common12.pollingHelper.test(polling4, context);
  },
  async onEnable(context) {
    await import_pieces_common12.pollingHelper.onEnable(polling4, context);
  },
  async onDisable(context) {
    await import_pieces_common12.pollingHelper.onDisable(polling4, context);
  },
  async run(context) {
    return await import_pieces_common12.pollingHelper.poll(polling4, context);
  }
});

// ../ap/packages/pieces/community/greenhouse/src/lib/triggers/new-scheduled-interview.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common");
var polling5 = {
  strategy: import_pieces_common13.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => {
    var _a;
    let daysAhead = (_a = propsValue.days_ahead) != null ? _a : 7, createdAfter = lastFetchEpochMS > 0 ? new Date(lastFetchEpochMS).toISOString() : new Date(Date.now() - 1440 * 60 * 1e3).toISOString(), startsBefore = new Date(Date.now() + daysAhead * 24 * 60 * 60 * 1e3).toISOString(), response = await greenhouseApiCall({
      accessToken: auth.access_token,
      method: import_pieces_common13.HttpMethod.GET,
      endpoint: "/interviews",
      queryParams: {
        "created_at[gte]": createdAfter,
        "starts_at[lte]": startsBefore,
        per_page: "500"
      }
    });
    return (Array.isArray(response.body) ? response.body : []).map((interview) => ({
      epochMilliSeconds: new Date(interview.created_at).getTime(),
      data: {
        id: interview.id,
        application_id: interview.application_id,
        external_event_id: interview.external_event_id,
        status: interview.status,
        starts_at: interview.starts_at,
        ends_at: interview.ends_at,
        all_day_start_on: interview.all_day_start_on,
        all_day_end_on: interview.all_day_end_on,
        location: interview.location,
        video_conferencing_url: interview.video_conferencing_url,
        organizer_id: interview.organizer_id,
        created_at: interview.created_at,
        updated_at: interview.updated_at
      }
    }));
  }
}, newScheduledInterviewTrigger = (0, import_pieces_framework13.createTrigger)({
  auth: greenhouseAuth,
  name: "new_scheduled_interview",
  displayName: "New Scheduled Interview",
  description: "Triggers when a new scheduled interview is created within the configured time window.",
  aiMetadata: {
    description: "Fires when a newly created interview is scheduled to start within a configurable look-ahead window (in days), representing an upcoming interview for an application. Use for interview reminders or prep; only interviews starting within the time window are emitted."
  },
  props: {
    days_ahead: import_pieces_framework13.Property.Number({
      displayName: "Time Window (Days)",
      description: "Only trigger for interviews scheduled to start within this many days from now. For example, `7` means only interviews starting within the next week will fire this trigger.",
      required: !0,
      defaultValue: 7
    })
  },
  sampleData: {
    id: 109170954,
    application_id: 102717457,
    external_event_id: "external_event_id_1",
    status: "scheduled",
    starts_at: "2024-03-20T13:15:00.000Z",
    ends_at: "2024-03-20T14:15:00.000Z",
    all_day_start_on: null,
    all_day_end_on: null,
    location: "Big Conference Room",
    video_conferencing_url: "https://zoom.us/j/123456789",
    organizer_id: 102361,
    created_at: "2024-03-10T19:22:07.000Z",
    updated_at: "2024-03-10T19:22:07.000Z"
  },
  type: import_pieces_framework13.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common13.pollingHelper.test(polling5, context);
  },
  async onEnable(context) {
    await import_pieces_common13.pollingHelper.onEnable(polling5, context);
  },
  async onDisable(context) {
    await import_pieces_common13.pollingHelper.onDisable(polling5, context);
  },
  async run(context) {
    return await import_pieces_common13.pollingHelper.poll(polling5, context);
  }
});

// ../ap/packages/pieces/community/greenhouse/src/lib/triggers/new-scorecard-due.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_common14 = require("@activepieces/pieces-common");
var polling6 = {
  strategy: import_pieces_common14.DedupeStrategy.TIMEBASED,
  items: async ({ auth, lastFetchEpochMS }) => {
    let createdAfter = lastFetchEpochMS > 0 ? new Date(lastFetchEpochMS).toISOString() : new Date(Date.now() - 864e5).toISOString(), response = await greenhouseApiCall({
      accessToken: auth.access_token,
      method: import_pieces_common14.HttpMethod.GET,
      endpoint: "/scorecards",
      queryParams: {
        "created_at[gte]": createdAfter,
        per_page: "500",
        status: "draft"
      }
    });
    return (Array.isArray(response.body) ? response.body : []).map((s) => ({
      epochMilliSeconds: new Date(s.created_at).getTime(),
      data: {
        id: s.id,
        application_id: s.application_id,
        interview_kit_id: s.interview_kit_id,
        interviewer_id: s.interviewer_id,
        submitter_id: s.submitter_id,
        status: s.status,
        candidate_rating: s.candidate_rating,
        notes: s.notes,
        interviewed_at: s.interviewed_at,
        submitted_at: s.submitted_at,
        created_at: s.created_at,
        updated_at: s.updated_at
      }
    }));
  }
}, newScorecardDueTrigger = (0, import_pieces_framework14.createTrigger)({
  auth: greenhouseAuth,
  name: "new_scorecard_due",
  displayName: "New Scorecard Due",
  description: "Triggers when a new scorecard is due \u2014 i.e. created but not yet submitted by the interviewer.",
  aiMetadata: {
    description: "Fires when a new scorecard becomes due in Greenhouse \u2014 created but still in draft, i.e. not yet submitted by the interviewer. Use to nudge interviewers for pending feedback; polls scorecards filtered to draft status."
  },
  props: {},
  sampleData: {
    id: 11274,
    application_id: 3456,
    interview_kit_id: 789,
    interviewer_id: 4080,
    submitter_id: 4080,
    status: "draft",
    candidate_rating: "strong_yes",
    notes: null,
    interviewed_at: "2024-03-18T16:00:00.000Z",
    submitted_at: null,
    created_at: "2024-03-18T17:00:00.000Z",
    updated_at: "2024-03-18T17:00:00.000Z"
  },
  type: import_pieces_framework14.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common14.pollingHelper.test(polling6, context);
  },
  async onEnable(context) {
    await import_pieces_common14.pollingHelper.onEnable(polling6, context);
  },
  async onDisable(context) {
    await import_pieces_common14.pollingHelper.onDisable(polling6, context);
  },
  async run(context) {
    return await import_pieces_common14.pollingHelper.poll(polling6, context);
  }
});

// ../ap/packages/pieces/community/greenhouse/src/index.ts
var greenhouse = (0, import_pieces_framework15.createPiece)({
  displayName: "Greenhouse",
  description: "Recruiting and onboarding software",
  auth: greenhouseAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/greenhouse.png",
  categories: [import_pieces_framework16.PieceCategory.HUMAN_RESOURCES],
  authors: ["onyedikachi-david"],
  actions: [
    createCandidateAction,
    createCandidateNoteAction,
    createProspectAction,
    updateCandidateAction,
    findCandidateAction,
    findOrCreateCandidateAction,
    findDueScorecardAction,
    (0, import_pieces_common15.createCustomApiCallAction)({
      baseUrl: () => "https://harvest.greenhouse.io/v3",
      auth: greenhouseAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.access_token}`
      })
    })
  ],
  triggers: [newCandidateApplicationTrigger, candidateHiredTrigger, newJobPostTrigger, jobUpdatedTrigger, newScheduledInterviewTrigger, newScorecardDueTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  greenhouse
});
