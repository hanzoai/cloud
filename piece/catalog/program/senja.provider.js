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

// ../ap/packages/pieces/community/senja/src/index.ts
var index_exports = {};
__export(index_exports, {
  senja: () => senja,
  senjaAuth: () => senjaAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common13 = require("@activepieces/pieces-common"), import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_framework14 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/senja/src/lib/actions/list-testimonials.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/senja/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common");
var BASE_URL = "https://api.senja.io/v1";
async function senjaApiCall({
  token,
  method,
  path,
  body,
  queryParams
}) {
  return import_pieces_common.httpClient.sendRequest({
    method,
    url: `${BASE_URL}${path}`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token
    },
    queryParams,
    body
  });
}
function mapTestimonial({
  testimonial,
  eventType
}) {
  var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n, _o, _p, _q, _r, _s, _t, _u, _v, _w, _x, _y, _z, _A, _B, _C, _D, _E, _F;
  let video = isRecord(testimonial.video) ? testimonial.video : null, mp4Urls = video && isRecord(video.mp4_urls) ? video.mp4_urls : null;
  return __spreadProps(__spreadValues({}, eventType !== void 0 ? { event_type: eventType != null ? eventType : null } : {}), {
    id: (_a = testimonial.id) != null ? _a : null,
    type: (_b = testimonial.type) != null ? _b : null,
    title: (_c = testimonial.title) != null ? _c : null,
    text: (_d = testimonial.text) != null ? _d : null,
    rating: (_e = testimonial.rating) != null ? _e : null,
    url: (_f = testimonial.url) != null ? _f : null,
    date: (_g = testimonial.date) != null ? _g : null,
    approved: (_h = testimonial.approved) != null ? _h : null,
    integration: (_i = testimonial.integration) != null ? _i : null,
    tags: Array.isArray(testimonial.tags) ? testimonial.tags : [],
    lang: (_j = testimonial.lang) != null ? _j : null,
    video_url: (_k = testimonial.video_url) != null ? _k : null,
    thumbnail_url: (_l = testimonial.thumbnail_url) != null ? _l : null,
    form_id: (_m = testimonial.form_id) != null ? _m : null,
    project_id: (_n = testimonial.project_id) != null ? _n : null,
    customer_name: (_o = testimonial.customer_name) != null ? _o : null,
    customer_email: (_p = testimonial.customer_email) != null ? _p : null,
    customer_company: (_q = testimonial.customer_company) != null ? _q : null,
    customer_tagline: (_r = testimonial.customer_tagline) != null ? _r : null,
    customer_username: (_s = testimonial.customer_username) != null ? _s : null,
    customer_url: (_t = testimonial.customer_url) != null ? _t : null,
    customer_avatar: (_u = testimonial.customer_avatar) != null ? _u : null,
    customer_company_logo: (_v = testimonial.customer_company_logo) != null ? _v : null,
    customer_custom_data: (_w = testimonial.customer_custom_data) != null ? _w : null,
    media: Array.isArray(testimonial.media) ? testimonial.media : [],
    video_duration: (_x = video == null ? void 0 : video.duration) != null ? _x : null,
    video_aspect_ratio: (_y = video == null ? void 0 : video.aspect_ratio) != null ? _y : null,
    video_hls_url: (_z = video == null ? void 0 : video.hls_url) != null ? _z : null,
    video_mp4_low: (_A = mp4Urls == null ? void 0 : mp4Urls.low) != null ? _A : null,
    video_mp4_medium: (_B = mp4Urls == null ? void 0 : mp4Urls.medium) != null ? _B : null,
    video_mp4_high: (_C = mp4Urls == null ? void 0 : mp4Urls.high) != null ? _C : null,
    video_transcript: (_D = video == null ? void 0 : video.transcript) != null ? _D : null,
    translations: Array.isArray(testimonial.translations) ? testimonial.translations : [],
    created_at: (_E = testimonial.created_at) != null ? _E : null,
    updated_at: (_F = testimonial.updated_at) != null ? _F : null
  });
}
function isRecord(value) {
  return typeof value == "object" && value !== null && !Array.isArray(value);
}
var SENJA_BASE_URL = BASE_URL, INTEGRATION_OPTIONS = [
  { label: "Twitter", value: "twitter" },
  { label: "LinkedIn", value: "linkedin" },
  { label: "Google", value: "google" },
  { label: "Facebook", value: "facebook" },
  { label: "Airbnb", value: "airbnb" },
  { label: "Amazon", value: "amazon" },
  { label: "App Store", value: "app_store" },
  { label: "Apple Podcasts", value: "apple_podcasts" },
  { label: "AppSumo", value: "appsumo" },
  { label: "Capterra", value: "capterra" },
  { label: "Chrome Web Store", value: "chrome-web-store" },
  { label: "EmbedSocial", value: "embedsocial" },
  { label: "Fiverr", value: "fiverr" },
  { label: "G2", value: "g2" },
  { label: "HomeStars", value: "homestars" },
  { label: "Instagram", value: "instagram" },
  { label: "Play Store", value: "play-store" },
  { label: "Product Hunt", value: "product-hunt" },
  { label: "Realtor", value: "realtor" },
  { label: "Reddit", value: "reddit" },
  { label: "Skillshare", value: "skillshare" },
  { label: "SourceForge", value: "sourceforge" },
  { label: "Testimonial.to", value: "testimonial-to" },
  { label: "TikTok", value: "tiktok" },
  { label: "Trustpilot", value: "trustpilot" },
  { label: "Udemy", value: "udemy" },
  { label: "Upwork", value: "upwork" },
  { label: "Whop", value: "whop" },
  { label: "WordPress", value: "wordpress" },
  { label: "Yelp", value: "yelp" },
  { label: "YouTube", value: "youtube" },
  { label: "Zillow", value: "zillow" }
];

// ../ap/packages/pieces/community/senja/src/lib/output-schemas.ts
var testimonialFields = [
  { key: "id", label: "Testimonial ID" },
  { key: "type", label: "Type" },
  { key: "title", label: "Title" },
  { key: "text", label: "Text" },
  { key: "rating", label: "Rating", format: "number" },
  { key: "url", label: "Source URL", format: "url" },
  { key: "date", label: "Date", format: "datetime" },
  { key: "approved", label: "Approved", format: "boolean" },
  { key: "integration", label: "Source / Integration" },
  { key: "tags", label: "Tags" },
  { key: "lang", label: "Language" },
  { key: "video_url", label: "Video URL", format: "url" },
  { key: "thumbnail_url", label: "Thumbnail", format: "image" },
  { key: "form_id", label: "Form ID" },
  { key: "project_id", label: "Project ID" },
  { key: "customer_name", label: "Customer Name" },
  { key: "customer_email", label: "Customer Email", format: "email" },
  { key: "customer_company", label: "Customer Company" },
  { key: "customer_tagline", label: "Customer Tagline" },
  { key: "customer_username", label: "Customer Username" },
  { key: "customer_url", label: "Customer Profile URL", format: "url" },
  { key: "customer_avatar", label: "Customer Avatar", format: "image" },
  { key: "customer_company_logo", label: "Company Logo", format: "image" },
  { key: "customer_custom_data", label: "Customer Custom Data", dynamicKey: !0 },
  { key: "media", label: "Media" },
  { key: "video_duration", label: "Video Duration", format: "duration" },
  { key: "video_aspect_ratio", label: "Video Aspect Ratio" },
  { key: "video_hls_url", label: "Video HLS URL", format: "url" },
  { key: "video_mp4_low", label: "Video MP4 (Low)", format: "url" },
  { key: "video_mp4_medium", label: "Video MP4 (Medium)", format: "url" },
  { key: "video_mp4_high", label: "Video MP4 (High)", format: "url" },
  { key: "video_transcript", label: "Video Transcript" },
  { key: "translations", label: "Translations" },
  { key: "created_at", label: "Created At", format: "datetime" },
  { key: "updated_at", label: "Updated At", format: "datetime" }
], testimonialActionOutputSchema = {
  fields: testimonialFields
}, findTestimonialsActionOutputSchema = {
  itemLabel: "{customer_name}",
  fields: [
    {
      key: "testimonials",
      label: "Testimonials",
      value: "",
      listItems: testimonialFields
    }
  ]
}, deleteTestimonialActionOutputSchema = {
  fields: [{ key: "message", label: "Message" }]
}, testimonialEventTriggerOutputSchema = {
  fields: [{ key: "event_type", label: "Event Type" }, ...testimonialFields]
};

// ../ap/packages/pieces/community/senja/src/lib/actions/list-testimonials.ts
var listTestimonialsAction = (0, import_pieces_framework.createAction)({
  auth: senjaAuth,
  name: "list_testimonials",
  displayName: "List Testimonials",
  description: "Retrieve all testimonials from your Senja project.",
  audience: "human",
  aiMetadata: { description: "Lists testimonials from a Senja project, returning all of them or only those matching optional filters (full-text search, approval status, type, rating, source platform, tags, language) with sorting and pagination. Use to find, audit, or enumerate testimonials before acting on them. Read-only and idempotent.", idempotent: !0 },
  outputSchema: findTestimonialsActionOutputSchema,
  props: {
    query: import_pieces_framework.Property.ShortText({
      displayName: "Search",
      description: "Full-text search across testimonial text, title, customer name, customer email, and customer tagline.",
      required: !1
    }),
    sort: import_pieces_framework.Property.StaticDropdown({
      displayName: "Sort By",
      description: "Field used to sort the results.",
      required: !1,
      defaultValue: "date",
      options: {
        options: [
          { label: "Date", value: "date" },
          { label: "Rating", value: "rating" }
        ]
      }
    }),
    order: import_pieces_framework.Property.StaticDropdown({
      displayName: "Order",
      description: "Sort direction.",
      required: !1,
      defaultValue: "desc",
      options: {
        options: [
          { label: "Newest first (descending)", value: "desc" },
          { label: "Oldest first (ascending)", value: "asc" }
        ]
      }
    }),
    approved: import_pieces_framework.Property.StaticDropdown({
      displayName: "Approval Status",
      description: "Filter by approval status. Leave empty to return all testimonials.",
      required: !1,
      options: {
        options: [
          { label: "Approved only", value: "true" },
          { label: "Pending only", value: "false" }
        ]
      }
    }),
    type: import_pieces_framework.Property.StaticDropdown({
      displayName: "Testimonial Type",
      description: "Filter by testimonial type. Leave empty to return all types.",
      required: !1,
      options: {
        options: [
          { label: "Text", value: "text" },
          { label: "Video", value: "video" }
        ]
      }
    }),
    rating: import_pieces_framework.Property.Number({
      displayName: "Rating",
      description: "Filter testimonials by star rating (1 to 5).",
      required: !1
    }),
    integration: import_pieces_framework.Property.StaticDropdown({
      displayName: "Source / Integration",
      description: "Filter testimonials by their source platform (e.g. Google, Twitter).",
      required: !1,
      options: {
        options: INTEGRATION_OPTIONS
      }
    }),
    tags: import_pieces_framework.Property.Array({
      displayName: "Tags",
      description: "Filter testimonials that have all of the specified tags.",
      required: !1
    }),
    lang: import_pieces_framework.Property.ShortText({
      displayName: "Language",
      description: 'Filter by language using an ISO 639 code (e.g. "en", "fr", "de").',
      required: !1
    }),
    limit: import_pieces_framework.Property.Number({
      displayName: "Limit",
      description: "Maximum number of testimonials to return (1\u20131000).",
      required: !1,
      defaultValue: 100
    }),
    page: import_pieces_framework.Property.Number({
      displayName: "Page",
      description: "Page number for pagination. Start at 1.",
      required: !1
    })
  },
  async run(context) {
    var _a;
    let {
      query,
      sort,
      order,
      approved,
      type,
      rating,
      integration,
      tags,
      lang,
      limit,
      page
    } = context.propsValue, queryParams = {};
    return query && (queryParams.query = query), sort && (queryParams.sort = sort), order && (queryParams.order = order), approved != null && (queryParams.approved = approved), type && (queryParams.type = type), rating != null && (queryParams.rating = String(rating)), integration && (queryParams.integration = integration), lang && (queryParams.lang = lang), limit != null && (queryParams.limit = String(limit)), page != null && (queryParams.page = String(page)), tags && Array.isArray(tags) && tags.length > 0 && tags.forEach((tag, i) => {
      queryParams[`tags[${i}]`] = tag;
    }), ((_a = (await senjaApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common2.HttpMethod.GET,
      path: "/testimonials",
      queryParams
    })).body.testimonials) != null ? _a : []).map((t) => mapTestimonial({ testimonial: t }));
  }
});

