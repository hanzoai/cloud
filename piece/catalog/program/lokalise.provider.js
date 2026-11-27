"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
};
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

// ../ap/packages/pieces/community/lokalise/src/index.ts
var index_exports = {};
__export(index_exports, {
  lokalise: () => lokalise
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework17 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/lokalise/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), lokaliseAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Token",
  description: "Lokalise API Token. You can generate one from your Lokalise account.",
  required: !0
});

// ../ap/packages/pieces/community/lokalise/src/index.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/lokalise/src/lib/actions/create-comment.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/lokalise/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.lokalise.com/api2";
async function makeRequest(apikey, method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        "X-Api-Token": apikey,
        "Content-Type": "application/json"
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/lokalise/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common"), projectDropdown = import_pieces_framework2.Property.Dropdown({
  auth: lokaliseAuth,
  displayName: "Project",
  description: "Select the Lokalise project",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please authenticate first",
        options: []
      };
    try {
      return {
        disabled: !1,
        options: (await makeRequest(
          auth.secret_text,
          import_pieces_common2.HttpMethod.GET,
          "/projects"
        )).projects.map((project) => ({
          label: project.name,
          value: project.project_id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: []
      };
    }
  }
}), keyIdProp = import_pieces_framework2.Property.Dropdown({
  auth: lokaliseAuth,
  displayName: "Key",
  description: "Select the key",
  required: !0,
  refreshers: ["projectId"],
  options: async ({ auth, projectId }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please authenticate first",
        options: []
      };
    if (!projectId)
      return {
        disabled: !0,
        placeholder: "Please select a project first",
        options: []
      };
    try {
      return {
        disabled: !1,
        options: (await makeRequest(
          auth.secret_text,
          import_pieces_common2.HttpMethod.GET,
          `/projects/${projectId}/keys`
        )).keys.map((key) => ({
          label: key.key_name.other || key.key_name.ios || key.key_name.android || key.key_name.web,
          value: key.key_id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: []
      };
    }
  }
});

// ../ap/packages/pieces/community/lokalise/src/lib/actions/create-comment.ts
var createComment = (0, import_pieces_framework3.createAction)({
  auth: lokaliseAuth,
  name: "createComment",
  displayName: "Create Comment",
  description: "Add comments to a key in your Lokalise project",
  audience: "both",
  aiMetadata: { description: "Post a new comment on a specific translation key in a Lokalise project. Requires the project ID and key ID. Not idempotent: each call appends another comment.", idempotent: !1 },
  props: {
    projectId: projectDropdown,
    keyId: keyIdProp,
    comment: import_pieces_framework3.Property.LongText({
      displayName: "Comment",
      description: "The comment text to add to the key",
      required: !0
    })
  },
  async run(context) {
    let { projectId, keyId, comment } = context.propsValue, body = {
      comments: [
        {
          comment
        }
      ]
    }, path = `/projects/${projectId}/keys/${keyId}/comments`;
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common3.HttpMethod.POST,
      path,
      body
    );
  }
});

// ../ap/packages/pieces/community/lokalise/src/lib/actions/create-project.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var createProject = (0, import_pieces_framework4.createAction)({
  auth: lokaliseAuth,
  name: "createProject",
  displayName: "Create Project",
  description: "Create a new project in your Lokalise team",
  audience: "both",
  aiMetadata: { description: "Create a new localization project in your Lokalise team, optionally setting its type, base language, and target team. Use when starting a new translation workspace. Not idempotent: repeating the call creates duplicate projects.", idempotent: !1 },
  props: {
    projectName: import_pieces_framework4.Property.ShortText({
      displayName: "Project Name",
      description: "Name of the project",
      required: !0
    }),
    description: import_pieces_framework4.Property.LongText({
      displayName: "Description",
      description: "Description of the project",
      required: !1
    }),
    projectType: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Project Type",
      description: "Type of the project",
      required: !1,
      options: {
        options: [
          {
            label: "Web and Mobile (Software Projects)",
            value: "localization_files"
          },
          { label: "Documents (Ad hoc documents)", value: "paged_documents" },
          {
            label: "Marketing Projects (with integrations)",
            value: "content_integration"
          },
          {
            label: "Marketing Projects (Automatically translated)",
            value: "marketing"
          },
          {
            label: "Marketing Projects (Automatically translated with integrations)",
            value: "marketing_integrations"
          }
        ]
      }
    }),
    baseLangIso: import_pieces_framework4.Property.ShortText({
      displayName: "Base Language ISO",
      description: 'Language/locale code of the project base language (e.g., "en", "en-us")',
      required: !1
    }),
    teamId: import_pieces_framework4.Property.ShortText({
      displayName: "Team ID",
      description: "ID of the team to create a project in (numerical ID or UUID). If omitted, the project will be created in your current team",
      required: !1
    }),
    isSegmentationEnabled: import_pieces_framework4.Property.Checkbox({
      displayName: "Enable Segmentation",
      description: "Enable Segmentation feature for project",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let {
      projectName,
      description,
      projectType,
      baseLangIso,
      teamId,
      isSegmentationEnabled
    } = context.propsValue, body = __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({
      name: projectName
    }, description && { description }), projectType && { project_type: projectType }), baseLangIso && { base_lang_iso: baseLangIso }), teamId && { team_id: teamId }), isSegmentationEnabled && { is_segmentation_enabled: !0 });
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common4.HttpMethod.POST,
      "/projects",
      body
    );
  }
});

