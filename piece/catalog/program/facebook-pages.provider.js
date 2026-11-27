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

// ../ap/packages/pieces/community/facebook-pages/src/index.ts
var index_exports = {};
__export(index_exports, {
  facebookPages: () => facebookPages,
  facebookPagesAuth: () => facebookPagesAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/facebook-pages/src/lib/actions/create-photo-post.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/facebook-pages/src/lib/common/common.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");
var facebookPagesCommon = {
  baseUrl: "https://graph.facebook.com/v17.0",
  page: import_pieces_framework.Property.Dropdown({
    auth: facebookPagesAuth,
    displayName: "Page",
    required: !0,
    refreshers: [],
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Connect your account"
        };
      try {
        let accessToken = (0, import_pieces_common.getAccessTokenOrThrow)(
          auth
        );
        return {
          options: (await facebookPagesCommon.getPages(accessToken)).map((page) => ({
            label: page.name,
            value: {
              id: page.id,
              accessToken: page.access_token
            }
          })),
          placeholder: "Choose a page"
        };
      } catch (e) {
        return console.debug(e), {
          disabled: !0,
          options: [],
          placeholder: "Connect your account"
        };
      }
    }
  }),
  message: import_pieces_framework.Property.LongText({
    displayName: "Message",
    required: !0
  }),
  link: import_pieces_framework.Property.ShortText({
    displayName: "Link",
    required: !1
  }),
  caption: import_pieces_framework.Property.LongText({
    displayName: "Caption",
    required: !1
  }),
  photo: import_pieces_framework.Property.ShortText({
    displayName: "Photo",
    description: "A URL we can access for the photo",
    required: !0
  }),
  title: import_pieces_framework.Property.ShortText({
    displayName: "Title",
    required: !1
  }),
  description: import_pieces_framework.Property.LongText({
    displayName: "Description",
    required: !1
  }),
  video: import_pieces_framework.Property.ShortText({
    displayName: "Video",
    description: "A URL we can access for the video (Limit: 1GB or 20 minutes)",
    required: !0
  }),
  getPages: async (accessToken) => (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: `${facebookPagesCommon.baseUrl}/me/accounts?access_token=${accessToken}`
  })).body.data,
  createPost: async (page, message, link) => (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.POST,
    url: `${facebookPagesCommon.baseUrl}/${page.id}/feed`,
    body: {
      access_token: page.accessToken,
      message,
      link
    }
  })).body,
  createPhotoPost: async (page, caption, photo) => (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.POST,
    url: `${facebookPagesCommon.baseUrl}/${page.id}/photos`,
    body: {
      access_token: page.accessToken,
      url: photo,
      caption
    }
  })).body,
  createVideoPost: async (page, title, description, video) => (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.POST,
    url: `${facebookPagesCommon.baseUrl}/${page.id}/videos`,
    body: {
      access_token: page.accessToken,
      title,
      description,
      file_url: video
    }
  })).body
};

// ../ap/packages/pieces/community/facebook-pages/src/lib/output-schemas.ts
var createPostActionOutputSchema = {
  fields: [{ key: "id", label: "Post ID" }]
}, createPhotoPostActionOutputSchema = {
  fields: [
    { key: "id", label: "Photo ID" },
    { key: "post_id", label: "Post ID" }
  ]
}, createVideoPostActionOutputSchema = {
  fields: [{ key: "id", label: "Video ID" }]
};

// ../ap/packages/pieces/community/facebook-pages/src/lib/actions/create-photo-post.ts
var createPhotoPost = (0, import_pieces_framework2.createAction)({
  auth: facebookPagesAuth,
  name: "create_photo_post",
  classification: "WRITE",
  displayName: "Create Page Photo",
  description: "Create a photo on a Facebook Page you manage",
  audience: "both",
  aiMetadata: { description: "Publishes a photo post to a Facebook Page the connected account manages by uploading an image from a publicly reachable URL, with an optional caption. Choose this when the post is an image rather than plain text or video. Requires a managed page and a photo URL that Facebook can fetch; not idempotent, as each call creates a new photo post.", idempotent: !1 },
  outputSchema: createPhotoPostActionOutputSchema,
  props: {
    page: facebookPagesCommon.page,
    photo: facebookPagesCommon.photo,
    caption: facebookPagesCommon.caption
  },
  async run(context) {
    let page = context.propsValue.page;
    return await facebookPagesCommon.createPhotoPost(
      page,
      context.propsValue.caption,
      context.propsValue.photo
    );
  }
});

