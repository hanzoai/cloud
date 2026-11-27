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
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key != "symbol" ? key + "" : key, value);

// ../ap/packages/pieces/community/vbout/src/index.ts
var index_exports = {};
__export(index_exports, {
  vbout: () => vbout
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_framework14 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/vbout/src/lib/actions/add-contact.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/vbout/src/lib/auth.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/vbout/src/lib/common/index.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/vbout/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
function emptyValueFilter(accessor) {
  return (key) => {
    let val = accessor(key);
    return val != null && (typeof val != "string" || val.length > 0);
  };
}
function prepareQuery(request) {
  let params = {};
  return request && Object.keys(request).filter(emptyValueFilter((k) => request[k])).forEach((k) => {
    params[k] = request[k].toString();
  }), params;
}
var VboutClient = class {
  constructor(apiKey) {
    __publicField(this, "apiKey", apiKey);
  }
  async makeRequest(method, url, query, body) {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: vboutCommon.baseUrl + url,
      queryParams: __spreadValues({ key: this.apiKey }, query),
      body
    })).body;
  }
  async validateAuth() {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, "/app/me");
  }
  async listEmailLists() {
    return (await this.makeRequest(import_pieces_common.HttpMethod.GET, "/emailmarketing/getlists")).response.data;
  }
  async getContactByEmail(email, listId) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/emailmarketing/getcontactbyemail",
      prepareQuery({ email, listid: listId })
    );
  }
  async getEmailList(listId) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/emailmarketing/getlist",
      prepareQuery({ id: listId })
    );
  }
  async createEmailList(request) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      "/emailMarketing/AddList",
      void 0,
      request
    );
  }
  async addContact(request) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      "/emailMarketing/AddContact",
      void 0,
      request
    );
  }
  async updateContact(request) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      "/emailMarketing/EditContact",
      void 0,
      request
    );
  }
  async addTagToContact(request) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      "/emailMarketing/AddTag",
      void 0,
      request
    );
  }
  async removeTagFromContact(email, tagname) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.DELETE,
      "/emailMarketing/RemoveTag",
      prepareQuery({
        email,
        tagname
      })
    );
  }
  async addCampaign(request) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      "/emailMarketing/AddCampaign",
      void 0,
      request
    );
  }
  async listSocialMediaChannels() {
    return (await this.makeRequest(import_pieces_common.HttpMethod.GET, "/socialMedia/Channels")).response.data;
  }
  async createSocialMediaPost(request) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      "/socialMedia/AddPost",
      void 0,
      request
    );
  }
};

