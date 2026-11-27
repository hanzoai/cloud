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

// ../ap/packages/pieces/community/teamhood/src/index.ts
var index_exports = {};
__export(index_exports, {
  teamhood: () => teamhood,
  teamhoodAuth: () => teamhoodAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common14 = require("@activepieces/pieces-common"), import_pieces_framework14 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/teamhood/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/teamhood/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
async function teamhoodApiCall({
  auth,
  method,
  path,
  body,
  queryParams
}) {
  let baseUrl = auth.baseUrl.replace(/\/+$/, "");
  return await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${baseUrl}/api/v1${path}`,
    headers: {
      "X-ApiKey": auth.apiKey,
      "Content-Type": "application/json"
    },
    body,
    queryParams
  });
}

// ../ap/packages/pieces/community/teamhood/src/lib/common/auth.ts
var teamhoodAuth = import_pieces_framework.PieceAuth.CustomAuth({
  required: !0,
  description: `Connect your Teamhood account to use this piece.

**How to get your credentials:**
1. Log in to your Teamhood account at https://app.teamhood.com.
2. Open **Settings \u2192 Integrations \u2192 Teamhood API**.
3. Copy your  API key.
4. Your **API URL** is shown on the same page. It looks like \`https://api-YOURTENANT.teamhood.com\` \u2014 replace \`YOURTENANT\` with the subdomain from the URL you use to log in.`,
  props: {
    baseUrl: import_pieces_framework.Property.ShortText({
      displayName: "API URL",
      description: "Your tenant-specific Teamhood API URL (e.g. https://api-YOURTENANT.teamhood.com).",
      required: !0
    }),
    apiKey: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Key",
      description: "Teamhood API key generated from Settings \u2192 Integrations \u2192 Teamhood API in Teamhood.",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    try {
      return await teamhoodApiCall({
        auth,
        method: import_pieces_common2.HttpMethod.GET,
        path: "/workspaces"
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid Teamhood credentials. Double-check your API URL and API key in Settings \u2192 Integrations \u2192 Teamhood API."
      };
    }
  }
});

// ../ap/packages/pieces/community/teamhood/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var workspaceIdDropdown = (required) => import_pieces_framework2.Property.Dropdown({
  auth: teamhoodAuth,
  displayName: "Workspace",
  description: "Select a Teamhood workspace.",
  required,
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your Teamhood account first"
      };
    try {
      let workspaces = (_a = (await teamhoodApiCall({
        auth: auth.props,
        method: import_pieces_common3.HttpMethod.GET,
        path: "/workspaces"
      })).body.workspaces) != null ? _a : [];
      return workspaces.length === 0 ? {
        disabled: !1,
        options: [],
        placeholder: "No workspaces found in your Teamhood account."
      } : {
        disabled: !1,
        options: workspaces.map((w) => ({
          label: w.title,
          value: w.id
        }))
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load workspaces. Check your API URL and API key."
      };
    }
  }
}), boardIdDropdown = (required) => import_pieces_framework2.Property.Dropdown({
  auth: teamhoodAuth,
  displayName: "Board",
  description: "Select a board in the chosen workspace.",
  required,
  refreshers: ["workspaceId"],
  options: async ({ auth, workspaceId }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your Teamhood account first"
      };
    if (!workspaceId)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please select a workspace first"
      };
    try {
      let boards = (_a = (await teamhoodApiCall({
        auth: auth.props,
        method: import_pieces_common3.HttpMethod.GET,
        path: `/workspaces/${workspaceId}/boards`
      })).body.boards) != null ? _a : [];
      return {
        disabled: !1,
        options: boards.map((b) => ({ label: b.title, value: b.id })),
        placeholder: boards.length === 0 ? "No boards found in this workspace." : void 0
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load boards."
      };
    }
  }
}), rowIdDropdown = (required) => import_pieces_framework2.Property.Dropdown({
  auth: teamhoodAuth,
  displayName: "Row",
  description: "Select a swimlane row on the selected board. Rows group items horizontally on a Kanban board.",
  required,
  refreshers: ["boardId"],
  options: async ({ auth, boardId }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your Teamhood account first"
      };
    if (!boardId)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please select a board first"
      };
    try {
      let rows = (_a = (await teamhoodApiCall({
        auth: auth.props,
        method: import_pieces_common3.HttpMethod.GET,
        path: `/boards/${boardId}/rows`
      })).body.rows) != null ? _a : [];
      return {
        disabled: !1,
        options: rows.map((r) => ({ label: r.title, value: r.id })),
        placeholder: rows.length === 0 ? "No rows found on this board." : void 0
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load rows."
      };
    }
  }
}), statusIdDropdown = (required) => import_pieces_framework2.Property.Dropdown({
  auth: teamhoodAuth,
  displayName: "Status",
  description: "The status column on the board (e.g. To Do, In Progress, Done).",
  required,
  refreshers: ["boardId"],
  options: async ({ auth, boardId }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your Teamhood account first"
      };
    if (!boardId)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please select a board first"
      };
    try {
      let statuses = (_a = (await teamhoodApiCall({
        auth: auth.props,
        method: import_pieces_common3.HttpMethod.GET,
        path: `/boards/${boardId}/statuses`
      })).body.statuses) != null ? _a : [];
      return {
        disabled: !1,
        options: statuses.map((s) => ({
          label: `${s.title} (${s.type})`,
          value: s.id
        })),
        placeholder: statuses.length === 0 ? "No statuses on this board." : void 0
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load statuses."
      };
    }
  }
}), userIdDropdown = (params) => import_pieces_framework2.Property.Dropdown({
  auth: teamhoodAuth,
  displayName: params.displayName,
  description: params.description,
  required: params.required,
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your Teamhood account first"
      };
    try {
      return {
        disabled: !1,
        options: ((_a = (await teamhoodApiCall({
          auth: auth.props,
          method: import_pieces_common3.HttpMethod.GET,
          path: "/users"
        })).body.users) != null ? _a : []).filter((u) => u.status !== "Disabled").map((u) => ({
          label: `${u.firstName} ${u.lastName} (${u.email})`,
          value: u.id
        }))
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load users."
      };
    }
  }
}), itemIdDropdown = (required) => import_pieces_framework2.Property.Dropdown({
  auth: teamhoodAuth,
  displayName: "Item",
  description: "The Teamhood item (task) to operate on. Select a workspace to load items.",
  required,
  refreshers: ["workspaceId", "boardId"],
  options: async ({ auth, workspaceId, boardId }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your Teamhood account first"
      };
    if (!workspaceId)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please select a workspace first"
      };
    try {
      let queryParams = {
        WorkspaceId: workspaceId
      };
      boardId && (queryParams.BoardId = boardId);
      let items = (_a = (await teamhoodApiCall({
        auth: auth.props,
        method: import_pieces_common3.HttpMethod.GET,
        path: "/items",
        queryParams
      })).body.items) != null ? _a : [];
      return {
        disabled: !1,
        options: items.map((item) => ({
          label: item.title ? `${item.title} (#${item.displayId})` : `#${item.displayId}`,
          value: item.id
        })),
        placeholder: items.length === 0 ? "No items found." : void 0
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load items."
      };
    }
  }
});

