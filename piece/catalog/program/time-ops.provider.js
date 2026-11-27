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

// ../ap/packages/pieces/community/time-ops/src/index.ts
var index_exports = {};
__export(index_exports, {
  timeOps: () => timeOps,
  timeOpsAuth: () => timeOpsAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/time-ops/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.timeops.dk/api/v1", timeOpsClient = {
  async makeRequest(auth, method, endpoint, body) {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${endpoint}`,
      headers: {
        "x-api-key": auth
      },
      body
    })).body;
  }
};

// ../ap/packages/pieces/community/time-ops/src/lib/actions/create-customer.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var createCustomer = (0, import_pieces_framework.createAction)({
  auth: timeOpsAuth,
  name: "create_customer",
  displayName: "Create Customer",
  description: "Creates a customer.",
  audience: "both",
  aiMetadata: { description: "Creates a new customer record in TimeOps, optionally with a name, VAT number, and default hourly rate. Use when onboarding a client into TimeOps before creating projects or registrations for them. Not idempotent: each call creates another customer with a new id even if the fields are identical.", idempotent: !1 },
  props: {
    name: import_pieces_framework.Property.ShortText({
      displayName: "Name",
      description: "The name of the customer.",
      required: !1
    }),
    vatNumber: import_pieces_framework.Property.ShortText({
      displayName: "VAT Number",
      description: "The VAT number of the customer.",
      required: !1
    }),
    defaultRate: import_pieces_framework.Property.Number({
      displayName: "Default Rate",
      description: "The default hourly rate for this customer.",
      required: !1
    })
  },
  async run(context) {
    let { name, vatNumber, defaultRate } = context.propsValue;
    return await timeOpsClient.makeRequest(
      context.auth.secret_text,
      import_pieces_common2.HttpMethod.POST,
      "/customers",
      {
        name: name != null ? name : null,
        vatNumber: vatNumber != null ? vatNumber : null,
        defaultRate: defaultRate != null ? defaultRate : null
      }
    );
  }
});

// ../ap/packages/pieces/community/time-ops/src/lib/actions/create-project.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var createProject = (0, import_pieces_framework2.createAction)({
  auth: timeOpsAuth,
  name: "create_project",
  displayName: "Create Project",
  description: "Create a project.",
  audience: "both",
  aiMetadata: { description: "Creates a new project in TimeOps under a specific customer, with an optional billable flag, hourly rate, and finished date. Use when setting up a project to track time against; requires a project name and the id of an existing customer. Not idempotent: each call creates another project with a new id.", idempotent: !1 },
  props: {
    name: import_pieces_framework2.Property.ShortText({
      displayName: "Name",
      description: "The name of the project.",
      required: !0
    }),
    customerId: import_pieces_framework2.Property.Dropdown({
      displayName: "Customer",
      description: "The customer this project belongs to.",
      auth: timeOpsAuth,
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await import_pieces_common3.httpClient.sendRequest({
          method: import_pieces_common3.HttpMethod.GET,
          url: `${BASE_URL}/Customers`,
          headers: {
            "x-api-key": auth.secret_text
          }
        })).body.map((customer) => {
          var _a;
          return {
            label: (_a = customer.name) != null ? _a : `Customer ${customer.id}`,
            value: customer.id
          };
        })
      } : {
        disabled: !0,
        placeholder: "Please connect your account.",
        options: []
      }
    }),
    billable: import_pieces_framework2.Property.Checkbox({
      displayName: "Billable",
      description: "Whether the project is billable.",
      required: !1,
      defaultValue: !1
    }),
    rate: import_pieces_framework2.Property.Number({
      displayName: "Rate",
      description: "The hourly rate for this project.",
      required: !1
    }),
    finishedAt: import_pieces_framework2.Property.DateTime({
      displayName: "Finished At",
      description: "The date and time when the project was finished.",
      required: !1
    })
  },
  async run(context) {
    let { name, customerId, billable, rate, finishedAt } = context.propsValue;
    return await timeOpsClient.makeRequest(
      context.auth.secret_text,
      import_pieces_common3.HttpMethod.POST,
      "/Projects",
      {
        name,
        customerId,
        billable: billable != null ? billable : !1,
        rate: rate != null ? rate : null,
        finishedAt: finishedAt != null ? finishedAt : null
      }
    );
  }
});

// ../ap/packages/pieces/community/time-ops/src/lib/actions/start-timer.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var startTimer = (0, import_pieces_framework3.createAction)({
  auth: timeOpsAuth,
  name: "start_timer",
  displayName: "Start Timer",
  description: "Starts the timer by creating a new Registration.",
  audience: "both",
  aiMetadata: { description: "Starts a running time entry (registration) for a given user in TimeOps, optionally tied to a project and tagged as billable. Use to begin live time tracking when the stop time is not yet known; pair with Stop Timer to close it out. Requires the user id. Not idempotent: each call opens another running registration.", idempotent: !1 },
  props: {
    userId: import_pieces_framework3.Property.Dropdown({
      displayName: "User",
      description: "The user to start the timer for.",
      auth: timeOpsAuth,
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await import_pieces_common4.httpClient.sendRequest({
          method: import_pieces_common4.HttpMethod.GET,
          url: `${BASE_URL}/Users`,
          headers: {
            "x-api-key": auth.secret_text
          }
        })).body.map((user) => {
          var _a;
          return {
            label: (_a = user.name) != null ? _a : `User ${user.id}`,
            value: user.id
          };
        })
      } : {
        disabled: !0,
        placeholder: "Please connect your account.",
        options: []
      }
    }),
    projectId: import_pieces_framework3.Property.Dropdown({
      displayName: "Project",
      description: "The project to associate with this timer.",
      auth: timeOpsAuth,
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await import_pieces_common4.httpClient.sendRequest({
          method: import_pieces_common4.HttpMethod.GET,
          url: `${BASE_URL}/Projects`,
          headers: {
            "x-api-key": auth.secret_text
          }
        })).body.map((project) => {
          var _a;
          return {
            label: (_a = project.name) != null ? _a : `Project ${project.id}`,
            value: project.id
          };
        })
      } : {
        disabled: !0,
        placeholder: "Please connect your account.",
        options: []
      }
    }),
    description: import_pieces_framework3.Property.LongText({
      displayName: "Description",
      description: "Description of the time entry.",
      required: !1
    }),
    billable: import_pieces_framework3.Property.Checkbox({
      displayName: "Billable",
      description: "Whether this time entry is billable.",
      required: !1,
      defaultValue: !1
    }),
    tags: import_pieces_framework3.Property.Array({
      displayName: "Tags",
      description: "Tag IDs to associate with this timer.",
      required: !1
    })
  },
  async run(context) {
    var _a;
    let { userId, projectId, description, billable, tags } = context.propsValue, tagIds = (_a = tags == null ? void 0 : tags.map((tag) => parseInt(tag, 10)).filter((id) => !isNaN(id))) != null ? _a : null;
    return await timeOpsClient.makeRequest(
      context.auth.secret_text,
      import_pieces_common4.HttpMethod.POST,
      `/Registrations/start/${userId}`,
      {
        description: description != null ? description : null,
        projectId: projectId != null ? projectId : null,
        billable: billable != null ? billable : !1,
        tags: tagIds
      }
    );
  }
});

// ../ap/packages/pieces/community/time-ops/src/lib/actions/create-registration.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var createRegistration = (0, import_pieces_framework4.createAction)({
  auth: timeOpsAuth,
  name: "create_registration",
  displayName: "Create Registration",
  description: "Creates a registration.",
  audience: "both",
  aiMetadata: { description: "Creates a complete time-entry registration in TimeOps with an explicit start (and optional stop) time for a given user, optionally tied to a project, description, billable flag, and tags. Use to log a past or fully specified time entry rather than tracking live (unlike Start Timer / Stop Timer). Requires the user id and a start time. Not idempotent: each call creates another registration with a new id.", idempotent: !1 },
  props: {
    userId: import_pieces_framework4.Property.Dropdown({
      displayName: "User",
      description: "The user for this registration.",
      auth: timeOpsAuth,
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await import_pieces_common5.httpClient.sendRequest({
          method: import_pieces_common5.HttpMethod.GET,
          url: `${BASE_URL}/Users`,
          headers: {
            "x-api-key": auth.secret_text
          }
        })).body.map((user) => {
          var _a;
          return {
            label: (_a = user.name) != null ? _a : `User ${user.id}`,
            value: user.id
          };
        })
      } : {
        disabled: !0,
        placeholder: "Please connect your account.",
        options: []
      }
    }),
    startedAt: import_pieces_framework4.Property.DateTime({
      displayName: "Started At",
      description: "The start date and time of the registration.",
      required: !0
    }),
    stoppedAt: import_pieces_framework4.Property.DateTime({
      displayName: "Stopped At",
      description: "The end date and time of the registration.",
      required: !1
    }),
    projectId: import_pieces_framework4.Property.Dropdown({
      displayName: "Project",
      description: "The project to associate with this registration.",
      auth: timeOpsAuth,
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await import_pieces_common5.httpClient.sendRequest({
          method: import_pieces_common5.HttpMethod.GET,
          url: `${BASE_URL}/Projects`,
          headers: {
            "x-api-key": auth.secret_text
          }
        })).body.map((project) => {
          var _a;
          return {
            label: (_a = project.name) != null ? _a : `Project ${project.id}`,
            value: project.id
          };
        })
      } : {
        disabled: !0,
        placeholder: "Please connect your account.",
        options: []
      }
    }),
    description: import_pieces_framework4.Property.LongText({
      displayName: "Description",
      description: "Description of the registration.",
      required: !1
    }),
    billable: import_pieces_framework4.Property.Checkbox({
      displayName: "Billable",
      description: "Whether this registration is billable.",
      required: !1,
      defaultValue: !1
    }),
    tags: import_pieces_framework4.Property.Array({
      displayName: "Tags",
      description: "Tag IDs to associate with this registration.",
      required: !1
    })
  },
  async run(context) {
    var _a;
    let { userId, startedAt, stoppedAt, projectId, description, billable, tags } = context.propsValue, tagIds = (_a = tags == null ? void 0 : tags.map((tag) => parseInt(tag, 10)).filter((id) => !isNaN(id))) != null ? _a : null;
    return await timeOpsClient.makeRequest(
      context.auth.secret_text,
      import_pieces_common5.HttpMethod.POST,
      "/Registrations",
      {
        userId,
        startedAt,
        stoppedAt: stoppedAt != null ? stoppedAt : null,
        projectId: projectId != null ? projectId : null,
        description: description != null ? description : null,
        billable: billable != null ? billable : !1,
        tags: tagIds
      }
    );
  }
});

// ../ap/packages/pieces/community/time-ops/src/lib/actions/stop-timer.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var stopTimer = (0, import_pieces_framework5.createAction)({
  auth: timeOpsAuth,
  name: "stop_timer",
  displayName: "Stop Timer",
  description: "Stop the currently running timer.",
  audience: "both",
  aiMetadata: { description: "Stops the currently running timer (open registration) for a given user in TimeOps, closing out the time entry. Use after Start Timer to finalize a live time entry; requires the user id. Not idempotent: it mutates the open registration, and a repeat call with no timer running has no entry to close.", idempotent: !1 },
  props: {
    userId: import_pieces_framework5.Property.Dropdown({
      displayName: "User",
      description: "The user to stop the timer for.",
      auth: timeOpsAuth,
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await import_pieces_common6.httpClient.sendRequest({
          method: import_pieces_common6.HttpMethod.GET,
          url: `${BASE_URL}/Users`,
          headers: {
            "x-api-key": auth.secret_text
          }
        })).body.map((user) => {
          var _a;
          return {
            label: (_a = user.name) != null ? _a : `User ${user.id}`,
            value: user.id
          };
        })
      } : {
        disabled: !0,
        placeholder: "Please connect your account.",
        options: []
      }
    })
  },
  async run(context) {
    let { userId } = context.propsValue;
    return await timeOpsClient.makeRequest(
      context.auth.secret_text,
      import_pieces_common6.HttpMethod.POST,
      `/Registrations/stop/${userId}`,
      {}
    );
  }
});

// ../ap/packages/pieces/community/time-ops/src/lib/triggers/new-customer.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var polling = {
  strategy: import_pieces_common7.DedupeStrategy.LAST_ITEM,
  items: async ({ auth }) => {
    var _a;
    return ((_a = (await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.GET,
      url: `${BASE_URL}/Customers`,
      headers: {
        "x-api-key": auth.secret_text
      }
    })).body) != null ? _a : []).sort((a, b) => b.id - a.id).map((customer) => ({
      id: customer.id,
      data: customer
    }));
  }
}, newCustomer = (0, import_pieces_framework6.createTrigger)({
  auth: timeOpsAuth,
  name: "new_customer",
  displayName: "New Customer",
  description: "Triggers when a new customer is created.",
  aiMetadata: {
    description: "Fires when a new customer is added in TimeOps. Polls the customer list and emits each newly seen customer record. Use to react to client onboarding, such as setting up a default project or notifying a team."
  },
  type: import_pieces_framework6.TriggerStrategy.POLLING,
  props: {},
  sampleData: {
    id: 1,
    vatNumber: "DK12345678",
    name: "Example Customer",
    defaultRate: 100
  },
  async onEnable(context) {
    await import_pieces_common7.pollingHelper.onEnable(polling, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async onDisable(context) {
    await import_pieces_common7.pollingHelper.onDisable(polling, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async run(context) {
    return await import_pieces_common7.pollingHelper.poll(polling, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue,
      files: context.files
    });
  },
  async test(context) {
    return await import_pieces_common7.pollingHelper.test(polling, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue,
      files: context.files
    });
  }
});

// ../ap/packages/pieces/community/time-ops/src/lib/triggers/new-project.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var polling2 = {
  strategy: import_pieces_common8.DedupeStrategy.LAST_ITEM,
  items: async ({ auth }) => {
    var _a;
    return ((_a = (await import_pieces_common8.httpClient.sendRequest({
      method: import_pieces_common8.HttpMethod.GET,
      url: `${BASE_URL}/Projects`,
      headers: {
        "x-api-key": auth.secret_text
      }
    })).body) != null ? _a : []).sort((a, b) => b.id - a.id).map((project) => ({
      id: project.id,
      data: project
    }));
  }
}, newProject = (0, import_pieces_framework7.createTrigger)({
  auth: timeOpsAuth,
  name: "new_project",
  displayName: "New Project",
  description: "Triggers when a new project is created.",
  aiMetadata: {
    description: "Fires when a new project is created in TimeOps. Polls the project list and emits each newly seen project record, including its customer association and billing settings. Use to react to project kickoff, such as creating tasks or alerting stakeholders."
  },
  type: import_pieces_framework7.TriggerStrategy.POLLING,
  props: {},
  sampleData: {
    id: 1,
    name: "Example Project",
    customerId: 1,
    finishedAt: null,
    billable: !0,
    rate: 100
  },
  async onEnable(context) {
    await import_pieces_common8.pollingHelper.onEnable(polling2, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async onDisable(context) {
    await import_pieces_common8.pollingHelper.onDisable(polling2, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async run(context) {
    return await import_pieces_common8.pollingHelper.poll(polling2, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue,
      files: context.files
    });
  },
  async test(context) {
    return await import_pieces_common8.pollingHelper.test(polling2, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue,
      files: context.files
    });
  }
});

// ../ap/packages/pieces/community/time-ops/src/lib/triggers/new-user.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var polling3 = {
  strategy: import_pieces_common9.DedupeStrategy.LAST_ITEM,
  items: async ({ auth }) => {
    var _a;
    return ((_a = (await import_pieces_common9.httpClient.sendRequest({
      method: import_pieces_common9.HttpMethod.GET,
      url: `${BASE_URL}/users`,
      headers: {
        "x-api-key": auth.secret_text
      }
    })).body) != null ? _a : []).sort((a, b) => b.id - a.id).map((user) => ({
      id: user.id,
      data: user
    }));
  }
}, newUser = (0, import_pieces_framework8.createTrigger)({
  auth: timeOpsAuth,
  name: "new_user",
  displayName: "New User",
  description: "Triggers when a new user has been created.",
  aiMetadata: {
    description: "Fires when a new user is added to the TimeOps account. Polls the user list and emits each newly seen user record, including name, email, and access level. Use to automate team-member onboarding such as welcome messages or provisioning."
  },
  type: import_pieces_framework8.TriggerStrategy.POLLING,
  props: {},
  sampleData: {
    id: 1,
    name: "John Doe",
    eMail: "john@example.com",
    accessLevel: "User",
    deactivatedAt: null
  },
  async onEnable(context) {
    await import_pieces_common9.pollingHelper.onEnable(polling3, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async onDisable(context) {
    await import_pieces_common9.pollingHelper.onDisable(polling3, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async run(context) {
    return await import_pieces_common9.pollingHelper.poll(polling3, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue,
      files: context.files
    });
  },
  async test(context) {
    return await import_pieces_common9.pollingHelper.test(polling3, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue,
      files: context.files
    });
  }
});

// ../ap/packages/pieces/community/time-ops/src/lib/triggers/new-registration.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var polling4 = {
  strategy: import_pieces_common10.DedupeStrategy.LAST_ITEM,
  items: async ({ auth }) => {
    var _a;
    return ((_a = (await import_pieces_common10.httpClient.sendRequest({
      method: import_pieces_common10.HttpMethod.GET,
      url: `${BASE_URL}/Registrations`,
      headers: {
        "x-api-key": auth.secret_text
      },
      queryParams: {
        pageSize: "100",
        page: "0"
      }
    })).body.results) != null ? _a : []).sort((a, b) => b.id - a.id).map((registration) => ({
      id: registration.id,
      data: registration
    }));
  }
}, newRegistration = (0, import_pieces_framework9.createTrigger)({
  auth: timeOpsAuth,
  name: "new_registration",
  displayName: "New Registration",
  description: "Triggers when new registrations are added.",
  aiMetadata: {
    description: "Fires when a new time-entry registration is added in TimeOps. Polls the registrations list and emits each newly seen registration, including its user, project, times, and billable flag. Use to react to logged time, such as updating reports or invoicing."
  },
  type: import_pieces_framework9.TriggerStrategy.POLLING,
  props: {},
  sampleData: {
    id: 1,
    description: "Working on project",
    startedAt: "2024-01-15T09:00:00Z",
    stoppedAt: "2024-01-15T17:00:00Z",
    projectId: 1,
    userId: 1,
    billable: !0,
    tags: []
  },
  async onEnable(context) {
    await import_pieces_common10.pollingHelper.onEnable(polling4, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async onDisable(context) {
    await import_pieces_common10.pollingHelper.onDisable(polling4, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async run(context) {
    return await import_pieces_common10.pollingHelper.poll(polling4, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue,
      files: context.files
    });
  },
  async test(context) {
    return await import_pieces_common10.pollingHelper.test(polling4, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue,
      files: context.files
    });
  }
});

// ../ap/packages/pieces/community/time-ops/src/index.ts
var timeOpsAuth = import_pieces_framework10.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Enter your TimeOps API key. You can find it in your TimeOps account settings.",
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common11.httpClient.sendRequest({
        method: import_pieces_common11.HttpMethod.GET,
        url: `${BASE_URL}/customers`,
        headers: {
          "x-api-key": auth
        }
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API key."
      };
    }
  }
}), timeOps = (0, import_pieces_framework10.createPiece)({
  displayName: "TimeOps",
  description: "Time tracking and project management for teams and freelancers.",
  auth: timeOpsAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/time-ops.png",
  authors: ["onyedikachi-david"],
  categories: [import_pieces_framework11.PieceCategory.PRODUCTIVITY],
  actions: [
    createCustomer,
    createProject,
    startTimer,
    stopTimer,
    createRegistration,
    (0, import_pieces_common11.createCustomApiCallAction)({
      auth: timeOpsAuth,
      baseUrl: () => BASE_URL,
      authMapping: async (auth) => ({
        "x-api-key": auth.secret_text
      })
    })
  ],
  triggers: [newCustomer, newProject, newUser, newRegistration]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  timeOps,
  timeOpsAuth
});
