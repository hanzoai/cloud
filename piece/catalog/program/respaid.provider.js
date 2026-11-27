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

// ../ap/packages/pieces/community/respaid/src/index.ts
var index_exports = {};
__export(index_exports, {
  respaid: () => respaid
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework16 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/respaid/src/lib/actions/create_new_campaign.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/respaid/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), respaidAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "You can find API Key in your Respaid account"
});

// ../ap/packages/pieces/community/respaid/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common");
var respaidCommon = {
  baseUrl: "https://backend.widr.app/api/workflow",
  getHeadersStructure: (auth) => ({
    "Content-Type": "application/json",
    Accept: "application/json",
    "X-API-KEY": auth
  })
}, respaidActionsCommon = {
  getPayloadBodyStructure: (propsValue) => ({
    type: "active_pieces",
    payload: JSON.stringify(__spreadValues(__spreadValues(__spreadValues(__spreadValues({}, propsValue.unique_identifier && { unique_identifier: propsValue.unique_identifier }), propsValue.invoice_number && { invoice_number: propsValue.invoice_number }), propsValue.amount && { amount: propsValue.amount }), propsValue.email && { email: propsValue.email }))
  }),
  validateProps: (propsValue) => {
    let { unique_identifier, email } = propsValue;
    if (!unique_identifier && !email)
      throw new Error("You must provide either a unique_identifier OR email.");
  }
}, respaidTriggersCommon = {
  onEnable: (eventType) => async (context) => {
    try {
      console.log("Trigger enabled, subscribing to webhook"), await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.POST,
        url: `${respaidCommon.baseUrl}/webhook/subscribe`,
        headers: respaidCommon.getHeadersStructure(context.auth.secret_text),
        body: {
          type: "active_pieces",
          event_type: eventType,
          target_url: context.webhookUrl
        }
      });
    } catch (error) {
      throw console.error("Error subscribing to webhook:", error), new Error("Failed to subscribe to webhook");
    }
  },
  onDisable: (eventType) => async (context) => {
    try {
      console.log("Trigger disabled, unsubscribing from webhook"), await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.DELETE,
        url: `${respaidCommon.baseUrl}/webhook/unsubscribe`,
        headers: respaidCommon.getHeadersStructure(context.auth.secret_text),
        body: {
          type: "active_pieces",
          event_type: eventType,
          target_url: context.webhookUrl
        }
      });
    } catch (error) {
      throw console.error("Error unsubscribing from webhook:", error), new Error("Failed to unsubscribe to webhook");
    }
  },
  getPayload: (context) => typeof context.payload.body == "string" ? JSON.parse(context.payload.body) : context.payload
};

// ../ap/packages/pieces/community/respaid/src/lib/actions/create_new_campaign.ts
var createNewCampaign = (0, import_pieces_framework2.createAction)({
  name: "create_new_campaign",
  displayName: "Create New Campaign",
  description: "Action for creating a new campaign.",
  audience: "both",
  aiMetadata: { description: "Creates a new Respaid debt-collection campaign by importing a batch of invoices/debtor records (each with debtor contact details, due amount, and invoice info). Use to kick off collection on a set of unpaid invoices. Requires importData to be an array of invoice objects; creates a new campaign on every call, so it is not idempotent.", idempotent: !1 },
  auth: respaidAuth,
  props: {
    campaign_name: import_pieces_framework2.Property.ShortText({
      displayName: "Campaign Name",
      required: !0
    }),
    is_agency_collection: import_pieces_framework2.Property.Checkbox({
      displayName: "Agency collection?",
      required: !1,
      defaultValue: !1
    }),
    importData: import_pieces_framework2.Property.Json({
      displayName: "Import Data (Array of Invoices)",
      required: !0,
      description: `Provide an array of invoice objects with the following example structure:
      [{
          "unique_identifier": "123",
          "company_name": "Company XYZ",
          "email": "john@example.com",
          "invoice_number": "INV123",
          "invoice_date": "01/01/2025",
          "description": "Invoice for service"
          "due_amount": 1000,
          "invoicing_entity_name": "Creditor ABC",
          "invoicing_entity_address": "456 Avenue, City",
          "full_name": "John Doe",
          "phone_number": "1234567890",
          "address": "123 Street, City",
        }]`
    })
  },
  async run({ auth, propsValue }) {
    if (!Array.isArray(propsValue.importData))
      throw new Error("Import Data must be an array of objects.");
    let requestBody = {
      campaign_name: propsValue.campaign_name,
      is_agency_collection: propsValue.is_agency_collection,
      import: propsValue.importData.map((invoice) => ({
        unique_identifier: invoice.unique_identifier,
        full_name: invoice.full_name,
        company_name: invoice.company_name,
        email: invoice.email,
        phone_number: invoice.phone_number,
        address: invoice.address,
        due_amount: invoice.due_amount,
        invoicing_entity_name: invoice.invoicing_entity_name,
        invoicing_entity_address: invoice.invoicing_entity_address,
        invoice_number: invoice.invoice_number,
        invoice_date: invoice.invoice_date,
        description: invoice.description
      }))
    };
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: `${respaidCommon.baseUrl}/actions/import_campaign`,
      headers: respaidCommon.getHeadersStructure(auth.secret_text),
      body: { type: "active_pieces", import: JSON.stringify(requestBody) }
    })).body;
  }
});

