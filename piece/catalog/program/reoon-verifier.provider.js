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

// ../ap/packages/pieces/community/reoon-verifier/src/index.ts
var index_exports = {};
__export(index_exports, {
  reoonEmailVerify: () => reoonEmailVerify,
  reoonEmailVerifyAuth: () => reoonEmailVerifyAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/reoon-verifier/src/lib/actions/verify-email.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/reoon-verifier/src/lib/common/send-util.ts
var import_pieces_common = require("@activepieces/pieces-common"), verifySingleEmail = (email, mode, apiKey) => import_pieces_common.httpClient.sendRequest({
  method: import_pieces_common.HttpMethod.GET,
  url: `https://emailverifier.reoon.com/api/v1/verify?email=${email}&key=${apiKey}&mode=${mode}`
}), verifyEmails = (emails, taskName, apiKey) => import_pieces_common.httpClient.sendRequest({
  method: import_pieces_common.HttpMethod.POST,
  url: "https://emailverifier.reoon.com/api/v1/create-bulk-verification-task",
  body: {
    name: taskName,
    emails,
    key: apiKey
  }
});
async function verifyEmailsResult(taskId, apiKey) {
  return await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: "https://emailverifier.reoon.com/api/v1/get-result-bulk-verification-task",
    queryParams: {
      key: apiKey,
      task_id: taskId
    }
  });
}

// ../ap/packages/pieces/community/reoon-verifier/src/lib/output-schemas.ts
var emailVerificationFields = [
  { key: "email", label: "Email", format: "email" },
  { key: "domain", label: "Domain" },
  { key: "username", label: "Username" },
  { key: "status", label: "Status" },
  { key: "overall_score", label: "Overall Score", format: "number" },
  { key: "is_valid_syntax", label: "Valid Syntax", format: "boolean" },
  { key: "is_disposable", label: "Disposable", format: "boolean" },
  { key: "is_role_account", label: "Role Account", format: "boolean" },
  { key: "is_free_email", label: "Free Email Provider", format: "boolean" },
  { key: "is_spamtrap", label: "Spamtrap", format: "boolean" },
  { key: "is_forwarded", label: "Forwarded", format: "boolean" },
  { key: "is_catch_all", label: "Catch-All Domain", format: "boolean" },
  { key: "is_disabled", label: "Disabled", format: "boolean" },
  { key: "is_deliverable", label: "Deliverable", format: "boolean" },
  { key: "is_safe_to_send", label: "Safe To Send", format: "boolean" },
  { key: "can_connect_smtp", label: "Can Connect Via SMTP", format: "boolean" },
  { key: "has_inbox_full", label: "Inbox Full", format: "boolean" },
  { key: "mx_accepts_mail", label: "MX Accepts Mail", format: "boolean" },
  { key: "mx_records", label: "MX Records" }
], verifyEmailActionOutputSchema = {
  fields: [
    ...emailVerificationFields,
    { key: "verification_mode", label: "Verification Mode" }
  ]
}, createBulkEmailVerificationActionOutputSchema = {
  fields: [
    { key: "status", label: "Status" },
    { key: "task_id", label: "Task ID" },
    { key: "count_submitted", label: "Emails Submitted", format: "number" },
    { key: "count_processing", label: "Emails Processing", format: "number" },
    { key: "count_rejected_emails", label: "Emails Rejected", format: "number" },
    { key: "count_duplicates_removed", label: "Duplicates Removed", format: "number" }
  ]
}, getBulkEmailVerificationResultActionOutputSchema = {
  fields: [
    { key: "task_id", label: "Task ID" },
    { key: "name", label: "Task Name" },
    { key: "status", label: "Status" },
    { key: "progress_percentage", label: "Progress Percentage", format: "number" },
    { key: "count_total", label: "Total Emails", format: "number" },
    { key: "count_checked", label: "Emails Checked", format: "number" },
    {
      key: "results",
      label: "Results",
      dynamicKey: !0,
      labelKey: "status",
      children: emailVerificationFields
    }
  ]
};

