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
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key != "symbol" ? key + "" : key, value);

// ../ap/packages/pieces/community/jungle-grid/src/index.ts
var index_exports = {};
__export(index_exports, {
  jungleGrid: () => jungleGrid
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_framework13 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/jungle-grid/src/lib/actions/cancel-job.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/jungle-grid/src/lib/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/jungle-grid/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
var JungleGridApiError = class extends Error {
  constructor({ status, code, message }) {
    super(`${code}: ${message}`);
    __publicField(this, "status");
    __publicField(this, "code");
    this.name = "JungleGridApiError", this.status = status, this.code = code;
  }
}, jungleGridClient = { apiCall, normalizeBaseUrl };
async function apiCall({
  auth,
  method,
  path,
  body,
  queryParams
}) {
  let { api_base_url, api_key } = getAuthProps(auth);
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${normalizeBaseUrl(api_base_url)}${path}`,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: api_key
      },
      queryParams,
      body
    })).body;
  } catch (error) {
    throw error instanceof import_pieces_common.HttpError ? toJungleGridApiError(error) : error;
  }
}
function normalizeBaseUrl(baseUrl) {
  return baseUrl.trim().replace(/\/+$/, "");
}
function toJungleGridApiError(error) {
  var _a, _b;
  let { status, body } = error.response, parsed = parseApiError(body);
  return new JungleGridApiError({
    status,
    code: (_a = parsed.code) != null ? _a : statusCodeToErrorCode(status),
    message: (_b = parsed.message) != null ? _b : statusCodeToMessage(status)
  });
}
function parseApiError(body) {
  var _a, _b, _c;
  if (!isRecord(body))
    return {};
  let nestedError = body.error;
  return isRecord(nestedError) ? {
    code: optionalString(nestedError, "code"),
    message: (_a = optionalString(nestedError, "message")) != null ? _a : optionalString(nestedError, "detail")
  } : {
    code: optionalString(body, "code"),
    message: (_c = (_b = optionalString(body, "message")) != null ? _b : optionalString(body, "detail")) != null ? _c : optionalString(body, "error")
  };
}
function optionalString(obj, key) {
  let value = obj[key];
  return typeof value != "string" ? void 0 : value.trim() || void 0;
}
function isRecord(value) {
  return typeof value == "object" && value !== null && !Array.isArray(value);
}
function statusCodeToErrorCode(status) {
  return status === 401 ? "UNAUTHORIZED" : status === 403 ? "FORBIDDEN" : status === 404 ? "NOT_FOUND" : status === 409 ? "CONFLICT" : status === 429 ? "RATE_LIMITED" : status >= 500 ? "UPSTREAM_ERROR" : "API_ERROR";
}
function statusCodeToMessage(status) {
  return status === 401 ? "Jungle Grid authentication failed. Check the API key on the connection." : status === 403 ? "The Jungle Grid API key is missing the required scope for this action." : status === 404 ? "The requested Jungle Grid resource was not found." : status === 409 ? "Jungle Grid could not complete the request because the resource is in a conflicting state." : status === 429 ? "Jungle Grid API rate limit exceeded. Try again later." : status >= 500 ? "Jungle Grid API is temporarily unavailable." : `Jungle Grid API request failed with status ${status}.`;
}
function getAuthProps(auth) {
  return "props" in auth ? auth.props : auth;
}

// ../ap/packages/pieces/community/jungle-grid/src/lib/common/props.ts
var import_pieces_framework = require("@activepieces/pieces-framework");
function buildEstimateJobPayload(propsValue) {
  return buildJobPayload({
    propsValue,
    includeSubmitOnlyFields: !1
  });
}
function buildSubmitJobPayload(propsValue) {
  return buildJobPayload({
    propsValue,
    includeSubmitOnlyFields: !0
  });
}
function buildJobPayload({
  propsValue,
  includeSubmitOnlyFields
}) {
  let workloadType = requiredString({
    propsValue,
    key: "workload_type",
    displayName: "Workload Type"
  });
  assertOneOf({
    value: workloadType,
    key: "workload_type",
    allowedValues: workloadTypes
  });
  let image = requiredString({
    propsValue,
    key: "image",
    displayName: "Image"
  }), constraints = removeEmptyValues({
    max_price_per_hour: optionalNumber({
      propsValue,
      key: "max_price_per_hour",
      displayName: "Max Price Per Hour",
      min: 0
    }),
    preferred_gpu_family: optionalString2(propsValue, "preferred_gpu_family"),
    avoid_gpu_families: optionalStringArray(propsValue, "avoid_gpu_families"),
    region_preference: optionalString2(propsValue, "region_preference"),
    latency_priority: optionalEnum({
      propsValue,
      key: "latency_priority",
      allowedValues: priorityValues
    }),
    cost_priority: optionalEnum({
      propsValue,
      key: "cost_priority",
      allowedValues: priorityValues
    })
  });
  return removeEmptyValues({
    name: optionalString2(propsValue, "name"),
    image,
    workload_type: workloadType,
    model_size_gb: optionalNumber({
      propsValue,
      key: "model_size_gb",
      displayName: "Model Size (GB)",
      min: 0
    }),
    command: optionalString2(propsValue, "command"),
    args: optionalStringArray(propsValue, "args"),
    optimize_for: optionalEnum({
      propsValue,
      key: "optimize_for",
      allowedValues: optimizeForValues
    }),
    constraints: Object.keys(constraints).length > 0 ? constraints : void 0,
    callback_url: includeSubmitOnlyFields ? optionalCallbackUrl(propsValue) : void 0,
    callback_metadata: includeSubmitOnlyFields ? optionalRecord(propsValue, "callback_metadata") : void 0
  });
}
function buildLogsQueryParams(propsValue) {
  let tailLines = optionalNumber({
    propsValue,
    key: "tail_lines",
    displayName: "Tail Lines"
  }), queryParams = removeEmptyValues({
    tail: tailLines === void 0 ? void 0 : String(numberInRange({
      value: tailLines,
      key: "tail_lines",
      min: 1,
      max: 500
    })),
    cursor: optionalString2(propsValue, "cursor"),
    stream: optionalEnum({
      propsValue,
      key: "stream",
      allowedValues: logStreamValues
    })
  });
  return stringifyRecord(queryParams);
}
function buildListJobsQueryParams(propsValue) {
  let limit = optionalNumber({
    propsValue,
    key: "limit",
    displayName: "Limit"
  }), queryParams = removeEmptyValues({
    limit: limit === void 0 ? void 0 : String(numberInRange({
      value: limit,
      key: "limit",
      min: 1,
      max: 100
    })),
    cursor: optionalString2(propsValue, "cursor"),
    status: optionalEnum({
      propsValue,
      key: "status",
      allowedValues: jobStatusValues
    })
  });
  return stringifyRecord(queryParams);
}
function optionalString2(propsValue, key) {
  let value = propsValue[key];
  if (value == null)
    return;
  if (typeof value != "string")
    throw new Error(`${key} must be a string.`);
  return value.trim() || void 0;
}
function requiredString({
  propsValue,
  key,
  displayName
}) {
  let value = optionalString2(propsValue, key);
  if (!value)
    throw new Error(`${displayName} is required.`);
  return value;
}
function optionalNumber({
  propsValue,
  key,
  displayName,
  min
}) {
  let value = propsValue[key];
  if (!(value == null || value === "")) {
    if (typeof value != "number" || !Number.isFinite(value))
      throw new Error(`${displayName} must be a finite number.`);
    if (min !== void 0 && value <= min)
      throw new Error(`${displayName} must be greater than ${min}.`);
    return value;
  }
}
function optionalStringArray(propsValue, key) {
  let value = propsValue[key];
  if (value == null)
    return;
  if (!Array.isArray(value))
    throw new Error(`${key} must be an array of strings.`);
  let items = value.map((item, index) => {
    if (typeof item != "string")
      throw new Error(`${key}[${index}] must be a string.`);
    return item;
  }).filter((item) => item.length > 0);
  return items.length > 0 ? items : void 0;
}
function optionalRecord(propsValue, key) {
  let value = propsValue[key];
  if (value != null) {
    if (!isRecord2(value))
      throw new Error(`${key} must be an object.`);
    return value;
  }
}
function optionalEnum({
  propsValue,
  key,
  allowedValues
}) {
  let value = optionalString2(propsValue, key);
  if (value !== void 0)
    return assertOneOf({
      value,
      key,
      allowedValues
    }), value;
}
function optionalCallbackUrl(propsValue) {
  let value = optionalString2(propsValue, "callback_url");
  if (value === void 0)
    return;
  let url = parseUrl(value);
  if (url.protocol === "https:" || isLocalhost(url.hostname))
    return value;
  throw new Error("Callback URL must use HTTPS unless it targets localhost.");
}
function parseUrl(value) {
  try {
    return new URL(value);
  } catch (e) {
    throw new Error("Callback URL must be a valid URL.");
  }
}
function isLocalhost(hostname) {
  return ["localhost", "127.0.0.1", "::1"].includes(hostname);
}
function assertOneOf({
  value,
  key,
  allowedValues
}) {
  if (!allowedValues.includes(value))
    throw new Error(`${key} must be one of: ${allowedValues.join(", ")}.`);
}
function numberInRange({
  value,
  key,
  min,
  max
}) {
  if (value < min || value > max)
    throw new Error(`${key} must be between ${min} and ${max}.`);
  return Math.floor(value);
}
function removeEmptyValues(value) {
  return Object.entries(value).reduce((result, [key, entry]) => entry == null || entry === "" ? result : __spreadProps(__spreadValues({}, result), {
    [key]: entry
  }), {});
}
function stringifyRecord(value) {
  return Object.entries(value).reduce((result, [key, entry]) => __spreadProps(__spreadValues({}, result), {
    [key]: String(entry)
  }), {});
}
function isRecord2(value) {
  return typeof value == "object" && value !== null && !Array.isArray(value);
}
var asyncInstructions = import_pieces_framework.Property.MarkDown({
  value: `### Async workflow

1. Run **Estimate Job** to preview route, queue, and cost signals.
2. Run **Submit Job** to queue work. This returns immediately with the Jungle Grid job status.
3. Add a Delay, branch, or loop, then call **Get Job Status**, **Get Job Runtime**, or **Get Job Logs**.
4. After completion, call **List Job Artifacts** and **Get Artifact Download URL** if the job produced files.`
}), jobId = import_pieces_framework.Property.ShortText({
  displayName: "Job ID",
  description: "The `job_id` returned by Submit Job or List Jobs.",
  required: !0
}), artifactId = import_pieces_framework.Property.ShortText({
  displayName: "Artifact ID",
  description: "The artifact ID returned by List Job Artifacts.",
  required: !0
}), priorityLabels = {
  low: "Low",
  balanced: "Balanced",
  high: "High"
}, workloadTypes = ["inference", "batch", "training"], optimizeForValues = ["balanced", "cost", "speed"], priorityValues = ["low", "balanced", "high"], logStreamValues = ["all", "stdout", "stderr"], jobStatusValues = [
  "pending",
  "queued",
  "assigned",
  "provisioning",
  "starting",
  "running",
  "completed",
  "failed",
  "rejected",
  "cancelled"
], baseJobPayloadProps = {
  name: import_pieces_framework.Property.ShortText({
    displayName: "Job Name",
    description: "Optional human-readable label for this Jungle Grid job.",
    required: !1
  }),
  workload_type: import_pieces_framework.Property.StaticDropdown({
    displayName: "Workload Type",
    description: "The workload class Jungle Grid should route.",
    required: !0,
    defaultValue: "inference",
    options: {
      options: [
        { label: "Inference", value: "inference" },
        { label: "Batch", value: "batch" },
        { label: "Training", value: "training" }
      ]
    }
  }),
  image: import_pieces_framework.Property.ShortText({
    displayName: "Container Image",
    description: "Public container image reference, for example `python:3.11` or `pytorch/pytorch:2.4.0-cuda12.1-cudnn9-runtime`.",
    required: !0
  }),
  model_size_gb: import_pieces_framework.Property.Number({
    displayName: "Model Size (GB)",
    description: "Approximate model size in GB. Jungle Grid uses this for routing and cost estimation.",
    required: !1
  }),
  command: import_pieces_framework.Property.LongText({
    displayName: "Command",
    description: "Container command to run. Leave blank to use the image default entrypoint or command.",
    required: !1
  }),
  args: import_pieces_framework.Property.Array({
    displayName: "Arguments",
    description: "Optional command arguments, one item per argument.",
    required: !1
  }),
  optimize_for: import_pieces_framework.Property.StaticDropdown({
    displayName: "Optimize For",
    description: "Placement preference for cost, speed, or balanced routing.",
    required: !1,
    defaultValue: "balanced",
    options: {
      options: [
        { label: "Balanced", value: "balanced" },
        { label: "Cost", value: "cost" },
        { label: "Speed", value: "speed" }
      ]
    }
  })
}, routingConstraintProps = {
  max_price_per_hour: import_pieces_framework.Property.Number({
    displayName: "Max Price Per Hour",
    description: "Optional maximum hourly price in USD.",
    required: !1
  }),
  preferred_gpu_family: import_pieces_framework.Property.ShortText({
    displayName: "Preferred GPU Family",
    description: "Optional GPU family preference, for example `l4`.",
    required: !1
  }),
  avoid_gpu_families: import_pieces_framework.Property.Array({
    displayName: "Avoid GPU Families",
    description: "Optional GPU families to avoid, one item per family, for example `a100`.",
    required: !1
  }),
  region_preference: import_pieces_framework.Property.ShortText({
    displayName: "Region Preference",
    description: "Optional region preference, for example `us-east`.",
    required: !1
  }),
  latency_priority: import_pieces_framework.Property.StaticDropdown({
    displayName: "Latency Priority",
    description: "Optional latency priority for routing.",
    required: !1,
    options: {
      options: priorityValues.map((value) => ({
        label: priorityLabels[value],
        value
      }))
    }
  }),
  cost_priority: import_pieces_framework.Property.StaticDropdown({
    displayName: "Cost Priority",
    description: "Optional cost priority for routing.",
    required: !1,
    options: {
      options: priorityValues.map((value) => ({
        label: priorityLabels[value],
        value
      }))
    }
  })
}, submitOnlyProps = {
  callback_url: import_pieces_framework.Property.ShortText({
    displayName: "Callback URL",
    description: "Optional HTTPS callback URL for terminal job lifecycle events.",
    required: !1
  }),
  callback_metadata: import_pieces_framework.Property.Object({
    displayName: "Callback Metadata",
    description: "Optional key-value metadata Jungle Grid should include with callback events.",
    required: !1
  })
}, estimateJobProps = __spreadValues(__spreadValues({}, baseJobPayloadProps), routingConstraintProps), submitJobProps = __spreadValues(__spreadValues(__spreadValues({}, baseJobPayloadProps), routingConstraintProps), submitOnlyProps), jungleGridProps = {
  artifactId,
  asyncInstructions,
  buildEstimateJobPayload,
  buildListJobsQueryParams,
  buildLogsQueryParams,
  buildSubmitJobPayload,
  estimateJobProps,
  jobId,
  jobStatusValues,
  logStreamValues,
  submitJobProps
};

// ../ap/packages/pieces/community/jungle-grid/src/lib/common/index.ts
var defaultBaseUrl = "https://api.junglegrid.dev", endpoints = {
  estimateJob: "/v1/jobs/estimate",
  submitJob: "/v1/jobs",
  listJobs: "/v1/jobs",
  jobStatus: (jobId2) => `/v1/jobs/${encodeURIComponent(jobId2)}`,
  jobRuntime: (jobId2) => `/v1/jobs/${encodeURIComponent(jobId2)}/runtime`,
  jobLogs: (jobId2) => `/v1/jobs/${encodeURIComponent(jobId2)}/logs`,
  artifacts: (jobId2) => `/v1/jobs/${encodeURIComponent(jobId2)}/artifacts`,
  cancelJob: (jobId2) => `/v1/jobs/${encodeURIComponent(jobId2)}/cancel`,
  artifactDownloadUrl: (jobId2, artifactId2) => `/v1/jobs/${encodeURIComponent(jobId2)}/artifacts/${encodeURIComponent(artifactId2)}/download`
}, jungleGridCommon = __spreadProps(__spreadValues(__spreadValues({}, jungleGridClient), jungleGridProps), {
  defaultBaseUrl,
  endpoints
});

// ../ap/packages/pieces/community/jungle-grid/src/lib/auth.ts
var jungleGridAuth = import_pieces_framework2.PieceAuth.CustomAuth({
  displayName: "Connection",
  description: "Connect with a scoped Jungle Grid API key. Use the default API base URL unless your workspace provides a custom endpoint.",
  required: !0,
  props: {
    api_base_url: import_pieces_framework2.Property.ShortText({
      displayName: "API Base URL",
      description: "Base URL for the Jungle Grid API. Keep the default unless Jungle Grid provides a specific endpoint for your workspace.",
      required: !0,
      defaultValue: jungleGridCommon.defaultBaseUrl
    }),
    api_key: import_pieces_framework2.PieceAuth.SecretText({
      displayName: "API Key",
      description: "Paste a scoped Jungle Grid API key from the Jungle Grid portal.",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    try {
      return await jungleGridCommon.apiCall({
        auth,
        method: import_pieces_common2.HttpMethod.GET,
        path: jungleGridCommon.endpoints.listJobs,
        queryParams: {
          limit: "1"
        }
      }), {
        valid: !0
      };
    } catch (error) {
      return error instanceof JungleGridApiError ? {
        valid: !1,
        error: error.message
      } : {
        valid: !1,
        error: "Unable to validate the Jungle Grid connection."
      };
    }
  }
});

// ../ap/packages/pieces/community/jungle-grid/src/lib/actions/cancel-job.ts
var cancelJob = (0, import_pieces_framework3.createAction)({
  auth: jungleGridAuth,
  name: "cancel_job",
  displayName: "Cancel Job",
  description: "Cancel a non-terminal Jungle Grid job. This may stop active execution.",
  audience: "both",
  aiMetadata: {
    description: "Request cancellation of a running or queued Jungle Grid job by job ID, optionally recording a reason. Pick this to stop a job that is no longer needed; it mutates job state and only applies to jobs that have not already reached a terminal status. Not idempotent: retrying against an already-cancelled or finished job may fail.",
    idempotent: !1
  },
  props: {
    job_id: jungleGridCommon.jobId,
    reason: import_pieces_framework3.Property.ShortText({
      displayName: "Reason",
      description: "Optional cancellation reason stored with the Jungle Grid job status.",
      required: !1,
      defaultValue: "Cancelled from Activepieces"
    })
  },
  async run(context) {
    return await jungleGridCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common3.HttpMethod.POST,
      path: jungleGridCommon.endpoints.cancelJob(context.propsValue.job_id),
      body: {
        reason: context.propsValue.reason
      }
    });
  }
});

// ../ap/packages/pieces/community/jungle-grid/src/lib/actions/estimate-job.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var estimateJob = (0, import_pieces_framework4.createAction)({
  auth: jungleGridAuth,
  name: "estimate_job",
  displayName: "Estimate Job",
  description: "Estimate the cost, duration, and resources for a Jungle Grid job.",
  audience: "both",
  aiMetadata: {
    description: "Compute a cost, duration, and resource estimate for a prospective Jungle Grid job from the same specification used by Submit Job, without actually running anything. Pick this to preview or budget-check a job before submission; it is a safe, idempotent calculation with no side effects. Use Submit Job afterwards to actually execute.",
    idempotent: !0
  },
  props: __spreadValues({
    instructions: jungleGridCommon.asyncInstructions
  }, jungleGridCommon.estimateJobProps),
  async run(context) {
    return await jungleGridCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common4.HttpMethod.POST,
      path: jungleGridCommon.endpoints.estimateJob,
      body: jungleGridCommon.buildEstimateJobPayload(context.propsValue)
    });
  }
});

// ../ap/packages/pieces/community/jungle-grid/src/lib/actions/get-artifact-download-url.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var getArtifactDownloadUrl = (0, import_pieces_framework5.createAction)({
  auth: jungleGridAuth,
  name: "get_artifact_download_url",
  displayName: "Get Artifact Download URL",
  description: "Create a temporary signed download URL for a Jungle Grid artifact. Treat the returned URL as a secret.",
  audience: "both",
  aiMetadata: {
    description: "Generate a short-lived signed download URL for one artifact of a Jungle Grid job, given the job ID and artifact ID. Pick this after List Job Artifacts when a file actually needs to be downloaded; calling it is safe to repeat (each call just issues a fresh temporary URL), but the URL expires and must be treated as a secret.",
    idempotent: !0
  },
  props: {
    job_id: jungleGridCommon.jobId,
    artifact_id: jungleGridCommon.artifactId
  },
  async run(context) {
    return await jungleGridCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common5.HttpMethod.POST,
      path: jungleGridCommon.endpoints.artifactDownloadUrl(
        context.propsValue.job_id,
        context.propsValue.artifact_id
      )
    });
  }
});

// ../ap/packages/pieces/community/jungle-grid/src/lib/actions/get-job-logs.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var getJobLogs = (0, import_pieces_framework6.createAction)({
  auth: jungleGridAuth,
  name: "get_job_logs",
  displayName: "Get Job Logs",
  description: "Get recent stdout, stderr, or combined logs for a submitted Jungle Grid job.",
  audience: "both",
  aiMetadata: {
    description: "Fetch recent log lines for a Jungle Grid job by job ID, choosing stdout, stderr, or both, with a tail of up to 500 lines and cursor-based paging for more. Pick this to debug a failing job or inspect output text; for structured status or exit-code details use Get Job Status or Get Job Runtime instead. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    job_id: jungleGridCommon.jobId,
    tail_lines: import_pieces_framework6.Property.Number({
      displayName: "Tail Lines",
      description: "Number of recent log lines to fetch. Jungle Grid accepts 1 to 500.",
      required: !1,
      defaultValue: 100
    }),
    cursor: import_pieces_framework6.Property.ShortText({
      displayName: "Cursor",
      description: "Optional `next_cursor` from a previous logs response.",
      required: !1
    }),
    stream: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Stream",
      description: "Choose which log stream to fetch.",
      required: !1,
      defaultValue: "all",
      options: {
        options: [
          { label: "All", value: "all" },
          { label: "stdout", value: "stdout" },
          { label: "stderr", value: "stderr" }
        ]
      }
    })
  },
  async run(context) {
    return await jungleGridCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common6.HttpMethod.GET,
      path: jungleGridCommon.endpoints.jobLogs(context.propsValue.job_id),
      queryParams: jungleGridCommon.buildLogsQueryParams(context.propsValue)
    });
  }
});

// ../ap/packages/pieces/community/jungle-grid/src/lib/actions/get-job-runtime.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");
var getJobRuntime = (0, import_pieces_framework7.createAction)({
  auth: jungleGridAuth,
  name: "get_job_runtime",
  displayName: "Get Job Runtime",
  description: "Get runtime tails, exit code, and runtime availability details for a Jungle Grid job.",
  audience: "both",
  aiMetadata: {
    description: "Retrieve runtime execution details for a Jungle Grid job by job ID, including log tails, exit code, and whether runtime data is available. Pick this to diagnose how a job actually ran; use Get Job Status for lifecycle state only, or Get Job Logs for fuller paginated log output. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    job_id: jungleGridCommon.jobId
  },
  async run(context) {
    return await jungleGridCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common7.HttpMethod.GET,
      path: jungleGridCommon.endpoints.jobRuntime(context.propsValue.job_id)
    });
  }
});

// ../ap/packages/pieces/community/jungle-grid/src/lib/actions/get-job-status.ts
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework");
var getJobStatus = (0, import_pieces_framework8.createAction)({
  auth: jungleGridAuth,
  name: "get_job_status",
  displayName: "Get Job Status",
  description: "Get the current status and metadata for a submitted Jungle Grid job.",
  audience: "both",
  aiMetadata: {
    description: "Look up the current lifecycle status and metadata of a Jungle Grid job by job ID. Pick this as the standard poll after Submit Job to check whether a job is queued, running, or finished; for exit codes and log tails use Get Job Runtime, and for output files use List Job Artifacts. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    job_id: jungleGridCommon.jobId
  },
  async run(context) {
    return await jungleGridCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common8.HttpMethod.GET,
      path: jungleGridCommon.endpoints.jobStatus(context.propsValue.job_id)
    });
  }
});

// ../ap/packages/pieces/community/jungle-grid/src/lib/actions/list-artifacts.ts
var import_pieces_common9 = require("@activepieces/pieces-common"), import_pieces_framework9 = require("@activepieces/pieces-framework");
var listArtifacts = (0, import_pieces_framework9.createAction)({
  auth: jungleGridAuth,
  name: "list_artifacts",
  displayName: "List Job Artifacts",
  description: "List files produced by a Jungle Grid job when managed artifact storage is available.",
  audience: "both",
  aiMetadata: {
    description: "List the output files (artifacts) produced by a Jungle Grid job, by job ID. Pick this after a job completes to discover what it produced, then use Get Artifact Download URL to fetch a specific file; only works when the workspace has managed artifact storage. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    job_id: jungleGridCommon.jobId
  },
  async run(context) {
    return await jungleGridCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common9.HttpMethod.GET,
      path: jungleGridCommon.endpoints.artifacts(context.propsValue.job_id)
    });
  }
});

// ../ap/packages/pieces/community/jungle-grid/src/lib/actions/list-jobs.ts
var import_pieces_common10 = require("@activepieces/pieces-common"), import_pieces_framework10 = require("@activepieces/pieces-framework");
var listJobs = (0, import_pieces_framework10.createAction)({
  auth: jungleGridAuth,
  name: "list_jobs",
  displayName: "List Jobs",
  description: "List Jungle Grid jobs for the authenticated workspace, optionally filtered by status.",
  audience: "both",
  aiMetadata: {
    description: "List jobs in the authenticated Jungle Grid workspace, optionally filtered by status, with cursor-based pagination up to 100 per page. Pick this to find a job ID when none is known or to survey recent activity; if you already have a job ID, Get Job Status is more direct. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    limit: import_pieces_framework10.Property.Number({
      displayName: "Limit",
      description: "Maximum number of jobs to return. Jungle Grid accepts 1 to 100.",
      required: !1,
      defaultValue: 20
    }),
    cursor: import_pieces_framework10.Property.ShortText({
      displayName: "Cursor",
      description: "Optional cursor from a previous List Jobs response.",
      required: !1
    }),
    status: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Status",
      description: "Optional job status filter.",
      required: !1,
      options: {
        options: jungleGridCommon.jobStatusValues.map((status) => ({
          label: status,
          value: status
        }))
      }
    })
  },
  async run(context) {
    return await jungleGridCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common10.HttpMethod.GET,
      path: jungleGridCommon.endpoints.listJobs,
      queryParams: jungleGridCommon.buildListJobsQueryParams(context.propsValue)
    });
  }
});

// ../ap/packages/pieces/community/jungle-grid/src/lib/actions/submit-job.ts
var import_pieces_common11 = require("@activepieces/pieces-common"), import_pieces_framework11 = require("@activepieces/pieces-framework");
var submitJob = (0, import_pieces_framework11.createAction)({
  auth: jungleGridAuth,
  name: "submit_job",
  displayName: "Submit Job",
  description: "Submit a Jungle Grid job and return immediately with job metadata. This action does not wait for completion.",
  audience: "both",
  aiMetadata: {
    description: "Submit a new Jungle Grid job for asynchronous execution; it returns immediately with job metadata and does not wait for completion, so poll with Get Job Status afterwards. Pick this to actually run a workload (use Estimate Job first to preview cost). Not idempotent: each call creates a new job, so retries can launch duplicates.",
    idempotent: !1
  },
  props: __spreadValues({
    instructions: jungleGridCommon.asyncInstructions
  }, jungleGridCommon.submitJobProps),
  async run(context) {
    return await jungleGridCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common11.HttpMethod.POST,
      path: jungleGridCommon.endpoints.submitJob,
      body: jungleGridCommon.buildSubmitJobPayload(context.propsValue)
    });
  }
});

// ../ap/packages/pieces/community/jungle-grid/src/index.ts
var import_pieces_common12 = require("@activepieces/pieces-common"), jungleGrid = (0, import_pieces_framework12.createPiece)({
  displayName: "Jungle Grid",
  description: "Submit async compute jobs, monitor status and logs, and retrieve artifacts from Jungle Grid.",
  auth: jungleGridAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/jungle-grid.png",
  categories: [
    import_pieces_framework13.PieceCategory.ARTIFICIAL_INTELLIGENCE,
    import_pieces_framework13.PieceCategory.DEVELOPER_TOOLS
  ],
  authors: ["junglegrid", "dejaguarkyng"],
  actions: [
    estimateJob,
    submitJob,
    listJobs,
    getJobStatus,
    getJobRuntime,
    getJobLogs,
    listArtifacts,
    getArtifactDownloadUrl,
    cancelJob,
    (0, import_pieces_common12.createCustomApiCallAction)({
      baseUrl: () => "https://api.junglegrid.dev",
      auth: jungleGridAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.props.api_key}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  jungleGrid
});