// ../ap/packages/pieces/community/respaid/src/lib/actions/stop_collection_client_paid_directly.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var stopCollectionClientPaidDirectly = (0, import_pieces_framework3.createAction)({
  name: "stop_collection_client_paid_directly",
  displayName: "Stop Collection for Direct Full Payment",
  description: "Stops the collection process for a case and mark it as paid directly to the creditor.",
  audience: "both",
  aiMetadata: { description: "Halts the Respaid collection process for a single case and marks the debt as paid in full directly to the creditor (outside Respaid). Use when a debtor settled the invoice with the creditor directly. The case must be identified by either unique_identifier or email (one is required); it mutates case state, so it is not idempotent.", idempotent: !1 },
  auth: respaidAuth,
  props: {
    unique_identifier: import_pieces_framework3.Property.ShortText({
      displayName: "Unique Identifier",
      required: !1
    }),
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email",
      required: !1
    }),
    invoice_number: import_pieces_framework3.Property.ShortText({
      displayName: "Invoice Number",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    return respaidActionsCommon.validateProps(propsValue), (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: `${respaidCommon.baseUrl}/actions/stop_collection_client_paid_directly`,
      headers: respaidCommon.getHeadersStructure(auth.secret_text),
      body: respaidActionsCommon.getPayloadBodyStructure(propsValue)
    })).body;
  }
});

// ../ap/packages/pieces/community/respaid/src/lib/actions/stop_collection_for_direct_instalment_payment.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var stopCollectionForDirectInstalmentPayment = (0, import_pieces_framework4.createAction)({
  name: "stop_collection_for_direct_instalment_payment",
  displayName: "Stop Collection for Direct Instalment Payment",
  description: "Stops the collection process for a case when an instalment plan is set up with the creditor.",
  audience: "both",
  aiMetadata: { description: "Halts the Respaid collection process for a single case because the debtor and creditor agreed on a direct instalment (payment plan) arrangement. Use when collection should pause in favour of a creditor-side instalment plan rather than full settlement. The case must be identified by either unique_identifier or email (one is required); it mutates case state, so it is not idempotent.", idempotent: !1 },
  auth: respaidAuth,
  props: {
    unique_identifier: import_pieces_framework4.Property.ShortText({
      displayName: "Unique Identifier",
      required: !1
    }),
    email: import_pieces_framework4.Property.ShortText({
      displayName: "Email",
      required: !1
    }),
    invoice_number: import_pieces_framework4.Property.ShortText({
      displayName: "Invoice Number",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    return respaidActionsCommon.validateProps(propsValue), (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: `${respaidCommon.baseUrl}/actions/stop_collection_for_direct_instalment_payment`,
      headers: respaidCommon.getHeadersStructure(auth.secret_text),
      body: respaidActionsCommon.getPayloadBodyStructure(propsValue)
    })).body;
  }
});

