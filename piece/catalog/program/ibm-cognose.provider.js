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

// ../ap/packages/pieces/community/ibm-cognose/src/index.ts
var index_exports = {};
__export(index_exports, {
  ibmCognose: () => ibmCognose
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common"), import_pieces_framework13 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/ibm-cognose/src/lib/common/cognos-client.ts
var import_pieces_common = require("@activepieces/pieces-common");
var CognosClient = class {
  constructor(auth) {
    __publicField(this, "auth");
    __publicField(this, "sessionCookies");
    this.auth = auth;
  }
  async createSession() {
    var _a;
    let parameters = [
      { name: "CAMNamespace", value: this.auth.CAMNamespace },
      { name: "CAMUsername", value: this.auth.username },
      { name: "CAMPassword", value: this.auth.password }
    ];
    try {
      let response = await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.PUT,
        url: `${this.auth.baseurl}/api/v1/session`,
        headers: {
          "Content-Type": "application/json"
        },
        body: {
          parameters
        }
      });
      if (response.status >= 200 && response.status < 300) {
        let cookies = (_a = response.headers) == null ? void 0 : _a["set-cookie"];
        return this.sessionCookies = cookies ? Array.isArray(cookies) ? cookies.join("; ") : cookies : "", this.sessionCookies;
      } else
        throw new Error(
          `Authentication failed: ${response.status} ${response.body}`
        );
    } catch (error) {
      throw new Error(
        `Failed to create session: ${error instanceof Error ? error.message : "Unknown error"}`
      );
    }
  }
  async makeAuthenticatedRequest(endpoint, method = import_pieces_common.HttpMethod.GET, body) {
    try {
      return this.sessionCookies || await this.createSession(), await import_pieces_common.httpClient.sendRequest({
        method,
        url: `${this.auth.baseurl}/api/v1${endpoint}`,
        headers: {
          "Content-Type": "application/json",
          Cookie: this.sessionCookies || ""
        },
        body
      });
    } catch (error) {
      throw new Error(
        `Request failed: ${error instanceof Error ? error.message : "Unknown error"}`
      );
    }
  }
};

// ../ap/packages/pieces/community/ibm-cognose/src/lib/actions/create-data-source.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/ibm-cognose/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");
var ibmCognoseAuth = import_pieces_framework.PieceAuth.CustomAuth({
  required: !0,
  description: `
## IBM Cognos Analytics Authentication

Enter your Cognos Analytics credentials:
- **Username**: Your Cognos username
- **Password**: Your Cognos password
  `,
  props: {
    baseurl: import_pieces_framework.Property.ShortText({
      displayName: "Base URL",
      description: "Cognos server URL (e.g., https://your-cognos-server.com)",
      required: !0
    }),
    CAMNamespace: import_pieces_framework.Property.ShortText({
      displayName: "CAM Namespace",
      description: "CAM namespace for authentication (e.g., LDAP)",
      required: !0
    }),
    username: import_pieces_framework.Property.ShortText({
      displayName: "Username",
      description: "Cognos username",
      required: !0
    }),
    password: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Password",
      description: "Cognos password",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    let { username, password, CAMNamespace, baseurl } = auth;
    if (!username || !password || !CAMNamespace || !baseurl)
      return {
        valid: !1,
        error: "All fields are required"
      };
    try {
      return await new CognosClient(auth).createSession(), {
        valid: !0
      };
    } catch (error) {
      return {
        valid: !1,
        error: `Authentication failed: ${error instanceof Error ? error.message : "Unknown error"}`
      };
    }
  }
});