// ../ap/packages/pieces/community/senja/src/lib/actions/get-testimonial.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var getTestimonialAction = (0, import_pieces_framework2.createAction)({
  auth: senjaAuth,
  name: "get_testimonial",
  displayName: "Get Testimonial",
  description: "Retrieve a specific testimonial by its ID.",
  audience: "human",
  aiMetadata: { description: "Fetches a single Senja testimonial by its unique ID. Use when you already have a testimonial ID (e.g. from List Testimonials or a trigger) and need its full details. Requires the testimonial ID; read-only and idempotent.", idempotent: !0 },
  outputSchema: testimonialActionOutputSchema,
  props: {
    id: import_pieces_framework2.Property.ShortText({
      displayName: "Testimonial ID",
      description: 'The unique ID of the testimonial. You can find it in the URL when viewing a testimonial in Senja, or from the output of a "List Testimonials" step.',
      required: !0
    })
  },
  async run(context) {
    let response = await senjaApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common3.HttpMethod.GET,
      path: `/testimonials/${context.propsValue.id}`
    });
    return mapTestimonial({ testimonial: response.body.testimonial });
  }
});

// ../ap/packages/pieces/community/senja/src/lib/actions/create-testimonial.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var createTestimonialAction = (0, import_pieces_framework3.createAction)({
  auth: senjaAuth,
  name: "create_testimonial",
  displayName: "Create Testimonial",
  description: "Add a new testimonial to your Senja project.",
  audience: "human",
  aiMetadata: { description: "Creates a new testimonial in a Senja project, either a text or a video testimonial (set by type). Use to import or record a customer testimonial along with its customer details, rating, tags, and source. Requires type and customer name; text needs body text and video needs a video URL. Not idempotent \u2014 each call creates a separate testimonial.", idempotent: !1 },
  outputSchema: testimonialActionOutputSchema,
  props: {
    type: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Testimonial Type",
      description: 'Choose "Text" for a written testimonial or "Video" for a video testimonial.',
      required: !0,
      options: {
        options: [
          { label: "Text", value: "text" },
          { label: "Video", value: "video" }
        ]
      }
    }),
    customer_name: import_pieces_framework3.Property.ShortText({
      displayName: "Customer Name",
      description: "Full name of the person giving the testimonial.",
      required: !0
    }),
    title: import_pieces_framework3.Property.ShortText({
      displayName: "Title",
      description: "A short headline for the testimonial.",
      required: !1
    }),
    text: import_pieces_framework3.Property.LongText({
      displayName: "Testimonial Text",
      description: "The body of the testimonial. Required for text testimonials.",
      required: !1
    }),
    rating: import_pieces_framework3.Property.Number({
      displayName: "Rating",
      description: "Star rating from 1 (lowest) to 5 (highest).",
      required: !1
    }),
    approved: import_pieces_framework3.Property.Checkbox({
      displayName: "Approved",
      description: "Check to mark the testimonial as approved immediately. Leave unchecked to keep it pending.",
      required: !1,
      defaultValue: !1
    }),
    date: import_pieces_framework3.Property.DateTime({
      displayName: "Date",
      description: "Date the testimonial was given. Leave empty to use today's date.",
      required: !1
    }),
    url: import_pieces_framework3.Property.ShortText({
      displayName: "Source URL",
      description: "Link to the original testimonial (e.g. a tweet URL, Google review link).",
      required: !1
    }),
    video_url: import_pieces_framework3.Property.ShortText({
      displayName: "Video URL",
      description: 'URL of the video testimonial. Required when Testimonial Type is "Video".',
      required: !1
    }),
    customer_email: import_pieces_framework3.Property.ShortText({
      displayName: "Customer Email",
      description: "Email address of the customer.",
      required: !1
    }),
    customer_company: import_pieces_framework3.Property.ShortText({
      displayName: "Customer Company",
      description: "Company or organisation the customer belongs to.",
      required: !1
    }),
    customer_tagline: import_pieces_framework3.Property.ShortText({
      displayName: "Customer Tagline",
      description: 'Short role or tagline for the customer (e.g. "CEO at Acme").',
      required: !1
    }),
    customer_username: import_pieces_framework3.Property.ShortText({
      displayName: "Customer Username",
      description: 'Social media handle or username of the customer (e.g. "@johndoe").',
      required: !1
    }),
    customer_url: import_pieces_framework3.Property.ShortText({
      displayName: "Customer Profile URL",
      description: "Link to the customer's social profile or website.",
      required: !1
    }),
    customer_avatar: import_pieces_framework3.Property.ShortText({
      displayName: "Customer Avatar URL",
      description: "Direct URL to the customer's profile picture.",
      required: !1
    }),
    customer_company_logo: import_pieces_framework3.Property.ShortText({
      displayName: "Company Logo URL",
      description: "Direct URL to the customer's company logo.",
      required: !1
    }),
    integration: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Source / Integration",
      description: "The platform where this testimonial originally came from.",
      required: !1,
      options: {
        options: INTEGRATION_OPTIONS
      }
    }),
    tags: import_pieces_framework3.Property.Array({
      displayName: "Tags",
      description: "Tags to attach to this testimonial for organisation and filtering.",
      required: !1
    }),
    thumbnail_url: import_pieces_framework3.Property.ShortText({
      displayName: "Thumbnail URL",
      description: "URL of the thumbnail image for this testimonial.",
      required: !1
    }),
    form_id: import_pieces_framework3.Property.ShortText({
      displayName: "Form ID",
      description: "ID of the Senja form to associate this testimonial with.",
      required: !1
    }),
    media: import_pieces_framework3.Property.Array({
      displayName: "Media (Images)",
      description: 'List of image URLs to attach to this testimonial. Each entry must be a JSON object with a "url" field and optionally "alt" text, e.g. {"url": "https://...", "alt": "description"}.',
      required: !1
    })
  },
  async run(context) {
    let {
      type,
      customer_name,
      title,
      text,
      rating,
      approved,
      date,
      url,
      video_url,
      customer_email,
      customer_company,
      customer_tagline,
      customer_username,
      customer_url,
      customer_avatar,
      customer_company_logo,
      integration,
      tags,
      thumbnail_url,
      form_id,
      media
    } = context.propsValue, body = {
      type,
      customer_name
    };
    title && (body.title = title), text && (body.text = text), rating != null && (body.rating = rating), approved != null && (body.approved = approved), date && (body.date = date), url && (body.url = url), video_url && (body.video_url = video_url), customer_email && (body.customer_email = customer_email), customer_company && (body.customer_company = customer_company), customer_tagline && (body.customer_tagline = customer_tagline), customer_username && (body.customer_username = customer_username), customer_url && (body.customer_url = customer_url), customer_avatar && (body.customer_avatar = customer_avatar), customer_company_logo && (body.customer_company_logo = customer_company_logo), integration && (body.integration = integration), tags && Array.isArray(tags) && tags.length > 0 && (body.tags = tags), thumbnail_url && (body.thumbnail_url = thumbnail_url), form_id && (body.form_id = form_id), media && Array.isArray(media) && media.length > 0 && (body.media = media.map((item) => {
      if (typeof item == "string")
        try {
          return JSON.parse(item);
        } catch (e) {
          return { url: item };
        }
      return item;
    }));
    let response = await senjaApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common4.HttpMethod.POST,
      path: "/testimonials",
      body
    });
    return mapTestimonial({ testimonial: response.body });
  }
});