// ../ap/packages/pieces/community/respaid/src/lib/actions/stop_collection_for_direct_partial_payment.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var stopCollectionForDirectPartialPayment = (0, import_pieces_framework5.createAction)({
  name: "stop_collection_for_direct_partial_payment",
  displayName: "Stop Collection for Direct Partial Payment",
  description: "Stops the collection process for a case and mark it as partially paid directly to the creditor.",
  audience: "both",
  aiMetadata: { description: "Halts the Respaid collection process for a single case and records a partial payment made directly to the creditor (outside Respaid), passing the paid amount. Use when a debtor settled part of the invoice with the creditor directly. The case must be identified by either unique_identifier or email (one is required); it records a payment and mutates case state, so it is not idempotent.", idempotent: !1 },
  auth: respaidAuth,
  props: {
    unique_identifier: import_pieces_framework5.Property.ShortText({
      displayName: "Unique Identifier",
      required: !1
    }),
    amount: import_pieces_framework5.Property.ShortText({
      displayName: "Amount",
      required: !1
    }),
    email: import_pieces_framework5.Property.ShortText({
      displayName: "Email",
      required: !1
    }),
    invoice_number: import_pieces_framework5.Property.ShortText({
      displayName: "Invoice Number",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    return respaidActionsCommon.validateProps(propsValue), (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.POST,
      url: `${respaidCommon.baseUrl}/actions/stop_collection_for_direct_partial_payment`,
      headers: respaidCommon.getHeadersStructure(auth.secret_text),
      body: respaidActionsCommon.getPayloadBodyStructure(propsValue)
    })).body;
  }
});

// ../ap/packages/pieces/community/respaid/src/lib/actions/index.ts
var respaidActions = [
  createNewCampaign,
  stopCollectionClientPaidDirectly,
  stopCollectionForDirectPartialPayment,
  stopCollectionForDirectInstalmentPayment
];

// ../ap/packages/pieces/community/respaid/src/lib/triggers/new_campaign_creation.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var newCampaignCreation = (0, import_pieces_framework6.createTrigger)({
  name: "new_campaign_creation",
  displayName: "New Campaign Creation Result",
  description: "Triggers when the campaign is created.",
  aiMetadata: {
    description: "Fires when Respaid finishes processing a campaign import, reporting the outcome: which imported invoice records were accepted (valid) versus rejected (invalid, e.g. bad email), and a link to the processing report. Use to react to the result of creating a collection campaign."
  },
  auth: respaidAuth,
  props: {},
  sampleData: {
    request_id: "1234",
    is_campaign_created: !0,
    valid_files: [{
      unique_identifier: "1",
      sequence_code: "SQ###1"
    }, {
      unique_identifier: "2",
      sequence_code: "SQ###2"
    }],
    invalid_files: [{
      invalid_email: {
        unique_identifier_3: "3",
        unique_identifier_4: "4"
      }
    }],
    file_processing_report: "https://link_excel.com"
  },
  type: import_pieces_framework6.TriggerStrategy.WEBHOOK,
  onEnable: respaidTriggersCommon.onEnable("new_campaign_creation"),
  onDisable: respaidTriggersCommon.onDisable("new_campaign_creation"),
  async run(context) {
    return [respaidTriggersCommon.getPayload(context)];
  }
});

// ../ap/packages/pieces/community/respaid/src/lib/triggers/new_cancelled_case.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var newCancelledCase = (0, import_pieces_framework7.createTrigger)({
  name: "new_cancelled_case",
  displayName: "New Cancelled Case",
  description: "Triggers when a collection process for a given sequence (case) was cancelled.",
  aiMetadata: {
    description: "Fires when the collection process for a debtor case (sequence) is cancelled, carrying the debtor/invoice details and the cancellation reason. Use to react to a case being dropped from active collection."
  },
  auth: respaidAuth,
  props: {},
  sampleData: {
    unique_identifier: "123",
    name: "John Doe",
    company_name: "Company XYZ",
    email: "john@example.com",
    phone_number: "1234567890",
    invoice_number: "INV123",
    amount: 1e3,
    currency: "usd",
    reason: "Issue with invoice"
  },
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  onEnable: respaidTriggersCommon.onEnable("new_cancelled_case"),
  onDisable: respaidTriggersCommon.onDisable("new_cancelled_case"),
  async run(context) {
    return [respaidTriggersCommon.getPayload(context)];
  }
});

