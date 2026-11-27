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

// ../ap/packages/pieces/community/splitwise/src/index.ts
var index_exports = {};
__export(index_exports, {
  splitwise: () => splitwise
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/splitwise/src/lib/actions/create-expense.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/splitwise/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), markdownDescription = `
You can generate an API key from your app's details page on Splitwise.
The API key should be kept secure as it provides access to your personal account.
`, splitwiseAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: markdownDescription,
  required: !0,
  validate: async ({ auth }) => {
    try {
      let response = await fetch("https://secure.splitwise.com/api/v3.0/get_current_user", {
        method: "GET",
        headers: {
          Authorization: `Bearer ${auth}`,
          "Content-Type": "application/json"
        }
      });
      return response.ok ? {
        valid: !0
      } : {
        valid: !1,
        error: `Invalid API key: ${response.status} ${response.statusText}`
      };
    } catch (error) {
      return {
        valid: !1,
        error: `Connection error: ${error.message}`
      };
    }
  }
});

// ../ap/packages/pieces/community/splitwise/src/lib/common/api.ts
var splitwiseApi = {
  getCategories: async (auth) => {
    let response = await fetch("https://secure.splitwise.com/api/v3.0/get_categories", {
      method: "GET",
      headers: {
        Authorization: `Bearer ${auth}`,
        "Content-Type": "application/json"
      }
    });
    if (!response.ok)
      throw new Error(`Failed to fetch categories: ${response.status} ${response.statusText}`);
    return (await response.json()).categories || [];
  },
  getGroups: async (auth) => {
    let response = await fetch("https://secure.splitwise.com/api/v3.0/get_groups", {
      method: "GET",
      headers: {
        Authorization: `Bearer ${auth}`,
        "Content-Type": "application/json"
      }
    });
    if (!response.ok)
      throw new Error(`Failed to fetch groups: ${response.status} ${response.statusText}`);
    return (await response.json()).groups || [];
  },
  getExpenses: async (auth, groupId, updatedAfter) => {
    let params = new URLSearchParams();
    groupId && params.append("group_id", groupId.toString()), updatedAfter && params.append("updated_after", updatedAfter), params.append("limit", "100");
    let response = await fetch(`https://secure.splitwise.com/api/v3.0/get_expenses?${params}`, {
      method: "GET",
      headers: {
        Authorization: `Bearer ${auth}`,
        "Content-Type": "application/json"
      }
    });
    if (!response.ok)
      throw new Error(`Failed to fetch expenses: ${response.status} ${response.statusText}`);
    return (await response.json()).expenses || [];
  },
  createExpense: async (auth, expenseData) => {
    let response = await fetch("https://secure.splitwise.com/api/v3.0/create_expense", {
      method: "POST",
      headers: {
        Authorization: `Bearer ${auth}`,
        "Content-Type": "application/json"
      },
      body: JSON.stringify(expenseData)
    });
    if (!response.ok)
      throw new Error(`Failed to create expense: ${response.status} ${response.statusText}`);
    let data = await response.json();
    if (data.errors && Object.keys(data.errors).length > 0)
      throw new Error(`Expense creation failed: ${JSON.stringify(data.errors)}`);
    return data;
  }
};