// ../ap/packages/pieces/community/senja/src/lib/actions/update-testimonial.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var updateTestimonialAction = (0, import_pieces_framework4.createAction)({
  auth: senjaAuth,
  name: "update_testimonial",
  displayName: "Update Testimonial",
  description: "Approve, unapprove, or update tags on an existing testimonial.",
  audience: "human",
  aiMetadata: { description: "Updates an existing Senja testimonial identified by ID, changing its approval status and/or adding or removing tags. Use to publish/unpublish a testimonial or manage its tags. Requires the testimonial ID and at least one of approval status, tags to add, or tags to remove, or the call fails. Idempotent \u2014 applying the same change yields the same final state.", idempotent: !0 },
  outputSchema: testimonialActionOutputSchema,
  props: {
    id: import_pieces_framework4.Property.ShortText({
      displayName: "Testimonial ID",
      description: 'The unique ID of the testimonial. You can find it in the URL when viewing a testimonial in Senja, or from the output of a "List Testimonials" step.',
      required: !0
    }),
    approved: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Approval Status",
      description: 'Set to "Approved" to publish the testimonial, or "Pending" to unpublish it. Leave empty to keep the current status.',
      required: !1,
      options: {
        options: [
          { label: "Approved", value: "true" },
          { label: "Pending", value: "false" }
        ]
      }
    }),
    add_tags: import_pieces_framework4.Property.Array({
      displayName: "Add Tags",
      description: "Tag names to add to this testimonial. Tags are created automatically if they do not already exist.",
      required: !1
    }),
    remove_tags: import_pieces_framework4.Property.Array({
      displayName: "Remove Tags",
      description: "Tag names to remove from this testimonial.",
      required: !1
    })
  },
  async run(context) {
    let { id, approved, add_tags, remove_tags } = context.propsValue, body = {};
    if (approved != null && (body.approved = approved === "true"), add_tags && Array.isArray(add_tags) && add_tags.length > 0 && (body.add_tags = add_tags), remove_tags && Array.isArray(remove_tags) && remove_tags.length > 0 && (body.remove_tags = remove_tags), Object.keys(body).length === 0)
      throw new Error(
        "At least one field must be provided: Approval Status, Add Tags, or Remove Tags."
      );
    let response = await senjaApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common5.HttpMethod.PATCH,
      path: `/testimonials/${id}`,
      body
    });
    return mapTestimonial({ testimonial: response.body.testimonial });
  }
});

