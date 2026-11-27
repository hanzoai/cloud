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

// ../ap/packages/pieces/community/weekdone/src/index.ts
var index_exports = {};
__export(index_exports, {
  weekdone: () => weekdone
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework10 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/weekdone/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), weekdoneAuth = import_pieces_framework.PieceAuth.OAuth2({
  required: !0,
  authUrl: "https://weekdone.com/oauth_authorize",
  tokenUrl: "https://weekdone.com/oauth_token",
  scope: []
});

// ../ap/packages/pieces/community/weekdone/src/lib/actions/create-item.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/weekdone/src/lib/common/index.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
async function fetchUsers({ token }) {
  var _a;
  return (_a = (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: `https://api.weekdone.com/1/users?token=${token}`
  })).body.users) != null ? _a : [];
}
async function fetchTeams({ token }) {
  var _a;
  return (_a = (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: `https://api.weekdone.com/1/teams?token=${token}`
  })).body.teams) != null ? _a : [];
}
function userDropdown({
  displayName,
  description,
  required
}) {
  return import_pieces_framework2.Property.Dropdown({
    displayName,
    description,
    required,
    auth: weekdoneAuth,
    refreshers: [],
    options: async ({ auth }) => {
      if (!auth)
        return { disabled: !0, options: [], placeholder: "Please connect your account first." };
      let token = auth.access_token;
      try {
        return {
          disabled: !1,
          options: (await fetchUsers({ token })).map((u) => ({ label: u.name, value: u.id }))
        };
      } catch (e) {
        return { disabled: !0, options: [], placeholder: "Failed to load users." };
      }
    }
  });
}
function teamDropdown({
  displayName,
  description,
  required
}) {
  return import_pieces_framework2.Property.Dropdown({
    displayName,
    description,
    required,
    auth: weekdoneAuth,
    refreshers: [],
    options: async ({ auth }) => {
      if (!auth)
        return { disabled: !0, options: [], placeholder: "Please connect your account first." };
      let token = auth.access_token;
      try {
        return {
          disabled: !1,
          options: (await fetchTeams({ token })).map((t) => ({ label: t.name, value: t.id }))
        };
      } catch (e) {
        return { disabled: !0, options: [], placeholder: "Failed to load teams." };
      }
    }
  });
}
var weekdoneCommon = { fetchUsers, fetchTeams, userDropdown, teamDropdown };