// ../ap/packages/pieces/community/vbout/src/lib/common/index.ts
function makeClient(apiKey) {
  return new VboutClient(apiKey);
}
var vboutCommon = {
  baseUrl: "https://api.vbout.com/1",
  listid: (required = !0) => import_pieces_framework.Property.Dropdown({
    auth: vboutAuth,
    displayName: "List ID",
    required,
    refreshers: [],
    options: async ({ auth }) => auth ? {
      disabled: !1,
      options: (await makeClient(auth.secret_text).listEmailLists()).lists.items.map((list) => ({
        label: list.name,
        value: list.id
      }))
    } : {
      disabled: !0,
      placeholder: "Connect your account first",
      options: []
    }
  }),
  listFields: import_pieces_framework.Property.DynamicProperties({
    auth: vboutAuth,
    displayName: "Fields",
    required: !0,
    refreshers: ["listid"],
    props: async ({ auth, listid }) => {
      if (!auth || !listid)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please connect your account and select Email List."
        };
      let fields = {}, contactListFields = (await makeClient(auth.secret_text).getEmailList(
        listid
      )).response.data.list.fields;
      return Object.keys(contactListFields).forEach((key) => {
        fields[key] = import_pieces_framework.Property.ShortText({
          displayName: contactListFields[key],
          required: !1
        });
      }), fields;
    }
  }),
  contactStatus: (required = !0) => import_pieces_framework.Property.StaticDropdown({
    displayName: "Contact Status",
    required,
    options: {
      disabled: !1,
      options: [
        {
          label: "Unconfirmed",
          value: "0" /* UNCONFIRMED */
        },
        {
          label: "Active",
          value: "1" /* ACTIVE */
        },
        {
          label: "Unsubscribe",
          value: "2" /* UNSUBSCRIBE */
        },
        {
          label: "Bounced Email",
          value: "3" /* BOUNCED_EMAIL */
        }
      ]
    }
  }),
  socialMediaChannel: import_pieces_framework.Property.StaticDropdown({
    displayName: "Social Media Channel",
    required: !0,
    options: {
      disabled: !1,
      options: [
        {
          label: "Twitter",
          value: "twitter" /* TWITTER */
        },
        {
          label: "LinkedIn",
          value: "linkedin" /* LINKEDIN */
        },
        {
          label: "Facebook",
          value: "facebook" /* FACEBOOK */
        }
      ]
    }
  }),
  socialMediaProfile: import_pieces_framework.Property.Dropdown({
    auth: vboutAuth,
    displayName: "Social Media Account",
    required: !0,
    refreshers: ["channel"],
    options: async ({ auth, channel }) => {
      if (!auth || !channel)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please connect your account and select social media channel."
        };
      let client = makeClient(auth.secret_text), { channels } = await client.listSocialMediaChannels(), options = [];
      switch (channel) {
        case "twitter" /* TWITTER */: {
          options = [
            ...options,
            ...mapSocialMediaProfile(channels.Twitter.profiles)
          ];
          break;
        }
        case "facebook" /* FACEBOOK */: {
          options = [
            ...options,
            ...mapSocialMediaProfile(channels.Facebook.pages)
          ];
          break;
        }
        case "linkedin" /* LINKEDIN */: {
          options = [
            ...options,
            ...mapSocialMediaProfile(channels.Linkedin.companies),
            ...mapSocialMediaProfile(channels.Linkedin.profiles)
          ];
          break;
        }
      }
      return {
        disabled: !1,
        options
      };
    }
  })
};
function mapSocialMediaProfile(profiles) {
  return profiles.map((profile) => ({
    label: profile.name,
    value: profile.id
  }));
}

// ../ap/packages/pieces/community/vbout/src/lib/auth.ts
var markdown = `
To obtain your API key, follow these steps:

1.Go to **settings** by clicking your profile-pic (top-right).

2.Navigate to **API Integrations** section.

3.Under **API USER KEY** ,copy API key.

`, vboutAuth = import_pieces_framework2.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: markdown,
  async validate({ auth }) {
    let client = makeClient(auth);
    try {
      return await client.validateAuth(), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API key."
      };
    }
  }
});

// ../ap/packages/pieces/community/vbout/src/lib/actions/add-contact.ts
var addContactAction = (0, import_pieces_framework3.createAction)({
  auth: vboutAuth,
  name: "vbout_add_contact",
  classification: "WRITE",
  displayName: "Add Contact to List",
  description: "Adds a contact to a given email list.",
  audience: "both",
  aiMetadata: {
    description: "Adds a new contact (by email) to a specific VBOUT email list, with an initial subscription status and optional custom field values. Use to subscribe or import someone into a list. Requires the target list ID and email; not idempotent, as each call adds the contact again.",
    idempotent: !1
  },
  props: {
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email Address",
      required: !0
    }),
    listid: vboutCommon.listid(!0),
    status: vboutCommon.contactStatus(!0),
    ipaddress: import_pieces_framework3.Property.ShortText({
      displayName: "IP Address",
      required: !1
    }),
    fields: vboutCommon.listFields
  },
  async run(context) {
    return await makeClient(context.auth.secret_text).addContact(context.propsValue);
  }
});

// ../ap/packages/pieces/community/vbout/src/lib/actions/add-tag-to-contact.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var addTagToContactAction = (0, import_pieces_framework4.createAction)({
  auth: vboutAuth,
  name: "vbout_add_tag",
  classification: "WRITE",
  displayName: "Add Tag to Contact",
  description: "Adds the tag to the contact.",
  audience: "both",
  aiMetadata: {
    description: "Adds one or more tags to an existing VBOUT contact identified by email. Use to label or segment a contact. Accepts a list of tag names; not idempotent, as it appends tags on each call.",
    idempotent: !1
  },
  props: {
    email: import_pieces_framework4.Property.ShortText({
      displayName: "Email Address",
      required: !0
    }),
    tagname: import_pieces_framework4.Property.Array({
      displayName: "Tag Name",
      required: !0
    })
  },
  async run(context) {
    let { email, tagname } = context.propsValue;
    return await makeClient(context.auth.secret_text).addTagToContact({
      email,
      tagname
    });
  }
});