// ../ap/packages/pieces/community/ibm-cognose/src/lib/actions/create-data-source.ts
var createDataSourceAction = (0, import_pieces_framework2.createAction)({
  auth: ibmCognoseAuth,
  name: "create_data_source",
  displayName: "Create Data Source",
  description: "Create a new data source",
  audience: "both",
  aiMetadata: { description: "Create a new data source in IBM Cognos Analytics from a JDBC connection string, optionally attaching a database driver and a signon (username/password) credential. Use when provisioning a database connection for reporting. Not idempotent: each call creates a new data source and fails if one with the same name already exists.", idempotent: !1 },
  props: {
    defaultName: import_pieces_framework2.Property.ShortText({
      displayName: "Data Source Name",
      description: "Name for the new data source",
      required: !0
    }),
    connectionString: import_pieces_framework2.Property.LongText({
      displayName: "Connection String",
      description: "JDBC URL or database connection string",
      required: !0,
      defaultValue: "jdbc:db2://localhost:50000/SAMPLE"
    }),
    driverName: import_pieces_framework2.Property.ShortText({
      displayName: "Driver Name",
      description: "Database driver class name",
      required: !1,
      defaultValue: "com.ibm.db2.jcc.DB2Driver"
    }),
    username: import_pieces_framework2.Property.ShortText({
      displayName: "Database Username",
      description: "Username for database connection",
      required: !1
    }),
    password: import_pieces_framework2.Property.ShortText({
      displayName: "Database Password",
      description: "Password for database connection",
      required: !1
    }),
    signonDefaultName: import_pieces_framework2.Property.ShortText({
      displayName: "Signon Name",
      description: "Name for the database credentials",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let { defaultName, connectionString, driverName, username, password, signonDefaultName } = propsValue;
    try {
      let client = new CognosClient(auth.props), dataSourceDefinition = {
        defaultName,
        connections: [
          {
            connectionString,
            defaultName: defaultName + "_connection"
          }
        ]
      };
      driverName && (dataSourceDefinition.connections[0].connectionString += `;DRIVER_NAME=${driverName}`), username && password && (dataSourceDefinition.connections[0].signons = [
        {
          defaultName: signonDefaultName || defaultName + "_signon",
          credentialsEx: {
            username,
            password
          }
        }
      ]);
      let response = await client.makeAuthenticatedRequest("/dataSources", import_pieces_common2.HttpMethod.POST, dataSourceDefinition);
      if (response.status === 201)
        return {
          success: !0,
          message: `Data source '${defaultName}' created successfully`,
          dataSource: response.body
        };
      throw response.status === 400 ? new Error(`Data source '${defaultName}' already exists or invalid parameters`) : response.status === 401 ? new Error("Authentication failed. Check your credentials.") : new Error(`Failed to create data source: ${response.status} ${response.body}`);
    } catch (error) {
      throw new Error(
        `Failed to create data source: ${error instanceof Error ? error.message : "Unknown error"}`
      );
    }
  }
});

// ../ap/packages/pieces/community/ibm-cognose/src/lib/actions/update-data-source.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/ibm-cognose/src/lib/common/data-source-dropdown.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var dataSourceDropdown = import_pieces_framework3.Property.Dropdown({
  auth: ibmCognoseAuth,
  displayName: "Data Source",
  description: "Select a data source",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Connect your account first"
      };
    try {
      let response = await new CognosClient(auth.props).makeAuthenticatedRequest("/dataSources", import_pieces_common3.HttpMethod.GET);
      if (response.status === 200) {
        let dataSources = response.body.dataSources || [];
        return dataSources.length === 0 ? {
          disabled: !0,
          options: [],
          placeholder: "No data sources found"
        } : {
          disabled: !1,
          options: dataSources.map((ds) => ({
            label: ds.defaultName || ds.id,
            value: ds.id
          }))
        };
      } else
        return {
          disabled: !0,
          options: [],
          placeholder: "Failed to load data sources"
        };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Error loading data sources"
      };
    }
  }
});