// ../ap/packages/pieces/community/weekdone/src/lib/actions/create-item.ts
var createItemAction = (0, import_pieces_framework3.createAction)({
  auth: weekdoneAuth,
  name: "create_item",
  displayName: "Create New Item",
  description: "Creates a new report item in Weekdone.",
  audience: "both",
  aiMetadata: { description: "Adds a new report item (a plan, progress note, problem, or plan-on-hold entry) to a Weekdone weekly report. Use to log status update entries for a user/team; the item text and report category are required, and it targets the current week unless a YYYYWW week is supplied. Not idempotent \u2014 each call appends a new item.", idempotent: !1 },
  props: {
    description: import_pieces_framework3.Property.LongText({
      displayName: "Description",
      description: "The text content of the report item.",
      required: !0
    }),
    type_id: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Report Category",
      description: "The category this item belongs to in your weekly report. Custom category IDs (4 and above) are available via the Custom API Call action.",
      required: !0,
      defaultValue: 2,
      options: {
        options: [
          { label: "Plans", value: 2 },
          { label: "Progress", value: 1 },
          { label: "Problems", value: 3 },
          { label: "Plans on Hold", value: 0 }
        ]
      }
    }),
    period: import_pieces_framework3.Property.ShortText({
      displayName: "Week",
      description: 'The report week to add this item to, in YYYYWW format (e.g. "202403" for week 3 of 2024). Leave empty to use the current week.',
      required: !1
    }),
    user_id: weekdoneCommon.userDropdown({
      displayName: "Assign To",
      description: "The user to assign this item to. Leave empty to assign it to yourself.",
      required: !1
    }),
    team_id: weekdoneCommon.teamDropdown({
      displayName: "Team",
      description: "The team to add this item to. Leave empty to use your primary team.",
      required: !1
    }),
    priority: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Priority",
      description: "The priority level for this item.",
      required: !1,
      defaultValue: 0,
      options: {
        options: [
          { label: "Not specified", value: 0 },
          { label: "Green (low)", value: 1 },
          { label: "Amber (medium)", value: 2 },
          { label: "Red (high)", value: 3 }
        ]
      }
    }),
    due_on: import_pieces_framework3.Property.ShortText({
      displayName: "Due Date",
      description: 'The due date for this item in YYYY-MM-DD format (e.g. "2024-03-15"). Leave empty for no due date.',
      required: !1
    }),
    is_private: import_pieces_framework3.Property.Checkbox({
      displayName: "Private",
      description: "If enabled, only you can see this item. If disabled (default), the item is visible to your team.",
      required: !1,
      defaultValue: !1
    }),
    source_id: import_pieces_framework3.Property.ShortText({
      displayName: "External Source ID",
      description: "The ID of this item in an external system (e.g. a JIRA ticket number or Asana task ID). Maximum 50 characters.",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e;
    let {
      description,
      type_id,
      period,
      user_id,
      team_id,
      priority,
      due_on,
      is_private,
      source_id
    } = context.propsValue, token = context.auth.access_token, body = {
      description,
      type_id
    };
    period && (body.period = period), user_id && (body.user_id = user_id), team_id != null && (body.team_id = team_id), priority != null && (body.priority = priority), due_on && (body.due_on = due_on), is_private != null && (body.private = is_private ? 1 : 0), source_id && (body.source_id = source_id);
    let response = await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: `https://api.weekdone.com/1/item?token=${token}`,
      body
    }), item = (_b = (_a = response.body.item) != null ? _a : response.body.data) != null ? _b : response.body;
    return {
      id: item.id,
      description: item.description,
      type_id: item.type_id,
      inserted: item.inserted,
      due_on: (_c = item.due_on) != null ? _c : null,
      priority: item.priority,
      user_id: item.user_id,
      team_id: item.team_id,
      from_id: (_d = item.from_id) != null ? _d : null,
      source: item.source,
      source_id: (_e = item.source_id) != null ? _e : null,
      is_private: item.private,
      position: item.nr,
      like_count: item.likecount,
      comment_count: item.commentcount
    };
  }
});

