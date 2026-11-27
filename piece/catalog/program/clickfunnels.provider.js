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

// ../ap/packages/pieces/community/clickfunnels/src/index.ts
var index_exports = {};
__export(index_exports, {
  clickfunnels: () => clickfunnels
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework17 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/clickfunnels/src/lib/triggers/scheduled-appointment-event-created.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/clickfunnels/src/lib/common/constants.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/clickfunnels/src/lib/common/requests.ts
var import_pieces_common = require("@activepieces/pieces-common");
async function fireHttpRequest({
  method,
  auth,
  path,
  body
}) {
  let BASE_URL = CLICKFUNNELS_BASE_URL(auth.subdomain);
  return await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${BASE_URL}${path}`,
    headers: {
      Accept: "application/json",
      "Content-Type": "application/json",
      Authorization: `Bearer ${auth.apiKey}`
    },
    body
  }).then((res) => res.body);
}
var clickfunnelsApiService = {
  fetchCurrentlyLoggedInUser: async (auth) => await fireHttpRequest({
    method: import_pieces_common.HttpMethod.GET,
    auth,
    path: API_ENDPOINTS.ME
  }),
  fetchTeams: async (auth) => await fireHttpRequest({
    method: import_pieces_common.HttpMethod.GET,
    auth,
    path: API_ENDPOINTS.TEAMS
  }),
  fetchTeam: async (auth, teamId) => await fireHttpRequest({
    method: import_pieces_common.HttpMethod.GET,
    auth,
    path: `${API_ENDPOINTS.TEAMS}/${teamId}`
  }),
  fetchWorkspaces: async (auth, teamId) => await fireHttpRequest({
    method: import_pieces_common.HttpMethod.GET,
    auth,
    path: `${API_ENDPOINTS.TEAMS}/${teamId}${API_ENDPOINTS.WORKSPACES}`
  }),
  fetchContacts: async (auth, workspaceId) => await fireHttpRequest({
    method: import_pieces_common.HttpMethod.GET,
    auth,
    path: `${API_ENDPOINTS.WORKSPACES}/${workspaceId}${API_ENDPOINTS.CONTACTS}`
  }),
  fetchContactByEmailSearch: async (auth, workspaceId, filterQuery) => {
    let filterParam = filterQuery.includes("@") ? `filter[email_address]=${encodeURIComponent(filterQuery)}` : `filter[id]=${encodeURIComponent(filterQuery)}`;
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.GET,
      auth,
      path: `${API_ENDPOINTS.WORKSPACES}/${workspaceId}${API_ENDPOINTS.CONTACTS}?${filterParam}`
    });
  },
  upsertContact: async (auth, workspaceId, payload) => await fireHttpRequest({
    method: import_pieces_common.HttpMethod.POST,
    auth,
    path: `${API_ENDPOINTS.WORKSPACES}/${workspaceId}${API_ENDPOINTS.CONTACTS}/upsert`,
    body: payload
  }),
  fetchTags: async (auth, workspaceId) => await fireHttpRequest({
    method: import_pieces_common.HttpMethod.GET,
    auth,
    path: `${API_ENDPOINTS.WORKSPACES}/${workspaceId}${API_ENDPOINTS.CONTACTS}${API_ENDPOINTS.TAGS}`
  }),
  fetchCourses: async (auth, workspaceId) => await fireHttpRequest({
    method: import_pieces_common.HttpMethod.GET,
    auth,
    path: `${API_ENDPOINTS.WORKSPACES}/${workspaceId}${API_ENDPOINTS.COURSES}`
  }),
  createCourseEnrollment: async (auth, courseId, payload) => await fireHttpRequest({
    method: import_pieces_common.HttpMethod.POST,
    auth,
    path: `${API_ENDPOINTS.COURSES}/${courseId}/enrollments`,
    body: payload
  }),
  fetchAppliedTags: async (auth, contactId) => await fireHttpRequest({
    method: import_pieces_common.HttpMethod.GET,
    auth,
    path: `${API_ENDPOINTS.CONTACTS}/${contactId}/applied_tags`
  }),
  removeAppliedTags: async (auth, tagId) => await fireHttpRequest({
    method: import_pieces_common.HttpMethod.DELETE,
    auth,
    path: `${API_ENDPOINTS.CONTACTS}/applied_tags/${tagId}`
  }),
  fetchPipelines: async (auth, workspaceId) => await fireHttpRequest({
    method: import_pieces_common.HttpMethod.GET,
    auth,
    path: `${API_ENDPOINTS.WORKSPACES}/${workspaceId}${API_ENDPOINTS.PIPELINES}`
  }),
  fetchPipelineStages: async (auth, pipelineId) => await fireHttpRequest({
    method: import_pieces_common.HttpMethod.GET,
    auth,
    path: `${API_ENDPOINTS.PIPELINES}/${pipelineId}/stages`
  }),
  createOpportunity: async (auth, workspaceId, payload) => await fireHttpRequest({
    method: import_pieces_common.HttpMethod.POST,
    auth,
    path: `${API_ENDPOINTS.WORKSPACES}/${workspaceId}/sales/opportunities`,
    body: payload
  }),
  createAppliedTag: async (auth, contactId, payload) => await fireHttpRequest({
    method: import_pieces_common.HttpMethod.POST,
    auth,
    path: `${API_ENDPOINTS.CONTACTS}/${contactId}/applied_tags`,
    body: payload
  }),
  createWebhook: async (auth, workspaceId, payload) => await fireHttpRequest({
    method: import_pieces_common.HttpMethod.POST,
    auth,
    path: `${API_ENDPOINTS.WORKSPACES}/${workspaceId}/webhooks/outgoing/endpoints`,
    body: payload
  }),
  deleteWebhook: async (auth, webhookId) => await fireHttpRequest({
    method: import_pieces_common.HttpMethod.DELETE,
    auth,
    path: `/webhooks/outgoing/endpoints/${webhookId}`
  })
};

// ../ap/packages/pieces/community/clickfunnels/src/lib/common/constants.ts
var CLICKFUNNELS_BASE_URL = (subdomain) => `https://${subdomain}.myclickfunnels.com/api/v2`, API_ENDPOINTS = {
  ME: "/me",
  TEAMS: "/teams",
  WORKSPACES: "/workspaces",
  PIPELINES: "/sales/pipelines",
  CONTACTS: "/contacts",
  COURSES: "/courses",
  TAGS: "/tags"
}, clickfunnelsAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: "Enter your ClickFunnels subdomain and API key.",
  required: !0,
  props: {
    subdomain: import_pieces_framework.Property.ShortText({
      displayName: "Subdomain",
      description: 'Your ClickFunnels subdomain (e.g., if your URL is https://mycompany.myclickfunnels.com, enter "mycompany").',
      required: !0
    }),
    apiKey: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Key",
      description: "Your ClickFunnels API key. You can find this in your ClickFunnels account settings.",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    try {
      return await clickfunnelsApiService.fetchCurrentlyLoggedInUser(auth).catch((err) => {
        throw new Error("something went wrong. Please check your username and API key and try again.");
      }), {
        valid: !0
      };
    } catch (error) {
      return {
        valid: !1,
        error: `Connection failed: ${error instanceof Error ? error.message : "Unknown error"}`
      };
    }
  }
});

