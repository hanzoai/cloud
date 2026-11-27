"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __hasOwnProp = Object.prototype.hasOwnProperty;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value;
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

// ../ap/packages/pieces/community/moxie-crm/src/index.ts
var index_exports = {};
__export(index_exports, {
  moxieCrm: () => moxieCrm
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_framework16 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/moxie-crm/src/lib/actions/create-client.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/moxie-crm/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");
var MoxieCRMClient = class {
  constructor(baseUrl, apiKey) {
    __publicField(this, "baseUrl", baseUrl);
    __publicField(this, "apiKey", apiKey);
    this.baseUrl = baseUrl.replace(/\/$/, "");
  }
  async makeRequest(method, resourceUri, body = void 0, query) {
    return await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${this.baseUrl}${resourceUri}`,
      headers: {
        "X-API-KEY": this.apiKey
      },
      body,
      queryParams: query
    });
  }
  async createContact(request) {
    return (await this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      "/action/contacts/create",
      request
    )).body;
  }
  async createClient(request) {
    return (await this.makeRequest(import_pieces_common.HttpMethod.POST, "/action/clients/create", request)).body;
  }
  async listClients() {
    return (await this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/action/clients/list"
    )).body;
  }
  async listInvoiceTemplates() {
    return (await this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/action/invoiceTemplates/list"
    )).body;
  }
  async createProject(request) {
    return (await this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      "/action/projects/create",
      request
    )).body;
  }
  async createTask(request) {
    return (await this.makeRequest(import_pieces_common.HttpMethod.POST, "/action/tasks/create", request)).body;
  }
  async searchProjects(clientName) {
    return (await this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/action/projects/search",
      void 0,
      { query: clientName }
    )).body;
  }
  async listProjectTaskStages() {
    return (await this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/action/taskStages/list"
    )).body;
  }
  async searchClients(query) {
    return (await this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/action/clients/search",
      void 0,
      { query }
    )).body;
  }
  async searchContacts(query) {
    return (await this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/action/contacts/search",
      void 0,
      (0, import_pieces_framework.isNil)(query) || query.length === 0 ? void 0 : { query }
    )).body;
  }
  async listPipelineStages() {
    return (await this.makeRequest(import_pieces_common.HttpMethod.GET, "/action/pipelineStages/list")).body;
  }
  async listWorkspaceUsers() {
    return (await this.makeRequest(import_pieces_common.HttpMethod.GET, "/action/users/list")).body;
  }
};

// ../ap/packages/pieces/community/moxie-crm/src/lib/common/index.ts
async function makeClient(auth) {
  return new MoxieCRMClient(auth.props.baseUrl, auth.props.apiKey);
}
function reformatDate(s) {
  if (s)
    return s.split("T", 2)[0];
}

// ../ap/packages/pieces/community/moxie-crm/src/lib/auth.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), moxieCRMAuth = import_pieces_framework2.PieceAuth.CustomAuth({
  required: !0,
  description: `
  To obtain your Moxie CRM token, follow these steps:

  1. Log in to your Moxie CRM account and click on **Workspace Settings** (Bottom left).
  2. Click on **Connected Apps** and navigate to **Integrations** tab.
  3. Now, under "Custom Integration", click on **Enable Custom Integration**.
  4. Copy **API Key** and **Base URL** and click on Save button.
  `,
  props: {
    apiKey: import_pieces_framework2.PieceAuth.SecretText({
      displayName: "API Key",
      description: "The API Key of the Moxie CRM account",
      required: !0
    }),
    baseUrl: import_pieces_framework2.Property.ShortText({
      displayName: "Base URL",
      description: "The Base URL of the Moxie CRM account",
      required: !0
    })
  }
});

// ../ap/packages/pieces/community/moxie-crm/src/lib/output-schemas.ts
var contactFields = [
  { key: "id", label: "Contact ID" },
  { key: "firstName", label: "First Name" },
  { key: "lastName", label: "Last Name" },
  { key: "email", label: "Email", format: "email" },
  { key: "phone", label: "Phone" },
  { key: "mobile", label: "Mobile" },
  { key: "role", label: "Role" },
  { key: "clientId", label: "Client ID" },
  { key: "defaultContact", label: "Default Contact", format: "boolean" },
  { key: "invoiceContact", label: "Invoice Contact", format: "boolean" },
  { key: "portalAccess", label: "Portal Access", format: "boolean" },
  { key: "notes", label: "Notes" }
], paymentTermsFields = [
  { key: "paymentDays", label: "Payment Days", format: "number" },
  { key: "latePaymentFee", label: "Late Payment Fee", format: "number" },
  { key: "depositAmount", label: "Deposit Amount", format: "number" },
  { key: "depositType", label: "Deposit Type" },
  { key: "hourlyAmount", label: "Hourly Amount", format: "number" },
  { key: "whoPaysCardFees", label: "Who Pays Card Fees" },
  { key: "updatedDate", label: "Updated Date", format: "datetime" },
  { key: "updatedBy", label: "Updated By" }
], clientFields = [
  { key: "id", label: "Client ID" },
  { key: "name", label: "Name" },
  { key: "clientType", label: "Client Type" },
  { key: "initials", label: "Initials" },
  { key: "address1", label: "Address Line 1" },
  { key: "address2", label: "Address Line 2" },
  { key: "city", label: "City" },
  { key: "locality", label: "State or Region" },
  { key: "postal", label: "Postal Code" },
  { key: "country", label: "Country" },
  { key: "website", label: "Website", format: "url" },
  { key: "phone", label: "Phone" },
  { key: "logo", label: "Logo", format: "image" },
  { key: "color", label: "Colour" },
  { key: "taxId", label: "Tax ID" },
  { key: "leadSource", label: "Lead Source" },
  { key: "archive", label: "Archived", format: "boolean" },
  { key: "hourlyAmount", label: "Hourly Amount", format: "number" },
  { key: "roundingIncrement", label: "Rounding Increment", format: "number" },
  { key: "defaultTaxRate", label: "Default Tax Rate", format: "number" },
  { key: "currency", label: "Currency" },
  { key: "payInstructions", label: "Payment Instructions" },
  { key: "notes", label: "Notes" },
  { key: "stripeClientId", label: "Stripe Customer ID" },
  { key: "created", label: "Created", format: "datetime" },
  { key: "lastInvoiceRunDate", label: "Last Invoice Run Date", format: "date" },
  { key: "nextInvoiceRunDate", label: "Next Invoice Run Date", format: "date" },
  { key: "paymentTerms", label: "Payment Terms", children: paymentTermsFields },
  {
    key: "integrationKeys",
    label: "Integration Keys",
    children: [
      { key: "quickbooksId", label: "QuickBooks ID" },
      { key: "xeroId", label: "Xero ID" }
    ]
  }
], clientMiniFields = [
  { key: "id", label: "Client ID" },
  { key: "name", label: "Name" },
  { key: "clientType", label: "Client Type" },
  { key: "initials", label: "Initials" },
  { key: "address1", label: "Address Line 1" },
  { key: "address2", label: "Address Line 2" },
  { key: "city", label: "City" },
  { key: "locality", label: "State or Region" },
  { key: "postal", label: "Postal Code" },
  { key: "country", label: "Country" },
  { key: "website", label: "Website", format: "url" },
  { key: "phone", label: "Phone" },
  { key: "logo", label: "Logo", format: "image" },
  { key: "color", label: "Colour" },
  { key: "taxId", label: "Tax ID" },
  { key: "leadSource", label: "Lead Source" },
  { key: "archive", label: "Archived", format: "boolean" },
  { key: "hourlyAmount", label: "Hourly Amount", format: "number" },
  { key: "defaultTaxRate", label: "Default Tax Rate", format: "number" },
  { key: "currency", label: "Currency" },
  { key: "whoPaysCardFees", label: "Who Pays Card Fees" }
], feeScheduleFields = [
  { key: "feeType", label: "Fee Type" },
  { key: "amount", label: "Amount", format: "number" },
  { key: "estimateMin", label: "Estimate Minimum", format: "number" },
  { key: "estimateMax", label: "Estimate Maximum", format: "number" },
  { key: "taxable", label: "Taxable", format: "boolean" },
  { key: "retainerSchedule", label: "Retainer Schedule" },
  { key: "retainerTiming", label: "Retainer Timing" },
  { key: "retainerPeriods", label: "Retainer Periods", format: "number" },
  { key: "retainerOverageRate", label: "Retainer Overage Rate", format: "number" },
  { key: "retainerActive", label: "Retainer Active", format: "boolean" },
  { key: "updatedDate", label: "Updated Date", format: "datetime" },
  { key: "updatedBy", label: "Updated By" }
], projectCoreFields = [
  { key: "id", label: "Project ID" },
  { key: "name", label: "Name" },
  { key: "clientId", label: "Client ID" },
  { key: "projectTypeId", label: "Project Type ID" },
  { key: "active", label: "Active", format: "boolean" },
  { key: "startDate", label: "Start Date", format: "date" },
  { key: "dueDate", label: "Due Date", format: "date" },
  { key: "dateCreated", label: "Created", format: "datetime" },
  { key: "hexColor", label: "Colour" },
  { key: "portalAccess", label: "Client Portal Access" },
  { key: "portalAccessAssignedOnly", label: "Portal Access Assigned Only", format: "boolean" },
  { key: "showTimeWorkedInPortal", label: "Show Time Worked In Portal", format: "boolean" },
  { key: "proposalId", label: "Proposal ID" },
  { key: "proposalName", label: "Proposal Name" },
  { key: "feeSchedule", label: "Fee Schedule", children: feeScheduleFields }
], taskFields = [
  { key: "id", label: "Task ID" },
  { key: "name", label: "Name" },
  { key: "clientId", label: "Client ID" },
  { key: "projectId", label: "Project ID" },
  { key: "projectTypeId", label: "Project Type ID" },
  { key: "statusId", label: "Status ID" },
  { key: "description", label: "Description" },
  { key: "descriptionFormat", label: "Description Format" },
  { key: "type", label: "Type" },
  { key: "priority", label: "Priority", format: "number" },
  { key: "taskPriority", label: "Priority Label" },
  { key: "startDate", label: "Start Date", format: "date" },
  { key: "dueDate", label: "Due Date", format: "date" },
  { key: "created", label: "Created", format: "datetime" },
  { key: "completed", label: "Completed", format: "datetime" },
  { key: "archived", label: "Archived", format: "boolean" },
  { key: "approvalRequired", label: "Approval Required", format: "boolean" },
  { key: "approvalRequestedAt", label: "Approval Requested At", format: "datetime" },
  { key: "isSubTask", label: "Is Subtask", format: "boolean" },
  { key: "parentTaskId", label: "Parent Task ID" },
  { key: "subTaskSort", label: "Subtask Sort", format: "number" },
  { key: "kanbanSort", label: "Kanban Sort", format: "number" },
  { key: "ticketId", label: "Ticket ID" },
  { key: "product", label: "Product" },
  { key: "quantity", label: "Quantity", format: "number" },
  { key: "invoiceId", label: "Invoice ID" },
  { key: "invoiceNumber", label: "Invoice Number" },
  { key: "assignedTo", label: "Assigned To" },
  { key: "assignedToList", label: "Assigned User IDs" },
  {
    key: "events",
    label: "Events",
    labelKey: "user",
    listItems: [
      { key: "user", label: "User" },
      { key: "events", label: "Events" },
      { key: "clientEvent", label: "Client Event", format: "boolean" },
      { key: "timestamp", label: "Timestamp", format: "datetime" }
    ]
  }
], triggerProjectFields = [
  ...projectCoreFields,
  { key: "client", label: "Client", children: clientMiniFields }
], triggerTaskFields = [
  { key: "id", label: "Task ID" },
  { key: "name", label: "Name" },
  { key: "clientId", label: "Client ID" },
  { key: "projectId", label: "Project ID" },
  { key: "projectTypeId", label: "Project Type ID" },
  { key: "statusId", label: "Status ID" },
  { key: "status", label: "Status" },
  { key: "description", label: "Description" },
  { key: "descriptionFormat", label: "Description Format" },
  { key: "priority", label: "Priority", format: "number" },
  { key: "taskPriority", label: "Priority Label" },
  { key: "startDate", label: "Start Date", format: "date" },
  { key: "dueDate", label: "Due Date", format: "date" },
  { key: "created", label: "Created", format: "datetime" },
  { key: "completed", label: "Completed", format: "datetime" },
  { key: "archived", label: "Archived", format: "boolean" },
  { key: "approvalRequired", label: "Approval Required", format: "boolean" },
  { key: "isSubTask", label: "Is Subtask", format: "boolean" },
  { key: "parentTaskId", label: "Parent Task ID" },
  { key: "subTaskSort", label: "Subtask Sort", format: "number" },
  { key: "kanbanSort", label: "Kanban Sort", format: "number" },
  { key: "ticketId", label: "Ticket ID" },
  { key: "product", label: "Product" },
  { key: "quantity", label: "Quantity", format: "number" },
  { key: "invoiceId", label: "Invoice ID" },
  { key: "invoiceNumber", label: "Invoice Number" },
  { key: "assignedTo", label: "Assigned To" },
  { key: "assignedToList", label: "Assigned User IDs" },
  {
    key: "project",
    label: "Project",
    children: [
      { key: "id", label: "Project ID" },
      { key: "name", label: "Name" },
      { key: "clientId", label: "Client ID" },
      { key: "projectTypeId", label: "Project Type ID" },
      { key: "active", label: "Active", format: "boolean" },
      { key: "hexColor", label: "Colour" }
    ]
  },
  {
    key: "client",
    label: "Client",
    children: [
      { key: "id", label: "Client ID" },
      { key: "name", label: "Name" },
      { key: "initials", label: "Initials" },
      { key: "logo", label: "Logo", format: "image" },
      { key: "color", label: "Colour" }
    ]
  },
  {
    key: "events",
    label: "Events",
    labelKey: "user",
    listItems: [
      { key: "user", label: "User" },
      { key: "events", label: "Events" },
      { key: "clientEvent", label: "Client Event", format: "boolean" },
      { key: "timestamp", label: "Timestamp", format: "datetime" }
    ]
  }
], triggerTimeEntryFields = [
  { key: "id", label: "Time Entry ID" },
  { key: "userId", label: "User ID", format: "number" },
  { key: "userFullName", label: "User Full Name" },
  { key: "timerStart", label: "Timer Start", format: "datetime" },
  { key: "timerEnd", label: "Timer End", format: "datetime" },
  { key: "pausedAt", label: "Paused At", format: "datetime" },
  { key: "pausedSeconds", label: "Paused Seconds", format: "number" },
  { key: "duration", label: "Duration", format: "duration" },
  { key: "wasRounded", label: "Was Rounded", format: "boolean" },
  { key: "billable", label: "Billable", format: "boolean" },
  { key: "notes", label: "Notes" },
  { key: "clientId", label: "Client ID" },
  { key: "clientName", label: "Client Name" },
  { key: "projectId", label: "Project ID" },
  { key: "projectName", label: "Project Name" },
  { key: "deliverableId", label: "Task ID" },
  { key: "deliverableName", label: "Task Name" },
  { key: "ticketId", label: "Ticket ID" },
  { key: "ticketName", label: "Ticket Name" },
  { key: "invoiceId", label: "Invoice ID" },
  { key: "invoiceNumber", label: "Invoice Number" },
  { key: "timestamp", label: "Timestamp", format: "datetime" },
  { key: "timestampUpdated", label: "Timestamp Updated", format: "datetime" }
], triggerOpportunityFields = [
  { key: "id", label: "Opportunity ID" },
  { key: "name", label: "Name" },
  { key: "description", label: "Description" },
  { key: "clientId", label: "Client ID" },
  { key: "statusId", label: "Stage ID" },
  { key: "statusLabel", label: "Stage" },
  { key: "value", label: "Value", format: "number" },
  { key: "sentiment", label: "Sentiment", format: "number" },
  { key: "timePeriod", label: "Time Period" },
  { key: "periods", label: "Periods", format: "number" },
  { key: "estCloseDate", label: "Estimated Close Date", format: "date" },
  { key: "actualCloseDate", label: "Actual Close Date", format: "date" },
  { key: "wonOn", label: "Won On", format: "date" },
  { key: "archive", label: "Archived", format: "boolean" },
  { key: "kanbanSort", label: "Kanban Sort", format: "number" },
  { key: "created", label: "Created", format: "datetime" },
  {
    key: "formData",
    label: "Lead Details",
    children: [
      { key: "firstName", label: "First Name" },
      { key: "lastName", label: "Last Name" },
      { key: "email", label: "Email", format: "email" },
      { key: "phone", label: "Phone" },
      { key: "role", label: "Role" },
      { key: "businessName", label: "Business Name" },
      { key: "website", label: "Website", format: "url" },
      { key: "address1", label: "Address Line 1" },
      { key: "address2", label: "Address Line 2" },
      { key: "city", label: "City" },
      { key: "locality", label: "State or Region" },
      { key: "postal", label: "Postal Code" },
      { key: "country", label: "Country" },
      { key: "sourceUrl", label: "Source URL", format: "url" },
      { key: "leadSource", label: "Lead Source" }
    ]
  },
  {
    key: "comments",
    label: "Comments",
    labelKey: "author",
    listItems: [
      { key: "id", label: "Comment ID" },
      { key: "author", label: "Author" },
      { key: "comment", label: "Comment" },
      { key: "clientComment", label: "Client Comment", format: "boolean" },
      { key: "privateComment", label: "Private Comment", format: "boolean" },
      { key: "edited", label: "Edited", format: "boolean" },
      { key: "timestamp", label: "Timestamp", format: "datetime" }
    ]
  },
  {
    key: "workflow",
    label: "Workflow",
    labelKey: "itemType",
    listItems: [
      { key: "id", label: "Item ID" },
      { key: "itemId", label: "Referenced ID" },
      { key: "itemType", label: "Item Type" },
      { key: "timestamp", label: "Timestamp", format: "datetime" }
    ]
  }
], createClientActionOutputSchema = {
  fields: clientFields
}, createContactActionOutputSchema = {
  fields: contactFields
}, createProjectActionOutputSchema = {
  fields: [
    ...projectCoreFields,
    { key: "description", label: "Description" },
    { key: "dateCompleted", label: "Date Completed", format: "date" },
    { key: "proposalVersion", label: "Proposal Version", format: "number" },
    { key: "clientMini", label: "Client", children: clientMiniFields }
  ]
}, createTaskActionOutputSchema = {
  fields: taskFields
}, searchContactsActionOutputSchema = {
  itemLabel: "{firstName} {lastName}",
  fields: [
    {
      key: "contacts",
      label: "Contacts",
      value: "",
      labelKey: "firstName",
      listItems: contactFields
    }
  ]
}, listClientsActionOutputSchema = {
  itemLabel: "{name}",
  fields: [
    {
      key: "clients",
      label: "Clients",
      value: "",
      labelKey: "name",
      listItems: clientFields
    }
  ]
}, searchClientsActionOutputSchema = listClientsActionOutputSchema, searchProjectsActionOutputSchema = {
  itemLabel: "{name}",
  fields: [
    {
      key: "projects",
      label: "Projects",
      value: "",
      labelKey: "name",
      listItems: [
        ...projectCoreFields,
        {
          key: "paymentHistory",
          label: "Payment History",
          labelKey: "invoiceNumberFormatted",
          listItems: [
            { key: "invoiceId", label: "Invoice ID" },
            { key: "invoiceNumber", label: "Invoice Number", format: "number" },
            { key: "invoiceNumberFormatted", label: "Invoice Number Formatted" },
            { key: "invoiceStatus", label: "Invoice Status" },
            { key: "invoiceDate", label: "Invoice Date", format: "date" },
            { key: "dateSent", label: "Date Sent", format: "date" },
            { key: "dateDue", label: "Date Due", format: "date" },
            { key: "amount", label: "Amount", format: "number" },
            { key: "amountDue", label: "Amount Due", format: "number" },
            { key: "currency", label: "Currency" },
            { key: "description", label: "Description" },
            { key: "itemType", label: "Item Type" }
          ]
        }
      ]
    }
  ]
}, listPipelineStagesActionOutputSchema = {
  itemLabel: "{label}",
  fields: [
    {
      key: "stages",
      label: "Pipeline Stages",
      value: "",
      labelKey: "label",
      listItems: [
        { key: "id", label: "Stage ID" },
        { key: "label", label: "Label" },
        { key: "hexColor", label: "Colour" },
        { key: "stageType", label: "Stage Type" }
      ]
    }
  ]
}, listWorkspaceUsersActionOutputSchema = {
  itemLabel: "{user.firstName} {user.lastName}",
  fields: [
    {
      key: "users",
      label: "Workspace Users",
      value: "",
      labelKey: "userType",
      listItems: [
        { key: "userType", label: "User Type" },
        {
          key: "user",
          label: "User",
          children: [
            { key: "userId", label: "User ID", format: "number" },
            { key: "firstName", label: "First Name" },
            { key: "lastName", label: "Last Name" },
            { key: "email", label: "Email", format: "email" },
            { key: "phone", label: "Phone" },
            { key: "phoneVerified", label: "Phone Verified", format: "boolean" },
            { key: "profilePicture", label: "Profile Picture", format: "image" },
            { key: "uuid", label: "UUID" }
          ]
        }
      ]
    }
  ]
}, clientEventTriggerOutputSchema = { fields: clientFields }, projectEventTriggerOutputSchema = { fields: triggerProjectFields }, projectTaskEventTriggerOutputSchema = { fields: triggerTaskFields }, timeEntryEventTriggerOutputSchema = { fields: triggerTimeEntryFields }, opportunityEventTriggerOutputSchema = { fields: triggerOpportunityFields }, moxieCRMTriggerOutputSchemas = {
  client_created: clientEventTriggerOutputSchema,
  client_updated: clientEventTriggerOutputSchema,
  client_deleted: clientEventTriggerOutputSchema,
  project_created: projectEventTriggerOutputSchema,
  project_updated: projectEventTriggerOutputSchema,
  project_completed: projectEventTriggerOutputSchema,
  task_created: projectTaskEventTriggerOutputSchema,
  task_updated: projectTaskEventTriggerOutputSchema,
  task_deleted: projectTaskEventTriggerOutputSchema,
  client_task_approval: projectTaskEventTriggerOutputSchema,
  time_entry_created: timeEntryEventTriggerOutputSchema,
  time_entry_updated: timeEntryEventTriggerOutputSchema,
  time_entry_deleted: timeEntryEventTriggerOutputSchema,
  opportunity_created: opportunityEventTriggerOutputSchema,
  opportunity_updated: opportunityEventTriggerOutputSchema,
  opportunity_deleted: opportunityEventTriggerOutputSchema
};

// ../ap/packages/pieces/community/moxie-crm/src/lib/actions/create-client.ts
var moxieCreateClientAction = (0, import_pieces_framework3.createAction)({
  auth: moxieCRMAuth,
  name: "moxie_create_client",
  classification: "WRITE",
  displayName: "Create a Client",
  description: "Create a new client record in moxie CRM.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new client or prospect record in Moxie CRM with contact, address, billing, and rate details. Use when onboarding a new account into the CRM; set Client Type to distinguish a converted Client from a Prospect lead. Not idempotent: each call creates a separate client even if the name matches an existing one.",
    idempotent: !1
  },
  outputSchema: createClientActionOutputSchema,
  props: {
    name: import_pieces_framework3.Property.ShortText({
      displayName: "Name",
      required: !0
    }),
    clientType: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Client Type",
      required: !0,
      defaultValue: "Client",
      options: {
        disabled: !1,
        options: [
          {
            label: "Client",
            value: "Client"
          },
          {
            label: "Prospect",
            value: "Prospect"
          }
        ]
      }
    }),
    initials: import_pieces_framework3.Property.ShortText({
      displayName: "Initials",
      required: !1
    }),
    address1: import_pieces_framework3.Property.ShortText({
      displayName: "Address 1",
      required: !1
    }),
    address2: import_pieces_framework3.Property.ShortText({
      displayName: "Address 2",
      required: !1
    }),
    city: import_pieces_framework3.Property.ShortText({
      displayName: "City",
      required: !1
    }),
    locality: import_pieces_framework3.Property.ShortText({
      displayName: "Locality",
      required: !1
    }),
    postal: import_pieces_framework3.Property.ShortText({
      displayName: "Postal",
      required: !1
    }),
    country: import_pieces_framework3.Property.ShortText({
      displayName: "Country",
      required: !1,
      description: "ISO 3166-1 alpha-2 country code"
    }),
    website: import_pieces_framework3.Property.ShortText({
      displayName: "Website",
      required: !1
    }),
    phone: import_pieces_framework3.Property.ShortText({
      displayName: "Phone",
      required: !1
    }),
    color: import_pieces_framework3.Property.ShortText({
      displayName: "Color",
      required: !1
    }),
    taxId: import_pieces_framework3.Property.ShortText({
      displayName: "Tax ID",
      required: !1
    }),
    leadSource: import_pieces_framework3.Property.ShortText({
      displayName: "Lead Source",
      required: !1
    }),
    archive: import_pieces_framework3.Property.Checkbox({
      displayName: "Archive ?",
      required: !0,
      defaultValue: !1
    }),
    payInstructions: import_pieces_framework3.Property.LongText({
      displayName: "Pay Instructions",
      required: !1
    }),
    hourlyAmount: import_pieces_framework3.Property.Number({
      displayName: "Hourly Amount",
      required: !1
    }),
    roundingIncrement: import_pieces_framework3.Property.Number({
      displayName: "Rounding Increment",
      required: !1
    }),
    currency: import_pieces_framework3.Property.ShortText({
      displayName: "Currency",
      required: !1,
      description: "Valid 3-Letter ISO 4217 currency code."
    }),
    stripeClientId: import_pieces_framework3.Property.ShortText({
      displayName: "Stripe Client ID",
      required: !1
    }),
    notes: import_pieces_framework3.Property.LongText({
      displayName: "Notes",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    return await (await makeClient(auth)).createClient(propsValue);
  }
});

// ../ap/packages/pieces/community/moxie-crm/src/lib/actions/create-contact.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var moxieCreateContactAction = (0, import_pieces_framework4.createAction)({
  auth: moxieCRMAuth,
  name: "moxie_create_contact",
  classification: "WRITE",
  displayName: "Create a Contact",
  description: "Create a new contact record in moxie CRM.",
  audience: "both",
  aiMetadata: {
    description: "Creates a contact in Moxie CRM, optionally attached to an existing client by name. Use when adding a person to an account, or when a lead needs a named contact. Not idempotent: each call creates a separate contact even if the email matches an existing one.",
    idempotent: !1
  },
  outputSchema: createContactActionOutputSchema,
  props: {
    first: import_pieces_framework4.Property.ShortText({
      displayName: "First Name",
      required: !0
    }),
    last: import_pieces_framework4.Property.ShortText({
      displayName: "Last Name",
      required: !0
    }),
    email: import_pieces_framework4.Property.ShortText({
      displayName: "Email",
      required: !1
    }),
    phone: import_pieces_framework4.Property.ShortText({
      displayName: "Phone",
      required: !1
    }),
    clientName: import_pieces_framework4.Property.Dropdown({
      auth: moxieCRMAuth,
      displayName: "Client",
      description: "The client this contact belongs to.",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        options: (await (await makeClient(auth)).listClients()).map((c) => ({ label: c.name, value: c.name }))
      } : {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first"
      }
    }),
    defaultContact: import_pieces_framework4.Property.Checkbox({
      displayName: "Default Contact",
      description: "Make this the primary contact for the client.",
      required: !1
    }),
    invoiceContact: import_pieces_framework4.Property.Checkbox({
      displayName: "Invoice Contact",
      description: "Send invoices for the client to this contact.",
      required: !1
    }),
    portalAccess: import_pieces_framework4.Property.Checkbox({
      displayName: "Portal Access",
      description: "Allow this contact to sign in to the client portal.",
      required: !1
    }),
    notes: import_pieces_framework4.Property.LongText({
      displayName: "Notes",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    return await (await makeClient(auth)).createContact(propsValue);
  }
});

// ../ap/packages/pieces/community/moxie-crm/src/lib/actions/create-project.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var moxieCreateProjectAction = (0, import_pieces_framework5.createAction)({
  auth: moxieCRMAuth,
  name: "moxie_create_project",
  classification: "WRITE",
  description: "Creates a new project in moxie CRM.",
  displayName: "Create a Project",
  audience: "both",
  aiMetadata: {
    description: "Creates a new project in Moxie CRM under an existing client, including its fee schedule (hourly, fixed price, retainer, or per item), portal access level, and dates. Use when starting a new engagement for a known client. The Client must already exist and is matched by exact client name. Not idempotent: each call creates a separate project.",
    idempotent: !1
  },
  outputSchema: createProjectActionOutputSchema,
  props: {
    name: import_pieces_framework5.Property.ShortText({
      displayName: "Project Name",
      required: !0
    }),
    clientName: import_pieces_framework5.Property.Dropdown({
      auth: moxieCRMAuth,
      displayName: "Client",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await (await makeClient(
          auth
        )).listClients()).map((client2) => ({
          label: client2.name,
          value: client2.name
        }))
      } : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    startDate: import_pieces_framework5.Property.DateTime({
      displayName: "Start Date",
      description: "Please enter date in YYYY-MM-DD format.",
      required: !1
    }),
    dueDate: import_pieces_framework5.Property.DateTime({
      displayName: "Due Date",
      description: "Please enter date in YYYY-MM-DD format.",
      required: !1
    }),
    portalAccess: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Client Portal Access",
      description: "One of: None, Overview, Full access, or Read only.",
      required: !0,
      defaultValue: "Read Only",
      options: {
        options: [
          {
            label: "Not Visible",
            value: "None"
          },
          {
            label: "Overview only",
            value: "Overview"
          },
          {
            label: "Read only project collaboration",
            value: "Read only"
          },
          {
            label: "Full project collaboration",
            value: "Full access"
          }
        ]
      }
    }),
    showTimeWorkedInPortal: import_pieces_framework5.Property.Checkbox({
      displayName: "Show time worked in portal ?",
      required: !1,
      defaultValue: !0
    }),
    feeType: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Fee Type",
      description: "One of: Hourly, Fixed Price, Retainer, Per Item.",
      required: !0,
      options: {
        options: [
          {
            label: "Hourly",
            value: "Hourly"
          },
          {
            label: "Fixed Price",
            value: "Fixed Price"
          },
          {
            label: "Retainer",
            value: "Retainer"
          },
          {
            label: "Per Item",
            value: "Per Item"
          }
        ]
      }
    }),
    amount: import_pieces_framework5.Property.Number({
      displayName: "Amount",
      required: !1,
      defaultValue: 0
    }),
    estimateMax: import_pieces_framework5.Property.Number({
      displayName: "Estimate maximum Amount",
      required: !1,
      defaultValue: 0
    }),
    estimateMin: import_pieces_framework5.Property.Number({
      displayName: "Estimate minimum Amount",
      required: !1,
      defaultValue: 0
    }),
    taxable: import_pieces_framework5.Property.Checkbox({
      displayName: "Is amount taxable ?",
      required: !1,
      defaultValue: !1
    })
  },
  async run({ auth, propsValue }) {
    let {
      name,
      clientName,
      portalAccess,
      showTimeWorkedInPortal,
      feeType,
      amount,
      estimateMax,
      estimateMin,
      taxable
    } = propsValue, dueDate = reformatDate(propsValue.dueDate), startDate = reformatDate(propsValue.startDate);
    return await (await makeClient(auth)).createProject({
      name,
      clientName,
      startDate,
      dueDate,
      portalAccess,
      showTimeWorkedInPortal,
      feeSchedule: {
        feeType,
        amount,
        estimateMax,
        estimateMin,
        taxable
      }
    });
  }
});

// ../ap/packages/pieces/community/moxie-crm/src/lib/actions/create-task.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var moxieCreateTaskAction = (0, import_pieces_framework6.createAction)({
  auth: moxieCRMAuth,
  name: "moxie_create_task",
  classification: "WRITE",
  displayName: "Create a Task",
  description: "Create a task in project.",
  audience: "both",
  aiMetadata: {
    description: "Creates a task (deliverable) inside an existing project in Moxie CRM, with status, dates, priority, assignees, subtasks, and custom values. Use when adding work items to a project. Requires an exact-match client name and a project name owned by that client. Not idempotent: each call creates a separate task.",
    idempotent: !1
  },
  outputSchema: createTaskActionOutputSchema,
  props: {
    name: import_pieces_framework6.Property.ShortText({
      displayName: "Name",
      required: !0
    }),
    clientName: import_pieces_framework6.Property.Dropdown({
      auth: moxieCRMAuth,
      displayName: "Client Name",
      description: "Exact match of a client name in your CRM",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await (await makeClient(
          auth
        )).listClients()).map((client2) => ({
          label: client2.name,
          value: client2.name
        }))
      } : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    projectName: import_pieces_framework6.Property.Dropdown({
      auth: moxieCRMAuth,
      displayName: "Project Name",
      description: "Exact match of a project that is owned by the client.",
      required: !0,
      refreshers: ["clientName"],
      options: async ({ auth, clientName }) => !auth || !clientName ? {
        disabled: !0,
        placeholder: "Connect your account first and select client",
        options: []
      } : {
        disabled: !1,
        options: (await (await makeClient(
          auth
        )).searchProjects(clientName)).map((project) => ({
          label: project.name,
          value: project.name
        }))
      }
    }),
    status: import_pieces_framework6.Property.Dropdown({
      auth: moxieCRMAuth,
      displayName: "Status",
      required: !0,
      defaultValue: "Not Started",
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await (await makeClient(
          auth
        )).listProjectTaskStages()).map((stage) => ({
          label: stage.label,
          value: stage.label
        }))
      } : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    description: import_pieces_framework6.Property.LongText({
      displayName: "Description",
      required: !1
    }),
    startDate: import_pieces_framework6.Property.DateTime({
      displayName: "Start Date",
      required: !1,
      description: "ISO 8601 format date i.e. 2023-07-20"
    }),
    dueDate: import_pieces_framework6.Property.DateTime({
      displayName: "Due Date",
      required: !1,
      description: "ISO 8601 format date i.e. 2023-07-20"
    }),
    priority: import_pieces_framework6.Property.Number({
      displayName: "Priority",
      required: !1,
      description: "Numeric priority for sorting in kanban."
    }),
    tasks: import_pieces_framework6.Property.Array({
      displayName: "Subtasks",
      required: !1
    }),
    assignedTo: import_pieces_framework6.Property.Array({
      displayName: "Assigned To",
      required: !1,
      description: "email addresses of users in the workspace."
    }),
    customValues: import_pieces_framework6.Property.Object({
      displayName: "Custom Values",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let { name, clientName, projectName, status, description, priority } = propsValue, dueDate = reformatDate(propsValue.dueDate), startDate = reformatDate(propsValue.startDate), tasks = propsValue.tasks || [], assignedTo = propsValue.assignedTo || [], customValues = propsValue.customValues || {};
    return await (await makeClient(auth)).createTask({
      name,
      clientName,
      projectName,
      status,
      description,
      dueDate,
      startDate,
      priority,
      tasks,
      assignedTo,
      customValues
    });
  }
});

// ../ap/packages/pieces/community/moxie-crm/src/lib/actions/list-clients.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var moxieListClientsAction = (0, import_pieces_framework7.createAction)({
  auth: moxieCRMAuth,
  name: "moxie_list_clients",
  classification: "READ",
  displayName: "List Clients",
  description: "Retrieve every client and prospect in the workspace.",
  audience: "both",
  aiMetadata: {
    description: "Returns all clients and prospects in the Moxie workspace. Use to find a client id or name before creating a project, task or invoice against it. Read-only and idempotent.",
    idempotent: !0
  },
  outputSchema: listClientsActionOutputSchema,
  props: {},
  async run({ auth }) {
    return await (await makeClient(auth)).listClients();
  }
});

// ../ap/packages/pieces/community/moxie-crm/src/lib/actions/list-invoice-templates.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var moxieListInvoiceTemplatesAction = (0, import_pieces_framework8.createAction)({
  auth: moxieCRMAuth,
  name: "moxie_list_invoice_templates",
  classification: "READ",
  displayName: "List Invoice Templates",
  description: "Retrieve the invoice template names of the workspace.",
  audience: "both",
  aiMetadata: {
    description: "Returns the names of the invoice templates configured in the Moxie workspace. Use to pick a valid template name before creating an invoice. Read-only and idempotent.",
    idempotent: !0
  },
  props: {},
  async run({ auth }) {
    return await (await makeClient(auth)).listInvoiceTemplates();
  }
});

// ../ap/packages/pieces/community/moxie-crm/src/lib/actions/list-pipeline-stages.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var moxieListPipelineStagesAction = (0, import_pieces_framework9.createAction)({
  auth: moxieCRMAuth,
  name: "moxie_list_pipeline_stages",
  classification: "READ",
  displayName: "List Pipeline Stages",
  description: "Retrieve the opportunity pipeline stages of the workspace.",
  audience: "both",
  aiMetadata: {
    description: "Returns the Moxie pipeline stages, each with its id, label, colour and stage type (New, InProgress, OnHold, ClosedWon, ClosedLost or Complete). Use to resolve a stage id before creating or moving an opportunity. Read-only and idempotent.",
    idempotent: !0
  },
  outputSchema: listPipelineStagesActionOutputSchema,
  props: {},
  async run({ auth }) {
    return await (await makeClient(auth)).listPipelineStages();
  }
});

// ../ap/packages/pieces/community/moxie-crm/src/lib/actions/list-workspace-users.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var moxieListWorkspaceUsersAction = (0, import_pieces_framework10.createAction)({
  auth: moxieCRMAuth,
  name: "moxie_list_workspace_users",
  classification: "READ",
  displayName: "List Workspace Users",
  description: "Retrieve the users of the workspace and their access.",
  audience: "both",
  aiMetadata: {
    description: "Returns the users in the Moxie workspace with their user type, contact details and project and feature access. Use to resolve the email address of an assignee before creating a task. Read-only and idempotent.",
    idempotent: !0
  },
  outputSchema: listWorkspaceUsersActionOutputSchema,
  props: {},
  async run({ auth }) {
    return await (await makeClient(auth)).listWorkspaceUsers();
  }
});

// ../ap/packages/pieces/community/moxie-crm/src/lib/actions/search-clients.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var moxieSearchClientsAction = (0, import_pieces_framework11.createAction)({
  auth: moxieCRMAuth,
  name: "moxie_search_clients",
  classification: "READ",
  displayName: "Search Clients",
  description: "Find clients by name, contact email or contact full name.",
  audience: "both",
  aiMetadata: {
    description: "Searches Moxie clients by client name (starts with), contact email (starts with) or contact full name (contains), and returns the matching client records with their address, billing terms and contacts. Returns an empty list when nothing matches. Read-only and idempotent.",
    idempotent: !0
  },
  outputSchema: searchClientsActionOutputSchema,
  props: {
    query: import_pieces_framework11.Property.ShortText({
      displayName: "Query",
      description: "Matches a client name or contact email that starts with this value, or a contact full name that contains it.",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    return await (await makeClient(auth)).searchClients(propsValue.query);
  }
});

// ../ap/packages/pieces/community/moxie-crm/src/lib/actions/search-contacts.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var moxieSearchContactsAction = (0, import_pieces_framework12.createAction)({
  auth: moxieCRMAuth,
  name: "moxie_search_contacts",
  classification: "READ",
  displayName: "Search Contacts",
  description: "Find contacts by first name, last name or email.",
  audience: "both",
  aiMetadata: {
    description: "Searches Moxie contacts by first name, last name or email and returns the matching contact records, including the client each belongs to. Leave the query empty to list every contact. Read-only and idempotent.",
    idempotent: !0
  },
  outputSchema: searchContactsActionOutputSchema,
  props: {
    query: import_pieces_framework12.Property.ShortText({
      displayName: "Query",
      description: "Matches a contact first name, last name or email. Leave empty to return every contact.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    return await (await makeClient(auth)).searchContacts(propsValue.query);
  }
});

// ../ap/packages/pieces/community/moxie-crm/src/lib/actions/search-projects.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var moxieSearchProjectsAction = (0, import_pieces_framework13.createAction)({
  auth: moxieCRMAuth,
  name: "moxie_search_projects",
  classification: "READ",
  displayName: "Search Projects",
  description: "Find projects belonging to a client.",
  audience: "both",
  aiMetadata: {
    description: "Searches Moxie projects and returns the matches. Use to resolve a project name or id before creating a task or logging time against it. Read-only and idempotent.",
    idempotent: !0
  },
  outputSchema: searchProjectsActionOutputSchema,
  props: {
    query: import_pieces_framework13.Property.ShortText({
      displayName: "Query",
      description: "Client name whose projects should be returned.",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    return await (await makeClient(auth)).searchProjects(propsValue.query);
  }
});

// ../ap/packages/pieces/community/moxie-crm/src/lib/triggers/register-trigger.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var moxieCRMRegisterTrigger = ({
  name,
  displayName,
  description,
  aiMetadata,
  eventType,
  sampleData
}) => (0, import_pieces_framework14.createTrigger)({
  auth: moxieCRMAuth,
  name: `moxie_trigger_${name}`,
  classification: "READ",
  displayName,
  description,
  aiMetadata,
  props: {
    md: import_pieces_framework14.Property.MarkDown({
      value: `
        - Go to the **Workspace Settngs -> Connected Apps -> Integration** section.
        - Under **Custom Integration** , click on **Add Rest Hook**.
        - Click on **Integrations** section.
        - In the endpoint field, paste the following URL: 
            \`\`\`text
            {{webhookUrl}}
            \`\`\`

        - Select the event as **\`${displayName}\`** and click on **Save**.
        `
    })
  },
  outputSchema: moxieCRMTriggerOutputSchemas[name],
  type: import_pieces_framework14.TriggerStrategy.WEBHOOK,
  sampleData,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/moxie-crm/src/lib/triggers/index.ts
var MoxieCRMWebhookSampleData = {
  PROJECT_EVENT_SAMPLE_DATA: {
    id: "64b800020000000000000000",
    accountId: 10016,
    projectTypeId: "64b800030000000000000000",
    sampleData: !1,
    clientId: "64b800010000000000000000",
    name: "Design & Development of Website",
    active: !0,
    startDate: null,
    dueDate: null,
    dateCreated: "2026-08-31T10:22:40.856Z",
    client: {
      accountId: 10016,
      sampleData: !1,
      id: "64b800010000000000000000",
      clientType: "Client",
      name: "Moxie, Inc.",
      initials: null,
      locality: "OR",
      country: null,
      color: "#78909C",
      address1: null,
      address2: null,
      city: "Portland",
      postal: null,
      website: null,
      phone: "+18887231235",
      s3LogoFile: null,
      taxId: null,
      projects: [],
      hourlyAmount: 225,
      archive: !1,
      currency: null,
      logo: null,
      leadSource: null,
      defaultTaxRate: null,
      defaultTaxRuleId: null,
      whoPaysCardFees: "Client",
      customValues: [],
      contact: null
    },
    leadGenArchived: !1,
    feeSchedule: {
      feeType: "Hourly",
      amount: 225,
      retainerSchedule: "Monthly",
      estimateMax: null,
      estimateMin: null,
      retainerStart: null,
      retainerTiming: "Advanced",
      retainerPeriods: -1,
      retainerOverageRate: null,
      taxable: !1,
      fromProposalId: null,
      fromProposalSignedDate: null,
      updatedDate: null,
      updatedBy: null,
      retainerActive: !0
    },
    proposalId: null,
    proposalName: null,
    hexColor: null,
    portalAccess: "Overview",
    showTimeWorkedInPortal: !0,
    portalAccessAssignedOnly: !1,
    projectOwners: [],
    customValues: [],
    paymentHistory: [],
    files: [],
    deliverables: []
  },
  CLIENT_EVENT_SAMPLE_DATA: {
    id: "64b800010000000000000000",
    accountId: 10016,
    name: "Moxie, Inc.",
    clientType: "Client",
    initials: null,
    address1: null,
    address2: null,
    city: "Portland",
    locality: "OR",
    postal: null,
    country: null,
    website: null,
    phone: "+18887231235",
    color: "#78909C",
    logo: null,
    s3LogoFile: null,
    taxId: null,
    leadSource: null,
    archive: !1,
    leadGenArchived: !1,
    paymentTerms: null,
    payInstructions: null,
    hourlyAmount: 225,
    defaultTaxRate: null,
    defaultTaxRuleId: null,
    roundingIncrement: 1,
    currency: null,
    activityInitialized: !0,
    lastInvoiceRunDate: null,
    nextInvoiceRunDate: null,
    importRecordId: null,
    integrationKeys: {
      quickbooksId: null,
      xeroId: null
    },
    customValues: [],
    files: [],
    comments: [],
    created: "2026-08-31T10:22:33.650Z",
    sampleData: !1,
    stripeClientId: null,
    peppolCompliant: !1,
    mergedIntoClientId: null,
    mergedAt: null,
    mergedByUserId: null,
    notes: null,
    notifyOnCreate: null,
    contacts: []
  },
  PROJECT_TASK_EVENT_SAMPLE_DATA: {
    id: "64b800040000000000000000",
    clientId: "64b800010000000000000000",
    projectId: "64b800020000000000000000",
    projectTypeId: "64b800030000000000000000",
    parentTaskId: null,
    subTaskSort: 0,
    project: {
      accountId: 10016,
      id: "64b800020000000000000000",
      clientId: "64b800010000000000000000",
      projectTypeId: "64b800030000000000000000",
      name: "Design & Development of Website",
      active: !0,
      hexColor: null
    },
    client: {
      accountId: 10016,
      id: "64b800010000000000000000",
      name: "Moxie, Inc.",
      initials: null,
      s3LogoFile: null,
      logo: null,
      color: "#78909C"
    },
    name: "Build the website",
    statusId: "3f9a1c62-0b7e-4d51-9a2f-8e6d4b1c7a05",
    status: "Not started",
    descriptionFormat: "Markdown",
    priority: 1,
    taskPriority: "Normal",
    description: "This is the description",
    assignedTo: null,
    assignedToList: [
      47158
    ],
    approvalRequired: !1,
    product: null,
    quantity: null,
    invoiceId: null,
    invoiceNumber: null,
    ticketId: null,
    initialWorkflowComplete: !0,
    customValues: [],
    comments: [],
    events: [
      {
        user: "Jamie Rivera",
        events: [
          "Jamie Rivera Created task"
        ],
        clientEvent: !1,
        timestamp: "2026-08-31T10:22:47.654Z"
      }
    ],
    startDate: null,
    dueDate: null,
    created: "2026-08-31T10:22:47.645Z",
    completed: null,
    tasks: [],
    archived: !1,
    kanbanSort: 0,
    isSubTask: !1
  },
  FORM_EVENT_SAMPLE_DATA: {
    id: "64b8233d6ce6226305f24b47",
    accountId: 10016,
    client: null,
    formName: "test-new-client",
    businessName: "Moxie",
    firstName: "Geoffrey",
    lastName: "Mina",
    phone: "+444445551212",
    email: "hello@withmoxie.com",
    role: null,
    address1: null,
    address2: null,
    city: null,
    locality: null,
    postal: null,
    country: null,
    website: null,
    leadSource: null,
    sourceUrl: "https://hello.withmoxie.dev/00/hectic-lab/test-new-client?inPortal=true",
    Field6: "Answer to your first question",
    Field7: "Another to your other question",
    submittedAt: "2023-07-19T17:54:05.257Z"
  },
  TIME_ENTRY_EVENT_SAMPLE_DATA: {
    id: "64b800050000000000000000",
    accountId: 10016,
    sampleData: !1,
    userId: 16,
    timerStart: "2026-08-29T09:00:00.000Z",
    timerEnd: "2026-08-29T10:15:00.000Z",
    pausedAt: null,
    userFullName: "Jamie Rivera",
    notes: "This is the description",
    format: "Markdown",
    clientId: "64b800010000000000000000",
    projectId: "64b800020000000000000000",
    deliverableId: null,
    ticketId: null,
    clientName: "Moxie, Inc.",
    projectName: "Design & Development of Website",
    deliverableName: null,
    ticketName: null,
    timestamp: "2026-08-31T10:22:54.688Z",
    timestampUpdated: null,
    billable: !0,
    pausedSeconds: 0,
    invoiceId: null,
    invoiceNumber: null,
    importRecordId: null,
    feeSchedule: null,
    customGroupFieldValue: null,
    duration: 4500,
    wasRounded: !1
  },
  MEETING_EVENT_SAMPLE_DATA: {
    id: "64b824df6ce6226305f24b53",
    accountId: 10016,
    ownerUserId: null,
    sampleData: !1,
    meetingId: "63076a1320e0d10001e6cb63",
    clientId: "63c5ea0c840e3207033931b5",
    meetingStatus: "Scheduled",
    meetingName: "Geoff Mina / 30 minute meeting",
    schedulerName: "30 minute meeting",
    confirmedTime: {
      start: "2023-07-19T22:30:00.000Z",
      end: "2023-07-19T23:00:00.000Z"
    },
    scheduledTimezone: "America/New_York",
    scheduledLocale: "en-US",
    formData: {
      firstName: "Geoff",
      lastName: "Mina",
      email: "geoff.mina@withmoxie.com",
      phone: "+445555555555",
      role: null,
      businessName: null,
      website: null,
      address1: null,
      address2: null,
      city: null,
      locality: null,
      postal: null,
      country: null,
      sourceUrl: null,
      opportunityId: null,
      templateId: null,
      cardTokenId: null,
      leadSource: null,
      answers: []
    },
    location: {
      type: "Google"
    },
    zoomMeeting: null,
    googleMeeting: {
      googleUserId: 16,
      eventId: "_6oq64e1i6hi6cdj3ckr34chm6co3aphi6hh3acq0d1im6t39cdgn0s1ecdnmq",
      hangoutLink: "https://meet.google.com/swq-azkw-zkt",
      htmlLink: "https://www.google.com/calendar/event?eid=XzZvcTY0ZTFpNmhpNmNkajNja3IzNGNobTZjbzNhcGhpNmhoM2FjcTBkMWltNnQzOWNkZ24wczFlY2RubXEgZ2VvZmYubWluYUBoZWN0aWMudXM",
      googleUser: {
        name: "Geoff Mina",
        given_name: "Geoff",
        family_name: "Mina",
        picture: "https://lh3.googleusercontent.com/a/AGNmyxYxJL9h8SnY6z2oXg7fNHAdov1TJ4--RQtiAMn8=s96-c",
        email: "geoff.mina@hectic.us",
        email_verified: !0,
        locale: "en"
      },
      entryPoints: [
        {
          entryPointType: "video",
          label: "meet.google.com/swq-azkw-zkt",
          uri: "https://meet.google.com/swq-azkw-zkt"
        },
        {
          entryPointType: "more",
          pin: "4419810087411",
          uri: "https://tel.meet/swq-azkw-zkt?pin=4419810087411"
        },
        {
          entryPointType: "phone",
          label: "+1 646-504-7945",
          pin: "978184485",
          regionCode: "US",
          uri: "tel:+1-646-504-7945"
        }
      ]
    },
    microsoftEvent: null,
    connectedICalUid: null,
    notes: null,
    cancellationReason: null,
    leadGenArchived: !1,
    opportunityId: null,
    files: [],
    meetingWith: "Moxie",
    incomeRecordId: null,
    icalUid: "64b824df6ce6226305f24b53@hecticapp.com"
  },
  OPPORTUNITY_EVENT_SAMPLE_DATA: {
    id: "64b800060000000000000000",
    accountId: 10016,
    clientId: null,
    statusId: "3f9a1c62-0b7e-4d51-9a2f-8e6d4b1c7a05",
    kanbanSort: 0,
    name: "Hook SubmitterC / null",
    description: null,
    assignedTo: [],
    format: "Markdown",
    sentiment: 2,
    value: 0,
    timePeriod: "OneTime",
    periods: 1,
    estCloseDate: null,
    actualCloseDate: null,
    formData: {
      firstName: "Jamie",
      lastName: "Rivera",
      email: "hello@withmoxie.com",
      phone: "+18887231235",
      role: null,
      businessName: "Hook Co C",
      website: null,
      address1: null,
      address2: null,
      city: null,
      locality: null,
      postal: null,
      country: null,
      taxId: null,
      sourceUrl: null,
      opportunityId: null,
      templateId: null,
      cardTokenId: null,
      leadSource: null,
      clientId: null,
      answers: [
        {
          id: "3f9a1c62-0b7e-4d51-9a2f-8e6d4b1c7a05",
          fieldKey: "q1",
          fieldType: "TextInput",
          question: "What do you need?",
          answer: "Sample notes"
        }
      ]
    },
    archive: !1,
    initialWorkflow: !0,
    toDos: [],
    comments: [
      {
        id: null,
        author: "Jamie Rivera",
        authorId: "0",
        comment: "Auto created from form: null",
        format: "Markdown",
        clientComment: !1,
        edited: !1,
        privateComment: !1,
        sendEmail: !1,
        timestamp: "2026-08-31T10:23:08.787Z"
      }
    ],
    files: [],
    workflow: [
      {
        id: "3f9a1c62-0b7e-4d51-9a2f-8e6d4b1c7a05",
        itemId: "64b800070000000000000000",
        itemType: "Form",
        properties: {},
        timestamp: "2026-08-31T10:23:08.786Z"
      }
    ],
    customValues: [],
    history: [],
    importRecordId: null,
    sampleData: !1,
    created: "2026-08-31T10:23:08.786Z",
    wonOn: null,
    client: null,
    statusLabel: "Inquiry"
  },
  INVOICE_EVENT_SAMPLE_DATA: {
    id: "64ae5aea99f38e74fc78ae46",
    invoiceNumber: 74,
    invoiceNumberFormatted: "E-2023-74",
    accountId: 10016,
    clientId: "6490580de30ecf51c2c22ffa",
    dateCreated: "2023-07-16",
    dateSent: "2023-07-12",
    dateDue: "2023-08-11",
    dateDueCalculated: null,
    datePaid: "2023-07-12",
    clientInfo: {
      id: "6490580de30ecf51c2c22ffa",
      name: "E164",
      initials: null,
      address1: "123 Any Street",
      address2: null,
      city: "Anytown",
      locality: "NY",
      postal: "12020",
      country: "US",
      phone: "+15555551212",
      color: "#8EA3B8",
      taxId: null,
      website: null,
      contact: {
        id: "6490580de30ecf51c2c22ffb",
        accountId: 10016,
        clientId: "6490580de30ecf51c2c22ffa",
        clientPortalUserId: -79,
        firstName: "Geoffrey",
        lastName: "Mina",
        role: null,
        phone: "+12224445234",
        email: "user@email.com",
        mobile: null,
        notes: null,
        defaultContact: !0,
        invoiceContact: !1,
        portalAccess: !0,
        importRecordId: null,
        sampleData: null
      },
      roundingIncrement: 1,
      customInfo: !1
    },
    status: "PARTIAL",
    invoiceType: "STANDARD",
    subTotal: 14950,
    convenienceFee: 0,
    lateFee: 0,
    discountAmount: 0,
    creditApplied: 0,
    tax: 225,
    total: 15175,
    localTotal: 15175,
    paymentTotal: 78.75,
    localPaymentTotal: 78.75,
    amountDue: 15096.25,
    localAmountDue: 15096.25,
    currency: "USD",
    integrationKeys: {
      quickbooksId: null,
      xeroId: null
    },
    viewOnlineUrl: "https://clients.domain.com/invoice?token=",
    payments: [
      {
        id: "29e34d7b-ef69-4059-bbe9-7b214c38a011",
        amount: 78.75,
        pending: !1,
        paidBy: "G. Mina",
        paymentProvider: "CHECK",
        currency: "USD",
        referenceNumber: null,
        memo: null,
        datePaid: "2023-07-12",
        timestamp: "2023-07-12T06:00:00.000Z",
        integratedPayment: !1,
        forcePaidInFull: !1,
        integrationKeys: {
          quickbooksId: null,
          xeroId: null
        },
        isFailedPayment: !1,
        localAmount: 78.75
      }
    ]
  }
}, moxieCRMTriggers = [
  {
    name: "client_created",
    displayName: "Client Created",
    description: "Triggered when a new client is created.",
    aiMetadata: {
      description: "Fires when a new client or prospect record is created in Moxie CRM. Use to start automations when an account is added to the CRM."
    },
    eventType: "ClientCreate" /* CLIENT_CREATED */,
    sampleData: MoxieCRMWebhookSampleData.CLIENT_EVENT_SAMPLE_DATA
  },
  {
    name: "client_updated",
    displayName: "Client Updated",
    description: "Triggered when an existing client is updated.",
    aiMetadata: {
      description: "Fires when an existing client record is modified in Moxie CRM, such as a change to contact, address, or billing details."
    },
    eventType: "ClientUpdate" /* CLIENT_UPDATED */,
    sampleData: MoxieCRMWebhookSampleData.CLIENT_EVENT_SAMPLE_DATA
  },
  {
    name: "client_deleted",
    displayName: "Client Deleted",
    description: "Triggered when an existing client is deleted.",
    aiMetadata: {
      description: "Fires when a client record is deleted from Moxie CRM. Use to clean up or sync downstream systems when an account is removed."
    },
    eventType: "ClientDelete" /* CLIENT_DELETED */,
    sampleData: MoxieCRMWebhookSampleData.CLIENT_EVENT_SAMPLE_DATA
  },
  {
    name: "project_created",
    displayName: "Project Created",
    description: "Triggered when a new project is created.",
    aiMetadata: {
      description: "Fires when a new project is created for a client in Moxie CRM. Use to kick off engagement setup automations."
    },
    eventType: "ProjectCreate" /* PROJECT_CREATED */,
    sampleData: MoxieCRMWebhookSampleData.PROJECT_EVENT_SAMPLE_DATA
  },
  {
    name: "project_updated",
    displayName: "Project Updated",
    description: "Triggered when an existing project is updated.",
    aiMetadata: {
      description: "Fires when an existing project is modified in Moxie CRM, such as a change to dates, fee schedule, or portal access."
    },
    eventType: "ProjectUpdate" /* PROJECT_UPDATED */,
    sampleData: MoxieCRMWebhookSampleData.PROJECT_EVENT_SAMPLE_DATA
  },
  {
    name: "project_completed",
    displayName: "Project Completed",
    description: "Triggered when an existing project is completed.",
    aiMetadata: {
      description: "Fires when a project is marked complete in Moxie CRM. Use to trigger wrap-up steps such as final invoicing or close-out notifications."
    },
    eventType: "ProjectComplete" /* PROJECT_COMPLETED */,
    sampleData: MoxieCRMWebhookSampleData.PROJECT_EVENT_SAMPLE_DATA
  },
  {
    name: "task_created",
    displayName: "Task Created",
    description: "Triggered when a new task is created.",
    aiMetadata: {
      description: "Fires when a new task (deliverable) is created within a project in Moxie CRM."
    },
    eventType: "DeliverableCreate" /* TASK_CREATED */,
    sampleData: MoxieCRMWebhookSampleData.PROJECT_TASK_EVENT_SAMPLE_DATA
  },
  {
    name: "task_updated",
    displayName: "Task Updated",
    description: "Triggered when an existing task is updated.",
    aiMetadata: {
      description: "Fires when an existing task (deliverable) is modified in Moxie CRM, such as a change to status, assignee, or due date."
    },
    eventType: "DeliverableUpdate" /* TASK_UPDATED */,
    sampleData: MoxieCRMWebhookSampleData.PROJECT_TASK_EVENT_SAMPLE_DATA
  },
  {
    name: "task_deleted",
    displayName: "Task Deleted",
    description: "Triggered when an existing task is deleted.",
    aiMetadata: {
      description: "Fires when a task (deliverable) is deleted from a project in Moxie CRM."
    },
    eventType: "DeliverableDelete" /* TASK_DELETED */,
    sampleData: MoxieCRMWebhookSampleData.PROJECT_TASK_EVENT_SAMPLE_DATA
  },
  {
    name: "client_task_approval",
    displayName: "Client Task Approval",
    description: "Triggered when a task is moved to client approval.",
    aiMetadata: {
      description: "Fires when a task (deliverable) is moved into the client-approval stage in Moxie CRM. Use to notify or escalate when work awaits client sign-off."
    },
    eventType: "DeliverableApproval" /* TASK_APPROVAL */,
    sampleData: MoxieCRMWebhookSampleData.PROJECT_TASK_EVENT_SAMPLE_DATA
  },
  {
    name: "form_submitted",
    displayName: "Form Submitted",
    description: "Triggered when a new form is submitted.",
    aiMetadata: {
      description: "Fires when a Moxie CRM form (such as an intake or lead form) is submitted. Use to capture inbound leads or client-supplied information."
    },
    eventType: "FormCompleted" /* FORM_SUBMITTED */,
    sampleData: MoxieCRMWebhookSampleData.FORM_EVENT_SAMPLE_DATA
  },
  {
    name: "time_entry_created",
    displayName: "Time Entry Created",
    description: "Triggered when a new time entry is created.",
    aiMetadata: {
      description: "Fires when a new time entry (timer record) is logged in Moxie CRM against a client, project, or task."
    },
    eventType: "TimerCreate" /* TIME_ENTRY_CREATED */,
    sampleData: MoxieCRMWebhookSampleData.TIME_ENTRY_EVENT_SAMPLE_DATA
  },
  {
    name: "time_entry_updated",
    displayName: "Time Entry Updated",
    description: "Triggered when an existing time entry is updated.",
    aiMetadata: {
      description: "Fires when an existing time entry (timer record) is modified in Moxie CRM, such as a change to duration or notes."
    },
    eventType: "TimerUpdate" /* TIME_ENTRY_UPDATED */,
    sampleData: MoxieCRMWebhookSampleData.TIME_ENTRY_EVENT_SAMPLE_DATA
  },
  {
    name: "time_entry_deleted",
    displayName: "Time Entry Deleted",
    description: "Triggered when an existing time entry is deleted.",
    aiMetadata: {
      description: "Fires when a time entry (timer record) is deleted in Moxie CRM."
    },
    eventType: "TimerDelete" /* TIME_ENTRY_DELETED */,
    sampleData: MoxieCRMWebhookSampleData.TIME_ENTRY_EVENT_SAMPLE_DATA
  },
  {
    name: "meeting_scheduled",
    displayName: "Meeting Scheduled",
    description: "Triggered when a new meeting is scheduled.",
    aiMetadata: {
      description: "Fires when a new meeting is scheduled in Moxie CRM, including its confirmed time and conferencing details."
    },
    eventType: "MeetingScheduled" /* MEETING_SCHEDULED */,
    sampleData: MoxieCRMWebhookSampleData.MEETING_EVENT_SAMPLE_DATA
  },
  {
    name: "meeting_updated",
    displayName: "Meeting Updated",
    description: "Triggered when an existing meeting is updated.",
    aiMetadata: {
      description: "Fires when an existing meeting is modified in Moxie CRM, such as a reschedule or change to its details."
    },
    eventType: "MeetingUpdated" /* MEETING_UPDATED */,
    sampleData: MoxieCRMWebhookSampleData.MEETING_EVENT_SAMPLE_DATA
  },
  {
    name: "meeting_cancelled",
    displayName: "Meeting Cancelled",
    description: "Triggered when a meeting is cancelled.",
    aiMetadata: {
      description: "Fires when a scheduled meeting is cancelled in Moxie CRM."
    },
    eventType: "MeetingCancelled" /* MEETING_CANCELLED */,
    sampleData: MoxieCRMWebhookSampleData.MEETING_EVENT_SAMPLE_DATA
  },
  {
    name: "opportunity_created",
    displayName: "Opportunity Created",
    description: "Triggered when a new pipeline opportunity is created.",
    aiMetadata: {
      description: "Fires when a new sales-pipeline opportunity is created in Moxie CRM. Use to start automations on incoming deals."
    },
    eventType: "OpportunityCreate" /* OPPORTUNITY_CREATED */,
    sampleData: MoxieCRMWebhookSampleData.OPPORTUNITY_EVENT_SAMPLE_DATA
  },
  {
    name: "opportunity_updated",
    displayName: "Opportunity Updated",
    description: "Triggered when an existing opportunity is updated.",
    aiMetadata: {
      description: "Fires when an existing pipeline opportunity is modified in Moxie CRM, such as a stage change or value update."
    },
    eventType: "OpportunityUpdate" /* OPPORTUNITY_UPDATED */,
    sampleData: MoxieCRMWebhookSampleData.OPPORTUNITY_EVENT_SAMPLE_DATA
  },
  {
    name: "opportunity_deleted",
    displayName: "Opportunity Deleted",
    description: "Triggered when an existing pipeline opportunity is deleted.",
    aiMetadata: {
      description: "Fires when a pipeline opportunity is deleted from Moxie CRM."
    },
    eventType: "OpportunityDelete" /* OPPORTUNITY_DELETED */,
    sampleData: MoxieCRMWebhookSampleData.OPPORTUNITY_EVENT_SAMPLE_DATA
  },
  {
    name: "invoice_sent",
    displayName: "Invoice Sent",
    description: "Triggered when an invoice is sent.",
    aiMetadata: {
      description: "Fires when an invoice is sent to a client in Moxie CRM. Use to track billing or notify on outgoing invoices."
    },
    eventType: "InvoiceSent" /* INVOICE_SENT */,
    sampleData: MoxieCRMWebhookSampleData.INVOICE_EVENT_SAMPLE_DATA
  },
  {
    name: "payment_received",
    displayName: "Payment Received",
    description: "Triggered when a payment is received.",
    aiMetadata: {
      description: "Fires when a payment against an invoice is received in Moxie CRM. Use to reconcile payments or trigger fulfillment."
    },
    eventType: "PaymentReceived" /* PAYMENT_RECEIVED */,
    sampleData: MoxieCRMWebhookSampleData.INVOICE_EVENT_SAMPLE_DATA
  }
].map((props) => moxieCRMRegisterTrigger(props));

// ../ap/packages/pieces/community/moxie-crm/src/index.ts
var moxieCrm = (0, import_pieces_framework15.createPiece)({
  displayName: "Moxie",
  description: "CRM build for the freelancers.",
  auth: moxieCRMAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/moxie-crm.png",
  authors: ["kishanprmr", "MoShizzle", "abuaboud"],
  categories: [import_pieces_framework16.PieceCategory.SALES_AND_CRM],
  actions: [
    moxieCreateClientAction,
    moxieCreateContactAction,
    moxieCreateTaskAction,
    moxieCreateProjectAction,
    moxieListClientsAction,
    moxieSearchClientsAction,
    moxieSearchContactsAction,
    moxieSearchProjectsAction,
    moxieListPipelineStagesAction,
    moxieListWorkspaceUsersAction,
    moxieListInvoiceTemplatesAction,
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: (auth) => {
        var _a;
        return (_a = auth == null ? void 0 : auth.props.baseUrl) != null ? _a : "";
      },
      auth: moxieCRMAuth,
      authMapping: async (auth) => ({
        "X-API-KEY": auth.props.apiKey
      })
    })
  ],
  triggers: moxieCRMTriggers
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  moxieCrm
});
