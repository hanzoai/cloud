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

// ../ap/packages/pieces/community/gitea/src/index.ts
var index_exports = {};
__export(index_exports, {
  gitea: () => gitea
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common10 = require("@activepieces/pieces-common"), import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_framework11 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/gitea/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), giteaAuth = import_pieces_framework.PieceAuth.OAuth2({
  description: "To connect Gitea, create an OAuth2 application in your Gitea instance: **Settings \u2192 Applications \u2192 Manage OAuth2 Applications \u2192 Create Application**. Set the redirect URI to the one provided by Activepieces, then copy the Client ID and Client Secret here.",
  props: {
    baseUrl: import_pieces_framework.Property.ShortText({
      displayName: "Base URL",
      description: "Enter your Gitea instance URL (e.g., https://gitea.com)",
      required: !0,
      defaultValue: "https://gitea.com"
    })
  },
  authUrl: "{baseUrl}/login/oauth/authorize",
  tokenUrl: "{baseUrl}/login/oauth/access_token",
  required: !0,
  scope: ["read:repository", "write:repository", "read:issue", "write:issue"]
});

// ../ap/packages/pieces/community/gitea/src/lib/triggers/new-commit.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/gitea/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/gitea/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
async function giteaApiCall({
  auth,
  method,
  resourceUri,
  query,
  body
}) {
  let request = {
    method,
    url: baseUrl(auth) + resourceUri,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token: auth.access_token
    },
    queryParams: query,
    body
  };
  return await import_pieces_common.httpClient.sendRequest(request);
}
async function giteaPaginatedApiCall({
  auth,
  method,
  resourceUri,
  query,
  body
}) {
  var _a;
  let qs = query ? __spreadValues({}, query) : {};
  qs.page = "1", qs.per_page = "100";
  let resultData = [], hasMoreItems = !0;
  for (; hasMoreItems; ) {
    let data = (_a = (await giteaApiCall({
      auth,
      method,
      resourceUri,
      query: qs,
      body
    })).body) != null ? _a : [];
    resultData.push(...data), data.length < 100 ? hasMoreItems = !1 : qs.page = (parseInt(qs.page) + 1).toString();
  }
  return resultData;
}
function baseUrl(auth) {
  var _a, _b;
  return ((_b = (_a = auth.props) == null ? void 0 : _a.baseUrl) != null ? _b : "https://gitea.com").replace(/\/$/, "") + "/api/v1";
}

// ../ap/packages/pieces/community/gitea/src/lib/common/props.ts
var giteaCommon = {
  repositoryDropdown: import_pieces_framework2.Property.Dropdown({
    displayName: "Repository",
    refreshers: [],
    auth: giteaAuth,
    required: !0,
    options: async ({ auth }) => auth ? {
      disabled: !1,
      options: (await giteaPaginatedApiCall({
        auth,
        method: import_pieces_common2.HttpMethod.GET,
        resourceUri: "/user/repos"
      })).map((repo) => ({
        label: repo.full_name,
        value: {
          owner: repo.owner.login,
          repo: repo.name
        }
      }))
    } : {
      disabled: !0,
      options: [],
      placeholder: "Please authenticate first"
    }
  }),
  branchDropdown: import_pieces_framework2.Property.Dropdown({
    displayName: "Branch",
    description: "Filter by branch",
    required: !1,
    refreshers: ["repository"],
    auth: giteaAuth,
    options: async ({ auth, repository }) => {
      if (!auth || !repository)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please select a repository first"
        };
      let { owner, repo } = repository;
      return {
        disabled: !1,
        options: (await giteaPaginatedApiCall({
          auth,
          method: import_pieces_common2.HttpMethod.GET,
          resourceUri: `/repos/${owner}/${repo}/branches`
        })).map((branch) => ({
          label: branch.name,
          value: branch.name
        }))
      };
    }
  })
};