// ../ap/packages/pieces/community/lokalise/src/lib/actions/create-key.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var createKey = (0, import_pieces_framework5.createAction)({
  auth: lokaliseAuth,
  name: "createKey",
  displayName: "Create Key",
  description: "Create one or more keys in your Lokalise project",
  audience: "both",
  aiMetadata: { description: "Create a new translation key in a Lokalise project, tagged to one or more platforms (iOS, Android, web, etc.). Requires the project ID and key name; platforms are mandatory. Not idempotent: each call adds a key even if the name repeats.", idempotent: !1 },
  props: {
    projectId: projectDropdown,
    keyName: import_pieces_framework5.Property.ShortText({
      displayName: "Key Name",
      description: 'The name/identifier for the key (e.g., "index.welcome")',
      required: !0
    }),
    description: import_pieces_framework5.Property.LongText({
      displayName: "Description",
      description: "Optional description for the key",
      required: !1
    }),
    platforms: import_pieces_framework5.Property.StaticMultiSelectDropdown({
      displayName: "Platforms",
      description: "Select the platforms this key applies to",
      required: !0,
      options: {
        options: [
          { label: "iOS", value: "ios" },
          { label: "Android", value: "android" },
          { label: "Web", value: "web" },
          { label: "Flutter", value: "flutter" },
          { label: "React Native", value: "react_native" },
          { label: "Other", value: "other" }
        ]
      }
    }),
    tags: import_pieces_framework5.Property.Array({
      displayName: "Tags",
      description: 'Comma-separated tags for the key (e.g., "urgent,ui")',
      required: !1
    }),
    useAutomations: import_pieces_framework5.Property.Checkbox({
      displayName: "Use Automations",
      description: "Whether to run automations on the new key translations",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { projectId, keyName, description, platforms, tags, useAutomations } = context.propsValue, body = {
      keys: [
        __spreadValues(__spreadValues(__spreadValues({
          key_name: keyName
        }, description && { description }), platforms && platforms.length > 0 && { platforms }), tags && tags.length > 0 && { tags })
      ],
      use_automations: useAutomations
    }, path = `/projects/${projectId}/keys`;
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common5.HttpMethod.POST,
      path,
      body
    );
  }
});

