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

// ../ap/packages/pieces/community/google-cloud-storage/src/index.ts
var index_exports = {};
__export(index_exports, {
  googleCloudStorage: () => googleCloudStorage
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework17 = require("@activepieces/pieces-framework"), import_pieces_framework18 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/google-cloud-storage/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), googleCloudStorageAuth = import_pieces_framework.PieceAuth.OAuth2({
  description: "",
  authUrl: "https://accounts.google.com/o/oauth2/auth",
  tokenUrl: "https://oauth2.googleapis.com/token",
  required: !0,
  scope: [
    "https://www.googleapis.com/auth/devstorage.read_write",
    "https://www.googleapis.com/auth/devstorage.full_control",
    "https://www.googleapis.com/auth/cloud-platform.read-only",
    "https://www.googleapis.com/auth/pubsub",
    "email"
  ]
});

// ../ap/packages/pieces/community/google-cloud-storage/src/lib/actions/create-bucket.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/google-cloud-storage/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), gcsCommon = {
  gcsBaseUrl: "https://www.googleapis.com/storage/v1",
  pubsubBaseUrl: "https://pubsub.googleapis.com/v1",
  async makeGCSRequest(method, path, accessToken, body) {
    let url = path.startsWith("http") ? path : `${this.gcsBaseUrl}${path}`;
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: accessToken
      },
      body
    })).body;
  },
  async makePubSubRequest(method, path, accessToken, body) {
    let url = path.startsWith("http") ? path : `${this.pubsubBaseUrl}${path}`;
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: accessToken
      },
      body
    })).body;
  },
  // For backward compatibility
  async makeRequest(method, path, accessToken, body) {
    return this.makeGCSRequest(method, path, accessToken, body);
  }
};

// ../ap/packages/pieces/community/google-cloud-storage/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common");
var bucketDropdown = import_pieces_framework2.Property.Dropdown({
  displayName: "Bucket",
  required: !0,
  refreshers: ["projectId"],
  auth: googleCloudStorageAuth,
  options: async ({ auth, projectId }) => {
    var _a;
    if (!auth || !projectId)
      return {
        disabled: !0,
        options: [],
        placeholder: projectId ? "Please connect your account first" : "Please enter project ID first"
      };
    try {
      let authValue = auth;
      return {
        disabled: !1,
        options: ((_a = (await gcsCommon.makeRequest(
          import_pieces_common2.HttpMethod.GET,
          `/b?project=${projectId}`,
          authValue.access_token
        )).items) == null ? void 0 : _a.map((bucket) => ({
          label: bucket.name,
          value: bucket.name
        }))) || []
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load buckets"
      };
    }
  }
}), objectDropdown = (bucketProperty) => import_pieces_framework2.Property.Dropdown({
  displayName: "Object",
  required: !0,
  auth: googleCloudStorageAuth,
  refreshers: [bucketProperty],
  options: async ({ auth, [bucketProperty]: bucket }) => {
    var _a;
    if (!auth || !bucket)
      return {
        disabled: !0,
        options: [],
        placeholder: bucket ? "Please connect your account first" : "Please select a bucket first"
      };
    try {
      let authValue = auth;
      return {
        disabled: !1,
        options: ((_a = (await gcsCommon.makeRequest(
          import_pieces_common2.HttpMethod.GET,
          `/b/${bucket}/o?maxResults=100`,
          authValue.access_token
        )).items) == null ? void 0 : _a.map((object) => ({
          label: object.name,
          value: object.name
        }))) || []
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load objects"
      };
    }
  }
}), projectIdProperty = import_pieces_framework2.Property.Dropdown({
  displayName: "Project",
  required: !0,
  auth: googleCloudStorageAuth,
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first"
      };
    try {
      let authValue = auth;
      return {
        disabled: !1,
        options: ((_a = (await gcsCommon.makeRequest(
          import_pieces_common2.HttpMethod.GET,
          "https://cloudresourcemanager.googleapis.com/v1/projects?filter=lifecycleState:ACTIVE",
          authValue.access_token
        )).projects) == null ? void 0 : _a.map((project) => ({
          label: `${project.displayName || project.name} (${project.projectId})`,
          value: project.projectId
        }))) || []
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load projects. Check your permissions."
      };
    }
  }
}), bucketNameProperty = import_pieces_framework2.Property.ShortText({
  displayName: "Bucket Name",
  description: "Unique name for your bucket (must be globally unique, 3-63 characters)",
  required: !0
}), objectNameProperty = import_pieces_framework2.Property.ShortText({
  displayName: "Object Name",
  required: !0
}), locationProperty = import_pieces_framework2.Property.StaticDropdown({
  displayName: "Location",
  required: !1,
  options: {
    options: [
      // Multi-region
      { label: "US (Multi-region)", value: "US" },
      { label: "EU (Multi-region)", value: "EU" },
      { label: "ASIA (Multi-region)", value: "ASIA" },
      // US regions
      { label: "us-central1 (Iowa)", value: "us-central1" },
      { label: "us-east1 (South Carolina)", value: "us-east1" },
      { label: "us-east4 (Northern Virginia)", value: "us-east4" },
      { label: "us-west1 (Oregon)", value: "us-west1" },
      { label: "us-west2 (Los Angeles)", value: "us-west2" },
      { label: "us-west3 (Salt Lake City)", value: "us-west3" },
      { label: "us-west4 (Las Vegas)", value: "us-west4" },
      { label: "us-south1 (Dallas)", value: "us-south1" },
      // Europe regions
      { label: "europe-central2 (Warsaw)", value: "europe-central2" },
      { label: "europe-north1 (Finland)", value: "europe-north1" },
      { label: "europe-southwest1 (Madrid)", value: "europe-southwest1" },
      { label: "europe-west1 (Belgium)", value: "europe-west1" },
      { label: "europe-west2 (London)", value: "europe-west2" },
      { label: "europe-west3 (Frankfurt)", value: "europe-west3" },
      { label: "europe-west4 (Netherlands)", value: "europe-west4" },
      { label: "europe-west6 (Zurich)", value: "europe-west6" },
      { label: "europe-west8 (Milan)", value: "europe-west8" },
      { label: "europe-west9 (Paris)", value: "europe-west9" },
      // Asia regions
      { label: "asia-east1 (Taiwan)", value: "asia-east1" },
      { label: "asia-east2 (Hong Kong)", value: "asia-east2" },
      { label: "asia-northeast1 (Tokyo)", value: "asia-northeast1" },
      { label: "asia-northeast2 (Osaka)", value: "asia-northeast2" },
      { label: "asia-northeast3 (Seoul)", value: "asia-northeast3" },
      { label: "asia-south1 (Mumbai)", value: "asia-south1" },
      { label: "asia-south2 (Delhi)", value: "asia-south2" },
      { label: "asia-southeast1 (Singapore)", value: "asia-southeast1" },
      { label: "asia-southeast2 (Jakarta)", value: "asia-southeast2" },
      // Other regions
      { label: "australia-southeast1 (Sydney)", value: "australia-southeast1" },
      { label: "australia-southeast2 (Melbourne)", value: "australia-southeast2" },
      { label: "northamerica-northeast1 (Montreal)", value: "northamerica-northeast1" },
      { label: "northamerica-northeast2 (Toronto)", value: "northamerica-northeast2" },
      { label: "southamerica-east1 (S\xE3o Paulo)", value: "southamerica-east1" },
      { label: "southamerica-west1 (Santiago)", value: "southamerica-west1" }
    ]
  }
}), storageClassProperty = import_pieces_framework2.Property.StaticDropdown({
  displayName: "Storage Class",
  required: !1,
  options: {
    options: [
      { label: "Standard", value: "STANDARD" },
      { label: "Nearline", value: "NEARLINE" },
      { label: "Coldline", value: "COLDLINE" },
      { label: "Archive", value: "ARCHIVE" },
      { label: "Multi-regional", value: "MULTI_REGIONAL" },
      { label: "Regional", value: "REGIONAL" },
      { label: "Durable Reduced Availability", value: "DURABLE_REDUCED_AVAILABILITY" }
    ]
  }
}), aclEntityProperty = import_pieces_framework2.Property.ShortText({
  displayName: "Entity",
  description: "The entity to grant access to. Must include the entity type prefix. Format: user-emailAddress, group-groupId, group-emailAddress, domain-domainName, project-team-projectId, allUsers, or allAuthenticatedUsers. Examples: user-liz@example.com, group-mygroup@googlegroups.com, domain-example.com, allUsers",
  required: !0
}), aclRoleProperty = import_pieces_framework2.Property.StaticDropdown({
  displayName: "Role",
  required: !0,
  options: {
    options: [
      { label: "Reader", value: "READER" },
      { label: "Writer", value: "WRITER" },
      { label: "Owner", value: "OWNER" }
    ]
  }
}), bucketAclRoleProperty = import_pieces_framework2.Property.StaticDropdown({
  displayName: "Role",
  required: !0,
  options: {
    options: [
      { label: "Reader", value: "READER" },
      { label: "Writer", value: "WRITER" },
      { label: "Owner", value: "OWNER" }
    ]
  }
}), objectAclRoleProperty = import_pieces_framework2.Property.StaticDropdown({
  displayName: "Role",
  required: !0,
  options: {
    options: [
      { label: "Reader", value: "READER" },
      { label: "Owner", value: "OWNER" }
    ]
  }
});