// ../ap/packages/pieces/community/respaid/src/lib/triggers/new_disputed_case.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var newDisputedCase = (0, import_pieces_framework8.createTrigger)({
  name: "new_disputed_case",
  displayName: "New Disputed Case",
  description: "Triggers when a collection process was disputed by the debtor.",
  aiMetadata: {
    description: "Fires when a debtor disputes (contests) an ongoing collection case, carrying the debtor/invoice details plus the dispute context and any attachment. Use to react to a contested debt that may need manual review."
  },
  auth: respaidAuth,
  props: {},
  sampleData: {
    unique_identifier: "123",
    name: "John Doe",
    company_name: "Company XYZ",
    email: "john@example.com",
    phone_number: "1234567890",
    invoice_number: "INV123",
    amount: 1e3,
    currency: "usd",
    context: `Q: In order to stop the proceedings against you and not increase the amount of the debt, we can offer you
A: Payment in instalments of up to 5 months (activation of instalments within 1 working day).
Q: Do you agree to sign the following mandate?
A: I agree`,
    attachment: "https://link_excel.com/"
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  onEnable: respaidTriggersCommon.onEnable("new_disputed_case"),
  onDisable: respaidTriggersCommon.onDisable("new_disputed_case"),
  async run(context) {
    return [respaidTriggersCommon.getPayload(context)];
  }
});

// ../ap/packages/pieces/community/respaid/src/lib/triggers/new_payout.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var newPayout = (0, import_pieces_framework9.createTrigger)({
  name: "new_payout",
  displayName: "New Payout",
  description: "Triggers when a payout is successfully sent to your bank account.",
  aiMetadata: {
    description: "Fires when Respaid sends a payout to your bank account, carrying the payout id, date, and the list of collected payments (with amounts and fees) bundled into that payout. Use to reconcile disbursed collections against your bank deposits."
  },
  auth: respaidAuth,
  props: {},
  sampleData: {
    payout_id: "1234",
    date: "2025-03-02T00:00:00+0000",
    payments: [{
      reference: "XXX123",
      unique_identifier: "123",
      name: "John Doe",
      company_name: "Company XYZ",
      email: "john@example.com",
      phone_number: "1234567890",
      invoice_number: "INV123",
      amount: 1e3,
      fees: 2.5,
      currency: "usd",
      paid_at: "2025-03-02T00:00:00+0000"
    }]
  },
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  onEnable: respaidTriggersCommon.onEnable("new_payout"),
  onDisable: respaidTriggersCommon.onDisable("new_payout"),
  async run(context) {
    return [respaidTriggersCommon.getPayload(context)];
  }
});

// ../ap/packages/pieces/community/respaid/src/lib/triggers/new_successful_collection_paid_to_creditor.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var newSuccessfulCollectionPaidToCreditor = (0, import_pieces_framework10.createTrigger)({
  name: "new_successful_collection_paid_to_creditor",
  displayName: "New Successful Collection Paid to Creditor",
  description: "Triggers when a debt is paid directly to the creditor.",
  aiMetadata: {
    description: "Fires when a debt is paid in full directly to the creditor (outside Respaid), carrying the debtor/invoice details, amount, and payment timestamp. Use to react to a full settlement that bypassed Respaid."
  },
  auth: respaidAuth,
  props: {},
  sampleData: {
    unique_identifier: "123",
    name: "John Doe",
    company_name: "Company XYZ",
    email: "john@example.com",
    phone_number: "1234567890",
    invoice_number: "INV123",
    amount: 1e3,
    currency: "usd",
    paid_at: "2025-03-02T00:00:00+0000"
  },
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  onEnable: respaidTriggersCommon.onEnable("new_successful_collection_paid_to_creditor"),
  onDisable: respaidTriggersCommon.onDisable("new_successful_collection_paid_to_creditor"),
  async run(context) {
    return [respaidTriggersCommon.getPayload(context)];
  }
});