// ../ap/packages/pieces/community/lokalise/src/lib/actions/create-task.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var createTask = (0, import_pieces_framework6.createAction)({
  auth: lokaliseAuth,
  name: "createTask",
  displayName: "Create Task",
  description: "Create a new task in your Lokalise project",
  audience: "both",
  aiMetadata: { description: "Create a workflow task (translation, automatic translation, LQA by AI, or review) in a Lokalise project, scoped to specific keys and target languages. Use to assign translation or review work. Not idempotent: each call creates a separate task.", idempotent: !1 },
  props: {
    projectId: projectDropdown,
    taskTitle: import_pieces_framework6.Property.ShortText({
      displayName: "Task Title",
      description: "Title of the task",
      required: !0
    }),
    description: import_pieces_framework6.Property.LongText({
      displayName: "Description",
      description: "Brief task description. Used as instructions for AI in automatic_translation and lqa_by_ai task types",
      required: !1
    }),
    taskType: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Task Type",
      description: "Type of the task",
      required: !1,
      options: {
        options: [
          { label: "Translation", value: "translation" },
          { label: "Automatic Translation", value: "automatic_translation" },
          { label: "LQA by AI", value: "lqa_by_ai" },
          { label: "Review", value: "review" }
        ]
      }
    }),
    keys: import_pieces_framework6.Property.Array({
      displayName: "Key",
      description: "Comma-separated list of key IDs to include in the task (required unless parent_task_id is specified)",
      required: !1
    }),
    languages: import_pieces_framework6.Property.Array({
      displayName: "Languages",
      description: 'Comma-separated language ISO codes for the task (e.g., "fr,de,es")',
      required: !1,
      properties: {
        languageIso: import_pieces_framework6.Property.ShortText({
          displayName: "Language ISO",
          description: 'Language ISO code (e.g., "fr", "de")',
          required: !0
        })
      }
    }),
    sourceLanguageIso: import_pieces_framework6.Property.ShortText({
      displayName: "Source Language ISO",
      description: "Source language code for the task",
      required: !1
    }),
    dueDate: import_pieces_framework6.Property.ShortText({
      displayName: "Due Date",
      description: 'Due date in format: Y-m-d H:i:s (e.g., "2024-12-31 23:59:59")',
      required: !1
    }),
    autoCloseLanguages: import_pieces_framework6.Property.Checkbox({
      displayName: "Auto Close Languages",
      description: "Whether languages should be closed automatically upon completion. Default is true",
      required: !1,
      defaultValue: !0
    }),
    autoCloseTask: import_pieces_framework6.Property.Checkbox({
      displayName: "Auto Close Task",
      description: "Whether the task should be automatically closed upon all language completion. Default is true",
      required: !1,
      defaultValue: !0
    }),
    closingTags: import_pieces_framework6.Property.Array({
      displayName: "Closing Tags",
      description: "Comma-separated tags to be added to keys when task is closed",
      required: !1
    }),
    doLockTranslations: import_pieces_framework6.Property.Checkbox({
      displayName: "Lock Translations",
      description: "If set to true, will lock translations for non-assigned project members",
      required: !1,
      defaultValue: !1
    }),
    markVerified: import_pieces_framework6.Property.Checkbox({
      displayName: "Mark Verified",
      description: "Mark translations as verified. Only for automatic_translation tasks. Default is true",
      required: !1,
      defaultValue: !0
    }),
    saveAiTranslationToTm: import_pieces_framework6.Property.Checkbox({
      displayName: "Save AI Translation to TM",
      description: "Save AI translations to Translation Memory. Only for automatic_translation tasks",
      required: !1,
      defaultValue: !1
    }),
    applyAiTm100Matches: import_pieces_framework6.Property.Checkbox({
      displayName: "Apply AI TM 100% Matches",
      description: "Apply 100% translation memory matches. Only for automatic_translation tasks",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let {
      projectId,
      taskTitle,
      description,
      taskType,
      keys,
      languages,
      sourceLanguageIso,
      dueDate,
      autoCloseLanguages,
      autoCloseTask,
      closingTags,
      doLockTranslations,
      markVerified,
      saveAiTranslationToTm,
      applyAiTm100Matches
    } = context.propsValue, body = __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({
      title: taskTitle
    }, description && { description }), taskType && { task_type: taskType }), keys && { keys }), languages && { languages }), sourceLanguageIso && { source_language_iso: sourceLanguageIso }), dueDate && { due_date: dueDate }), autoCloseLanguages !== void 0 && {
      auto_close_languages: autoCloseLanguages
    }), autoCloseTask !== void 0 && {
      auto_close_task: autoCloseTask
    }), closingTags && { closing_tags: closingTags }), doLockTranslations && { do_lock_translations: !0 }), markVerified !== void 0 && { mark_verified: markVerified }), saveAiTranslationToTm && {
      save_ai_translation_to_tm: !0
    }), applyAiTm100Matches && {
      apply_ai_tm100_matches: !0
    }), path = `/projects/${projectId}/tasks`;
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common6.HttpMethod.POST,
      path,
      body
    );
  }
});

