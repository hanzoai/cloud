// notion.js is the in-process Notion connector for the KB long-tail sync. It
// is the generic custom_api_call action bound to Notion's base URL + bearer
// auth — behaviourally identical to @activepieces/piece-notion's own
// custom_api_call (baseUrl https://api.notion.com/v1, Authorization: Bearer
// <access_token>), which is the ONLY Notion action the KB sync invokes. It
// carries none of the 12 Notion actions/triggers the sync never touches, so it
// needs no @notionhq/client bundle. When the full Notion piece is vendored via
// the bundle step, this file is replaced by that blob under the same name.
const { createPiece, PieceAuth } = require('@activepieces/pieces-framework');
const { createCustomApiCallAction } = require('@activepieces/pieces-common');

const notionAuth = PieceAuth.OAuth2({ required: true, authUrl: '', tokenUrl: '', scope: [] });

module.exports.notion = createPiece({
  displayName: 'Notion',
  description: 'The all-in-one workspace',
  auth: notionAuth,
  actions: [
    createCustomApiCallAction({
      auth: notionAuth,
      baseUrl: () => 'https://api.notion.com/v1',
      authMapping: async (auth) => ({
        Authorization: 'Bearer ' + (auth && (auth.access_token || auth)),
      }),
    }),
  ],
  triggers: [],
});