// ../ap/packages/pieces/community/respaid/src/lib/triggers/new_successful_installment_payment_via_respaid.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var newSuccessfulInstallmentPaymentViaRespaid = (0, import_pieces_framework11.createTrigger)({
  name: "new_successful_installment_payment_via_respaid",
  displayName: "New Successful Installment Payment via Respaid",
  description: "Triggers when one of the installment payments is made for a given case within a collection's payment plan.",
  aiMetadata: {
    description: "Fires when a debtor makes one installment payment within a Respaid payment plan, carrying the debtor/invoice details, paid amount, current installment step, total installments, and remaining balance. Use to track progress through a collection's installment schedule."
  },
  auth: respaidAuth,
  props: {},
  sampleData: {
    unique_identifier: "123",
    name: "John Doe",
    company_name: "Company XYZ",
    email: "john@example.com",
    phone_number: "1234567890",
    invoice_number: "INV123",
    amount: 1e3,
    paid_amount: 250,
    installments_number: 4,
    current_installment_step: 1,
    balance: 750,
    currency: "usd",
    paid_at: "2025-03-02T00:00:00+0000"
  },
  type: import_pieces_framework11.TriggerStrategy.WEBHOOK,
  onEnable: respaidTriggersCommon.onEnable("new_successful_installment_payment_via_respaid"),
  onDisable: respaidTriggersCommon.onDisable("new_successful_installment_payment_via_respaid"),
  async run(context) {
    return [respaidTriggersCommon.getPayload(context)];
  }
});

// ../ap/packages/pieces/community/respaid/src/lib/triggers/new_successful_collection_via_legal_officer.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var newSuccessfulCollectionViaLegalOfficer = (0, import_pieces_framework12.createTrigger)({
  name: "new_successful_collection_via_legal_officer",
  displayName: "New Successful Collection via Legal Officer",
  description: "Triggers when a debt is paid to the Legal Officer responsible for the collection campaign.",
  aiMetadata: {
    description: "Fires when a debt is paid to the legal officer handling the collection campaign, carrying the debtor/invoice details, amount, and payment timestamp. Use to react to a settlement collected through the legal channel."
  },
  auth: respaidAuth,
  props: {},
  sampleData: {
    unique_identifier: "123",
    name: "John Doe",
    company_name: "Company XYZ",
    email: "john@example.com",
    phone_number: "1234567890",
    invoice_number: "INV123",
    amount: 1e3,
    currency: "usd",
    paid_at: "2025-03-02T00:00:00+0000"
  },
  type: import_pieces_framework12.TriggerStrategy.WEBHOOK,
  onEnable: respaidTriggersCommon.onEnable("new_successful_collection_via_legal_officer"),
  onDisable: respaidTriggersCommon.onDisable("new_successful_collection_via_legal_officer"),
  async run(context) {
    return [respaidTriggersCommon.getPayload(context)];
  }
});

// ../ap/packages/pieces/community/respaid/src/lib/triggers/new_successful_partial_payment_to_creditor.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var newSuccessfulPartialPaymentToCreditor = (0, import_pieces_framework13.createTrigger)({
  name: "new_successful_partial_payment_to_creditor",
  displayName: "New Successful Partial Payment to Creditor",
  description: "Triggers when the debt is partially paid directly to the creditor.",
  aiMetadata: {
    description: "Fires when a debt is partially paid directly to the creditor (outside Respaid), carrying the debtor/invoice details, the paid amount, and the remaining balance. Use to react to a partial settlement that bypassed Respaid."
  },
  auth: respaidAuth,
  props: {},
  sampleData: {
    unique_identifier: "123",
    name: "John Doe",
    company_name: "Company XYZ",
    email: "john@example.com",
    phone_number: "1234567890",
    invoice_number: "INV123",
    amount: 1e3,
    paid_amount: 250,
    balance: 750,
    currency: "usd",
    paid_at: "2025-03-02T00:00:00+0000"
  },
  type: import_pieces_framework13.TriggerStrategy.WEBHOOK,
  onEnable: respaidTriggersCommon.onEnable("new_successful_partial_payment_to_creditor"),
  onDisable: respaidTriggersCommon.onDisable("new_successful_partial_payment_to_creditor"),
  async run(context) {
    return [respaidTriggersCommon.getPayload(context)];
  }
});