// ../ap/packages/pieces/community/lokalise/src/lib/actions/retrieve-translation.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var retrieveTranslation = (0, import_pieces_framework7.createAction)({
  auth: lokaliseAuth,
  name: "retrieveTranslation",
  displayName: "Retrieve Translation",
  description: "Retrieve a specific translation from your Lokalise project",
  audience: "both",
  aiMetadata: { description: "Fetch a single translation by its translation ID from a Lokalise project. Use to read the current value, status, and metadata of one translation. Read-only and idempotent.", idempotent: !0 },
  props: {
    projectId: projectDropdown,
    translationId: import_pieces_framework7.Property.ShortText({
      displayName: "Translation ID",
      description: "Unique translation identifier",
      required: !0
    }),
    disableReferences: import_pieces_framework7.Property.Checkbox({
      displayName: "Disable References",
      description: "Disable key references in the translation",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { projectId, translationId, disableReferences } = context.propsValue, queryParams = new URLSearchParams();
    disableReferences && queryParams.append("disable_references", "1");
    let path = `/projects/${projectId}/translations/${translationId}${queryParams.toString() ? `?${queryParams.toString()}` : ""}`;
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common7.HttpMethod.GET,
      path
    );
  }
});

// ../ap/packages/pieces/community/lokalise/src/lib/actions/update-key.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var updateKey = (0, import_pieces_framework8.createAction)({
  auth: lokaliseAuth,
  name: "updateKey",
  displayName: "Update Key",
  description: "Update properties of a key in your Lokalise project",
  audience: "both",
  aiMetadata: { description: "Update properties of an existing translation key (name, description, platforms, tags, plural/hidden/archived flags, context, character limit) identified by project ID and key ID. Idempotent: re-sending the same field values leaves the key in the same state, though enabling Merge Tags appends tags rather than replacing them.", idempotent: !0 },
  props: {
    projectId: projectDropdown,
    keyId: keyIdProp,
    keyName: import_pieces_framework8.Property.ShortText({
      displayName: "Key Name",
      description: "Key identifier (or JSON with ios, android, web, other for per-platform names)",
      required: !1
    }),
    description: import_pieces_framework8.Property.LongText({
      displayName: "Description",
      description: "Description of the key",
      required: !1
    }),
    platforms: import_pieces_framework8.Property.StaticMultiSelectDropdown({
      displayName: "Platforms",
      description: "List of platforms enabled for this key",
      required: !1,
      options: {
        options: [
          { label: "iOS", value: "ios" },
          { label: "Android", value: "android" },
          { label: "Web", value: "web" },
          { label: "Other", value: "other" }
        ]
      }
    }),
    tags: import_pieces_framework8.Property.Array({
      displayName: "Tags",
      description: "Comma-separated tags for the key",
      required: !1
    }),
    mergeTags: import_pieces_framework8.Property.Checkbox({
      displayName: "Merge Tags",
      description: "Enable to merge specified tags with current tags",
      required: !1,
      defaultValue: !1
    }),
    isPlural: import_pieces_framework8.Property.Checkbox({
      displayName: "Is Plural",
      description: "Whether this key is plural",
      required: !1,
      defaultValue: !1
    }),
    pluralName: import_pieces_framework8.Property.ShortText({
      displayName: "Plural Name",
      description: "Optional custom plural name",
      required: !1
    }),
    isHidden: import_pieces_framework8.Property.Checkbox({
      displayName: "Is Hidden",
      description: "Whether this key is hidden from non-admins",
      required: !1,
      defaultValue: !1
    }),
    isArchived: import_pieces_framework8.Property.Checkbox({
      displayName: "Is Archived",
      description: "Whether this key is archived",
      required: !1,
      defaultValue: !1
    }),
    context: import_pieces_framework8.Property.ShortText({
      displayName: "Context",
      description: "Optional context of the key (used with some file formats)",
      required: !1
    }),
    charLimit: import_pieces_framework8.Property.ShortText({
      displayName: "Character Limit",
      description: "Maximum allowed number of characters in translations",
      required: !1
    })
  },
  async run(context) {
    let {
      projectId,
      keyId,
      keyName,
      description,
      platforms,
      tags,
      mergeTags,
      isPlural,
      pluralName,
      isHidden,
      isArchived,
      context: keyContext,
      charLimit
    } = context.propsValue, body = __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({}, keyName && { key_name: keyName }), description && { description }), platforms && platforms.length > 0 && { platforms }), tags && tags.length > 0 && { tags }), mergeTags && { merge_tags: !0 }), isPlural !== void 0 && { is_plural: isPlural }), pluralName && { plural_name: pluralName }), isHidden !== void 0 && { is_hidden: isHidden }), isArchived !== void 0 && { is_archived: isArchived }), keyContext && { context: keyContext }), charLimit && { char_limit: parseInt(charLimit, 10) }), path = `/projects/${projectId}/keys/${keyId}`;
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common8.HttpMethod.PUT,
      path,
      body
    );
  }
});