// ../ap/packages/pieces/community/clickfunnels/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var teamsDropdown = (refreshers) => import_pieces_framework2.Property.Dropdown({
  displayName: "Team",
  description: "Select the team",
  required: !0,
  refreshers,
  auth: clickfunnelsAuth,
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your ClickFunnels account first"
      };
    try {
      return {
        options: (await clickfunnelsApiService.fetchTeams(auth.props)).map((team) => ({
          label: team.name,
          value: team.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load teams. Please check your authentication."
      };
    }
  }
}), coursesDropdown = (refreshers) => import_pieces_framework2.Property.Dropdown({
  auth: clickfunnelsAuth,
  displayName: "Course",
  description: "Select a course",
  required: !0,
  refreshers,
  options: async ({ auth, workspaceId }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your ClickFunnels account first"
      };
    if (!workspaceId)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please select a workspace first"
      };
    try {
      return {
        options: (await clickfunnelsApiService.fetchCourses(
          auth.props,
          workspaceId
        )).map((course) => ({
          label: course.title,
          value: course.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load teams. Please check your authentication."
      };
    }
  }
}), teamMembershipsDropdown = (refreshers, required = !0) => import_pieces_framework2.Property.Dropdown({
  auth: clickfunnelsAuth,
  displayName: "Assignee",
  description: "Select an assignee on your team",
  required,
  refreshers,
  options: async ({ auth, teamId }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your ClickFunnels account first"
      };
    if (!teamId)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please select a team first"
      };
    try {
      let response = await clickfunnelsApiService.fetchTeam(auth.props, teamId);
      return {
        options: [
          { label: "Leave Unassigned", value: null },
          ...response.memberships
        ].map((membership) => membership.label ? {
          label: membership.label,
          value: membership.value
        } : {
          label: `${membership.user.first_name} ${membership.user.last_name}`,
          value: membership.id
        })
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load team members. Please check your authentication."
      };
    }
  }
}), workspacesDropdown = (refreshers) => import_pieces_framework2.Property.Dropdown({
  auth: clickfunnelsAuth,
  displayName: "Workspace",
  description: "Select the workspace",
  required: !0,
  refreshers,
  options: async ({ auth, teamId }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your ClickFunnels account first"
      };
    if (!teamId)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please select a team first"
      };
    try {
      return {
        options: (await clickfunnelsApiService.fetchWorkspaces(
          auth.props,
          teamId
        )).map((workspace) => ({
          label: workspace.name,
          value: workspace.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load workspaces. Please check your authentication."
      };
    }
  }
}), pipelinesDropdown = (refreshers) => import_pieces_framework2.Property.Dropdown({
  auth: clickfunnelsAuth,
  displayName: "Pipeline",
  description: "Select a pipeline",
  required: !0,
  refreshers,
  options: async ({ auth, workspaceId }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your ClickFunnels account first"
      };
    if (!workspaceId)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please select a workspace first"
      };
    try {
      return {
        options: (await clickfunnelsApiService.fetchPipelines(
          auth.props,
          workspaceId
        )).map((pipeline) => ({
          label: pipeline.name,
          value: pipeline.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load pipelines. Please check your authentication."
      };
    }
  }
}), pipelineStagesDropdown = (refreshers) => import_pieces_framework2.Property.Dropdown({
  auth: clickfunnelsAuth,
  displayName: "Pipeline Stage",
  description: "Select a pipeline stage.",
  required: !0,
  refreshers,
  options: async ({ auth, pipelineId }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your ClickFunnels account first"
      };
    if (!pipelineId)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please select a pipeline first"
      };
    try {
      return {
        options: (await clickfunnelsApiService.fetchPipelineStages(
          auth.props,
          pipelineId
        )).map((pipelineStage) => ({
          label: pipelineStage.name,
          value: pipelineStage.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load pipeline stages. Please check your authentication."
      };
    }
  }
}), contactsDropdown = (refreshers) => import_pieces_framework2.Property.Dropdown({
  auth: clickfunnelsAuth,
  displayName: "Contact",
  description: "Select a contact",
  required: !0,
  refreshers,
  options: async ({ auth, workspaceId }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your ClickFunnels account first"
      };
    if (!workspaceId)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please select a workspace first"
      };
    try {
      return {
        options: (await clickfunnelsApiService.fetchContacts(
          auth.props,
          workspaceId
        )).map((contact) => ({
          label: (() => {
            let firstName = contact.first_name || "", lastName = contact.last_name || "";
            return `${firstName} ${lastName}`.trim() || contact.email_address || contact.phone_number || "Unknown Contact";
          })(),
          value: contact.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load contacts. Please check your authentication."
      };
    }
  }
}), tagsDropdown = (refreshers) => import_pieces_framework2.Property.Dropdown({
  auth: clickfunnelsAuth,
  displayName: "Tag",
  description: "Select a tag to apply",
  required: !0,
  refreshers,
  options: async ({ auth, workspaceId }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your ClickFunnels account first"
      };
    if (!workspaceId)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please select a workspace first"
      };
    try {
      return {
        options: (await clickfunnelsApiService.fetchTags(auth.props, workspaceId)).map((tag) => ({
          label: tag.name,
          value: tag.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load tags. Please check your authentication."
      };
    }
  }
}), multiTagsDropdown = (refreshers) => import_pieces_framework2.Property.MultiSelectDropdown({
  auth: clickfunnelsAuth,
  displayName: "Tag",
  description: "Select tags to apply",
  required: !1,
  refreshers,
  options: async ({ auth, workspaceId }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your ClickFunnels account first"
      };
    if (!workspaceId)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please select a workspace first"
      };
    try {
      return {
        options: (await clickfunnelsApiService.fetchTags(auth.props, workspaceId)).map((tag) => ({
          label: tag.name,
          value: tag.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load tags. Please check your authentication."
      };
    }
  }
}), appliedTagsDropdown = (refreshers) => import_pieces_framework2.Property.Dropdown({
  auth: clickfunnelsAuth,
  displayName: "Tag",
  description: "Select a tag",
  required: !0,
  refreshers,
  options: async ({ auth, contactId }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your ClickFunnels account first"
      };
    if (!contactId)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please select a contact first"
      };
    try {
      return {
        options: (await clickfunnelsApiService.fetchAppliedTags(
          auth.props,
          contactId
        )).map((tag) => ({
          label: tag.tag.name,
          value: tag.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load tags. Please check your authentication."
      };
    }
  }
});

// ../ap/packages/pieces/community/clickfunnels/src/lib/triggers/scheduled-appointment-event-created.ts
var CACHE_KEY = "clickfunnels_scheduled_appointment_event_created_trigger", MODULE_NAME = "Scheduled Appointment Event", scheduledAppointmentEventCreated = (0, import_pieces_framework3.createTrigger)({
  auth: clickfunnelsAuth,
  name: "scheduledAppointmentEventCreated",
  displayName: `${MODULE_NAME} Created`,
  description: "Triggers when a scheduled appointment event is created.",
  aiMetadata: {
    description: "Fires when a scheduled appointment event is created in the selected ClickFunnels workspace, representing a newly booked appointment. Delivers the appointment event payload."
  },
  props: {
    teamId: teamsDropdown(["auth"]),
    workspaceId: workspacesDropdown(["auth", "teamId"])
  },
  sampleData: {},
  type: import_pieces_framework3.TriggerStrategy.WEBHOOK,
  async onEnable({ auth, webhookUrl, propsValue: { workspaceId }, store }) {
    if (!workspaceId)
      throw new Error("Workspace ID is required. Please select a workspace.");
    try {
      let webhookId = (await clickfunnelsApiService.createWebhook(
        auth.props,
        workspaceId,
        {
          webhooks_outgoing_endpoint: {
            url: webhookUrl,
            name: `ActivePieces ${MODULE_NAME} Webhook - ${Date.now()}`,
            event_type_ids: ["appointments/scheduled_event.created"]
          }
        }
      )).id;
      await store.put(CACHE_KEY, { webhookId });
    } catch (error) {
      throw console.error("Failed to create ClickFunnels webhook:", error), new Error(
        `Failed to create webhook: ${error instanceof Error ? error.message : "Unknown error"}`
      );
    }
  },
  async onDisable({ auth, store }) {
    let cachedData = await store.get(CACHE_KEY);
    cachedData && await clickfunnelsApiService.deleteWebhook(auth.props, cachedData.webhookId).then(async () => {
      await store.delete(CACHE_KEY);
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/clickfunnels/src/lib/triggers/course-enrollment-created-for-contact.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var CACHE_KEY2 = "clickfunnels_course_enrollment_created_for_contact_trigger", MODULE_NAME2 = "Course Enrollment Created", courseEnrollmentCreatedForContact = (0, import_pieces_framework4.createTrigger)({
  auth: clickfunnelsAuth,
  name: "courseEnrollmentCreatedForContact",
  displayName: `${MODULE_NAME2} for contact`,
  description: "Triggers when a course enrollment is created for a contact.",
  aiMetadata: {
    description: "Fires when a contact is enrolled into a course in the selected ClickFunnels workspace, representing newly granted course access. Delivers the course enrollment payload."
  },
  props: {
    teamId: teamsDropdown(["auth"]),
    workspaceId: workspacesDropdown(["auth", "teamId"])
  },
  sampleData: {},
  type: import_pieces_framework4.TriggerStrategy.WEBHOOK,
  async onEnable({ auth, webhookUrl, propsValue: { workspaceId }, store }) {
    if (!workspaceId)
      throw new Error("Workspace ID is required. Please select a workspace.");
    try {
      let webhookId = (await clickfunnelsApiService.createWebhook(
        auth.props,
        workspaceId,
        {
          webhooks_outgoing_endpoint: {
            url: webhookUrl,
            name: `ActivePieces ${MODULE_NAME2} Webhook - ${Date.now()}`,
            event_type_ids: ["courses/enrollment.created"]
          }
        }
      )).id;
      await store.put(CACHE_KEY2, { webhookId });
    } catch (error) {
      throw console.error("Failed to create ClickFunnels webhook:", error), new Error(
        `Failed to create webhook: ${error instanceof Error ? error.message : "Unknown error"}`
      );
    }
  },
  async onDisable({ auth, store }) {
    let cachedData = await store.get(CACHE_KEY2);
    cachedData && await clickfunnelsApiService.deleteWebhook(auth.props, cachedData.webhookId).then(async () => {
      await store.delete(CACHE_KEY2);
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/clickfunnels/src/lib/triggers/contact-submitted-form.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var CACHE_KEY3 = "clickfunnels_contact_submitted_form_trigger", MODULE_NAME3 = "Contact Submitted Form", contactSubmittedForm = (0, import_pieces_framework5.createTrigger)({
  auth: clickfunnelsAuth,
  name: "contactSubmittedForm",
  displayName: MODULE_NAME3,
  description: "Triggers each time a contact submits a form (opt-in or order).",
  aiMetadata: {
    description: "Fires each time a contact submits a form (such as an opt-in or order form) in the selected ClickFunnels workspace. Delivers the form submission payload."
  },
  props: {
    teamId: teamsDropdown(["auth"]),
    workspaceId: workspacesDropdown(["auth", "teamId"])
  },
  sampleData: {},
  type: import_pieces_framework5.TriggerStrategy.WEBHOOK,
  async onEnable({ auth, webhookUrl, propsValue: { workspaceId }, store }) {
    if (!workspaceId)
      throw new Error("Workspace ID is required. Please select a workspace.");
    try {
      let webhookId = (await clickfunnelsApiService.createWebhook(
        auth.props,
        workspaceId,
        {
          webhooks_outgoing_endpoint: {
            url: webhookUrl,
            name: `ActivePieces ${MODULE_NAME3} Webhook - ${Date.now()}`,
            event_type_ids: ["form_submission.created"]
          }
        }
      )).id;
      await store.put(CACHE_KEY3, { webhookId });
    } catch (error) {
      throw console.error("Failed to create ClickFunnels webhook:", error), new Error(
        `Failed to create webhook: ${error instanceof Error ? error.message : "Unknown error"}`
      );
    }
  },
  async onDisable({ auth, store }) {
    let cachedData = await store.get(CACHE_KEY3);
    cachedData && await clickfunnelsApiService.deleteWebhook(auth.props, cachedData.webhookId).then(async () => {
      await store.delete(CACHE_KEY3);
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/clickfunnels/src/lib/triggers/one-time-order-paid.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var CACHE_KEY4 = "clickfunnels_onetime_order_paid_trigger", MODULE_NAME4 = "One-Time Order Paid", oneTimeOrderPaid = (0, import_pieces_framework6.createTrigger)({
  auth: clickfunnelsAuth,
  name: "OneTimeOrderPaid",
  displayName: MODULE_NAME4,
  description: "Triggers when a customer pays a one-time order.",
  aiMetadata: {
    description: "Fires when a one-time (non-subscription) order is completed/paid in the selected ClickFunnels workspace, representing a successful single-purchase payment. Delivers the order payload."
  },
  props: {
    teamId: teamsDropdown(["auth"]),
    workspaceId: workspacesDropdown(["auth", "teamId"])
  },
  sampleData: {},
  type: import_pieces_framework6.TriggerStrategy.WEBHOOK,
  async onEnable({ auth, webhookUrl, propsValue: { workspaceId }, store }) {
    if (!workspaceId)
      throw new Error("Workspace ID is required. Please select a workspace.");
    try {
      let webhookId = (await clickfunnelsApiService.createWebhook(
        auth.props,
        workspaceId,
        {
          webhooks_outgoing_endpoint: {
            url: webhookUrl,
            name: `ActivePieces ${MODULE_NAME4} Webhook - ${Date.now()}`,
            event_type_ids: ["one-time-order.completed"]
          }
        }
      )).id;
      await store.put(CACHE_KEY4, { webhookId });
    } catch (error) {
      throw console.error("Failed to create ClickFunnels webhook:", error), new Error(
        `Failed to create webhook: ${error instanceof Error ? error.message : "Unknown error"}`
      );
    }
  },
  async onDisable({ auth, store }) {
    let cachedData = await store.get(CACHE_KEY4);
    cachedData && await clickfunnelsApiService.deleteWebhook(auth.props, cachedData.webhookId).then(async () => {
      await store.delete(CACHE_KEY4);
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/clickfunnels/src/lib/triggers/subscription-invoice-paid.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var CACHE_KEY5 = "clickfunnels_subscription_invoice_paid_trigger", MODULE_NAME5 = "Subscription Invoice Paid", subscriptionInvoicePaid = (0, import_pieces_framework7.createTrigger)({
  auth: clickfunnelsAuth,
  name: "subscriptionInvoicePaid",
  displayName: MODULE_NAME5,
  description: "Triggers when a subscription invoice is paid.",
  aiMetadata: {
    description: "Fires when a subscription invoice is paid in the selected ClickFunnels workspace, representing a successful recurring-billing payment. Delivers the invoice payload."
  },
  props: {
    teamId: teamsDropdown(["auth"]),
    workspaceId: workspacesDropdown(["auth", "teamId"])
  },
  sampleData: {},
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  async onEnable({ auth, webhookUrl, propsValue: { workspaceId }, store }) {
    if (!workspaceId)
      throw new Error("Workspace ID is required. Please select a workspace.");
    try {
      let webhookId = (await clickfunnelsApiService.createWebhook(
        auth.props,
        workspaceId,
        {
          webhooks_outgoing_endpoint: {
            url: webhookUrl,
            name: `ActivePieces ${MODULE_NAME5} Webhook - ${Date.now()}`,
            event_type_ids: ["subscription.invoice.paid"]
          }
        }
      )).id;
      await store.put(CACHE_KEY5, { webhookId });
    } catch (error) {
      throw console.error("Failed to create ClickFunnels webhook:", error), new Error(
        `Failed to create webhook: ${error instanceof Error ? error.message : "Unknown error"}`
      );
    }
  },
  async onDisable({ auth, store }) {
    let cachedData = await store.get(CACHE_KEY5);
    cachedData && await clickfunnelsApiService.deleteWebhook(auth.props, cachedData.webhookId).then(async () => {
      await store.delete(CACHE_KEY5);
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/clickfunnels/src/lib/triggers/contact-completed-course.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var CACHE_KEY6 = "clickfunnels_contact_completed_course_trigger", MODULE_NAME6 = "Contact Completed Course", contactCompletedCourse = (0, import_pieces_framework8.createTrigger)({
  auth: clickfunnelsAuth,
  name: "contactCompletedCourse",
  displayName: MODULE_NAME6,
  description: "Triggers when a contact completes a course.",
  aiMetadata: {
    description: "Fires when a contact completes a course in the selected ClickFunnels workspace, representing the enrollment reaching completed status. Delivers the enrollment payload."
  },
  props: {
    teamId: teamsDropdown(["auth"]),
    workspaceId: workspacesDropdown(["auth", "teamId"])
  },
  sampleData: {},
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  async onEnable({ auth, webhookUrl, propsValue: { workspaceId }, store }) {
    if (!workspaceId)
      throw new Error("Workspace ID is required. Please select a workspace.");
    try {
      let webhookId = (await clickfunnelsApiService.createWebhook(
        auth.props,
        workspaceId,
        {
          webhooks_outgoing_endpoint: {
            url: webhookUrl,
            name: `ActivePieces ${MODULE_NAME6} Webhook - ${Date.now()}`,
            event_type_ids: ["courses/enrollment.course_completed"]
          }
        }
      )).id;
      await store.put(CACHE_KEY6, { webhookId });
    } catch (error) {
      throw console.error("Failed to create ClickFunnels webhook:", error), new Error(
        `Failed to create webhook: ${error instanceof Error ? error.message : "Unknown error"}`
      );
    }
  },
  async onDisable({ auth, store }) {
    let cachedData = await store.get(CACHE_KEY6);
    cachedData && await clickfunnelsApiService.deleteWebhook(auth.props, cachedData.webhookId).then(async () => {
      await store.delete(CACHE_KEY6);
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/clickfunnels/src/lib/triggers/contact-identified.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var CACHE_KEY7 = "clickfunnels_contact_identified_trigger", MODULE_NAME7 = "Contact Identified", contactIdentified = (0, import_pieces_framework9.createTrigger)({
  auth: clickfunnelsAuth,
  name: "contactIdentified",
  displayName: MODULE_NAME7,
  description: "Triggers when a new contact is identified by email/phone.",
  aiMetadata: {
    description: "Fires when a contact is identified (by email or phone) in the selected ClickFunnels workspace, representing a visitor becoming a known contact. Delivers the contact payload."
  },
  props: {
    teamId: teamsDropdown(["auth"]),
    workspaceId: workspacesDropdown(["auth", "teamId"])
  },
  sampleData: {},
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  async onEnable({ auth, webhookUrl, propsValue: { workspaceId }, store }) {
    if (!workspaceId)
      throw new Error("Workspace ID is required. Please select a workspace.");
    try {
      let webhookId = (await clickfunnelsApiService.createWebhook(
        auth.props,
        workspaceId,
        {
          webhooks_outgoing_endpoint: {
            url: webhookUrl,
            name: `ActivePieces ${MODULE_NAME7} Webhook - ${Date.now()}`,
            event_type_ids: ["contact.identified"]
          }
        }
      )).id;
      await store.put(CACHE_KEY7, { webhookId });
    } catch (error) {
      throw console.error("Failed to create ClickFunnels webhook:", error), new Error(
        `Failed to create webhook: ${error instanceof Error ? error.message : "Unknown error"}`
      );
    }
  },
  async onDisable({ auth, store }) {
    let cachedData = await store.get(CACHE_KEY7);
    cachedData && await clickfunnelsApiService.deleteWebhook(auth.props, cachedData.webhookId).then(async () => {
      await store.delete(CACHE_KEY7);
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/clickfunnels/src/lib/triggers/contact-suspended-from-course.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var CACHE_KEY8 = "clickfunnels_contact_suspended_from_course_trigger", MODULE_NAME8 = "Contact Suspended", contactSuspendedFromCourse = (0, import_pieces_framework10.createTrigger)({
  auth: clickfunnelsAuth,
  name: "contactSuspendedFromCourse",
  displayName: `${MODULE_NAME8} From Course`,
  description: "Triggers when a contact is suspended from a course.",
  aiMetadata: {
    description: "Fires when a contact is suspended from a course in the selected ClickFunnels workspace, representing the enrollment being revoked or paused. Delivers the enrollment payload."
  },
  props: {
    teamId: teamsDropdown(["auth"]),
    workspaceId: workspacesDropdown(["auth", "teamId"])
  },
  sampleData: {},
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  async onEnable({ auth, webhookUrl, propsValue: { workspaceId }, store }) {
    if (!workspaceId)
      throw new Error("Workspace ID is required. Please select a workspace.");
    try {
      let webhookId = (await clickfunnelsApiService.createWebhook(
        auth.props,
        workspaceId,
        {
          webhooks_outgoing_endpoint: {
            url: webhookUrl,
            name: `ActivePieces ${MODULE_NAME8} Webhook - ${Date.now()}`,
            event_type_ids: ["courses/enrollment.suspended"]
          }
        }
      )).id;
      await store.put(CACHE_KEY8, { webhookId });
    } catch (error) {
      throw console.error("Failed to create ClickFunnels webhook:", error), new Error(
        `Failed to create webhook: ${error instanceof Error ? error.message : "Unknown error"}`
      );
    }
  },
  async onDisable({ auth, store }) {
    let cachedData = await store.get(CACHE_KEY8);
    cachedData && await clickfunnelsApiService.deleteWebhook(auth.props, cachedData.webhookId).then(async () => {
      await store.delete(CACHE_KEY8);
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/clickfunnels/src/lib/actions/create-opportunity.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var createOpportunity = (0, import_pieces_framework11.createAction)({
  auth: clickfunnelsAuth,
  name: "createOpportunity",
  displayName: "Create Opportunity",
  description: "Create a new opportunity for a contact.",
  audience: "both",
  aiMetadata: {
    description: "Creates a sales opportunity in a ClickFunnels CRM pipeline, attached to a primary contact and placed in a specific pipeline stage. Use to register a new deal/lead in the sales pipeline. Requires a workspace, pipeline stage, and contact; optional value and close date. Not idempotent \u2014 each call creates a separate opportunity.",
    idempotent: !1
  },
  props: {
    name: import_pieces_framework11.Property.ShortText({
      displayName: "Opportunity Name",
      description: "The name or title of the opportunity.",
      required: !0
    }),
    teamId: teamsDropdown(["auth"]),
    workspaceId: workspacesDropdown(["auth", "teamId"]),
    pipelineId: pipelinesDropdown(["auth", "workspaceId"]),
    pipelineStageId: pipelineStagesDropdown(["auth", "pipelineId"]),
    contactId: contactsDropdown(["auth", "workspaceId"]),
    assigneeId: teamMembershipsDropdown(["auth", "teamId"], !1),
    value: import_pieces_framework11.Property.Number({
      displayName: "Value",
      description: "The potential value of this opportunity in the default currency of the workspace",
      required: !1
    }),
    closedAt: import_pieces_framework11.Property.DateTime({
      displayName: "Close Date",
      description: "The expected close date for the opportunity.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let payload = __spreadProps(__spreadValues(__spreadValues({
      name: propsValue.name,
      pipelines_stage_id: propsValue.pipelineStageId,
      primary_contact_id: propsValue.contactId
    }, propsValue.value && {
      value: propsValue.value
    }), propsValue.closedAt && {
      closed_at: new Date(propsValue.closedAt).toISOString()
    }), {
      assignee_id: propsValue.assigneeId
    });
    return await clickfunnelsApiService.createOpportunity(
      auth.props,
      propsValue.workspaceId,
      {
        sales_opportunity: payload
      }
    );
  }
});

// ../ap/packages/pieces/community/clickfunnels/src/lib/actions/apply-tag-to-contact.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var applyTagToContact = (0, import_pieces_framework12.createAction)({
  auth: clickfunnelsAuth,
  name: "applyTagToContact",
  displayName: "Apply Tag to Contact",
  description: "Apply a tag to a contact if it doesn\u2019t already exist.",
  audience: "both",
  aiMetadata: {
    description: "Applies a tag to a ClickFunnels contact for segmentation or automation. Use to label or categorize a contact. Requires the workspace, contact, and tag. Effectively idempotent \u2014 re-applying a tag the contact already has does not add a duplicate.",
    idempotent: !0
  },
  props: {
    teamId: teamsDropdown(["auth"]),
    workspaceId: workspacesDropdown(["auth", "teamId"]),
    contactId: contactsDropdown(["auth", "workspaceId"]),
    tagId: tagsDropdown(["auth", "workspaceId"])
  },
  async run({ auth, propsValue }) {
    let payload = {
      contacts_applied_tag: {
        tag_id: propsValue.tagId
      }
    };
    return await clickfunnelsApiService.createAppliedTag(
      auth.props,
      propsValue.contactId,
      payload
    );
  }
});

// ../ap/packages/pieces/community/clickfunnels/src/lib/actions/remove-tag-from-contact.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var removeTagFromContact = (0, import_pieces_framework13.createAction)({
  auth: clickfunnelsAuth,
  name: "removeTagFromContact",
  displayName: "Remove Tag From Contact",
  description: "Remove a specific tag from a contact.",
  audience: "both",
  aiMetadata: {
    description: "Removes a previously applied tag from a ClickFunnels contact, identified by the applied-tag record on that contact. Use to unlabel or reverse a tag-based segmentation. Idempotent \u2014 once the tag is gone, repeating leaves the contact in the same state.",
    idempotent: !0
  },
  props: {
    teamId: teamsDropdown(["auth"]),
    workspaceId: workspacesDropdown(["auth", "teamId"]),
    contactId: contactsDropdown(["auth", "workspaceId"]),
    tagId: appliedTagsDropdown(["auth", "contactId"])
  },
  async run({ auth, propsValue }) {
    return await clickfunnelsApiService.removeAppliedTags(auth.props, propsValue.tagId), {
      message: "Tag removed"
    };
  }
});

// ../ap/packages/pieces/community/clickfunnels/src/lib/actions/enroll-a-contact-into-a-course.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var enrollAContactIntoACourse = (0, import_pieces_framework14.createAction)({
  auth: clickfunnelsAuth,
  name: "enrollAContactIntoACourse",
  displayName: "Enroll a Contact Into a Course",
  description: "Create an enrollment for a contact in a course.",
  audience: "both",
  aiMetadata: {
    description: "Enrolls a ClickFunnels contact into a specific course, granting them access to its content. Use to give a contact course access programmatically. Requires the workspace, course, and contact. Not idempotent \u2014 each call creates a new enrollment record.",
    idempotent: !1
  },
  props: {
    teamId: teamsDropdown(["auth"]),
    workspaceId: workspacesDropdown(["auth", "teamId"]),
    courseId: coursesDropdown(["auth", "workspaceId"]),
    contactId: contactsDropdown(["auth", "workspaceId"])
  },
  async run({ auth, propsValue }) {
    let payload = {
      courses_enrollment: {
        contact_id: propsValue.contactId
      }
    };
    return await clickfunnelsApiService.createCourseEnrollment(auth.props, propsValue.courseId, payload);
  }
});

// ../ap/packages/pieces/community/clickfunnels/src/lib/actions/update-or-create-contact.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var updateOrCreateContact = (0, import_pieces_framework15.createAction)({
  auth: clickfunnelsAuth,
  name: "updateOrCreateContact",
  displayName: "Update or Create Contact",
  description: "Searches for a contact by email and updates it, or creates a new one if not found.",
  audience: "both",
  aiMetadata: {
    description: "Upserts a ClickFunnels contact keyed on email address: updates the matching contact if one exists, otherwise creates it. Use to add or sync a contact (name, phone, tags, custom attributes) without first checking for a duplicate. Idempotent \u2014 repeating with the same email and fields converges to the same contact rather than creating duplicates. Custom-attribute keys that collide with default contact fields will error.",
    idempotent: !0
  },
  props: {
    teamId: teamsDropdown(["auth"]),
    workspaceId: workspacesDropdown(["auth", "teamId"]),
    emailAddress: import_pieces_framework15.Property.ShortText({
      displayName: "Email",
      description: "The contact's email address. This is used to find and (update/create) the contact.",
      required: !0
    }),
    firstName: import_pieces_framework15.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    lastName: import_pieces_framework15.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    phoneNumber: import_pieces_framework15.Property.ShortText({
      displayName: "Phone Number",
      required: !1
    }),
    tagIds: multiTagsDropdown(["auth", "workspaceId"]),
    customAttributes: import_pieces_framework15.Property.Object({
      displayName: "Custom Attributes",
      description: "A key-value object for custom contact data. Keys that are default properties on the Contact resource or variations of it will result in an error. E.g., first_name, First Name, etc. are not valid inputs.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let payload = {
      contact: {
        email_address: propsValue.emailAddress,
        first_name: propsValue.firstName,
        last_name: propsValue.lastName,
        phone_number: propsValue.phoneNumber,
        tag_ids: propsValue.tagIds,
        custom_attributes: propsValue.customAttributes
      }
    };
    return await clickfunnelsApiService.upsertContact(auth.props, propsValue.workspaceId, payload);
  }
});

// ../ap/packages/pieces/community/clickfunnels/src/lib/actions/search-contacts.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var searchContacts = (0, import_pieces_framework16.createAction)({
  auth: clickfunnelsAuth,
  name: "searchContacts",
  displayName: "Search Contacts",
  description: "Look up contacts by ID or email",
  audience: "both",
  aiMetadata: {
    description: 'Looks up contacts within a ClickFunnels workspace by a single search term, which is auto-detected as an email address (filters on email) when it contains "@", or otherwise as a contact ID. Use to resolve a contact record or its ID before another action. Read-only and idempotent.',
    idempotent: !0
  },
  props: {
    teamId: teamsDropdown(["auth"]),
    workspaceId: workspacesDropdown(["auth", "teamId"]),
    searchTerm: import_pieces_framework16.Property.ShortText({
      displayName: "Search Query",
      description: "Filter contacts by either email or id",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    return await clickfunnelsApiService.fetchContactByEmailSearch(
      auth.props,
      propsValue.workspaceId,
      propsValue.searchTerm
    );
  }
});

// ../ap/packages/pieces/community/clickfunnels/src/index.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common"), clickfunnels = (0, import_pieces_framework17.createPiece)({
  displayName: "ClickFunnels",
  auth: clickfunnelsAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/clickfunnels.png",
  categories: [import_pieces_framework18.PieceCategory.COMMUNICATION, import_pieces_framework18.PieceCategory.MARKETING],
  authors: ["gs03dev"],
  actions: [
    createOpportunity,
    applyTagToContact,
    removeTagFromContact,
    enrollAContactIntoACourse,
    updateOrCreateContact,
    enrollAContactIntoACourse,
    searchContacts,
    (0, import_pieces_common2.createCustomApiCallAction)({
      auth: clickfunnelsAuth,
      baseUrl: (auth) => {
        var _a;
        let authValue = auth == null ? void 0 : auth.props;
        return CLICKFUNNELS_BASE_URL((_a = authValue == null ? void 0 : authValue.subdomain) != null ? _a : "");
      },
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.props.apiKey}`
      })
    })
  ],
  triggers: [
    scheduledAppointmentEventCreated,
    courseEnrollmentCreatedForContact,
    contactSubmittedForm,
    oneTimeOrderPaid,
    subscriptionInvoicePaid,
    contactCompletedCourse,
    contactIdentified,
    contactSuspendedFromCourse
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  clickfunnels
});