// ../ap/packages/pieces/community/weekdone/src/lib/actions/update-item.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var updateItemAction = (0, import_pieces_framework4.createAction)({
  auth: weekdoneAuth,
  name: "update_item",
  displayName: "Update Item",
  description: "Updates an existing report item in Weekdone.",
  audience: "both",
  aiMetadata: { description: "Updates an existing Weekdone report item identified by its item ID, changing fields such as description, report category, priority, or due date. Use when modifying an item the agent already knows the ID of; only supplied fields change, and moving the item to a different category also requires a target week. Idempotent \u2014 re-sending the same field values leaves the item in the same state.", idempotent: !0 },
  props: {
    search_period: import_pieces_framework4.Property.ShortText({
      displayName: "Search Week",
      description: 'Filter the item list by week in YYYYWW format (e.g. "202403" for week 3 of 2024). Leave empty to search the current week.',
      required: !1
    }),
    item_id: import_pieces_framework4.Property.Dropdown({
      displayName: "Item",
      description: 'The report item to update. Change "Search Week" above to find items from other weeks.',
      required: !0,
      refreshers: ["search_period"],
      auth: weekdoneAuth,
      options: async ({ auth, search_period }) => {
        var _a;
        if (!auth)
          return { disabled: !0, options: [], placeholder: "Please connect your account first." };
        let token = auth.access_token, queryParams = { user_id: "me" };
        search_period && (queryParams.period = search_period);
        try {
          let items = (_a = (await import_pieces_common3.httpClient.sendRequest({
            method: import_pieces_common3.HttpMethod.GET,
            url: `https://api.weekdone.com/1/items?token=${token}`,
            queryParams
          })).body.items) != null ? _a : [];
          return items.length === 0 ? {
            disabled: !1,
            options: [],
            placeholder: "No items found for this week. Try a different Search Week."
          } : {
            disabled: !1,
            options: items.map((item) => ({
              label: item.description,
              value: item.id
            }))
          };
        } catch (e) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load items. Check your connection."
          };
        }
      }
    }),
    description: import_pieces_framework4.Property.LongText({
      displayName: "Description",
      description: "The new text content for the item. Leave empty to keep the current value.",
      required: !1
    }),
    type_id: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Report Category",
      description: 'Move this item to a different report category. If you change this, you must also fill in "Target Week" below.',
      required: !1,
      options: {
        options: [
          { label: "Plans", value: 2 },
          { label: "Progress", value: 1 },
          { label: "Problems", value: 3 },
          { label: "Plans on Hold", value: 0 }
        ]
      }
    }),
    move_to_period: import_pieces_framework4.Property.ShortText({
      displayName: "Target Week",
      description: 'Required when changing the Report Category. The week to move the item into, in YYYYWW format (e.g. "202403").',
      required: !1
    }),
    priority: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Priority",
      description: "The new priority level for this item. Leave empty to keep the current value.",
      required: !1,
      options: {
        options: [
          { label: "Not specified", value: 0 },
          { label: "Green (low)", value: 1 },
          { label: "Amber (medium)", value: 2 },
          { label: "Red (high)", value: 3 }
        ]
      }
    }),
    due_on: import_pieces_framework4.Property.ShortText({
      displayName: "Due Date",
      description: 'The new due date in YYYY-MM-DD format (e.g. "2024-03-15"). Leave empty to keep the current value.',
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e;
    let { item_id, description, type_id, move_to_period, priority, due_on } = context.propsValue, token = context.auth.access_token, body = {};
    description && (body.description = description), type_id != null && (body.type_id = type_id, move_to_period && (body.period = move_to_period)), priority != null && (body.priority = priority), due_on && (body.due_on = due_on);
    let response = await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.PATCH,
      url: `https://api.weekdone.com/1/item/${item_id}?token=${token}`,
      body
    }), item = (_b = (_a = response.body.item) != null ? _a : response.body.data) != null ? _b : response.body;
    return {
      id: item.id,
      description: item.description,
      type_id: item.type_id,
      inserted: item.inserted,
      due_on: (_c = item.due_on) != null ? _c : null,
      priority: item.priority,
      user_id: item.user_id,
      team_id: item.team_id,
      from_id: (_d = item.from_id) != null ? _d : null,
      source: item.source,
      source_id: (_e = item.source_id) != null ? _e : null,
      is_private: item.private,
      position: item.nr,
      like_count: item.likecount,
      comment_count: item.commentcount
    };
  }
});