// ../ap/packages/pieces/community/lokalise/src/lib/actions/update-translation.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var updateTranslation = (0, import_pieces_framework9.createAction)({
  auth: lokaliseAuth,
  name: "updateTranslation",
  displayName: "Update Translation",
  description: "Update a translation in your Lokalise project",
  audience: "both",
  aiMetadata: { description: "Set the content and review flags of an existing translation, identified by project ID and translation ID. Use to write or correct a translation value. Idempotent: re-sending the same content and flags yields the same final state.", idempotent: !0 },
  props: {
    projectId: projectDropdown,
    translationId: import_pieces_framework9.Property.ShortText({
      displayName: "Translation ID",
      description: "Unique translation identifier",
      required: !0
    }),
    translation: import_pieces_framework9.Property.LongText({
      displayName: "Translation",
      description: "The actual translation content. Use JSON object format for plural keys",
      required: !0
    }),
    isUnverified: import_pieces_framework9.Property.Checkbox({
      displayName: "Mark as Unverified",
      description: "Whether the Unverified flag is enabled",
      required: !1,
      defaultValue: !1
    }),
    isReviewed: import_pieces_framework9.Property.Checkbox({
      displayName: "Mark as Reviewed",
      description: "Whether the Reviewed flag is enabled",
      required: !1,
      defaultValue: !1
    }),
    custom_translation_status_ids: import_pieces_framework9.Property.Array({
      displayName: "Custom Translation Status IDs",
      description: "Comma-separated custom translation status IDs to assign (existing statuses will be replaced)",
      required: !1
    })
  },
  async run(context) {
    let {
      projectId,
      translationId,
      translation,
      isUnverified,
      isReviewed,
      custom_translation_status_ids
    } = context.propsValue, body = __spreadValues(__spreadValues(__spreadValues({
      translation
    }, isUnverified !== void 0 && { is_unverified: isUnverified }), isReviewed !== void 0 && { is_reviewed: isReviewed }), custom_translation_status_ids && custom_translation_status_ids.length > 0 && {
      custom_translation_status_ids
    }), path = `/projects/${projectId}/translations/${translationId}`;
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common9.HttpMethod.PUT,
      path,
      body
    );
  }
});

// ../ap/packages/pieces/community/lokalise/src/lib/actions/delete-key.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var deleteKey = (0, import_pieces_framework10.createAction)({
  auth: lokaliseAuth,
  name: "deleteKey",
  displayName: "Delete Key",
  description: "Delete a key from your Lokalise project (software and marketing projects only)",
  audience: "both",
  aiMetadata: { description: "Permanently delete a translation key (and its translations) from a Lokalise project, identified by project ID and key ID. Only works for software and marketing project types. Idempotent in effect: once deleted, re-running has no further effect, though the key must currently exist.", idempotent: !0 },
  props: {
    projectId: projectDropdown,
    keyId: keyIdProp
  },
  async run(context) {
    let { projectId, keyId } = context.propsValue, path = `/projects/${projectId}/keys/${keyId}`;
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common10.HttpMethod.DELETE,
      path
    );
  }
});

// ../ap/packages/pieces/community/lokalise/src/lib/actions/retrieve-a-comment.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var retrieveAComment = (0, import_pieces_framework11.createAction)({
  auth: lokaliseAuth,
  name: "retrieveAComment",
  displayName: "Retrieve a comment",
  description: "Retrieve a specific comment on a key in your Lokalise project",
  audience: "both",
  aiMetadata: { description: "Fetch a single comment on a translation key, identified by project ID, key ID, and comment ID. Use to read one comment's text and metadata. Read-only and idempotent.", idempotent: !0 },
  props: {
    projectId: projectDropdown,
    keyId: keyIdProp,
    commentId: import_pieces_framework11.Property.ShortText({
      displayName: "Comment ID",
      description: "Unique identifier of the comment",
      required: !0
    })
  },
  async run(context) {
    let { projectId, keyId, commentId } = context.propsValue, path = `/projects/${projectId}/keys/${keyId}/comments/${commentId}`;
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common11.HttpMethod.GET,
      path
    );
  }
});