// ../ap/packages/pieces/community/senja/src/lib/actions/delete-testimonial.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var deleteTestimonialAction = (0, import_pieces_framework5.createAction)({
  auth: senjaAuth,
  name: "delete_testimonial",
  displayName: "Delete Testimonial",
  description: "Permanently delete a testimonial from your Senja project. This action cannot be undone.",
  audience: "human",
  aiMetadata: { description: "Permanently deletes a Senja testimonial by its ID; this cannot be undone. Use only when you intend to remove a testimonial for good. Requires the testimonial ID. Not idempotent \u2014 the testimonial exists only on the first successful call and a repeat targets a missing resource.", idempotent: !1 },
  outputSchema: deleteTestimonialActionOutputSchema,
  props: {
    id: import_pieces_framework5.Property.ShortText({
      displayName: "Testimonial ID",
      description: 'The unique ID of the testimonial to delete. You can find it in the URL when viewing a testimonial in Senja, or from the output of a "List Testimonials" step.',
      required: !0
    })
  },
  async run(context) {
    return { message: (await senjaApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common6.HttpMethod.DELETE,
      path: `/testimonials/${context.propsValue.id}`
    })).body.message };
  }
});

// ../ap/packages/pieces/community/senja/src/lib/actions/find-testimonials.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var findTestimonialsAction = (0, import_pieces_framework6.createAction)({
  auth: senjaAuth,
  name: "find_testimonials",
  displayName: "Find Testimonials",
  description: "Search and list testimonials from your Senja project.",
  audience: "ai",
  aiMetadata: { description: "Searches and lists Senja testimonials, returning all of them or only those matching optional filters (full-text query, approval status, type, rating, source platform, tags, language) with sorting and pagination. Pick this over Get Testimonial when you do not yet have a testimonial ID \u2014 it is the resolver that returns the IDs the get/update/delete atomics require. Read-only.", idempotent: !0 },
  outputSchema: findTestimonialsActionOutputSchema,
  props: {
    query: import_pieces_framework6.Property.ShortText({
      displayName: "Search",
      description: "Full-text search across testimonial text, title, customer name, customer email, and customer tagline.",
      required: !1
    }),
    sort: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Sort By",
      description: "Field used to sort the results.",
      required: !1,
      defaultValue: "date",
      options: {
        options: [
          { label: "Date", value: "date" },
          { label: "Rating", value: "rating" }
        ]
      }
    }),
    order: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Order",
      description: "Sort direction.",
      required: !1,
      defaultValue: "desc",
      options: {
        options: [
          { label: "Newest first (descending)", value: "desc" },
          { label: "Oldest first (ascending)", value: "asc" }
        ]
      }
    }),
    approved: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Approval Status",
      description: "Filter by approval status. Leave empty to return all testimonials.",
      required: !1,
      options: {
        options: [
          { label: "Approved only", value: "true" },
          { label: "Pending only", value: "false" }
        ]
      }
    }),
    type: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Testimonial Type",
      description: "Filter by testimonial type. Leave empty to return all types.",
      required: !1,
      options: {
        options: [
          { label: "Text", value: "text" },
          { label: "Video", value: "video" }
        ]
      }
    }),
    rating: import_pieces_framework6.Property.Number({
      displayName: "Rating",
      description: "Filter testimonials by star rating (1 to 5).",
      required: !1
    }),
    integration: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Source / Integration",
      description: "Filter testimonials by their source platform (e.g. Google, Twitter).",
      required: !1,
      options: {
        options: INTEGRATION_OPTIONS
      }
    }),
    tags: import_pieces_framework6.Property.Array({
      displayName: "Tags",
      description: "Filter testimonials that have all of the specified tags.",
      required: !1
    }),
    lang: import_pieces_framework6.Property.ShortText({
      displayName: "Language",
      description: 'Filter by language using an ISO 639 code (e.g. "en", "fr", "de").',
      required: !1
    }),
    limit: import_pieces_framework6.Property.Number({
      displayName: "Limit",
      description: "Maximum number of testimonials to return (1\u20131000).",
      required: !1,
      defaultValue: 100
    }),
    page: import_pieces_framework6.Property.Number({
      displayName: "Page",
      description: "Page number for pagination. Start at 1.",
      required: !1
    })
  },
  async run(context) {
    var _a;
    let {
      query,
      sort,
      order,
      approved,
      type,
      rating,
      integration,
      tags,
      lang,
      limit,
      page
    } = context.propsValue, queryParams = {};
    return query && (queryParams.query = query), sort && (queryParams.sort = sort), order && (queryParams.order = order), approved != null && (queryParams.approved = approved), type && (queryParams.type = type), rating != null && (queryParams.rating = String(rating)), integration && (queryParams.integration = integration), lang && (queryParams.lang = lang), limit != null && (queryParams.limit = String(limit)), page != null && (queryParams.page = String(page)), tags && Array.isArray(tags) && tags.length > 0 && tags.forEach((tag, i) => {
      queryParams[`tags[${i}]`] = tag;
    }), ((_a = (await senjaApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common7.HttpMethod.GET,
      path: "/testimonials",
      queryParams
    })).body.testimonials) != null ? _a : []).map((t) => mapTestimonial({ testimonial: t }));
  }
});