// ../ap/packages/pieces/community/vbout/src/lib/actions/create-campaign.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var createEmailMarketingCampaignAction = (0, import_pieces_framework5.createAction)({
  auth: vboutAuth,
  name: "vbout_add_email_marketing_campaign",
  classification: "WRITE",
  displayName: "Create Email Marketing Campaign",
  description: "Creates a new email campaign for specific list.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new VBOUT email marketing campaign targeting one or more email lists, either standard or automated type. Use to set up an email send to existing recipients. Requires recipient list(s), name, subject, sender details, and message body; not idempotent, as each call creates a separate campaign.",
    idempotent: !1
  },
  props: {
    lists: import_pieces_framework5.Property.MultiSelectDropdown({
      auth: vboutAuth,
      displayName: "Campaign Recipients List",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await makeClient(auth.secret_text).listEmailLists()).lists.items.map((list) => ({
          label: list.name,
          value: list.id
        }))
      } : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    name: import_pieces_framework5.Property.ShortText({
      displayName: "Campaign Name",
      required: !0
    }),
    subject: import_pieces_framework5.Property.ShortText({
      displayName: "Campaign Subject",
      required: !0
    }),
    type: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Campaign Type",
      required: !0,
      defaultValue: "standard",
      options: {
        disabled: !1,
        options: [
          {
            label: "Standard",
            value: "standard"
          },
          {
            label: "Automated",
            value: "automated"
          }
        ]
      }
    }),
    fromemail: import_pieces_framework5.Property.ShortText({
      displayName: "From Email",
      required: !0
    }),
    from_name: import_pieces_framework5.Property.ShortText({
      displayName: "From Name",
      required: !0
    }),
    reply_to: import_pieces_framework5.Property.ShortText({
      displayName: "Reply To",
      required: !0
    }),
    body: import_pieces_framework5.Property.LongText({
      displayName: "Message Body",
      required: !0
    })
  },
  async run(context) {
    let { lists, name, from_name, fromemail, reply_to, subject, body, type } = context.propsValue;
    return await makeClient(context.auth.secret_text).addCampaign({
      lists: lists.join(","),
      name,
      from_name,
      fromemail,
      reply_to,
      subject,
      body,
      type
    });
  }
});

// ../ap/packages/pieces/community/vbout/src/lib/actions/create-email-list.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var createEmailListAction = (0, import_pieces_framework6.createAction)({
  auth: vboutAuth,
  name: "vbout_create_email_list",
  classification: "WRITE",
  displayName: "Create Email List",
  description: "Creates a new email list.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new VBOUT email list with optional subscription settings (sender, reply-to, confirmation and notification emails/messages). Use to set up a new audience before adding contacts or sending campaigns. Only the list name is required; not idempotent, as each call creates a new list.",
    idempotent: !1
  },
  props: {
    name: import_pieces_framework6.Property.ShortText({
      required: !0,
      displayName: "Name",
      description: "The name of the list."
    }),
    email_subject: import_pieces_framework6.Property.LongText({
      required: !1,
      displayName: "Email Subject",
      description: "The default subscription subject."
    }),
    reply_to: import_pieces_framework6.Property.ShortText({
      required: !1,
      displayName: "Reply To",
      description: "The Reply to email of the list."
    }),
    fromemail: import_pieces_framework6.Property.ShortText({
      required: !1,
      displayName: "From Email",
      description: "The From email of the list."
    }),
    from_name: import_pieces_framework6.Property.ShortText({
      required: !1,
      displayName: "From Name",
      description: "The From name of the list."
    }),
    notify_email: import_pieces_framework6.Property.ShortText({
      required: !1,
      displayName: "Notify Email",
      description: "Notification Email."
    }),
    success_email: import_pieces_framework6.Property.ShortText({
      required: !1,
      displayName: "Success Email",
      description: "Subscription Success Email."
    }),
    success_message: import_pieces_framework6.Property.ShortText({
      required: !1,
      displayName: "Success Message",
      description: "Subscription Success Message."
    }),
    error_message: import_pieces_framework6.Property.ShortText({
      required: !1,
      displayName: "Error Message",
      description: "Subscription Error Message."
    }),
    confirmation_email: import_pieces_framework6.Property.ShortText({
      required: !1,
      displayName: "Confirmation Email",
      description: "Confirmation Email Message."
    }),
    confirmation_message: import_pieces_framework6.Property.ShortText({
      required: !1,
      displayName: "Confirmation Message"
    })
  },
  async run({ auth, propsValue }) {
    return await makeClient(auth.secret_text).createEmailList(propsValue);
  }
});