// ../ap/packages/pieces/community/splitwise/src/lib/actions/create-expense.ts
var createExpenseAction = (0, import_pieces_framework2.createAction)({
  auth: splitwiseAuth,
  name: "create_expense",
  displayName: "Create Expense",
  description: "Creates a new expense, optionally in a group or with specific friends",
  audience: "both",
  aiMetadata: { description: 'Records a new shared expense in Splitwise under a group, supporting two split modes: split equally among all group members, or split by custom per-user paid/owed shares (each user identified by Splitwise user ID, or by name/email when no ID is known). Use to log a bill an agent needs to track and settle; a group is required and the cost is a string amount (e.g. "25.50"). Not idempotent \u2014 each call posts a new expense, so repeating it creates duplicates.', idempotent: !1 },
  props: {
    split_type: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Split Type",
      description: "How to split the expense",
      required: !0,
      options: {
        options: [
          { label: "Split equally in group", value: "equal_group_split" },
          { label: "Split by custom shares", value: "by_shares" }
        ]
      },
      defaultValue: "equal_group_split"
    }),
    cost: import_pieces_framework2.Property.ShortText({
      displayName: "Cost",
      description: 'Amount (e.g., "25.50")',
      required: !0
    }),
    description: import_pieces_framework2.Property.ShortText({
      displayName: "Description",
      description: "Short description of the expense",
      required: !0
    }),
    details: import_pieces_framework2.Property.LongText({
      displayName: "Details/Notes",
      description: "Additional notes about the expense",
      required: !1
    }),
    date: import_pieces_framework2.Property.DateTime({
      displayName: "Date",
      description: "Date and time the expense took place",
      required: !1
    }),
    repeat_interval: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Repeat Interval",
      description: "How often to repeat this expense",
      required: !1,
      options: {
        options: [
          { label: "Never", value: "never" },
          { label: "Weekly", value: "weekly" },
          { label: "Fortnightly", value: "fortnightly" },
          { label: "Monthly", value: "monthly" },
          { label: "Yearly", value: "yearly" }
        ]
      },
      defaultValue: "never"
    }),
    currency_code: import_pieces_framework2.Property.ShortText({
      displayName: "Currency Code",
      description: "Currency code (e.g., USD, EUR)",
      required: !1,
      defaultValue: "USD"
    }),
    category_id: import_pieces_framework2.Property.Dropdown({
      displayName: "Category",
      description: "Expense category",
      required: !1,
      auth: splitwiseAuth,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Please connect your account first",
            options: []
          };
        try {
          return {
            options: (await splitwiseApi.getCategories(auth.secret_text)).map((cat) => ({
              label: cat.name,
              value: cat.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            placeholder: "Failed to load categories",
            options: []
          };
        }
      }
    }),
    group_id: import_pieces_framework2.Property.Dropdown({
      displayName: "Group",
      description: "Group to put this expense in (required for equal split)",
      required: !0,
      auth: splitwiseAuth,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Please connect your account first",
            options: []
          };
        try {
          return {
            options: (await splitwiseApi.getGroups(auth.secret_text)).map((group) => ({
              label: group.name,
              value: group.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            placeholder: "Failed to load groups",
            options: []
          };
        }
      }
    }),
    // Custom shares - only shown when split_type is 'by_shares'
    users: import_pieces_framework2.Property.Array({
      displayName: "Users (Custom Split)",
      description: 'Users and their shares for custom split (only used when Split Type is "by_shares")',
      required: !1,
      properties: {
        user_id: import_pieces_framework2.Property.Number({
          displayName: "User ID",
          description: "Splitwise user ID",
          required: !1
        }),
        first_name: import_pieces_framework2.Property.ShortText({
          displayName: "First Name",
          description: "User first name (if no user ID)",
          required: !1
        }),
        last_name: import_pieces_framework2.Property.ShortText({
          displayName: "Last Name",
          description: "User last name (if no user ID)",
          required: !1
        }),
        email: import_pieces_framework2.Property.ShortText({
          displayName: "Email",
          description: "User email (if no user ID)",
          required: !1
        }),
        paid_share: import_pieces_framework2.Property.ShortText({
          displayName: "Paid Share",
          description: 'Amount this user paid (e.g., "25.50")',
          required: !0
        }),
        owed_share: import_pieces_framework2.Property.ShortText({
          displayName: "Owed Share",
          description: 'Amount this user owes (e.g., "12.75")',
          required: !0
        })
      }
    })
  },
  async run(context) {
    let { split_type, cost, description, details, date, repeat_interval, currency_code, category_id, group_id, users } = context.propsValue, expenseData = {
      cost,
      description,
      group_id
    };
    return details && (expenseData.details = details), date && (expenseData.date = date), repeat_interval && repeat_interval !== "never" && (expenseData.repeat_interval = repeat_interval), currency_code && (expenseData.currency_code = currency_code), category_id && (expenseData.category_id = category_id), split_type === "equal_group_split" ? expenseData.split_equally = !0 : split_type === "by_shares" && users && users.length > 0 && (expenseData.split_equally = !1, users.forEach((user, index) => {
      user.user_id && (expenseData[`users__${index}__user_id`] = user.user_id), user.first_name && (expenseData[`users__${index}__first_name`] = user.first_name), user.last_name && (expenseData[`users__${index}__last_name`] = user.last_name), user.email && (expenseData[`users__${index}__email`] = user.email), expenseData[`users__${index}__paid_share`] = user.paid_share, expenseData[`users__${index}__owed_share`] = user.owed_share;
    })), await splitwiseApi.createExpense(context.auth.secret_text, expenseData);
  }
});

