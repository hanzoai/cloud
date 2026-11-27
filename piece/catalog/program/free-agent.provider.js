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

// ../ap/packages/pieces/community/free-agent/src/index.ts
var index_exports = {};
__export(index_exports, {
  freeAgent: () => freeAgent
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/free-agent/src/lib/actions/create-task.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/free-agent/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), freeAgentAuth = import_pieces_framework.PieceAuth.OAuth2({
  description: "Connect your FreeAgent account",
  authUrl: "https://api.freeagent.com/v2/approve_app",
  tokenUrl: "https://api.freeagent.com/v2/token_endpoint",
  required: !0,
  scope: [],
  validate: async (auth) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: "https://api.freeagent.com/v2/users/me",
        authentication: {
          type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
          token: auth.auth.access_token
        }
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Authentication failed. Please check your credentials."
      };
    }
  }
});

// ../ap/packages/pieces/community/free-agent/src/lib/actions/create-task.ts
var freeAgentCreateTask = (0, import_pieces_framework2.createAction)({
  displayName: "Create Task",
  description: "Create a task inside a project",
  audience: "both",
  aiMetadata: { description: "Creates a new task within a specific FreeAgent project. Requires a project (selected from the account) and a task name; optionally sets billable status, billing rate/period, and task status. Not idempotent \u2014 each call adds a new task, so guard against duplicates.", idempotent: !1 },
  auth: freeAgentAuth,
  name: "create_task",
  props: {
    project_id: import_pieces_framework2.Property.Dropdown({
      displayName: "Project",
      description: "Select the project to create the task in",
      required: !0,
      refreshers: [],
      auth: freeAgentAuth,
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please authenticate first"
          };
        try {
          return {
            options: ((await import_pieces_common2.httpClient.sendRequest({
              method: import_pieces_common2.HttpMethod.GET,
              url: "https://api.freeagent.com/v2/projects",
              authentication: {
                type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
                token: auth.access_token
              }
            })).body.projects || []).map((project) => ({
              label: project.name,
              value: project.url.split("/").pop()
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load projects"
          };
        }
      }
    }),
    name: import_pieces_framework2.Property.ShortText({
      displayName: "Task Name",
      description: "The name of the task",
      required: !0
    }),
    is_billable: import_pieces_framework2.Property.Checkbox({
      displayName: "Billable",
      description: "Whether this task is billable to clients",
      required: !1,
      defaultValue: !0
    }),
    billing_rate: import_pieces_framework2.Property.Number({
      displayName: "Billing Rate",
      description: "The rate at which the project is billed per billing period",
      required: !1
    }),
    billing_period: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Billing Period",
      description: "The period for billing rate",
      required: !1,
      options: {
        options: [
          { label: "Hour", value: "hour" },
          { label: "Day", value: "day" }
        ]
      },
      defaultValue: "hour"
    }),
    status: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Status",
      description: "The status of the task",
      required: !1,
      options: {
        options: [
          { label: "Active", value: "Active" },
          { label: "Completed", value: "Completed" },
          { label: "Hidden", value: "Hidden" }
        ]
      },
      defaultValue: "Active"
    })
  },
  async run(context) {
    let { project_id, name, is_billable, billing_rate, billing_period, status } = context.propsValue, auth = context.auth, payload = {
      task: {
        name,
        is_billable: is_billable !== !1,
        // Default to true if not specified
        status: status || "Active"
      }
    };
    return payload.task.is_billable && (billing_rate !== void 0 && (payload.task.billing_rate = billing_rate.toString()), billing_period && (payload.task.billing_period = billing_period)), (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: `https://api.freeagent.com/v2/tasks?project=${project_id}`,
      body: payload,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      },
      headers: {
        "Content-Type": "application/json"
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/free-agent/src/lib/actions/create-contact.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var freeAgentCreateContact = (0, import_pieces_framework3.createAction)({
  displayName: "Create Contact",
  description: "Create a new contact in FreeAgent",
  audience: "both",
  aiMetadata: { description: "Creates a new contact (client or supplier) in FreeAgent. Use when adding a person or organization to bill or work with. Requires either a first and last name or an organization name; all other fields (email, address, tax, payment terms, locale) are optional. Not idempotent \u2014 each call creates another contact even with identical details.", idempotent: !1 },
  auth: freeAgentAuth,
  name: "create_contact",
  props: {
    first_name: import_pieces_framework3.Property.ShortText({
      displayName: "First Name",
      description: "First name (required if organization name is not specified)",
      required: !1
    }),
    last_name: import_pieces_framework3.Property.ShortText({
      displayName: "Last Name",
      description: "Last name (required if organization name is not specified)",
      required: !1
    }),
    organisation_name: import_pieces_framework3.Property.ShortText({
      displayName: "Organization Name",
      description: "Organization name (required if first and last name are not specified)",
      required: !1
    }),
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email",
      description: "Email address",
      required: !1
    }),
    billing_email: import_pieces_framework3.Property.ShortText({
      displayName: "Billing Email",
      description: "Billing email address",
      required: !1
    }),
    phone_number: import_pieces_framework3.Property.ShortText({
      displayName: "Phone Number",
      description: "Telephone number",
      required: !1
    }),
    mobile: import_pieces_framework3.Property.ShortText({
      displayName: "Mobile",
      description: "Mobile number",
      required: !1
    }),
    address1: import_pieces_framework3.Property.ShortText({
      displayName: "Address Line 1",
      description: "First line of the address",
      required: !1
    }),
    address2: import_pieces_framework3.Property.ShortText({
      displayName: "Address Line 2",
      description: "Second line of the address",
      required: !1
    }),
    address3: import_pieces_framework3.Property.ShortText({
      displayName: "Address Line 3",
      description: "Third line of the address",
      required: !1
    }),
    town: import_pieces_framework3.Property.ShortText({
      displayName: "Town",
      description: "Town or city",
      required: !1
    }),
    region: import_pieces_framework3.Property.ShortText({
      displayName: "Region",
      description: "Region or state",
      required: !1
    }),
    postcode: import_pieces_framework3.Property.ShortText({
      displayName: "Postcode",
      description: "Post or ZIP code",
      required: !1
    }),
    country: import_pieces_framework3.Property.ShortText({
      displayName: "Country",
      description: "Country",
      required: !1
    }),
    contact_name_on_invoices: import_pieces_framework3.Property.Checkbox({
      displayName: "Show Contact Name on Invoices",
      description: "Show contact name as well as organization name on invoices",
      required: !1,
      defaultValue: !1
    }),
    default_payment_terms_in_days: import_pieces_framework3.Property.Number({
      displayName: "Default Payment Terms (Days)",
      description: "Default payment terms in days",
      required: !1
    }),
    charge_sales_tax: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Charge Sales Tax",
      description: "How to handle sales tax for this contact",
      required: !1,
      options: {
        options: [
          { label: "Auto", value: "Auto" },
          { label: "Always", value: "Always" },
          { label: "Never", value: "Never" }
        ]
      },
      defaultValue: "Auto"
    }),
    sales_tax_registration_number: import_pieces_framework3.Property.ShortText({
      displayName: "Sales Tax Registration Number",
      description: "Sales tax registration number if applicable",
      required: !1
    }),
    status: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Status",
      description: "Contact status",
      required: !1,
      options: {
        options: [
          { label: "Active", value: "Active" },
          { label: "Hidden", value: "Hidden" }
        ]
      },
      defaultValue: "Active"
    }),
    locale: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Locale",
      description: "Invoice/estimate language",
      required: !1,
      options: {
        options: [
          { label: "English", value: "en" },
          { label: "English (US)", value: "en-US" },
          { label: "Spanish", value: "es" },
          { label: "French", value: "fr" },
          { label: "German", value: "de" },
          { label: "Dutch", value: "nl" },
          { label: "Italian", value: "it" },
          { label: "Portuguese (Brazil)", value: "pt-BR" }
        ]
      },
      defaultValue: "en"
    })
  },
  async run(context) {
    let {
      first_name,
      last_name,
      organisation_name,
      email,
      billing_email,
      phone_number,
      mobile,
      address1,
      address2,
      address3,
      town,
      region,
      postcode,
      country,
      contact_name_on_invoices,
      default_payment_terms_in_days,
      charge_sales_tax,
      sales_tax_registration_number,
      status,
      locale
    } = context.propsValue, auth = context.auth, payload = {
      contact: {}
    };
    return first_name && (payload.contact.first_name = first_name), last_name && (payload.contact.last_name = last_name), organisation_name && (payload.contact.organisation_name = organisation_name), email && (payload.contact.email = email), billing_email && (payload.contact.billing_email = billing_email), phone_number && (payload.contact.phone_number = phone_number), mobile && (payload.contact.mobile = mobile), address1 && (payload.contact.address1 = address1), address2 && (payload.contact.address2 = address2), address3 && (payload.contact.address3 = address3), town && (payload.contact.town = town), region && (payload.contact.region = region), postcode && (payload.contact.postcode = postcode), country && (payload.contact.country = country), contact_name_on_invoices !== void 0 && (payload.contact.contact_name_on_invoices = contact_name_on_invoices), default_payment_terms_in_days !== void 0 && (payload.contact.default_payment_terms_in_days = default_payment_terms_in_days), charge_sales_tax && (payload.contact.charge_sales_tax = charge_sales_tax), sales_tax_registration_number && (payload.contact.sales_tax_registration_number = sales_tax_registration_number), status && (payload.contact.status = status), locale && (payload.contact.locale = locale), (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: "https://api.freeagent.com/v2/contacts",
      body: payload,
      authentication: {
        type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      },
      headers: {
        "Content-Type": "application/json"
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/free-agent/src/lib/triggers/new-invoice.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var props = {
  status: import_pieces_framework4.Property.StaticDropdown({
    displayName: "Invoice Status",
    description: "Filter invoices by status",
    required: !1,
    options: {
      options: [
        { label: "All Invoices", value: "all" },
        { label: "Open", value: "open" },
        { label: "Overdue", value: "overdue" },
        { label: "Open or Overdue", value: "open_or_overdue" },
        { label: "Draft", value: "draft" },
        { label: "Paid", value: "paid" },
        { label: "Scheduled to Email", value: "scheduled_to_email" }
      ]
    },
    defaultValue: "all"
  }),
  include_items: import_pieces_framework4.Property.Checkbox({
    displayName: "Include Invoice Items",
    description: "Include invoice items in the response",
    required: !1,
    defaultValue: !1
  })
}, polling = {
  strategy: import_pieces_common4.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => {
    let { status, include_items } = propsValue, url = "https://api.freeagent.com/v2/invoices?";
    if (status && status !== "all" && (url += `view=${status}&`), lastFetchEpochMS > 0) {
      let updatedSince = new Date(lastFetchEpochMS).toISOString();
      url += `updated_since=${encodeURIComponent(updatedSince)}&`;
    }
    return include_items && (url += "nested_invoice_items=true&"), url = url.replace(/&$/, ""), ((await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.GET,
      url,
      authentication: {
        type: import_pieces_common4.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      }
    })).body.invoices || []).map((invoice) => ({
      epochMilliSeconds: new Date(invoice.created_at).getTime(),
      data: invoice
    }));
  }
}, freeAgentNewInvoiceTrigger = (0, import_pieces_framework4.createTrigger)({
  auth: freeAgentAuth,
  name: "new_invoice",
  displayName: "New Invoice",
  description: "Triggers when a new invoice is created",
  aiMetadata: {
    description: "Fires when a new invoice appears in FreeAgent, detected by polling. Can be filtered to a specific invoice status (open, overdue, draft, paid, etc.) or left to watch all invoices."
  },
  type: import_pieces_framework4.TriggerStrategy.POLLING,
  props,
  sampleData: {
    url: "https://api.freeagent.com/v2/invoices/1",
    contact: "https://api.freeagent.com/v2/contacts/2",
    dated_on: "2011-08-29",
    due_on: "2011-09-28",
    reference: "001",
    currency: "GBP",
    exchange_rate: "1.0",
    net_value: "100.0",
    sales_tax_value: "20.0",
    total_value: "120.0",
    paid_value: "0.0",
    due_value: "120.0",
    status: "Open",
    comments: "An example invoice comment.",
    payment_terms_in_days: 30,
    created_at: "2011-08-29T00:00:00Z",
    updated_at: "2011-08-29T00:00:00Z"
  },
  async onEnable(context) {
    await import_pieces_common4.pollingHelper.onEnable(polling, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async onDisable(context) {
    await import_pieces_common4.pollingHelper.onDisable(polling, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async run(context) {
    return await import_pieces_common4.pollingHelper.poll(polling, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue,
      files: context.files
    });
  },
  async test(context) {
    return await import_pieces_common4.pollingHelper.test(polling, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue,
      files: context.files
    });
  }
});

// ../ap/packages/pieces/community/free-agent/src/lib/triggers/new-contact.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var props2 = {
  status: import_pieces_framework5.Property.StaticDropdown({
    displayName: "Contact Status",
    description: "Filter contacts by status",
    required: !1,
    options: {
      options: [
        { label: "All Contacts", value: "all" },
        { label: "Active", value: "active" },
        { label: "Clients", value: "clients" },
        { label: "Suppliers", value: "suppliers" },
        { label: "Active Projects", value: "active_projects" },
        { label: "Completed Projects", value: "completed_projects" },
        { label: "Open Clients", value: "open_clients" },
        { label: "Open Suppliers", value: "open_suppliers" },
        { label: "Hidden", value: "hidden" }
      ]
    },
    defaultValue: "all"
  })
}, polling2 = {
  strategy: import_pieces_common5.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => {
    let { status } = propsValue, url = "https://api.freeagent.com/v2/contacts?";
    if (status && status !== "all" && (url += `view=${status}&`), lastFetchEpochMS > 0) {
      let updatedSince = new Date(lastFetchEpochMS).toISOString();
      url += `updated_since=${encodeURIComponent(updatedSince)}&`;
    }
    return url = url.replace(/&$/, ""), ((await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.GET,
      url,
      authentication: {
        type: import_pieces_common5.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      }
    })).body.contacts || []).map((contact) => ({
      epochMilliSeconds: new Date(contact.created_at).getTime(),
      data: contact
    }));
  }
}, freeAgentNewContactTrigger = (0, import_pieces_framework5.createTrigger)({
  auth: freeAgentAuth,
  name: "new_contact",
  displayName: "New Contact",
  description: "Triggers when a new contact is created",
  aiMetadata: {
    description: "Fires when a new contact appears in FreeAgent, detected by polling. Can be filtered to a contact view (active, clients, suppliers, hidden, project-based, etc.) or left to watch all contacts."
  },
  type: import_pieces_framework5.TriggerStrategy.POLLING,
  props: props2,
  sampleData: {
    url: "https://api.freeagent.com/v2/contacts/2",
    first_name: "test",
    last_name: "me",
    organisation_name: "Acme Ltd",
    email: "test@example.com",
    billing_email: "billing@example.com",
    phone_number: "12345678",
    mobile: "9876543210",
    address1: "11 George Street",
    address2: "South Court",
    address3: "Flat 6",
    town: "London",
    region: "Southwark",
    postcode: "SE1 6HA",
    country: "United Kingdom",
    contact_name_on_invoices: !0,
    default_payment_terms_in_days: 30,
    locale: "en",
    status: "Active",
    created_at: "2011-09-14T16:00:41Z",
    updated_at: "2011-09-16T09:34:41Z"
  },
  async onEnable(context) {
    await import_pieces_common5.pollingHelper.onEnable(polling2, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async onDisable(context) {
    await import_pieces_common5.pollingHelper.onDisable(polling2, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async run(context) {
    return await import_pieces_common5.pollingHelper.poll(polling2, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue,
      files: context.files
    });
  },
  async test(context) {
    return await import_pieces_common5.pollingHelper.test(polling2, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue,
      files: context.files
    });
  }
});

// ../ap/packages/pieces/community/free-agent/src/lib/triggers/new-user.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var props3 = {
  view: import_pieces_framework6.Property.StaticDropdown({
    displayName: "User Type",
    description: "Filter users by type",
    required: !1,
    options: {
      options: [
        { label: "All Users", value: "all" },
        { label: "Staff", value: "staff" },
        { label: "Active Staff", value: "active_staff" },
        { label: "Advisors", value: "advisors" },
        { label: "Active Advisors", value: "active_advisors" }
      ]
    },
    defaultValue: "all"
  })
}, polling3 = {
  strategy: import_pieces_common6.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => {
    let { view } = propsValue, url = "https://api.freeagent.com/v2/users?";
    if (view && view !== "all" && (url += `view=${view}&`), lastFetchEpochMS > 0) {
      let updatedSince = new Date(lastFetchEpochMS).toISOString();
      url += `updated_since=${encodeURIComponent(updatedSince)}&`;
    }
    return url = url.replace(/&$/, ""), ((await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.GET,
      url,
      authentication: {
        type: import_pieces_common6.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      }
    })).body.users || []).map((user) => ({
      epochMilliSeconds: new Date(user.created_at).getTime(),
      data: user
    }));
  }
}, freeAgentNewUserTrigger = (0, import_pieces_framework6.createTrigger)({
  auth: freeAgentAuth,
  name: "new_user",
  displayName: "New User",
  description: "Triggers when a new user is added",
  aiMetadata: {
    description: "Fires when a new user is added to the FreeAgent account, detected by polling. Can be filtered by user type (staff, advisors, active staff/advisors) or left to watch all users."
  },
  type: import_pieces_framework6.TriggerStrategy.POLLING,
  props: props3,
  sampleData: {
    url: "https://api.freeagent.com/v2/users/1",
    first_name: "Development",
    last_name: "Team",
    email: "dev@example.com",
    role: "Director",
    permission_level: 8,
    ni_number: "QQ123456C",
    unique_tax_reference: "1234567890",
    opening_mileage: 0,
    created_at: "2011-07-28T11:25:11Z",
    updated_at: "2011-08-24T08:10:23Z"
  },
  async onEnable(context) {
    await import_pieces_common6.pollingHelper.onEnable(polling3, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async onDisable(context) {
    await import_pieces_common6.pollingHelper.onDisable(polling3, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async run(context) {
    return await import_pieces_common6.pollingHelper.poll(polling3, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue,
      files: context.files
    });
  },
  async test(context) {
    return await import_pieces_common6.pollingHelper.test(polling3, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue,
      files: context.files
    });
  }
});

// ../ap/packages/pieces/community/free-agent/src/lib/triggers/new-task.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var props4 = {
  status: import_pieces_framework7.Property.StaticDropdown({
    displayName: "Task Status",
    description: "Filter tasks by status",
    required: !1,
    options: {
      options: [
        { label: "All Tasks", value: "all" },
        { label: "Active", value: "active" },
        { label: "Completed", value: "completed" },
        { label: "Hidden", value: "hidden" }
      ]
    },
    defaultValue: "all"
  })
}, polling4 = {
  strategy: import_pieces_common7.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => {
    let { status } = propsValue, url = "https://api.freeagent.com/v2/tasks?";
    if (status && status !== "all" && (url += `view=${status}&`), lastFetchEpochMS > 0) {
      let updatedSince = new Date(lastFetchEpochMS).toISOString();
      url += `updated_since=${encodeURIComponent(updatedSince)}&`;
    }
    return url = url.replace(/&$/, ""), ((await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.GET,
      url,
      authentication: {
        type: import_pieces_common7.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      }
    })).body.tasks || []).map((task) => ({
      epochMilliSeconds: new Date(task.created_at).getTime(),
      data: task
    }));
  }
}, freeAgentNewTaskTrigger = (0, import_pieces_framework7.createTrigger)({
  auth: freeAgentAuth,
  name: "new_task",
  displayName: "New Task",
  description: "Triggers when a new task is added",
  aiMetadata: {
    description: "Fires when a new task is added in FreeAgent, detected by polling. Can be filtered by task status (active, completed, hidden) or left to watch all tasks."
  },
  type: import_pieces_framework7.TriggerStrategy.POLLING,
  props: props4,
  sampleData: {
    url: "https://api.freeagent.com/v2/tasks/1",
    project: "https://api.freeagent.com/v2/projects/1",
    name: "Sample Task",
    currency: "GBP",
    is_billable: !0,
    billing_rate: "0.0",
    billing_period: "hour",
    status: "Active",
    created_at: "2011-08-16T11:06:57Z",
    updated_at: "2011-08-16T11:06:57Z",
    is_deletable: !1
  },
  async onEnable(context) {
    await import_pieces_common7.pollingHelper.onEnable(polling4, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async onDisable(context) {
    await import_pieces_common7.pollingHelper.onDisable(polling4, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async run(context) {
    return await import_pieces_common7.pollingHelper.poll(polling4, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue,
      files: context.files
    });
  },
  async test(context) {
    return await import_pieces_common7.pollingHelper.test(polling4, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue,
      files: context.files
    });
  }
});

// ../ap/packages/pieces/community/free-agent/src/index.ts
var freeAgent = (0, import_pieces_framework8.createPiece)({
  displayName: "FreeAgent",
  description: "Accounting and invoicing software for small businesses",
  auth: freeAgentAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/free-agent.png",
  authors: ["onyedikachi-david"],
  categories: [import_pieces_framework9.PieceCategory.ACCOUNTING],
  actions: [freeAgentCreateTask, freeAgentCreateContact],
  triggers: [freeAgentNewInvoiceTrigger, freeAgentNewContactTrigger, freeAgentNewUserTrigger, freeAgentNewTaskTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  freeAgent
});