// ../ap/packages/pieces/community/vbout/src/lib/actions/create-social-media-message.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var createSocialMediaMessageAction = (0, import_pieces_framework7.createAction)({
  auth: vboutAuth,
  name: "vbout_create_social_media_message",
  classification: "WRITE",
  displayName: "Create Social Media Message",
  description: "Post a message to one of your social media channel.",
  audience: "both",
  aiMetadata: {
    description: "Publishes a message to one connected VBOUT social media account on a chosen channel (Twitter, LinkedIn, or Facebook). Use to post content to social media. Requires the message text, the channel, and the specific account/profile ID on that channel; not idempotent, as each call creates a new post.",
    idempotent: !1
  },
  props: {
    message: import_pieces_framework7.Property.LongText({
      displayName: "Message",
      required: !0
    }),
    channel: vboutCommon.socialMediaChannel,
    channelid: vboutCommon.socialMediaProfile
  },
  async run(context) {
    return await makeClient(context.auth.secret_text).createSocialMediaPost(context.propsValue);
  }
});

// ../ap/packages/pieces/community/vbout/src/lib/actions/get-contact-by-email.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var getContactByEmailAction = (0, import_pieces_framework8.createAction)({
  auth: vboutAuth,
  name: "vbout_get_contact_by_email",
  classification: "READ",
  displayName: "Get Contact by Email",
  description: "Retrieves the contact by email.",
  audience: "both",
  aiMetadata: {
    description: "Looks up a VBOUT contact by email address, optionally scoped to a specific email list. Use to fetch a contact record or resolve its ID before updating, tagging, or unsubscribing. The email is required; this is a read-only lookup and is idempotent.",
    idempotent: !0
  },
  props: {
    listid: vboutCommon.listid(!1),
    email: import_pieces_framework8.Property.ShortText({
      displayName: "Email",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let client = makeClient(auth.secret_text), { listid, email } = propsValue;
    return await client.getContactByEmail(email, listid);
  }
});

// ../ap/packages/pieces/community/vbout/src/lib/actions/get-email-list.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var getEmailListAction = (0, import_pieces_framework9.createAction)({
  auth: vboutAuth,
  name: "vbout_get_email_list",
  classification: "READ",
  displayName: "Get List Details with Custom Fields",
  description: "Retrieves specific list details with custom fields.",
  audience: "both",
  aiMetadata: {
    description: "Retrieves details of a specific VBOUT email list, including its configured custom fields. Use to inspect a list or discover its custom field names before adding or updating contacts. Requires the list ID; this is a read-only lookup and is idempotent.",
    idempotent: !0
  },
  props: {
    listid: vboutCommon.listid(!0)
  },
  async run({ auth, propsValue }) {
    let client = makeClient(auth.secret_text), listId = propsValue.listid;
    return await client.getEmailList(listId);
  }
});