// ../ap/packages/pieces/community/ibm-cognose/src/lib/actions/update-data-source.ts
var updateDataSourceAction = (0, import_pieces_framework4.createAction)({
  auth: ibmCognoseAuth,
  name: "update_data_source",
  displayName: "Update Data Source",
  description: "Update an existing data source",
  audience: "both",
  aiMetadata: { description: "Update an existing IBM Cognos data source identified by its id, changing its name and/or its disabled/hidden flags. At least one field must be supplied. Idempotent: re-applying the same values leaves the data source in the same state.", idempotent: !0 },
  props: {
    datasourceId: dataSourceDropdown,
    defaultName: import_pieces_framework4.Property.ShortText({
      displayName: "Data Source Name",
      description: "New name for the data source",
      required: !1
    }),
    disabled: import_pieces_framework4.Property.Checkbox({
      displayName: "Disabled",
      description: "Disable the data source",
      required: !1,
      defaultValue: !1
    }),
    hidden: import_pieces_framework4.Property.Checkbox({
      displayName: "Hidden",
      description: "Hide the data source",
      required: !1,
      defaultValue: !1
    })
  },
  async run({ auth, propsValue }) {
    let { datasourceId, defaultName, disabled, hidden } = propsValue;
    try {
      let client = new CognosClient(auth.props), updateDefinition = {};
      if (defaultName && (updateDefinition.defaultName = defaultName), disabled !== void 0 && (updateDefinition.disabled = disabled), hidden !== void 0 && (updateDefinition.hidden = hidden), Object.keys(updateDefinition).length === 0)
        throw new Error("At least one field must be provided to update");
      let response = await client.makeAuthenticatedRequest(
        `/dataSources/${datasourceId}`,
        import_pieces_common4.HttpMethod.PUT,
        updateDefinition
      );
      if (response.status === 204 || response.status === 200)
        return {
          success: !0,
          message: "Data source updated successfully",
          updatedFields: updateDefinition
        };
      throw response.status === 401 ? new Error("Authentication failed. Check your credentials.") : response.status === 404 ? new Error("Data source not found") : new Error(`Failed to update: ${response.status} ${response.body}`);
    } catch (error) {
      throw new Error(
        `Failed to update data source: ${error instanceof Error ? error.message : "Unknown error"}`
      );
    }
  }
});

// ../ap/packages/pieces/community/ibm-cognose/src/lib/actions/delete-data-source.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var deleteDataSourceAction = (0, import_pieces_framework5.createAction)({
  auth: ibmCognoseAuth,
  name: "delete_data_source",
  displayName: "Delete Data Source",
  description: "Delete a data source",
  audience: "both",
  aiMetadata: { description: "Permanently delete an IBM Cognos data source by its id. Use to remove a database connection that is no longer needed. Destructive and not idempotent: a repeat call on an already-deleted data source returns not-found.", idempotent: !1 },
  props: {
    datasourceId: dataSourceDropdown
  },
  async run({ auth, propsValue }) {
    let { datasourceId } = propsValue;
    try {
      let response = await new CognosClient(auth.props).makeAuthenticatedRequest(
        `/dataSources/${datasourceId}`,
        import_pieces_common5.HttpMethod.DELETE
      );
      if (response.status === 204)
        return {
          success: !0,
          message: "Data source deleted successfully"
        };
      throw response.status === 401 ? new Error("Authentication failed. Check your credentials.") : response.status === 404 ? new Error("Data source not found") : new Error(`Failed to delete: ${response.status} ${response.body}`);
    } catch (error) {
      throw new Error(
        `Failed to delete data source: ${error instanceof Error ? error.message : "Unknown error"}`
      );
    }
  }
});