// ../ap/packages/pieces/community/gitea/src/lib/triggers/new-commit.ts
var newCommit = (0, import_pieces_framework3.createTrigger)({
  auth: giteaAuth,
  name: "new_commit",
  displayName: "New Commit",
  description: "Triggers when a new commit is pushed to a repository.",
  aiMetadata: {
    description: "Fires when commits are pushed to a Gitea repository (a push event), optionally filtered to a single branch. Each event represents a push and carries the commits it delivered."
  },
  props: {
    repository: giteaCommon.repositoryDropdown,
    branch: giteaCommon.branchDropdown
  },
  sampleData: {
    ref: "refs/heads/main",
    before: "5a4d...3c2f",
    after: "1b2c...9d8e",
    compare_url: "https://gitea.com/owner/repo/compare/5a4d...1b2c",
    commits: [
      {
        id: "1b2c...9d8e",
        message: "Add new feature",
        url: "https://gitea.com/owner/repo/commit/1b2c...9d8e",
        author: {
          name: "Author Name",
          email: "author@test.com",
          username: "author"
        },
        committer: {
          name: "Committer Name",
          email: "committer@test.com",
          username: "committer"
        },
        timestamp: "2023-01-01T00:00:00Z"
      }
    ],
    repository: {
      name: "repo",
      full_name: "owner/repo",
      owner: {
        login: "owner"
      }
    },
    pusher: {
      login: "pusher"
    },
    sender: {
      login: "sender"
    }
  },
  type: import_pieces_framework3.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let { repo, owner } = context.propsValue.repository, response = await giteaApiCall({
      auth: context.auth,
      method: import_pieces_common3.HttpMethod.POST,
      resourceUri: `/repos/${owner}/${repo}/hooks`,
      body: {
        type: "gitea",
        active: !0,
        events: ["push"],
        config: {
          url: context.webhookUrl,
          content_type: "json"
        }
      }
    });
    await context.store.put("gitea_new_commit_trigger", {
      webhookId: response.body.id,
      repo,
      owner
    });
  },
  async onDisable(context) {
    let webhook = await context.store.get(
      "gitea_new_commit_trigger"
    );
    webhook && await giteaApiCall({
      auth: context.auth,
      method: import_pieces_common3.HttpMethod.DELETE,
      resourceUri: `/repos/${webhook.owner}/${webhook.repo}/hooks/${webhook.webhookId}`
    });
  },
  async run(context) {
    let payloadBody = context.payload.body, branch = context.propsValue.branch;
    return branch && payloadBody.ref !== `refs/heads/${branch}` ? [] : [context.payload.body];
  }
});

// ../ap/packages/pieces/community/gitea/src/lib/triggers/new-issue.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var newIssue = (0, import_pieces_framework4.createTrigger)({
  auth: giteaAuth,
  name: "new_issue",
  displayName: "New Issue",
  description: "Triggers when a new issue is created.",
  aiMetadata: {
    description: "Fires when an issue is opened in the selected Gitea repository. Each event represents a newly created issue (only the opened action passes through)."
  },
  props: {
    repository: giteaCommon.repositoryDropdown
  },
  sampleData: {
    action: "opened",
    issue: {
      id: 1,
      url: "https://gitea.com/api/v1/repos/owner/repo/issues/1",
      number: 1,
      user: {
        id: 1,
        username: "user"
      },
      title: "Issue Title",
      body: "Issue Body",
      state: "open"
    },
    repository: {
      id: 1,
      name: "repo",
      owner: {
        login: "owner"
      },
      full_name: "owner/repo"
    },
    sender: {
      id: 1,
      username: "user"
    }
  },
  type: import_pieces_framework4.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let { repo, owner } = context.propsValue.repository, response = await giteaApiCall({
      auth: context.auth,
      method: import_pieces_common4.HttpMethod.POST,
      resourceUri: `/repos/${owner}/${repo}/hooks`,
      body: {
        type: "gitea",
        active: !0,
        events: ["issues"],
        config: {
          url: context.webhookUrl,
          content_type: "json"
        }
      }
    });
    await context.store.put("gitea_new_issue_trigger", {
      webhookId: response.body.id,
      repo,
      owner
    });
  },
  async onDisable(context) {
    let webhook = await context.store.get(
      "gitea_new_issue_trigger"
    );
    webhook && await giteaApiCall({
      auth: context.auth,
      method: import_pieces_common4.HttpMethod.DELETE,
      resourceUri: `/repos/${webhook.owner}/${webhook.repo}/hooks/${webhook.webhookId}`
    });
  },
  async run(context) {
    return context.payload.body.action === "opened" ? [context.payload.body] : [];
  }
});

