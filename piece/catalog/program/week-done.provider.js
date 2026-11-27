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

// ../ap/packages/pieces/community/week-done/src/index.ts
var index_exports = {};
__export(index_exports, {
  weekdone: () => weekdone
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common16 = require("@activepieces/pieces-common"), import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_framework16 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/week-done/src/lib/actions/items/create-item.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/week-done/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), weekdoneAuth = import_pieces_framework.PieceAuth.OAuth2({
  description: "Weekdone OAuth2 Authentication",
  authUrl: "https://weekdone.com/oauth_authorize",
  tokenUrl: "https://weekdone.com/oauth_token",
  required: !0,
  scope: []
});

// ../ap/packages/pieces/community/week-done/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), WEEKDONE_BASE_URL = "https://api.weekdone.com/1";
async function weekdoneApiCall({
  auth,
  method,
  path,
  query,
  body
}) {
  let queryParams = {
    token: auth.access_token
  };
  if (query)
    for (let [key, value] of Object.entries(query))
      value != null && (queryParams[key] = String(value));
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${WEEKDONE_BASE_URL}${path}`,
    queryParams,
    body
  })).body;
}

// ../ap/packages/pieces/community/week-done/src/lib/common/dropdowns.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
async function weekdoneUsersDropdown(auth) {
  var _a;
  return {
    disabled: !1,
    options: ((_a = (await weekdoneApiCall({
      auth,
      method: import_pieces_common2.HttpMethod.GET,
      path: "/users"
    })).users) != null ? _a : []).map((u) => ({
      label: u.name,
      value: u.id
    }))
  };
}
async function weekdoneItemCommentsDropdown(auth, itemId) {
  var _a;
  return {
    disabled: !1,
    options: ((_a = (await weekdoneApiCall({
      auth,
      method: import_pieces_common2.HttpMethod.GET,
      path: `/item/${itemId}/comments`
    })).comments) != null ? _a : []).map((c) => ({
      label: c.comment ? c.comment.slice(0, 80) : String(c.id),
      value: c.id
    }))
  };
}
async function weekdoneTeamsDropdown(auth) {
  var _a;
  return {
    disabled: !1,
    options: ((_a = (await weekdoneApiCall({
      auth,
      method: import_pieces_common2.HttpMethod.GET,
      path: "/teams"
    })).teams) != null ? _a : []).map((t) => ({
      label: t.name,
      value: t.id
    }))
  };
}
async function weekdoneTypesDropdown(auth, teamId) {
  var _a;
  return {
    disabled: !1,
    options: ((_a = (await weekdoneApiCall({
      auth,
      method: import_pieces_common2.HttpMethod.GET,
      path: "/types"
    })).types) != null ? _a : []).filter((t) => teamId === void 0 ? !0 : t.team_id === void 0 || t.team_id === null || Number(t.team_id) === teamId).map((t) => ({
      label: t.name,
      value: t.id
    }))
  };
}
async function weekdoneItemsDropdown(auth, params) {
  var _a;
  return {
    disabled: !1,
    options: ((_a = (await weekdoneApiCall({
      auth,
      method: import_pieces_common2.HttpMethod.GET,
      path: "/items",
      query: {
        user_id: params.userId,
        team_id: params.teamId,
        period: params.period
      }
    })).items) != null ? _a : []).filter((i) => params.typeId !== void 0 ? i.type_id === params.typeId : !0).map((i) => ({
      label: i.description || String(i.id),
      value: i.id
    }))
  };
}

// ../ap/packages/pieces/community/week-done/src/lib/actions/items/create-item.ts
var createItemAction = (0, import_pieces_framework2.createAction)({
  auth: weekdoneAuth,
  name: "create_item",
  displayName: "Create Item",
  description: "Create a new item.",
  audience: "both",
  aiMetadata: { description: "Create a new Weekdone item (a progress, plan, problem, or other typed entry) for a user/team within a reporting period. Requires a description and a type ID. Not idempotent; each call adds a distinct item.", idempotent: !1 },
  props: {
    description: import_pieces_framework2.Property.LongText({
      displayName: "Description",
      required: !0
    }),
    team_id: import_pieces_framework2.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "Team",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth != null && auth.access_token ? weekdoneTeamsDropdown(auth) : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    type_id: import_pieces_framework2.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "Type",
      required: !0,
      refreshers: ["team_id"],
      options: async ({ auth, team_id }) => auth != null && auth.access_token ? weekdoneTypesDropdown(
        auth,
        team_id ? Number(team_id) : void 0
      ) : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    period: import_pieces_framework2.Property.ShortText({
      displayName: "Period",
      required: !1
    }),
    user_id: import_pieces_framework2.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "User",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth != null && auth.access_token ? weekdoneUsersDropdown(auth) : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    priority: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Priority",
      required: !1,
      options: {
        options: [
          { label: "Not specified", value: 0 },
          { label: "Green", value: 1 },
          { label: "Amber", value: 2 },
          { label: "Red", value: 3 }
        ]
      }
    }),
    source_id: import_pieces_framework2.Property.ShortText({
      displayName: "Source ID",
      required: !1
    }),
    due_on: import_pieces_framework2.Property.ShortText({
      displayName: "Due Date",
      required: !1
    }),
    private: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Visibility",
      required: !1,
      options: {
        options: [
          { label: "Public", value: 0 },
          { label: "Private", value: 1 }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    return weekdoneApiCall({
      auth,
      method: import_pieces_common3.HttpMethod.POST,
      path: "/item",
      body: {
        description: propsValue.description,
        type_id: propsValue.type_id,
        period: propsValue.period,
        user_id: propsValue.user_id,
        team_id: propsValue.team_id,
        priority: propsValue.priority,
        source_id: propsValue.source_id,
        due_on: propsValue.due_on,
        private: propsValue.private
      }
    });
  }
});

// ../ap/packages/pieces/community/week-done/src/lib/actions/items/delete-item.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");
var deleteItemAction = (0, import_pieces_framework3.createAction)({
  auth: weekdoneAuth,
  name: "delete_item",
  displayName: "Delete Item",
  description: "Delete an item.",
  audience: "both",
  aiMetadata: { description: "Permanently delete a Weekdone item by its ID; the user/team/period filters only help locate the item. Destructive and not reversible. Not idempotent: a repeat call fails once the item is gone.", idempotent: !1 },
  props: {
    user_id_filter: import_pieces_framework3.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "User (Filter)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!(auth != null && auth.access_token))
          return {
            disabled: !0,
            placeholder: "Connect your account first",
            options: []
          };
        let state = await weekdoneUsersDropdown(auth);
        return __spreadProps(__spreadValues({}, state), {
          options: [{ label: "Me", value: "me" }, ...state.options]
        });
      }
    }),
    team_id: import_pieces_framework3.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "Team (Filter)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth != null && auth.access_token ? weekdoneTeamsDropdown(auth) : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    period: import_pieces_framework3.Property.ShortText({
      displayName: "Period (Filter)",
      required: !1
    }),
    item_id: import_pieces_framework3.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "Item",
      required: !0,
      refreshers: ["user_id_filter", "team_id", "period"],
      options: async ({ auth, user_id_filter, team_id, period }) => auth != null && auth.access_token ? weekdoneItemsDropdown(auth, {
        userId: user_id_filter,
        teamId: team_id ? Number(team_id) : void 0,
        period
      }) : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    })
  },
  async run({ auth, propsValue }) {
    return weekdoneApiCall({
      auth,
      method: import_pieces_common4.HttpMethod.DELETE,
      path: `/item/${propsValue.item_id}`
    });
  }
});

// ../ap/packages/pieces/community/week-done/src/lib/actions/items/search-items.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var searchItemsAction = (0, import_pieces_framework4.createAction)({
  auth: weekdoneAuth,
  name: "search_items",
  displayName: "Search Items",
  description: "Search for items.",
  audience: "both",
  aiMetadata: { description: "List/search Weekdone items (OKR plans, progress, problems, etc.), optionally narrowing by user, team, and reporting period; leave the filters empty to retrieve all accessible items. Use to find an item or its ID before acting on it. Read-only and idempotent.", idempotent: !0 },
  props: {
    user_id: import_pieces_framework4.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "User",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!(auth != null && auth.access_token))
          return {
            disabled: !0,
            placeholder: "Connect your account first",
            options: []
          };
        let state = await weekdoneUsersDropdown(auth);
        return __spreadProps(__spreadValues({}, state), {
          options: [{ label: "Me", value: "me" }, ...state.options]
        });
      }
    }),
    team_id: import_pieces_framework4.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "Team",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth != null && auth.access_token ? weekdoneTeamsDropdown(auth) : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    period: import_pieces_framework4.Property.ShortText({
      displayName: "Period",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    return weekdoneApiCall({
      auth,
      method: import_pieces_common5.HttpMethod.GET,
      path: "/items",
      query: {
        user_id: propsValue.user_id,
        team_id: propsValue.team_id,
        period: propsValue.period
      }
    });
  }
});

// ../ap/packages/pieces/community/week-done/src/lib/actions/items/update-item.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var updateItemAction = (0, import_pieces_framework5.createAction)({
  auth: weekdoneAuth,
  name: "update_item",
  displayName: "Update Item",
  description: "Update an existing item.",
  audience: "both",
  aiMetadata: { description: "Update an existing Weekdone item by its ID, changing fields such as description, type, priority, or due date; the user/team/period filters only help locate the item. Note that changing the type requires also supplying the period. Mutates the target item, so not idempotent across differing inputs.", idempotent: !1 },
  props: {
    user_id: import_pieces_framework5.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "User (Filter)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!(auth != null && auth.access_token))
          return {
            disabled: !0,
            placeholder: "Connect your account first",
            options: []
          };
        let state = await weekdoneUsersDropdown(auth);
        return __spreadProps(__spreadValues({}, state), {
          options: [{ label: "Me", value: "me" }, ...state.options]
        });
      }
    }),
    team_id: import_pieces_framework5.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "Team (Filter)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth != null && auth.access_token ? weekdoneTeamsDropdown(auth) : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    period: import_pieces_framework5.Property.ShortText({
      displayName: "Period (Filter)",
      required: !1
    }),
    item_id: import_pieces_framework5.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "Item",
      required: !0,
      refreshers: ["user_id", "team_id", "period"],
      options: async ({ auth, user_id, team_id, period }) => auth != null && auth.access_token ? weekdoneItemsDropdown(auth, {
        userId: user_id,
        teamId: team_id ? Number(team_id) : void 0,
        period
      }) : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    description: import_pieces_framework5.Property.LongText({
      displayName: "Description",
      required: !1
    }),
    type_id: import_pieces_framework5.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "Type",
      required: !1,
      refreshers: ["team_id"],
      options: async ({ auth, team_id }) => auth != null && auth.access_token ? weekdoneTypesDropdown(
        auth,
        team_id ? Number(team_id) : void 0
      ) : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    update_period: import_pieces_framework5.Property.ShortText({
      displayName: "Period (Required when updating Type)",
      required: !1
    }),
    priority: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Priority",
      required: !1,
      options: {
        options: [
          { label: "Not specified", value: 0 },
          { label: "Green", value: 1 },
          { label: "Amber", value: 2 },
          { label: "Red", value: 3 }
        ]
      }
    }),
    due_on: import_pieces_framework5.Property.ShortText({
      displayName: "Due Date",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    return weekdoneApiCall({
      auth,
      method: import_pieces_common6.HttpMethod.PATCH,
      path: `/item/${propsValue.item_id}`,
      body: {
        description: propsValue.description,
        type_id: propsValue.type_id,
        period: propsValue.update_period,
        priority: propsValue.priority,
        due_on: propsValue.due_on
      }
    });
  }
});

// ../ap/packages/pieces/community/week-done/src/lib/actions/items/assign-item.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var assignItemAction = (0, import_pieces_framework6.createAction)({
  auth: weekdoneAuth,
  name: "assign_item",
  displayName: "Assign Item",
  description: "Assign an item to another user.",
  audience: "both",
  aiMetadata: { description: "Reassign an existing Weekdone item to a different user by item ID; the user/team/period filters only help locate the item. Requires the target item and the assignee user ID. Mutates the item, so not idempotent.", idempotent: !1 },
  props: {
    user_id_filter: import_pieces_framework6.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "User (Filter)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!(auth != null && auth.access_token))
          return {
            disabled: !0,
            placeholder: "Connect your account first",
            options: []
          };
        let state = await weekdoneUsersDropdown(auth);
        return __spreadProps(__spreadValues({}, state), {
          options: [{ label: "Me", value: "me" }, ...state.options]
        });
      }
    }),
    team_id: import_pieces_framework6.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "Team (Filter)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth != null && auth.access_token ? weekdoneTeamsDropdown(auth) : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    period: import_pieces_framework6.Property.ShortText({
      displayName: "Period (Filter)",
      required: !1
    }),
    item_id: import_pieces_framework6.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "Item",
      required: !0,
      refreshers: ["user_id_filter", "team_id", "period"],
      options: async ({ auth, user_id_filter, team_id, period }) => auth != null && auth.access_token ? weekdoneItemsDropdown(auth, {
        userId: user_id_filter,
        teamId: team_id ? Number(team_id) : void 0,
        period
      }) : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    assignee_user_id: import_pieces_framework6.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "Assign To",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth != null && auth.access_token ? weekdoneUsersDropdown(auth) : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    })
  },
  async run({ auth, propsValue }) {
    return weekdoneApiCall({
      auth,
      method: import_pieces_common7.HttpMethod.PATCH,
      path: `/item/${propsValue.item_id}/assign`,
      body: {
        user_id: propsValue.assignee_user_id
      }
    });
  }
});

// ../ap/packages/pieces/community/week-done/src/lib/actions/items/add-item-comment.ts
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");
var addItemCommentAction = (0, import_pieces_framework7.createAction)({
  auth: weekdoneAuth,
  name: "add_item_comment",
  displayName: "Add Item Comment",
  description: "Add a comment to an item.",
  audience: "both",
  aiMetadata: { description: "Post a new comment on a Weekdone item identified by its ID; the user/team/period filters only help locate the item. Requires the comment text. Not idempotent; each call appends a distinct comment.", idempotent: !1 },
  props: {
    user_id_filter: import_pieces_framework7.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "User (Filter)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!(auth != null && auth.access_token))
          return {
            disabled: !0,
            placeholder: "Connect your account first",
            options: []
          };
        let state = await weekdoneUsersDropdown(auth);
        return __spreadProps(__spreadValues({}, state), {
          options: [{ label: "Me", value: "me" }, ...state.options]
        });
      }
    }),
    team_id: import_pieces_framework7.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "Team (Filter)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth != null && auth.access_token ? weekdoneTeamsDropdown(auth) : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    period: import_pieces_framework7.Property.ShortText({
      displayName: "Period (Filter)",
      required: !1
    }),
    item_id: import_pieces_framework7.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "Item",
      required: !0,
      refreshers: ["user_id_filter", "team_id", "period"],
      options: async ({ auth, user_id_filter, team_id, period }) => auth != null && auth.access_token ? weekdoneItemsDropdown(auth, {
        userId: user_id_filter,
        teamId: team_id ? Number(team_id) : void 0,
        period
      }) : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    comment: import_pieces_framework7.Property.LongText({
      displayName: "Comment",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    return weekdoneApiCall({
      auth,
      method: import_pieces_common8.HttpMethod.POST,
      path: `/item/${propsValue.item_id}/comments`,
      body: {
        comment: propsValue.comment
      }
    });
  }
});

// ../ap/packages/pieces/community/week-done/src/lib/actions/items/delete-item-comment.ts
var import_pieces_common9 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework");
var deleteItemCommentAction = (0, import_pieces_framework8.createAction)({
  auth: weekdoneAuth,
  name: "delete_item_comment",
  displayName: "Delete Item Comment",
  description: "Delete a comment from an item.",
  audience: "both",
  aiMetadata: { description: "Delete a specific comment from a Weekdone item, identified by both the item ID and the comment ID; the user/team/period filters only help locate the item. Destructive. Not idempotent: a repeat call fails once the comment is gone.", idempotent: !1 },
  props: {
    user_id_filter: import_pieces_framework8.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "User (Filter)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!(auth != null && auth.access_token))
          return {
            disabled: !0,
            placeholder: "Connect your account first",
            options: []
          };
        let state = await weekdoneUsersDropdown(auth);
        return __spreadProps(__spreadValues({}, state), {
          options: [{ label: "Me", value: "me" }, ...state.options]
        });
      }
    }),
    team_id: import_pieces_framework8.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "Team (Filter)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth != null && auth.access_token ? weekdoneTeamsDropdown(auth) : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    period: import_pieces_framework8.Property.ShortText({
      displayName: "Period (Filter)",
      required: !1
    }),
    item_id: import_pieces_framework8.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "Item",
      required: !0,
      refreshers: ["user_id_filter", "team_id", "period"],
      options: async ({ auth, user_id_filter, team_id, period }) => auth != null && auth.access_token ? weekdoneItemsDropdown(auth, {
        userId: user_id_filter,
        teamId: team_id ? Number(team_id) : void 0,
        period
      }) : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    comment_id: import_pieces_framework8.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "Comment",
      required: !0,
      refreshers: ["item_id"],
      options: async ({ auth, item_id }) => auth != null && auth.access_token ? item_id ? weekdoneItemCommentsDropdown(auth, Number(item_id)) : {
        disabled: !0,
        placeholder: "Select an item first",
        options: []
      } : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    })
  },
  async run({ auth, propsValue }) {
    return weekdoneApiCall({
      auth,
      method: import_pieces_common9.HttpMethod.DELETE,
      path: `/item/${propsValue.item_id}/comments/${propsValue.comment_id}`
    });
  }
});

// ../ap/packages/pieces/community/week-done/src/lib/actions/items/get-item-comments.ts
var import_pieces_common10 = require("@activepieces/pieces-common"), import_pieces_framework9 = require("@activepieces/pieces-framework");
var getItemCommentsAction = (0, import_pieces_framework9.createAction)({
  auth: weekdoneAuth,
  name: "get_item_comments",
  displayName: "Get Item Comments",
  description: "Get comments for an item.",
  audience: "both",
  aiMetadata: { description: "Retrieve the comments on a specific Weekdone item by its ID; the user/team/period filters only help locate the item. Use to read discussion or to find a comment ID before deleting one. Read-only and idempotent.", idempotent: !0 },
  props: {
    user_id_filter: import_pieces_framework9.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "User (Filter)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!(auth != null && auth.access_token))
          return {
            disabled: !0,
            placeholder: "Connect your account first",
            options: []
          };
        let state = await weekdoneUsersDropdown(auth);
        return __spreadProps(__spreadValues({}, state), {
          options: [{ label: "Me", value: "me" }, ...state.options]
        });
      }
    }),
    team_id: import_pieces_framework9.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "Team (Filter)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth != null && auth.access_token ? weekdoneTeamsDropdown(auth) : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    period: import_pieces_framework9.Property.ShortText({
      displayName: "Period (Filter)",
      required: !1
    }),
    item_id: import_pieces_framework9.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "Item",
      required: !0,
      refreshers: ["user_id_filter", "team_id", "period"],
      options: async ({ auth, user_id_filter, team_id, period }) => auth != null && auth.access_token ? weekdoneItemsDropdown(auth, {
        userId: user_id_filter,
        teamId: team_id ? Number(team_id) : void 0,
        period
      }) : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    })
  },
  async run({ auth, propsValue }) {
    return weekdoneApiCall({
      auth,
      method: import_pieces_common10.HttpMethod.GET,
      path: `/item/${propsValue.item_id}/comments`
    });
  }
});

// ../ap/packages/pieces/community/week-done/src/lib/actions/items/add-item-like.ts
var import_pieces_common11 = require("@activepieces/pieces-common"), import_pieces_framework10 = require("@activepieces/pieces-framework");
var addItemLikeAction = (0, import_pieces_framework10.createAction)({
  auth: weekdoneAuth,
  name: "add_item_like",
  displayName: "Add Item Like",
  description: "Add a like to an item.",
  audience: "both",
  aiMetadata: { description: "Add a like (from the authenticated user) to a Weekdone item identified by its ID; the user/team/period filters only help locate the item. Mutates the item, so not idempotent.", idempotent: !1 },
  props: {
    user_id_filter: import_pieces_framework10.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "User (Filter)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!(auth != null && auth.access_token))
          return {
            disabled: !0,
            placeholder: "Connect your account first",
            options: []
          };
        let state = await weekdoneUsersDropdown(auth);
        return __spreadProps(__spreadValues({}, state), {
          options: [{ label: "Me", value: "me" }, ...state.options]
        });
      }
    }),
    team_id: import_pieces_framework10.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "Team (Filter)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth != null && auth.access_token ? weekdoneTeamsDropdown(auth) : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    period: import_pieces_framework10.Property.ShortText({
      displayName: "Period (Filter)",
      required: !1
    }),
    item_id: import_pieces_framework10.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "Item",
      required: !0,
      refreshers: ["user_id_filter", "team_id", "period"],
      options: async ({ auth, user_id_filter, team_id, period }) => auth != null && auth.access_token ? weekdoneItemsDropdown(auth, {
        userId: user_id_filter,
        teamId: team_id ? Number(team_id) : void 0,
        period
      }) : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    })
  },
  async run({ auth, propsValue }) {
    return weekdoneApiCall({
      auth,
      method: import_pieces_common11.HttpMethod.POST,
      path: `/item/${propsValue.item_id}/likes`
    });
  }
});

// ../ap/packages/pieces/community/week-done/src/lib/actions/items/delete-item-like.ts
var import_pieces_common12 = require("@activepieces/pieces-common"), import_pieces_framework11 = require("@activepieces/pieces-framework");
var deleteItemLikeAction = (0, import_pieces_framework11.createAction)({
  auth: weekdoneAuth,
  name: "delete_item_like",
  displayName: "Delete Item Like",
  description: "Remove your like from an item.",
  audience: "both",
  aiMetadata: { description: "Remove the authenticated user's like from a Weekdone item identified by its ID; the user/team/period filters only help locate the item. Mutates the item, so not idempotent.", idempotent: !1 },
  props: {
    user_id_filter: import_pieces_framework11.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "User (Filter)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!(auth != null && auth.access_token))
          return {
            disabled: !0,
            placeholder: "Connect your account first",
            options: []
          };
        let state = await weekdoneUsersDropdown(auth);
        return __spreadProps(__spreadValues({}, state), {
          options: [{ label: "Me", value: "me" }, ...state.options]
        });
      }
    }),
    team_id: import_pieces_framework11.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "Team (Filter)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth != null && auth.access_token ? weekdoneTeamsDropdown(auth) : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    period: import_pieces_framework11.Property.ShortText({
      displayName: "Period (Filter)",
      required: !1
    }),
    item_id: import_pieces_framework11.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "Item",
      required: !0,
      refreshers: ["user_id_filter", "team_id", "period"],
      options: async ({ auth, user_id_filter, team_id, period }) => auth != null && auth.access_token ? weekdoneItemsDropdown(auth, {
        userId: user_id_filter,
        teamId: team_id ? Number(team_id) : void 0,
        period
      }) : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    })
  },
  async run({ auth, propsValue }) {
    return weekdoneApiCall({
      auth,
      method: import_pieces_common12.HttpMethod.DELETE,
      path: `/item/${propsValue.item_id}/likes`
    });
  }
});

// ../ap/packages/pieces/community/week-done/src/lib/actions/items/get-item-likes.ts
var import_pieces_common13 = require("@activepieces/pieces-common"), import_pieces_framework12 = require("@activepieces/pieces-framework");
var getItemLikesAction = (0, import_pieces_framework12.createAction)({
  auth: weekdoneAuth,
  name: "get_item_likes",
  displayName: "Get Item Likes",
  description: "Get likes for an item.",
  audience: "both",
  aiMetadata: { description: "Retrieve the list of likes on a specific Weekdone item by its ID; the user/team/period filters only help locate the item. Read-only and idempotent.", idempotent: !0 },
  props: {
    user_id_filter: import_pieces_framework12.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "User (Filter)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!(auth != null && auth.access_token))
          return {
            disabled: !0,
            placeholder: "Connect your account first",
            options: []
          };
        let state = await weekdoneUsersDropdown(auth);
        return __spreadProps(__spreadValues({}, state), {
          options: [{ label: "Me", value: "me" }, ...state.options]
        });
      }
    }),
    team_id: import_pieces_framework12.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "Team (Filter)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth != null && auth.access_token ? weekdoneTeamsDropdown(auth) : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    period: import_pieces_framework12.Property.ShortText({
      displayName: "Period (Filter)",
      required: !1
    }),
    item_id: import_pieces_framework12.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "Item",
      required: !0,
      refreshers: ["user_id_filter", "team_id", "period"],
      options: async ({ auth, user_id_filter, team_id, period }) => auth != null && auth.access_token ? weekdoneItemsDropdown(auth, {
        userId: user_id_filter,
        teamId: team_id ? Number(team_id) : void 0,
        period
      }) : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    })
  },
  async run({ auth, propsValue }) {
    return weekdoneApiCall({
      auth,
      method: import_pieces_common13.HttpMethod.GET,
      path: `/item/${propsValue.item_id}/likes`
    });
  }
});

// ../ap/packages/pieces/community/week-done/src/lib/actions/items/sort-items.ts
var import_pieces_common14 = require("@activepieces/pieces-common"), import_pieces_framework13 = require("@activepieces/pieces-framework");
var sortItemsAction = (0, import_pieces_framework13.createAction)({
  auth: weekdoneAuth,
  name: "sort_items",
  displayName: "Sort Items",
  description: "Sort items within a type and period.",
  audience: "both",
  aiMetadata: { description: "Reorder the Weekdone items of a given type within a team and reporting period by supplying the full ordered list of item IDs. Requires type, period, the desired ID order, and any one item ID from the list (used in the request URL). Mutates the stored ordering, so not idempotent across differing inputs.", idempotent: !1 },
  props: {
    team_id: import_pieces_framework13.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "Team",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth != null && auth.access_token ? weekdoneTeamsDropdown(auth) : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    type_id: import_pieces_framework13.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "Type",
      required: !0,
      refreshers: ["team_id"],
      options: async ({ auth, team_id }) => auth != null && auth.access_token ? weekdoneTypesDropdown(
        auth,
        team_id ? Number(team_id) : void 0
      ) : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    period: import_pieces_framework13.Property.ShortText({
      displayName: "Period",
      required: !0
    }),
    item_id: import_pieces_framework13.Property.Dropdown({
      auth: weekdoneAuth,
      displayName: "Any Item From This List",
      description: "Weekdone requires an item ID in the URL to sort the list.",
      required: !0,
      refreshers: ["team_id", "type_id", "period"],
      options: async ({ auth, team_id, type_id, period }) => auth != null && auth.access_token ? !type_id || !period ? {
        disabled: !0,
        placeholder: "Select type and period first",
        options: []
      } : weekdoneItemsDropdown(auth, {
        teamId: team_id ? Number(team_id) : void 0,
        period,
        typeId: Number(type_id)
      }) : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    list: import_pieces_framework13.Property.Array({
      displayName: "Item IDs (in order)",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    var _a;
    let itemIds = ((_a = propsValue.list) != null ? _a : []).map((v) => String(v)).join(",");
    return weekdoneApiCall({
      auth,
      method: import_pieces_common14.HttpMethod.POST,
      path: `/item/${propsValue.item_id}/sort`,
      body: {
        type_id: propsValue.type_id,
        period: propsValue.period,
        list: itemIds
      }
    });
  }
});

// ../ap/packages/pieces/community/week-done/src/lib/actions/company/get-company-info.ts
var import_pieces_common15 = require("@activepieces/pieces-common"), import_pieces_framework14 = require("@activepieces/pieces-framework");
var getCompanyInfoAction = (0, import_pieces_framework14.createAction)({
  auth: weekdoneAuth,
  name: "get_company_info",
  displayName: "Get Company Info",
  description: "Get company configuration and settings.",
  audience: "both",
  aiMetadata: { description: "Retrieve the authenticated account's Weekdone company configuration and settings. Takes no input. Read-only and idempotent.", idempotent: !0 },
  props: {},
  async run({ auth }) {
    return weekdoneApiCall({
      auth,
      method: import_pieces_common15.HttpMethod.GET,
      path: "/company"
    });
  }
});

// ../ap/packages/pieces/community/week-done/src/index.ts
var weekdone = (0, import_pieces_framework15.createPiece)({
  displayName: "Weekdone",
  description: "Goal-setting and progress tracking software that helps teams align their objectives and key results (OKRs).",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/week-done.png",
  categories: [import_pieces_framework16.PieceCategory.PRODUCTIVITY],
  authors: ["onyedikachi-david"],
  auth: weekdoneAuth,
  actions: [
    searchItemsAction,
    createItemAction,
    updateItemAction,
    assignItemAction,
    deleteItemAction,
    getItemLikesAction,
    addItemLikeAction,
    deleteItemLikeAction,
    getItemCommentsAction,
    addItemCommentAction,
    deleteItemCommentAction,
    sortItemsAction,
    getCompanyInfoAction,
    (0, import_pieces_common16.createCustomApiCallAction)({
      auth: weekdoneAuth,
      baseUrl: () => "https://api.weekdone.com/1",
      authLocation: "queryParams",
      authMapping: async (auth) => ({
        token: auth.access_token
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  weekdone
});