// ../ap/packages/pieces/community/weekdone/src/lib/actions/create-objective.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var createObjectiveAction = (0, import_pieces_framework5.createAction)({
  auth: weekdoneAuth,
  name: "create_objective",
  displayName: "Create New Objective",
  description: "Creates a new OKR objective in Weekdone.",
  audience: "both",
  aiMetadata: { description: "Creates a new OKR objective in Weekdone at a chosen level \u2014 company, department, team, or personal (user) \u2014 which determines the required scoping field (a department ID, team, or team+user). Use to set up a new goal; the objective text and level are required and it targets the current quarter unless a YYYYQ period is supplied. Not idempotent \u2014 each call creates a new objective.", idempotent: !1 },
  props: {
    description: import_pieces_framework5.Property.LongText({
      displayName: "Description",
      description: 'The text of the objective (e.g. "Grow revenue by 20% this quarter").',
      required: !0
    }),
    type: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Objective Level",
      description: "The level this objective applies to. Company objectives apply to the whole organisation; Department, Team, and Personal are scoped to a specific group or individual.",
      required: !0,
      defaultValue: "company",
      options: {
        options: [
          { label: "Company", value: "company" },
          { label: "Department", value: "department" },
          { label: "Team", value: "team" },
          { label: "Personal (User)", value: "user" }
        ]
      }
    }),
    level_details: import_pieces_framework5.Property.DynamicProperties({
      auth: weekdoneAuth,
      displayName: "Level Details",
      description: "Additional fields required for the selected objective level.",
      required: !0,
      refreshers: ["type"],
      props: async ({ auth, type }) => {
        if (type === "department")
          return {
            department_id: import_pieces_framework5.Property.Number({
              displayName: "Department ID",
              description: "The numeric ID of the department. You can find this in your Weekdone organisation settings.",
              required: !0
            })
          };
        if (type === "team") {
          let token = auth.access_token, teams = await weekdoneCommon.fetchTeams({ token });
          return {
            team_id: import_pieces_framework5.Property.StaticDropdown({
              displayName: "Team",
              description: "The team this objective belongs to.",
              required: !0,
              options: { options: teams.map((t) => ({ label: t.name, value: t.id })) }
            })
          };
        }
        if (type === "user") {
          let token = auth.access_token, [teams, users] = await Promise.all([
            weekdoneCommon.fetchTeams({ token }),
            weekdoneCommon.fetchUsers({ token })
          ]);
          return {
            team_id: import_pieces_framework5.Property.StaticDropdown({
              displayName: "Team",
              description: "The user's primary team.",
              required: !0,
              options: { options: teams.map((t) => ({ label: t.name, value: t.id })) }
            }),
            user_id: import_pieces_framework5.Property.StaticDropdown({
              displayName: "User",
              description: "The user this objective belongs to.",
              required: !0,
              options: { options: users.map((u) => ({ label: u.name, value: u.id })) }
            })
          };
        }
        return {};
      }
    }),
    period: import_pieces_framework5.Property.ShortText({
      displayName: "Quarter",
      description: 'The quarter this objective belongs to in YYYYQ format (e.g. "2024Q1" for Q1 2024, "2024Q3" for Q3 2024). Leave empty to use the current quarter.',
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f;
    let { description, type, level_details, period } = context.propsValue, token = context.auth.access_token, body = { description, type };
    period && (body.period = period), type === "department" && level_details ? body.department_id = level_details.department_id : type === "team" && level_details ? body.team_id = level_details.team_id : type === "user" && level_details && (body.team_id = level_details.team_id, body.user_id = level_details.user_id);
    let response = await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: `https://api.weekdone.com/1/objective?token=${token}`,
      body
    }), obj = (_b = (_a = response.body.data) != null ? _a : response.body.objective) != null ? _b : response.body;
    return {
      id: obj.id,
      type: obj.type,
      description: obj.description,
      period: obj.period,
      progress: obj.progress,
      comment_count: Array.isArray(obj.comments) ? obj.comments.length : (_c = obj.comments) != null ? _c : 0,
      key_result_count: Array.isArray(obj.results) ? obj.results.length : 0,
      department_id: (_d = obj.department_id) != null ? _d : null,
      team_id: (_e = obj.team_id) != null ? _e : null,
      user_id: (_f = obj.user_id) != null ? _f : null
    };
  }
});

// ../ap/packages/pieces/community/weekdone/src/lib/actions/update-objective.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var updateObjectiveAction = (0, import_pieces_framework6.createAction)({
  auth: weekdoneAuth,
  name: "update_objective",
  displayName: "Update Objective",
  description: "Updates the description of an existing OKR objective in Weekdone.",
  audience: "both",
  aiMetadata: { description: "Updates the description text of an existing Weekdone OKR objective identified by its objective ID. Use to rename or reword a known objective; the objective ID and new description are required (level/quarter filters only help locate the objective and are not changed). Idempotent \u2014 re-sending the same description leaves the objective in the same state.", idempotent: !0 },
  props: {
    search_type: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Filter by Level",
      description: "Narrow the objective list by level to make it easier to find the one you want.",
      required: !1,
      defaultValue: "company",
      options: {
        options: [
          { label: "Company", value: "company" },
          { label: "Department", value: "department" },
          { label: "Team", value: "team" },
          { label: "Personal (User)", value: "user" }
        ]
      }
    }),
    search_period: import_pieces_framework6.Property.ShortText({
      displayName: "Filter by Quarter",
      description: 'Narrow the objective list by quarter in YYYYQ format (e.g. "2024Q1"). Leave empty to show objectives from all periods.',
      required: !1
    }),
    objective_id: import_pieces_framework6.Property.Dropdown({
      displayName: "Objective",
      description: 'The objective to update. Use "Filter by Level" and "Filter by Quarter" above to narrow the list.',
      required: !0,
      auth: weekdoneAuth,
      refreshers: ["search_type", "search_period"],
      options: async ({ auth, search_type, search_period }) => {
        var _a;
        if (!auth)
          return { disabled: !0, options: [], placeholder: "Please connect your account first." };
        let token = auth.access_token, queryParams = {};
        search_type && (queryParams.type = search_type), search_period && (queryParams.period = search_period);
        try {
          let objectives = (_a = (await import_pieces_common5.httpClient.sendRequest({
            method: import_pieces_common5.HttpMethod.GET,
            url: `https://api.weekdone.com/1/objective?token=${token}`,
            queryParams
          })).body.data) != null ? _a : [];
          return objectives.length === 0 ? {
            disabled: !1,
            options: [],
            placeholder: "No objectives found. Try different filters."
          } : {
            disabled: !1,
            options: objectives.map((obj) => {
              var _a2;
              return {
                label: `${obj.description} (${(_a2 = obj.period) != null ? _a2 : "no period"})`,
                value: obj.id
              };
            })
          };
        } catch (e) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load objectives. Check your connection."
          };
        }
      }
    }),
    description: import_pieces_framework6.Property.LongText({
      displayName: "Description",
      description: "The new text for the objective.",
      required: !0
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f;
    let { objective_id, description } = context.propsValue, token = context.auth.access_token, response = await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.PATCH,
      url: `https://api.weekdone.com/1/objective/${objective_id}?token=${token}`,
      body: { description }
    }), obj = (_b = (_a = response.body.data) != null ? _a : response.body.objective) != null ? _b : response.body;
    return {
      id: obj.id,
      type: obj.type,
      description: obj.description,
      period: obj.period,
      progress: obj.progress,
      comment_count: Array.isArray(obj.comments) ? obj.comments.length : (_c = obj.comments) != null ? _c : 0,
      key_result_count: Array.isArray(obj.results) ? obj.results.length : 0,
      department_id: (_d = obj.department_id) != null ? _d : null,
      team_id: (_e = obj.team_id) != null ? _e : null,
      user_id: (_f = obj.user_id) != null ? _f : null
    };
  }
});