// ../ap/packages/pieces/community/teamhood/src/lib/actions/create-item.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var createItemAction = (0, import_pieces_framework3.createAction)({
  auth: teamhoodAuth,
  name: "create_item",
  displayName: "Create Item",
  description: "Create a new item (task) in a Teamhood board.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new task item on a Teamhood board, requiring a workspace, board, and title; optionally set assignee, owner, status, row/swimlane, dates, estimation, budget, progress, tags, and milestone/completed flags. Use to add work into Teamhood. Not idempotent \u2014 each call creates a separate item.",
    idempotent: !1
  },
  props: {
    workspaceId: workspaceIdDropdown(!0),
    boardId: boardIdDropdown(!0),
    title: import_pieces_framework3.Property.ShortText({
      displayName: "Title",
      description: "The title of the item.",
      required: !0
    }),
    description: import_pieces_framework3.Property.LongText({
      displayName: "Description",
      description: "Optional description for the item. Plain text or HTML.",
      required: !1
    }),
    rowId: rowIdDropdown(!1),
    statusId: statusIdDropdown(!1),
    assignedUserId: userIdDropdown({
      displayName: "Assignee",
      description: "The user this item is assigned to.",
      required: !1
    }),
    ownerId: userIdDropdown({
      displayName: "Owner",
      description: "The user who owns this item.",
      required: !1
    }),
    startDate: import_pieces_framework3.Property.DateTime({
      displayName: "Start Date",
      description: "When work on this item should start.",
      required: !1
    }),
    dueDate: import_pieces_framework3.Property.DateTime({
      displayName: "Due Date",
      description: "When this item is due.",
      required: !1
    }),
    estimation: import_pieces_framework3.Property.Number({
      displayName: "Estimation (hours)",
      description: "Estimated effort to complete the item, in hours.",
      required: !1
    }),
    budget: import_pieces_framework3.Property.Number({
      displayName: "Budget",
      description: "Budget allocated for this item.",
      required: !1
    }),
    progress: import_pieces_framework3.Property.Number({
      displayName: "Progress (%)",
      description: "Percentage of work completed, between 0 and 100. Leave empty to track progress automatically.",
      required: !1
    }),
    milestone: import_pieces_framework3.Property.Checkbox({
      displayName: "Is Milestone",
      description: "Mark this item as a milestone.",
      required: !1,
      defaultValue: !1
    }),
    completed: import_pieces_framework3.Property.Checkbox({
      displayName: "Mark as Completed",
      description: "Create the item as already completed.",
      required: !1,
      defaultValue: !1
    }),
    tags: import_pieces_framework3.Property.Array({
      displayName: "Tags",
      description: "List of tag names to attach to the item.",
      required: !1
    })
  },
  async run(context) {
    let {
      workspaceId,
      boardId,
      title,
      description,
      rowId,
      statusId,
      assignedUserId,
      ownerId,
      startDate,
      dueDate,
      estimation,
      budget,
      progress,
      milestone,
      completed,
      tags
    } = context.propsValue, body = {
      title,
      workspaceId,
      boardId,
      isSuspended: !1,
      suspendReason: "",
      milestone: milestone != null ? milestone : !1,
      tags: Array.isArray(tags) ? tags : [],
      customFields: [],
      blocking: [],
      waiting: []
    };
    return description !== void 0 && (body.description = description), rowId && (body.rowId = rowId), statusId && (body.statusId = statusId), assignedUserId && (body.assignedUserId = assignedUserId), ownerId && (body.ownerId = ownerId), startDate && (body.startDate = startDate), dueDate && (body.dueDate = dueDate), estimation != null && (body.estimation = estimation), budget != null && (body.budget = budget), progress != null && (body.progress = progress), completed !== void 0 && (body.completed = completed), (await teamhoodApiCall({
      auth: context.auth.props,
      method: import_pieces_common4.HttpMethod.POST,
      path: "/items",
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/teamhood/src/lib/actions/update-item.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var updateItemAction = (0, import_pieces_framework4.createAction)({
  auth: teamhoodAuth,
  name: "update_item",
  displayName: "Update Item",
  description: "Update an existing Teamhood item. Only fields you fill in will be updated.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing Teamhood item identified by its item ID; only the fields you supply are changed (title, description, status, row, assignee, dates, estimation, budget, progress, tags, and completed/archived/milestone flags). Use to edit a known item. Note tags REPLACE the existing tag set rather than appending. Idempotent \u2014 re-applying the same values to the same item yields the same state.",
    idempotent: !0
  },
  props: {
    workspaceId: workspaceIdDropdown(!0),
    boardId: boardIdDropdown(!1),
    itemId: itemIdDropdown(!0),
    title: import_pieces_framework4.Property.ShortText({
      displayName: "Title",
      description: "New title for the item. Leave empty to keep current title.",
      required: !1
    }),
    description: import_pieces_framework4.Property.LongText({
      displayName: "Description",
      description: "New description for the item. Leave empty to keep current description.",
      required: !1
    }),
    rowId: rowIdDropdown(!1),
    statusId: statusIdDropdown(!1),
    assignedUserId: userIdDropdown({
      displayName: "Assignee",
      description: "New assignee for the item. Leave empty to keep current assignee.",
      required: !1
    }),
    startDate: import_pieces_framework4.Property.DateTime({
      displayName: "Start Date",
      required: !1
    }),
    dueDate: import_pieces_framework4.Property.DateTime({
      displayName: "Due Date",
      required: !1
    }),
    estimation: import_pieces_framework4.Property.Number({
      displayName: "Estimation (hours)",
      required: !1
    }),
    budget: import_pieces_framework4.Property.Number({
      displayName: "Budget",
      required: !1
    }),
    progress: import_pieces_framework4.Property.Number({
      displayName: "Progress (%)",
      description: "Between 0 and 100.",
      required: !1
    }),
    completed: import_pieces_framework4.Property.Checkbox({
      displayName: "Completed",
      description: "Mark the item as completed or not.",
      required: !1
    }),
    archived: import_pieces_framework4.Property.Checkbox({
      displayName: "Archived",
      description: "Archive or unarchive the item.",
      required: !1
    }),
    milestone: import_pieces_framework4.Property.Checkbox({
      displayName: "Is Milestone",
      required: !1
    }),
    tags: import_pieces_framework4.Property.Array({
      displayName: "Tags",
      description: "List of tag names. This REPLACES the current tags on the item.",
      required: !1
    })
  },
  async run(context) {
    let {
      itemId,
      title,
      description,
      rowId,
      statusId,
      assignedUserId,
      startDate,
      dueDate,
      estimation,
      budget,
      progress,
      completed,
      archived,
      milestone,
      tags
    } = context.propsValue, data = {};
    return title !== void 0 && title !== "" && (data.title = title), description !== void 0 && description !== "" && (data.description = description), rowId && (data.rowId = rowId), statusId && (data.statusId = statusId), assignedUserId && (data.userId = assignedUserId), startDate && (data.startDate = startDate), dueDate && (data.dueDate = dueDate), estimation != null && (data.estimation = estimation), budget != null && (data.budget = budget), progress != null && (data.progress = progress), completed !== void 0 && (data.completed = completed), archived !== void 0 && (data.archived = archived), milestone !== void 0 && (data.milestone = milestone), Array.isArray(tags) && (data.tags = tags), (await teamhoodApiCall({
      auth: context.auth.props,
      method: import_pieces_common5.HttpMethod.PUT,
      path: `/items/${itemId}`,
      body: { data }
    })).body;
  }
});

// ../ap/packages/pieces/community/teamhood/src/lib/actions/get-item.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var getItemAction = (0, import_pieces_framework5.createAction)({
  auth: teamhoodAuth,
  name: "get_item",
  displayName: "Get Item",
  description: "Retrieve a single Teamhood item by ID.",
  audience: "both",
  aiMetadata: {
    description: "Fetches a single Teamhood item by its item ID. Use to read the full details of a known item before acting on it. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    workspaceId: workspaceIdDropdown(!0),
    boardId: boardIdDropdown(!1),
    itemId: itemIdDropdown(!0)
  },
  async run(context) {
    let { itemId } = context.propsValue;
    return (await teamhoodApiCall({
      auth: context.auth.props,
      method: import_pieces_common6.HttpMethod.GET,
      path: `/items/${itemId}`
    })).body;
  }
});

// ../ap/packages/pieces/community/teamhood/src/lib/actions/delete-item.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var deleteItemAction = (0, import_pieces_framework6.createAction)({
  auth: teamhoodAuth,
  name: "delete_item",
  displayName: "Delete Item",
  description: "Permanently delete a Teamhood item.",
  audience: "both",
  aiMetadata: {
    description: "Permanently deletes a Teamhood item by its item ID. Use to remove a task that should no longer exist. Destructive and irreversible; idempotent in that repeating the call leaves the item gone with no additional effect.",
    idempotent: !0
  },
  props: {
    workspaceId: workspaceIdDropdown(!0),
    boardId: boardIdDropdown(!1),
    itemId: itemIdDropdown(!0)
  },
  async run(context) {
    let { itemId } = context.propsValue, response = await teamhoodApiCall({
      auth: context.auth.props,
      method: import_pieces_common7.HttpMethod.DELETE,
      path: `/items/${itemId}`
    });
    return { id: itemId, deleted: !0 };
  }
});