// ../ap/packages/pieces/community/vbout/src/lib/actions/remove-tag-from-contact.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var removeTagFromContactAction = (0, import_pieces_framework10.createAction)({
  auth: vboutAuth,
  name: "vbout_remove_tag",
  classification: "WRITE",
  displayName: "Remove Tags from Contact",
  description: "Removes tags from an existing contact.",
  audience: "both",
  aiMetadata: {
    description: "Removes one or more tags from an existing VBOUT contact identified by email. Use to unlabel or re-segment a contact. Tag names are passed as a comma-separated string; idempotent, since removing already-absent tags yields the same end state.",
    idempotent: !0
  },
  props: {
    email: import_pieces_framework10.Property.ShortText({
      displayName: "Email Address",
      required: !0
    }),
    tagname: import_pieces_framework10.Property.ShortText({
      displayName: "Tag Name",
      required: !0,
      description: "use comma for multiple tag e.g. **tag1,tag2**."
    })
  },
  async run(context) {
    let { email, tagname } = context.propsValue;
    return await makeClient(context.auth.secret_text).removeTagFromContact(email, tagname);
  }
});

// ../ap/packages/pieces/community/vbout/src/lib/actions/unsubscribe-contact.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var unsubscribeContactAction = (0, import_pieces_framework11.createAction)({
  auth: vboutAuth,
  name: "vbout_unsubscribe_contact",
  classification: "DESTRUCTIVE",
  displayName: "Unsubscribe Contact",
  description: "Unsubscribes an existing contact in a given email list.",
  audience: "both",
  aiMetadata: {
    description: "Sets an existing VBOUT contact to unsubscribed status within a given email list, looking the contact up by email first. Use to opt a contact out of a list. Requires the contact email and list ID; idempotent, since re-running leaves the contact in the same unsubscribed state.",
    idempotent: !0
  },
  props: {
    email: import_pieces_framework11.Property.ShortText({
      displayName: "Contact Email",
      required: !0,
      description: "Contact email for update."
    }),
    listid: vboutCommon.listid(!0)
  },
  async run(context) {
    let client = makeClient(context.auth.secret_text), { email, listid } = context.propsValue, res = await client.getContactByEmail(
      email,
      listid
    ), contact = res.response.data.contact;
    if ("errorCode" in contact)
      return res;
    {
      let contactId = contact[0].id;
      return await client.updateContact({
        id: contactId,
        status: "2" /* UNSUBSCRIBE */
      });
    }
  }
});

// ../ap/packages/pieces/community/vbout/src/lib/actions/update-contact.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var updateContactAction = (0, import_pieces_framework12.createAction)({
  auth: vboutAuth,
  name: "vbout_update_contact",
  classification: "WRITE",
  displayName: "Update Contact",
  description: "Updates a contact in a given email list.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing VBOUT contact (found by email) with a new status, IP address, and custom field values for a given list. Use to modify an already-subscribed contact rather than add a new one. Requires the contact email and list ID; idempotent, since re-applying the same values yields the same record state.",
    idempotent: !0
  },
  props: {
    email: import_pieces_framework12.Property.ShortText({
      displayName: "Contact Email",
      required: !0,
      description: "Contact email for update."
    }),
    listid: vboutCommon.listid(!0),
    status: vboutCommon.contactStatus(!1),
    ipaddress: import_pieces_framework12.Property.ShortText({
      displayName: "IP Address",
      required: !1
    }),
    fields: vboutCommon.listFields
  },
  async run(context) {
    let client = makeClient(context.auth.secret_text), { email } = context.propsValue, res = await client.getContactByEmail(email), contact = res.response.data.contact;
    if ("errorCode" in contact)
      return res;
    {
      let contactId = contact[0].id;
      return await client.updateContact(__spreadValues({
        id: contactId
      }, context.propsValue));
    }
  }
});

// ../ap/packages/pieces/community/vbout/src/index.ts
var vbout = (0, import_pieces_framework13.createPiece)({
  displayName: "VBOUT",
  description: "Marketing automation platform for agencies",
  auth: vboutAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/vbout.png",
  authors: ["kishanprmr", "abuaboud"],
  categories: [import_pieces_framework14.PieceCategory.MARKETING],
  actions: [
    addContactAction,
    addTagToContactAction,
    createEmailListAction,
    createEmailMarketingCampaignAction,
    createSocialMediaMessageAction,
    getContactByEmailAction,
    getEmailListAction,
    removeTagFromContactAction,
    unsubscribeContactAction,
    updateContactAction
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  vbout
});