// ../ap/packages/pieces/community/splitwise/src/lib/triggers/new-expense.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common = require("@activepieces/pieces-common");
var props = {
  group_id: import_pieces_framework3.Property.Dropdown({
    displayName: "Group",
    description: "Filter expenses from a specific group (optional)",
    required: !1,
    auth: splitwiseAuth,
    refreshers: [],
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          placeholder: "Please connect your account first",
          options: []
        };
      try {
        let groups = await splitwiseApi.getGroups(auth.secret_text);
        return {
          options: [
            { label: "All Groups", value: "" },
            ...groups.map((group) => ({
              label: group.name,
              value: String(group.id)
            }))
          ]
        };
      } catch (error) {
        return {
          disabled: !0,
          placeholder: "Failed to load groups",
          options: []
        };
      }
    }
  })
}, polling = {
  strategy: import_pieces_common.DedupeStrategy.TIMEBASED,
  async items({ auth, lastFetchEpochMS, propsValue }) {
    let updatedAfter = lastFetchEpochMS === 0 ? void 0 : new Date(lastFetchEpochMS).toISOString(), groupIdNum = propsValue.group_id ? parseInt(propsValue.group_id) : void 0;
    return (await splitwiseApi.getExpenses(auth.secret_text, groupIdNum, updatedAfter)).map((expense) => ({
      epochMilliSeconds: new Date(expense.created_at).getTime(),
      data: expense
    }));
  }
}, newExpenseTrigger = (0, import_pieces_framework3.createTrigger)({
  auth: splitwiseAuth,
  name: "new_expense",
  displayName: "New Expense",
  description: "Triggers when a new expense is created. Optionally, filter for a specific group.",
  aiMetadata: {
    description: "Fires when a new expense appears in the connected Splitwise account, representing a freshly recorded shared bill. Optionally scope to a single group; leave the group unset to watch expenses across all groups."
  },
  props,
  type: import_pieces_framework3.TriggerStrategy.POLLING,
  sampleData: {},
  async test(context) {
    return await import_pieces_common.pollingHelper.test(polling, context);
  },
  async onEnable(context) {
    await import_pieces_common.pollingHelper.onEnable(polling, context);
  },
  async onDisable(context) {
    await import_pieces_common.pollingHelper.onDisable(polling, context);
  },
  async run(context) {
    return await import_pieces_common.pollingHelper.poll(polling, context);
  }
});

// ../ap/packages/pieces/community/splitwise/src/index.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var splitwise = (0, import_pieces_framework4.createPiece)({
  displayName: "Splitwise",
  auth: splitwiseAuth,
  minimumSupportedRelease: "0.36.1",
  description: "Splitwise is a expense splitting app that helps you track and settle bills with friends, family, and roommates.",
  categories: [import_pieces_framework5.PieceCategory.ACCOUNTING],
  logoUrl: "https://cdn.activepieces.com/pieces/splitwise.png",
  authors: ["onyedikachi-david"],
  actions: [createExpenseAction],
  triggers: [newExpenseTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  splitwise
});