// ../ap/packages/pieces/community/weekdone/src/lib/triggers/new-item.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var props = {
  user_id: weekdoneCommon.userDropdown({
    displayName: "Filter by User",
    description: "Only trigger for items belonging to this user. Leave empty to receive items from all users.",
    required: !1
  }),
  team_id: weekdoneCommon.teamDropdown({
    displayName: "Filter by Team",
    description: "Only trigger for items added to this team. Leave empty to receive items from all teams.",
    required: !1
  })
}, polling = {
  strategy: import_pieces_common6.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue }) => {
    var _a;
    let token = auth.access_token, queryParams = {};
    return propsValue.user_id !== null && propsValue.user_id !== void 0 && (queryParams.user_id = String(propsValue.user_id)), propsValue.team_id !== null && propsValue.team_id !== void 0 && (queryParams.team_id = String(propsValue.team_id)), ((_a = (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.GET,
      url: `https://api.weekdone.com/1/items?token=${token}`,
      queryParams
    })).body.items) != null ? _a : []).map((item) => {
      var _a2, _b, _c;
      return {
        epochMilliSeconds: new Date(item.inserted).getTime(),
        data: {
          id: item.id,
          description: item.description,
          type_id: item.type_id,
          inserted: item.inserted,
          due_on: (_a2 = item.due_on) != null ? _a2 : null,
          priority: item.priority,
          user_id: item.user_id,
          team_id: item.team_id,
          from_id: (_b = item.from_id) != null ? _b : null,
          source: item.source,
          source_id: (_c = item.source_id) != null ? _c : null,
          is_private: item.private,
          position: item.nr,
          like_count: item.likecount,
          comment_count: item.commentcount
        }
      };
    });
  }
}, newItemTrigger = (0, import_pieces_framework7.createTrigger)({
  auth: weekdoneAuth,
  name: "new_item",
  displayName: "New Item",
  description: "Triggers when a new item is added to a Weekdone report.",
  aiMetadata: {
    description: "Fires when a new report item (a plan, progress note, problem, or plan-on-hold entry) is added to a Weekdone weekly report, optionally limited to a specific user or team. Represents a newly logged status update entry."
  },
  props,
  sampleData: {
    id: 690,
    description: "Finished Q1 planning session",
    type_id: 2,
    inserted: "2024-01-15T09:00:00Z",
    due_on: null,
    priority: 1,
    user_id: 12,
    team_id: 3,
    from_id: null,
    source: 0,
    source_id: null,
    is_private: 0,
    position: 1,
    like_count: 0,
    comment_count: 0
  },
  type: import_pieces_framework7.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common6.pollingHelper.test(polling, context);
  },
  async onEnable(context) {
    await import_pieces_common6.pollingHelper.onEnable(polling, context);
  },
  async onDisable(context) {
    await import_pieces_common6.pollingHelper.onDisable(polling, context);
  },
  async run(context) {
    return await import_pieces_common6.pollingHelper.poll(polling, context);
  }
});

