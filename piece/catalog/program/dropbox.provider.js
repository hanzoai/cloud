"use strict";
var __defProp = Object.defineProperty, __defProps = Object.defineProperties, __getOwnPropDesc = Object.getOwnPropertyDescriptor, __getOwnPropDescs = Object.getOwnPropertyDescriptors, __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __knownSymbol = (name, symbol) => (symbol = Symbol[name]) ? symbol : /* @__PURE__ */ Symbol.for("Symbol." + name);
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
var __forAwait = (obj, it, method) => (it = obj[__knownSymbol("asyncIterator")]) ? it.call(obj) : (obj = obj[__knownSymbol("iterator")](), it = {}, method = (key, fn) => (fn = obj[key]) && (it[key] = (arg) => new Promise((yes, no, done) => (arg = fn.call(obj, arg), done = arg.done, Promise.resolve(arg.value).then((value) => yes({ value, done }), no)))), method("next"), method("return"), it);

// ../ap/packages/pieces/community/dropbox/src/index.ts
var index_exports = {};
__export(index_exports, {
  dropbox: () => dropbox
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common15 = require("@activepieces/pieces-common"), import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_framework17 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/dropbox/src/lib/actions/copy-file.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/dropbox/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), dropboxAuth = import_pieces_framework.PieceAuth.OAuth2({
  description: "",
  authUrl: "https://www.dropbox.com/oauth2/authorize",
  tokenUrl: "https://api.dropboxapi.com/oauth2/token",
  required: !0,
  // include token_access_type=offline as a parameter on Authorization URL in order to return a refresh_token
  extra: { token_access_type: "offline" },
  scope: [
    "files.metadata.write",
    "files.metadata.read",
    "files.content.write",
    "files.content.read"
  ]
});

// ../ap/packages/pieces/community/dropbox/src/lib/output-schemas.ts
var entryFields = [
  { key: "name", label: "Name" },
  { key: "id", label: "ID" },
  { key: "path_display", label: "Path" },
  { key: "path_lower", label: "Path (Lowercase)" }
], fileFields = [
  ...entryFields,
  { key: "size", label: "Size", format: "filesize" },
  { key: "client_modified", label: "Client Modified", format: "datetime" },
  { key: "server_modified", label: "Server Modified", format: "datetime" },
  { key: "rev", label: "Revision" },
  { key: "content_hash", label: "Content Hash" },
  { key: "is_downloadable", label: "Is Downloadable", format: "boolean" }
], uploadedFileOutputSchema = { fields: fileFields }, fileMetadataOutputSchema = {
  fields: [{ key: "metadata", label: "File", children: fileFields }]
}, folderMetadataOutputSchema = {
  fields: [{ key: "metadata", label: "Folder", children: entryFields }]
}, getFileLinkOutputSchema = {
  fields: [
    { key: "link", label: "Temporary Link", format: "url" },
    { key: "metadata", label: "File", children: fileFields }
  ]
}, downloadFileOutputSchema = {
  fields: [
    {
      key: "file",
      label: "File",
      format: "url",
      description: "Signed download URL for the retrieved file."
    }
  ]
}, listFolderOutputSchema = {
  fields: [
    { key: "entries", label: "Entries", labelKey: "name", listItems: entryFields },
    { key: "has_more", label: "Has More", format: "boolean" },
    { key: "cursor", label: "Cursor" }
  ]
}, searchOutputSchema = {
  fields: [
    {
      key: "matches",
      label: "Matches",
      labelKey: "metadata.metadata.name",
      listItems: entryFields.map((field) => {
        var _a;
        return __spreadProps(__spreadValues({}, field), {
          value: `metadata.metadata.${(_a = field.value) != null ? _a : field.key}`
        });
      })
    },
    { key: "has_more", label: "Has More", format: "boolean" }
  ]
}, newFolderTriggerOutputSchema = { fields: entryFields };

// ../ap/packages/pieces/community/dropbox/src/lib/actions/copy-file.ts
var dropboxCopyFile = (0, import_pieces_framework2.createAction)({
  auth: dropboxAuth,
  name: "copy_dropbox_file",
  classification: "WRITE",
  description: "Copy a file",
  audience: "both",
  aiMetadata: { description: "Copies the file at the source path to a new destination path within Dropbox, leaving the original in place; optionally autorenames on conflict. Use to duplicate a single file. Not idempotent: each call creates a copy, so repeating it errors on conflict or, with autorename, produces additional duplicates.", idempotent: !1 },
  displayName: "Copy file",
  outputSchema: fileMetadataOutputSchema,
  props: {
    from_path: import_pieces_framework2.Property.ShortText({
      displayName: "From Path",
      description: "The source path of the file (e.g. /folder1/sourcefile.txt)",
      required: !0
    }),
    to_path: import_pieces_framework2.Property.ShortText({
      displayName: "To Path",
      description: "The destination path for the copied (e.g. /folder2/destinationfile.txt)",
      required: !0
    }),
    autorename: import_pieces_framework2.Property.Checkbox({
      displayName: "Auto Rename",
      description: "If there's a conflict, have the Dropbox server try to autorename the file to avoid conflict.",
      defaultValue: !1,
      required: !1
    }),
    allow_ownership_transfer: import_pieces_framework2.Property.Checkbox({
      displayName: "Allow Ownership Transfer",
      description: "Allows copy by owner even if it would result in an ownership transfer.",
      defaultValue: !1,
      required: !1
    })
  },
  async run(context) {
    let params = {
      from_path: context.propsValue.from_path,
      to_path: context.propsValue.to_path,
      autorename: context.propsValue.autorename,
      allow_ownership_transfer: context.propsValue.allow_ownership_transfer
    };
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://api.dropboxapi.com/2/files/copy_v2",
      headers: {
        "Content-Type": "application/json"
      },
      body: params,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: context.auth.access_token
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/dropbox/src/lib/actions/copy-folder.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var dropboxCopyFolder = (0, import_pieces_framework3.createAction)({
  auth: dropboxAuth,
  name: "copy_dropbox_folder",
  classification: "WRITE",
  description: "Copy a folder",
  audience: "both",
  aiMetadata: { description: "Copies the folder at the source path, including its contents, to a new destination path within Dropbox, leaving the original in place; optionally autorenames on conflict. Use to duplicate an entire directory. Not idempotent: each call creates a copy, so repeating it errors on conflict or, with autorename, produces additional duplicates.", idempotent: !1 },
  displayName: "Copy folder",
  outputSchema: folderMetadataOutputSchema,
  props: {
    from_path: import_pieces_framework3.Property.ShortText({
      displayName: "From Path",
      description: "The source path of the folder (e.g. /folder1/sourceFolder)",
      required: !0
    }),
    to_path: import_pieces_framework3.Property.ShortText({
      displayName: "To Path",
      description: "The destination path for the copied folder (e.g. /folder2/destinationFolder)",
      required: !0
    }),
    autorename: import_pieces_framework3.Property.Checkbox({
      displayName: "Auto Rename",
      description: "If there's a conflict, have the Dropbox server try to autorename the folder to avoid conflict.",
      defaultValue: !1,
      required: !1
    }),
    allow_ownership_transfer: import_pieces_framework3.Property.Checkbox({
      displayName: "Allow Ownership Transfer",
      description: "Allows copy by owner even if it would result in an ownership transfer.",
      defaultValue: !1,
      required: !1
    })
  },
  async run(context) {
    let params = {
      from_path: context.propsValue.from_path,
      to_path: context.propsValue.to_path,
      autorename: context.propsValue.autorename,
      allow_ownership_transfer: context.propsValue.allow_ownership_transfer
    };
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://api.dropboxapi.com/2/files/copy_v2",
      headers: {
        "Content-Type": "application/json"
      },
      body: params,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: context.auth.access_token
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/dropbox/src/lib/actions/create-new-folder.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var dropboxCreateNewFolder = (0, import_pieces_framework4.createAction)({
  auth: dropboxAuth,
  name: "create_new_dropbox_folder",
  classification: "WRITE",
  description: "Create a new empty folder",
  audience: "both",
  aiMetadata: { description: "Creates a new empty folder at the given Dropbox path; optionally autorenames on conflict. Use to set up a destination directory before placing files. Not idempotent: a repeat call for an existing path errors (or, with autorename, creates a differently named folder).", idempotent: !1 },
  displayName: "Create New Folder",
  outputSchema: folderMetadataOutputSchema,
  props: {
    path: import_pieces_framework4.Property.ShortText({
      displayName: "Path",
      description: "The path of the new folder e.g. /Homework/math",
      required: !0
    }),
    autorename: import_pieces_framework4.Property.Checkbox({
      displayName: "Auto Rename",
      description: "If there's a conflict, have the Dropbox server try to autorename the folder to avoid the conflict. The default for this field is False.",
      required: !1
    })
  },
  async run(context) {
    let body = {
      autorename: !!context.propsValue.autorename,
      path: context.propsValue.path
    }, request = {
      method: import_pieces_common3.HttpMethod.POST,
      url: "https://api.dropboxapi.com/2/files/create_folder_v2",
      body,
      authentication: {
        type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
        token: context.auth.access_token
      }
    }, result = await import_pieces_common3.httpClient.sendRequest(request);
    return result.status == 200 ? result.body : result;
  }
});

// ../ap/packages/pieces/community/dropbox/src/lib/actions/create-new-text-file.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var dropboxCreateNewTextFile = (0, import_pieces_framework5.createAction)({
  auth: dropboxAuth,
  name: "create_new_dropbox_text_file",
  classification: "WRITE",
  description: "Create a new text file from text input",
  audience: "both",
  aiMetadata: { description: "Writes the provided text content to a new file at the given Dropbox path (upload in add mode). Use when an agent needs to persist generated or supplied text directly as a file without first producing a file object. Not idempotent: each call uploads, so repeating it can create autorenamed duplicates rather than overwriting.", idempotent: !1 },
  displayName: "Create New Text File",
  outputSchema: uploadedFileOutputSchema,
  props: {
    path: import_pieces_framework5.Property.ShortText({
      displayName: "Path",
      description: "The path of the new folder e.g. /Homework/math",
      required: !0
    }),
    text: import_pieces_framework5.Property.LongText({
      displayName: "Text",
      description: "The text to write into the file.",
      required: !0
    }),
    autorename: import_pieces_framework5.Property.Checkbox({
      displayName: "Autorename",
      description: "If there's a conflict, have the Dropbox server try to autorename the folder to avoid the conflict. The default for this field is False.",
      required: !1
    }),
    mute: import_pieces_framework5.Property.Checkbox({
      displayName: "Mute",
      description: "Normally, users are made aware of any file modifications in their Dropbox account via notifications in the client software. If true, this tells the clients that this modification shouldn't result in a user notification.",
      required: !1
    }),
    strict_conflict: import_pieces_framework5.Property.Checkbox({
      displayName: "Strict conflict",
      description: `Be more strict about how each WriteMode detects conflict. For example, always return a conflict error when mode = WriteMode.update and the given "rev" doesn't match the existing file's "rev", even if the existing file has been deleted.`,
      required: !1
    })
  },
  async run(context) {
    let params = {
      autorename: context.propsValue.autorename,
      path: context.propsValue.path,
      mode: "add",
      mute: context.propsValue.mute,
      strict_conflict: context.propsValue.strict_conflict
    }, request = {
      method: import_pieces_common4.HttpMethod.POST,
      url: "https://content.dropboxapi.com/2/files/upload",
      body: Buffer.from(context.propsValue.text, "utf-8"),
      authentication: {
        type: import_pieces_common4.AuthenticationType.BEARER_TOKEN,
        token: context.auth.access_token
      },
      headers: {
        "Dropbox-API-Arg": JSON.stringify(params),
        "Content-Type": "application/octet-stream"
      }
    }, result = await import_pieces_common4.httpClient.sendRequest(request);
    return result.status == 200 ? result.body : result;
  }
});

// ../ap/packages/pieces/community/dropbox/src/lib/actions/delete-file.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var dropboxDeleteFile = (0, import_pieces_framework6.createAction)({
  auth: dropboxAuth,
  name: "delete_dropbox_file",
  classification: "DESTRUCTIVE",
  description: "Delete a file",
  audience: "both",
  aiMetadata: { description: "Permanently deletes the file at the given Dropbox path. Use to remove a specific file an agent has resolved the path for. Effectively idempotent on the end state once the path is gone, but a repeat call fails because the path no longer exists; treat as a destructive, non-recoverable mutation.", idempotent: !1 },
  displayName: "Delete file",
  outputSchema: fileMetadataOutputSchema,
  props: {
    path: import_pieces_framework6.Property.ShortText({
      displayName: "Path",
      description: "The path of the file to be deleted (e.g. /folder1/file.txt)",
      required: !0
    })
  },
  async run(context) {
    let params = {
      path: context.propsValue.path
    };
    return (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.POST,
      url: "https://api.dropboxapi.com/2/files/delete_v2",
      headers: {
        "Content-Type": "application/json"
      },
      body: { path: context.propsValue.path },
      authentication: {
        type: import_pieces_common5.AuthenticationType.BEARER_TOKEN,
        token: context.auth.access_token
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/dropbox/src/lib/actions/delete-folder.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var dropboxDeleteFolder = (0, import_pieces_framework7.createAction)({
  auth: dropboxAuth,
  name: "delete_dropbox_folder",
  classification: "DESTRUCTIVE",
  description: "Delete a folder",
  audience: "both",
  aiMetadata: { description: "Permanently deletes the folder at the given Dropbox path along with all of its contents. Use to remove an entire directory. Effectively idempotent on the end state once the path is gone, but a repeat call fails because the path no longer exists; treat as a destructive, non-recoverable mutation.", idempotent: !1 },
  displayName: "Delete folder",
  outputSchema: folderMetadataOutputSchema,
  props: {
    path: import_pieces_framework7.Property.ShortText({
      displayName: "Path",
      description: "The path of the folder to be deleted (e.g. /folder1)",
      required: !0
    })
  },
  async run(context) {
    let params = {
      path: context.propsValue.path
    };
    return (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.POST,
      url: "https://api.dropboxapi.com/2/files/delete_v2",
      headers: {
        "Content-Type": "application/json"
      },
      body: params,
      authentication: {
        type: import_pieces_common6.AuthenticationType.BEARER_TOKEN,
        token: context.auth.access_token
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/dropbox/src/lib/actions/get-file-link.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var dropboxGetFileLink = (0, import_pieces_framework8.createAction)({
  auth: dropboxAuth,
  name: "get_dropbox_file_link",
  classification: "READ",
  description: "Get a temporary file link",
  audience: "both",
  aiMetadata: { description: "Returns a temporary, directly downloadable URL for the file at the given Dropbox path. Use when an agent needs a shareable or fetchable link to file contents rather than downloading the bytes into the flow. Read-only lookup; safe to repeat, though the returned URL is short-lived.", idempotent: !0 },
  displayName: "Get temporary file link",
  outputSchema: getFileLinkOutputSchema,
  props: {
    path: import_pieces_framework8.Property.ShortText({
      displayName: "Path",
      description: "The path of the file (e.g. /folder1/file.txt)",
      required: !0
    })
  },
  async run(context) {
    let params = {
      path: context.propsValue.path
    };
    return (await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.POST,
      url: "https://api.dropboxapi.com/2/files/get_temporary_link",
      headers: {
        "Content-Type": "application/json"
      },
      body: { path: context.propsValue.path },
      authentication: {
        type: import_pieces_common7.AuthenticationType.BEARER_TOKEN,
        token: context.auth.access_token
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/dropbox/src/lib/actions/list-a-folder.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var dropboxListAFolder = (0, import_pieces_framework9.createAction)({
  auth: dropboxAuth,
  name: "list_dropbox_folder",
  classification: "SEARCH",
  description: "List the contents of a folder",
  audience: "both",
  aiMetadata: { description: "Lists the files and subfolders within the given Dropbox folder path (use an empty string for the root), optionally recursing into all subfolders. Use to enumerate folder contents and discover item paths/IDs. Read-only; safe to repeat and returns the same listing for unchanged folders.", idempotent: !0 },
  displayName: "List a folder",
  outputSchema: listFolderOutputSchema,
  props: {
    path: import_pieces_framework9.Property.ShortText({
      displayName: "Path",
      description: "The path of the folder to be listed (e.g. /folder1). Use an empty string for the root folder.",
      required: !0
    }),
    recursive: import_pieces_framework9.Property.Checkbox({
      displayName: "Recursive",
      description: "If set to true, the list folder operation will be applied recursively to all subfolders and the response will contain contents of all subfolders.",
      defaultValue: !1,
      required: !1
    }),
    limit: import_pieces_framework9.Property.Number({
      displayName: "Limit",
      description: "The maximum number of results to return (between 1 and 2000). Default is 2000 if not specified.",
      required: !1
    })
  },
  async run(context) {
    let params = {
      path: context.propsValue.path,
      recursive: context.propsValue.recursive,
      limit: context.propsValue.limit || 2e3
    };
    return (await import_pieces_common8.httpClient.sendRequest({
      method: import_pieces_common8.HttpMethod.POST,
      url: "https://api.dropboxapi.com/2/files/list_folder",
      headers: {
        "Content-Type": "application/json"
      },
      body: params,
      authentication: {
        type: import_pieces_common8.AuthenticationType.BEARER_TOKEN,
        token: context.auth.access_token
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/dropbox/src/lib/actions/move-file.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var dropboxMoveFile = (0, import_pieces_framework10.createAction)({
  auth: dropboxAuth,
  name: "move_dropbox_file",
  classification: "WRITE",
  description: "Move a file",
  audience: "both",
  aiMetadata: { description: "Moves (or renames) the file at the source path to a new destination path within Dropbox; optionally autorenames on conflict. Use to relocate or rename a single file. Not idempotent: after a successful move the source no longer exists, so repeating the call fails or, with autorename, can produce a differently named copy.", idempotent: !1 },
  displayName: "Move file",
  outputSchema: fileMetadataOutputSchema,
  props: {
    from_path: import_pieces_framework10.Property.ShortText({
      displayName: "From Path",
      description: "The current path of the file (e.g. /folder1/oldfile.txt)",
      required: !0
    }),
    to_path: import_pieces_framework10.Property.ShortText({
      displayName: "To Path",
      description: "The new path for the file (e.g. /folder2/newfile.txt)",
      required: !0
    }),
    autorename: import_pieces_framework10.Property.Checkbox({
      displayName: "Auto Rename",
      description: "If there's a conflict, have the Dropbox server try to autorename the file to avoid conflict.",
      defaultValue: !1,
      required: !1
    }),
    allow_ownership_transfer: import_pieces_framework10.Property.Checkbox({
      displayName: "Allow Ownership Transfer",
      description: "Allows moves by owner even if it would result in an ownership transfer.",
      defaultValue: !1,
      required: !1
    })
  },
  async run(context) {
    let params = {
      from_path: context.propsValue.from_path,
      to_path: context.propsValue.to_path,
      autorename: context.propsValue.autorename,
      allow_ownership_transfer: context.propsValue.allow_ownership_transfer
    };
    return (await import_pieces_common9.httpClient.sendRequest({
      method: import_pieces_common9.HttpMethod.POST,
      url: "https://api.dropboxapi.com/2/files/move_v2",
      headers: {
        "Content-Type": "application/json"
      },
      body: params,
      authentication: {
        type: import_pieces_common9.AuthenticationType.BEARER_TOKEN,
        token: context.auth.access_token
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/dropbox/src/lib/actions/move-folder.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var dropboxMoveFolder = (0, import_pieces_framework11.createAction)({
  auth: dropboxAuth,
  name: "move_dropbox_folder",
  classification: "WRITE",
  description: "Move a folder",
  audience: "both",
  aiMetadata: { description: "Moves (or renames) the folder at the source path, including its contents, to a new destination path within Dropbox; optionally autorenames on conflict. Use to relocate or rename an entire directory. Not idempotent: after a successful move the source no longer exists, so repeating the call fails or, with autorename, produces a differently named folder.", idempotent: !1 },
  displayName: "Move folder",
  outputSchema: folderMetadataOutputSchema,
  props: {
    from_path: import_pieces_framework11.Property.ShortText({
      displayName: "From Path",
      description: "The current path of the folder (e.g. /folder1/sourceFolder)",
      required: !0
    }),
    to_path: import_pieces_framework11.Property.ShortText({
      displayName: "To Path",
      description: "The new path for the folder (e.g. /folder2/destinationFolder)",
      required: !0
    }),
    autorename: import_pieces_framework11.Property.Checkbox({
      displayName: "Auto Rename",
      description: "If there's a conflict, have the Dropbox server try to autorename the folder to avoid conflict.",
      defaultValue: !1,
      required: !1
    }),
    allow_ownership_transfer: import_pieces_framework11.Property.Checkbox({
      displayName: "Allow Ownership Transfer",
      description: "Allows moves by owner even if it would result in an ownership transfer.",
      defaultValue: !1,
      required: !1
    })
  },
  async run(context) {
    let params = {
      from_path: context.propsValue.from_path,
      to_path: context.propsValue.to_path,
      autorename: context.propsValue.autorename,
      allow_ownership_transfer: context.propsValue.allow_ownership_transfer
    };
    return (await import_pieces_common10.httpClient.sendRequest({
      method: import_pieces_common10.HttpMethod.POST,
      url: "https://api.dropboxapi.com/2/files/move_v2",
      headers: {
        "Content-Type": "application/json"
      },
      body: params,
      authentication: {
        type: import_pieces_common10.AuthenticationType.BEARER_TOKEN,
        token: context.auth.access_token
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/dropbox/src/lib/actions/search.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var dropboxSearch = (0, import_pieces_framework12.createAction)({
  auth: dropboxAuth,
  name: "search_dropbox",
  classification: "SEARCH",
  description: "Search for files and folders",
  audience: "both",
  aiMetadata: { description: "Searches a Dropbox account for files and folders whose name or content matches a query string (minimum 3 characters), optionally scoped to a path and filtered by file status, extensions, categories, or account. Use to locate items and obtain their paths/IDs before acting on them. Read-only; repeating the same search is safe and returns the same matches.", idempotent: !0 },
  displayName: "Search",
  outputSchema: searchOutputSchema,
  props: {
    query: import_pieces_framework12.Property.ShortText({
      displayName: "Query",
      description: "The search string. Must be at least 3 characters.",
      required: !0
    }),
    path: import_pieces_framework12.Property.ShortText({
      displayName: "Path",
      description: "The path to search in. If not specified, the search is performed from the root.",
      required: !1
    }),
    max_results: import_pieces_framework12.Property.Number({
      displayName: "Max Results",
      description: "The maximum number of search results to return (up to 1000). Default is 100 if not specified.",
      required: !1
    }),
    order_by: import_pieces_framework12.Property.StaticDropdown({
      displayName: "Order By",
      description: "Specified property of the order of search results.",
      options: {
        options: [
          { label: "Relevance", value: "relevance" },
          { label: "Modified Time", value: "modified_time" }
        ]
      },
      defaultValue: "relevance",
      required: !1
    }),
    file_status: import_pieces_framework12.Property.StaticDropdown({
      displayName: "File Status",
      description: "Restricts search to the given file status.",
      options: {
        options: [
          { label: "Active", value: "active" },
          { label: "Deleted", value: "deleted" }
        ]
      },
      defaultValue: "active",
      required: !1
    }),
    filename_only: import_pieces_framework12.Property.Checkbox({
      displayName: "Filename Only",
      description: "Restricts search to only match on filenames.",
      defaultValue: !1,
      required: !1
    }),
    file_extensions: import_pieces_framework12.Property.ShortText({
      displayName: "File Extensions",
      description: "Restricts search to only the extensions specified (comma-separated).",
      required: !1
    }),
    file_categories: import_pieces_framework12.Property.ShortText({
      displayName: "File Categories",
      description: "Restricts search to only the file categories specified (comma-separated).",
      required: !1
    }),
    account_id: import_pieces_framework12.Property.ShortText({
      displayName: "Account ID",
      description: "Restricts results to the given account id.",
      required: !1
    })
  },
  async run(context) {
    let options = {
      path: context.propsValue.path || "",
      max_results: context.propsValue.max_results || 100,
      file_status: context.propsValue.file_status,
      filename_only: context.propsValue.filename_only,
      file_extensions: context.propsValue.file_extensions ? context.propsValue.file_extensions.split(",") : void 0,
      file_categories: context.propsValue.file_categories ? context.propsValue.file_categories.split(",") : void 0,
      account_id: context.propsValue.account_id
    }, requestBody = {
      query: context.propsValue.query,
      options
    };
    return (await import_pieces_common11.httpClient.sendRequest({
      method: import_pieces_common11.HttpMethod.POST,
      url: "https://api.dropboxapi.com/2/files/search_v2",
      headers: {
        "Content-Type": "application/json"
      },
      body: requestBody,
      authentication: {
        type: import_pieces_common11.AuthenticationType.BEARER_TOKEN,
        token: context.auth.access_token
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/dropbox/src/lib/actions/upload-file.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var CONTENT_API_URL = "https://content.dropboxapi.com/2", SINGLE_REQUEST_LIMIT = 150 * 1024 * 1024, CHUNK_SIZE = 8 * 1024 * 1024, dropboxUploadFile = (0, import_pieces_framework13.createAction)({
  auth: dropboxAuth,
  name: "upload_dropbox_file",
  classification: "WRITE",
  description: "Upload a file",
  audience: "both",
  aiMetadata: { description: "Uploads a file (provided as a URL or base64 file object) to the given Dropbox path in add mode. Files over 150 MB are uploaded in chunks automatically. Use to store binary or arbitrary file content; prefer the create-text-file action when the source is plain text. Not idempotent: each call uploads, so repeating it can create autorenamed duplicates rather than overwriting.", idempotent: !1 },
  displayName: "Upload file",
  outputSchema: uploadedFileOutputSchema,
  props: {
    path: import_pieces_framework13.Property.ShortText({
      displayName: "Path",
      description: "The path where the file should be saved (e.g. /folder1/file.txt)",
      required: !0
    }),
    file: import_pieces_framework13.Property.File({
      displayName: "File",
      description: "The file URL or base64 to upload",
      required: !0,
      streaming: !0
    }),
    autorename: import_pieces_framework13.Property.Checkbox({
      displayName: "Auto Rename",
      description: "If there's a conflict, as determined by mode, have the Dropbox server try to autorename the file to avoid conflict.",
      defaultValue: !1,
      required: !1
    }),
    mute: import_pieces_framework13.Property.Checkbox({
      displayName: "Mute",
      description: "Normally, users are made aware of any file modifications in their Dropbox account via notifications in the client software. If true, this tells the clients that this modification shouldn't result in a user notification.",
      required: !1
    }),
    strict_conflict: import_pieces_framework13.Property.Checkbox({
      displayName: "Strict conflict",
      description: `Be more strict about how each WriteMode detects conflict. For example, always return a conflict error when mode = WriteMode.update and the given "rev" doesn't match the existing file's "rev", even if the existing file has been deleted.`,
      required: !1
    })
  },
  async run(context) {
    let { body, size } = import_pieces_common12.streamUtils.toStreamingBody(context.propsValue.file), token = context.auth.access_token, commit = {
      autorename: context.propsValue.autorename,
      path: context.propsValue.path,
      mode: "add",
      mute: context.propsValue.mute,
      strict_conflict: context.propsValue.strict_conflict
    };
    return size != null && size <= SINGLE_REQUEST_LIMIT ? await sendToDropbox({
      endpoint: "files/upload",
      apiArg: commit,
      body,
      contentLength: size,
      token
    }) : await uploadInSession({ body, commit, token });
  }
});
async function uploadInSession({
  body,
  commit,
  token
}) {
  let chunks = import_pieces_common12.streamUtils.readChunks({ readable: body, chunkSize: CHUNK_SIZE }), firstChunk = await chunks.next(), { session_id } = await sendToDropbox({
    endpoint: "files/upload_session/start",
    apiArg: { close: !1 },
    body: firstChunk.done ? Buffer.alloc(0) : firstChunk.value,
    token
  }), offset = firstChunk.done ? 0 : firstChunk.value.length;
  try {
    for (var iter = __forAwait(chunks), more, temp, error; more = !(temp = await iter.next()).done; more = !1) {
      let chunk = temp.value;
      await sendToDropbox({
        endpoint: "files/upload_session/append_v2",
        apiArg: { cursor: { session_id, offset }, close: !1 },
        body: chunk,
        token
      }), offset += chunk.length;
    }
  } catch (temp) {
    error = [temp];
  } finally {
    try {
      more && (temp = iter.return) && await temp.call(iter);
    } finally {
      if (error)
        throw error[0];
    }
  }
  return await sendToDropbox({
    endpoint: "files/upload_session/finish",
    apiArg: { cursor: { session_id, offset }, commit },
    body: Buffer.alloc(0),
    token
  });
}
async function sendToDropbox({
  endpoint,
  apiArg,
  body,
  contentLength,
  token
}) {
  return (await import_pieces_common12.httpClient.sendRequest({
    method: import_pieces_common12.HttpMethod.POST,
    url: `${CONTENT_API_URL}/${endpoint}`,
    body,
    headers: {
      "Dropbox-API-Arg": encodeApiArg(apiArg),
      "Content-Type": "application/octet-stream",
      "Content-Length": contentLength != null ? String(contentLength) : void 0
    },
    authentication: {
      type: import_pieces_common12.AuthenticationType.BEARER_TOKEN,
      token
    }
  })).body;
}
function encodeApiArg(value) {
  return JSON.stringify(value).replace(/[\u007f-\uffff]/g, (character) => "\\u" + ("000" + character.charCodeAt(0).toString(16)).slice(-4));
}

// ../ap/packages/pieces/community/dropbox/src/lib/actions/download-file.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common");
var dropboxDownloadFile = (0, import_pieces_framework14.createAction)({
  auth: dropboxAuth,
  name: "downloadFile",
  classification: "READ",
  displayName: "Download File",
  description: "Download a File from Dropbox",
  audience: "both",
  aiMetadata: { description: "Downloads the file at the given Dropbox path and returns it as a file object for use by later steps. Use to retrieve file contents into a flow. Read-only on Dropbox; repeating the call with the same path is safe and yields the same file.", idempotent: !0 },
  outputSchema: downloadFileOutputSchema,
  props: {
    path: import_pieces_framework14.Property.ShortText({
      displayName: "Path",
      description: "The path of the file (e.g. /folder1/file.txt)",
      required: !0
    })
  },
  async run(context) {
    var _a;
    let fileName = ((_a = context.propsValue.path.match(/[^/]+$/)) != null ? _a : ["output.pdf"])[0], dropboxApiArg = JSON.stringify({ path: context.propsValue.path }).replace(/[\u007f-\uffff]/g, (c) => "\\u" + ("000" + c.charCodeAt(0).toString(16)).slice(-4)), result = await import_pieces_common13.httpClient.sendRequest({
      method: import_pieces_common13.HttpMethod.POST,
      url: "https://content.dropboxapi.com/2/files/download",
      headers: {
        "Content-Type": "application/octet-stream",
        "Dropbox-API-Arg": dropboxApiArg
      },
      authentication: {
        type: import_pieces_common13.AuthenticationType.BEARER_TOKEN,
        token: context.auth.access_token
      },
      responseType: "stream"
    });
    return {
      file: await context.files.write({
        fileName,
        data: result.body
      })
    };
  }
});

// ../ap/packages/pieces/community/dropbox/src/lib/triggers/new-folder.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_common14 = require("@activepieces/pieces-common");
var getLatestCursor = async (accessToken, path, recursive) => (await import_pieces_common14.httpClient.sendRequest({
  method: import_pieces_common14.HttpMethod.POST,
  url: "https://api.dropboxapi.com/2/files/list_folder/get_latest_cursor",
  headers: { "Content-Type": "application/json" },
  body: { path, recursive, include_deleted: !1 },
  authentication: {
    type: import_pieces_common14.AuthenticationType.BEARER_TOKEN,
    token: accessToken
  }
})).body.cursor, getChangedFolders = async (accessToken, cursor) => {
  let folders = [], currentCursor = cursor, hasMore = !0;
  for (; hasMore; ) {
    let res = await import_pieces_common14.httpClient.sendRequest({
      method: import_pieces_common14.HttpMethod.POST,
      url: "https://api.dropboxapi.com/2/files/list_folder/continue",
      headers: { "Content-Type": "application/json" },
      body: { cursor: currentCursor },
      authentication: {
        type: import_pieces_common14.AuthenticationType.BEARER_TOKEN,
        token: accessToken
      }
    }), newFolders = res.body.entries.filter((e) => e[".tag"] === "folder");
    folders.push(...newFolders), currentCursor = res.body.cursor, hasMore = res.body.has_more;
  }
  return { folders, newCursor: currentCursor };
}, listRecentFolders = async (accessToken, path, recursive) => (await import_pieces_common14.httpClient.sendRequest({
  method: import_pieces_common14.HttpMethod.POST,
  url: "https://api.dropboxapi.com/2/files/list_folder",
  headers: { "Content-Type": "application/json" },
  body: { path, recursive, include_deleted: !1, limit: 100 },
  authentication: {
    type: import_pieces_common14.AuthenticationType.BEARER_TOKEN,
    token: accessToken
  }
})).body.entries.filter((e) => e[".tag"] === "folder").slice(0, 5), dropboxNewFolder = (0, import_pieces_framework15.createTrigger)({
  auth: dropboxAuth,
  name: "new_folder",
  classification: "READ",
  displayName: "New Folder",
  description: "Triggers when a new folder is created inside a watched Dropbox folder.",
  aiMetadata: {
    description: "Fires when a new folder appears inside the watched Dropbox folder path, detected by polling the folder for changes since the last cursor. Each fired event represents one newly created folder; enable recursive watching to also detect folders created within subfolders."
  },
  type: import_pieces_framework15.TriggerStrategy.POLLING,
  outputSchema: newFolderTriggerOutputSchema,
  props: {
    path: import_pieces_framework15.Property.ShortText({
      displayName: "Watched Folder Path",
      description: 'Folder path to watch. Use empty string "" for root.',
      required: !0,
      defaultValue: ""
    }),
    recursive: import_pieces_framework15.Property.Checkbox({
      displayName: "Recursive",
      description: "Watch subfolders recursively. May return many results.",
      required: !1,
      defaultValue: !1
    })
  },
  sampleData: {
    ".tag": "folder",
    name: "Project Docs",
    id: "id:a4ayc_80_OEAAAAAAAAAXw",
    path_display: "/Work/Project Docs",
    path_lower: "/work/project docs"
  },
  onEnable: async (context) => {
    var _a;
    let cursor = await getLatestCursor(
      context.auth.access_token,
      context.propsValue.path || "",
      (_a = context.propsValue.recursive) != null ? _a : !1
    );
    await context.store.put("cursor", cursor);
  },
  onDisable: async (context) => {
    await context.store.delete("cursor");
  },
  run: async (context) => {
    let cursor = await context.store.get("cursor");
    if (!cursor) return [];
    let { folders, newCursor } = await getChangedFolders(
      context.auth.access_token,
      cursor
    );
    return await context.store.put("cursor", newCursor), folders;
  },
  test: async (context) => {
    var _a;
    return listRecentFolders(
      context.auth.access_token,
      context.propsValue.path || "",
      (_a = context.propsValue.recursive) != null ? _a : !1
    );
  }
});

// ../ap/packages/pieces/community/dropbox/src/index.ts
var dropbox = (0, import_pieces_framework16.createPiece)({
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/dropbox.png",
  actions: [
    dropboxSearch,
    dropboxCreateNewTextFile,
    dropboxUploadFile,
    dropboxDownloadFile,
    dropboxGetFileLink,
    dropboxDeleteFile,
    dropboxMoveFile,
    dropboxCopyFile,
    dropboxCreateNewFolder,
    dropboxDeleteFolder,
    dropboxMoveFolder,
    dropboxCopyFolder,
    dropboxListAFolder,
    (0, import_pieces_common15.createCustomApiCallAction)({
      baseUrl: () => "https://api.dropboxapi.com/2",
      auth: dropboxAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.access_token}`
      })
    })
  ],
  displayName: "Dropbox",
  description: "Cloud storage and file synchronization",
  authors: [
    "BastienMe",
    "kishanprmr",
    "MoShizzle",
    "khaledmashaly",
    "abuaboud"
  ],
  categories: [import_pieces_framework17.PieceCategory.CONTENT_AND_FILES],
  triggers: [dropboxNewFolder],
  auth: dropboxAuth
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  dropbox
});
