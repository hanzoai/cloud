"use strict";
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

// ../ap/packages/pieces/community/paperform/src/index.ts
var index_exports = {};
__export(index_exports, {
  paperform: () => paperform
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework21 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/paperform/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/paperform/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
var paperformCommon = {
  baseUrl: "https://api.paperform.co/v1",
  async apiCall({
    method,
    url,
    body,
    auth,
    headers
  }) {
    let apiKey = typeof auth == "string" ? auth : auth.apiKey, response = await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${this.baseUrl}${url}`,
      headers: __spreadValues({
        Authorization: `Bearer ${apiKey}`,
        "Content-Type": "application/json"
      }, headers),
      body
    });
    if (response.status >= 400)
      throw new Error(`Paperform API error: ${response.status}`);
    return response.body;
  },
  async getSpaces({
    auth,
    search,
    limit = 20,
    skip = 0,
    afterId,
    beforeId,
    beforeDate,
    afterDate,
    sort = "DESC"
  }) {
    let params = new URLSearchParams();
    return search && params.append("search", search), limit && params.append("limit", limit.toString()), skip && params.append("skip", skip.toString()), afterId && params.append("after_id", afterId), beforeId && params.append("before_id", beforeId), beforeDate && params.append("before_date", beforeDate), afterDate && params.append("after_date", afterDate), sort && params.append("sort", sort), this.apiCall({
      method: import_pieces_common.HttpMethod.GET,
      url: `/spaces?${params.toString()}`,
      auth
    });
  },
  async getForms({
    auth,
    search,
    limit = 20,
    skip = 0,
    afterId,
    beforeId,
    beforeDate,
    afterDate,
    sort = "DESC"
  }) {
    let params = new URLSearchParams();
    return search && params.append("search", search), limit && params.append("limit", limit.toString()), skip && params.append("skip", skip.toString()), afterId && params.append("after_id", afterId), beforeId && params.append("before_id", beforeId), beforeDate && params.append("before_date", beforeDate), afterDate && params.append("after_date", afterDate), sort && params.append("sort", sort), this.apiCall({
      method: import_pieces_common.HttpMethod.GET,
      url: `/forms?${params.toString()}`,
      auth
    });
  },
  async getForm({
    formId,
    auth
  }) {
    return this.apiCall({
      method: import_pieces_common.HttpMethod.GET,
      url: `/forms/${formId}`,
      auth
    });
  },
  async getFormFields({
    formSlugOrId,
    auth,
    search
  }) {
    let params = new URLSearchParams();
    return search && params.append("search", search), this.apiCall({
      method: import_pieces_common.HttpMethod.GET,
      url: `/forms/${formSlugOrId}/fields?${params.toString()}`,
      auth
    });
  },
  async getSubmissions({
    formId,
    auth,
    limit = 50,
    skip = 0
  }) {
    let params = new URLSearchParams();
    return limit && params.append("limit", limit.toString()), skip && params.append("skip", skip.toString()), this.apiCall({
      method: import_pieces_common.HttpMethod.GET,
      url: `/forms/${formId}/submissions?${params.toString()}`,
      auth
    });
  },
  async getPartialSubmissions({
    formSlugOrId,
    auth,
    limit = 50,
    skip = 0
  }) {
    let params = new URLSearchParams();
    return limit && params.append("limit", limit.toString()), skip && params.append("skip", skip.toString()), this.apiCall({
      method: import_pieces_common.HttpMethod.GET,
      url: `/forms/${formSlugOrId}/partial-submissions?${params.toString()}`,
      auth
    });
  },
  async getCoupons({
    formSlugOrId,
    auth,
    limit = 50,
    skip = 0
  }) {
    let params = new URLSearchParams();
    return limit && params.append("limit", limit.toString()), skip && params.append("skip", skip.toString()), this.apiCall({
      method: import_pieces_common.HttpMethod.GET,
      url: `/forms/${formSlugOrId}/coupons?${params.toString()}`,
      auth
    });
  },
  async getProducts({
    formSlugOrId,
    auth,
    search,
    limit = 50,
    skip = 0
  }) {
    let params = new URLSearchParams();
    return search && params.append("search", search), limit && params.append("limit", limit.toString()), skip && params.append("skip", skip.toString()), this.apiCall({
      method: import_pieces_common.HttpMethod.GET,
      url: `/forms/${formSlugOrId}/products?${params.toString()}`,
      auth
    });
  },
  async getSubmission({
    submissionId,
    auth
  }) {
    return this.apiCall({
      method: import_pieces_common.HttpMethod.GET,
      url: `/submissions/${submissionId}`,
      auth
    });
  },
  async getPartialSubmission({
    submissionId,
    auth
  }) {
    return this.apiCall({
      method: import_pieces_common.HttpMethod.GET,
      url: `/partial-submissions/${submissionId}`,
      auth
    });
  },
  async createWebhook({
    formId,
    webhookUrl,
    auth,
    eventType
  }) {
    return this.apiCall({
      method: import_pieces_common.HttpMethod.POST,
      url: `/forms/${formId}/webhooks`,
      body: {
        target_url: webhookUrl,
        triggers: [eventType]
      },
      auth
    });
  },
  async deleteWebhook({
    webhookId,
    auth
  }) {
    return this.apiCall({
      method: import_pieces_common.HttpMethod.DELETE,
      url: `/webhooks/${webhookId}`,
      auth
    });
  },
  transformSubmissionData(formFields, submissionFields) {
    var _a;
    let fieldMap = formFields.reduce((acc, field) => (acc[field.key] = field.title, acc), {}), transformedFields = {};
    for (let [key, value] of Object.entries(submissionFields)) {
      let label = (_a = fieldMap[key]) != null ? _a : key;
      transformedFields[label] = value;
    }
    return transformedFields;
  }
};

// ../ap/packages/pieces/community/paperform/src/lib/common/auth.ts
var paperformAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Your Paperform API key. You can find this in your Paperform dashboard under Settings > Developer.",
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await paperformCommon.apiCall({
        method: import_pieces_common2.HttpMethod.GET,
        url: "/forms",
        auth: { apiKey: auth }
      }), {
        valid: !0
      };
    } catch (error) {
      return error.message.includes("401") || error.message.includes("403") ? {
        valid: !1,
        error: "Invalid API key. Please check your API key and try again."
      } : {
        valid: !1,
        error: `Authentication failed: ${error.message}. Please verify your API key is correct.`
      };
    }
  }
});

// ../ap/packages/pieces/community/paperform/src/lib/triggers/new-partial-form-submission.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/paperform/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var paperformCommonProps = {
  spaceId: import_pieces_framework2.Property.Dropdown({
    auth: paperformAuth,
    displayName: "Space",
    required: !0,
    refreshers: ["auth"],
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          placeholder: "Please connect your account first.",
          options: []
        };
      try {
        return {
          disabled: !1,
          options: (await paperformCommon.getSpaces({
            auth: auth.secret_text,
            limit: 100
          })).results.spaces.map((space) => ({
            label: space.name,
            value: space.id
          }))
        };
      } catch (error) {
        return {
          disabled: !0,
          placeholder: "Error loading spaces.",
          options: []
        };
      }
    }
  }),
  formId: import_pieces_framework2.Property.Dropdown({
    auth: paperformAuth,
    displayName: "Form",
    required: !0,
    refreshers: ["auth"],
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          placeholder: "Please connect your account first.",
          options: []
        };
      try {
        return {
          disabled: !1,
          options: (await paperformCommon.getForms({
            auth: auth.secret_text,
            limit: 100
          })).results.forms.map((form) => ({
            label: form.title,
            value: form.id
          }))
        };
      } catch (error) {
        return {
          disabled: !0,
          placeholder: "Error loading forms.",
          options: []
        };
      }
    }
  }),
  productFieldKey: import_pieces_framework2.Property.Dropdown({
    auth: paperformAuth,
    displayName: "Product Field",
    required: !0,
    refreshers: ["auth", "formId"],
    options: async ({ auth, formId }) => {
      if (!auth || !formId)
        return {
          disabled: !0,
          placeholder: "Please select a form first",
          options: []
        };
      try {
        let productFields = (await paperformCommon.getFormFields({
          formSlugOrId: formId,
          auth: auth.secret_text
        })).results.fields.filter(
          (field) => field.type === "products"
        );
        return productFields.length === 0 ? {
          disabled: !0,
          placeholder: "No product fields found on this form",
          options: []
        } : {
          disabled: !1,
          options: productFields.map((field) => ({
            label: `${field.title} (${field.key})`,
            value: field.key
          }))
        };
      } catch (error) {
        return {
          disabled: !0,
          placeholder: "Error loading form fields",
          options: []
        };
      }
    }
  }),
  productSku: import_pieces_framework2.Property.Dropdown({
    auth: paperformAuth,
    displayName: "Product",
    required: !0,
    refreshers: ["auth", "formId"],
    options: async ({ auth, formId }) => {
      if (!auth || !formId)
        return {
          disabled: !0,
          placeholder: "Please select a form first.",
          options: []
        };
      try {
        return {
          disabled: !1,
          options: (await paperformCommon.getProducts({
            formSlugOrId: formId,
            auth: auth.secret_text,
            limit: 100
          })).results.products.map(
            (product) => ({
              label: (0, import_pieces_framework3.isNil)(product.name) || product.name === "" ? product.SKU : product.name,
              value: product.SKU
            })
          )
        };
      } catch (error) {
        return {
          disabled: !0,
          placeholder: "Error loading products.",
          options: []
        };
      }
    }
  }),
  couponCode: import_pieces_framework2.Property.Dropdown({
    auth: paperformAuth,
    displayName: "Coupon",
    required: !0,
    refreshers: ["auth", "formId"],
    options: async ({ auth, formId }) => {
      if (!auth || !formId)
        return {
          disabled: !0,
          placeholder: "Please select a form first",
          options: []
        };
      try {
        return {
          disabled: !1,
          options: (await paperformCommon.getCoupons({
            formSlugOrId: formId,
            auth: auth.secret_text,
            limit: 100
          })).results.coupons.map((coupon) => ({
            label: `${coupon.code} - ${coupon.enabled ? "Enabled" : "Disabled"}`,
            value: coupon.code
          }))
        };
      } catch (error) {
        return {
          disabled: !0,
          placeholder: "Error loading coupons",
          options: []
        };
      }
    }
  })
};

// ../ap/packages/pieces/community/paperform/src/lib/triggers/new-partial-form-submission.ts
var TRIGGER_KEY = "new_partial_form_submission", newPartialFormSubmission = (0, import_pieces_framework4.createTrigger)({
  auth: paperformAuth,
  name: "new_partial_form_submission",
  displayName: "New Partial Form Submission",
  description: "Triggers when a partial/in-progress submission is received.",
  aiMetadata: {
    description: "Fires when a respondent saves or abandons an in-progress (not yet completed) submission on the selected Paperform form. The event represents a partial submission and may carry empty or incomplete field data."
  },
  props: {
    formId: paperformCommonProps.formId
  },
  type: import_pieces_framework4.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let formId = context.propsValue.formId, response = await paperformCommon.createWebhook({
      formId,
      webhookUrl: context.webhookUrl,
      auth: context.auth.secret_text,
      eventType: "partial_submission"
    });
    await context.store.put(TRIGGER_KEY, response.results.webhook.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(TRIGGER_KEY);
    webhookId && await paperformCommon.deleteWebhook({
      webhookId,
      auth: context.auth.secret_text
    });
  },
  async run(context) {
    let { formId } = context.propsValue, payload = context.payload.body, submission = (await paperformCommon.getPartialSubmission({
      submissionId: payload.submission_id,
      auth: context.auth.secret_text
    })).results["partial-submission"], fields = await paperformCommon.getFormFields({
      formSlugOrId: formId,
      auth: context.auth.secret_text
    }), transformedFields = Array.isArray(submission.data) && submission.data.length === 0 ? {} : paperformCommon.transformSubmissionData(
      fields.results.fields,
      submission.data
    );
    return [
      __spreadProps(__spreadValues({}, submission), {
        data: transformedFields
      })
    ];
  },
  async test(context) {
    let { formId } = context.propsValue, response = await paperformCommon.getPartialSubmissions({
      formSlugOrId: formId,
      auth: context.auth.secret_text,
      limit: 10
    });
    if (response.results["partial-submissions"].length === 0) return [];
    let fields = await paperformCommon.getFormFields({
      formSlugOrId: formId,
      auth: context.auth.secret_text
    });
    return response.results["partial-submissions"].map((submission) => {
      if (Array.isArray(submission.data) && submission.data.length === 0)
        return __spreadProps(__spreadValues({}, submission), {
          data: {}
        });
      let transformedFields = paperformCommon.transformSubmissionData(
        fields.results.fields,
        submission.data
      );
      return __spreadProps(__spreadValues({}, submission), {
        data: transformedFields
      });
    });
  },
  sampleData: {}
});

// ../ap/packages/pieces/community/paperform/src/lib/triggers/new-form-submission-.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var TRIGGER_KEY2 = "new_form_submission", newFormSubmission = (0, import_pieces_framework5.createTrigger)({
  auth: paperformAuth,
  name: "new_form_submission",
  displayName: "New Form Submission",
  description: "Triggers when a completed form submission is received.",
  aiMetadata: {
    description: "Fires when a respondent completes and submits the selected Paperform form. The event represents a finished submission, with its field answers resolved into named values."
  },
  props: {
    formId: paperformCommonProps.formId
  },
  type: import_pieces_framework5.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let formId = context.propsValue.formId, response = await paperformCommon.createWebhook({
      formId,
      webhookUrl: context.webhookUrl,
      auth: context.auth.secret_text,
      eventType: "submission"
    });
    await context.store.put(TRIGGER_KEY2, response.results.webhook.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(TRIGGER_KEY2);
    webhookId && await paperformCommon.deleteWebhook({
      webhookId,
      auth: context.auth.secret_text
    });
  },
  async run(context) {
    let { formId } = context.propsValue, payload = context.payload.body, submission = (await paperformCommon.getSubmission({
      submissionId: payload.submission_id,
      auth: context.auth.secret_text
    })).results.submission, fields = await paperformCommon.getFormFields({
      formSlugOrId: formId,
      auth: context.auth.secret_text
    }), transformedFields = paperformCommon.transformSubmissionData(
      fields.results.fields,
      submission.data
    );
    return [
      __spreadProps(__spreadValues({}, submission), {
        data: transformedFields
      })
    ];
  },
  async test(context) {
    let { formId } = context.propsValue, response = await paperformCommon.getSubmissions({
      formId,
      auth: context.auth.secret_text,
      limit: 10
    });
    if (response.results.submissions.length === 0) return [];
    let fields = await paperformCommon.getFormFields({
      formSlugOrId: formId,
      auth: context.auth.secret_text
    });
    return response.results.submissions.map((submission) => {
      let transformedFields = paperformCommon.transformSubmissionData(
        fields.results.fields,
        submission.data
      );
      return __spreadProps(__spreadValues({}, submission), {
        data: transformedFields
      });
    });
  },
  sampleData: {}
});

// ../ap/packages/pieces/community/paperform/src/lib/actions/delete-form-submission.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common");
var deleteFormSubmission = (0, import_pieces_framework6.createAction)({
  auth: paperformAuth,
  name: "deleteFormSubmission",
  displayName: "Delete Form Submission",
  description: "Deletes a completed submission by its ID.",
  audience: "both",
  aiMetadata: { description: "Permanently deletes a completed Paperform submission, identified by its submission ID, from the given form. Use to remove a finished response; the deletion is destructive and not idempotent since the record no longer exists on repeat calls.", idempotent: !1 },
  props: {
    formId: paperformCommonProps.formId,
    submissionId: import_pieces_framework6.Property.ShortText({
      displayName: "Form Submission ID",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let { submissionId } = propsValue;
    try {
      return await paperformCommon.apiCall({
        method: import_pieces_common3.HttpMethod.DELETE,
        url: `/submissions/${submissionId}`,
        auth: auth.secret_text
      }), {
        success: !0,
        message: `Submission with ID ${submissionId} has been successfully deleted.`
      };
    } catch (error) {
      throw new Error(`Failed to delete submission: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/paperform/src/lib/actions/delete-partial-form-submission.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common");
var deletePartialFormSubmission = (0, import_pieces_framework7.createAction)({
  auth: paperformAuth,
  name: "deletePartialFormSubmission",
  displayName: "Delete Partial Form Submission",
  description: "Deletes a partial/in-progress submission by its ID.",
  audience: "both",
  aiMetadata: { description: "Permanently deletes a partial (in-progress, not yet completed) Paperform submission by its ID from the given form. Use to discard an abandoned draft response; this is destructive and not idempotent.", idempotent: !1 },
  props: {
    formId: paperformCommonProps.formId,
    partialSubmissionId: import_pieces_framework7.Property.Dropdown({
      auth: paperformAuth,
      displayName: "Partial Submission ID",
      required: !0,
      refreshers: ["auth", "formId"],
      options: async ({ auth, formId }) => {
        if (!auth || !formId)
          return {
            disabled: !0,
            placeholder: "Please select a form first",
            options: []
          };
        try {
          return {
            disabled: !1,
            options: (await paperformCommon.getPartialSubmissions({
              formSlugOrId: formId,
              auth: auth.secret_text,
              limit: 100
            })).results["partial-submissions"].map((partialSubmission) => ({
              label: `${partialSubmission.id}`,
              value: partialSubmission.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            placeholder: "Error loading partial submissions",
            options: []
          };
        }
      }
    })
  },
  async run({ auth, propsValue }) {
    let { partialSubmissionId } = propsValue;
    try {
      return await paperformCommon.apiCall({
        method: import_pieces_common4.HttpMethod.DELETE,
        url: `/partial-submissions/${partialSubmissionId}`,
        auth: auth.secret_text
      }), {
        success: !0,
        message: `Partial submission with ID ${partialSubmissionId} has been successfully deleted.`
      };
    } catch (error) {
      throw new Error(`Failed to delete partial submission: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/paperform/src/lib/actions/create-form-coupon.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common");
var createFormCoupon = (0, import_pieces_framework8.createAction)({
  auth: paperformAuth,
  name: "createFormCoupon",
  displayName: "Create Form Coupon",
  description: "Creates a new discount coupon to a specified form.",
  audience: "both",
  aiMetadata: { description: "Creates a new discount coupon on a specified Paperform form, configured as either a fixed amount or a percentage discount (the discount type prop selects the mode and the matching amount/percentage is required). Use to add a promotional code to a form; each call creates a separate coupon, so it is not idempotent.", idempotent: !1 },
  props: {
    formId: paperformCommonProps.formId,
    code: import_pieces_framework8.Property.ShortText({
      displayName: "Coupon Code",
      required: !0
    }),
    enabled: import_pieces_framework8.Property.Checkbox({
      displayName: "Enabled",
      description: "Whether the coupon is enabled or not.",
      required: !1,
      defaultValue: !0
    }),
    target: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Target",
      description: "The target of the coupon.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Price", value: "price" },
          { label: "Subscription", value: "subscription" }
        ]
      }
    }),
    discountType: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Discount Type",
      description: "Choose between amount or percentage discount.",
      required: !0,
      options: {
        disabled: !1,
        options: [
          { label: "Amount", value: "amount" },
          { label: "Percentage", value: "percentage" }
        ]
      }
    }),
    discountAmount: import_pieces_framework8.Property.Number({
      displayName: "Discount Amount",
      required: !1
    }),
    discountPercentage: import_pieces_framework8.Property.Number({
      displayName: "Discount Percentage",
      required: !1
    }),
    expiresAt: import_pieces_framework8.Property.DateTime({
      displayName: "Expires At",
      description: "The date and time when the coupon expires.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let {
      formId,
      code,
      enabled,
      target,
      discountType,
      discountAmount,
      discountPercentage,
      expiresAt
    } = propsValue;
    if (discountType === "amount" && (!discountAmount || discountAmount < 0))
      throw new Error(
        "Discount amount is required and must be \u2265 0 when discount type is amount"
      );
    if (discountType === "percentage" && (!discountPercentage || discountPercentage < 0 || discountPercentage > 100))
      throw new Error(
        "Discount percentage is required and must be between 0 and 100 when discount type is percentage"
      );
    let requestBody = {
      code,
      enabled: enabled != null ? enabled : !0
    };
    target && (requestBody.target = target), discountType === "amount" && discountAmount && (requestBody.discountAmount = discountAmount), discountType === "percentage" && discountPercentage && (requestBody.discountPercentage = discountPercentage), expiresAt && (requestBody.expiresAt = expiresAt);
    try {
      return (await paperformCommon.apiCall({
        method: import_pieces_common5.HttpMethod.POST,
        url: `/forms/${formId}/coupons`,
        body: requestBody,
        auth: auth.secret_text
      })).results.coupon;
    } catch (error) {
      throw new Error(`Failed to create coupon: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/paperform/src/lib/actions/update-form-coupon.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common");
var updateFormCoupon = (0, import_pieces_framework9.createAction)({
  auth: paperformAuth,
  name: "updateFormCoupon",
  displayName: "Update Form Coupon",
  description: "Updates an existing form coupon.",
  audience: "both",
  aiMetadata: { description: "Updates an existing discount coupon on a Paperform form, identified by its coupon code, modifying only the supplied fields (enabled state, target, discount amount or percentage, expiry). Use to change coupon terms; repeating with the same input yields the same end state, so it is idempotent.", idempotent: !0 },
  props: {
    formId: paperformCommonProps.formId,
    couponCode: paperformCommonProps.couponCode,
    enabled: import_pieces_framework9.Property.Checkbox({
      displayName: "Enabled",
      description: "Whether the coupon is enabled or not",
      required: !1
    }),
    target: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Target",
      description: "The target of the coupon.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Price", value: "price" },
          { label: "Subscription", value: "subscription" }
        ]
      }
    }),
    discountType: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Discount Type",
      description: "Choose between amount or percentage discount.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Amount", value: "amount" },
          { label: "Percentage", value: "percentage" }
        ]
      }
    }),
    discountAmount: import_pieces_framework9.Property.Number({
      displayName: "Discount Amount",
      required: !1
    }),
    discountPercentage: import_pieces_framework9.Property.Number({
      displayName: "Discount Percentage",
      required: !1
    }),
    expiresAt: import_pieces_framework9.Property.DateTime({
      displayName: "Expires At",
      description: "The date and time when the coupon expires.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let {
      formId,
      couponCode,
      enabled,
      target,
      discountType,
      discountAmount,
      discountPercentage,
      expiresAt
    } = propsValue;
    if (discountType === "amount" && discountAmount !== void 0 && discountAmount < 0)
      throw new Error(
        "Discount amount must be \u2265 0 when discount type is amount"
      );
    if (discountType === "percentage" && discountPercentage !== void 0 && (discountPercentage < 0 || discountPercentage > 100))
      throw new Error(
        "Discount percentage must be between 0 and 100 when discount type is percentage"
      );
    let requestBody = {};
    enabled !== void 0 && (requestBody.enabled = enabled), target && (requestBody.target = target), discountType === "amount" && discountAmount !== void 0 && (requestBody.discountAmount = discountAmount), discountType === "percentage" && discountPercentage !== void 0 && (requestBody.discountPercentage = discountPercentage), expiresAt && (requestBody.expiresAt = expiresAt);
    try {
      return (await paperformCommon.apiCall({
        method: import_pieces_common6.HttpMethod.PUT,
        url: `/forms/${formId}/coupons/${couponCode}`,
        body: requestBody,
        auth: auth.secret_text
      })).results.coupon;
    } catch (error) {
      throw new Error(`Failed to update coupon: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/paperform/src/lib/actions/delete-form-coupon.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common");
var deleteFormCoupon = (0, import_pieces_framework10.createAction)({
  auth: paperformAuth,
  name: "deleteFormCoupon",
  displayName: "Delete Form Coupon",
  description: "Deletes a coupon from a form.",
  audience: "both",
  aiMetadata: { description: "Permanently deletes a discount coupon, identified by its coupon code, from the specified Paperform form. Use to remove a promotional code; this is destructive and not idempotent.", idempotent: !1 },
  props: {
    formId: paperformCommonProps.formId,
    couponCode: paperformCommonProps.couponCode
  },
  async run({ auth, propsValue }) {
    let { formId, couponCode } = propsValue;
    try {
      return await paperformCommon.apiCall({
        method: import_pieces_common7.HttpMethod.DELETE,
        url: `/forms/${formId}/coupons/${couponCode}`,
        auth: auth.secret_text
      }), {
        success: !0,
        message: `Coupon "${couponCode}" has been successfully deleted.`
      };
    } catch (error) {
      throw new Error(`Failed to delete coupon: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/paperform/src/lib/actions/create-form-product-.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var import_pieces_common8 = require("@activepieces/pieces-common");
var import_pieces_framework12 = require("@activepieces/pieces-framework"), createFormProduct = (0, import_pieces_framework11.createAction)({
  auth: paperformAuth,
  name: "createFormProduct",
  displayName: "Create Form Product",
  description: "Creates a form product.",
  audience: "both",
  aiMetadata: { description: "Creates a new purchasable product (name, SKU, price, optional quantity/min/max and image) on a product field of a specified Paperform form. Use to add an item to a form-based store; each call creates a separate product, so it is not idempotent.", idempotent: !1 },
  props: {
    formId: paperformCommonProps.formId,
    productFieldKey: paperformCommonProps.productFieldKey,
    name: import_pieces_framework11.Property.ShortText({
      displayName: "Product Name",
      required: !0
    }),
    sku: import_pieces_framework11.Property.ShortText({
      displayName: "Product SKU",
      required: !0
    }),
    price: import_pieces_framework11.Property.Number({
      displayName: "Product Price",
      required: !0
    }),
    quantity: import_pieces_framework11.Property.Number({
      displayName: "Quantity",
      description: "Product quantity",
      required: !1
    }),
    minimum: import_pieces_framework11.Property.Number({
      displayName: "Minimum Quantity",
      required: !1
    }),
    maximum: import_pieces_framework11.Property.Number({
      displayName: "Maximum Quantity",
      required: !1
    }),
    discountable: import_pieces_framework11.Property.Checkbox({
      displayName: "Discountable ?",
      required: !1,
      defaultValue: !1
    }),
    imageUrl: import_pieces_framework11.Property.ShortText({
      displayName: "Image URL",
      description: "URL of the product image",
      required: !1
    }),
    imageWidth: import_pieces_framework11.Property.Number({
      displayName: "Image Width",
      description: "Width of the product image in pixels",
      required: !1
    }),
    imageHeight: import_pieces_framework11.Property.Number({
      displayName: "Image Height",
      description: "Height of the product image in pixels (optional)",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let {
      formId,
      productFieldKey,
      name,
      sku,
      price,
      quantity,
      minimum,
      maximum,
      discountable,
      imageUrl,
      imageWidth,
      imageHeight
    } = propsValue;
    if (!price || price < 0)
      throw new Error("Product price is required and must be \u2265 0");
    if (quantity !== void 0 && quantity < 0)
      throw new Error("Quantity must be \u2265 0");
    if (minimum !== void 0 && minimum < 0)
      throw new Error("Minimum quantity must be \u2265 0");
    if (maximum !== void 0 && maximum < 0)
      throw new Error("Maximum quantity must be \u2265 0");
    if (minimum !== void 0 && maximum !== void 0 && minimum > maximum)
      throw new Error("Minimum quantity cannot be greater than maximum quantity");
    if (imageUrl && (0, import_pieces_framework12.isNil)(imageWidth))
      throw new Error("Provide Image Width.");
    let requestBody = {
      product_field_key: productFieldKey,
      name,
      price,
      SKU: sku
    };
    if (quantity !== void 0 && (requestBody.quantity = quantity), minimum !== void 0 && (requestBody.minimum = minimum), maximum !== void 0 && (requestBody.maximum = maximum), discountable !== void 0 && (requestBody.discountable = discountable), imageUrl && imageWidth) {
      let image = {
        url: imageUrl,
        width: imageWidth
      };
      imageHeight && (image.height = imageHeight), requestBody.images = [image];
    }
    try {
      return (await paperformCommon.apiCall({
        method: import_pieces_common8.HttpMethod.POST,
        url: `/forms/${formId}/products`,
        body: requestBody,
        auth: auth.secret_text
      })).results.product;
    } catch (error) {
      throw new Error(`Failed to create product: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/paperform/src/lib/actions/update-form-product.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var import_pieces_common9 = require("@activepieces/pieces-common");
var import_pieces_framework14 = require("@activepieces/pieces-framework"), updateFormProduct = (0, import_pieces_framework13.createAction)({
  auth: paperformAuth,
  name: "updateFormProduct",
  displayName: "Update Form Product",
  description: "Updates an existing form product.",
  audience: "both",
  aiMetadata: { description: "Updates an existing product on a Paperform form, identified by its SKU, changing only the supplied fields (name, price, quantity, min/max, discountable, image). Use to modify a store item; repeating with the same input yields the same end state, so it is idempotent.", idempotent: !0 },
  props: {
    formId: paperformCommonProps.formId,
    productSku: paperformCommonProps.productSku,
    name: import_pieces_framework13.Property.ShortText({
      displayName: "Product Name",
      required: !1
    }),
    price: import_pieces_framework13.Property.Number({
      displayName: "Product Price",
      required: !1
    }),
    quantity: import_pieces_framework13.Property.Number({
      displayName: "Quantity",
      description: "Product quantity",
      required: !1
    }),
    minimum: import_pieces_framework13.Property.Number({
      displayName: "Minimum Quantity",
      required: !1
    }),
    maximum: import_pieces_framework13.Property.Number({
      displayName: "Maximum Quantity",
      required: !1
    }),
    discountable: import_pieces_framework13.Property.Checkbox({
      displayName: "Discountable ?",
      required: !1
    }),
    imageUrl: import_pieces_framework13.Property.ShortText({
      displayName: "Image URL",
      description: "URL of the product image",
      required: !1
    }),
    imageWidth: import_pieces_framework13.Property.Number({
      displayName: "Image Width",
      description: "Width of the product image in pixels.",
      required: !1
    }),
    imageHeight: import_pieces_framework13.Property.Number({
      displayName: "Image Height",
      description: "Height of the product image in pixels (optional).",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let {
      formId,
      productSku,
      name,
      price,
      quantity,
      minimum,
      maximum,
      discountable,
      imageUrl,
      imageWidth,
      imageHeight
    } = propsValue;
    if (price !== void 0 && price < 0)
      throw new Error("Product price must be \u2265 0");
    if (quantity !== void 0 && quantity < 0)
      throw new Error("Quantity must be \u2265 0");
    if (minimum !== void 0 && minimum < 0)
      throw new Error("Minimum quantity must be \u2265 0");
    if (maximum !== void 0 && maximum < 0)
      throw new Error("Maximum quantity must be \u2265 0");
    if (minimum !== void 0 && maximum !== void 0 && minimum > maximum)
      throw new Error("Minimum quantity cannot be greater than maximum quantity");
    if (imageUrl && (0, import_pieces_framework14.isNil)(imageWidth))
      throw new Error("Provide Image Width.");
    let requestBody = {};
    if (name !== void 0 && (requestBody.name = name), price !== void 0 && (requestBody.price = price), quantity !== void 0 && (requestBody.quantity = quantity), minimum !== void 0 && (requestBody.minimum = minimum), maximum !== void 0 && (requestBody.maximum = maximum), discountable !== void 0 && (requestBody.discountable = discountable), imageUrl && imageWidth) {
      let image = {
        url: imageUrl,
        width: imageWidth
      };
      imageHeight && (image.height = imageHeight), requestBody.images = [image];
    }
    try {
      return (await paperformCommon.apiCall({
        method: import_pieces_common9.HttpMethod.PUT,
        url: `/forms/${formId}/products/${productSku}`,
        body: requestBody,
        auth: auth.secret_text
      })).results.product;
    } catch (error) {
      throw new Error(`Failed to update product: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/paperform/src/lib/actions/delete-form-product.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var import_pieces_common10 = require("@activepieces/pieces-common");
var deleteFormProduct = (0, import_pieces_framework15.createAction)({
  auth: paperformAuth,
  name: "deleteFormProduct",
  displayName: "Delete Form Product",
  description: "Deletes an existing form product.",
  audience: "both",
  aiMetadata: { description: "Permanently deletes a product, identified by its SKU, from the specified Paperform form. Use to remove a store item; this is destructive and not idempotent.", idempotent: !1 },
  props: {
    formId: paperformCommonProps.formId,
    productSku: paperformCommonProps.productSku
  },
  async run({ auth, propsValue }) {
    let { formId, productSku } = propsValue;
    try {
      return await paperformCommon.apiCall({
        method: import_pieces_common10.HttpMethod.DELETE,
        url: `/forms/${formId}/products/${productSku}`,
        auth: auth.secret_text
      }), {
        success: !0,
        message: `Product "${productSku}" has been successfully deleted.`
      };
    } catch (error) {
      throw new Error(`Failed to delete product: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/paperform/src/lib/actions/create-space.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var import_pieces_common11 = require("@activepieces/pieces-common"), createSpace = (0, import_pieces_framework16.createAction)({
  auth: paperformAuth,
  name: "createSpace",
  displayName: "Create Space",
  description: "Creates a new space.",
  audience: "both",
  aiMetadata: { description: "Creates a new Paperform space (a workspace/folder for organizing forms) with the given name. Use to set up a new container for forms; each call creates a separate space, so it is not idempotent.", idempotent: !1 },
  props: {
    name: import_pieces_framework16.Property.ShortText({
      displayName: "Space Name",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let { name } = propsValue;
    try {
      return (await paperformCommon.apiCall({
        method: import_pieces_common11.HttpMethod.POST,
        url: "/spaces",
        body: {
          name
        },
        auth: auth.secret_text
      })).results.space;
    } catch (error) {
      throw new Error(`Failed to create space: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/paperform/src/lib/actions/update-space.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework");
var import_pieces_common12 = require("@activepieces/pieces-common");
var updateSpace = (0, import_pieces_framework17.createAction)({
  auth: paperformAuth,
  name: "updateSpace",
  displayName: "Update Space",
  description: "Updates an existing space.",
  audience: "both",
  aiMetadata: { description: "Renames an existing Paperform space, identified by its space ID, to the supplied name. Use to update a workspace label; repeating with the same input yields the same end state, so it is idempotent.", idempotent: !0 },
  props: {
    spaceId: paperformCommonProps.spaceId,
    name: import_pieces_framework17.Property.ShortText({
      displayName: "Space Name",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let { spaceId, name } = propsValue;
    try {
      return (await paperformCommon.apiCall({
        method: import_pieces_common12.HttpMethod.PUT,
        url: `/spaces/${spaceId}`,
        body: {
          name
        },
        auth: auth.secret_text
      })).results.space;
    } catch (error) {
      throw new Error(`Failed to update space: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/paperform/src/lib/actions/find-form-product.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework");
var findFormProduct = (0, import_pieces_framework18.createAction)({
  auth: paperformAuth,
  name: "findFormProduct",
  displayName: "Find Form Product",
  description: "Finds a form product by name.",
  audience: "both",
  aiMetadata: { description: "Searches the products of a selected Paperform form by product name and returns matching products. Use to look up a product (e.g. to get its SKU) before updating or deleting it; this is a read-only search and is idempotent.", idempotent: !0 },
  props: {
    formId: import_pieces_framework18.Property.Dropdown({
      auth: paperformAuth,
      displayName: "Form",
      description: "Select the form to search products in",
      required: !0,
      refreshers: ["auth"],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Please connect your account first",
            options: []
          };
        try {
          return {
            disabled: !1,
            options: (await paperformCommon.getForms({
              auth: auth.secret_text,
              limit: 100
            })).results.forms.map((form) => ({
              label: form.title,
              value: form.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            placeholder: "Error loading forms",
            options: []
          };
        }
      }
    }),
    search: import_pieces_framework18.Property.ShortText({
      displayName: "Form Product Name",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let { formId, search } = propsValue;
    try {
      let response = await paperformCommon.getProducts({
        formSlugOrId: formId,
        auth: auth.secret_text,
        search,
        limit: 100
      });
      return {
        found: response.results.products.length > 0,
        data: response.results.products
      };
    } catch (error) {
      throw new Error(`Failed to find products: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/paperform/src/lib/actions/find-form.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework");
var findForm = (0, import_pieces_framework19.createAction)({
  auth: paperformAuth,
  name: "findForm",
  displayName: "Find Form",
  description: "Finds a form by title.",
  audience: "both",
  aiMetadata: { description: "Searches the account's Paperform forms by title and returns matching forms. Use to resolve a form ID from its title before acting on it; this is a read-only search and is idempotent.", idempotent: !0 },
  props: {
    search: import_pieces_framework19.Property.ShortText({
      displayName: "Form Title",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let { search } = propsValue;
    try {
      let response = await paperformCommon.getForms({
        auth: auth.secret_text,
        search,
        limit: 100
      });
      return {
        found: response.results.forms.length > 0,
        data: response.results.forms
      };
    } catch (error) {
      throw new Error(`Failed to find forms: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/paperform/src/lib/actions/find-space.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework");
var findSpace = (0, import_pieces_framework20.createAction)({
  auth: paperformAuth,
  name: "findSpace",
  displayName: "Find Space",
  description: "Finds a space by name.",
  audience: "both",
  aiMetadata: { description: "Searches the account's Paperform spaces by name and returns matching spaces. Use to resolve a space ID from its name before updating it; this is a read-only search and is idempotent.", idempotent: !0 },
  props: {
    search: import_pieces_framework20.Property.ShortText({
      displayName: "Space Name",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let { search } = propsValue;
    try {
      let response = await paperformCommon.getSpaces({
        auth: auth.secret_text,
        search,
        limit: 100
      });
      return {
        found: response.results.spaces.length > 0,
        data: response.results.spaces
      };
    } catch (error) {
      throw new Error(`Failed to find spaces: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/paperform/src/index.ts
var import_pieces_common13 = require("@activepieces/pieces-common");
var import_pieces_framework22 = require("@activepieces/pieces-framework"), paperform = (0, import_pieces_framework21.createPiece)({
  displayName: "Paperform",
  auth: paperformAuth,
  categories: [import_pieces_framework22.PieceCategory.FORMS_AND_SURVEYS],
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/paperform.png",
  authors: ["nuvex-dev"],
  actions: [
    deleteFormSubmission,
    deletePartialFormSubmission,
    createFormCoupon,
    updateFormCoupon,
    deleteFormCoupon,
    createFormProduct,
    updateFormProduct,
    deleteFormProduct,
    createSpace,
    updateSpace,
    findFormProduct,
    findForm,
    findSpace,
    (0, import_pieces_common13.createCustomApiCallAction)({
      auth: paperformAuth,
      baseUrl: () => paperformCommon.baseUrl,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [newFormSubmission, newPartialFormSubmission]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  paperform
});