// ../ap/packages/pieces/community/lokalise/src/lib/actions/retrieve-a-project.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var retrieveAProject = (0, import_pieces_framework12.createAction)({
  auth: lokaliseAuth,
  name: "retrieveAProject",
  displayName: "Retrieve a project",
  description: "Retrieve detailed information about a Lokalise project",
  audience: "both",
  aiMetadata: { description: "Fetch the details and settings of a single Lokalise project by its project ID. Use to inspect project configuration or confirm a project exists. Read-only and idempotent.", idempotent: !0 },
  props: {
    projectId: projectDropdown
  },
  async run(context) {
    let { projectId } = context.propsValue, path = `/projects/${projectId}`;
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common12.HttpMethod.GET,
      path
    );
  }
});

// ../ap/packages/pieces/community/lokalise/src/lib/actions/retrieve-a-key.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common");
var retrieveAKey = (0, import_pieces_framework13.createAction)({
  auth: lokaliseAuth,
  name: "retrieveAKey",
  displayName: "Retrieve a key",
  description: "Retrieve detailed information about a specific key in your Lokalise project",
  audience: "both",
  aiMetadata: { description: "Fetch the details of a single translation key, including its translations and metadata, by project ID and key ID. Use to inspect a key before updating or referencing it. Read-only and idempotent.", idempotent: !0 },
  props: {
    projectId: projectDropdown,
    keyId: keyIdProp,
    disableReferences: import_pieces_framework13.Property.Checkbox({
      displayName: "Disable References",
      description: "Disable key references in the response",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { projectId, keyId, disableReferences } = context.propsValue, queryParams = new URLSearchParams();
    disableReferences && queryParams.append("disable_references", "1");
    let path = `/projects/${projectId}/keys/${keyId}${queryParams.toString() ? `?${queryParams.toString()}` : ""}`;
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common13.HttpMethod.GET,
      path
    );
  }
});

// ../ap/packages/pieces/community/lokalise/src/lib/triggers/key-added.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_common14 = require("@activepieces/pieces-common");
var polling = {
  strategy: import_pieces_common14.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => {
    let keys = await makeRequest(
      auth.secret_text,
      import_pieces_common14.HttpMethod.GET,
      `/projects/${propsValue.projectId}/keys`
    );
    return console.log(keys), keys.keys.filter(
      (key) => key.created_at_timestamp * 1e3 > lastFetchEpochMS
    ).map((key) => ({
      epochMilliSeconds: key.created_at_timestamp * 1e3,
      data: key
    }));
  }
}, keyAdded = (0, import_pieces_framework14.createTrigger)({
  auth: lokaliseAuth,
  name: "keyAdded",
  displayName: "Key Added",
  description: "Trigger when a new key is added to your Lokalise project",
  aiMetadata: {
    description: "Fires when a new translation key is created in the selected Lokalise project, emitting that key. Polls the project keys list and detects keys by creation timestamp."
  },
  props: {
    projectId: projectDropdown
  },
  sampleData: {
    event: "project.key.added",
    key: {
      id: 783570856,
      name: "index.welcome",
      base_value: null,
      filenames: {
        ios: null,
        android: null,
        web: null,
        other: null
      },
      tags: []
    },
    project: {
      id: "aasasasasas",
      name: "test"
    },
    user: {
      full_name: "fadse",
      email: "sasdf@gmail.com"
    },
    created_at: "2026-01-09 07:38:20",
    created_at_timestamp: 1767940700
  },
  type: import_pieces_framework14.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common14.pollingHelper.test(polling, context);
  },
  async onEnable(context) {
    await import_pieces_common14.pollingHelper.onEnable(polling, context);
  },
  async onDisable(context) {
    await import_pieces_common14.pollingHelper.onDisable(polling, context);
  },
  async run(context) {
    return await import_pieces_common14.pollingHelper.poll(polling, context);
  }
});

