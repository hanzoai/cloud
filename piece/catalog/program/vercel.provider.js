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

// ../ap/packages/pieces/community/vercel/src/index.ts
var index_exports = {};
__export(index_exports, {
  vercel: () => vercel
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/vercel/src/lib/common/auth.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework"), vercelAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: "Authenticate with a Vercel personal access token. Optionally provide a team ID or team slug to operate on team-owned resources.",
  required: !0,
  props: {
    token: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Vercel Access Token",
      description: "Create a token in Vercel Settings \u2192 Tokens.",
      required: !0
    }),
    teamId: import_pieces_framework.Property.ShortText({
      displayName: "Team ID",
      description: "Optional. Use this to target a specific Vercel team.",
      required: !1
    }),
    slug: import_pieces_framework.Property.ShortText({
      displayName: "Team Slug",
      description: "Optional. Team slug. Ignored when Team ID is provided.",
      required: !1
    })
  },
  validate: async ({ auth }) => {
    try {
      let queryParams = {
        limit: "1"
      };
      return auth.teamId ? queryParams.teamId = auth.teamId : auth.slug && (queryParams.slug = auth.slug), await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: "https://api.vercel.com/v10/projects",
        authentication: {
          type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
          token: auth.token
        },
        queryParams
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: e.message
      };
    }
  }
});

// ../ap/packages/pieces/community/vercel/src/lib/actions/list-projects.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/vercel/src/lib/common/client.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), BASE_URL = "https://api.vercel.com";
async function vercelApiCall({
  method,
  path,
  auth,
  query,
  body
}) {
  let queryParams = {};
  if (query)
    for (let [key, value] of Object.entries(query))
      value != null && value !== "" && (queryParams[key] = String(value));
  auth.props.teamId ? queryParams.teamId = auth.props.teamId : auth.props.slug && (queryParams.slug = auth.props.slug);
  let request = {
    method,
    url: `${BASE_URL}${path}`,
    authentication: {
      type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
      token: auth.props.token
    },
    headers: {
      "Content-Type": "application/json"
    },
    queryParams: Object.keys(queryParams).length > 0 ? queryParams : void 0,
    body
  };
  return (await import_pieces_common2.httpClient.sendRequest(request)).body;
}
async function listAllProjects(auth, search) {
  var _a, _b;
  let projects = [], nextFrom, pageCount = 0;
  for (; pageCount < 10; ) {
    pageCount += 1;
    let response = await vercelApiCall({
      method: import_pieces_common2.HttpMethod.GET,
      path: "/v10/projects",
      auth,
      query: {
        limit: 100,
        from: nextFrom,
        search
      }
    }), batch = Array.isArray(response) ? response : (_a = response.projects) != null ? _a : [];
    projects.push(...batch);
    let next = Array.isArray(response) || (_b = response.pagination) == null ? void 0 : _b.next;
    if (!next)
      break;
    nextFrom = String(next);
  }
  return projects;
}
function toProjectDropdownOptions(projects) {
  return projects.map((project) => ({
    label: project.name,
    value: project.id
  }));
}
async function listDeployments(auth, projectId) {
  var _a;
  return (_a = (await vercelApiCall({
    method: import_pieces_common2.HttpMethod.GET,
    path: "/v6/deployments",
    auth,
    query: {
      projectId,
      limit: 100
    }
  })).deployments) != null ? _a : [];
}

// ../ap/packages/pieces/community/vercel/src/lib/actions/list-projects.ts
var listProjects = (0, import_pieces_framework2.createAction)({
  auth: vercelAuth,
  name: "list_projects",
  displayName: "List Projects",
  description: "Retrieve all Vercel projects for the authenticated user or team.",
  audience: "both",
  aiMetadata: {
    description: "List Vercel projects for the authenticated account or team, paging through all results. Leave the search value empty to fetch every project, or set it to filter projects by name. Use this to discover project IDs/names before deploying or managing env vars. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    search: import_pieces_framework2.Property.ShortText({
      displayName: "Search",
      description: "Filter projects by name.",
      required: !1
    })
  },
  async run(context) {
    let { search } = context.propsValue;
    return await listAllProjects(context.auth, search);
  }
});

