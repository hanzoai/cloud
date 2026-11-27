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

// ../ap/packages/pieces/community/videoask/src/index.ts
var index_exports = {};
__export(index_exports, {
  videoask: () => videoask
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework12 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/videoask/src/lib/actions/create-contact.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/videoask/src/lib/common/props.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/videoask/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.videoask.com";
async function makeRequest(organizationId, access_token, method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        Authorization: `Bearer ${access_token}`,
        "Content-Type": "application/json",
        "organization-id": organizationId
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/videoask/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), authdec = `
`, videoaskAuth = import_pieces_framework.PieceAuth.OAuth2({
  description: authdec,
  authUrl: "https://auth.videoask.com/authorize",
  tokenUrl: "https://auth.videoask.com/oauth/token",
  required: !0,
  scope: []
});

// ../ap/packages/pieces/community/videoask/src/lib/common/props.ts
var organizationIdDropdown = import_pieces_framework2.Property.Dropdown({
  displayName: "Organization ID",
  auth: videoaskAuth,
  description: "Select the organization",
  required: !0,
  refreshers: ["auth"],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first"
      };
    try {
      let access_token = auth.access_token;
      return {
        disabled: !1,
        options: (await makeRequest("", access_token, import_pieces_common2.HttpMethod.GET, "/organizations")).results.map((organization) => ({
          label: organization.name || organization.organization_id,
          value: organization.organization_id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Error loading organizations"
      };
    }
  }
}), videoaskIdDropdown = import_pieces_framework2.Property.Dropdown({
  auth: videoaskAuth,
  displayName: "Team ID",
  description: "Select the team containing the database",
  required: !0,
  refreshers: ["auth", "organizationId"],
  options: async ({ auth, organizationId }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first"
      };
    if (!organizationId)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please select organization first"
      };
    try {
      return {
        disabled: !1,
        options: (await makeRequest(organizationId, auth.access_token, import_pieces_common2.HttpMethod.GET, "/forms")).results.map((videoask2) => ({
          label: videoask2.title,
          value: videoask2.form_id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Error loading teams"
      };
    }
  }
}), tagIdDropdown = import_pieces_framework2.Property.Dropdown({
  displayName: "Tag ID",
  auth: videoaskAuth,
  description: "Select the tag",
  required: !0,
  refreshers: ["auth", "organizationId"],
  options: async ({ auth, organizationId }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first"
      };
    if (!organizationId)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please select organization first"
      };
    try {
      return {
        disabled: !1,
        options: (await makeRequest(organizationId, auth.access_token, import_pieces_common2.HttpMethod.GET, "/tags")).results.map((tag) => ({
          label: tag.title,
          value: tag.tag_id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Error loading tags"
      };
    }
  }
});

// ../ap/packages/pieces/community/videoask/src/lib/actions/create-contact.ts
var createContact = (0, import_pieces_framework3.createAction)({
  auth: videoaskAuth,
  name: "createContact",
  displayName: "create contact",
  description: "Create a new respondent (contact)",
  audience: "both",
  aiMetadata: { description: "Create a new VideoAsk contact (respondent) in an organization; email is required, name and phone optional. Use when you need to register a respondent record. Not idempotent: each call creates a separate contact, with no dedup on email.", idempotent: !1 },
  props: {
    organizationId: organizationIdDropdown,
    name: import_pieces_framework3.Property.ShortText({
      displayName: "Name",
      description: "The name of the contact",
      required: !1
    }),
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email",
      description: "The email of the contact",
      required: !0
    }),
    phone_number: import_pieces_framework3.Property.ShortText({
      displayName: "Phone",
      description: "The phone number of the contact",
      required: !1
    })
  },
  async run(context) {
    let { organizationId, name, email, phone_number } = context.propsValue, access_token = context.auth.access_token, body = {
      name,
      email,
      phone_number
    };
    return await makeRequest(
      organizationId,
      access_token,
      import_pieces_common3.HttpMethod.POST,
      "/contacts",
      body
    );
  }
});

// ../ap/packages/pieces/community/videoask/src/index.ts
var import_pieces_common12 = require("@activepieces/pieces-common"), import_pieces_framework13 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/videoask/src/lib/triggers/form-contact-message.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common");
var formContactMessage = (0, import_pieces_framework4.createTrigger)({
  auth: videoaskAuth,
  name: "formContactMessage",
  displayName: "Form contact message",
  description: "Triggers when a contact sends a message via a form",
  aiMetadata: {
    description: "Fires when a respondent (contact) sends a message in a conversation thread on a specific VideoAsk form. Represents an inbound message from the contact, carrying the message, contact, and form context. Scoped to one form via a registered webhook."
  },
  props: {
    organizationId: organizationIdDropdown,
    formId: videoaskIdDropdown,
    tag: import_pieces_framework4.Property.ShortText({
      displayName: "Tag",
      description: "A short, unique identifier for this webhook. Tags help you easily distinguish webhooks when you need to retrieve, update, or delete them.",
      required: !0
    })
  },
  sampleData: {
    event_id: "35f63f63c4c31d04a938d5b279a6a4e6",
    event_type: "form_contact_message",
    interaction_id: "386bbc0b-bb60-4b62-82fa-d630d0d2b83b",
    contact: {
      contact_id: "6f0cde71-546c-499b-8cdb-c661300fb7f3",
      name: "John Doe",
      email: "john.doe@example.com",
      status: "completed",
      created_at: "2020-08-13T09:07:25.501318Z",
      updated_at: "2020-08-13T09:07:55.461776Z",
      answers: [
        {
          answer_id: "f0f5a564-1f46-49e9-b579-731e6e3f9c4e",
          created_at: "2020-08-13T09:07:31.216396Z",
          is_public: !1,
          poll_options: [
            {
              id: "38ee39f0-aca3-4f3a-a74b-ec65beb139b3",
              content: "Strawberries"
            }
          ],
          question_id: "4d73e927-e0ef-4b73-a5da-cdda038545e1",
          type: "poll",
          poll_option_id: "38ee39f0-aca3-4f3a-a74b-ec65beb139b3",
          poll_option_content: "Strawberries"
        },
        {
          answer_id: "6254f48a-a29d-4623-9525-9a8aeab9b5dc",
          created_at: "2020-08-13T09:07:55.181435Z",
          input_text: "Bacause they're tasty!",
          is_public: !1,
          poll_options: [],
          question_id: "a74ac70a-b207-492d-a3c8-43c8508f5c7a",
          type: "text"
        }
      ],
      messages: [
        {
          author_type: "form_author",
          created_at: "2020-08-13T09:09:47.351432Z",
          gif: "https://media.videoask.com/transcoded/b3286b15-8722-44ad-99dd-ef643f0bb9da/thumbnails/preview.gif?token=eyJ0eXAiOiJKV1QiLCJhbGciOiJSUzI1NiJ9.eyJtZWRpYV9pZCI6ImIzMjg2YjE1LTg3MjItNDRhZC05OWRkLWVmNjQzZjBiYjlkYSIsImV4cCI6MTU5NzkxNDU4N30.sUyDJaJVDlaCTtwXIeytwRwNS9wHLSTge15owz3La2deuJLJd1xLuEEyS35F2hfPLdVsoQl6Whn9fugdCGRl9KUllPYMyJuqJwSh9bzQTsJ_VO7A3kEna13P3VOP5G3skq-XfWn3PWdmNy90HeiDikAbV4RTaxGx6OLlV6s7swqT--lQUhn8NjRYElkPBkhOKtdt_sTGXDfEcvuCB2ymvRjLxHjSB3W0o0MThiizKevEhOuwo4z6su2EECmoQ_ahGVo-f64qgSYGflflYAWtvwLntjEa1uJQ8oCbSJaffk34VHenTLj4bc-rCfbGjXK4azw_HxTm7l8em-hxpoGEm_QzNMar0NGY5hn2nZove6ekDLHUp5DpcwuAhN2nbuswRjCeI3i4f2e8EUa4w_PNHvlMk5vBTErMqNTA-28pyfz5OD1MyEQ7fH5vSnsLvRWvpYFTl-1J22krPTurN_TxPGBaEEyHSJHdK_5rqBE4kwJBwDWY-tPJrRJKKQaRF7AtvtYBhiRFlcU1eX5tWiSJvVKK5jwCLpd8nq58cx1xo5bWDspidNbAWDAh_xTQ6YFQcBX4gVDUPtse-tOBiXFjX-7O7EL3ykkWDlybRUgOw__HcemaneydY1fytJBcK_AUbB46pO0aNct11cP2-S92ie-HTwibymWuoU-YhLDEid4",
          is_public: !0,
          media_duration: 9,
          media_type: "video",
          media_url: "https://media.videoask.com/transcoded/b3286b15-8722-44ad-99dd-ef643f0bb9da/video.mp4?token=eyJ0eXAiOiJKV1QiLCJhbGciOiJSUzI1NiJ9.eyJtZWRpYV9pZCI6ImIzMjg2YjE1LTg3MjItNDRhZC05OWRkLWVmNjQzZjBiYjlkYSIsImV4cCI6MTU5NzkxNDU4N30.sUyDJaJVDlaCTtwXIeytwRwNS9wHLSTge15owz3La2deuJLJd1xLuEEyS35F2hfPLdVsoQl6Whn9fugdCGRl9KUllPYMyJuqJwSh9bzQTsJ_VO7A3kEna13P3VOP5G3skq-XfWn3PWdmNy90HeiDikAbV4RTaxGx6OLlV6s7swqT--lQUhn8NjRYElkPBkhOKtdt_sTGXDfEcvuCB2ymvRjLxHjSB3W0o0MThiizKevEhOuwo4z6su2EECmoQ_ahGVo-f64qgSYGflflYAWtvwLntjEa1uJQ8oCbSJaffk34VHenTLj4bc-rCfbGjXK4azw_HxTm7l8em-hxpoGEm_QzNMar0NGY5hn2nZove6ekDLHUp5DpcwuAhN2nbuswRjCeI3i4f2e8EUa4w_PNHvlMk5vBTErMqNTA-28pyfz5OD1MyEQ7fH5vSnsLvRWvpYFTl-1J22krPTurN_TxPGBaEEyHSJHdK_5rqBE4kwJBwDWY-tPJrRJKKQaRF7AtvtYBhiRFlcU1eX5tWiSJvVKK5jwCLpd8nq58cx1xo5bWDspidNbAWDAh_xTQ6YFQcBX4gVDUPtse-tOBiXFjX-7O7EL3ykkWDlybRUgOw__HcemaneydY1fytJBcK_AUbB46pO0aNct11cP2-S92ie-HTwibymWuoU-YhLDEid4",
          message_id: "e4d547ca-e29b-47cb-9096-8f8bbafedde2",
          share_id: "mctlhdikwzcxs0f618gu5mn247pexyxuqpbvodny",
          share_url: "https://www.videoask.com/mctlhdikwzcxs0f618gu5mn247pexyxuqpbvodny",
          thread_id: "95ae6155-f970-4d18-9116-191777fef28a",
          thumbnail: "https://media.videoask.com/transcoded/b3286b15-8722-44ad-99dd-ef643f0bb9da/thumbnails/image.0000001.jpg?token=eyJ0eXAiOiJKV1QiLCJhbGciOiJSUzI1NiJ9.eyJtZWRpYV9pZCI6ImIzMjg2YjE1LTg3MjItNDRhZC05OWRkLWVmNjQzZjBiYjlkYSIsImV4cCI6MTU5NzkxNDU4N30.sUyDJaJVDlaCTtwXIeytwRwNS9wHLSTge15owz3La2deuJLJd1xLuEEyS35F2hfPLdVsoQl6Whn9fugdCGRl9KUllPYMyJuqJwSh9bzQTsJ_VO7A3kEna13P3VOP5G3skq-XfWn3PWdmNy90HeiDikAbV4RTaxGx6OLlV6s7swqT--lQUhn8NjRYElkPBkhOKtdt_sTGXDfEcvuCB2ymvRjLxHjSB3W0o0MThiizKevEhOuwo4z6su2EECmoQ_ahGVo-f64qgSYGflflYAWtvwLntjEa1uJQ8oCbSJaffk34VHenTLj4bc-rCfbGjXK4azw_HxTm7l8em-hxpoGEm_QzNMar0NGY5hn2nZove6ekDLHUp5DpcwuAhN2nbuswRjCeI3i4f2e8EUa4w_PNHvlMk5vBTErMqNTA-28pyfz5OD1MyEQ7fH5vSnsLvRWvpYFTl-1J22krPTurN_TxPGBaEEyHSJHdK_5rqBE4kwJBwDWY-tPJrRJKKQaRF7AtvtYBhiRFlcU1eX5tWiSJvVKK5jwCLpd8nq58cx1xo5bWDspidNbAWDAh_xTQ6YFQcBX4gVDUPtse-tOBiXFjX-7O7EL3ykkWDlybRUgOw__HcemaneydY1fytJBcK_AUbB46pO0aNct11cP2-S92ie-HTwibymWuoU-YhLDEid4",
          transcode_status: "completed",
          transcribe_status: "completed",
          transcription: "Hello, John. Thank you so much for your response. Can you please explain have it better? Why do you love strawberries? Thank you.",
          transcription_data: [
            {
              words: [
                {
                  word: "Hello,",
                  end_time: 1.1,
                  start_time: 0.8
                },
                {
                  word: "John.",
                  end_time: 1.5,
                  start_time: 1.1
                },
                {
                  word: "Thank",
                  end_time: 1.7,
                  start_time: 1.5
                },
                {
                  word: "you",
                  end_time: 1.8,
                  start_time: 1.7
                },
                {
                  word: "so",
                  end_time: 1.9,
                  start_time: 1.8
                },
                {
                  word: "much",
                  end_time: 2.1,
                  start_time: 1.9
                },
                {
                  word: "for",
                  end_time: 2.3,
                  start_time: 2.1
                },
                {
                  word: "your",
                  end_time: 2.4,
                  start_time: 2.3
                },
                {
                  word: "response.",
                  end_time: 3.1,
                  start_time: 2.4
                },
                {
                  word: "Can",
                  end_time: 4,
                  start_time: 3.9
                },
                {
                  word: "you",
                  end_time: 4.1,
                  start_time: 4
                },
                {
                  word: "please",
                  end_time: 4.8,
                  start_time: 4.1
                },
                {
                  word: "explain",
                  end_time: 5.3,
                  start_time: 4.8
                },
                {
                  word: "have",
                  end_time: 5.5,
                  start_time: 5.3
                },
                {
                  word: "it",
                  end_time: 5.6,
                  start_time: 5.5
                },
                {
                  word: "better?",
                  end_time: 6.1,
                  start_time: 5.6
                },
                {
                  word: "Why",
                  end_time: 6.3,
                  start_time: 6.1
                },
                {
                  word: "do",
                  end_time: 6.4,
                  start_time: 6.3
                },
                {
                  word: "you",
                  end_time: 6.5,
                  start_time: 6.4
                },
                {
                  word: "love",
                  end_time: 6.7,
                  start_time: 6.5
                },
                {
                  word: "strawberries?",
                  end_time: 7.5,
                  start_time: 6.7
                },
                {
                  word: "Thank",
                  end_time: 8.2,
                  start_time: 8
                },
                {
                  word: "you.",
                  end_time: 8.5,
                  start_time: 8.2
                }
              ],
              confidence: 0.90139115,
              transcript: "Hello, John. Thank you so much for your response. Can you please explain have it better? Why do you love strawberries? Thank you."
            }
          ],
          type: "video"
        },
        {
          author_type: "contact",
          created_at: "2020-08-13T09:32:21.909981Z",
          is_public: !1,
          media_type: "text",
          message_id: "511bfc20-691e-471f-9f73-02e1d9888b41",
          share_id: "mqre28jqxifto11ejkxhvln4c3akaks8ejciqlk1",
          share_url: "https://www.videoask.com/mqre28jqxifto11ejkxhvln4c3akaks8ejciqlk1",
          thread_id: "95ae6155-f970-4d18-9116-191777fef28a",
          transcode_status: "completed",
          transcription: `Because they look good, they smell good and they taste good! 
They're even a great source of nutrition and antioxidants.`,
          type: "text",
          input_text: `Because they look good, they smell good and they taste good! 
They're even a great source of nutrition and antioxidants.`
        }
      ],
      platform: "desktop",
      tags: [],
      are_answers_public: !1,
      are_messages_public: !1,
      share_id: "c3rqviz79cva7xmuoo70rva4l5fi699isbzvxjym",
      share_url: "https://www.videoask.com/c3rqviz79cva7xmuoo70rva4l5fi699isbzvxjym"
    },
    form: {
      form_id: "5a1b8066-f92c-47bf-b8f2-3beb0a738122",
      title: "Example form for documentation",
      requires_contact_info: !0,
      requires_contact_email: !0,
      requires_contact_name: !0,
      requires_contact_phone_number: !1,
      hide_branding: !1,
      metadata: {
        primary_color: "#522CBB",
        secondary_color: "#4CD982",
        locale: "en-US",
        show_form_title: !0
      },
      share_id: "f72l3mugt",
      share_url: "https://www.videoask.com/f72l3mugt",
      created_at: "2020-03-30T15:53:59.331687Z",
      updated_at: "2020-04-23T08:40:51.775168Z",
      respondents_count: 13,
      author_id: "8a484e48-2e8e-423d-8d91-b1d6c98b60f2",
      questions: [
        {
          answers_count: 9,
          created_at: "2020-03-30T15:53:59.697150Z",
          form_id: "5a1b8066-f92c-47bf-b8f2-3beb0a738122",
          media_id: "e627bdd1-a9db-41a9-80db-a6546c9c6ba6",
          media_type: "video",
          media_url: "https://media3.giphy.com/media/xUPGcuomRFMUcsB9nO/giphy.mp4?cid=c83daf2546ecd961cfc590162cdb4787bf1c482527149606&rid=giphy.mp4",
          metadata: {
            text: "What's your favorite fruit?",
            darken_text_background: !0,
            fit_video: !1
          },
          poll_options: [
            {
              id: "27b7cd85-db1e-4316-8dbe-772523feb098",
              content: "Bananas",
              total_count: 3
            },
            {
              id: "38ee39f0-aca3-4f3a-a74b-ec65beb139b3",
              content: "Strawberries",
              total_count: 4
            },
            {
              id: "b15efc9c-ae2e-4254-b653-50c8ef2b7364",
              content: "Apples",
              total_count: 0
            },
            {
              id: "3c4a9c2e-fab0-47bf-b545-ff2d77ce3b21",
              content: "Grapes",
              total_count: 1
            },
            {
              id: "6daada9f-cc7b-4ffe-9529-f16f35517770",
              content: "Other",
              total_count: 1
            }
          ],
          allow_multiple_selection: !1,
          question_id: "4d73e927-e0ef-4b73-a5da-cdda038545e1",
          share_id: "f72l3mugt",
          share_url: "https://www.videoask.com/f72l3mugt",
          thumbnail: "https://media3.giphy.com/media/xUPGcuomRFMUcsB9nO/giphy-preview.gif?cid=c83daf2546ecd961cfc590162cdb4787bf1c482527149606&rid=giphy-preview.gif",
          transcode_status: "completed",
          transcribe_status: "completed",
          type: "poll",
          updated_at: "2020-03-30T15:57:11.319072Z"
        },
        {
          allowed_answer_media_types: [
            "video",
            "audio",
            "text"
          ],
          created_at: "2020-03-30T15:57:10.931514Z",
          form_id: "5a1b8066-f92c-47bf-b8f2-3beb0a738122",
          media_id: "8031b331-c5e1-4fda-8523-314c2fa5cdd8",
          media_type: "video",
          media_url: "https://media2.giphy.com/media/9nIIo3LBIlzvW/giphy.mp4?cid=c83daf25eacd7951d2151b3805a0177237e0b1c9c45d0b31&rid=giphy.mp4",
          metadata: {
            text: "Why do you love that fruit?",
            darken_text_background: !0,
            fit_video: !1
          },
          allow_multiple_selection: !1,
          question_id: "a74ac70a-b207-492d-a3c8-43c8508f5c7a",
          share_id: "f72l3mugt",
          share_url: "https://www.videoask.com/f72l3mugt",
          thumbnail: "https://media2.giphy.com/media/9nIIo3LBIlzvW/giphy-preview.gif?cid=c83daf25eacd7951d2151b3805a0177237e0b1c9c45d0b31&rid=giphy-preview.gif",
          transcode_status: "completed",
          transcribe_status: "completed",
          type: "standard",
          updated_at: "2020-03-30T15:57:11.324552Z"
        }
      ],
      are_answers_public: !1,
      notifications: {
        send_contact_message_emails: !0
      }
    },
    message: {
      author_type: "contact",
      created_at: "2020-08-13T09:32:21.909981Z",
      is_public: !1,
      media_type: "text",
      message_id: "511bfc20-691e-471f-9f73-02e1d9888b41",
      share_id: "mqre28jqxifto11ejkxhvln4c3akaks8ejciqlk1",
      share_url: "https://www.videoask.com/mqre28jqxifto11ejkxhvln4c3akaks8ejciqlk1",
      thread_id: "95ae6155-f970-4d18-9116-191777fef28a",
      transcode_status: "completed",
      transcription: `Because they look good, they smell good and they taste good! 
They're even a great source of nutrition and antioxidants.`,
      type: "text",
      input_text: `Because they look good, they smell good and they taste good! 
They're even a great source of nutrition and antioxidants.`
    }
  },
  type: import_pieces_framework4.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    await makeRequest(
      context.propsValue.organizationId,
      context.auth.access_token,
      import_pieces_common4.HttpMethod.PUT,
      `/forms/${context.propsValue.formId}/webhooks/${context.propsValue.tag}`,
      {
        url: context.webhookUrl,
        event_types: ["form_contact_message"]
      }
    );
  },
  async onDisable(context) {
    await makeRequest(
      context.propsValue.organizationId,
      context.auth.access_token,
      import_pieces_common4.HttpMethod.DELETE,
      `/forms/${context.propsValue.formId}/webhooks/${context.propsValue.tag}`
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/videoask/src/lib/triggers/form-transcribed.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common"), formTranscribed = (0, import_pieces_framework5.createTrigger)({
  auth: videoaskAuth,
  name: "formTranscribed",
  displayName: "Form transcribed",
  description: "Triggered when a form is transcribed",
  aiMetadata: {
    description: "Fires when a response submitted to a specific VideoAsk form finishes transcription, signaling that text transcripts of the respondent's audio/video answers are now available alongside the contact and form data. Scoped to one form via a registered webhook."
  },
  props: {
    organizationId: organizationIdDropdown,
    formId: videoaskIdDropdown,
    tag: import_pieces_framework5.Property.ShortText({
      displayName: "Tag",
      description: "A short, unique identifier for this webhook. Tags help you easily distinguish webhooks when you need to retrieve, update, or delete them.",
      required: !0
    })
  },
  sampleData: {
    event_id: "1cc057d1a8acc59ed2ed7a7cd5edbf1b",
    event_type: "form_response_transcribed",
    interaction_id: "386bbc0b-bb60-4b62-82fa-d630d0d2b83b",
    contact: {
      contact_id: "7c6550ff-fe15-424a-918e-2c1936db8aed",
      name: "John Doe",
      email: "john.doe@example.com",
      phone_number: null,
      thumbnail: "https://media.videoask.com/transcoded/a8v1jwbdwwc7uq0keni9kf0c5l42vt42gp23f4ej-00001.jpg",
      status: "completed",
      created_at: "2020-03-30T16:04:09.021715Z",
      updated_at: "2020-03-30T16:05:08.952236Z",
      answers: [
        {
          answer_id: "0f4ab93a-4914-4e4a-a33b-8466fda6147a",
          created_at: "2020-03-30T16:04:09.353595Z",
          poll_option_content: "Grapes",
          poll_option_id: "3c4a9c2e-fab0-47bf-b545-ff2d77ce3b21",
          question_id: "4d73e927-e0ef-4b73-a5da-cdda038545e1",
          type: "poll"
        },
        {
          answer_id: "995948a9-8a02-48f8-a018-769f0f25e7f8",
          created_at: "2020-03-30T16:05:05.434020Z",
          media_url: "https://media.videoask.com/transcoded/a8v1jwbdwwc7uq0keni9kf0c5l42vt42gp23f4ej.mp4",
          question_id: "a74ac70a-b207-492d-a3c8-43c8508f5c7a",
          share_id: "a8v1jwbdwwc7uq0keni9kf0c5l42vt42gp23f4ej",
          share_url: "https://www.videoask.com/a8v1jwbdwwc7uq0keni9kf0c5l42vt42gp23f4ej",
          thumbnail: "https://media.videoask.com/transcoded/a8v1jwbdwwc7uq0keni9kf0c5l42vt42gp23f4ej-00001.jpg",
          transcode_status: "completed",
          transcription: "I don't actually know, but once I start eating them, I can never stop.",
          type: "video"
        }
      ]
    },
    form: {
      form_id: "5a1b8066-f92c-47bf-b8f2-3beb0a738122",
      title: "Example form for documentation",
      requires_contact_info: !0,
      requires_contact_email: !0,
      requires_contact_name: !0,
      requires_contact_phone_number: !1,
      hide_branding: !1,
      metadata: {
        primary_color: "#522CBB",
        secondary_color: "#4CD982",
        locale: "en-US",
        show_form_title: !0
      },
      share_id: "f72l3mugt",
      share_url: "https://www.videoask.com/f72l3mugt",
      created_at: "2020-03-30T15:53:59.331687Z",
      updated_at: "2020-03-30T15:57:11.330236Z",
      author_id: "8a484e48-2e8e-423d-8d91-b1d6c98b60f2",
      questions: [
        {
          question_id: "4d73e927-e0ef-4b73-a5da-cdda038545e1",
          form_id: "5a1b8066-f92c-47bf-b8f2-3beb0a738122",
          metadata: {
            text: "What's your favorite fruit?",
            darken_text_background: !0,
            fit_video: !1
          },
          type: "poll",
          share_id: "ql0eet9ga",
          share_url: "https://www.videoask.com/ql0eet9ga",
          created_at: "2020-03-30T15:53:59.697150Z",
          updated_at: "2020-03-30T15:57:11.319072Z",
          thumbnail: "https://media3.giphy.com/media/xUPGcuomRFMUcsB9nO/giphy-preview.gif?cid=c83daf2546ecd961cfc590162cdb4787bf1c482527149606&rid=giphy-preview.gif",
          transcode_status: "completed",
          media_type: "video",
          media_url: "https://media3.giphy.com/media/xUPGcuomRFMUcsB9nO/giphy.mp4?cid=c83daf2546ecd961cfc590162cdb4787bf1c482527149606&rid=giphy.mp4",
          poll_options: [
            {
              id: "27b7cd85-db1e-4316-8dbe-772523feb098",
              content: "Bananas"
            },
            {
              id: "38ee39f0-aca3-4f3a-a74b-ec65beb139b3",
              content: "Strawberries"
            },
            {
              id: "b15efc9c-ae2e-4254-b653-50c8ef2b7364",
              content: "Apples"
            },
            {
              id: "3c4a9c2e-fab0-47bf-b545-ff2d77ce3b21",
              content: "Grapes"
            },
            {
              id: "6daada9f-cc7b-4ffe-9529-f16f35517770",
              content: "Other"
            }
          ]
        },
        {
          question_id: "a74ac70a-b207-492d-a3c8-43c8508f5c7a",
          form_id: "5a1b8066-f92c-47bf-b8f2-3beb0a738122",
          metadata: {
            text: "Why do you love that fruit?",
            darken_text_background: !0,
            fit_video: !1
          },
          type: "standard",
          allowed_answer_media_types: [
            "video",
            "audio",
            "text"
          ],
          share_id: "qrv6rnyg3",
          share_url: "https://www.videoask.com/qrv6rnyg3",
          created_at: "2020-03-30T15:57:10.931514Z",
          updated_at: "2020-03-30T15:57:11.324552Z",
          thumbnail: "https://media2.giphy.com/media/9nIIo3LBIlzvW/giphy-preview.gif?cid=c83daf25eacd7951d2151b3805a0177237e0b1c9c45d0b31&rid=giphy-preview.gif",
          transcode_status: "completed",
          media_type: "video",
          media_url: "https://media2.giphy.com/media/9nIIo3LBIlzvW/giphy.mp4?cid=c83daf25eacd7951d2151b3805a0177237e0b1c9c45d0b31&rid=giphy.mp4"
        }
      ]
    }
  },
  type: import_pieces_framework5.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    await makeRequest(
      context.propsValue.organizationId,
      context.auth.access_token,
      import_pieces_common5.HttpMethod.PUT,
      `/forms/${context.propsValue.formId}/webhooks/${context.propsValue.tag}`,
      {
        url: context.webhookUrl,
        event_types: ["form_response_transcribed"]
      }
    );
  },
  async onDisable(context) {
    await makeRequest(
      context.propsValue.organizationId,
      context.auth.access_token,
      import_pieces_common5.HttpMethod.DELETE,
      `/forms/${context.propsValue.formId}/webhooks/${context.propsValue.tag}`
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/videoask/src/lib/triggers/new-form-response.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common");
var newFormResponse = (0, import_pieces_framework6.createTrigger)({
  auth: videoaskAuth,
  name: "newFormResponse",
  displayName: "new form response",
  description: "Trigger when a new form response is submitted",
  aiMetadata: {
    description: "Fires when a respondent submits a new response to a specific VideoAsk form, carrying the contact and their answers along with the form context. Use as the entry point for processing incoming form submissions. Scoped to one form via a registered webhook."
  },
  props: {
    organizationId: organizationIdDropdown,
    formId: videoaskIdDropdown,
    tag: import_pieces_framework6.Property.ShortText({
      displayName: "Tag",
      description: "A short, unique identifier for this webhook. Tags help you easily distinguish webhooks when you need to retrieve, update, or delete them.",
      required: !0
    })
  },
  sampleData: {
    event_id: "7df19833701662b6414ed1765dcc626c",
    event_type: "form_response",
    interaction_id: "386bbc0b-bb60-4b62-82fa-d630d0d2b83b",
    contact: {
      contact_id: "7c6550ff-fe15-424a-918e-2c1936db8aed",
      name: "John Doe",
      email: "john.doe@example.com",
      phone_number: null,
      thumbnail: "https://media.videoask.com/transcoded/a8v1jwbdwwc7uq0keni9kf0c5l42vt42gp23f4ej-00001.jpg",
      status: "completed",
      created_at: "2020-03-30T16:04:09.021715Z",
      updated_at: "2020-03-30T16:05:08.952236Z",
      answers: [
        {
          answer_id: "0f4ab93a-4914-4e4a-a33b-8466fda6147a",
          created_at: "2020-03-30T16:04:09.353595Z",
          poll_option_content: "Grapes",
          poll_option_id: "3c4a9c2e-fab0-47bf-b545-ff2d77ce3b21",
          question_id: "4d73e927-e0ef-4b73-a5da-cdda038545e1",
          type: "poll"
        },
        {
          answer_id: "995948a9-8a02-48f8-a018-769f0f25e7f8",
          created_at: "2020-03-30T16:05:05.434020Z",
          media_url: "https://media.videoask.com/transcoded/a8v1jwbdwwc7uq0keni9kf0c5l42vt42gp23f4ej.mp4",
          media_duration: 6,
          question_id: "a74ac70a-b207-492d-a3c8-43c8508f5c7a",
          share_id: "a8v1jwbdwwc7uq0keni9kf0c5l42vt42gp23f4ej",
          share_url: "https://www.videoask.com/a8v1jwbdwwc7uq0keni9kf0c5l42vt42gp23f4ej",
          thumbnail: "https://media.videoask.com/transcoded/a8v1jwbdwwc7uq0keni9kf0c5l42vt42gp23f4ej-00001.jpg",
          transcode_status: "completed",
          type: "video"
        }
      ]
    },
    form: {
      form_id: "5a1b8066-f92c-47bf-b8f2-3beb0a738122",
      title: "Example form for documentation",
      requires_contact_info: !0,
      requires_contact_email: !0,
      requires_contact_name: !0,
      requires_contact_phone_number: !1,
      hide_branding: !1,
      metadata: {
        primary_color: "#522CBB",
        secondary_color: "#4CD982",
        locale: "en-US",
        show_form_title: !0
      },
      share_id: "f72l3mugt",
      share_url: "https://www.videoask.com/f72l3mugt",
      created_at: "2020-03-30T15:53:59.331687Z",
      updated_at: "2020-03-30T15:57:11.330236Z",
      respondents_count: 2,
      author_id: "8a484e48-2e8e-423d-8d91-b1d6c98b60f2",
      questions: [
        {
          question_id: "4d73e927-e0ef-4b73-a5da-cdda038545e1",
          form_id: "5a1b8066-f92c-47bf-b8f2-3beb0a738122",
          metadata: {
            text: "What's your favorite fruit?",
            darken_text_background: !0,
            fit_video: !1
          },
          type: "poll",
          share_id: "ql0eet9ga",
          share_url: "https://www.videoask.com/ql0eet9ga",
          created_at: "2020-03-30T15:53:59.697150Z",
          updated_at: "2020-03-30T15:57:11.319072Z",
          thumbnail: "https://media3.giphy.com/media/xUPGcuomRFMUcsB9nO/giphy-preview.gif?cid=c83daf2546ecd961cfc590162cdb4787bf1c482527149606&rid=giphy-preview.gif",
          transcode_status: "completed",
          media_type: "video",
          media_url: "https://media3.giphy.com/media/xUPGcuomRFMUcsB9nO/giphy.mp4?cid=c83daf2546ecd961cfc590162cdb4787bf1c482527149606&rid=giphy.mp4",
          poll_options: [
            {
              id: "27b7cd85-db1e-4316-8dbe-772523feb098",
              content: "Bananas"
            },
            {
              id: "38ee39f0-aca3-4f3a-a74b-ec65beb139b3",
              content: "Strawberries"
            },
            {
              id: "b15efc9c-ae2e-4254-b653-50c8ef2b7364",
              content: "Apples"
            },
            {
              id: "3c4a9c2e-fab0-47bf-b545-ff2d77ce3b21",
              content: "Grapes"
            },
            {
              id: "6daada9f-cc7b-4ffe-9529-f16f35517770",
              content: "Other"
            }
          ]
        },
        {
          question_id: "a74ac70a-b207-492d-a3c8-43c8508f5c7a",
          form_id: "5a1b8066-f92c-47bf-b8f2-3beb0a738122",
          metadata: {
            text: "Why do you love that fruit?",
            darken_text_background: !0,
            fit_video: !1
          },
          type: "standard",
          allowed_answer_media_types: [
            "video",
            "audio",
            "text"
          ],
          share_id: "qrv6rnyg3",
          share_url: "https://www.videoask.com/qrv6rnyg3",
          created_at: "2020-03-30T15:57:10.931514Z",
          updated_at: "2020-03-30T15:57:11.324552Z",
          thumbnail: "https://media2.giphy.com/media/9nIIo3LBIlzvW/giphy-preview.gif?cid=c83daf25eacd7951d2151b3805a0177237e0b1c9c45d0b31&rid=giphy-preview.gif",
          transcode_status: "completed",
          media_type: "video",
          media_url: "https://media2.giphy.com/media/9nIIo3LBIlzvW/giphy.mp4?cid=c83daf25eacd7951d2151b3805a0177237e0b1c9c45d0b31&rid=giphy.mp4"
        }
      ]
    }
  },
  type: import_pieces_framework6.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    await makeRequest(
      context.propsValue.organizationId,
      context.auth.access_token,
      import_pieces_common6.HttpMethod.PUT,
      `/forms/${context.propsValue.formId}/webhooks/${context.propsValue.tag}`,
      {
        url: context.webhookUrl,
        event_types: ["form_response"]
      }
    );
  },
  async onDisable(context) {
    await makeRequest(
      context.propsValue.organizationId,
      context.auth.access_token,
      import_pieces_common6.HttpMethod.DELETE,
      `/forms/${context.propsValue.formId}/webhooks/${context.propsValue.tag}`
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/videoask/src/lib/triggers/new-form-author-response.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common");
var newFormAuthorResponse = (0, import_pieces_framework7.createTrigger)({
  auth: videoaskAuth,
  name: "newFormAuthorResponse",
  displayName: "New form author response",
  description: "Trigger when a form author responds to a contact message",
  aiMetadata: {
    description: "Fires when a form author (the form owner/team) replies to a contact within a conversation thread on a specific VideoAsk form, carrying the author message, contact, and form context. Use to react to outbound author replies. Scoped to one form via a registered webhook."
  },
  props: {
    organizationId: organizationIdDropdown,
    formId: videoaskIdDropdown,
    tag: import_pieces_framework7.Property.ShortText({
      displayName: "Tag",
      description: "A short, unique identifier for this webhook. Tags help you easily distinguish webhooks when you need to retrieve, update, or delete them.",
      required: !0
    })
  },
  sampleData: {
    event_id: "a69aeb109cd0c1408e71c3836e84e74f",
    event_type: "form_author_message",
    contact: {
      contact_id: "6f0cde71-546c-499b-8cdb-c661300fb7f3",
      name: "John Doe",
      email: "john.doe@example.com",
      status: "completed",
      created_at: "2020-08-13T09:07:25.501318Z",
      updated_at: "2020-08-13T09:07:55.461776Z",
      answers: [
        {
          answer_id: "f0f5a564-1f46-49e9-b579-731e6e3f9c4e",
          created_at: "2020-08-13T09:07:31.216396Z",
          is_public: !1,
          poll_options: [
            {
              id: "38ee39f0-aca3-4f3a-a74b-ec65beb139b3",
              content: "Strawberries"
            }
          ],
          question_id: "4d73e927-e0ef-4b73-a5da-cdda038545e1",
          type: "poll",
          poll_option_id: "38ee39f0-aca3-4f3a-a74b-ec65beb139b3",
          poll_option_content: "Strawberries"
        },
        {
          answer_id: "6254f48a-a29d-4623-9525-9a8aeab9b5dc",
          created_at: "2020-08-13T09:07:55.181435Z",
          input_text: "Bacause they're tasty!",
          is_public: !1,
          poll_options: [],
          question_id: "a74ac70a-b207-492d-a3c8-43c8508f5c7a",
          type: "text"
        }
      ],
      messages: [
        {
          author_type: "form_author",
          created_at: "2020-08-13T09:09:47.351432Z",
          gif: "https://media.videoask.com/transcoded/b3286b15-8722-44ad-99dd-ef643f0bb9da/thumbnails/preview.gif?token=eyJ0eXAiOiJKV1QiLCJhbGciOiJSUzI1NiJ9.eyJtZWRpYV9pZCI6ImIzMjg2YjE1LTg3MjItNDRhZC05OWRkLWVmNjQzZjBiYjlkYSIsImV4cCI6MTU5NzkxNDU4N30.sUyDJaJVDlaCTtwXIeytwRwNS9wHLSTge15owz3La2deuJLJd1xLuEEyS35F2hfPLdVsoQl6Whn9fugdCGRl9KUllPYMyJuqJwSh9bzQTsJ_VO7A3kEna13P3VOP5G3skq-XfWn3PWdmNy90HeiDikAbV4RTaxGx6OLlV6s7swqT--lQUhn8NjRYElkPBkhOKtdt_sTGXDfEcvuCB2ymvRjLxHjSB3W0o0MThiizKevEhOuwo4z6su2EECmoQ_ahGVo-f64qgSYGflflYAWtvwLntjEa1uJQ8oCbSJaffk34VHenTLj4bc-rCfbGjXK4azw_HxTm7l8em-hxpoGEm_QzNMar0NGY5hn2nZove6ekDLHUp5DpcwuAhN2nbuswRjCeI3i4f2e8EUa4w_PNHvlMk5vBTErMqNTA-28pyfz5OD1MyEQ7fH5vSnsLvRWvpYFTl-1J22krPTurN_TxPGBaEEyHSJHdK_5rqBE4kwJBwDWY-tPJrRJKKQaRF7AtvtYBhiRFlcU1eX5tWiSJvVKK5jwCLpd8nq58cx1xo5bWDspidNbAWDAh_xTQ6YFQcBX4gVDUPtse-tOBiXFjX-7O7EL3ykkWDlybRUgOw__HcemaneydY1fytJBcK_AUbB46pO0aNct11cP2-S92ie-HTwibymWuoU-YhLDEid4",
          is_public: !0,
          media_duration: 9,
          media_type: "video",
          media_url: "https://media.videoask.com/transcoded/b3286b15-8722-44ad-99dd-ef643f0bb9da/video.mp4?token=eyJ0eXAiOiJKV1QiLCJhbGciOiJSUzI1NiJ9.eyJtZWRpYV9pZCI6ImIzMjg2YjE1LTg3MjItNDRhZC05OWRkLWVmNjQzZjBiYjlkYSIsImV4cCI6MTU5NzkxNDU4N30.sUyDJaJVDlaCTtwXIeytwRwNS9wHLSTge15owz3La2deuJLJd1xLuEEyS35F2hfPLdVsoQl6Whn9fugdCGRl9KUllPYMyJuqJwSh9bzQTsJ_VO7A3kEna13P3VOP5G3skq-XfWn3PWdmNy90HeiDikAbV4RTaxGx6OLlV6s7swqT--lQUhn8NjRYElkPBkhOKtdt_sTGXDfEcvuCB2ymvRjLxHjSB3W0o0MThiizKevEhOuwo4z6su2EECmoQ_ahGVo-f64qgSYGflflYAWtvwLntjEa1uJQ8oCbSJaffk34VHenTLj4bc-rCfbGjXK4azw_HxTm7l8em-hxpoGEm_QzNMar0NGY5hn2nZove6ekDLHUp5DpcwuAhN2nbuswRjCeI3i4f2e8EUa4w_PNHvlMk5vBTErMqNTA-28pyfz5OD1MyEQ7fH5vSnsLvRWvpYFTl-1J22krPTurN_TxPGBaEEyHSJHdK_5rqBE4kwJBwDWY-tPJrRJKKQaRF7AtvtYBhiRFlcU1eX5tWiSJvVKK5jwCLpd8nq58cx1xo5bWDspidNbAWDAh_xTQ6YFQcBX4gVDUPtse-tOBiXFjX-7O7EL3ykkWDlybRUgOw__HcemaneydY1fytJBcK_AUbB46pO0aNct11cP2-S92ie-HTwibymWuoU-YhLDEid4",
          message_id: "e4d547ca-e29b-47cb-9096-8f8bbafedde2",
          share_id: "mctlhdikwzcxs0f618gu5mn247pexyxuqpbvodny",
          share_url: "https://www.videoask.com/mctlhdikwzcxs0f618gu5mn247pexyxuqpbvodny",
          thread_id: "95ae6155-f970-4d18-9116-191777fef28a",
          thumbnail: "https://media.videoask.com/transcoded/b3286b15-8722-44ad-99dd-ef643f0bb9da/thumbnails/image.0000001.jpg?token=eyJ0eXAiOiJKV1QiLCJhbGciOiJSUzI1NiJ9.eyJtZWRpYV9pZCI6ImIzMjg2YjE1LTg3MjItNDRhZC05OWRkLWVmNjQzZjBiYjlkYSIsImV4cCI6MTU5NzkxNDU4N30.sUyDJaJVDlaCTtwXIeytwRwNS9wHLSTge15owz3La2deuJLJd1xLuEEyS35F2hfPLdVsoQl6Whn9fugdCGRl9KUllPYMyJuqJwSh9bzQTsJ_VO7A3kEna13P3VOP5G3skq-XfWn3PWdmNy90HeiDikAbV4RTaxGx6OLlV6s7swqT--lQUhn8NjRYElkPBkhOKtdt_sTGXDfEcvuCB2ymvRjLxHjSB3W0o0MThiizKevEhOuwo4z6su2EECmoQ_ahGVo-f64qgSYGflflYAWtvwLntjEa1uJQ8oCbSJaffk34VHenTLj4bc-rCfbGjXK4azw_HxTm7l8em-hxpoGEm_QzNMar0NGY5hn2nZove6ekDLHUp5DpcwuAhN2nbuswRjCeI3i4f2e8EUa4w_PNHvlMk5vBTErMqNTA-28pyfz5OD1MyEQ7fH5vSnsLvRWvpYFTl-1J22krPTurN_TxPGBaEEyHSJHdK_5rqBE4kwJBwDWY-tPJrRJKKQaRF7AtvtYBhiRFlcU1eX5tWiSJvVKK5jwCLpd8nq58cx1xo5bWDspidNbAWDAh_xTQ6YFQcBX4gVDUPtse-tOBiXFjX-7O7EL3ykkWDlybRUgOw__HcemaneydY1fytJBcK_AUbB46pO0aNct11cP2-S92ie-HTwibymWuoU-YhLDEid4",
          transcode_status: "completed",
          transcribe_status: "completed",
          transcription: "Hello, John. Thank you so much for your response. Can you please explain have it better? Why do you love strawberries? Thank you.",
          transcription_data: [
            {
              words: [
                {
                  word: "Hello,",
                  end_time: 1.1,
                  start_time: 0.8
                },
                {
                  word: "John.",
                  end_time: 1.5,
                  start_time: 1.1
                },
                {
                  word: "Thank",
                  end_time: 1.7,
                  start_time: 1.5
                },
                {
                  word: "you",
                  end_time: 1.8,
                  start_time: 1.7
                },
                {
                  word: "so",
                  end_time: 1.9,
                  start_time: 1.8
                },
                {
                  word: "much",
                  end_time: 2.1,
                  start_time: 1.9
                },
                {
                  word: "for",
                  end_time: 2.3,
                  start_time: 2.1
                },
                {
                  word: "your",
                  end_time: 2.4,
                  start_time: 2.3
                },
                {
                  word: "response.",
                  end_time: 3.1,
                  start_time: 2.4
                },
                {
                  word: "Can",
                  end_time: 4,
                  start_time: 3.9
                },
                {
                  word: "you",
                  end_time: 4.1,
                  start_time: 4
                },
                {
                  word: "please",
                  end_time: 4.8,
                  start_time: 4.1
                },
                {
                  word: "explain",
                  end_time: 5.3,
                  start_time: 4.8
                },
                {
                  word: "have",
                  end_time: 5.5,
                  start_time: 5.3
                },
                {
                  word: "it",
                  end_time: 5.6,
                  start_time: 5.5
                },
                {
                  word: "better?",
                  end_time: 6.1,
                  start_time: 5.6
                },
                {
                  word: "Why",
                  end_time: 6.3,
                  start_time: 6.1
                },
                {
                  word: "do",
                  end_time: 6.4,
                  start_time: 6.3
                },
                {
                  word: "you",
                  end_time: 6.5,
                  start_time: 6.4
                },
                {
                  word: "love",
                  end_time: 6.7,
                  start_time: 6.5
                },
                {
                  word: "strawberries?",
                  end_time: 7.5,
                  start_time: 6.7
                },
                {
                  word: "Thank",
                  end_time: 8.2,
                  start_time: 8
                },
                {
                  word: "you.",
                  end_time: 8.5,
                  start_time: 8.2
                }
              ],
              confidence: 0.90139115,
              transcript: "Hello, John. Thank you so much for your response. Can you please explain have it better? Why do you love strawberries? Thank you."
            }
          ],
          type: "video"
        }
      ],
      platform: "desktop",
      tags: [],
      are_answers_public: !1,
      are_messages_public: !1,
      share_id: "c3rqviz79cva7xmuoo70rva4l5fi699isbzvxjym",
      share_url: "https://www.videoask.com/c3rqviz79cva7xmuoo70rva4l5fi699isbzvxjym"
    },
    form: {
      form_id: "5a1b8066-f92c-47bf-b8f2-3beb0a738122",
      title: "Example form for documentation",
      requires_contact_info: !0,
      requires_contact_email: !0,
      requires_contact_name: !0,
      requires_contact_phone_number: !1,
      hide_branding: !1,
      metadata: {
        primary_color: "#522CBB",
        secondary_color: "#4CD982",
        locale: "en-US",
        show_form_title: !0
      },
      share_id: "f72l3mugt",
      share_url: "https://www.videoask.com/f72l3mugt",
      created_at: "2020-03-30T15:53:59.331687Z",
      updated_at: "2020-04-23T08:40:51.775168Z",
      respondents_count: 13,
      author_id: "8a484e48-2e8e-423d-8d91-b1d6c98b60f2",
      questions: [
        {
          answers_count: 9,
          created_at: "2020-03-30T15:53:59.697150Z",
          form_id: "5a1b8066-f92c-47bf-b8f2-3beb0a738122",
          media_id: "e627bdd1-a9db-41a9-80db-a6546c9c6ba6",
          media_type: "video",
          media_url: "https://media3.giphy.com/media/xUPGcuomRFMUcsB9nO/giphy.mp4?cid=c83daf2546ecd961cfc590162cdb4787bf1c482527149606&rid=giphy.mp4",
          metadata: {
            text: "What's your favorite fruit?",
            darken_text_background: !0,
            fit_video: !1
          },
          poll_options: [
            {
              id: "27b7cd85-db1e-4316-8dbe-772523feb098",
              content: "Bananas",
              total_count: 3
            },
            {
              id: "38ee39f0-aca3-4f3a-a74b-ec65beb139b3",
              content: "Strawberries",
              total_count: 4
            },
            {
              id: "b15efc9c-ae2e-4254-b653-50c8ef2b7364",
              content: "Apples",
              total_count: 0
            },
            {
              id: "3c4a9c2e-fab0-47bf-b545-ff2d77ce3b21",
              content: "Grapes",
              total_count: 1
            },
            {
              id: "6daada9f-cc7b-4ffe-9529-f16f35517770",
              content: "Other",
              total_count: 1
            }
          ],
          allow_multiple_selection: !1,
          question_id: "4d73e927-e0ef-4b73-a5da-cdda038545e1",
          share_id: "f72l3mugt",
          share_url: "https://www.videoask.com/f72l3mugt",
          thumbnail: "https://media3.giphy.com/media/xUPGcuomRFMUcsB9nO/giphy-preview.gif?cid=c83daf2546ecd961cfc590162cdb4787bf1c482527149606&rid=giphy-preview.gif",
          transcode_status: "completed",
          transcribe_status: "completed",
          type: "poll",
          updated_at: "2020-03-30T15:57:11.319072Z"
        },
        {
          allowed_answer_media_types: [
            "video",
            "audio",
            "text"
          ],
          created_at: "2020-03-30T15:57:10.931514Z",
          form_id: "5a1b8066-f92c-47bf-b8f2-3beb0a738122",
          media_id: "8031b331-c5e1-4fda-8523-314c2fa5cdd8",
          media_type: "video",
          media_url: "https://media2.giphy.com/media/9nIIo3LBIlzvW/giphy.mp4?cid=c83daf25eacd7951d2151b3805a0177237e0b1c9c45d0b31&rid=giphy.mp4",
          metadata: {
            text: "Why do you love that fruit?",
            darken_text_background: !0,
            fit_video: !1
          },
          allow_multiple_selection: !1,
          question_id: "a74ac70a-b207-492d-a3c8-43c8508f5c7a",
          share_id: "f72l3mugt",
          share_url: "https://www.videoask.com/f72l3mugt",
          thumbnail: "https://media2.giphy.com/media/9nIIo3LBIlzvW/giphy-preview.gif?cid=c83daf25eacd7951d2151b3805a0177237e0b1c9c45d0b31&rid=giphy-preview.gif",
          transcode_status: "completed",
          transcribe_status: "completed",
          type: "standard",
          updated_at: "2020-03-30T15:57:11.324552Z"
        }
      ],
      are_answers_public: !1,
      notifications: {
        send_contact_message_emails: !0
      }
    },
    message: {
      author_type: "form_author",
      created_at: "2020-08-13T09:09:47.351432Z",
      gif: "https://media.videoask.com/transcoded/b3286b15-8722-44ad-99dd-ef643f0bb9da/thumbnails/preview.gif?token=eyJ0eXAiOiJKV1QiLCJhbGciOiJSUzI1NiJ9.eyJtZWRpYV9pZCI6ImIzMjg2YjE1LTg3MjItNDRhZC05OWRkLWVmNjQzZjBiYjlkYSIsImV4cCI6MTU5NzkxNDU4N30.sUyDJaJVDlaCTtwXIeytwRwNS9wHLSTge15owz3La2deuJLJd1xLuEEyS35F2hfPLdVsoQl6Whn9fugdCGRl9KUllPYMyJuqJwSh9bzQTsJ_VO7A3kEna13P3VOP5G3skq-XfWn3PWdmNy90HeiDikAbV4RTaxGx6OLlV6s7swqT--lQUhn8NjRYElkPBkhOKtdt_sTGXDfEcvuCB2ymvRjLxHjSB3W0o0MThiizKevEhOuwo4z6su2EECmoQ_ahGVo-f64qgSYGflflYAWtvwLntjEa1uJQ8oCbSJaffk34VHenTLj4bc-rCfbGjXK4azw_HxTm7l8em-hxpoGEm_QzNMar0NGY5hn2nZove6ekDLHUp5DpcwuAhN2nbuswRjCeI3i4f2e8EUa4w_PNHvlMk5vBTErMqNTA-28pyfz5OD1MyEQ7fH5vSnsLvRWvpYFTl-1J22krPTurN_TxPGBaEEyHSJHdK_5rqBE4kwJBwDWY-tPJrRJKKQaRF7AtvtYBhiRFlcU1eX5tWiSJvVKK5jwCLpd8nq58cx1xo5bWDspidNbAWDAh_xTQ6YFQcBX4gVDUPtse-tOBiXFjX-7O7EL3ykkWDlybRUgOw__HcemaneydY1fytJBcK_AUbB46pO0aNct11cP2-S92ie-HTwibymWuoU-YhLDEid4",
      is_public: !0,
      media_duration: 9,
      media_type: "video",
      media_url: "https://media.videoask.com/transcoded/b3286b15-8722-44ad-99dd-ef643f0bb9da/video.mp4?token=eyJ0eXAiOiJKV1QiLCJhbGciOiJSUzI1NiJ9.eyJtZWRpYV9pZCI6ImIzMjg2YjE1LTg3MjItNDRhZC05OWRkLWVmNjQzZjBiYjlkYSIsImV4cCI6MTU5NzkxNDU4N30.sUyDJaJVDlaCTtwXIeytwRwNS9wHLSTge15owz3La2deuJLJd1xLuEEyS35F2hfPLdVsoQl6Whn9fugdCGRl9KUllPYMyJuqJwSh9bzQTsJ_VO7A3kEna13P3VOP5G3skq-XfWn3PWdmNy90HeiDikAbV4RTaxGx6OLlV6s7swqT--lQUhn8NjRYElkPBkhOKtdt_sTGXDfEcvuCB2ymvRjLxHjSB3W0o0MThiizKevEhOuwo4z6su2EECmoQ_ahGVo-f64qgSYGflflYAWtvwLntjEa1uJQ8oCbSJaffk34VHenTLj4bc-rCfbGjXK4azw_HxTm7l8em-hxpoGEm_QzNMar0NGY5hn2nZove6ekDLHUp5DpcwuAhN2nbuswRjCeI3i4f2e8EUa4w_PNHvlMk5vBTErMqNTA-28pyfz5OD1MyEQ7fH5vSnsLvRWvpYFTl-1J22krPTurN_TxPGBaEEyHSJHdK_5rqBE4kwJBwDWY-tPJrRJKKQaRF7AtvtYBhiRFlcU1eX5tWiSJvVKK5jwCLpd8nq58cx1xo5bWDspidNbAWDAh_xTQ6YFQcBX4gVDUPtse-tOBiXFjX-7O7EL3ykkWDlybRUgOw__HcemaneydY1fytJBcK_AUbB46pO0aNct11cP2-S92ie-HTwibymWuoU-YhLDEid4",
      message_id: "e4d547ca-e29b-47cb-9096-8f8bbafedde2",
      share_id: "mctlhdikwzcxs0f618gu5mn247pexyxuqpbvodny",
      share_url: "https://www.videoask.com/mctlhdikwzcxs0f618gu5mn247pexyxuqpbvodny",
      thread_id: "95ae6155-f970-4d18-9116-191777fef28a",
      thumbnail: "https://media.videoask.com/transcoded/b3286b15-8722-44ad-99dd-ef643f0bb9da/thumbnails/image.0000001.jpg?token=eyJ0eXAiOiJKV1QiLCJhbGciOiJSUzI1NiJ9.eyJtZWRpYV9pZCI6ImIzMjg2YjE1LTg3MjItNDRhZC05OWRkLWVmNjQzZjBiYjlkYSIsImV4cCI6MTU5NzkxNDU4N30.sUyDJaJVDlaCTtwXIeytwRwNS9wHLSTge15owz3La2deuJLJd1xLuEEyS35F2hfPLdVsoQl6Whn9fugdCGRl9KUllPYMyJuqJwSh9bzQTsJ_VO7A3kEna13P3VOP5G3skq-XfWn3PWdmNy90HeiDikAbV4RTaxGx6OLlV6s7swqT--lQUhn8NjRYElkPBkhOKtdt_sTGXDfEcvuCB2ymvRjLxHjSB3W0o0MThiizKevEhOuwo4z6su2EECmoQ_ahGVo-f64qgSYGflflYAWtvwLntjEa1uJQ8oCbSJaffk34VHenTLj4bc-rCfbGjXK4azw_HxTm7l8em-hxpoGEm_QzNMar0NGY5hn2nZove6ekDLHUp5DpcwuAhN2nbuswRjCeI3i4f2e8EUa4w_PNHvlMk5vBTErMqNTA-28pyfz5OD1MyEQ7fH5vSnsLvRWvpYFTl-1J22krPTurN_TxPGBaEEyHSJHdK_5rqBE4kwJBwDWY-tPJrRJKKQaRF7AtvtYBhiRFlcU1eX5tWiSJvVKK5jwCLpd8nq58cx1xo5bWDspidNbAWDAh_xTQ6YFQcBX4gVDUPtse-tOBiXFjX-7O7EL3ykkWDlybRUgOw__HcemaneydY1fytJBcK_AUbB46pO0aNct11cP2-S92ie-HTwibymWuoU-YhLDEid4",
      transcode_status: "completed",
      transcribe_status: "completed",
      transcription: "Hello, John. Thank you so much for your response. Can you please explain have it better? Why do you love strawberries? Thank you.",
      transcription_data: [
        {
          transcript: "Hello, John. Thank you so much for your response. Can you please explain have it better? Why do you love strawberries? Thank you.",
          confidence: 0.90139115,
          words: [
            {
              word: "Hello,",
              start_time: 0.8,
              end_time: 1.1
            },
            {
              word: "John.",
              start_time: 1.1,
              end_time: 1.5
            },
            {
              word: "Thank",
              start_time: 1.5,
              end_time: 1.7
            },
            {
              word: "you",
              start_time: 1.7,
              end_time: 1.8
            },
            {
              word: "so",
              start_time: 1.8,
              end_time: 1.9
            },
            {
              word: "much",
              start_time: 1.9,
              end_time: 2.1
            },
            {
              word: "for",
              start_time: 2.1,
              end_time: 2.3
            },
            {
              word: "your",
              start_time: 2.3,
              end_time: 2.4
            },
            {
              word: "response.",
              start_time: 2.4,
              end_time: 3.1
            },
            {
              word: "Can",
              start_time: 3.9,
              end_time: 4
            },
            {
              word: "you",
              start_time: 4,
              end_time: 4.1
            },
            {
              word: "please",
              start_time: 4.1,
              end_time: 4.8
            },
            {
              word: "explain",
              start_time: 4.8,
              end_time: 5.3
            },
            {
              word: "have",
              start_time: 5.3,
              end_time: 5.5
            },
            {
              word: "it",
              start_time: 5.5,
              end_time: 5.6
            },
            {
              word: "better?",
              start_time: 5.6,
              end_time: 6.1
            },
            {
              word: "Why",
              start_time: 6.1,
              end_time: 6.3
            },
            {
              word: "do",
              start_time: 6.3,
              end_time: 6.4
            },
            {
              word: "you",
              start_time: 6.4,
              end_time: 6.5
            },
            {
              word: "love",
              start_time: 6.5,
              end_time: 6.7
            },
            {
              word: "strawberries?",
              start_time: 6.7,
              end_time: 7.5
            },
            {
              word: "Thank",
              start_time: 8,
              end_time: 8.2
            },
            {
              word: "you.",
              start_time: 8.2,
              end_time: 8.5
            }
          ]
        }
      ],
      type: "video"
    }
  },
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    await makeRequest(
      context.propsValue.organizationId,
      context.auth.access_token,
      import_pieces_common7.HttpMethod.PUT,
      `/forms/${context.propsValue.formId}/webhooks/${context.propsValue.tag}`,
      {
        url: context.webhookUrl,
        event_types: ["form_author_response"]
      }
    );
  },
  async onDisable(context) {
    await makeRequest(
      context.propsValue.organizationId,
      context.auth.access_token,
      import_pieces_common7.HttpMethod.DELETE,
      `/forms/${context.propsValue.formId}/webhooks/${context.propsValue.tag}`
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/videoask/src/lib/actions/add-tag-to-contact.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var addTagToContact = (0, import_pieces_framework8.createAction)({
  auth: videoaskAuth,
  name: "addTagToContact",
  displayName: "Add tag to contact",
  description: "Add a tag to a contact (respondent) in VideoAsk",
  audience: "both",
  aiMetadata: { description: "Attach an existing tag to a VideoAsk contact (respondent), identified by its contact ID and the tag ID, within a given organization. Use to label or segment a respondent for downstream filtering. Idempotent: re-adding the same tag leaves the contact in the same state.", idempotent: !0 },
  props: {
    organizationId: organizationIdDropdown,
    contactId: import_pieces_framework8.Property.ShortText({
      displayName: "Contact ID",
      description: "The contact (respondent) id to add the tag to",
      required: !0
    }),
    tagId: tagIdDropdown
  },
  async run(context) {
    let { organizationId, contactId, tagId } = context.propsValue, access_token = context.auth.access_token;
    return await makeRequest(
      organizationId,
      access_token,
      import_pieces_common8.HttpMethod.PATCH,
      `/contacts/${contactId}/tags/${tagId}`,
      { action: "add" }
    );
  }
});

// ../ap/packages/pieces/community/videoask/src/lib/actions/remove-tag-from-contact.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var removeTagFromContact = (0, import_pieces_framework9.createAction)({
  auth: videoaskAuth,
  name: "removeTagFromContact",
  displayName: "Remove tag from contact",
  description: "Remove a tag from a contact (respondent) in VideoAsk",
  audience: "both",
  aiMetadata: { description: "Detach a tag from a VideoAsk contact (respondent), identified by its contact ID and the tag ID, within a given organization. Use to unlabel or re-segment a respondent. Idempotent: removing a tag that is not present leaves the contact in the same state.", idempotent: !0 },
  props: {
    organizationId: organizationIdDropdown,
    contactId: import_pieces_framework9.Property.ShortText({
      displayName: "Contact ID",
      description: "The contact (respondent) id to remove the tag from",
      required: !0
    }),
    tagId: tagIdDropdown
  },
  async run(context) {
    let { organizationId, contactId, tagId } = context.propsValue, access_token = context.auth.access_token;
    return await makeRequest(
      organizationId,
      access_token,
      import_pieces_common9.HttpMethod.PATCH,
      `/contacts/${contactId}/tags/${tagId}`,
      { action: "remove" }
    );
  }
});

// ../ap/packages/pieces/community/videoask/src/lib/actions/search-form.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var searchForm = (0, import_pieces_framework10.createAction)({
  auth: videoaskAuth,
  name: "searchForm",
  displayName: "Search form by keyword(s)",
  description: "Search forms by keyword(s) within the organization",
  audience: "both",
  aiMetadata: { description: "Search an organization's VideoAsk forms by keyword matched against form titles, returning up to a configurable limit. Use to resolve a form name to its ID or discover available forms before referencing one. Read-only and idempotent. The search term is required.", idempotent: !0 },
  props: {
    organizationId: organizationIdDropdown,
    limit: import_pieces_framework10.Property.Number({
      displayName: "Limit",
      description: "Maximum number of results to return",
      required: !1,
      defaultValue: 100
    }),
    search: import_pieces_framework10.Property.ShortText({
      displayName: "Search term",
      description: "Keyword(s) to search for in form titles",
      required: !0
    })
  },
  async run(context) {
    let { organizationId, limit, search } = context.propsValue, access_token = context.auth.access_token, effectiveLimit = limit != null ? limit : 100, encodedSearch = encodeURIComponent(search);
    return await makeRequest(
      organizationId,
      access_token,
      import_pieces_common10.HttpMethod.GET,
      `/search/forms?limit=${effectiveLimit}&search=${encodedSearch}`
    );
  }
});

// ../ap/packages/pieces/community/videoask/src/lib/actions/update-contact.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var updateContact = (0, import_pieces_framework11.createAction)({
  auth: videoaskAuth,
  name: "updateContact",
  displayName: "Update contact",
  description: "Update an existing respondent (contact) in VideoAsk",
  audience: "both",
  aiMetadata: { description: "Update an existing VideoAsk respondent (contact), identified by respondent ID, within an organization; only the name, email, and phone fields you supply are changed and empty values are skipped. Use to correct or enrich a known contact. Idempotent: repeating with the same values yields the same record.", idempotent: !0 },
  props: {
    organizationId: organizationIdDropdown,
    respondentId: import_pieces_framework11.Property.ShortText({
      displayName: "Respondent ID",
      description: "The ID of the respondent to update",
      required: !0
    }),
    name: import_pieces_framework11.Property.ShortText({
      displayName: "Name",
      description: "The name of the contact",
      required: !1
    }),
    email: import_pieces_framework11.Property.ShortText({
      displayName: "Email",
      description: "The email of the contact",
      required: !1
    }),
    phone_number: import_pieces_framework11.Property.ShortText({
      displayName: "Phone",
      description: "The phone number of the contact",
      required: !1
    })
  },
  async run(context) {
    let { organizationId, respondentId, name, email, phone_number } = context.propsValue, access_token = context.auth.access_token, body = {};
    name != null && name !== "" && (body.name = name), email != null && email !== "" && (body.email = email), phone_number != null && phone_number !== "" && (body.phone_number = phone_number);
    let path = `/respondents/${respondentId}`;
    return await makeRequest(organizationId, access_token, import_pieces_common11.HttpMethod.PATCH, path, body);
  }
});

// ../ap/packages/pieces/community/videoask/src/index.ts
var videoask = (0, import_pieces_framework12.createPiece)({
  displayName: "VideoAsk",
  auth: videoaskAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/videoask.png",
  authors: ["sanket-a11y"],
  categories: [import_pieces_framework13.PieceCategory.FORMS_AND_SURVEYS],
  actions: [
    addTagToContact,
    createContact,
    removeTagFromContact,
    searchForm,
    updateContact,
    (0, import_pieces_common12.createCustomApiCallAction)({
      auth: videoaskAuth,
      baseUrl: () => "https://api.videoask.com",
      authMapping: async (auth, propsValue) => ({
        Authorization: `Bearer ${auth.access_token}`,
        "Content-Type": "application/json",
        "organization-id": propsValue.organizationId
      }),
      extraProps: {
        organizationId: organizationIdDropdown
      }
    })
  ],
  triggers: [
    formContactMessage,
    formTranscribed,
    newFormResponse,
    newFormAuthorResponse
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  videoask
});