// ../ap/packages/pieces/community/senja/src/lib/actions/fetch-testimonial.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var fetchTestimonialAction = (0, import_pieces_framework7.createAction)({
  auth: senjaAuth,
  name: "fetch_testimonial",
  displayName: "Fetch Testimonial",
  description: "Retrieve a specific testimonial by its ID.",
  audience: "ai",
  aiMetadata: { description: "Fetches one Senja testimonial's full details by its ID. Pick this over Find Testimonials when you already hold a specific testimonial ID (from Find Testimonials or the Testimonial Event trigger) and want only that record. Requires the testimonial ID; resolve unknown IDs via Find Testimonials first. Read-only.", idempotent: !0 },
  outputSchema: testimonialActionOutputSchema,
  props: {
    id: import_pieces_framework7.Property.ShortText({
      displayName: "Testimonial ID",
      description: 'The unique ID of the testimonial. You can find it in the URL when viewing a testimonial in Senja, or from the output of a "List Testimonials" step.',
      required: !0
    })
  },
  async run(context) {
    let response = await senjaApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common8.HttpMethod.GET,
      path: `/testimonials/${context.propsValue.id}`
    });
    return mapTestimonial({ testimonial: response.body.testimonial });
  }
});

// ../ap/packages/pieces/community/senja/src/lib/actions/add-testimonial.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var addTestimonialAction = (0, import_pieces_framework8.createAction)({
  auth: senjaAuth,
  name: "add_testimonial",
  displayName: "Add Testimonial",
  description: "Add a new testimonial to your Senja project.",
  audience: "ai",
  aiMetadata: { description: "Creates a new Senja testimonial \u2014 text or video, set by type \u2014 with the customer's details, rating, tags, and source. Requires type and customer name; a text testimonial also needs body text and a video testimonial needs a video URL. Each call creates a separate record (no de-duplication), so do not retry blindly on timeout.", idempotent: !1 },
  outputSchema: testimonialActionOutputSchema,
  props: {
    type: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Testimonial Type",
      description: 'Choose "Text" for a written testimonial or "Video" for a video testimonial.',
      required: !0,
      options: {
        options: [
          { label: "Text", value: "text" },
          { label: "Video", value: "video" }
        ]
      }
    }),
    customer_name: import_pieces_framework8.Property.ShortText({
      displayName: "Customer Name",
      description: "Full name of the person giving the testimonial.",
      required: !0
    }),
    title: import_pieces_framework8.Property.ShortText({
      displayName: "Title",
      description: "A short headline for the testimonial.",
      required: !1
    }),
    text: import_pieces_framework8.Property.LongText({
      displayName: "Testimonial Text",
      description: "The body of the testimonial. Required for text testimonials.",
      required: !1
    }),
    rating: import_pieces_framework8.Property.Number({
      displayName: "Rating",
      description: "Star rating from 1 (lowest) to 5 (highest).",
      required: !1
    }),
    approved: import_pieces_framework8.Property.Checkbox({
      displayName: "Approved",
      description: "Check to mark the testimonial as approved immediately. Leave unchecked to keep it pending.",
      required: !1,
      defaultValue: !1
    }),
    date: import_pieces_framework8.Property.DateTime({
      displayName: "Date",
      description: "Date the testimonial was given. Leave empty to use today's date.",
      required: !1
    }),
    url: import_pieces_framework8.Property.ShortText({
      displayName: "Source URL",
      description: "Link to the original testimonial (e.g. a tweet URL, Google review link).",
      required: !1
    }),
    video_url: import_pieces_framework8.Property.ShortText({
      displayName: "Video URL",
      description: 'URL of the video testimonial. Required when Testimonial Type is "Video".',
      required: !1
    }),
    customer_email: import_pieces_framework8.Property.ShortText({
      displayName: "Customer Email",
      description: "Email address of the customer.",
      required: !1
    }),
    customer_company: import_pieces_framework8.Property.ShortText({
      displayName: "Customer Company",
      description: "Company or organisation the customer belongs to.",
      required: !1
    }),
    customer_tagline: import_pieces_framework8.Property.ShortText({
      displayName: "Customer Tagline",
      description: 'Short role or tagline for the customer (e.g. "CEO at Acme").',
      required: !1
    }),
    customer_username: import_pieces_framework8.Property.ShortText({
      displayName: "Customer Username",
      description: 'Social media handle or username of the customer (e.g. "@johndoe").',
      required: !1
    }),
    customer_url: import_pieces_framework8.Property.ShortText({
      displayName: "Customer Profile URL",
      description: "Link to the customer's social profile or website.",
      required: !1
    }),
    customer_avatar: import_pieces_framework8.Property.ShortText({
      displayName: "Customer Avatar URL",
      description: "Direct URL to the customer's profile picture.",
      required: !1
    }),
    customer_company_logo: import_pieces_framework8.Property.ShortText({
      displayName: "Company Logo URL",
      description: "Direct URL to the customer's company logo.",
      required: !1
    }),
    integration: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Source / Integration",
      description: "The platform where this testimonial originally came from.",
      required: !1,
      options: {
        options: INTEGRATION_OPTIONS
      }
    }),
    tags: import_pieces_framework8.Property.Array({
      displayName: "Tags",
      description: "Tags to attach to this testimonial for organisation and filtering.",
      required: !1
    }),
    thumbnail_url: import_pieces_framework8.Property.ShortText({
      displayName: "Thumbnail URL",
      description: "URL of the thumbnail image for this testimonial.",
      required: !1
    }),
    form_id: import_pieces_framework8.Property.ShortText({
      displayName: "Form ID",
      description: "ID of the Senja form to associate this testimonial with.",
      required: !1
    }),
    media: import_pieces_framework8.Property.Array({
      displayName: "Media (Images)",
      description: 'List of image URLs to attach to this testimonial. Each entry must be a JSON object with a "url" field and optionally "alt" text, e.g. {"url": "https://...", "alt": "description"}.',
      required: !1
    })
  },
  async run(context) {
    let {
      type,
      customer_name,
      title,
      text,
      rating,
      approved,
      date,
      url,
      video_url,
      customer_email,
      customer_company,
      customer_tagline,
      customer_username,
      customer_url,
      customer_avatar,
      customer_company_logo,
      integration,
      tags,
      thumbnail_url,
      form_id,
      media
    } = context.propsValue, body = {
      type,
      customer_name
    };
    title && (body.title = title), text && (body.text = text), rating != null && (body.rating = rating), approved != null && (body.approved = approved), date && (body.date = date), url && (body.url = url), video_url && (body.video_url = video_url), customer_email && (body.customer_email = customer_email), customer_company && (body.customer_company = customer_company), customer_tagline && (body.customer_tagline = customer_tagline), customer_username && (body.customer_username = customer_username), customer_url && (body.customer_url = customer_url), customer_avatar && (body.customer_avatar = customer_avatar), customer_company_logo && (body.customer_company_logo = customer_company_logo), integration && (body.integration = integration), tags && Array.isArray(tags) && tags.length > 0 && (body.tags = tags), thumbnail_url && (body.thumbnail_url = thumbnail_url), form_id && (body.form_id = form_id), media && Array.isArray(media) && media.length > 0 && (body.media = media.map((item) => {
      if (typeof item == "string")
        try {
          return JSON.parse(item);
        } catch (e) {
          return { url: item };
        }
      return item;
    }));
    let response = await senjaApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common9.HttpMethod.POST,
      path: "/testimonials",
      body
    });
    return mapTestimonial({ testimonial: response.body });
  }
});