// ../ap/packages/pieces/community/vercel/src/lib/actions/create-deployment.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/vercel/src/lib/common/props.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var vercelProjectDropdown = import_pieces_framework3.Property.Dropdown({
  auth: vercelAuth,
  displayName: "Project",
  description: "Select a Vercel project.",
  required: !0,
  refreshers: ["auth"],
  refreshOnSearch: !0,
  options: async ({ auth }, { searchValue }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Vercel account first",
        options: []
      };
    let projects = await listAllProjects(auth, searchValue);
    return {
      disabled: !1,
      options: toProjectDropdownOptions(projects)
    };
  }
}), deploymentTargetProperty = import_pieces_framework3.Property.StaticDropdown({
  displayName: "Target",
  description: "Vercel deployment target environment.",
  required: !1,
  defaultValue: "preview",
  options: {
    options: [
      { label: "Preview", value: "preview" },
      { label: "Production", value: "production" }
    ]
  }
}), envTargetsProperty = import_pieces_framework3.Property.StaticMultiSelectDropdown({
  displayName: "Target Environments",
  description: "Environment(s) where this variable should apply.",
  required: !0,
  options: {
    options: [
      { label: "Production", value: "production" },
      { label: "Preview", value: "preview" },
      { label: "Development", value: "development" }
    ]
  },
  defaultValue: ["production", "preview", "development"]
});

