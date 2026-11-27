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

// ../ap/packages/pieces/community/instagram-business/src/index.ts
var index_exports = {};
__export(index_exports, {
  instagramBusiness: () => instagramBusiness
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/instagram-business/src/lib/actions/upload-photo.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/instagram-business/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework"), markdown = `
To Obtain the following credentials:
1. Visit https://developers.facebook.com/
2. Create an application, Select Other for Usecase.
3. Select Business as App Type.
4. Copy App Id and App Secret from Basic Settings.
`, instagramBusinessAuth = import_pieces_framework.PieceAuth.OAuth2({
  description: markdown,
  authUrl: "https://graph.facebook.com/oauth/authorize",
  tokenUrl: "https://graph.facebook.com/oauth/access_token",
  required: !0,
  scope: ["instagram_basic", "instagram_content_publish", "business_management", "pages_show_list"]
}), instagramCommon = {
  baseUrl: "https://graph.facebook.com/v17.0",
  authentication: instagramBusinessAuth,
  page: import_pieces_framework.Property.Dropdown({
    auth: instagramBusinessAuth,
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
      let accessToken = (0, import_pieces_common.getAccessTokenOrThrow)(
        auth
      );
      return {
        options: (await instagramCommon.getPages(accessToken)).map((page) => page.instagram_business_account ? {
          label: page.name,
          value: {
            id: page.instagram_business_account.id,
            accessToken: page.access_token
          }
        } : null).filter((f) => f !== null),
        placeholder: "Choose a page"
      };
    }
  }),
  caption: import_pieces_framework.Property.LongText({
    displayName: "Caption",
    required: !1
  }),
  photo: import_pieces_framework.Property.ShortText({
    displayName: "Photo",
    description: "A URL we can access for the photo (JPG only)",
    required: !0
  }),
  video: import_pieces_framework.Property.ShortText({
    displayName: "Video",
    description: "A URL we can access for the video (Limit: 1GB or 15 minutes)",
    required: !0
  }),
  getPages: async (accessToken) => (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: `${instagramCommon.baseUrl}/me/accounts`,
    queryParams: {
      access_token: accessToken,
      fields: "id,name,access_token,instagram_business_account"
    }
  })).body.data,
  createPhotoPost: async (page, caption, photo) => {
    let createContainerRequest = await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: `${instagramCommon.baseUrl}/${page.id}/media`,
      body: {
        access_token: page.accessToken,
        image_url: photo,
        caption
      }
    });
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: `${instagramCommon.baseUrl}/${page.id}/media_publish`,
      body: {
        access_token: page.accessToken,
        creation_id: createContainerRequest.body.id
      }
    })).body;
  },
  createVideoPost: async (page, caption, video) => {
    let createContainerRequest = await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: `${instagramCommon.baseUrl}/${page.id}/media`,
      body: {
        access_token: page.accessToken,
        video_url: video,
        caption,
        media_type: "REELS"
      }
    });
    return await isUploadSuccessful(
      createContainerRequest.body.id,
      page.accessToken,
      0
    ) ? (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: `${instagramCommon.baseUrl}/${page.id}/media_publish`,
      body: {
        access_token: page.accessToken,
        creation_id: createContainerRequest.body.id
      }
    })).body : !1;
  }
};
async function isUploadSuccessful(containerId, accessToken, retryCount) {
  return retryCount > 20 ? !1 : (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: `${instagramCommon.baseUrl}/${containerId}`,
    queryParams: {
      access_token: accessToken,
      fields: "status_code"
    }
  })).body.status_code != "FINISHED" ? (await _wait(5e3), await isUploadSuccessful(containerId, accessToken, retryCount + 1)) : !0;
}
function _wait(n) {
  return new Promise((resolve) => setTimeout(resolve, n));
}

// ../ap/packages/pieces/community/instagram-business/src/lib/actions/upload-photo.ts
var uploadPhoto = (0, import_pieces_framework2.createAction)({
  auth: instagramCommon.authentication,
  name: "upload_photo",
  classification: "WRITE",
  displayName: "Upload Photo",
  description: "Upload a photo to an Instagram Professional Account",
  audience: "both",
  aiMetadata: { description: "Publishes a single photo post to an Instagram Professional (Business/Creator) account linked to a selected Facebook Page, with an optional caption. Use to post a new image to Instagram; the photo must be supplied as a publicly accessible JPG URL. Not idempotent \u2014 each call creates and publishes a new post.", idempotent: !1 },
  props: {
    page: instagramCommon.page,
    photo: instagramCommon.photo,
    caption: instagramCommon.caption
  },
  async run({ propsValue }) {
    let page = propsValue.page;
    return await instagramCommon.createPhotoPost(
      page,
      propsValue.caption,
      propsValue.photo
    );
  }
});

// ../ap/packages/pieces/community/instagram-business/src/lib/actions/upload-reel.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var uploadReel = (0, import_pieces_framework3.createAction)({
  auth: instagramCommon.authentication,
  name: "upload_reel",
  classification: "WRITE",
  displayName: "Upload Reel",
  description: "Upload a reel to an Instagram Professional Account",
  audience: "both",
  aiMetadata: { description: "Publishes a video reel to an Instagram Professional (Business/Creator) account linked to a selected Facebook Page, with an optional caption; it waits for the video to finish processing before publishing. Use to post a new reel to Instagram; the video must be a publicly accessible URL (max 1GB or 15 minutes). Not idempotent \u2014 each call creates and publishes a new post.", idempotent: !1 },
  props: {
    page: instagramCommon.page,
    video: instagramCommon.video,
    caption: instagramCommon.caption
  },
  async run(context) {
    let page = context.propsValue.page;
    return await instagramCommon.createVideoPost(
      page,
      context.propsValue.caption,
      context.propsValue.video
    );
  }
});

// ../ap/packages/pieces/community/instagram-business/src/index.ts
var instagramBusiness = (0, import_pieces_framework4.createPiece)({
  displayName: "Instagram for Business",
  description: "Grow your business on Instagram",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/instagram.png",
  categories: [import_pieces_framework5.PieceCategory.BUSINESS_INTELLIGENCE],
  authors: ["kishanprmr", "MoShizzle", "abuaboud"],
  auth: instagramCommon.authentication,
  actions: [uploadPhoto, uploadReel],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  instagramBusiness
});