// ../ap/packages/pieces/community/ibm-cognose/src/lib/actions/get-data-source.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var getDataSourceAction = (0, import_pieces_framework6.createAction)({
  auth: ibmCognoseAuth,
  name: "get_data_source",
  displayName: "Get Data Source",
  description: "Retrieve data source details",
  audience: "both",
  aiMetadata: { description: "Fetch the details of a single IBM Cognos data source by its id. Use to read or verify a data source's configuration before acting on it; optionally request extra fields such as connections or signons. Idempotent read-only lookup.", idempotent: !0 },
  props: {
    datasourceId: dataSourceDropdown,
    fields: import_pieces_framework6.Property.ShortText({
      displayName: "Extra Fields",
      description: "Comma-separated list of extra fields (e.g., connections,signons)",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let { datasourceId, fields } = propsValue;
    try {
      let client = new CognosClient(auth.props), queryParams = [];
      fields && fields.trim() && queryParams.push(`fields=${encodeURIComponent(fields.trim())}`);
      let queryString = queryParams.length > 0 ? `?${queryParams.join("&")}` : "", response = await client.makeAuthenticatedRequest(
        `/dataSources/${datasourceId}${queryString}`,
        import_pieces_common6.HttpMethod.GET
      );
      if (response.status === 200)
        return response.body;
      throw response.status === 401 ? new Error("Authentication failed. Check your credentials.") : response.status === 404 ? new Error("Data source not found") : new Error(`Failed to retrieve: ${response.status} ${response.body}`);
    } catch (error) {
      throw new Error(
        `Failed to retrieve data source: ${error instanceof Error ? error.message : "Unknown error"}`
      );
    }
  }
});

// ../ap/packages/pieces/community/ibm-cognose/src/lib/actions/update-content-object.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/ibm-cognose/src/lib/common/content-object-dropdown.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var contentObjectDropdown = import_pieces_framework7.Property.Dropdown({
  auth: ibmCognoseAuth,
  displayName: "Content Object",
  description: "Select a content object",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Connect your account first"
      };
    try {
      let response = await new CognosClient(auth.props).makeAuthenticatedRequest("/content", import_pieces_common7.HttpMethod.GET);
      if (response.status === 200) {
        let contentObjects = response.body.content || [];
        return contentObjects.length === 0 ? {
          disabled: !0,
          options: [],
          placeholder: "No content objects found"
        } : {
          disabled: !1,
          options: contentObjects.map((obj) => ({
            label: `${obj.defaultName || "Unnamed"} (${obj.type || "Unknown"})`,
            value: obj.id
          }))
        };
      } else
        return {
          disabled: !0,
          options: [],
          placeholder: "Failed to load content objects"
        };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Error loading content objects"
      };
    }
  }
});