// ../ap/packages/pieces/community/teamhood/src/lib/actions/find-items.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var findItemsAction = (0, import_pieces_framework7.createAction)({
  auth: teamhoodAuth,
  name: "find_items",
  displayName: "Find Items",
  description: "Search for Teamhood items using filters.",
  audience: "both",
  aiMetadata: {
    description: "Searches Teamhood items, optionally narrowed by workspace, board, row, status, assignee, owner, completion state, and created/modified/completed-since timestamps; with all filters left empty it returns items broadly. Use to look up or enumerate items matching criteria, or to resolve an item ID before another action. Supports take/skip pagination (defaults to 100). Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    workspaceId: workspaceIdDropdown(!1),
    boardId: boardIdDropdown(!1),
    rowId: rowIdDropdown(!1),
    statusId: statusIdDropdown(!1),
    assignedUserId: userIdDropdown({
      displayName: "Assignee",
      description: "Only return items assigned to this user.",
      required: !1
    }),
    ownerId: userIdDropdown({
      displayName: "Owner",
      description: "Only return items owned by this user.",
      required: !1
    }),
    completed: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Completion Status",
      description: "Filter by completion. Leave empty to return both.",
      required: !1,
      options: {
        options: [
          { label: "Only completed", value: "true" },
          { label: "Only open", value: "false" }
        ]
      }
    }),
    createdSince: import_pieces_framework7.Property.DateTime({
      displayName: "Created Since",
      description: "Only return items created after this UTC date/time.",
      required: !1
    }),
    modifiedSince: import_pieces_framework7.Property.DateTime({
      displayName: "Modified Since",
      description: "Only return items modified after this UTC date/time.",
      required: !1
    }),
    completedSince: import_pieces_framework7.Property.DateTime({
      displayName: "Completed Since",
      description: "Only return items completed after this UTC date/time.",
      required: !1
    }),
    includeChildItems: import_pieces_framework7.Property.Checkbox({
      displayName: "Include Sub-items",
      description: "Include child items (sub-tasks) in the results.",
      required: !1,
      defaultValue: !1
    }),
    take: import_pieces_framework7.Property.Number({
      displayName: "Max Results",
      description: "Maximum number of items to return. Defaults to 100.",
      required: !1,
      defaultValue: 100
    }),
    skip: import_pieces_framework7.Property.Number({
      displayName: "Skip",
      description: "Number of items to skip \u2014 useful for pagination.",
      required: !1,
      defaultValue: 0
    })
  },
  async run(context) {
    let {
      workspaceId,
      boardId,
      rowId,
      statusId,
      assignedUserId,
      ownerId,
      completed,
      createdSince,
      modifiedSince,
      completedSince,
      includeChildItems,
      take,
      skip
    } = context.propsValue, queryParams = {};
    return workspaceId && (queryParams.WorkspaceId = workspaceId), boardId && (queryParams.BoardId = boardId), rowId && (queryParams.RowId = rowId), statusId && (queryParams.StatusId = statusId), assignedUserId && (queryParams.AssignedUserId = assignedUserId), ownerId && (queryParams.OwnerId = ownerId), completed && (queryParams.Completed = completed), createdSince && (queryParams.CreatedSince = createdSince), modifiedSince && (queryParams.ModifiedSince = modifiedSince), completedSince && (queryParams.CompletedSince = completedSince), includeChildItems !== void 0 && (queryParams.IncludeChildItems = String(includeChildItems)), queryParams.Take = String(take != null ? take : 100), queryParams.Skip = String(skip != null ? skip : 0), (await teamhoodApiCall({
      auth: context.auth.props,
      method: import_pieces_common8.HttpMethod.GET,
      path: "/items",
      queryParams
    })).body;
  }
});