// ../ap/packages/pieces/community/lokalise/src/lib/triggers/key-updated.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var import_pieces_common15 = require("@activepieces/pieces-common");
var polling2 = {
  strategy: import_pieces_common15.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => (await makeRequest(
    auth.secret_text,
    import_pieces_common15.HttpMethod.GET,
    `/projects/${propsValue.projectId}/keys`
  )).keys.filter(
    (key) => key.modified_at_timestamp * 1e3 > lastFetchEpochMS
  ).map((key) => ({
    epochMilliSeconds: key.modified_at_timestamp * 1e3,
    data: key
  }))
}, keyUpdated = (0, import_pieces_framework15.createTrigger)({
  auth: lokaliseAuth,
  name: "keyUpdated",
  displayName: "Key Updated",
  description: "Trigger when a key is updated in your Lokalise project",
  aiMetadata: {
    description: "Fires when an existing translation key is modified in the selected Lokalise project, emitting that key. Polls the project keys list and detects changes by modification timestamp."
  },
  props: { projectId: projectDropdown },
  sampleData: {
    event: "project.key.modified",
    key: {
      id: 782130622,
      name: "test update key",
      previous_name: "welcome_header",
      filenames: { ios: null, android: null, web: null, other: null },
      tags: [],
      hidden: !1,
      screenshots: []
    },
    project: { id: "30473913695e05bacfe965.32690341", name: "test" },
    user: {
      full_name: "jon ",
      email: "jon@example.com"
    },
    created_at: "2026-01-09 07:43:32",
    created_at_timestamp: 1767941012
  },
  type: import_pieces_framework15.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common15.pollingHelper.test(polling2, context);
  },
  async onEnable(context) {
    await import_pieces_common15.pollingHelper.onEnable(polling2, context);
  },
  async onDisable(context) {
    await import_pieces_common15.pollingHelper.onDisable(polling2, context);
  },
  async run(context) {
    return await import_pieces_common15.pollingHelper.poll(polling2, context);
  }
});

// ../ap/packages/pieces/community/lokalise/src/lib/triggers/translation-updated.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_common16 = require("@activepieces/pieces-common");
var polling3 = {
  strategy: import_pieces_common16.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => (await makeRequest(
    auth.secret_text,
    import_pieces_common16.HttpMethod.GET,
    `/projects/${propsValue.projectId}/translations`
  )).translations.filter(
    (translation) => translation.modified_at_timestamp * 1e3 > lastFetchEpochMS
  ).map((translation) => ({
    epochMilliSeconds: translation.modified_at_timestamp * 1e3,
    data: translation
  }))
}, translationUpdated = (0, import_pieces_framework16.createTrigger)({
  auth: lokaliseAuth,
  name: "translationUpdated",
  displayName: "Translation Updated",
  description: "Trigger when a translation is updated in your Lokalise project",
  aiMetadata: {
    description: "Fires when a translation is modified in the selected Lokalise project, emitting that translation. Polls the project translations list and detects changes by modification timestamp."
  },
  props: {
    projectId: projectDropdown
  },
  sampleData: {},
  type: import_pieces_framework16.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common16.pollingHelper.test(polling3, context);
  },
  async onEnable(context) {
    await import_pieces_common16.pollingHelper.onEnable(polling3, context);
  },
  async onDisable(context) {
    await import_pieces_common16.pollingHelper.onDisable(polling3, context);
  },
  async run(context) {
    return await import_pieces_common16.pollingHelper.poll(polling3, context);
  }
});

// ../ap/packages/pieces/community/lokalise/src/index.ts
var import_pieces_common17 = require("@activepieces/pieces-common"), lokalise = (0, import_pieces_framework17.createPiece)({
  displayName: "Lokalise",
  auth: lokaliseAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/lokalise.png",
  authors: ["sanket-a11y"],
  categories: [import_pieces_framework18.PieceCategory.DEVELOPER_TOOLS],
  description: "Lokalise is a collaborative translation platform.",
  actions: [
    createComment,
    createKey,
    createProject,
    createTask,
    deleteKey,
    retrieveAComment,
    retrieveAKey,
    retrieveAProject,
    retrieveTranslation,
    updateKey,
    updateTranslation,
    (0, import_pieces_common17.createCustomApiCallAction)({
      auth: lokaliseAuth,
      baseUrl: () => "https://api.lokalise.com/api2",
      authMapping: async (auth) => ({
        "X-Api-Token": auth.secret_text
      })
    })
  ],
  triggers: [keyAdded, keyUpdated, translationUpdated]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  lokalise
});