// ../ap/packages/pieces/community/ibm-cognose/src/lib/actions/update-content-object.ts
var updateContentObjectAction = (0, import_pieces_framework8.createAction)({
  auth: ibmCognoseAuth,
  name: "update_content_object",
  displayName: "Update Content Object",
  description: "Update an existing content object",
  audience: "both",
  aiMetadata: { description: "Update an existing content-store object (report, dashboard, folder, etc.) identified by its id, changing its name and/or description. The object type is required, and an optional version enables optimistic-concurrency checking that rejects the update if the object changed since it was fetched. Idempotent: re-applying the same values leaves the object in the same state.", idempotent: !0 },
  props: {
    objectId: contentObjectDropdown,
    type: import_pieces_framework8.Property.ShortText({
      displayName: "Type",
      description: "Object type (e.g., report, dashboard, folder)",
      required: !0
    }),
    defaultName: import_pieces_framework8.Property.ShortText({
      displayName: "Name",
      description: "New name for the object",
      required: !1
    }),
    defaultDescriptions: import_pieces_framework8.Property.LongText({
      displayName: "Description",
      description: "New description for the object",
      required: !1
    }),
    version: import_pieces_framework8.Property.Number({
      displayName: "Version",
      description: "Current version for optimistic concurrency",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let { objectId, defaultName, defaultDescriptions, type, version } = propsValue;
    try {
      let client = new CognosClient(auth.props), updateDefinition = { type };
      defaultName && (updateDefinition.defaultName = defaultName), defaultDescriptions && (updateDefinition.defaultDescriptions = defaultDescriptions), version != null && (updateDefinition.version = version);
      let response = await client.makeAuthenticatedRequest(
        `/content/${objectId}`,
        import_pieces_common8.HttpMethod.PUT,
        updateDefinition
      );
      if (response.status === 204 || response.status === 200)
        return {
          success: !0,
          message: "Content object updated successfully"
        };
      throw response.status === 401 ? new Error("Authentication failed. Check your credentials.") : response.status === 404 ? new Error("Content object not found") : response.status === 409 ? new Error("Object has changed since you fetched it. Refresh and try again.") : new Error(`Failed to update: ${response.status} ${response.body}`);
    } catch (error) {
      throw new Error(
        `Failed to update content object: ${error instanceof Error ? error.message : "Unknown error"}`
      );
    }
  }
});

// ../ap/packages/pieces/community/ibm-cognose/src/lib/actions/get-content-object.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var getContentObjectAction = (0, import_pieces_framework9.createAction)({
  auth: ibmCognoseAuth,
  name: "get_content_object",
  displayName: "Get Content Object",
  description: "Get content object details",
  audience: "both",
  aiMetadata: { description: "Fetch the details of a single content-store object (report, dashboard, folder, etc.) by its id from the IBM Cognos content tree. Use to inspect an object before moving, copying, or updating it; optionally request extra fields such as owner, permissions, or children. Idempotent read-only lookup.", idempotent: !0 },
  props: {
    objectId: contentObjectDropdown,
    fields: import_pieces_framework9.Property.ShortText({
      displayName: "Extra Fields",
      description: "Comma-separated list of extra fields (e.g., owner,permissions,children)",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let { objectId, fields } = propsValue;
    try {
      let client = new CognosClient(auth.props), queryParams = [];
      fields && fields.trim() && queryParams.push(`fields=${encodeURIComponent(fields.trim())}`);
      let queryString = queryParams.length > 0 ? `?${queryParams.join("&")}` : "", response = await client.makeAuthenticatedRequest(
        `/content/${objectId}${queryString}`,
        import_pieces_common9.HttpMethod.GET
      );
      if (response.status === 200)
        return response.body;
      throw response.status === 401 ? new Error("Authentication failed. Check your credentials.") : response.status === 404 ? new Error("Content object not found") : new Error(`Failed to retrieve: ${response.status} ${response.body}`);
    } catch (error) {
      throw new Error(
        `Failed to retrieve content object: ${error instanceof Error ? error.message : "Unknown error"}`
      );
    }
  }
});

// ../ap/packages/pieces/community/ibm-cognose/src/lib/actions/move-content-object.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var moveContentObjectAction = (0, import_pieces_framework10.createAction)({
  auth: ibmCognoseAuth,
  name: "move_content_object",
  displayName: "Move Content Object",
  description: "Move an object with all its descendants",
  audience: "both",
  aiMetadata: { description: "Move a content-store object, together with all of its descendants, from its current location into a destination container within the IBM Cognos content tree. Use to reorganize reports, dashboards, or folders. Source and destination must differ. Not idempotent: a repeat call after the object has moved fails because the source no longer exists at its original location.", idempotent: !1 },
  props: {
    sourceId: contentObjectDropdown,
    destinationId: __spreadProps(__spreadValues({}, contentObjectDropdown), {
      displayName: "Destination Container",
      description: "Destination container where the object will be moved"
    })
  },
  async run({ auth, propsValue }) {
    var _a;
    let { sourceId, destinationId } = propsValue;
    if (sourceId === destinationId)
      throw new Error("Source and destination cannot be the same object");
    try {
      let client = new CognosClient(auth.props), moveRequest = {
        source_id: sourceId,
        destination_id: destinationId
      }, response = await client.makeAuthenticatedRequest(
        "/content/move",
        import_pieces_common10.HttpMethod.PUT,
        moveRequest
      );
      if (response.status === 204)
        return {
          success: !0,
          message: "Content object moved successfully"
        };
      throw response.status === 400 ? new Error(`Bad request: ${((_a = response.body) == null ? void 0 : _a.message) || "Invalid move operation"}`) : response.status === 401 ? new Error("Authentication failed. Check your credentials.") : response.status === 403 ? new Error("Permission denied. Insufficient permissions to move this object.") : response.status === 404 ? new Error("Object or parent object not found.") : new Error(`Failed to move: ${response.status} ${response.body}`);
    } catch (error) {
      throw new Error(
        `Failed to move content object: ${error instanceof Error ? error.message : "Unknown error"}`
      );
    }
  }
});