// ../ap/packages/pieces/community/weekdone/src/lib/triggers/new-objective.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var props2 = {
  type: import_pieces_framework8.Property.StaticDropdown({
    displayName: "Objective Level",
    description: "Only trigger for objectives at a specific level. Leave empty to receive objectives from all levels.",
    required: !1,
    options: {
      options: [
        { label: "Company", value: "company" },
        { label: "Department", value: "department" },
        { label: "Team", value: "team" },
        { label: "Personal (User)", value: "user" }
      ]
    }
  }),
  period: import_pieces_framework8.Property.ShortText({
    displayName: "Quarter",
    description: 'Only trigger for objectives in a specific quarter. Use YYYYQ format (e.g. "2024Q1") or a full year (e.g. "2024"). Leave empty to watch all periods.',
    required: !1
  })
}, polling2 = {
  strategy: import_pieces_common7.DedupeStrategy.LAST_ITEM,
  items: async ({ auth, propsValue }) => {
    var _a;
    let token = auth.access_token, queryParams = {};
    return propsValue.type && (queryParams.type = propsValue.type), propsValue.period && (queryParams.period = propsValue.period), ((_a = (await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.GET,
      url: `https://api.weekdone.com/1/objective?token=${token}`,
      queryParams
    })).body.data) != null ? _a : []).sort((a, b) => b.id - a.id).map((obj) => {
      var _a2, _b, _c, _d;
      return {
        id: obj.id,
        data: {
          id: obj.id,
          type: obj.type,
          description: obj.description,
          period: obj.period,
          progress: obj.progress,
          comment_count: Array.isArray(obj.comments) ? obj.comments.length : (_a2 = obj.comments) != null ? _a2 : 0,
          key_result_count: Array.isArray(obj.results) ? obj.results.length : 0,
          department_id: (_b = obj.department_id) != null ? _b : null,
          team_id: (_c = obj.team_id) != null ? _c : null,
          user_id: (_d = obj.user_id) != null ? _d : null
        }
      };
    });
  }
}, newObjectiveTrigger = (0, import_pieces_framework8.createTrigger)({
  auth: weekdoneAuth,
  name: "new_objective",
  displayName: "New Objective",
  description: "Triggers when a new OKR objective is added in Weekdone.",
  aiMetadata: {
    description: "Fires when a new OKR objective is created in Weekdone, optionally limited to a specific level (company, department, team, or personal) or quarter. Represents a newly defined goal."
  },
  props: props2,
  sampleData: {
    id: 1,
    type: "company",
    description: "Grow revenue by 20% this quarter",
    period: "2024Q1",
    progress: 0,
    comment_count: 0,
    key_result_count: 0,
    department_id: null,
    team_id: null,
    user_id: null
  },
  type: import_pieces_framework8.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common7.pollingHelper.test(polling2, context);
  },
  async onEnable(context) {
    await import_pieces_common7.pollingHelper.onEnable(polling2, context);
  },
  async onDisable(context) {
    await import_pieces_common7.pollingHelper.onDisable(polling2, context);
  },
  async run(context) {
    return await import_pieces_common7.pollingHelper.poll(polling2, context);
  }
});

// ../ap/packages/pieces/community/weekdone/src/index.ts
var weekdone = (0, import_pieces_framework9.createPiece)({
  displayName: "Weekdone",
  description: "OKR & weekly status reporting tool for teams.",
  auth: weekdoneAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/weekdone.png",
  categories: [import_pieces_framework10.PieceCategory.PRODUCTIVITY],
  authors: ["onyedikachi-david"],
  actions: [
    createItemAction,
    updateItemAction,
    createObjectiveAction,
    updateObjectiveAction,
    (0, import_pieces_common8.createCustomApiCallAction)({
      baseUrl: () => "https://api.weekdone.com/1",
      auth: weekdoneAuth,
      authMapping: async (auth) => ({
        token: auth.access_token
      }),
      authLocation: "queryParams"
    })
  ],
  triggers: [newItemTrigger, newObjectiveTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  weekdone
});