// ../ap/packages/pieces/community/reoon-verifier/src/lib/actions/verify-email.ts
var verifyEmail = (0, import_pieces_framework.createAction)({
  auth: reoonEmailVerifyAuth,
  name: "verifyEmail",
  displayName: "Verify Email",
  description: "Verify a single email",
  audience: "human",
  aiMetadata: { description: "Validates one email address against the Reoon email verification API, returning deliverability and risk signals (e.g. invalid, disposable, role-based). Choose between a fast Quick mode and a slower, more accurate Power mode. Use for checking a single address before sending; for many addresses use the bulk action instead. Idempotent: a read-only lookup that returns the same result for the same input.", idempotent: !0 },
  outputSchema: verifyEmailActionOutputSchema,
  props: {
    email: import_pieces_framework.Property.ShortText({
      displayName: "Email",
      description: "Email to verify",
      required: !0
    }),
    mode: import_pieces_framework.Property.StaticDropdown({
      displayName: "Mode",
      defaultValue: "power",
      description: "Verification mode (Power mode is more accurate but a bit slower)",
      options: {
        placeholder: "Select a mode",
        options: [
          {
            label: "Quick",
            value: "quick"
          },
          {
            label: "Power",
            value: "power"
          }
        ]
      },
      required: !0
    })
  },
  async run(context) {
    return verifySingleEmail(
      context.propsValue.email,
      context.propsValue.mode,
      context.auth.secret_text
    ).then((res) => res.body);
  }
});

// ../ap/packages/pieces/community/reoon-verifier/src/lib/actions/bulk-email-verification.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var bulkEmailVerification = (0, import_pieces_framework2.createAction)({
  auth: reoonEmailVerifyAuth,
  name: "bulkEmailVerificationTask",
  displayName: "Create Bulk Email Verification",
  description: "Creates bulk email verification task.",
  audience: "human",
  aiMetadata: { description: "Submits a named batch of email addresses to the Reoon API for asynchronous bulk verification and returns the task identifier used to poll for results later (pair with the Get Bulk Verification Result action). Use when validating many addresses at once rather than one-by-one; emails may be passed as separate entries or comma-separated. Not idempotent: each call creates a new verification task.", idempotent: !1 },
  outputSchema: createBulkEmailVerificationActionOutputSchema,
  props: {
    taskName: import_pieces_framework2.Property.ShortText({
      displayName: "Task Name",
      description: "Name of the verification task",
      required: !0
    }),
    emails: import_pieces_framework2.Property.Array({
      displayName: "Emails",
      description: "Emails to verify (You can also provide multiple emails separated by comma)",
      required: !0
    })
  },
  async run(context) {
    let emailsToVerify = context.propsValue.emails.reduce(
      (acc, email) => [...acc, ...email.split(",")],
      []
    );
    return (await verifyEmails(
      emailsToVerify,
      context.propsValue.taskName,
      context.auth.secret_text
    )).body;
  }
});

// ../ap/packages/pieces/community/reoon-verifier/src/index.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/reoon-verifier/src/lib/actions/bulk-email-verification-status.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var bulkVerificationResult = (0, import_pieces_framework3.createAction)({
  auth: reoonEmailVerifyAuth,
  name: "bulkVerificationResult",
  displayName: "Get Bulk Verification Result",
  description: "Retrieves result of bulk verification email by task ID.",
  audience: "human",
  aiMetadata: { description: "Fetches the current status and verification results for a previously created bulk email verification task, identified by its task ID (obtained from the Create Bulk Email Verification action). Use to poll for or read back batch results. Idempotent: a read-only lookup that does not change the task.", idempotent: !0 },
  outputSchema: getBulkEmailVerificationResultActionOutputSchema,
  props: {
    task_id: import_pieces_framework3.Property.ShortText({
      displayName: "Task ID",
      description: "Provide the task ID for the bulk verification task. You can fetch this from the `Create Bulk Email Verification` action.",
      required: !0
    })
  },
  async run(context) {
    let { task_id } = context.propsValue;
    return (await verifyEmailsResult(task_id, context.auth.secret_text)).body;
  }
});

// ../ap/packages/pieces/community/reoon-verifier/src/lib/actions/verify-email-address.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var verifyEmailAddress = (0, import_pieces_framework4.createAction)({
  auth: reoonEmailVerifyAuth,
  name: "verify_email_address",
  displayName: "Verify Email Address",
  description: "Verify a single email address synchronously.",
  audience: "ai",
  aiMetadata: {
    description: "Verifies a single email address synchronously via Reoon and returns deliverability and risk signals (valid / invalid / disposable / role-based, etc.). Use this for one address at a time and when you need the result immediately; for many addresses use Create Bulk Email Verification instead. Each call consumes Reoon verification credits, and Power mode (more accurate, slower) costs more than Quick mode.",
    idempotent: !0
  },
  outputSchema: verifyEmailActionOutputSchema,
  props: {
    email: import_pieces_framework4.Property.ShortText({
      displayName: "Email",
      description: "Email to verify",
      required: !0
    }),
    mode: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Mode",
      defaultValue: "power",
      description: "Verification mode (Power mode is more accurate but a bit slower)",
      options: {
        placeholder: "Select a mode",
        options: [
          {
            label: "Quick",
            value: "quick"
          },
          {
            label: "Power",
            value: "power"
          }
        ]
      },
      required: !0
    })
  },
  async run(context) {
    return verifySingleEmail(
      context.propsValue.email,
      context.propsValue.mode,
      context.auth.secret_text
    ).then((res) => res.body);
  }
});