// ../ap/packages/pieces/community/senja/src/lib/actions/modify-testimonial.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var modifyTestimonialAction = (0, import_pieces_framework9.createAction)({
  auth: senjaAuth,
  name: "modify_testimonial",
  displayName: "Modify Testimonial",
  description: "Approve, unapprove, or update tags on an existing testimonial.",
  audience: "ai",
  aiMetadata: { description: "Updates an existing Senja testimonial by ID \u2014 change its approval status (publish/unpublish) and/or add or remove tags. Requires the testimonial ID (resolve via Find Testimonials) plus at least one of approval status, tags to add, or tags to remove, or it fails. Applying the same change again converges to the same state.", idempotent: !0 },
  outputSchema: testimonialActionOutputSchema,
  props: {
    id: import_pieces_framework9.Property.ShortText({
      displayName: "Testimonial ID",
      description: 'The unique ID of the testimonial. You can find it in the URL when viewing a testimonial in Senja, or from the output of a "List Testimonials" step.',
      required: !0
    }),
    approved: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Approval Status",
      description: 'Set to "Approved" to publish the testimonial, or "Pending" to unpublish it. Leave empty to keep the current status.',
      required: !1,
      options: {
        options: [
          { label: "Approved", value: "true" },
          { label: "Pending", value: "false" }
        ]
      }
    }),
    add_tags: import_pieces_framework9.Property.Array({
      displayName: "Add Tags",
      description: "Tag names to add to this testimonial. Tags are created automatically if they do not already exist.",
      required: !1
    }),
    remove_tags: import_pieces_framework9.Property.Array({
      displayName: "Remove Tags",
      description: "Tag names to remove from this testimonial.",
      required: !1
    })
  },
  async run(context) {
    let { id, approved, add_tags, remove_tags } = context.propsValue, body = {};
    if (approved != null && (body.approved = approved === "true"), add_tags && Array.isArray(add_tags) && add_tags.length > 0 && (body.add_tags = add_tags), remove_tags && Array.isArray(remove_tags) && remove_tags.length > 0 && (body.remove_tags = remove_tags), Object.keys(body).length === 0)
      throw new Error(
        "At least one field must be provided: Approval Status, Add Tags, or Remove Tags."
      );
    let response = await senjaApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common10.HttpMethod.PATCH,
      path: `/testimonials/${id}`,
      body
    });
    return mapTestimonial({ testimonial: response.body.testimonial });
  }
});