// ../ap/packages/pieces/community/respaid/src/lib/triggers/new_successful_partial_payment_via_respaid.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var newSuccessfulPartialPaymentViaRespaid = (0, import_pieces_framework14.createTrigger)({
  name: "new_successful_partial_payment_via_respaid",
  displayName: "New Successful Partial Payment via Respaid",
  description: "Triggers when the debt is partially paid via Respaid's payment link.",
  aiMetadata: {
    description: "Fires when a debt is partially paid online through Respaid's payment link, carrying the debtor/invoice details, the paid amount, and the remaining balance. Use to react to a partial settlement collected via Respaid."
  },
  auth: respaidAuth,
  props: {},
  sampleData: {
    unique_identifier: "123",
    name: "John Doe",
    company_name: "Company XYZ",
    email: "john@example.com",
    phone_number: "1234567890",
    invoice_number: "INV123",
    amount: 1e3,
    paid_amount: 250,
    balance: 750,
    currency: "usd",
    paid_at: "2025-03-02T00:00:00+0000"
  },
  type: import_pieces_framework14.TriggerStrategy.WEBHOOK,
  onEnable: respaidTriggersCommon.onEnable("new_successful_partial_payment_via_respaid"),
  onDisable: respaidTriggersCommon.onDisable("new_successful_partial_payment_via_respaid"),
  async run(context) {
    return [respaidTriggersCommon.getPayload(context)];
  }
});

// ../ap/packages/pieces/community/respaid/src/lib/triggers/new_successful_collection_via_respaid.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var newSuccessfulCollectionViaRespaid = (0, import_pieces_framework15.createTrigger)({
  name: "new_successful_collection_via_respaid",
  displayName: "New Successful Collection via Respaid",
  description: "Triggers when a debt is paid online via Respaid's payment link.",
  aiMetadata: {
    description: "Fires when a debt is paid in full online through Respaid's payment link, carrying the debtor/invoice details, amount, payment mode, and timestamp. Use to react to a full settlement collected via Respaid."
  },
  auth: respaidAuth,
  props: {},
  sampleData: {
    unique_identifier: "123",
    name: "John Doe",
    company_name: "Company XYZ",
    email: "john@example.com",
    phone_number: "1234567890",
    invoice_number: "INV123",
    amount: 1e3,
    currency: "usd",
    payment_mode: "one-shot",
    paid_at: "2025-03-02T00:00:00+0000"
  },
  type: import_pieces_framework15.TriggerStrategy.WEBHOOK,
  onEnable: respaidTriggersCommon.onEnable("new_successful_collection_via_respaid"),
  onDisable: respaidTriggersCommon.onDisable("new_successful_collection_via_respaid"),
  async run(context) {
    return [respaidTriggersCommon.getPayload(context)];
  }
});

// ../ap/packages/pieces/community/respaid/src/lib/triggers/index.ts
var respaidTriggers = [
  newCampaignCreation,
  newCancelledCase,
  newDisputedCase,
  newPayout,
  newSuccessfulCollectionPaidToCreditor,
  newSuccessfulInstallmentPaymentViaRespaid,
  newSuccessfulCollectionViaLegalOfficer,
  newSuccessfulPartialPaymentToCreditor,
  newSuccessfulPartialPaymentViaRespaid,
  newSuccessfulCollectionViaRespaid
];

// ../ap/packages/pieces/community/respaid/src/index.ts
var respaid = (0, import_pieces_framework16.createPiece)({
  displayName: "Respaid",
  auth: respaidAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/respaid.jpg",
  authors: [],
  actions: respaidActions,
  triggers: respaidTriggers
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  respaid
});