// ../ap/packages/pieces/community/facebook-pages/src/lib/actions/create-post.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var createPost = (0, import_pieces_framework3.createAction)({
  auth: facebookPagesAuth,
  name: "create_post",
  classification: "WRITE",
  displayName: "Create Page Post",
  description: "Create a post on a Facebook Page you manage",
  audience: "both",
  aiMetadata: { description: "Publishes a text status update to the feed of a Facebook Page the connected account manages, optionally attaching a link that Facebook will render as a preview card. Choose this for plain-text or link announcements; use the photo or video post actions for media uploads. Requires selecting a managed page (which supplies the page-scoped access token) and a message; not idempotent, as each call publishes a separate post.", idempotent: !1 },
  outputSchema: createPostActionOutputSchema,
  props: {
    page: facebookPagesCommon.page,
    message: facebookPagesCommon.message,
    link: facebookPagesCommon.link
  },
  async run(context) {
    let page = context.propsValue.page;
    return await facebookPagesCommon.createPost(
      page,
      context.propsValue.message,
      context.propsValue.link
    );
  }
});

// ../ap/packages/pieces/community/facebook-pages/src/lib/actions/create-video-post.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var createVideoPost = (0, import_pieces_framework4.createAction)({
  auth: facebookPagesAuth,
  name: "create_video_post",
  classification: "WRITE",
  displayName: "Create Page Video",
  description: "Create a video on a Facebook Page you manage",
  audience: "both",
  aiMetadata: { description: "Publishes a video post to a Facebook Page the connected account manages by uploading a video from a publicly reachable URL, with an optional title and description. Choose this for video content rather than text or photo posts. Requires a managed page and a video URL Facebook can fetch (limit 1GB or 20 minutes); not idempotent, as each call uploads a new video.", idempotent: !1 },
  outputSchema: createVideoPostActionOutputSchema,
  props: {
    page: facebookPagesCommon.page,
    video: facebookPagesCommon.video,
    title: facebookPagesCommon.title,
    description: facebookPagesCommon.description
  },
  async run(context) {
    let page = context.propsValue.page;
    return await facebookPagesCommon.createVideoPost(
      page,
      context.propsValue.title,
      context.propsValue.description,
      context.propsValue.video
    );
  }
});

// ../ap/packages/pieces/community/facebook-pages/src/index.ts
var markdown = `
To Obtain a Client ID and Client Secret:

1. Go to https://developers.facebook.com/
2. Register for a Facebook Developer account.
3. Once login, click "Make a new app" button.
4. Select "Other" for use cases.
5. Choose "Business" as the type of app. 
6. Provide application details: custom name and associated email.
7. Once your application is created, you need to add a new "product".
8. Configure a new product of type "Facebook Login Settings".
9. Default settings should be fine, you only need to provide the Redirect URL in "Valid OAuth Redirect URIs" and your domain name in "Allowed Domains for the JavaScript SDK".
10. Finally, get your application ID and application secret from your app dashboard in Settings > Basic.
`, facebookPagesAuth = import_pieces_framework5.PieceAuth.OAuth2({
  description: markdown,
  authUrl: "https://graph.facebook.com/oauth/authorize",
  tokenUrl: "https://graph.facebook.com/oauth/access_token",
  required: !0,
  scope: [
    "pages_show_list",
    "pages_manage_posts",
    "business_management",
    "pages_read_engagement"
  ]
}), facebookPages = (0, import_pieces_framework5.createPiece)({
  displayName: "Facebook Pages",
  description: "Manage your Facebook pages to grow your business",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/facebook.png",
  categories: [import_pieces_framework6.PieceCategory.MARKETING],
  authors: ["kishanprmr", "MoShizzle", "khaledmashaly", "abuaboud"],
  auth: facebookPagesAuth,
  actions: [createPost, createPhotoPost, createVideoPost],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  facebookPages,
  facebookPagesAuth
});