// ../ap/packages/pieces/community/senja/src/lib/actions/remove-testimonial.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var removeTestimonialAction = (0, import_pieces_framework10.createAction)({
  auth: senjaAuth,
  name: "remove_testimonial",
  displayName: "Remove Testimonial",
  description: "Permanently delete a testimonial from your Senja project. This action cannot be undone.",
  audience: "ai",
  aiMetadata: { description: "Permanently deletes a Senja testimonial by ID; this cannot be undone and there is no archive or recovery. Use only when removal is explicitly intended. Requires the testimonial ID (resolve via Find Testimonials). Destructive \u2014 a repeat call targets a now-missing record and returns 404.", idempotent: !1 },
  outputSchema: deleteTestimonialActionOutputSchema,
  props: {
    id: import_pieces_framework10.Property.ShortText({
      displayName: "Testimonial ID",
      description: 'The unique ID of the testimonial to delete. You can find it in the URL when viewing a testimonial in Senja, or from the output of a "List Testimonials" step.',
      required: !0
    })
  },
  async run(context) {
    return { message: (await senjaApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common11.HttpMethod.DELETE,
      path: `/testimonials/${context.propsValue.id}`
    })).body.message };
  }
});

// ../ap/packages/pieces/community/senja/src/lib/triggers/testimonial-event.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var testimonialEventTrigger = (0, import_pieces_framework11.createTrigger)({
  auth: senjaAuth,
  name: "testimonial_event",
  displayName: "Testimonial Event",
  description: "Triggers when a testimonial is created, updated, or deleted in Senja.",
  aiMetadata: {
    description: "Fires when a testimonial is created, updated, or deleted in Senja, delivering the affected testimonial via a manually configured webhook. Can be filtered to only the selected event types (created, updated, deleted) or left to fire on all of them."
  },
  props: {
    instructions: import_pieces_framework11.Property.MarkDown({
      value: `## Setup Instructions

1. In Senja, go to **Automate** in the left sidebar and scroll down to the Webhooks section.
2. Click **Create webhook**.
3. Paste the following URL into the webhook URL field:
\`\`\`text
{{webhookUrl}}
\`\`\`
4. Select the events you want to receive and click **Save**.`,
      variant: import_pieces_framework12.MarkdownVariant.INFO
    }),
    events: import_pieces_framework11.Property.StaticMultiSelectDropdown({
      displayName: "Filter Events",
      description: "Only trigger for the selected event types. Leave empty to trigger on all events. Make sure the matching events are enabled in Senja.",
      required: !1,
      options: {
        options: [
          { label: "Testimonial Created", value: "testimonial_created" },
          { label: "Testimonial Updated", value: "testimonial_updated" },
          { label: "Testimonial Deleted", value: "testimonial_deleted" }
        ]
      }
    })
  },
  sampleData: {
    event_type: "testimonial_created",
    id: "rev_1234567890abcdef",
    type: "text",
    title: "Amazing product experience!",
    text: "This product has completely transformed how we handle our workflow.",
    rating: 5,
    approved: !0,
    date: "2024-01-15T10:30:00Z",
    url: "https://google.com/reviews/123",
    integration: "google",
    tags: ["featured", "enterprise"],
    lang: null,
    video_url: null,
    thumbnail_url: "https://cdn.senja.io/optimized/thumbnail.jpg",
    form_id: "3b5d354-6556-4f88-b29a-99e9552ca906",
    project_id: "7db5d354-6556-4f88-b29a-99e9552ca906",
    customer_name: "Sarah Johnson",
    customer_email: "sarah.johnson@example.com",
    customer_company: "Tech Solutions Inc",
    customer_tagline: "Senior Product Manager",
    customer_username: null,
    customer_url: null,
    customer_avatar: "https://cdn.senja.io/optimized/avatar1.jpg",
    customer_company_logo: "https://cdn.senja.io/optimized/company-logo1.png",
    customer_custom_data: null,
    media: [],
    video_duration: null,
    video_aspect_ratio: null,
    video_hls_url: null,
    video_mp4_low: null,
    video_mp4_medium: null,
    video_mp4_high: null,
    video_transcript: null,
    translations: [],
    created_at: "2024-01-15T10:30:00Z",
    updated_at: "2024-01-15T10:30:00Z"
  },
  type: import_pieces_framework11.TriggerStrategy.WEBHOOK,
  outputSchema: testimonialEventTriggerOutputSchema,
  async onEnable(_context) {
  },
  async onDisable(_context) {
  },
  async run(context) {
    var _a, _b;
    let payload = context.payload.body, eventType = payload.type, selectedEvents = context.propsValue.events;
    if (selectedEvents && selectedEvents.length > 0 && (!eventType || !selectedEvents.includes(eventType)))
      return [];
    let data = payload.data, t = (_b = (_a = eventType === "testimonial_deleted" ? data == null ? void 0 : data.old : data == null ? void 0 : data.new) != null ? _a : data) != null ? _b : payload;
    return [mapTestimonial({ testimonial: t, eventType: eventType != null ? eventType : null })];
  },
  async test(context) {
    var _a;
    return ((_a = (await senjaApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common12.HttpMethod.GET,
      path: "/testimonials",
      queryParams: { limit: "5", sort: "date", order: "desc" }
    })).body.testimonials) != null ? _a : []).map((t) => mapTestimonial({ testimonial: t, eventType: "testimonial_created" }));
  }
});