// ../ap/packages/pieces/community/reoon-verifier/src/lib/actions/create-bulk-email-verification.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var createBulkEmailVerification = (0, import_pieces_framework5.createAction)({
  auth: reoonEmailVerifyAuth,
  name: "create_bulk_email_verification",
  displayName: "Create Bulk Email Verification",
  description: "Creates an asynchronous bulk email verification task.",
  audience: "ai",
  aiMetadata: {
    description: "Submits a named batch of email addresses to Reoon for asynchronous bulk verification and returns a task_id used to poll for results. Use this instead of single-email verify when validating many addresses at once; results are not immediate \u2014 afterward call Get Bulk Email Verification Result with the returned task_id. Each address verified consumes Reoon credits.",
    idempotent: !1
  },
  outputSchema: createBulkEmailVerificationActionOutputSchema,
  props: {
    taskName: import_pieces_framework5.Property.ShortText({
      displayName: "Task Name",
      description: "Name of the verification task",
      required: !0
    }),
    emails: import_pieces_framework5.Property.Array({
      displayName: "Emails",
      description: "Emails to verify (You can also provide multiple emails separated by comma)",
      required: !0
    })
  },
  async run(context) {
    let emailsToVerify = context.propsValue.emails.reduce(
      (acc, email) => [...acc, ...email.split(",")],
      []
    );
    return (await verifyEmails(
      emailsToVerify,
      context.propsValue.taskName,
      context.auth.secret_text
    )).body;
  }
});

// ../ap/packages/pieces/community/reoon-verifier/src/lib/actions/get-bulk-email-verification-result.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var getBulkEmailVerificationResult = (0, import_pieces_framework6.createAction)({
  auth: reoonEmailVerifyAuth,
  name: "get_bulk_email_verification_result",
  displayName: "Get Bulk Email Verification Result",
  description: "Reads the status and results of a bulk verification task.",
  audience: "ai",
  aiMetadata: {
    description: "Reads the current status and verification results of a bulk email verification task by its task_id. The task_id is obtained from the Create Bulk Email Verification action (run that first); poll this until the task reports complete. Read-only \u2014 does not change the task or consume additional verification credits.",
    idempotent: !0
  },
  outputSchema: getBulkEmailVerificationResultActionOutputSchema,
  props: {
    task_id: import_pieces_framework6.Property.ShortText({
      displayName: "Task ID",
      description: "Provide the task ID for the bulk verification task. You can fetch this from the `Create Bulk Email Verification` action.",
      required: !0
    })
  },
  async run(context) {
    let { task_id } = context.propsValue;
    return (await verifyEmailsResult(task_id, context.auth.secret_text)).body;
  }
});

// ../ap/packages/pieces/community/reoon-verifier/src/index.ts
var description = `
To obtain a Reoon API key, follow these steps:
1. Navigate to [API Setting](https://emailverifier.reoon.com/api-settings).
2. Click on the **Create New API Key** button.
3. Enter a valid title and copy the API key.
`, reoonEmailVerifyAuth = import_pieces_framework7.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description,
  validate: async (auth) => {
    try {
      await verifySingleEmail("placeholder@test", "quick", auth.auth);
    } catch (e) {
      let status = e.response.status;
      if (status === 401)
        return {
          valid: !1,
          error: "Your API key is invalid or has expired"
        };
      if (status >= 500)
        return {
          valid: !1,
          error: "An error occurred from the Reoon Email Verifier API"
        };
    }
    return {
      valid: !0
    };
  }
}), reoonEmailVerify = (0, import_pieces_framework7.createPiece)({
  displayName: "Reoon Email Verifier",
  auth: reoonEmailVerifyAuth,
  minimumSupportedRelease: "0.87.0",
  categories: [import_pieces_framework8.PieceCategory.MARKETING],
  logoUrl: "https://cdn.activepieces.com/pieces/reoon-verifier.png",
  description: "Email validation service that cleans invalid, temporary & unsafe email addresses.",
  authors: ["AnneMariel95"],
  actions: [
    verifyEmail,
    bulkEmailVerification,
    bulkVerificationResult,
    verifyEmailAddress,
    createBulkEmailVerification,
    getBulkEmailVerificationResult
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  reoonEmailVerify,
  reoonEmailVerifyAuth
});