// ../ap/packages/pieces/community/teamhood/src/lib/actions/create-row.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var createRowAction = (0, import_pieces_framework8.createAction)({
  auth: teamhoodAuth,
  name: "create_row",
  displayName: "Create Board Row",
  description: "Create a new horizontal swimlane row on a Teamhood board.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new horizontal swimlane row on a Teamhood board, requiring a workspace, board, and row title; optional start/end dates position it on the timeline view. Use to add a grouping lane that items can be placed into. Not idempotent \u2014 each call creates a separate row.",
    idempotent: !1
  },
  props: {
    workspaceId: workspaceIdDropdown(!0),
    boardId: boardIdDropdown(!0),
    title: import_pieces_framework8.Property.ShortText({
      displayName: "Row Title",
      description: "The title for the new swimlane row.",
      required: !0
    }),
    startDate: import_pieces_framework8.Property.DateTime({
      displayName: "Start Date",
      description: "Optional start date for the row (timeline view).",
      required: !1
    }),
    endDate: import_pieces_framework8.Property.DateTime({
      displayName: "End Date",
      description: "Optional end date for the row (timeline view).",
      required: !1
    })
  },
  async run(context) {
    let { boardId, title, startDate, endDate } = context.propsValue, body = { boardId, title };
    return startDate && (body.startDate = startDate), endDate && (body.endDate = endDate), (await teamhoodApiCall({
      auth: context.auth.props,
      method: import_pieces_common9.HttpMethod.POST,
      path: "/rows",
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/teamhood/src/lib/actions/list-users.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var listUsersAction = (0, import_pieces_framework9.createAction)({
  auth: teamhoodAuth,
  name: "list_users",
  displayName: "List Users",
  description: "List all users in your Teamhood account.",
  audience: "both",
  aiMetadata: {
    description: "Lists all users in the connected Teamhood account. Use to discover available users or resolve a user ID for assigning an item or filtering a search. Takes no input; read-only and idempotent.",
    idempotent: !0
  },
  props: {},
  async run(context) {
    var _a;
    return (_a = (await teamhoodApiCall({
      auth: context.auth.props,
      method: import_pieces_common10.HttpMethod.GET,
      path: "/users"
    })).body.users) != null ? _a : [];
  }
});

// ../ap/packages/pieces/community/teamhood/src/lib/triggers/new-item.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var props = {
  workspaceId: workspaceIdDropdown(!1),
  boardId: boardIdDropdown(!1)
}, polling = {
  strategy: import_pieces_common11.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => {
    var _a;
    let queryParams = {};
    return propsValue.workspaceId && (queryParams.WorkspaceId = propsValue.workspaceId), propsValue.boardId && (queryParams.BoardId = propsValue.boardId), lastFetchEpochMS && lastFetchEpochMS > 0 && (queryParams.CreatedSince = new Date(lastFetchEpochMS).toISOString()), ((_a = (await teamhoodApiCall({
      auth: auth.props,
      method: import_pieces_common11.HttpMethod.GET,
      path: "/items",
      queryParams
    })).body.items) != null ? _a : []).map((item) => ({
      epochMilliSeconds: new Date(item.createdDate).getTime(),
      data: item
    }));
  }
}, newItemTrigger = (0, import_pieces_framework10.createTrigger)({
  auth: teamhoodAuth,
  name: "new_item",
  displayName: "New Item",
  description: "Fires when a new item (task) is created in Teamhood.",
  aiMetadata: {
    description: "Fires when a new item (task) is created in Teamhood, optionally scoped to a specific workspace and board. Each event represents one newly created item."
  },
  props,
  type: import_pieces_framework10.TriggerStrategy.POLLING,
  sampleData: {
    id: "00000000-0000-0000-0000-000000000000",
    display_id: "AP-1",
    workspace_id: "00000000-0000-0000-0000-000000000000",
    board_id: "00000000-0000-0000-0000-000000000000",
    row_id: "00000000-0000-0000-0000-000000000000",
    title: "New task",
    status_id: null,
    owner_id: null,
    assigned_user_id: null,
    color: 0,
    start_date: null,
    due_date: null,
    is_schedule_locked: !1,
    completed_on: null,
    description: null,
    budget: null,
    estimation: null,
    estimation_type: null,
    completed: !1,
    archived: !1,
    modified_date: "2026-01-01T00:00:00Z",
    created_date: "2026-01-01T00:00:00Z",
    archived_date: null,
    total_logged_time: 0,
    milestone: !1,
    progress: null,
    is_suspended: !1,
    suspend_reason: null,
    parent_id: null,
    tags: null,
    url: "https://app.teamhood.com/items/0",
    has_attachments: !1,
    duration: null,
    sort_order: 1
  },
  async test(context) {
    return await import_pieces_common11.pollingHelper.test(polling, context);
  },
  async onEnable(context) {
    await import_pieces_common11.pollingHelper.onEnable(polling, context);
  },
  async onDisable(context) {
    await import_pieces_common11.pollingHelper.onDisable(polling, context);
  },
  async run(context) {
    return await import_pieces_common11.pollingHelper.poll(polling, context);
  }
});

// ../ap/packages/pieces/community/teamhood/src/lib/triggers/updated-item.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var props2 = {
  workspaceId: workspaceIdDropdown(!1),
  boardId: boardIdDropdown(!1)
}, polling2 = {
  strategy: import_pieces_common12.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => {
    var _a;
    let queryParams = {};
    return propsValue.workspaceId && (queryParams.WorkspaceId = propsValue.workspaceId), propsValue.boardId && (queryParams.BoardId = propsValue.boardId), lastFetchEpochMS && lastFetchEpochMS > 0 && (queryParams.ModifiedSince = new Date(lastFetchEpochMS).toISOString()), ((_a = (await teamhoodApiCall({
      auth: auth.props,
      method: import_pieces_common12.HttpMethod.GET,
      path: "/items",
      queryParams
    })).body.items) != null ? _a : []).map((item) => ({
      epochMilliSeconds: new Date(item.modifiedDate).getTime(),
      data: item
    }));
  }
}, updatedItemTrigger = (0, import_pieces_framework11.createTrigger)({
  auth: teamhoodAuth,
  name: "updated_item",
  displayName: "Updated Item",
  description: "Fires when an existing Teamhood item is modified.",
  aiMetadata: {
    description: "Fires when an existing Teamhood item is modified, optionally scoped to a specific workspace and board. Each event represents one item whose details changed since the last poll."
  },
  props: props2,
  type: import_pieces_framework11.TriggerStrategy.POLLING,
  sampleData: {
    id: "00000000-0000-0000-0000-000000000000",
    display_id: "AP-1",
    workspace_id: "00000000-0000-0000-0000-000000000000",
    board_id: "00000000-0000-0000-0000-000000000000",
    row_id: "00000000-0000-0000-0000-000000000000",
    title: "Updated task",
    status_id: null,
    owner_id: null,
    assigned_user_id: null,
    color: 0,
    start_date: null,
    due_date: null,
    is_schedule_locked: !1,
    completed_on: null,
    description: null,
    budget: null,
    estimation: null,
    estimation_type: null,
    completed: !1,
    archived: !1,
    modified_date: "2026-01-01T00:00:00Z",
    created_date: "2026-01-01T00:00:00Z",
    archived_date: null,
    total_logged_time: 0,
    milestone: !1,
    progress: null,
    is_suspended: !1,
    suspend_reason: null,
    parent_id: null,
    tags: null,
    url: "https://app.teamhood.com/items/0",
    has_attachments: !1,
    duration: null,
    sort_order: 1
  },
  async test(context) {
    return await import_pieces_common12.pollingHelper.test(polling2, context);
  },
  async onEnable(context) {
    await import_pieces_common12.pollingHelper.onEnable(polling2, context);
  },
  async onDisable(context) {
    await import_pieces_common12.pollingHelper.onDisable(polling2, context);
  },
  async run(context) {
    return await import_pieces_common12.pollingHelper.poll(polling2, context);
  }
});

// ../ap/packages/pieces/community/teamhood/src/lib/triggers/completed-item.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common");
var props3 = {
  workspaceId: workspaceIdDropdown(!1),
  boardId: boardIdDropdown(!1)
}, polling3 = {
  strategy: import_pieces_common13.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => {
    var _a;
    let queryParams = {
      Completed: "true"
    };
    return propsValue.workspaceId && (queryParams.WorkspaceId = propsValue.workspaceId), propsValue.boardId && (queryParams.BoardId = propsValue.boardId), lastFetchEpochMS && lastFetchEpochMS > 0 && (queryParams.CompletedSince = new Date(lastFetchEpochMS).toISOString()), ((_a = (await teamhoodApiCall({
      auth: auth.props,
      method: import_pieces_common13.HttpMethod.GET,
      path: "/items",
      queryParams
    })).body.items) != null ? _a : []).filter((item) => item.completedOn).map((item) => ({
      epochMilliSeconds: new Date(item.completedOn).getTime(),
      data: item
    }));
  }
}, completedItemTrigger = (0, import_pieces_framework12.createTrigger)({
  auth: teamhoodAuth,
  name: "completed_item",
  displayName: "Item Completed",
  description: "Fires when a Teamhood item is marked as completed.",
  aiMetadata: {
    description: "Fires when a Teamhood item is marked as completed, optionally scoped to a specific workspace and board. Each event represents one item that transitioned to completed since the last poll."
  },
  props: props3,
  type: import_pieces_framework12.TriggerStrategy.POLLING,
  sampleData: {
    id: "00000000-0000-0000-0000-000000000000",
    display_id: "AP-1",
    workspace_id: "00000000-0000-0000-0000-000000000000",
    board_id: "00000000-0000-0000-0000-000000000000",
    row_id: "00000000-0000-0000-0000-000000000000",
    title: "Completed task",
    status_id: null,
    owner_id: null,
    assigned_user_id: null,
    color: 0,
    start_date: null,
    due_date: null,
    is_schedule_locked: !1,
    completed_on: "2026-01-02T00:00:00Z",
    description: null,
    budget: null,
    estimation: null,
    estimation_type: null,
    completed: !0,
    archived: !1,
    modified_date: "2026-01-02T00:00:00Z",
    created_date: "2026-01-01T00:00:00Z",
    archived_date: null,
    total_logged_time: 0,
    milestone: !1,
    progress: 100,
    is_suspended: !1,
    suspend_reason: null,
    parent_id: null,
    tags: null,
    url: "https://app.teamhood.com/items/0",
    has_attachments: !1,
    duration: null,
    sort_order: 1
  },
  async test(context) {
    return await import_pieces_common13.pollingHelper.test(polling3, context);
  },
  async onEnable(context) {
    await import_pieces_common13.pollingHelper.onEnable(polling3, context);
  },
  async onDisable(context) {
    await import_pieces_common13.pollingHelper.onDisable(polling3, context);
  },
  async run(context) {
    return await import_pieces_common13.pollingHelper.poll(polling3, context);
  }
});

// ../ap/packages/pieces/community/teamhood/src/index.ts
var teamhood = (0, import_pieces_framework13.createPiece)({
  displayName: "Teamhood",
  description: "Visual project management for boards, tasks, and timelogs in Teamhood.",
  auth: teamhoodAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/teamhood.png",
  categories: [import_pieces_framework14.PieceCategory.PRODUCTIVITY],
  authors: ["sanket-a11y"],
  actions: [
    createItemAction,
    updateItemAction,
    getItemAction,
    deleteItemAction,
    findItemsAction,
    createRowAction,
    listUsersAction,
    (0, import_pieces_common14.createCustomApiCallAction)({
      baseUrl: (auth) => `${(auth == null ? void 0 : auth.props).baseUrl.replace(/\/+$/, "")}/api/v1`,
      auth: teamhoodAuth,
      authMapping: async (auth) => ({ "X-ApiKey": (auth == null ? void 0 : auth.props).apiKey })
    })
  ],
  triggers: [newItemTrigger, updatedItemTrigger, completedItemTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  teamhood,
  teamhoodAuth
});