// ../ap/packages/pieces/community/senja/src/index.ts
var senjaAuth = import_pieces_framework13.PieceAuth.SecretText({
  displayName: "API Key",
  description: `To get your Senja API key:
1. Log in to your Senja account.
2. Go to **Automate** in the left sidebar.
3. Click **REST API** and copy your API key.`,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common13.httpClient.sendRequest({
        method: import_pieces_common13.HttpMethod.GET,
        url: `${SENJA_BASE_URL}/testimonials`,
        authentication: {
          type: import_pieces_common13.AuthenticationType.BEARER_TOKEN,
          token: auth
        },
        queryParams: { limit: "1" }
      }), { valid: !0 };
    } catch (e) {
      return { valid: !1, error: "Invalid API Key. Please check and try again." };
    }
  }
}), senja = (0, import_pieces_framework13.createPiece)({
  displayName: "Senja",
  description: "Collect, manage, and share testimonials with Senja.",
  minimumSupportedRelease: "0.87.0",
  logoUrl: "https://cdn.activepieces.com/pieces/senja.png",
  categories: [import_pieces_framework14.PieceCategory.MARKETING],
  auth: senjaAuth,
  authors: ["bst1n", "onyedikachi-david"],
  actions: [
    listTestimonialsAction,
    getTestimonialAction,
    createTestimonialAction,
    updateTestimonialAction,
    deleteTestimonialAction,
    findTestimonialsAction,
    fetchTestimonialAction,
    addTestimonialAction,
    modifyTestimonialAction,
    removeTestimonialAction,
    (0, import_pieces_common13.createCustomApiCallAction)({
      baseUrl: () => SENJA_BASE_URL,
      auth: senjaAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth}`
      })
    })
  ],
  triggers: [testimonialEventTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  senja,
  senjaAuth
});
