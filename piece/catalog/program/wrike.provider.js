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

// ../ap/packages/pieces/community/wrike/src/index.ts
var index_exports = {};
__export(index_exports, {
  wrike: () => wrike
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common13 = require("@activepieces/pieces-common"), import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_framework17 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/wrike/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), wrikeAuth = import_pieces_framework.PieceAuth.OAuth2({
  description: `
    To obtain your OAuth2 credentials:

    1. Go to Wrike (https://www.wrike.com/)
    2. Sign in to your account or create one
    3. Go to Apps & Integrations (https://www.wrike.com/apps/)
    4. Click "Create new app" or use an existing one
    5. For local development, add \`http://localhost:4200/redirect\` to the allowed redirect URIs
    6. For production, add \`https://cloud.activepieces.com/redirect\` to the allowed redirect URIs
    7. Copy the Client ID and Client Secret
    `,
  authUrl: "https://login.wrike.com/oauth2/authorize/v4",
  tokenUrl: "https://login.wrike.com/oauth2/token",
  required: !0,
  scope: []
});

// ../ap/packages/pieces/community/wrike/src/lib/actions/create-task.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/wrike/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), WRIKE_API_BASE_URL = "https://www.wrike.com/api/v4", wrikeCommon = {
  baseUrl: WRIKE_API_BASE_URL,
  async apiCall({
    auth,
    method,
    resourceUri,
    body = void 0,
    queryParams = void 0
  }) {
    return await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${WRIKE_API_BASE_URL}${resourceUri}`,
      body,
      queryParams,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      }
    });
  }
};

// ../ap/packages/pieces/community/wrike/src/lib/actions/create-task.ts
var createTask = (0, import_pieces_framework2.createAction)({
  name: "create_task",
  displayName: "Create Task",
  description: "Create a new task in Wrike",
  audience: "both",
  aiMetadata: { description: "Create a task inside a specific Wrike folder, with optional status, importance, assignees (user IDs), start/due dates, duration, and custom fields. Use when an agent needs to add work items to a Wrike project or folder. Requires the target folder ID; each call creates a distinct task, so it is not idempotent.", idempotent: !1 },
  auth: wrikeAuth,
  props: {
    folderId: import_pieces_framework2.Property.ShortText({
      displayName: "Folder ID",
      description: "The ID of the folder where the task will be created",
      required: !0
    }),
    title: import_pieces_framework2.Property.ShortText({
      displayName: "Task Title",
      description: "The title of the task",
      required: !0
    }),
    description: import_pieces_framework2.Property.LongText({
      displayName: "Description",
      description: "The description of the task",
      required: !1
    }),
    status: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Status",
      description: "The status of the task",
      required: !1,
      options: {
        options: [
          { label: "Active", value: "Active" },
          { label: "Completed", value: "Completed" },
          { label: "Deferred", value: "Deferred" },
          { label: "Cancelled", value: "Cancelled" }
        ]
      }
    }),
    importance: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Importance",
      description: "The importance/priority of the task",
      required: !1,
      options: {
        options: [
          { label: "Low", value: "Low" },
          { label: "Normal", value: "Normal" },
          { label: "High", value: "High" },
          { label: "Critical", value: "Critical" }
        ]
      }
    }),
    assignees: import_pieces_framework2.Property.Array({
      displayName: "Assignees",
      description: "User IDs to assign to the task",
      required: !1,
      properties: {
        userId: import_pieces_framework2.Property.ShortText({
          displayName: "User ID",
          required: !0
        })
      }
    }),
    start_date: import_pieces_framework2.Property.DateTime({
      displayName: "Start Date",
      description: "Task start date (format: YYYY-MM-DD)",
      required: !1
    }),
    due_date: import_pieces_framework2.Property.DateTime({
      displayName: "Due Date",
      description: "Task due date (format: YYYY-MM-DD)",
      required: !1
    }),
    duration: import_pieces_framework2.Property.Number({
      displayName: "Duration",
      description: "Task duration in minutes",
      required: !1
    }),
    customFields: import_pieces_framework2.Property.Array({
      displayName: "Custom Fields",
      description: "Custom field values for the task",
      required: !1,
      properties: {
        fieldId: import_pieces_framework2.Property.ShortText({
          displayName: "Field ID",
          description: "Custom field identifier",
          required: !0
        }),
        value: import_pieces_framework2.Property.ShortText({
          displayName: "Value",
          description: "Field value",
          required: !0
        })
      }
    })
  },
  async run(context) {
    let props = context.propsValue, { folderId, title, description, status, importance, assignees, start_date, due_date, duration, customFields } = props, taskData = {
      title
    };
    description && (taskData.description = description), status && (taskData.status = status), importance && (taskData.importance = importance), assignees && assignees.length > 0 && (taskData.responsibles = assignees.map((assignee) => assignee.userId));
    let dates = {};
    return start_date && (dates.start = start_date), due_date && (dates.due = due_date), duration && (dates.duration = duration), Object.keys(dates).length > 0 && (taskData.dates = dates), customFields && customFields.length > 0 && (taskData.customFields = customFields.map((field) => ({
      id: field.fieldId,
      value: field.value
    }))), (await wrikeCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common2.HttpMethod.POST,
      resourceUri: `/folders/${folderId}/tasks`,
      body: taskData
    })).body;
  }
});

// ../ap/packages/pieces/community/wrike/src/lib/actions/update-task.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var updateTask = (0, import_pieces_framework3.createAction)({
  name: "update_task",
  displayName: "Update Task",
  description: "Update fields of an existing task in Wrike",
  audience: "both",
  aiMetadata: { description: "Update an existing Wrike task identified by its task ID, setting only the provided fields (title, description, status, importance, assignees, dates, duration, custom fields); supplied assignees replace the existing ones. Use when an agent needs to modify a known task rather than create one. Requires the task ID; re-running with the same input leaves the task in the same state, so it is idempotent.", idempotent: !0 },
  auth: wrikeAuth,
  props: {
    taskId: import_pieces_framework3.Property.ShortText({
      displayName: "Task ID",
      description: "The ID of the task to update",
      required: !0
    }),
    title: import_pieces_framework3.Property.ShortText({
      displayName: "Task Title",
      description: "The new title of the task",
      required: !1
    }),
    description: import_pieces_framework3.Property.LongText({
      displayName: "Description",
      description: "The new description of the task",
      required: !1
    }),
    status: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Status",
      description: "The new status of the task",
      required: !1,
      options: {
        options: [
          { label: "Active", value: "Active" },
          { label: "Completed", value: "Completed" },
          { label: "Deferred", value: "Deferred" },
          { label: "Cancelled", value: "Cancelled" }
        ]
      }
    }),
    importance: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Importance",
      description: "The new importance/priority of the task",
      required: !1,
      options: {
        options: [
          { label: "Low", value: "Low" },
          { label: "Normal", value: "Normal" },
          { label: "High", value: "High" },
          { label: "Critical", value: "Critical" }
        ]
      }
    }),
    assignees: import_pieces_framework3.Property.Array({
      displayName: "Assignees",
      description: "User IDs to assign to the task (replaces existing assignees)",
      required: !1,
      properties: {
        userId: import_pieces_framework3.Property.ShortText({
          displayName: "User ID",
          required: !0
        })
      }
    }),
    start_date: import_pieces_framework3.Property.DateTime({
      displayName: "Start Date",
      description: "Task start date (format: YYYY-MM-DD)",
      required: !1
    }),
    due_date: import_pieces_framework3.Property.DateTime({
      displayName: "Due Date",
      description: "Task due date (format: YYYY-MM-DD)",
      required: !1
    }),
    duration: import_pieces_framework3.Property.Number({
      displayName: "Duration",
      description: "Task duration in minutes",
      required: !1
    }),
    customFields: import_pieces_framework3.Property.Array({
      displayName: "Custom Fields",
      description: "Custom field values to update for the task",
      required: !1,
      properties: {
        fieldId: import_pieces_framework3.Property.ShortText({
          displayName: "Field ID",
          description: "Custom field identifier",
          required: !0
        }),
        value: import_pieces_framework3.Property.ShortText({
          displayName: "Value",
          description: "New field value",
          required: !0
        })
      }
    })
  },
  async run(context) {
    let props = context.propsValue, { taskId, title, description, status, importance, assignees, start_date, due_date, duration, customFields } = props, taskData = {};
    title && (taskData.title = title), description !== void 0 && (taskData.description = description), status && (taskData.status = status), importance && (taskData.importance = importance), assignees && assignees.length > 0 && (taskData.responsibles = assignees.map((assignee) => assignee.userId));
    let dates = {};
    return start_date && (dates.start = start_date), due_date && (dates.due = due_date), duration && (dates.duration = duration), Object.keys(dates).length > 0 && (taskData.dates = dates), customFields && customFields.length > 0 && (taskData.customFields = customFields.map((field) => ({
      id: field.fieldId,
      value: field.value
    }))), (await wrikeCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common3.HttpMethod.PUT,
      resourceUri: `/tasks/${taskId}`,
      body: taskData
    })).body;
  }
});

// ../ap/packages/pieces/community/wrike/src/lib/actions/create-folder.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var createFolder = (0, import_pieces_framework4.createAction)({
  name: "create_folder",
  displayName: "Create Folder",
  description: "Create a new folder in Wrike",
  audience: "both",
  aiMetadata: { description: "Create a folder in Wrike, either at root level or nested under a parent folder ID; enabling the project option turns it into a project with owner and start/end dates instead of a plain folder. Use when an agent needs to set up a new container for tasks. Each call creates a new folder, so it is not idempotent.", idempotent: !1 },
  auth: wrikeAuth,
  props: {
    parentFolderId: import_pieces_framework4.Property.ShortText({
      displayName: "Parent Folder ID",
      description: "The ID of the parent folder where the new folder will be created. Leave empty to create at root level.",
      required: !1
    }),
    title: import_pieces_framework4.Property.ShortText({
      displayName: "Folder Title",
      description: "The title of the folder",
      required: !0
    }),
    description: import_pieces_framework4.Property.LongText({
      displayName: "Description",
      description: "The description of the folder",
      required: !1
    }),
    shareds: import_pieces_framework4.Property.Array({
      displayName: "Shared Users",
      description: "User IDs to share the folder with",
      required: !1,
      properties: {
        userId: import_pieces_framework4.Property.ShortText({
          displayName: "User ID",
          required: !0
        })
      }
    }),
    project: import_pieces_framework4.Property.Checkbox({
      displayName: "Create as Project",
      description: "Whether this folder should be created as a project with additional project settings",
      required: !1,
      defaultValue: !1
    }),
    projectOwnerId: import_pieces_framework4.Property.ShortText({
      displayName: "Project Owner ID",
      description: "The ID of the user who will own the project (required when creating as project)",
      required: !1
    }),
    projectStartDate: import_pieces_framework4.Property.DateTime({
      displayName: "Project Start Date",
      description: "The start date of the project (format: YYYY-MM-DD)",
      required: !1
    }),
    projectEndDate: import_pieces_framework4.Property.DateTime({
      displayName: "Project End Date",
      description: "The end date of the project (format: YYYY-MM-DD)",
      required: !1
    })
  },
  async run(context) {
    let props = context.propsValue, { parentFolderId, title, description, shareds, project, projectOwnerId, projectStartDate, projectEndDate } = props, folderData = {
      title
    };
    if (description && (folderData.description = description), shareds && shareds.length > 0 && (folderData.shareds = shareds.map((shared) => shared.userId)), project) {
      let projectData = {};
      projectOwnerId ? projectData.ownerIds = [projectOwnerId] : projectData.ownerIds = [context.auth.access_token.split(".")[0]], projectStartDate && (projectData.startDate = projectStartDate.split("T")[0]), projectEndDate && (projectData.endDate = projectEndDate.split("T")[0]), folderData.project = projectData;
    }
    let resourceUri = "/folders";
    return parentFolderId && (resourceUri = `/folders/${parentFolderId}/folders`), (await wrikeCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common4.HttpMethod.POST,
      resourceUri,
      body: folderData
    })).body;
  }
});

// ../ap/packages/pieces/community/wrike/src/lib/actions/create-project.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var createProject = (0, import_pieces_framework5.createAction)({
  name: "create_project",
  displayName: "Create Project",
  description: "Create a new project in Wrike",
  audience: "both",
  aiMetadata: { description: "Create a project in Wrike (a folder configured as a project) at root level or under a parent folder ID, with optional owners, shared users, and start/end dates. Use when an agent needs a project container rather than a plain folder. Each call creates a new project, so it is not idempotent.", idempotent: !1 },
  auth: wrikeAuth,
  props: {
    parentFolderId: import_pieces_framework5.Property.ShortText({
      displayName: "Parent Folder ID",
      description: "The ID of the parent folder where the project will be created. Leave empty to create at root level.",
      required: !1
    }),
    title: import_pieces_framework5.Property.ShortText({
      displayName: "Project Title",
      description: "The title of the project",
      required: !0
    }),
    description: import_pieces_framework5.Property.LongText({
      displayName: "Description",
      description: "The description of the project",
      required: !1
    }),
    ownerIds: import_pieces_framework5.Property.Array({
      displayName: "Owner IDs",
      description: "User IDs who will own the project",
      required: !1,
      properties: {
        userId: import_pieces_framework5.Property.ShortText({
          displayName: "User ID",
          required: !0
        })
      }
    }),
    shareds: import_pieces_framework5.Property.Array({
      displayName: "Shared Users",
      description: "User IDs to share the project with",
      required: !1,
      properties: {
        userId: import_pieces_framework5.Property.ShortText({
          displayName: "User ID",
          required: !0
        })
      }
    }),
    start_date: import_pieces_framework5.Property.DateTime({
      displayName: "Start Date",
      description: "Project start date (format: YYYY-MM-DD)",
      required: !1
    }),
    end_date: import_pieces_framework5.Property.DateTime({
      displayName: "End Date",
      description: "Project end date (format: YYYY-MM-DD)",
      required: !1
    })
  },
  async run(context) {
    let props = context.propsValue, { parentFolderId, title, description, ownerIds, shareds, start_date, end_date } = props, projectData = {
      title,
      project: {}
    };
    ownerIds && ownerIds.length > 0 && (projectData.project.ownerIds = ownerIds.map((owner) => owner.userId)), description && (projectData.description = description), shareds && shareds.length > 0 && (projectData.shareds = shareds.map((shared) => shared.userId)), (start_date || end_date) && (projectData.project.dates = {}, start_date && (projectData.project.dates.start = start_date.split("T")[0]), end_date && (projectData.project.dates.end = end_date.split("T")[0]));
    let resourceUri = "/folders";
    return parentFolderId && (resourceUri = `/folders/${parentFolderId}/folders`), (await wrikeCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common5.HttpMethod.POST,
      resourceUri,
      body: projectData
    })).body;
  }
});

// ../ap/packages/pieces/community/wrike/src/lib/actions/add-comment.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var addComment = (0, import_pieces_framework6.createAction)({
  name: "add_comment",
  displayName: "Add Comment",
  description: "Add a comment or internal note to a task or folder",
  audience: "both",
  aiMetadata: { description: "Post a comment on a Wrike task or folder, selected via the entity type and entity ID; the comment text can be HTML or plain text. Use when an agent needs to annotate or leave a note on an item. Each call appends a new comment, so it is not idempotent.", idempotent: !1 },
  auth: wrikeAuth,
  props: {
    entityType: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Entity Type",
      description: "Select whether you want to add the comment to a task or folder",
      required: !0,
      options: {
        options: [
          { label: "Task", value: "tasks" },
          { label: "Folder", value: "folders" }
        ]
      }
    }),
    entityId: import_pieces_framework6.Property.ShortText({
      displayName: "Entity ID",
      description: "The ID of the task or folder to add the comment to",
      required: !0
    }),
    text: import_pieces_framework6.Property.LongText({
      displayName: "Comment Text",
      description: "The text content of the comment",
      required: !0
    }),
    plainText: import_pieces_framework6.Property.Checkbox({
      displayName: "Plain Text",
      description: "Whether the comment should be treated as plain text (no HTML formatting). Default is HTML formatting.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let commentData = {
      text: context.propsValue.text
    };
    return context.propsValue.plainText !== void 0 && (commentData.plainText = context.propsValue.plainText), (await wrikeCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common6.HttpMethod.POST,
      resourceUri: `/${context.propsValue.entityType}/${context.propsValue.entityId}/comments`,
      body: commentData
    })).body;
  }
});

// ../ap/packages/pieces/community/wrike/src/lib/actions/upload-attachment.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var uploadAttachment = (0, import_pieces_framework7.createAction)({
  name: "upload_attachment",
  displayName: "Upload Attachment",
  description: "Upload a file and attach it to a task or folder",
  audience: "both",
  aiMetadata: { description: "Upload a file and attach it to a Wrike task or folder, chosen via the entity type and entity ID, with an optional custom file name. Use when an agent needs to add a document or asset to an item. Each call uploads a new attachment, so it is not idempotent.", idempotent: !1 },
  auth: wrikeAuth,
  props: {
    entityType: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Entity Type",
      description: "Select whether to attach the file to a task or folder",
      required: !0,
      options: {
        options: [
          { label: "Task", value: "tasks" },
          { label: "Folder", value: "folders" }
        ]
      }
    }),
    entityId: import_pieces_framework7.Property.ShortText({
      displayName: "Entity ID",
      description: "The ID of the task or folder to attach the file to",
      required: !0
    }),
    file: import_pieces_framework7.Property.File({
      displayName: "File",
      description: "The file to upload and attach",
      required: !0
    }),
    fileName: import_pieces_framework7.Property.ShortText({
      displayName: "File Name",
      description: "Optional custom name for the uploaded file",
      required: !1
    })
  },
  async run(context) {
    let props = context.propsValue, { entityType, entityId, file, fileName } = props;
    return (await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.POST,
      url: `${wrikeCommon.baseUrl}/${entityType}/${entityId}/attachments`,
      body: file.data,
      authentication: {
        type: import_pieces_common7.AuthenticationType.BEARER_TOKEN,
        token: context.auth.access_token
      },
      headers: {
        "Content-Type": "application/octet-stream",
        "X-Requested-With": "XMLHttpRequest",
        "X-File-Name": fileName || file.filename
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/wrike/src/lib/actions/find-task.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var findTask = (0, import_pieces_framework8.createAction)({
  name: "find_task",
  displayName: "Find Task",
  description: "Retrieve a task by its ID or lookup fields",
  audience: "both",
  aiMetadata: { description: "Look up Wrike tasks: pass a task ID to fetch that exact task (other criteria are ignored), or omit it to search/filter by title, folder, status, importance, assignees, or authors with sorting and page size. Use when an agent needs to find or read tasks before acting on them. Read-only and idempotent.", idempotent: !0 },
  auth: wrikeAuth,
  props: {
    taskId: import_pieces_framework8.Property.ShortText({
      displayName: "Task ID",
      description: "The ID of the task to retrieve. If provided, other search criteria will be ignored.",
      required: !1
    }),
    title: import_pieces_framework8.Property.ShortText({
      displayName: "Task Title",
      description: "Search for tasks by title (partial match)",
      required: !1
    }),
    folderId: import_pieces_framework8.Property.ShortText({
      displayName: "Folder ID",
      description: "Search for tasks within a specific folder",
      required: !1
    }),
    status: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Status",
      description: "Filter tasks by status",
      required: !1,
      options: {
        options: [
          { label: "Active", value: "Active" },
          { label: "Completed", value: "Completed" },
          { label: "Deferred", value: "Deferred" },
          { label: "Cancelled", value: "Cancelled" }
        ]
      }
    }),
    importance: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Importance",
      description: "Filter tasks by importance/priority",
      required: !1,
      options: {
        options: [
          { label: "Low", value: "Low" },
          { label: "Normal", value: "Normal" },
          { label: "High", value: "High" },
          { label: "Critical", value: "Critical" }
        ]
      }
    }),
    assignees: import_pieces_framework8.Property.Array({
      displayName: "Assignee IDs",
      description: "Filter tasks by assignee user IDs",
      required: !1,
      properties: {
        userId: import_pieces_framework8.Property.ShortText({
          displayName: "User ID",
          required: !0
        })
      }
    }),
    authors: import_pieces_framework8.Property.Array({
      displayName: "Author IDs",
      description: "Filter tasks by author user IDs",
      required: !1,
      properties: {
        userId: import_pieces_framework8.Property.ShortText({
          displayName: "User ID",
          required: !0
        })
      }
    }),
    pageSize: import_pieces_framework8.Property.Number({
      displayName: "Page Size",
      description: "Maximum number of tasks to return (max 1000)",
      required: !1,
      defaultValue: 100
    }),
    sortField: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Sort Field",
      description: "Field to sort tasks by",
      required: !1,
      options: {
        options: [
          { label: "Created Date", value: "CreatedDate" },
          { label: "Updated Date", value: "UpdatedDate" },
          { label: "Due Date", value: "DueDate" },
          { label: "Start Date", value: "StartDate" },
          { label: "Completed Date", value: "CompletedDate" },
          { label: "Title", value: "Title" },
          { label: "Status", value: "Status" },
          { label: "Importance", value: "Importance" }
        ]
      }
    }),
    sortOrder: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Sort Order",
      description: "Sort order for results",
      required: !1,
      options: {
        options: [
          { label: "Ascending", value: "Asc" },
          { label: "Descending", value: "Desc" }
        ]
      }
    })
  },
  async run(context) {
    let props = context.propsValue, { taskId, title, folderId, status, importance, assignees, authors, pageSize, sortField, sortOrder } = props;
    if (taskId)
      return (await wrikeCommon.apiCall({
        auth: context.auth,
        method: import_pieces_common8.HttpMethod.GET,
        resourceUri: `/tasks/${taskId}`
      })).body;
    let queryParams = {};
    return title && (queryParams.title = title), folderId && (queryParams.folderId = folderId), status && (queryParams.status = status), importance && (queryParams.importance = importance), pageSize && (queryParams.pageSize = pageSize.toString()), sortField && (queryParams.sortField = sortField), sortOrder && (queryParams.sortOrder = sortOrder), assignees && assignees.length > 0 && (queryParams.responsibles = assignees.map((assignee) => assignee.userId).join(",")), authors && authors.length > 0 && (queryParams.authors = authors.map((author) => author.userId).join(",")), (await wrikeCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common8.HttpMethod.GET,
      resourceUri: "/tasks",
      queryParams
    })).body;
  }
});

// ../ap/packages/pieces/community/wrike/src/lib/actions/find-folder.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var findFolder = (0, import_pieces_framework9.createAction)({
  name: "find_folder",
  displayName: "Find Folder / Project",
  description: "Retrieve folder / project metadata by ID or name",
  audience: "both",
  aiMetadata: { description: "Look up Wrike folders or projects: pass a folder ID to fetch that exact one (other criteria are ignored), or omit it to search/filter by title, parent folders, authors, owners, or the projects-only flag. Use when an agent needs to resolve or read folder/project metadata before acting. Read-only and idempotent.", idempotent: !0 },
  auth: wrikeAuth,
  props: {
    folderId: import_pieces_framework9.Property.ShortText({
      displayName: "Folder ID",
      description: "The ID of the folder/project to retrieve. If provided, other search criteria will be ignored.",
      required: !1
    }),
    title: import_pieces_framework9.Property.ShortText({
      displayName: "Title",
      description: "Search for folders/projects by title (partial match)",
      required: !1
    }),
    parentIds: import_pieces_framework9.Property.Array({
      displayName: "Parent IDs",
      description: "Search for folders/projects within specific parent folders",
      required: !1,
      properties: {
        folderId: import_pieces_framework9.Property.ShortText({
          displayName: "Folder ID",
          required: !0
        })
      }
    }),
    project: import_pieces_framework9.Property.Checkbox({
      displayName: "Is Project",
      description: "Filter to only return projects (folders marked as projects)",
      required: !1
    }),
    pageSize: import_pieces_framework9.Property.Number({
      displayName: "Page Size",
      description: "Maximum number of folders/projects to return (max 1000)",
      required: !1,
      defaultValue: 100
    }),
    authors: import_pieces_framework9.Property.Array({
      displayName: "Author IDs",
      description: "Filter by authors (users who created the folders)",
      required: !1,
      properties: {
        userId: import_pieces_framework9.Property.ShortText({
          displayName: "User ID",
          required: !0
        })
      }
    }),
    owners: import_pieces_framework9.Property.Array({
      displayName: "Owner IDs",
      description: "Filter by owners (users who own the projects)",
      required: !1,
      properties: {
        userId: import_pieces_framework9.Property.ShortText({
          displayName: "User ID",
          required: !0
        })
      }
    })
  },
  async run(context) {
    let props = context.propsValue, { folderId, title, parentIds, project, pageSize, authors, owners } = props;
    if (folderId)
      return (await wrikeCommon.apiCall({
        auth: context.auth,
        method: import_pieces_common9.HttpMethod.GET,
        resourceUri: `/folders/${folderId}`
      })).body;
    let queryParams = {};
    return title && (queryParams.title = title), parentIds && parentIds.length > 0 && (queryParams.parentIds = parentIds.map((parent) => parent.folderId).join(",")), project !== void 0 && (queryParams.project = project.toString()), pageSize && (queryParams.pageSize = pageSize.toString()), authors && authors.length > 0 && (queryParams.authors = authors.map((author) => author.userId).join(",")), owners && owners.length > 0 && (queryParams.owners = owners.map((owner) => owner.userId).join(",")), (await wrikeCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common9.HttpMethod.GET,
      resourceUri: "/folders",
      queryParams
    })).body;
  }
});

// ../ap/packages/pieces/community/wrike/src/lib/triggers/new-task.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var import_pieces_common10 = require("@activepieces/pieces-common"), import_pieces_framework11 = require("@activepieces/pieces-framework");
var newTask = (0, import_pieces_framework11.createTrigger)({
  name: "new_task",
  displayName: "New Task",
  description: "Fires when a new task is created in Wrike (optionally within a folder).",
  aiMetadata: {
    description: "Fires when a new task is created in Wrike, registered via a webhook on the TaskCreated event; can be scoped to a single folder so it only fires for tasks created there."
  },
  auth: wrikeAuth,
  type: import_pieces_framework11.TriggerStrategy.WEBHOOK,
  props: {
    folderId: import_pieces_framework10.Property.ShortText({
      displayName: "Folder ID",
      description: "Optional: Only trigger for tasks created in this specific folder",
      required: !1
    })
  },
  sampleData: {
    webhookId: "IEAAABC6JAAAABAS",
    eventAuthorId: "KUAAABKU",
    eventType: "TaskCreated",
    taskId: "IEAAABC6KQAB5FKW",
    lastUpdatedDate: "2016-10-10T11:33:28Z"
  },
  onEnable: async (context) => {
    let webhookUrl = context.webhookUrl, { folderId } = context.propsValue, webhookData = {
      hookUrl: webhookUrl
    };
    folderId && (webhookData.folderId = folderId);
    let request = {
      method: import_pieces_common10.HttpMethod.POST,
      url: `${wrikeCommon.baseUrl}/webhooks`,
      body: webhookData,
      headers: {
        Authorization: `Bearer ${context.auth.access_token}`,
        "Content-Type": "application/json"
      }
    }, { status, body } = await import_pieces_common10.httpClient.sendRequest(request);
    if (status !== 200)
      throw new Error(
        `Failed to register webhook. Status: ${status}, Body: ${JSON.stringify(
          body
        )}`
      );
    await context.store.put("webhook_id", body.data[0].id);
  },
  onDisable: async (context) => {
    let webhookId = await context.store.get("webhook_id");
    if (webhookId) {
      let request = {
        method: import_pieces_common10.HttpMethod.DELETE,
        url: `${wrikeCommon.baseUrl}/webhooks/${webhookId}`,
        headers: {
          Authorization: `Bearer ${context.auth.access_token}`
        }
      };
      try {
        await import_pieces_common10.httpClient.sendRequest(request), await context.store.delete("webhook_id");
      } catch (error) {
        console.warn("Failed to unregister webhook:", error);
      }
    }
  },
  run: async (context) => {
    let payload = context.payload.body;
    return payload && payload.eventType === "TaskCreated" ? payload.data : [];
  }
});

// ../ap/packages/pieces/community/wrike/src/lib/triggers/new-folder.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var import_pieces_common11 = require("@activepieces/pieces-common"), import_pieces_framework13 = require("@activepieces/pieces-framework");
var newFolder = (0, import_pieces_framework13.createTrigger)({
  name: "new_folder",
  displayName: "New Folder",
  description: "Fires when a new folder (or project) is created in Wrike.",
  aiMetadata: {
    description: "Fires when a new folder or project is created in Wrike, registered via a webhook on the FolderCreated event; can be scoped to a single parent folder so it only fires for folders created underneath it."
  },
  auth: wrikeAuth,
  type: import_pieces_framework13.TriggerStrategy.WEBHOOK,
  props: {
    parentFolderId: import_pieces_framework12.Property.ShortText({
      displayName: "Parent Folder ID",
      description: "Optional: Only trigger for folders created in this specific parent folder",
      required: !1
    })
  },
  sampleData: {
    folderId: "IEAAABDMI4AB5FML",
    webhookId: "IEAAABDMJAAAABA7",
    eventAuthorId: "KUAAABLG",
    eventType: "FolderCreated",
    lastUpdatedDate: "2024-09-05T07:49:34Z"
  },
  onEnable: async (context) => {
    let webhookUrl = context.webhookUrl, { parentFolderId } = context.propsValue, webhookData = {
      hookUrl: webhookUrl
    };
    parentFolderId && (webhookData.folderId = parentFolderId);
    let request = {
      method: import_pieces_common11.HttpMethod.POST,
      url: `${wrikeCommon.baseUrl}/webhooks`,
      body: webhookData,
      headers: {
        Authorization: `Bearer ${context.auth.access_token}`,
        "Content-Type": "application/json"
      }
    }, { status, body } = await import_pieces_common11.httpClient.sendRequest(request);
    if (status !== 200)
      throw new Error(
        `Failed to register webhook. Status: ${status}, Body: ${JSON.stringify(
          body
        )}`
      );
    await context.store.put("webhook_id", body.data[0].id);
  },
  onDisable: async (context) => {
    let webhookId = await context.store.get("webhook_id");
    if (webhookId) {
      let request = {
        method: import_pieces_common11.HttpMethod.DELETE,
        url: `${wrikeCommon.baseUrl}/webhooks/${webhookId}`,
        headers: {
          Authorization: `Bearer ${context.auth.access_token}`
        }
      };
      try {
        await import_pieces_common11.httpClient.sendRequest(request), await context.store.delete("webhook_id");
      } catch (error) {
        console.warn("Failed to unregister webhook:", error);
      }
    }
  },
  run: async (context) => {
    let payload = context.payload.body;
    return payload && payload.eventType === "FolderCreated" ? payload : [];
  }
});

// ../ap/packages/pieces/community/wrike/src/lib/triggers/new-subtask.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var import_pieces_common12 = require("@activepieces/pieces-common"), import_pieces_framework15 = require("@activepieces/pieces-framework");
var newSubtask = (0, import_pieces_framework15.createTrigger)({
  name: "new_subtask_created",
  displayName: "New Subtask Created",
  description: "Triggers when a subtask is created.",
  aiMetadata: {
    description: "Fires when a task gains a parent in Wrike (the TaskParentsAdded event), representing a subtask being created under another task; can be scoped to a single parent task so it only fires for subtasks added beneath it."
  },
  auth: wrikeAuth,
  type: import_pieces_framework15.TriggerStrategy.WEBHOOK,
  props: {
    parentTaskId: import_pieces_framework14.Property.ShortText({
      displayName: "Parent Task ID",
      description: "Optional: Only trigger for subtasks created under this specific parent task",
      required: !1
    })
  },
  sampleData: {
    addedParents: ["IEAAABDCI4AB5FLD", "IEAAABDCI4AB5FLE"],
    taskId: "IEAAABDCKQAB5FLC",
    webhookId: "IEAAABDCJAAAABAW",
    eventAuthorId: "KUAAABKY",
    eventType: "TaskParentsAdded",
    lastUpdatedDate: "2024-09-05T07:22:25Z"
  },
  onEnable: async (context) => {
    let webhookUrl = context.webhookUrl, { parentTaskId } = context.propsValue, webhookData = {
      hookUrl: webhookUrl
    };
    parentTaskId && (webhookData.taskId = parentTaskId);
    let request = {
      method: import_pieces_common12.HttpMethod.POST,
      url: `${wrikeCommon.baseUrl}/webhooks`,
      body: webhookData,
      headers: {
        Authorization: `Bearer ${context.auth.access_token}`,
        "Content-Type": "application/json"
      }
    }, { status, body } = await import_pieces_common12.httpClient.sendRequest(request);
    if (status !== 200)
      throw new Error(
        `Failed to register webhook. Status: ${status}, Body: ${JSON.stringify(
          body
        )}`
      );
    await context.store.put("webhook_id", body.data[0].id);
  },
  onDisable: async (context) => {
    let webhookId = await context.store.get("webhook_id");
    if (webhookId) {
      let request = {
        method: import_pieces_common12.HttpMethod.DELETE,
        url: `${wrikeCommon.baseUrl}/webhooks/${webhookId}`,
        headers: {
          Authorization: `Bearer ${context.auth.access_token}`
        }
      };
      try {
        await import_pieces_common12.httpClient.sendRequest(request), await context.store.delete("webhook_id");
      } catch (error) {
        console.warn("Failed to unregister webhook:", error);
      }
    }
  },
  run: async (context) => {
    let payload = context.payload.body;
    return payload && payload.eventType === "TaskParentsAdded" ? payload : [payload];
  }
});

// ../ap/packages/pieces/community/wrike/src/index.ts
var wrike = (0, import_pieces_framework16.createPiece)({
  displayName: "Wrike",
  auth: wrikeAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/wrike.png",
  categories: [import_pieces_framework17.PieceCategory.PRODUCTIVITY],
  authors: ["owuzo", "sanket-a11y"],
  actions: [
    createTask,
    updateTask,
    createFolder,
    createProject,
    addComment,
    uploadAttachment,
    findTask,
    findFolder,
    (0, import_pieces_common13.createCustomApiCallAction)({
      baseUrl: () => "https://www.wrike.com/api/v4",
      auth: wrikeAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.access_token}`
      })
    })
  ],
  triggers: [
    newTask,
    newFolder,
    newSubtask
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  wrike
});