// ../ap/packages/pieces/community/gitea/src/lib/triggers/new-pull-request.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var newPullRequest = (0, import_pieces_framework5.createTrigger)({
  auth: giteaAuth,
  name: "new_pull_request",
  displayName: "New Pull Request",
  description: "Triggers when a new pull request is created.",
  aiMetadata: {
    description: "Fires when a pull request is opened in the selected Gitea repository. Each event represents a newly created pull request (only the opened action passes through)."
  },
  props: {
    repository: giteaCommon.repositoryDropdown
  },
  sampleData: {
    action: "opened",
    number: 1,
    pull_request: {
      id: 1,
      url: "https://gitea.com/api/v1/repos/owner/repo/pulls/1",
      number: 1,
      user: {
        id: 1,
        username: "user"
      },
      title: "PR Title",
      body: "PR Body",
      state: "open",
      head: {
        ref: "feature-branch"
      },
      base: {
        ref: "main"
      }
    },
    repository: {
      id: 1,
      name: "repo",
      owner: {
        login: "owner"
      },
      full_name: "owner/repo"
    },
    sender: {
      id: 1,
      username: "user"
    }
  },
  type: import_pieces_framework5.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let { repo, owner } = context.propsValue.repository, response = await giteaApiCall({
      auth: context.auth,
      method: import_pieces_common5.HttpMethod.POST,
      resourceUri: `/repos/${owner}/${repo}/hooks`,
      body: {
        type: "gitea",
        active: !0,
        events: ["pull_request"],
        config: {
          url: context.webhookUrl,
          content_type: "json"
        }
      }
    });
    await context.store.put("gitea_new_pr_trigger", {
      webhookId: response.body.id,
      repo,
      owner
    });
  },
  async onDisable(context) {
    let webhook = await context.store.get(
      "gitea_new_pr_trigger"
    );
    webhook && await giteaApiCall({
      auth: context.auth,
      method: import_pieces_common5.HttpMethod.DELETE,
      resourceUri: `/repos/${webhook.owner}/${webhook.repo}/hooks/${webhook.webhookId}`
    });
  },
  async run(context) {
    return context.payload.body.action === "opened" ? [context.payload.body] : [];
  }
});

// ../ap/packages/pieces/community/gitea/src/lib/triggers/workflow-run.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var workflowRunCompleted = (0, import_pieces_framework6.createTrigger)({
  auth: giteaAuth,
  name: "workflow_run_completed",
  displayName: "Workflow Run Completed",
  description: "Triggers when a workflow run is completed. Requires Gitea Actions to be enabled on your Gitea instance.",
  aiMetadata: {
    description: "Fires when a Gitea Actions workflow run finishes in the selected repository, regardless of success or failure. Each event represents a completed run and includes its conclusion. Requires Gitea Actions to be enabled on the instance."
  },
  props: {
    repository: giteaCommon.repositoryDropdown
  },
  sampleData: {
    action: "completed",
    workflow_run: {
      id: 1,
      name: "CI",
      head_branch: "main",
      head_sha: "sha",
      status: "completed",
      conclusion: "success",
      created_at: "2023-01-01T00:00:00Z",
      updated_at: "2023-01-01T00:01:00Z"
    },
    repository: {
      name: "repo",
      owner: {
        login: "owner"
      },
      full_name: "owner/repo"
    },
    sender: {
      login: "sender"
    }
  },
  type: import_pieces_framework6.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let { repo, owner } = context.propsValue.repository, response = await giteaApiCall({
      auth: context.auth,
      method: import_pieces_common6.HttpMethod.POST,
      resourceUri: `/repos/${owner}/${repo}/hooks`,
      body: {
        type: "gitea",
        active: !0,
        events: ["workflow_run"],
        config: {
          url: context.webhookUrl,
          content_type: "json"
        }
      }
    });
    await context.store.put("gitea_workflow_run_trigger", {
      webhookId: response.body.id,
      repo,
      owner
    });
  },
  async onDisable(context) {
    let webhook = await context.store.get(
      "gitea_workflow_run_trigger"
    );
    webhook && await giteaApiCall({
      auth: context.auth,
      method: import_pieces_common6.HttpMethod.DELETE,
      resourceUri: `/repos/${webhook.owner}/${webhook.repo}/hooks/${webhook.webhookId}`
    });
  },
  async run(context) {
    return context.payload.body.action === "completed" ? [context.payload.body] : [];
  }
});

