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

// ../ap/packages/pieces/community/aiprise/src/index.ts
var index_exports = {};
__export(index_exports, {
  aiprise: () => aiprise2
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework18 = require("@activepieces/pieces-framework"), import_pieces_common18 = require("@activepieces/pieces-common"), import_pieces_framework19 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/aiprise/src/lib/actions/run-user-verification-profile.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/aiprise/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), PRODUCTION_BASE_URL = "https://api.aiprise.com/api/v1", SANDBOX_BASE_URL = "https://api-sandbox.aiprise.com/api/v1";
function getBaseUrl(environment) {
  return environment === "sandbox" ? SANDBOX_BASE_URL : PRODUCTION_BASE_URL;
}
async function makeRequest({
  auth,
  method,
  path,
  body
}) {
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${getBaseUrl(auth.environment)}${path}`,
    headers: {
      "X-API-KEY": auth.secret_text,
      "Content-Type": "application/json"
    },
    body
  })).body;
}
var aiprise = {
  PRODUCTION_BASE_URL,
  SANDBOX_BASE_URL,
  getBaseUrl,
  makeRequest
};

// ../ap/packages/pieces/community/aiprise/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), aipriseAuth = import_pieces_framework.PieceAuth.CustomAuth({
  displayName: "API Key",
  description: `To get your AiPrise API key:
1. Log in to your [AiPrise dashboard](https://app.aiprise.com)
2. Go to **Settings > API Keys**
3. Copy your API key and paste it here
`,
  required: !0,
  props: {
    secret_text: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Key",
      description: "Your AiPrise API key",
      required: !0
    }),
    environment: import_pieces_framework.Property.StaticDropdown({
      displayName: "Environment",
      description: "Select the AiPrise environment to use",
      required: !0,
      options: {
        options: [
          { label: "Production", value: "production" },
          { label: "Sandbox", value: "sandbox" }
        ]
      }
    })
  }
});

// ../ap/packages/pieces/community/aiprise/src/lib/actions/run-user-verification-profile.ts
var runUserVerificationProfileAction = (0, import_pieces_framework2.createAction)({
  auth: aipriseAuth,
  name: "run_user_verification_profile",
  classification: "WRITE",
  displayName: "Start Identity Verification for profile",
  description: "Kicks off a new identity check for a person. AiPrise will run the checks defined in your chosen template (e.g. ID document scan, liveness, address).",
  audience: "both",
  aiMetadata: {
    description: "Starts a server-side identity (KYC) verification for an existing user profile by running the checks bundled in a given AiPrise template (ID scan, liveness, address, etc.). Use when you want to programmatically begin verifying a person you have already created a profile for, rather than sending them a hosted link. Requires a template ID and your user_profile_id. Not idempotent \u2014 each call launches a new verification session.",
    idempotent: !1
  },
  props: {
    template_id: import_pieces_framework2.Property.ShortText({
      displayName: "Verification Template",
      description: "Which set of checks to run. To find this: log in to AiPrise \u2192 go to **Templates** \u2192 open the template you want \u2192 copy the **Template ID** shown at the top of the page.",
      required: !0
    }),
    user_profile_id: import_pieces_framework2.Property.ShortText({
      displayName: "User Profile ID",
      description: "Your own identifier for this person \u2014 for example their user ID, email address, or order number. AiPrise stores this alongside the verification so you can match results back to your records.",
      required: !0
    })
  },
  async run(context) {
    let {
      template_id,
      user_profile_id
    } = context.propsValue, body = { template_id, user_profile_id };
    return aiprise.makeRequest({
      auth: context.auth.props,
      method: import_pieces_common2.HttpMethod.POST,
      path: "/verify/run_verification_for_user_profile_id",
      body
    });
  }
});

// ../ap/packages/pieces/community/aiprise/src/lib/actions/get-verification-url.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var getVerificationUrlAction = (0, import_pieces_framework3.createAction)({
  auth: aipriseAuth,
  name: "get_verification_url",
  classification: "WRITE",
  displayName: "Get Identity Verification Link",
  description: "Generates a secure link you can send to a person so they can complete their identity verification on an AiPrise-hosted page \u2014 no extra development needed.",
  audience: "both",
  aiMetadata: {
    description: "Creates a unique hosted-verification URL for a given template that you can send to a person so they complete their identity (KYC) check on an AiPrise-hosted page. Use this for the no-code/self-service flow instead of collecting data yourself. Requires a template ID and a redirect URL the person returns to afterward. Not idempotent \u2014 each call mints a new verification link/session.",
    idempotent: !1
  },
  props: {
    template_id: import_pieces_framework3.Property.ShortText({
      displayName: "Verification Template",
      description: "Which set of checks the person will go through. To find this: log in to AiPrise \u2192 go to **Templates** \u2192 open the template you want \u2192 copy the **Template ID** shown at the top of the page.",
      required: !0
    }),
    redirect_uri: import_pieces_framework3.Property.ShortText({
      displayName: "Redirect URL",
      description: 'The page the person will be sent to after they finish the verification on the AiPrise-hosted form (e.g. a "Thank you" or confirmation page on your website).',
      required: !0
    })
  },
  async run(context) {
    let { template_id, redirect_uri } = context.propsValue;
    return await aiprise.makeRequest({
      auth: context.auth.props,
      method: import_pieces_common3.HttpMethod.POST,
      path: "/verify/get_user_verification_url",
      body: {
        template_id,
        redirect_uri
      }
    });
  }
});

// ../ap/packages/pieces/community/aiprise/src/lib/actions/get-verification-result.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var getVerificationResultAction = (0, import_pieces_framework4.createAction)({
  auth: aipriseAuth,
  name: "get_verification_result",
  classification: "READ",
  displayName: "Get Identity Verification Result",
  description: "Fetches the current status and outcome of a person's identity check \u2014 whether it is still in progress, approved, or declined.",
  audience: "both",
  aiMetadata: {
    description: "Looks up the current status and decision (in progress, approved, or declined) of a single identity (KYC) verification by its session ID. Use this to poll or read the outcome of a previously started user verification. Requires the verification session ID. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    session_id: import_pieces_framework4.Property.ShortText({
      displayName: "Verification Session ID",
      description: "The ID of the specific verification to look up. You can get this from the output of the **Start Identity Verification** action, or from the **session_id** field in any webhook payload from AiPrise.",
      required: !0
    })
  },
  async run(context) {
    let { session_id } = context.propsValue;
    return await aiprise.makeRequest({
      auth: context.auth.props,
      method: import_pieces_common4.HttpMethod.GET,
      path: `/verify/get_user_verification_result/${session_id}`
    });
  }
});

// ../ap/packages/pieces/community/aiprise/src/lib/actions/get-user-input-data.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var getUserInputDataAction = (0, import_pieces_framework5.createAction)({
  auth: aipriseAuth,
  name: "get_user_input_data",
  classification: "READ",
  displayName: "Get Person's Submitted Verification Data",
  description: "Returns the information the person entered and the documents they uploaded during their identity check (e.g. name, date of birth, ID document details).",
  audience: "both",
  aiMetadata: {
    description: "Retrieves the raw data a person submitted during an identity verification \u2014 the details they typed and the documents they uploaded \u2014 for a single verification session. Use this when you need the person's submitted input itself rather than the pass/fail decision (see Get Identity Verification Result for the outcome). Requires the verification session ID. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    session_id: import_pieces_framework5.Property.ShortText({
      displayName: "Verification Session ID",
      description: "The ID of the verification to retrieve data from. You can get this from the output of the **Start Identity Verification** action, or from the **session_id** field in any webhook payload from AiPrise.",
      required: !0
    })
  },
  async run(context) {
    let { session_id } = context.propsValue;
    return await aiprise.makeRequest({
      auth: context.auth.props,
      method: import_pieces_common5.HttpMethod.GET,
      path: `/verify/get_user_input_data_from_request/${session_id}`
    });
  }
});

// ../ap/packages/pieces/community/aiprise/src/lib/actions/update-verification-result.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var updateVerificationResultAction = (0, import_pieces_framework6.createAction)({
  auth: aipriseAuth,
  name: "update_verification_result",
  classification: "DESTRUCTIVE",
  displayName: "Override Identity Verification Decision",
  description: "Manually changes the outcome of an identity check \u2014 useful when a human reviewer needs to approve or decline a case that AiPrise flagged for manual review.",
  audience: "both",
  aiMetadata: {
    description: "Manually overrides the decision on an existing identity verification, setting it to Approved, Declined, Review (back to manual review), or Deactivated. Use this to implement a human-review/escalation step on a case AiPrise flagged. Requires the verification session ID and the target decision. Idempotent \u2014 it sets the decision to an explicit value, so repeating the same call leaves the case in the same state.",
    idempotent: !0
  },
  props: {
    verification_session_id: import_pieces_framework6.Property.ShortText({
      displayName: "Verification Session ID",
      description: "The ID of the verification whose decision you want to change. You can get this from the output of the **Start Identity Verification** action, or from the **session_id** field in any webhook payload from AiPrise.",
      required: !0
    }),
    result: import_pieces_framework6.Property.StaticDropdown({
      displayName: "New Decision",
      description: "The outcome you want to set. Choose **Approved** to pass the person, **Declined** to reject them, or **Pending** to put the case back into manual review.",
      required: !0,
      options: {
        options: [
          { label: "Approved \u2014 pass the person", value: "APPROVED" },
          { label: "Declined \u2014 reject the person", value: "DECLINED" },
          { label: "Review \u2014 put in manual review", value: "REVIEW" },
          { label: "Deactivated \u2014 deactivate the profile", value: "DEACTIVATED" }
        ]
      }
    })
  },
  async run(context) {
    let { verification_session_id, result } = context.propsValue;
    return await aiprise.makeRequest({
      auth: context.auth.props,
      method: import_pieces_common6.HttpMethod.POST,
      path: "/verify/update_user_verification_result",
      body: { verification_session_id, result }
    });
  }
});

// ../ap/packages/pieces/community/aiprise/src/lib/actions/run-business-verification.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var runBusinessVerificationAction = (0, import_pieces_framework7.createAction)({
  auth: aipriseAuth,
  name: "run_business_verification",
  classification: "WRITE",
  displayName: "Start Business Verification",
  description: "Kicks off a company background check. AiPrise will run the checks defined in your template \u2014 which can include company registry lookup, ownership structure (UBO), sanctions screening, and adverse media.",
  audience: "both",
  aiMetadata: {
    description: "Starts a business (KYB) verification on an existing business profile by running the checks bundled in a given template (registry lookup, UBO/ownership, sanctions screening, adverse media). Use when you want to begin verifying a company you have already created or looked up a profile for. Requires a template ID and the business_profile_id. Not idempotent \u2014 each call launches a new verification session.",
    idempotent: !1
  },
  props: {
    template_id: import_pieces_framework7.Property.ShortText({
      displayName: "Verification Template",
      description: "Which set of checks to run on the business. To find this: log in to AiPrise \u2192 go to **Templates** \u2192 open the template you want \u2192 copy the **Template ID** shown at the top of the page.",
      required: !0
    }),
    business_profile_id: import_pieces_framework7.Property.ShortText({
      displayName: "Business Profile ID",
      description: " The ID of the business profile you want to verify. This is returned when you create a business profile, or you can look it up using the List Business Profiles action.",
      required: !0
    })
  },
  async run(context) {
    let { template_id, business_profile_id } = context.propsValue;
    return await aiprise.makeRequest({
      auth: context.auth.props,
      method: import_pieces_common7.HttpMethod.POST,
      path: "/verify/run_verification_for_business_profile_id",
      body: { template_id, business_profile_id }
    });
  }
});

// ../ap/packages/pieces/community/aiprise/src/lib/actions/get-business-result.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var getBusinessResultAction = (0, import_pieces_framework8.createAction)({
  auth: aipriseAuth,
  name: "get_business_verification_result",
  classification: "READ",
  displayName: "Get Business Verification Result",
  description: "Fetches the current status and outcome of a company's background check \u2014 whether it is still running, approved, or declined.",
  audience: "both",
  aiMetadata: {
    description: "Looks up the current status and decision (running, approved, or declined) of a single business (KYB) verification by its session ID. Use this to poll or read the outcome of a previously started business verification. Requires the verification session ID. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    session_id: import_pieces_framework8.Property.ShortText({
      displayName: "Verification Session ID",
      description: "The ID of the business verification to look up. You can get this from the output of the **Start Business Verification** action, or from the **session_id** field in any webhook payload from AiPrise.",
      required: !0
    })
  },
  async run(context) {
    let { session_id } = context.propsValue;
    return await aiprise.makeRequest({
      auth: context.auth.props,
      method: import_pieces_common8.HttpMethod.GET,
      path: `/verify/get_business_verification_result/${session_id}`
    });
  }
});

// ../ap/packages/pieces/community/aiprise/src/lib/actions/get-business-input-data.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var getBusinessInputDataAction = (0, import_pieces_framework9.createAction)({
  auth: aipriseAuth,
  name: "get_business_input_data",
  classification: "READ",
  displayName: "Get Business's Submitted Verification Data",
  description: "Returns the company information provided during a business verification \u2014 such as company name, registration number, directors, and ownership details.",
  audience: "both",
  aiMetadata: {
    description: "Retrieves the company data submitted for a single business verification session \u2014 name, registration number, directors, ownership, etc. Use this when you need the business's submitted input itself rather than the pass/fail decision (see Get Business Verification Result for the outcome). Requires the verification session ID. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    session_id: import_pieces_framework9.Property.ShortText({
      displayName: "Verification Session ID",
      description: "The ID of the business verification to retrieve data from. You can get this from the output of the **Start Business Verification** action, or from the **session_id** field in any webhook payload from AiPrise.",
      required: !0
    })
  },
  async run(context) {
    let { session_id } = context.propsValue;
    return await aiprise.makeRequest({
      auth: context.auth.props,
      method: import_pieces_common9.HttpMethod.GET,
      path: `/verify/get_business_data_from_request/${session_id}`
    });
  }
});

// ../ap/packages/pieces/community/aiprise/src/lib/actions/run-document-check.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var runDocumentCheckAction = (0, import_pieces_framework10.createAction)({
  auth: aipriseAuth,
  name: "run_document_check",
  classification: "WRITE",
  displayName: "Run Business Document Check",
  description: "Runs a verification check on a document belonging to an existing business profile. AiPrise will process the uploaded file against the checks defined in the chosen template.",
  audience: "both",
  aiMetadata: {
    description: "Runs a verification check on a single previously-uploaded document attached to a business profile, processing the file against a chosen template. Use when you need to validate a specific business document (rather than running a full business verification). Requires the business_profile_id, a template ID, and the file UUID of the already-uploaded document. Not idempotent \u2014 each call launches a new document check.",
    idempotent: !1
  },
  props: {
    business_profile_id: import_pieces_framework10.Property.ShortText({
      displayName: "Business Profile ID",
      description: "The ID of the business profile the document belongs to. You can find this in AiPrise \u2192 **Businesses** \u2192 open the business \u2192 copy the **Profile ID**.",
      required: !0
    }),
    template_id: import_pieces_framework10.Property.ShortText({
      displayName: "Verification Template",
      description: "Which set of checks to run. To find this: log in to AiPrise \u2192 go to **Templates** \u2192 open the template you want \u2192 copy the **Template ID** shown at the top of the page.",
      required: !0
    }),
    file_uuid: import_pieces_framework10.Property.ShortText({
      displayName: "File UUID",
      description: "The UUID of the document file that was previously uploaded to AiPrise.",
      required: !0
    }),
    document_input_title: import_pieces_framework10.Property.ShortText({
      displayName: "Document Input Title",
      description: "An optional unique identifier for this document input. This value is included in webhook payloads and API responses so you can distinguish between multiple documents.",
      required: !1
    }),
    client_reference_id: import_pieces_framework10.Property.ShortText({
      displayName: "Client Reference ID",
      description: "Your own identifier to associate with this request \u2014 for example an order number or internal case ID.",
      required: !1
    }),
    client_reference_data: import_pieces_framework10.Property.Json({
      displayName: "Client Reference Data",
      description: "Any additional JSON data you want to store alongside this request.",
      required: !1
    })
  },
  async run(context) {
    let {
      business_profile_id,
      template_id,
      file_uuid,
      document_input_title,
      client_reference_id,
      client_reference_data
    } = context.propsValue, body = {
      business_profile_id,
      template_id,
      file_uuid
    };
    return document_input_title && (body.document_input_title = document_input_title), client_reference_id && (body.client_reference_id = client_reference_id), client_reference_data && (body.client_reference_data = client_reference_data), aiprise.makeRequest({
      auth: context.auth.props,
      method: import_pieces_common10.HttpMethod.POST,
      path: "/verify/run_check_on_business_document",
      body
    });
  }
});

// ../ap/packages/pieces/community/aiprise/src/lib/actions/create-business-profile.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var createBusinessProfileAction = (0, import_pieces_framework11.createAction)({
  auth: aipriseAuth,
  name: "create_business_profile",
  classification: "WRITE",
  displayName: "Create Business Profile",
  description: "Creates a new business profile in AiPrise. The returned profile can then be used to run document checks or other business verifications.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new business (company) profile in AiPrise from supplied details (name, tax ID, addresses, contacts, etc.); the resulting profile is the entity you later run business verifications or document checks against. Use this as the setup step before Start Business Verification or Run Business Document Check. Only the business name is required. Not idempotent \u2014 each call creates a separate profile, even with identical input.",
    idempotent: !1
  },
  props: {
    name: import_pieces_framework11.Property.ShortText({
      displayName: "Business Name",
      description: "The business name. The legal name is preferred.",
      required: !0
    }),
    alternate_name: import_pieces_framework11.Property.ShortText({
      displayName: "Alternate Name (DBA)",
      description: 'An alternate "doing business as" name, if any.',
      required: !1
    }),
    tax_identification_number: import_pieces_framework11.Property.ShortText({
      displayName: "Tax Identification Number",
      description: "The tax ID for this business (e.g. EIN in the US).",
      required: !1
    }),
    website: import_pieces_framework11.Property.ShortText({
      displayName: "Website",
      description: "The business website URL.",
      required: !1
    }),
    formation_date: import_pieces_framework11.Property.ShortText({
      displayName: "Formation Date",
      description: "The date the business was formed, in YYYY-MM-DD format.",
      required: !1
    }),
    country_code: import_pieces_framework11.Property.ShortText({
      displayName: "Country Code",
      description: "The 2-letter ISO country code where the business is registered (e.g. US, GB).",
      required: !1
    }),
    state_code: import_pieces_framework11.Property.ShortText({
      displayName: "State Code",
      description: "The state or province code where the business is registered (e.g. CA, NY).",
      required: !1
    }),
    business_entity_id: import_pieces_framework11.Property.ShortText({
      displayName: "Business Entity ID",
      description: "A government-issued entity identifier for this business.",
      required: !1
    }),
    addresses: import_pieces_framework11.Property.Array({
      displayName: "Addresses",
      description: "A JSON array of address objects associated with the business.",
      required: !1,
      properties: {
        address_street_1: import_pieces_framework11.Property.ShortText({
          displayName: "Street Address 1",
          required: !1
        }),
        address_street_2: import_pieces_framework11.Property.ShortText({
          displayName: "Street Address 2",
          required: !1
        }),
        address_city: import_pieces_framework11.Property.ShortText({
          displayName: "City",
          required: !1
        }),
        address_state: import_pieces_framework11.Property.ShortText({
          displayName: "State/Province",
          required: !1
        }),
        address_zip_code: import_pieces_framework11.Property.ShortText({
          displayName: "ZIP/Postal Code",
          required: !1
        }),
        address_country: import_pieces_framework11.Property.ShortText({
          displayName: "Country Code",
          description: "2-letter ISO country code (e.g. US, GB).",
          required: !1
        })
      }
    }),
    phone_numbers: import_pieces_framework11.Property.Array({
      displayName: "Phone Numbers",
      description: "One or more phone numbers associated with the business.",
      required: !1
    }),
    email_addresses: import_pieces_framework11.Property.Array({
      displayName: "Email Addresses",
      description: "One or more email addresses associated with the business.",
      required: !1
    }),
    additional_information: import_pieces_framework11.Property.Array({
      displayName: "Additional Information",
      description: "A JSON array of supporting documents or extra data for this business.",
      required: !1
    }),
    client_reference_id: import_pieces_framework11.Property.ShortText({
      displayName: "Client Reference ID",
      description: "Your own internal identifier for this business.",
      required: !1
    }),
    callback_url: import_pieces_framework11.Property.ShortText({
      displayName: "Callback URL",
      description: "A webhook URL that AiPrise will call with verification results.",
      required: !1
    }),
    events_callback_url: import_pieces_framework11.Property.ShortText({
      displayName: "Events Callback URL",
      description: "A webhook URL that AiPrise will call with event notifications.",
      required: !1
    }),
    tags: import_pieces_framework11.Property.Array({
      displayName: "Tags",
      description: "Tag names used to categorize this business profile.",
      required: !1
    })
  },
  async run(context) {
    let {
      name,
      alternate_name,
      tax_identification_number,
      website,
      formation_date,
      country_code,
      state_code,
      business_entity_id,
      addresses,
      phone_numbers,
      email_addresses,
      additional_information,
      client_reference_id,
      callback_url,
      events_callback_url,
      tags
    } = context.propsValue, body = { name };
    return alternate_name && (body.alternate_name = alternate_name), tax_identification_number && (body.tax_identification_number = tax_identification_number), website && (body.website = website), formation_date && (body.formation_date = formation_date), country_code && (body.country_code = country_code), state_code && (body.state_code = state_code), business_entity_id && (body.business_entity_id = business_entity_id), addresses && addresses.length > 0 && (body.addresses = addresses), phone_numbers && phone_numbers.length > 0 && (body.phone_numbers = phone_numbers), email_addresses && email_addresses.length > 0 && (body.email_addresses = email_addresses), additional_information && (body.additional_information = additional_information), client_reference_id && (body.client_reference_id = client_reference_id), callback_url && (body.callback_url = callback_url), events_callback_url && (body.events_callback_url = events_callback_url), tags && tags.length > 0 && (body.tags = tags), aiprise.makeRequest({
      auth: context.auth.props,
      method: import_pieces_common11.HttpMethod.POST,
      path: "/verify/create_business_profile",
      body
    });
  }
});

// ../ap/packages/pieces/community/aiprise/src/lib/actions/create-user-profile.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var createUserProfileAction = (0, import_pieces_framework12.createAction)({
  auth: aipriseAuth,
  name: "create_user_profile",
  classification: "WRITE",
  displayName: "Create User Profile",
  description: "Creates a new user profile in AiPrise. The returned profile can then be used for repeated verifications or ongoing monitoring for the same person.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new person (user) profile in AiPrise from supplied identity details (name, date of birth, contact, address, etc.); the resulting profile is the entity you later run identity verifications or ongoing monitoring against. Use this as the setup step before Start Identity Verification for a profile. All fields are optional. Not idempotent \u2014 each call creates a separate profile, even with identical input.",
    idempotent: !1
  },
  props: {
    first_name: import_pieces_framework12.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    middle_name: import_pieces_framework12.Property.ShortText({
      displayName: "Middle Name",
      required: !1
    }),
    last_name: import_pieces_framework12.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    second_last_name: import_pieces_framework12.Property.ShortText({
      displayName: "Second Last Name",
      description: "User's second surname, where applicable (e.g. in some Latin American naming conventions).",
      required: !1
    }),
    full_name: import_pieces_framework12.Property.ShortText({
      displayName: "Full Name",
      description: "The complete name. Use this if you do not have the name split into parts.",
      required: !1
    }),
    date_of_birth: import_pieces_framework12.Property.ShortText({
      displayName: "Date of Birth",
      description: "User's date of birth in YYYY-MM-DD format.",
      required: !1
    }),
    email_address: import_pieces_framework12.Property.ShortText({
      displayName: "Email Address",
      required: !1
    }),
    phone_number: import_pieces_framework12.Property.ShortText({
      displayName: "Phone Number",
      required: !1
    }),
    address_street_1: import_pieces_framework12.Property.ShortText({
      displayName: "Street Address 1",
      required: !1
    }),
    address_street_2: import_pieces_framework12.Property.ShortText({
      displayName: "Street Address 2",
      required: !1
    }),
    address_city: import_pieces_framework12.Property.ShortText({
      displayName: "City",
      required: !1
    }),
    address_state: import_pieces_framework12.Property.ShortText({
      displayName: "State/Province",
      required: !1
    }),
    address_zip_code: import_pieces_framework12.Property.ShortText({
      displayName: "ZIP/Postal Code",
      required: !1
    }),
    address_country: import_pieces_framework12.Property.ShortText({
      displayName: "Country Code",
      description: "2-letter ISO country code (e.g. US, GB).",
      required: !1
    }),
    client_reference_id: import_pieces_framework12.Property.ShortText({
      displayName: "Client Reference ID",
      description: "Your own internal identifier for this user.",
      required: !1
    }),
    events_callback_url: import_pieces_framework12.Property.ShortText({
      displayName: "Events Callback URL",
      description: "A webhook URL that AiPrise will call with event notifications for this profile. Paste the webhook URL from the **Verification Event Received** trigger here.",
      required: !1
    }),
    tags: import_pieces_framework12.Property.Array({
      displayName: "Tags",
      description: "Tag names used to categorize this user profile. Tags are created if they do not already exist.",
      required: !1
    })
  },
  async run(context) {
    let {
      first_name,
      middle_name,
      last_name,
      second_last_name,
      full_name,
      date_of_birth,
      email_address,
      phone_number,
      address_street_1,
      address_street_2,
      address_city,
      address_state,
      address_zip_code,
      address_country,
      client_reference_id,
      events_callback_url,
      tags
    } = context.propsValue, body = {};
    first_name && (body.first_name = first_name), middle_name && (body.middle_name = middle_name), last_name && (body.last_name = last_name), second_last_name && (body.second_last_name = second_last_name), full_name && (body.full_name = full_name), date_of_birth && (body.date_of_birth = date_of_birth), email_address && (body.email_address = email_address), phone_number && (body.phone_number = phone_number);
    let address = {};
    return address_street_1 && (address.address_street_1 = address_street_1), address_street_2 && (address.address_street_2 = address_street_2), address_city && (address.address_city = address_city), address_state && (address.address_state = address_state), address_zip_code && (address.address_zip_code = address_zip_code), address_country && (address.address_country = address_country), Object.keys(address).length > 0 && (body.address = address), client_reference_id && (body.client_reference_id = client_reference_id), events_callback_url && (body.events_callback_url = events_callback_url), tags && tags.length > 0 && (body.tags = tags), aiprise.makeRequest({
      auth: context.auth.props,
      method: import_pieces_common12.HttpMethod.POST,
      path: "/verify/create_user_profile",
      body
    });
  }
});

// ../ap/packages/pieces/community/aiprise/src/lib/actions/get-additional-user-info.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common");
var getAdditionalUserInfoAction = (0, import_pieces_framework13.createAction)({
  auth: aipriseAuth,
  name: "get_additional_user_info",
  classification: "READ",
  displayName: "Get Person's Additional Info",
  description: "Fetches supplementary information collected from the person during a verification \u2014 anything beyond the primary identity fields, such as follow-up answers, extra documents, or custom form data.",
  audience: "both",
  aiMetadata: {
    description: "Retrieves supplementary information gathered from a person during an identity verification \u2014 anything beyond the core identity fields, such as follow-up answers, extra documents, or custom form data \u2014 for a single session. Use when the primary submitted data (Get Person's Submitted Verification Data) is not enough. Requires the verification session ID. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    session_id: import_pieces_framework13.Property.ShortText({
      displayName: "Verification Session ID",
      description: "The ID of the verification to look up. You can get this from the output of the **Start Identity Verification** action",
      required: !0
    })
  },
  async run(context) {
    let { session_id } = context.propsValue;
    return aiprise.makeRequest({
      auth: context.auth.props,
      method: import_pieces_common13.HttpMethod.GET,
      path: `/verify/get_additional_user_info_from_request/${session_id}`
    });
  }
});

// ../ap/packages/pieces/community/aiprise/src/lib/actions/get-business-documents.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_common14 = require("@activepieces/pieces-common");
var getBusinessDocumentsAction = (0, import_pieces_framework14.createAction)({
  auth: aipriseAuth,
  name: "get_business_documents",
  classification: "SEARCH",
  displayName: "Get Business Documents",
  description: "Lists all documents attached to a business profile \u2014 including file UUIDs, names, S3 URLs, timestamps, document sources, and associated verification sessions.",
  audience: "both",
  aiMetadata: {
    description: "Lists every document attached to a given business profile, including each file's UUID, name, download URL, and the verification session it belongs to. Use this to discover a document's file UUID before running a document check, or to inventory what files a business has on file. Requires the business_profile_id. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    business_profile_id: import_pieces_framework14.Property.ShortText({
      displayName: "Business Profile ID",
      description: "The ID of the business profile whose documents you want to list. You can get this from the output of the **Create Business Profile** action.",
      required: !0
    })
  },
  async run(context) {
    let { business_profile_id } = context.propsValue;
    return aiprise.makeRequest({
      auth: context.auth.props,
      method: import_pieces_common14.HttpMethod.GET,
      path: `/verify/get_business_documents/${business_profile_id}`
    });
  }
});

// ../ap/packages/pieces/community/aiprise/src/lib/actions/get-user-profile.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_common15 = require("@activepieces/pieces-common");
var getUserProfileAction = (0, import_pieces_framework15.createAction)({
  auth: aipriseAuth,
  name: "get_user_profile",
  classification: "READ",
  displayName: "Get User Profile",
  description: "Fetches a user profile from AiPrise by its ID \u2014 including the person's stored details, tags, linked verification sessions, and metadata.",
  audience: "both",
  aiMetadata: {
    description: "Fetches a single user (person) profile by its ID, returning the stored identity details, tags, linked verification sessions, and metadata. Use this to read back a profile you created or referenced. Requires the user_profile_id. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    user_profile_id: import_pieces_framework15.Property.ShortText({
      displayName: "User Profile ID",
      description: "The ID of the user profile to retrieve. You can get this from the output of the **Create User Profile** action, or from the `user_profile_id` field in any webhook payload from AiPrise.",
      required: !0
    })
  },
  async run(context) {
    let { user_profile_id } = context.propsValue;
    return aiprise.makeRequest({
      auth: context.auth.props,
      method: import_pieces_common15.HttpMethod.GET,
      path: `/verify/get_user_profile/${encodeURIComponent(user_profile_id)}`
    });
  }
});

// ../ap/packages/pieces/community/aiprise/src/lib/actions/get-business-profile.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_common16 = require("@activepieces/pieces-common");
var getBusinessProfileAction = (0, import_pieces_framework16.createAction)({
  auth: aipriseAuth,
  name: "get_business_profile",
  classification: "READ",
  displayName: "Get Business Profile",
  description: "Fetches a business profile from AiPrise by its ID \u2014 including the stored company details, addresses, tags, linked verification sessions, and metadata.",
  audience: "both",
  aiMetadata: {
    description: "Fetches a single business (company) profile by its ID, returning the stored company details, addresses, tags, linked verification sessions, and metadata. Use this to read back a business profile you created or referenced. Requires the business_profile_id. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    business_profile_id: import_pieces_framework16.Property.ShortText({
      displayName: "Business Profile ID",
      description: "The ID of the business profile to retrieve. You can get this from the output of the **Create Business Profile** action, or from the `business_profile_id` field in any webhook payload from AiPrise.",
      required: !0
    })
  },
  async run(context) {
    let { business_profile_id } = context.propsValue;
    return aiprise.makeRequest({
      auth: context.auth.props,
      method: import_pieces_common16.HttpMethod.GET,
      path: `/verify/get_business_profile/${encodeURIComponent(business_profile_id)}`
    });
  }
});

// ../ap/packages/pieces/community/aiprise/src/lib/actions/search-businesses.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework"), import_pieces_common17 = require("@activepieces/pieces-common");
var searchBusinessesAction = (0, import_pieces_framework17.createAction)({
  auth: aipriseAuth,
  name: "search_businesses",
  classification: "SEARCH",
  displayName: "Search Businesses",
  description: "Searches public business registries by name and country, returning a list of matching companies with their entity IDs, types, and addresses. Useful for looking up a business before creating a profile or running a verification.",
  audience: "both",
  aiMetadata: {
    description: "Searches public business registries by company name and country (optionally narrowed by state/province), returning matching companies with their registry entity IDs, types, and addresses. Use this to look up and identify a real-world business before creating a profile or running a verification. Requires a business name and a 2-letter country code; partial name matches are supported. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    name: import_pieces_framework17.Property.ShortText({
      displayName: "Business Name",
      description: "The business name to search for. Partial matches are supported by AiPrise.",
      required: !0
    }),
    country_code: import_pieces_framework17.Property.ShortText({
      displayName: "Country Code",
      description: "2-letter ISO country code where the business is registered (e.g. US, GB).",
      required: !0
    }),
    state_code: import_pieces_framework17.Property.ShortText({
      displayName: "State/Province Code",
      description: "Optional state or province code to narrow the search (e.g. CA, NY for the US). Leave empty to search the whole country.",
      required: !1
    })
  },
  async run(context) {
    let { name, country_code, state_code } = context.propsValue, body = {
      name,
      country_code
    };
    return state_code && (body.state_code = state_code), aiprise.makeRequest({
      auth: context.auth.props,
      method: import_pieces_common17.HttpMethod.POST,
      path: "/data/business/search",
      body
    });
  }
});

// ../ap/packages/pieces/community/aiprise/src/index.ts
var aiprise2 = (0, import_pieces_framework18.createPiece)({
  displayName: "AiPrise",
  description: "KYC, KYB, and fraud prevention platform \u2014 verify user identities, onboard businesses, run AML/sanctions checks, and detect fraud signals.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/aiprise.png",
  categories: [import_pieces_framework19.PieceCategory.BUSINESS_INTELLIGENCE],
  auth: aipriseAuth,
  authors: ["sanket-a11y"],
  actions: [
    runUserVerificationProfileAction,
    getVerificationUrlAction,
    getVerificationResultAction,
    getUserInputDataAction,
    updateVerificationResultAction,
    runBusinessVerificationAction,
    getBusinessResultAction,
    getBusinessInputDataAction,
    runDocumentCheckAction,
    createBusinessProfileAction,
    createUserProfileAction,
    getUserProfileAction,
    getBusinessProfileAction,
    searchBusinessesAction,
    getAdditionalUserInfoAction,
    getBusinessDocumentsAction,
    (0, import_pieces_common18.createCustomApiCallAction)({
      baseUrl: (auth) => (auth == null ? void 0 : auth.props.environment) === "sandbox" ? "https://api-sandbox.aiprise.com/api/v1" : "https://api.aiprise.com/api/v1",
      auth: aipriseAuth,
      authMapping: async (auth) => ({
        "X-API-KEY": auth.props.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  aiprise
});