// ../ap/packages/pieces/community/vercel/src/lib/actions/create-deployment.ts
var createDeployment = (0, import_pieces_framework4.createAction)({
  auth: vercelAuth,
  name: "create_deployment",
  displayName: "Create Deployment",
  description: "Create a deployment for an existing Vercel project using either a redeploy source deployment ID or a git source payload.",
  audience: "both",
  aiMetadata: {
    description: "Trigger a new deployment for an existing Vercel project. Operates in one of two modes: redeploy an existing deployment by its ID (optionally using the latest commit), or deploy from a connected Git source (GitHub, GitLab, or Bitbucket) by branch/ref plus repo identifiers. Requires a target project; for Git mode a branch/ref and either a repo ID or org+repo name are mandatory. Not idempotent: each call starts a separate deployment.",
    idempotent: !1
  },
  props: {
    project: vercelProjectDropdown,
    deployment_source: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Deployment Source",
      description: "Choose whether to redeploy a previous deployment or deploy from a connected Git source.",
      required: !0,
      defaultValue: "redeploy",
      options: {
        options: [
          { label: "Redeploy Existing Deployment", value: "redeploy" },
          { label: "Git Source", value: "git_source" }
        ]
      }
    }),
    source_fields: import_pieces_framework4.Property.DynamicProperties({
      displayName: "Source Configuration",
      required: !1,
      auth: vercelAuth,
      refreshers: ["deployment_source", "project"],
      props: async ({ auth, deployment_source, project }) => {
        let fields = {};
        return deployment_source === "redeploy" ? (fields.deployment_id = import_pieces_framework4.Property.Dropdown({
          displayName: "Deployment",
          description: "Select the deployment to redeploy.",
          required: !0,
          auth: vercelAuth,
          refreshers: ["auth", "project"],
          options: async () => !auth || !project ? { disabled: !0, placeholder: "Select a project first", options: [] } : {
            disabled: !1,
            options: (await listDeployments(auth, String(project))).map((d) => {
              var _a;
              return {
                label: d.url ? `${d.url} (${(_a = d.state) != null ? _a : "unknown"}${d.target ? ` \xB7 ${d.target}` : ""})` : d.name,
                value: d.uid
              };
            })
          }
        }), fields.with_latest_commit = import_pieces_framework4.Property.Checkbox({
          displayName: "Use Latest Commit for Redeploy",
          description: "When enabled, redeploys using the latest commit instead of the original deployment files.",
          required: !1,
          defaultValue: !1
        }), fields) : (fields.target = deploymentTargetProperty, fields.git_type = import_pieces_framework4.Property.StaticDropdown({
          displayName: "Git Provider",
          description: "Git provider used for the source deployment.",
          required: !0,
          defaultValue: "github",
          options: {
            options: [
              { label: "GitHub", value: "github" },
              { label: "GitHub (Limited)", value: "github-limited" },
              { label: "GitLab", value: "gitlab" },
              { label: "Bitbucket", value: "bitbucket" }
            ]
          }
        }), fields.git_repo_org = import_pieces_framework4.Property.ShortText({
          displayName: "Repository Organization / Workspace",
          description: "For GitHub use the org/owner. For Bitbucket slug mode, use the owner.",
          required: !1
        }), fields.git_repo_name = import_pieces_framework4.Property.ShortText({
          displayName: "Repository Name / Slug",
          description: "Repository name for GitHub or slug for Bitbucket slug mode.",
          required: !1
        }), fields.git_branch = import_pieces_framework4.Property.ShortText({
          displayName: "Git Branch / Ref",
          description: "Branch or ref to deploy.",
          required: !0
        }), fields.git_sha = import_pieces_framework4.Property.ShortText({
          displayName: "Git Commit SHA",
          description: "Optional commit SHA.",
          required: !1
        }), fields.git_repo_id = import_pieces_framework4.Property.ShortText({
          displayName: "Git Repository ID / Project ID",
          description: "Use this for GitHub repoId or GitLab projectId mode.",
          required: !1
        }), fields.git_repo_uuid = import_pieces_framework4.Property.ShortText({
          displayName: "Bitbucket Repository UUID",
          description: "Use this for Bitbucket UUID mode.",
          required: !1
        }), fields.git_workspace_uuid = import_pieces_framework4.Property.ShortText({
          displayName: "Bitbucket Workspace UUID",
          description: "Optional Bitbucket workspace UUID.",
          required: !1
        }), fields);
      }
    }),
    force_new: import_pieces_framework4.Property.Checkbox({
      displayName: "Force New Deployment",
      description: "Create a new deployment even if a previous similar deployment exists.",
      required: !1,
      defaultValue: !1
    }),
    skip_auto_detection_confirmation: import_pieces_framework4.Property.Checkbox({
      displayName: "Skip Auto Detection Confirmation",
      description: "Automatically confirm framework detection without prompting.",
      required: !1,
      defaultValue: !0
    })
  },
  async run(context) {
    let {
      project,
      deployment_source,
      source_fields,
      force_new,
      skip_auto_detection_confirmation
    } = context.propsValue, deployment_id = source_fields == null ? void 0 : source_fields.deployment_id, with_latest_commit = source_fields == null ? void 0 : source_fields.with_latest_commit, target = source_fields == null ? void 0 : source_fields.target, git_type = source_fields == null ? void 0 : source_fields.git_type, git_repo_org = source_fields == null ? void 0 : source_fields.git_repo_org, git_repo_name = source_fields == null ? void 0 : source_fields.git_repo_name, git_branch = source_fields == null ? void 0 : source_fields.git_branch, git_sha = source_fields == null ? void 0 : source_fields.git_sha, git_repo_id = source_fields == null ? void 0 : source_fields.git_repo_id, git_repo_uuid = source_fields == null ? void 0 : source_fields.git_repo_uuid, git_workspace_uuid = source_fields == null ? void 0 : source_fields.git_workspace_uuid, projectId = String(project), body = {
      name: projectId,
      project: projectId
    };
    if (deployment_source === "redeploy") {
      if (!deployment_id)
        throw new Error("Please select a deployment to redeploy.");
      body.deploymentId = deployment_id, with_latest_commit && (body.withLatestCommit = !0);
    } else {
      if (!git_branch)
        throw new Error("Git Branch / Ref is required when Deployment Source is Git Source.");
      if (body.target = target != null ? target : "preview", git_type === "github" || git_type === "github-limited")
        if (git_repo_id)
          body.gitSource = __spreadValues({
            type: git_type,
            ref: git_branch,
            repoId: git_repo_id
          }, git_sha ? { sha: git_sha } : {});
        else if (git_repo_org && git_repo_name)
          body.gitSource = __spreadValues({
            type: git_type,
            ref: git_branch,
            org: git_repo_org,
            repo: git_repo_name
          }, git_sha ? { sha: git_sha } : {});
        else
          throw new Error("For GitHub deployments, provide either Repository ID or both Repository Organization and Repository Name.");
      else if (git_type === "gitlab") {
        if (!git_repo_id)
          throw new Error("Git Repository ID / Project ID is required for GitLab deployments.");
        body.gitSource = __spreadValues({
          type: "gitlab",
          ref: git_branch,
          projectId: git_repo_id
        }, git_sha ? { sha: git_sha } : {});
      } else if (git_type === "bitbucket")
        if (git_repo_uuid)
          body.gitSource = __spreadValues(__spreadValues({
            type: "bitbucket",
            ref: git_branch,
            repoUuid: git_repo_uuid
          }, git_workspace_uuid ? { workspaceUuid: git_workspace_uuid } : {}), git_sha ? { sha: git_sha } : {});
        else if (git_repo_org && git_repo_name)
          body.gitSource = __spreadValues({
            type: "bitbucket",
            ref: git_branch,
            owner: git_repo_org,
            slug: git_repo_name
          }, git_sha ? { sha: git_sha } : {});
        else
          throw new Error("For Bitbucket deployments, provide either Repository UUID or both Organization / Workspace and Repository Name / Slug.");
      else
        throw new Error(`Unsupported Git Provider: ${String(git_type)}. Please select a valid provider.`);
    }
    return await vercelApiCall({
      method: import_pieces_common3.HttpMethod.POST,
      path: "/v13/deployments",
      auth: context.auth,
      query: {
        forceNew: force_new ? 1 : void 0,
        skipAutoDetectionConfirmation: skip_auto_detection_confirmation ? 1 : void 0
      },
      body
    });
  }
});