// ../ap/packages/pieces/community/ibm-cognose/src/lib/actions/copy-content-object.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var copyContentObjectAction = (0, import_pieces_framework11.createAction)({
  auth: ibmCognoseAuth,
  name: "copy_content_object",
  displayName: "Copy Content Object",
  description: "Copy an object optionally with all its descendants",
  audience: "both",
  aiMetadata: { description: "Copy a content-store object into a destination container within the IBM Cognos content tree, optionally including all of its descendants (recursive, on by default). Use to duplicate reports, dashboards, or folders. Source and destination must differ. Not idempotent: each call creates a new copy.", idempotent: !1 },
  props: {
    sourceId: contentObjectDropdown,
    destinationId: __spreadProps(__spreadValues({}, contentObjectDropdown), {
      displayName: "Destination Container",
      description: "Destination container where the object will be copied"
    }),
    recursive: import_pieces_framework11.Property.Checkbox({
      displayName: "Recursive Copy",
      description: "Copy all descendants (child objects)",
      required: !1,
      defaultValue: !0
    })
  },
  async run({ auth, propsValue }) {
    var _a;
    let { sourceId, destinationId, recursive } = propsValue;
    if (sourceId === destinationId)
      throw new Error("Source and destination cannot be the same object");
    try {
      let client = new CognosClient(auth.props), copyRequest = {
        source_id: sourceId,
        destination_id: destinationId,
        recursive: recursive !== void 0 ? recursive : !0
      }, response = await client.makeAuthenticatedRequest(
        "/content/copy",
        import_pieces_common11.HttpMethod.POST,
        copyRequest
      );
      if (response.status === 201)
        return {
          success: !0,
          message: `Content object copied successfully${recursive ? " (including descendants)" : ""}`,
          copiedObject: response.body
        };
      throw response.status === 400 ? new Error(`Bad request: ${((_a = response.body) == null ? void 0 : _a.message) || "Invalid copy operation"}`) : response.status === 401 ? new Error("Authentication failed. Check your credentials.") : response.status === 403 ? new Error("Permission denied. Insufficient permissions to copy this object.") : response.status === 404 ? new Error("Object or parent object not found.") : new Error(`Failed to copy: ${response.status} ${response.body}`);
    } catch (error) {
      throw new Error(
        `Failed to copy content object: ${error instanceof Error ? error.message : "Unknown error"}`
      );
    }
  }
});

// ../ap/packages/pieces/community/ibm-cognose/src/index.ts
var ibmCognose = (0, import_pieces_framework12.createPiece)({
  displayName: "IBM Cognos Analytics",
  description: "Business intelligence and performance management suite for data analysis and reporting",
  auth: ibmCognoseAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/ibm-cognose.png",
  categories: [import_pieces_framework13.PieceCategory.BUSINESS_INTELLIGENCE],
  authors: ["fortunamide", "onyedikachi-david"],
  actions: [
    createDataSourceAction,
    getDataSourceAction,
    updateDataSourceAction,
    deleteDataSourceAction,
    getContentObjectAction,
    updateContentObjectAction,
    moveContentObjectAction,
    copyContentObjectAction,
    (0, import_pieces_common12.createCustomApiCallAction)({
      baseUrl: (auth) => `${auth.baseurl}/api/v1`,
      auth: ibmCognoseAuth,
      authMapping: async (auth) => {
        try {
          let client = new CognosClient(auth.props);
          return await client.createSession(), client.sessionCookies ? {
            Cookie: client.sessionCookies
          } : {};
        } catch (error) {
          throw new Error(
            `Failed to authenticate: ${error instanceof Error ? error.message : "Unknown error"}`
          );
        }
      }
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  ibmCognose
});