// ../ap/packages/pieces/community/gitea/src/lib/actions/create-issue.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var createIssue = (0, import_pieces_framework7.createAction)({
  auth: giteaAuth,
  name: "create_issue",
  displayName: "Create Issue",
  description: "Creates a new issue in a repository.",
  audience: "both",
  aiMetadata: { description: "Opens a new issue on a Gitea repository with a title and optional body. Use to file a bug, task, or feature request. Requires the target repository; each call creates a separate issue, so it is not idempotent.", idempotent: !1 },
  props: {
    repository: giteaCommon.repositoryDropdown,
    title: import_pieces_framework7.Property.ShortText({
      displayName: "Title",
      required: !0
    }),
    body: import_pieces_framework7.Property.LongText({
      displayName: "Body",
      required: !1
    })
  },
  async run(context) {
    let { repo, owner } = context.propsValue.repository, { title, body } = context.propsValue;
    return (await giteaApiCall({
      auth: context.auth,
      method: import_pieces_common7.HttpMethod.POST,
      resourceUri: `/repos/${owner}/${repo}/issues`,
      body: {
        title,
        body
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/gitea/src/lib/actions/create-comment.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var createComment = (0, import_pieces_framework8.createAction)({
  auth: giteaAuth,
  name: "create_comment",
  displayName: "Add Comment to Issue/PR",
  description: "Adds a comment to an issue or pull request.",
  audience: "both",
  aiMetadata: { description: "Posts a comment on a Gitea issue or pull request, identified by its index number within the repository (issues and PRs share the same number space). Use to reply, give feedback, or note status. Each call appends a new comment, so it is not idempotent.", idempotent: !1 },
  props: {
    repository: giteaCommon.repositoryDropdown,
    index: import_pieces_framework8.Property.Number({
      displayName: "Issue/PR Index",
      description: "The index of the issue or pull request.",
      required: !0
    }),
    body: import_pieces_framework8.Property.LongText({
      displayName: "Comment Body",
      required: !0
    })
  },
  async run(context) {
    let { repo, owner } = context.propsValue.repository, { index, body } = context.propsValue;
    return (await giteaApiCall({
      auth: context.auth,
      method: import_pieces_common8.HttpMethod.POST,
      resourceUri: `/repos/${owner}/${repo}/issues/${index}/comments`,
      body: {
        body
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/gitea/src/lib/actions/update-repo.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var updateRepoSync = (0, import_pieces_framework9.createAction)({
  auth: giteaAuth,
  name: "update_repo",
  displayName: "Update Repository",
  description: "Update repository settings.",
  audience: "both",
  aiMetadata: { description: "Updates settings on an existing Gitea repository such as its name, description, website, or private/public visibility; only the fields you supply are changed. Use to rename a repo or adjust its metadata. Repeating the call with the same values leaves the repository in the same state, so it is idempotent.", idempotent: !0 },
  props: {
    repository: giteaCommon.repositoryDropdown,
    name: import_pieces_framework9.Property.ShortText({
      displayName: "Name",
      required: !1,
      description: "The name of the repository."
    }),
    description: import_pieces_framework9.Property.ShortText({
      displayName: "Description",
      required: !1
    }),
    private: import_pieces_framework9.Property.Checkbox({
      displayName: "Private",
      required: !1
    }),
    website: import_pieces_framework9.Property.ShortText({
      displayName: "Website",
      required: !1
    })
  },
  async run(context) {
    let { repo, owner } = context.propsValue.repository, { name, description, website } = context.propsValue, isPrivate = context.propsValue.private, body = {};
    return name != null && (body.name = name), description != null && (body.description = description), website != null && (body.website = website), isPrivate !== void 0 && (body.private = isPrivate), (await giteaApiCall({
      auth: context.auth,
      method: import_pieces_common9.HttpMethod.PATCH,
      resourceUri: `/repos/${owner}/${repo}`,
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/gitea/src/index.ts
var gitea = (0, import_pieces_framework10.createPiece)({
  displayName: "Gitea",
  description: "Self-hosted Git service",
  auth: giteaAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/gitea.png",
  categories: [import_pieces_framework11.PieceCategory.DEVELOPER_TOOLS],
  authors: ["Anmol202005"],
  actions: [
    createIssue,
    createComment,
    updateRepoSync,
    (0, import_pieces_common10.createCustomApiCallAction)({
      baseUrl: (auth) => {
        var _a, _b;
        return `${((_b = (_a = auth.props) == null ? void 0 : _a.baseUrl) != null ? _b : "https://gitea.com").replace(/\/$/, "")}/api/v1`;
      },
      auth: giteaAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.access_token}`
      })
    })
  ],
  triggers: [newCommit, newIssue, newPullRequest, workflowRunCompleted]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  gitea
});