// ../ap/packages/pieces/community/vercel/src/lib/actions/get-deployment-status.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var getDeploymentStatus = (0, import_pieces_framework5.createAction)({
  auth: vercelAuth,
  name: "get_deployment_status",
  displayName: "Get Deployment Status",
  description: "Retrieve a deployment and inspect its current status.",
  audience: "both",
  aiMetadata: {
    description: "Fetch a single Vercel deployment by ID and inspect its current state (e.g. building, ready, error), optionally including Git repository info. Use this to poll or check the outcome of a deployment. Requires the deployment ID, scoped to a chosen project. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    project: vercelProjectDropdown,
    deployment: import_pieces_framework5.Property.Dropdown({
      displayName: "Deployment",
      description: "Select the deployment to inspect.",
      required: !0,
      auth: vercelAuth,
      refreshers: ["auth", "project"],
      options: async ({ auth, project }) => !auth || !project ? { disabled: !0, placeholder: "Select a project first", options: [] } : {
        disabled: !1,
        options: (await listDeployments(auth, String(project))).map((d) => {
          var _a;
          return {
            label: d.url ? `${d.url} (${(_a = d.state) != null ? _a : "unknown"}${d.target ? ` \xB7 ${d.target}` : ""})` : d.name,
            value: d.uid
          };
        })
      }
    }),
    with_git_repo_info: import_pieces_framework5.Property.Checkbox({
      displayName: "Include Git Repository Info",
      description: "Adds gitRepo information when available.",
      required: !1,
      defaultValue: !0
    })
  },
  async run(context) {
    let { deployment, with_git_repo_info } = context.propsValue;
    return await vercelApiCall({
      method: import_pieces_common4.HttpMethod.GET,
      path: `/v13/deployments/${encodeURIComponent(String(deployment))}`,
      auth: context.auth,
      query: {
        withGitRepoInfo: with_git_repo_info ? "true" : void 0
      }
    });
  }
});

// ../ap/packages/pieces/community/vercel/src/lib/actions/list-environment-variables.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var listEnvironmentVariables = (0, import_pieces_framework6.createAction)({
  auth: vercelAuth,
  name: "list_environment_variables",
  displayName: "List Environment Variables",
  description: "Retrieve environment variables for a Vercel project.",
  audience: "both",
  aiMetadata: {
    description: "List the environment variables configured on a Vercel project, optionally filtered to a specific Git branch (preview-scoped vars only) and optionally requesting decrypted values where permitted. Use this to discover existing keys before reading or upserting a variable. Requires the target project. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    project: vercelProjectDropdown,
    decrypt: import_pieces_framework6.Property.Checkbox({
      displayName: "Decrypt Values",
      description: "Deprecated by Vercel. If enabled, Vercel will attempt to return decrypted values when allowed.",
      required: !1,
      defaultValue: !1
    }),
    git_branch: import_pieces_framework6.Property.ShortText({
      displayName: "Git Branch",
      description: "Optional branch filter. Only valid for preview-scoped variables.",
      required: !1
    })
  },
  async run(context) {
    let { project, decrypt, git_branch } = context.propsValue;
    return await vercelApiCall({
      method: import_pieces_common5.HttpMethod.GET,
      path: `/v10/projects/${encodeURIComponent(String(project))}/env`,
      auth: context.auth,
      query: {
        decrypt: decrypt ? "true" : void 0,
        gitBranch: git_branch
      }
    });
  }
});