// ../ap/packages/pieces/community/google-cloud-storage/src/lib/actions/create-bucket.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), createBucket = (0, import_pieces_framework3.createAction)({
  auth: googleCloudStorageAuth,
  name: "create_bucket",
  displayName: "Create Bucket",
  description: "Create a new bucket in a specified location/configuration. Perfect for automating storage provisioning for new projects.",
  audience: "both",
  aiMetadata: { description: "Creates a new Cloud Storage bucket in the given project, with optional location, storage class, versioning, uniform bucket-level access, and labels. Use to provision storage before uploading objects. Not idempotent: bucket names are globally unique, so re-running with the same name fails. Requires a project ID and a globally-unique bucket name.", idempotent: !1 },
  props: {
    projectId: projectIdProperty,
    name: bucketNameProperty,
    location: locationProperty,
    storageClass: storageClassProperty,
    versioning: import_pieces_framework3.Property.Checkbox({
      displayName: "Enable Versioning",
      required: !1
    }),
    uniformBucketLevelAccess: import_pieces_framework3.Property.Checkbox({
      displayName: "Uniform Bucket Level Access",
      required: !1
    }),
    labels: import_pieces_framework3.Property.Object({
      displayName: "Labels",
      description: "Key-value pairs for bucket labels",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c;
    let { projectId, name, location, storageClass, versioning, uniformBucketLevelAccess, labels } = context.propsValue, auth = context.auth, bucketConfig = {
      name,
      location: location || "US",
      storageClass: storageClass || "STANDARD"
    };
    versioning && (bucketConfig.versioning = { enabled: !0 }), uniformBucketLevelAccess !== void 0 && (bucketConfig.iamConfiguration = {
      uniformBucketLevelAccess: {
        enabled: uniformBucketLevelAccess
      }
    }), labels && (bucketConfig.labels = labels);
    try {
      return await gcsCommon.makeRequest(import_pieces_common3.HttpMethod.POST, `/b?project=${projectId}`, auth.access_token, bucketConfig);
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 409 ? new Error(`Bucket "${name}" already exists. Bucket names must be globally unique.`) : ((_b = error.response) == null ? void 0 : _b.status) === 403 ? new Error("Access denied. Check your permissions for the selected project.") : ((_c = error.response) == null ? void 0 : _c.status) === 400 ? new Error("Invalid bucket configuration. Check bucket name format and project settings.") : new Error(`Failed to create bucket: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/google-cloud-storage/src/lib/actions/delete-empty-bucket.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common"), deleteEmptyBucket = (0, import_pieces_framework4.createAction)({
  auth: googleCloudStorageAuth,
  name: "delete_empty_bucket",
  displayName: "Delete Empty Bucket",
  description: "Clean up unused buckets by deleting them if they contain no live objects.",
  audience: "both",
  aiMetadata: { description: "Permanently deletes a Cloud Storage bucket. Use to clean up an unused bucket. The bucket must be empty (no live or noncurrent objects) or the call fails, so delete its objects first. Not idempotent: the first call removes the bucket and a repeat fails because it no longer exists.", idempotent: !1 },
  props: {
    projectId: projectIdProperty,
    bucket: bucketDropdown
  },
  async run(context) {
    var _a, _b, _c;
    let { bucket } = context.propsValue, auth = context.auth;
    try {
      return await gcsCommon.makeRequest(import_pieces_common4.HttpMethod.DELETE, `/b/${bucket}`, auth.access_token), {
        success: !0,
        message: `Bucket "${bucket}" deleted successfully`
      };
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 409 ? new Error(`Bucket "${bucket}" contains live or noncurrent objects and cannot be deleted. Remove all objects first.`) : ((_b = error.response) == null ? void 0 : _b.status) === 403 ? new Error("Access denied. You need storage.buckets.delete permission to delete this bucket.") : ((_c = error.response) == null ? void 0 : _c.status) === 404 ? new Error(`Bucket "${bucket}" not found. It may have already been deleted.`) : new Error(`Failed to delete bucket: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/google-cloud-storage/src/lib/actions/clone-object.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common"), cloneObject = (0, import_pieces_framework5.createAction)({
  auth: googleCloudStorageAuth,
  name: "clone_object",
  displayName: "Clone Object",
  description: "Copy / clone an object (file) to a new location (same or different bucket), optionally overriding metadata. Perfect for duplicating files as backup or version.",
  audience: "both",
  aiMetadata: { description: "Copies an existing object to a destination bucket/name (same or different bucket), optionally overriding metadata such as contentType or cacheControl. Use to duplicate a file for backups or to move data between buckets. Not idempotent: it writes a new destination object on each call, creating a fresh generation. Requires the source bucket/object and a destination bucket and object name.", idempotent: !1 },
  props: {
    projectId: projectIdProperty,
    sourceBucket: bucketDropdown,
    sourceObject: objectDropdown("sourceBucket"),
    destBucket: bucketDropdown,
    destObject: import_pieces_framework5.Property.ShortText({
      displayName: "Destination Object Name",
      description: "Name for the copied object (must be valid object name)",
      required: !0
    }),
    metadataOverrides: import_pieces_framework5.Property.Object({
      displayName: "Metadata Overrides",
      description: "Optional metadata and properties to override in the copied object (contentType, cacheControl, contentDisposition, etc.)",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e;
    let { sourceBucket, sourceObject, destBucket, destObject, metadataOverrides } = context.propsValue, auth = context.auth, body = metadataOverrides || void 0;
    try {
      let response = await gcsCommon.makeRequest(
        import_pieces_common5.HttpMethod.POST,
        `/b/${sourceBucket}/o/${encodeURIComponent(sourceObject)}/copyTo/b/${destBucket}/o/${encodeURIComponent(destObject)}`,
        auth.access_token,
        body
      );
      return {
        success: !0,
        source: {
          bucket: sourceBucket,
          object: sourceObject
        },
        destination: {
          bucket: destBucket,
          object: destObject
        },
        object: response
      };
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 404 ? new Error(`Source object "${sourceObject}" not found in bucket "${sourceBucket}".`) : ((_b = error.response) == null ? void 0 : _b.status) === 403 ? new Error("Access denied. Check permissions for source and destination buckets.") : ((_c = error.response) == null ? void 0 : _c.status) === 412 ? new Error("Precondition failed. Source object may have been modified.") : ((_d = error.response) == null ? void 0 : _d.status) === 409 ? new Error(`Destination object "${destObject}" already exists in bucket "${destBucket}".`) : ((_e = error.response) == null ? void 0 : _e.status) === 400 ? new Error("Invalid request. Check object names and bucket configurations.") : new Error(`Failed to clone object: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/google-cloud-storage/src/lib/actions/delete-object.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common"), deleteObject = (0, import_pieces_framework6.createAction)({
  auth: googleCloudStorageAuth,
  name: "delete_object",
  displayName: "Delete Object",
  description: "Permanently delete a specific object. Perfect for removing obsolete files.",
  audience: "both",
  aiMetadata: { description: "Permanently deletes an object from a bucket, optionally a specific generation/revision for versioned objects. Use to remove an obsolete or unwanted file. Not idempotent: the first call removes the object and a repeat fails because it no longer exists. Requires the bucket and object name.", idempotent: !1 },
  props: {
    projectId: projectIdProperty,
    bucket: bucketDropdown,
    object: objectDropdown("bucket"),
    generation: import_pieces_framework6.Property.Number({
      displayName: "Generation",
      description: "Optional generation number for versioned objects (permanently deletes a specific revision)",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d;
    let { bucket, object, generation } = context.propsValue, auth = context.auth, path = `/b/${bucket}/o/${encodeURIComponent(object)}`;
    generation && (path += `?generation=${generation}`);
    try {
      return await gcsCommon.makeRequest(import_pieces_common6.HttpMethod.DELETE, path, auth.access_token), {
        success: !0,
        bucket,
        object,
        generation: generation || "latest",
        message: `Object "${object}" deleted successfully from bucket "${bucket}"`
      };
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 404 ? new Error(`Object "${object}" not found in bucket "${bucket}". It may have already been deleted.`) : ((_b = error.response) == null ? void 0 : _b.status) === 403 ? new Error("Access denied. You need storage.objects.delete permission to delete this object.") : ((_c = error.response) == null ? void 0 : _c.status) === 412 ? new Error("Precondition failed. Object may have been modified or deleted by another process.") : ((_d = error.response) == null ? void 0 : _d.status) === 400 ? new Error("Invalid request. Check object name and generation parameters.") : new Error(`Failed to delete object: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/google-cloud-storage/src/lib/actions/search-objects.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common"), searchObjects = (0, import_pieces_framework7.createAction)({
  auth: googleCloudStorageAuth,
  name: "search_objects",
  displayName: "Search Objects",
  description: "Search objects by criteria. Perfect for finding files in your bucket.",
  audience: "both",
  aiMetadata: { description: "Lists objects in a bucket, optionally narrowed by name prefix, glob pattern, delimiter (for hierarchical/folder listing), or object versions; with no filters it returns all objects. Use to find files or enumerate bucket contents, and pass the returned page token to paginate. Idempotent read that does not modify storage. Requires the bucket name.", idempotent: !0 },
  props: {
    projectId: projectIdProperty,
    bucket: bucketDropdown,
    prefix: import_pieces_framework7.Property.ShortText({
      displayName: "Prefix",
      description: "Filter objects whose names begin with this prefix",
      required: !1
    }),
    matchGlob: import_pieces_framework7.Property.ShortText({
      displayName: "Glob Pattern",
      description: 'Glob pattern to filter results (e.g., "folder/*", "backup-*.txt")',
      required: !1
    }),
    delimiter: import_pieces_framework7.Property.ShortText({
      displayName: "Delimiter",
      description: 'Delimiter for hierarchical listing (commonly "/")',
      required: !1
    }),
    includeFoldersAsPrefixes: import_pieces_framework7.Property.Checkbox({
      displayName: "Include Folders",
      description: "Include empty folders and managed folders in results",
      required: !1
    }),
    versions: import_pieces_framework7.Property.Checkbox({
      displayName: "Include Versions",
      description: "List all versions of objects as distinct results",
      required: !1
    }),
    pageToken: import_pieces_framework7.Property.ShortText({
      displayName: "Page Token",
      description: "Token for pagination (from previous response)",
      required: !1
    }),
    maxResults: import_pieces_framework7.Property.Number({
      displayName: "Max Results",
      description: "Maximum number of objects to return (recommended: \u22641000)",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d;
    let {
      bucket,
      prefix,
      matchGlob,
      delimiter,
      includeFoldersAsPrefixes,
      versions,
      pageToken,
      maxResults
    } = context.propsValue, auth = context.auth, params = new URLSearchParams();
    prefix && params.append("prefix", prefix), matchGlob && params.append("matchGlob", matchGlob), delimiter && params.append("delimiter", delimiter), includeFoldersAsPrefixes && params.append("includeFoldersAsPrefixes", "true"), versions && params.append("versions", "true"), pageToken && params.append("pageToken", pageToken), maxResults && params.append("maxResults", maxResults.toString());
    let path = `/b/${bucket}/o?${params.toString()}`;
    try {
      let response = await gcsCommon.makeRequest(import_pieces_common7.HttpMethod.GET, path, auth.access_token);
      return {
        success: !0,
        bucket,
        items: response.items || [],
        nextPageToken: response.nextPageToken,
        prefixes: response.prefixes || [],
        totalItems: ((_a = response.items) == null ? void 0 : _a.length) || 0
      };
    } catch (error) {
      throw ((_b = error.response) == null ? void 0 : _b.status) === 403 ? new Error("Access denied. You need storage.objects.list permission to search objects in this bucket.") : ((_c = error.response) == null ? void 0 : _c.status) === 404 ? new Error(`Bucket "${bucket}" not found.`) : ((_d = error.response) == null ? void 0 : _d.status) === 400 ? new Error("Invalid search parameters. Check your prefix, glob pattern, or other filters.") : new Error(`Failed to search objects: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/google-cloud-storage/src/lib/actions/search-buckets.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var import_pieces_common8 = require("@activepieces/pieces-common"), searchBuckets = (0, import_pieces_framework8.createAction)({
  auth: googleCloudStorageAuth,
  name: "search_buckets",
  displayName: "Search Buckets",
  description: "Search buckets by name. Perfect for finding buckets in your project.",
  audience: "both",
  aiMetadata: { description: "Lists buckets in a project, optionally filtered by name prefix, including soft-deleted versions, and choosing whether ACLs are returned; with no prefix it returns all buckets. Use to find a bucket or enumerate a project's buckets, and pass the returned page token to paginate. Idempotent read that does not modify storage. Requires the project ID.", idempotent: !0 },
  props: {
    projectId: projectIdProperty,
    prefix: import_pieces_framework8.Property.ShortText({
      displayName: "Name Prefix",
      description: "Filter buckets whose names begin with this prefix",
      required: !1
    }),
    includeSoftDeleted: import_pieces_framework8.Property.Checkbox({
      displayName: "Include Soft-Deleted",
      description: "Include soft-deleted bucket versions in results",
      required: !1
    }),
    projection: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Projection",
      description: "Set of properties to return",
      required: !1,
      options: {
        options: [
          { label: "No ACLs (faster)", value: "noAcl" },
          { label: "Full (includes ACLs)", value: "full" }
        ]
      }
    }),
    pageToken: import_pieces_framework8.Property.ShortText({
      displayName: "Page Token",
      description: "Token for pagination (from previous response)",
      required: !1
    }),
    maxResults: import_pieces_framework8.Property.Number({
      displayName: "Max Results",
      description: "Maximum number of buckets to return",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d;
    let {
      projectId,
      prefix,
      includeSoftDeleted,
      projection,
      pageToken,
      maxResults
    } = context.propsValue, auth = context.auth, params = new URLSearchParams();
    params.append("project", projectId), prefix && params.append("prefix", prefix), includeSoftDeleted && params.append("softDeleted", "true"), projection && params.append("projection", projection), pageToken && params.append("pageToken", pageToken), maxResults && params.append("maxResults", maxResults.toString());
    let path = `/b?${params.toString()}`;
    try {
      let response = await gcsCommon.makeRequest(import_pieces_common8.HttpMethod.GET, path, auth.access_token);
      return {
        success: !0,
        projectId,
        items: response.items || [],
        nextPageToken: response.nextPageToken,
        totalBuckets: ((_a = response.items) == null ? void 0 : _a.length) || 0
      };
    } catch (error) {
      throw ((_b = error.response) == null ? void 0 : _b.status) === 403 ? new Error("Access denied. You need storage.buckets.list permission to search buckets in this project.") : ((_c = error.response) == null ? void 0 : _c.status) === 404 ? new Error(`Project "${projectId}" not found or you don't have access to it.`) : ((_d = error.response) == null ? void 0 : _d.status) === 400 ? new Error("Invalid search parameters. Check your project ID and other filters.") : new Error(`Failed to search buckets: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/google-cloud-storage/src/lib/actions/create-object-acl.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var import_pieces_common9 = require("@activepieces/pieces-common"), createObjectAcl = (0, import_pieces_framework9.createAction)({
  auth: googleCloudStorageAuth,
  name: "create_object_acl",
  displayName: "Create Object ACL",
  description: "Add an ACL entry to an object (grant a permission). Perfect for granting read/write access to a user or group.",
  audience: "both",
  aiMetadata: { description: "Grants an entity (user, group, domain, or special scope) a role on a single object's access-control list, optionally targeting a specific generation. Use to give read or owner access to one file. Not idempotent: adding an entry that already exists fails. Only works on buckets using fine-grained ACLs, not uniform bucket-level access; requires bucket, object, entity, and role.", idempotent: !1 },
  props: {
    projectId: projectIdProperty,
    bucket: bucketDropdown,
    object: objectDropdown("bucket"),
    entity: aclEntityProperty,
    role: objectAclRoleProperty,
    generation: import_pieces_framework9.Property.Number({
      displayName: "Generation",
      description: "Optional generation number for versioned objects",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d;
    let { bucket, object, entity, role, generation } = context.propsValue, auth = context.auth, aclEntry = {
      entity,
      role
    }, path = `/b/${bucket}/o/${encodeURIComponent(object)}/acl`;
    generation && (path += `?generation=${generation}`);
    try {
      let response = await gcsCommon.makeRequest(
        import_pieces_common9.HttpMethod.POST,
        path,
        auth.access_token,
        aclEntry
      );
      return {
        success: !0,
        bucket,
        object,
        generation: generation || "latest",
        acl: response
      };
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 400 ? new Error("Bad request. This bucket may have uniform bucket-level access enabled, which doesn't support object ACLs. Use IAM policies instead.") : ((_b = error.response) == null ? void 0 : _b.status) === 403 ? new Error("Access denied. You need storage.objects.setIamPolicy permission or OWNER ACL permission on the object.") : ((_c = error.response) == null ? void 0 : _c.status) === 404 ? new Error(`Object "${object}" not found in bucket "${bucket}".`) : ((_d = error.response) == null ? void 0 : _d.status) === 409 ? new Error(`ACL entry for entity "${entity}" already exists on this object.`) : new Error(`Failed to create object ACL: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/google-cloud-storage/src/lib/actions/delete-object-acl.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var import_pieces_common10 = require("@activepieces/pieces-common"), deleteObjectAcl = (0, import_pieces_framework10.createAction)({
  auth: googleCloudStorageAuth,
  name: "delete_object_acl",
  displayName: "Delete Object ACL",
  description: "Remove an ACL entry from an object. Perfect for revoking access for a user.",
  audience: "both",
  aiMetadata: { description: "Removes an entity's entry from a single object's access-control list, optionally targeting a specific generation. Use to revoke a user or group's access to one file. Not idempotent: the first call removes the entry and a repeat fails because it no longer exists. Only works on buckets using fine-grained ACLs, not uniform bucket-level access; requires bucket, object, and entity.", idempotent: !1 },
  props: {
    projectId: projectIdProperty,
    bucket: bucketDropdown,
    object: objectDropdown("bucket"),
    entity: aclEntityProperty,
    generation: import_pieces_framework10.Property.Number({
      displayName: "Generation",
      description: "Optional generation number for versioned objects",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g;
    let { bucket, object, entity, generation } = context.propsValue, auth = context.auth, path = `/b/${bucket}/o/${encodeURIComponent(object)}/acl/${encodeURIComponent(entity)}`;
    generation && (path += `?generation=${generation}`);
    try {
      return await gcsCommon.makeRequest(import_pieces_common10.HttpMethod.DELETE, path, auth.access_token), {
        success: !0,
        bucket,
        object,
        entity,
        generation: generation || "latest",
        message: `ACL entry for entity "${entity}" removed successfully from object "${object}"`
      };
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 400 ? entity.includes("user-") && entity.includes("@") ? new Error("Cannot remove OWNER access from the object owner. The only way to remove owner access is to delete or replace the object.") : new Error("Bad request. This bucket may have uniform bucket-level access enabled, which doesn't support object ACLs. Use IAM policies instead.") : ((_b = error.response) == null ? void 0 : _b.status) === 403 ? new Error("Access denied. You need storage.objects.setIamPolicy permission or OWNER ACL permission on the object.") : ((_c = error.response) == null ? void 0 : _c.status) === 404 ? (_g = (_f = (_e = (_d = error.response) == null ? void 0 : _d.data) == null ? void 0 : _e.error) == null ? void 0 : _f.message) != null && _g.includes("ACL") ? new Error(`ACL entry for entity "${entity}" not found on object "${object}".`) : new Error(`Object "${object}" not found in bucket "${bucket}".`) : new Error(`Failed to delete object ACL: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/google-cloud-storage/src/lib/actions/create-bucket-acl.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var import_pieces_common11 = require("@activepieces/pieces-common"), createBucketAcl = (0, import_pieces_framework11.createAction)({
  auth: googleCloudStorageAuth,
  name: "create_bucket_acl",
  displayName: "Create Bucket ACL",
  description: "Add an ACL entry at bucket level. Perfect for granting permission to manage the bucket.",
  audience: "both",
  aiMetadata: { description: "Grants an entity (user, group, domain, or special scope) a role on a bucket's own access-control list. Use to let someone read or manage the bucket itself. Not idempotent: adding an entry that already exists fails. Only works on buckets using fine-grained ACLs, not uniform bucket-level access; requires bucket, entity, and role.", idempotent: !1 },
  props: {
    projectId: projectIdProperty,
    bucket: bucketDropdown,
    entity: aclEntityProperty,
    role: bucketAclRoleProperty
  },
  async run(context) {
    var _a, _b, _c, _d;
    let { bucket, entity, role } = context.propsValue, auth = context.auth, aclEntry = {
      entity,
      role
    };
    try {
      let response = await gcsCommon.makeRequest(
        import_pieces_common11.HttpMethod.POST,
        `/b/${bucket}/acl`,
        auth.access_token,
        aclEntry
      );
      return {
        success: !0,
        bucket,
        acl: response
      };
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 400 ? new Error("Bad request. This bucket may have uniform bucket-level access enabled, which doesn't support bucket ACLs. Use IAM policies instead.") : ((_b = error.response) == null ? void 0 : _b.status) === 403 ? new Error("Access denied. You need storage.buckets.update permission to modify bucket ACLs.") : ((_c = error.response) == null ? void 0 : _c.status) === 404 ? new Error(`Bucket "${bucket}" not found.`) : ((_d = error.response) == null ? void 0 : _d.status) === 409 ? new Error(`ACL entry for entity "${entity}" already exists on bucket "${bucket}".`) : new Error(`Failed to create bucket ACL: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/google-cloud-storage/src/lib/actions/delete-bucket-acl.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var import_pieces_common12 = require("@activepieces/pieces-common"), deleteBucketAcl = (0, import_pieces_framework12.createAction)({
  auth: googleCloudStorageAuth,
  name: "delete_bucket_acl",
  displayName: "Delete Bucket ACL",
  description: "Remove an ACL entry from a bucket. Perfect for revoking access for a user or group.",
  audience: "both",
  aiMetadata: { description: "Removes an entity's entry from a bucket's own access-control list. Use to revoke a user or group's access to the bucket. Not idempotent: the first call removes the entry and a repeat fails because it no longer exists. Only works on buckets using fine-grained ACLs, not uniform bucket-level access; requires bucket and entity.", idempotent: !1 },
  props: {
    projectId: projectIdProperty,
    bucket: bucketDropdown,
    entity: aclEntityProperty
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g;
    let { bucket, entity } = context.propsValue, auth = context.auth;
    try {
      return await gcsCommon.makeRequest(
        import_pieces_common12.HttpMethod.DELETE,
        `/b/${bucket}/acl/${encodeURIComponent(entity)}`,
        auth.access_token
      ), {
        success: !0,
        bucket,
        entity,
        message: `ACL entry for entity "${entity}" removed successfully from bucket "${bucket}"`
      };
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 400 ? new Error("Bad request. This bucket may have uniform bucket-level access enabled, which doesn't support bucket ACLs. Use IAM policies instead.") : ((_b = error.response) == null ? void 0 : _b.status) === 403 ? new Error("Access denied. You need storage.buckets.update permission to modify bucket ACLs.") : ((_c = error.response) == null ? void 0 : _c.status) === 404 ? (_g = (_f = (_e = (_d = error.response) == null ? void 0 : _d.data) == null ? void 0 : _e.error) == null ? void 0 : _f.message) != null && _g.includes("ACL") ? new Error(`ACL entry for entity "${entity}" not found on bucket "${bucket}".`) : new Error(`Bucket "${bucket}" not found.`) : new Error(`Failed to delete bucket ACL: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/google-cloud-storage/src/lib/actions/create-bucket-default-object-acl.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var import_pieces_common13 = require("@activepieces/pieces-common"), createBucketDefaultObjectAcl = (0, import_pieces_framework13.createAction)({
  auth: googleCloudStorageAuth,
  name: "create_bucket_default_object_acl",
  displayName: "Create Bucket Default Object ACL",
  description: "Set default ACLs for new objects added to a bucket. Perfect for automatically assigning permissions to new uploads.",
  audience: "both",
  aiMetadata: { description: "Adds an entry to a bucket's default object ACL, which is applied automatically to objects later uploaded without their own ACL. Use to pre-set permissions for future uploads. Not idempotent: adding an entry that already exists fails, and it does not affect existing objects. Only works on buckets using fine-grained ACLs, not uniform bucket-level access; requires bucket, entity, and role.", idempotent: !1 },
  props: {
    projectId: projectIdProperty,
    bucket: bucketDropdown,
    entity: aclEntityProperty,
    role: objectAclRoleProperty
  },
  async run(context) {
    var _a, _b, _c, _d;
    let { bucket, entity, role } = context.propsValue, auth = context.auth, aclEntry = {
      entity,
      role
    };
    try {
      let response = await gcsCommon.makeRequest(
        import_pieces_common13.HttpMethod.POST,
        `/b/${bucket}/defaultObjectAcl`,
        auth.access_token,
        aclEntry
      );
      return {
        success: !0,
        bucket,
        acl: response
      };
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 400 ? new Error("Bad request. This bucket may have uniform bucket-level access enabled, which doesn't support default object ACLs. Use IAM policies instead.") : ((_b = error.response) == null ? void 0 : _b.status) === 403 ? new Error("Access denied. You need storage.buckets.update permission to modify bucket default object ACLs.") : ((_c = error.response) == null ? void 0 : _c.status) === 404 ? new Error(`Bucket "${bucket}" not found.`) : ((_d = error.response) == null ? void 0 : _d.status) === 409 ? new Error(`Default object ACL entry for entity "${entity}" already exists on bucket "${bucket}".`) : new Error(`Failed to create bucket default object ACL: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/google-cloud-storage/src/lib/actions/delete-bucket-default-object-acl.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var import_pieces_common14 = require("@activepieces/pieces-common"), deleteBucketDefaultObjectAcl = (0, import_pieces_framework14.createAction)({
  auth: googleCloudStorageAuth,
  name: "delete_bucket_default_object_acl",
  displayName: "Delete Bucket Default Object ACL",
  description: "Remove default ACL settings from a bucket. Perfect for reverting to default behavior.",
  audience: "both",
  aiMetadata: { description: "Removes an entity's entry from a bucket's default object ACL, so it no longer applies to future uploads. Use to revert a default-permission rule for new objects. Not idempotent: the first call removes the entry and a repeat fails because it no longer exists, and it does not affect already-uploaded objects. Only works on buckets using fine-grained ACLs, not uniform bucket-level access; requires bucket and entity.", idempotent: !1 },
  props: {
    projectId: projectIdProperty,
    bucket: bucketDropdown,
    entity: aclEntityProperty
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g;
    let { bucket, entity } = context.propsValue, auth = context.auth;
    try {
      return await gcsCommon.makeRequest(
        import_pieces_common14.HttpMethod.DELETE,
        `/b/${bucket}/defaultObjectAcl/${encodeURIComponent(entity)}`,
        auth.access_token
      ), {
        success: !0,
        bucket,
        entity,
        message: `Default object ACL entry for entity "${entity}" removed successfully from bucket "${bucket}"`
      };
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 400 ? new Error("Bad request. This bucket may have uniform bucket-level access enabled, which doesn't support default object ACLs. Use IAM policies instead.") : ((_b = error.response) == null ? void 0 : _b.status) === 403 ? new Error("Access denied. You need storage.buckets.update permission to modify bucket default object ACLs.") : ((_c = error.response) == null ? void 0 : _c.status) === 404 ? (_g = (_f = (_e = (_d = error.response) == null ? void 0 : _d.data) == null ? void 0 : _e.error) == null ? void 0 : _f.message) != null && _g.includes("ACL") ? new Error(`Default object ACL entry for entity "${entity}" not found on bucket "${bucket}".`) : new Error(`Bucket "${bucket}" not found.`) : new Error(`Failed to delete bucket default object ACL: ${error.message || "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/google-cloud-storage/src/lib/triggers/new-object-created.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var import_pieces_common15 = require("@activepieces/pieces-common"), newObjectCreated = (0, import_pieces_framework15.createTrigger)({
  auth: googleCloudStorageAuth,
  name: "new_object_created",
  displayName: "New Object Created",
  description: "Triggers when a new object is created in a bucket",
  aiMetadata: {
    description: "Fires when a new object is finalized (fully written) in the configured bucket, optionally limited to objects whose name starts with a given prefix. Represents a newly created/uploaded file; delivered via a Cloud Pub/Sub push notification (OBJECT_FINALIZE event)."
  },
  props: {
    projectId: projectIdProperty,
    bucket: bucketDropdown,
    prefix: import_pieces_framework15.Property.ShortText({
      displayName: "Prefix Filter",
      description: "Only trigger for objects with this prefix",
      required: !1
    })
  },
  type: import_pieces_framework15.TriggerStrategy.WEBHOOK,
  sampleData: {
    kind: "storage#object",
    id: "example-bucket/example-object/1234567890",
    name: "example-object.txt",
    bucket: "example-bucket",
    generation: "1234567890",
    contentType: "text/plain",
    timeCreated: "2023-01-01T00:00:00.000Z",
    updated: "2023-01-01T00:00:00.000Z",
    size: "1024"
  },
  onEnable: async (context) => {
    let { projectId, bucket, prefix } = context.propsValue, auth = context.auth, triggerId = `ap-gcs-${bucket}-${Date.now()}`, topicName = `projects/${projectId}/topics/${triggerId}`, subscriptionName = `projects/${projectId}/subscriptions/${triggerId}`;
    try {
      await gcsCommon.makePubSubRequest(
        import_pieces_common15.HttpMethod.PUT,
        `/projects/${projectId}/topics/${triggerId}`,
        auth.access_token
      );
      let notificationConfig = {
        topic: topicName,
        payload_format: "JSON_API_V1",
        event_types: ["OBJECT_FINALIZE"]
      };
      prefix && (notificationConfig.object_name_prefix = prefix);
      let notificationResponse = await gcsCommon.makeGCSRequest(
        import_pieces_common15.HttpMethod.POST,
        `/b/${bucket}/notificationConfigs`,
        auth.access_token,
        notificationConfig
      ), subscriptionConfig = {
        topic: topicName,
        pushConfig: {
          pushEndpoint: context.webhookUrl
        },
        ackDeadlineSeconds: 60
      };
      await gcsCommon.makePubSubRequest(
        import_pieces_common15.HttpMethod.PUT,
        `/projects/${projectId}/subscriptions/${triggerId}`,
        auth.access_token,
        subscriptionConfig
      ), await context.store.put("_trigger", {
        topicName: triggerId,
        subscriptionName: triggerId,
        notificationId: notificationResponse.id
      });
    } catch (error) {
      try {
        await gcsCommon.makePubSubRequest(
          import_pieces_common15.HttpMethod.DELETE,
          `/projects/${projectId}/subscriptions/${triggerId}`,
          auth.access_token
        );
      } catch (e) {
      }
      try {
        await gcsCommon.makePubSubRequest(
          import_pieces_common15.HttpMethod.DELETE,
          `/projects/${projectId}/topics/${triggerId}`,
          auth.access_token
        );
      } catch (e) {
      }
      throw new Error(`Failed to setup Pub/Sub notifications: ${(error == null ? void 0 : error.message) || "Unknown error"}`);
    }
  },
  onDisable: async (context) => {
    let triggerData = await context.store.get("_trigger");
    if (!triggerData) return;
    let { projectId } = context.propsValue, { bucket } = context.propsValue, auth = context.auth;
    try {
      await gcsCommon.makePubSubRequest(
        import_pieces_common15.HttpMethod.DELETE,
        `/projects/${projectId}/subscriptions/${triggerData.subscriptionName}`,
        auth.access_token
      );
    } catch (e) {
    }
    try {
      await gcsCommon.makeGCSRequest(
        import_pieces_common15.HttpMethod.DELETE,
        `/b/${bucket}/notificationConfigs/${triggerData.notificationId}`,
        auth.access_token
      );
    } catch (e) {
    }
    try {
      await gcsCommon.makePubSubRequest(
        import_pieces_common15.HttpMethod.DELETE,
        `/projects/${projectId}/topics/${triggerData.topicName}`,
        auth.access_token
      );
    } catch (e) {
    }
  },
  run: async (context) => {
    var _a;
    let payload = context.payload.body;
    return (_a = payload == null ? void 0 : payload.message) != null && _a.data ? [JSON.parse(
      Buffer.from(payload.message.data, "base64").toString()
    )] : [];
  }
});

// ../ap/packages/pieces/community/google-cloud-storage/src/lib/triggers/object-updated.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var import_pieces_common16 = require("@activepieces/pieces-common"), objectUpdated = (0, import_pieces_framework16.createTrigger)({
  auth: googleCloudStorageAuth,
  name: "object_updated",
  displayName: "Object Updated",
  description: "Triggers when an existing object is updated in a bucket",
  aiMetadata: {
    description: "Fires when the metadata of an existing object in the configured bucket is updated, optionally limited to objects whose name starts with a given prefix. Represents a change to an existing file's metadata; delivered via a Cloud Pub/Sub push notification (OBJECT_METADATA_UPDATE event)."
  },
  props: {
    projectId: projectIdProperty,
    bucket: bucketDropdown,
    prefix: import_pieces_framework16.Property.ShortText({
      displayName: "Prefix Filter",
      description: "Only trigger for objects with this prefix",
      required: !1
    })
  },
  type: import_pieces_framework16.TriggerStrategy.WEBHOOK,
  sampleData: {
    kind: "storage#object",
    id: "example-bucket/example-object/1234567890",
    name: "example-object.txt",
    bucket: "example-bucket",
    generation: "1234567890",
    contentType: "text/plain",
    timeCreated: "2023-01-01T00:00:00.000Z",
    updated: "2023-01-01T01:00:00.000Z",
    size: "2048"
  },
  onEnable: async (context) => {
    let { projectId, bucket, prefix } = context.propsValue, auth = context.auth, triggerId = `ap-gcs-update-${bucket}-${Date.now()}`, topicName = `projects/${projectId}/topics/${triggerId}`, subscriptionName = `projects/${projectId}/subscriptions/${triggerId}`;
    try {
      await gcsCommon.makePubSubRequest(
        import_pieces_common16.HttpMethod.PUT,
        `/projects/${projectId}/topics/${triggerId}`,
        auth.access_token
      );
      let notificationConfig = {
        topic: topicName,
        payload_format: "JSON_API_V1",
        event_types: ["OBJECT_METADATA_UPDATE"]
      };
      prefix && (notificationConfig.object_name_prefix = prefix);
      let notificationResponse = await gcsCommon.makeGCSRequest(
        import_pieces_common16.HttpMethod.POST,
        `/b/${bucket}/notificationConfigs`,
        auth.access_token,
        notificationConfig
      ), subscriptionConfig = {
        topic: topicName,
        pushConfig: {
          pushEndpoint: context.webhookUrl
        },
        ackDeadlineSeconds: 60
      };
      await gcsCommon.makePubSubRequest(
        import_pieces_common16.HttpMethod.PUT,
        `/projects/${projectId}/subscriptions/${triggerId}`,
        auth.access_token,
        subscriptionConfig
      ), await context.store.put("_trigger", {
        topicName: triggerId,
        subscriptionName: triggerId,
        notificationId: notificationResponse.id
      });
    } catch (error) {
      try {
        await gcsCommon.makePubSubRequest(
          import_pieces_common16.HttpMethod.DELETE,
          `/projects/${projectId}/subscriptions/${triggerId}`,
          auth.access_token
        );
      } catch (e) {
      }
      try {
        await gcsCommon.makePubSubRequest(
          import_pieces_common16.HttpMethod.DELETE,
          `/projects/${projectId}/topics/${triggerId}`,
          auth.access_token
        );
      } catch (e) {
      }
      throw new Error(`Failed to setup Pub/Sub notifications: ${(error == null ? void 0 : error.message) || "Unknown error"}`);
    }
  },
  onDisable: async (context) => {
    let triggerData = await context.store.get("_trigger");
    if (!triggerData) return;
    let { projectId } = context.propsValue, { bucket } = context.propsValue, auth = context.auth;
    try {
      await gcsCommon.makePubSubRequest(
        import_pieces_common16.HttpMethod.DELETE,
        `/projects/${projectId}/subscriptions/${triggerData.subscriptionName}`,
        auth.access_token
      );
    } catch (e) {
    }
    try {
      await gcsCommon.makeGCSRequest(
        import_pieces_common16.HttpMethod.DELETE,
        `/b/${bucket}/notificationConfigs/${triggerData.notificationId}`,
        auth.access_token
      );
    } catch (e) {
    }
    try {
      await gcsCommon.makePubSubRequest(
        import_pieces_common16.HttpMethod.DELETE,
        `/projects/${projectId}/topics/${triggerData.topicName}`,
        auth.access_token
      );
    } catch (e) {
    }
  },
  run: async (context) => {
    var _a;
    let payload = context.payload.body;
    return (_a = payload == null ? void 0 : payload.message) != null && _a.data ? [JSON.parse(
      Buffer.from(payload.message.data, "base64").toString()
    )] : [];
  }
});

// ../ap/packages/pieces/community/google-cloud-storage/src/index.ts
var googleCloudStorage = (0, import_pieces_framework17.createPiece)({
  displayName: "Google Cloud Storage",
  description: "Automate file storage operations with Google Cloud Storage. Upload, download, manage buckets, set permissions, and monitor changes with real-time triggers.",
  auth: googleCloudStorageAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/google-cloud-storage.png",
  authors: ["sparkybug"],
  categories: [import_pieces_framework18.PieceCategory.CONTENT_AND_FILES],
  actions: [
    createBucket,
    deleteEmptyBucket,
    cloneObject,
    deleteObject,
    searchObjects,
    searchBuckets,
    createObjectAcl,
    deleteObjectAcl,
    createBucketAcl,
    deleteBucketAcl,
    createBucketDefaultObjectAcl,
    deleteBucketDefaultObjectAcl
  ],
  triggers: [
    newObjectCreated,
    objectUpdated
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  googleCloudStorage
});