// ../ap/packages/pieces/community/vercel/src/lib/actions/upsert-environment-variable.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var upsertEnvironmentVariable = (0, import_pieces_framework7.createAction)({
  auth: vercelAuth,
  name: "upsert_environment_variable",
  displayName: "Upsert Environment Variable",
  description: "Create or update a Vercel environment variable using Vercel's upsert API.",
  audience: "both",
  aiMetadata: {
    description: "Create or update an environment variable on a Vercel project via Vercel's upsert endpoint, keyed by the variable name. Choose the variable type (plain, sensitive, or encrypted) and one or more target environments (production, preview, development); a Git branch may only be set when preview is among the targets. Use when you need a specific key set to a known value. Idempotent: repeating with the same key and value yields the same stored variable.",
    idempotent: !0
  },
  props: {
    project: vercelProjectDropdown,
    key: import_pieces_framework7.Property.ShortText({
      displayName: "Key",
      description: "Environment variable key.",
      required: !0
    }),
    value: import_pieces_framework7.Property.LongText({
      displayName: "Value",
      description: "Environment variable value.",
      required: !0
    }),
    type: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Variable Type",
      description: "Vercel environment variable type.",
      required: !0,
      defaultValue: "plain",
      options: {
        options: [
          { label: "Plain", value: "plain" },
          { label: "Sensitive", value: "sensitive" },
          { label: "Encrypted", value: "encrypted" }
        ]
      }
    }),
    target: envTargetsProperty,
    git_branch: import_pieces_framework7.Property.ShortText({
      displayName: "Git Branch",
      description: "Optional branch. Only valid when Preview is one of the selected targets.",
      required: !1
    }),
    comment: import_pieces_framework7.Property.ShortText({
      displayName: "Comment",
      description: "Optional note describing the variable usage.",
      required: !1
    })
  },
  async run(context) {
    let { project, key, value, type, target, git_branch, comment } = context.propsValue, targetValues = Array.isArray(target) ? target.map((entry) => String(entry)) : [String(target)];
    if (git_branch && !targetValues.includes("preview"))
      throw new Error("Git Branch can only be used when Preview is included in Target Environments.");
    return await vercelApiCall({
      method: import_pieces_common6.HttpMethod.POST,
      path: `/v10/projects/${encodeURIComponent(String(project))}/env`,
      auth: context.auth,
      query: {
        upsert: "true"
      },
      body: __spreadValues(__spreadValues({
        key,
        value,
        type,
        target: targetValues
      }, git_branch ? { gitBranch: git_branch } : {}), comment ? { comment } : {})
    });
  }
});

// ../ap/packages/pieces/community/vercel/src/index.ts
var vercel = (0, import_pieces_framework8.createPiece)({
  displayName: "Vercel",
  auth: vercelAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/vercel.png",
  description: "Deploy projects and manage environment variables on Vercel.",
  categories: [import_pieces_framework9.PieceCategory.DEVELOPER_TOOLS],
  authors: ["atlas-hunter"],
  actions: [
    listProjects,
    createDeployment,
    getDeploymentStatus,
    listEnvironmentVariables,
    upsertEnvironmentVariable,
    (0, import_pieces_common7.createCustomApiCallAction)({
      auth: vercelAuth,
      baseUrl: () => "https://api.vercel.com",
      description: "Make a custom API call to Vercel. The Authorization header is injected automatically. If your connection uses Team ID or Team Slug, add `teamId` or `slug` manually in the URL or Query Parameters for team-scoped requests.",
      props: {
        queryParams: {
          description: "Optional query parameters. For team-scoped requests, manually include `teamId` or `slug` here when needed."
        }
      },
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.props.token}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  vercel
});
