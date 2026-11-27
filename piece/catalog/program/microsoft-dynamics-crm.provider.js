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

// ../ap/packages/pieces/community/microsoft-dynamics-crm/src/index.ts
var index_exports = {};
__export(index_exports, {
  getBaseUrl: () => getBaseUrl,
  microsoftDynamicsCrm: () => microsoftDynamicsCrm
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/microsoft-dynamics-crm/src/lib/actions/create-record.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/microsoft-dynamics-crm/src/lib/auth.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/microsoft-dynamics-crm/src/lib/common/microsoft-cloud.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), COMMERCIAL_LOGIN_HOST = "login.microsoftonline.com", GOV_LOGIN_HOST = "login.microsoftonline.us", microsoftCloudProperty = import_pieces_framework.Property.StaticDropdown({
  displayName: "Cloud Environment",
  description: "Select your Microsoft cloud environment.",
  required: !0,
  defaultValue: COMMERCIAL_LOGIN_HOST,
  options: {
    disabled: !1,
    options: [
      { label: "Commercial (Global)", value: COMMERCIAL_LOGIN_HOST },
      { label: "US Government (GCC High)", value: GOV_LOGIN_HOST }
    ]
  }
});

// ../ap/packages/pieces/community/microsoft-dynamics-crm/src/lib/auth.ts
var authDesc = `
Follow the [Azure app creation guide](https://learn.microsoft.com/en-us/entra/identity-platform/quickstart-register-app#register-an-application),
 set the **Redirect URI** to {{redirectUrl}} and add the **Dynamics CRM (Delegated) permissions** under **API permissions**.`, dynamicsCRMAuth = import_pieces_framework2.PieceAuth.OAuth2({
  description: authDesc,
  props: {
    cloud: microsoftCloudProperty,
    hostUrl: import_pieces_framework2.Property.ShortText({
      displayName: "Host URL (without trailing slash)",
      description: "Host URL without trailing slash.For example **https://demo.crm.dynamics.com**",
      required: !0
    }),
    tenantId: import_pieces_framework2.Property.ShortText({
      displayName: "Tenant ID",
      description: "You can find this in the Azure portal.",
      defaultValue: "common",
      required: !0
    }),
    proxyUrl: import_pieces_framework2.Property.ShortText({
      displayName: "Proxy URL with Port",
      description: "Keep empty if not needed. Optional proxy URL used for establishing connections when proxying requests is needed. For example: **https://proxy.com:8080**.",
      required: !1
    })
  },
  required: !0,
  scope: ["{hostUrl}/.default", "openid", "email", "profile", "offline_access"],
  prompt: "omit",
  authUrl: "https://{cloud}/{tenantId}/oauth2/v2.0/authorize",
  tokenUrl: "https://{cloud}/{tenantId}/oauth2/v2.0/token"
});

// ../ap/packages/pieces/community/microsoft-dynamics-crm/src/lib/common/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/microsoft-dynamics-crm/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
var DynamicsCRMClient = class {
  constructor(hostUrl, accessToken, proxyUrl) {
    __publicField(this, "hostUrl", hostUrl);
    __publicField(this, "accessToken", accessToken);
    __publicField(this, "proxyUrl", proxyUrl);
  }
  async makeRequest(method, resourceUri, headers, query, body = void 0) {
    let baseUrl = getBaseUrl(this.hostUrl.replace(/\/$/, ""), this.proxyUrl);
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${baseUrl}/api/data/v9.2` + resourceUri,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: this.accessToken
      },
      headers: __spreadValues({
        Accept: "application/json",
        "OData-MaxVersion": "4.0",
        "OData-Version": "4.0",
        "Content-Type": "application/json"
      }, headers),
      queryParams: query,
      body
    })).body;
  }
  async createRecord(entityUrlPath, request) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      `/${entityUrlPath}`,
      {
        // return created data in response
        // docs: https://learn.microsoft.com/en-us/previous-versions/dynamicscrm-2016/developers-guide/gg328090(v=crm.8)#create-with-data-returned
        Prefer: "return=representation"
      },
      void 0,
      request
    );
  }
  async updatedRecord(entityUrlPath, recordId, request) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.PATCH,
      `/${entityUrlPath}(${recordId})`,
      {
        // return created data in response
        // docs: https://learn.microsoft.com/en-us/previous-versions/dynamicscrm-2016/developers-guide/mt607664(v=crm.8)#update-with-data-returned
        Prefer: "return=representation"
      },
      void 0,
      request
    );
  }
  async getRecord(entityUrlPath, recordId) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      `/${entityUrlPath}(${recordId})`
    );
  }
  async deleteRecord(entityUrlPath, recordId) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.DELETE,
      `/${entityUrlPath}(${recordId})`
    );
  }
  async fetchEntityTypes() {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/EntityDefinitions",
      void 0,
      {
        $select: ["EntitySetName", "LogicalName"].join(",")
      }
    );
  }
  async fetchEntityTypeAttributes(entitySetName) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/EntityDefinitions",
      void 0,
      {
        $select: [
          "PrimaryIdAttribute",
          "PrimaryNameAttribute",
          "LogicalName"
        ].join(","),
        $filter: `EntitySetName eq '${entitySetName}'`
      }
    );
  }
  async fetchEntityAttributes(entityName) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      `/EntityDefinitions(LogicalName='${entityName}')/Attributes`,
      void 0,
      {
        $select: [
          "AttributeType",
          "LogicalName",
          "Description",
          "DisplayName",
          "IsPrimaryName",
          "IsValidForCreate"
        ].join(",")
      }
    );
  }
  async fetchOptionFieldValues(entityName, entityAttributeName, entityAttributeType) {
    var _a, _b;
    let res = await this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      `/EntityDefinitions(LogicalName='${entityName}')/Attributes(LogicalName='${entityAttributeName}')/Microsoft.Dynamics.CRM.${entityAttributeType}AttributeMetadata`,
      void 0,
      {
        $select: "LogicalName",
        $expand: "OptionSet($select=Options),GlobalOptionSet($select=Options)"
      }
    ), optionSet = (_a = res.OptionSet) != null ? _a : res.GlobalOptionSet;
    return ((_b = optionSet == null ? void 0 : optionSet.Options) == null ? void 0 : _b.map(({ Value, Label }) => {
      var _a2;
      return {
        value: Value,
        label: (_a2 = Label.UserLocalizedLabel.Label) != null ? _a2 : String(Value)
      };
    })) || [];
  }
};

// ../ap/packages/pieces/community/microsoft-dynamics-crm/src/lib/common/index.ts
function makeClient(auth) {
  var _a, _b;
  return new DynamicsCRMClient(
    (_a = auth.props) == null ? void 0 : _a.hostUrl,
    auth.access_token,
    (_b = auth.props) == null ? void 0 : _b.proxyUrl
  );
}
var DynamicsCRMCommon = {
  entityType: (description) => import_pieces_framework3.Property.Dropdown({
    auth: dynamicsCRMAuth,
    displayName: "Entity Type",
    refreshers: [],
    description,
    required: !0,
    options: async ({ auth }) => auth ? {
      disabled: !1,
      options: (await makeClient(
        auth
      ).fetchEntityTypes()).value.map((val) => ({
        label: val.EntitySetName,
        value: val.EntitySetName
      }))
    } : {
      disabled: !0,
      options: [],
      placeholder: ""
    }
  }),
  recordId: import_pieces_framework3.Property.Dropdown({
    auth: dynamicsCRMAuth,
    displayName: "Record ID",
    refreshers: ["entityType"],
    required: !0,
    options: async ({ auth, entityType }) => {
      var _a, _b;
      if (!auth || !entityType)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please select entity type first."
        };
      let res = await makeClient(
        auth
      ).fetchEntityTypeAttributes(entityType);
      if (!res.value[0])
        return {
          disabled: !0,
          options: [],
          placeholder: "Please select entity type first."
        };
      let entityUrlPath = entityType, entityPrimaryKey = res.value[0].PrimaryIdAttribute, entityprimaryNameAttribute = res.value[0].PrimaryNameAttribute, authValue = auth, request = {
        method: import_pieces_common2.HttpMethod.GET,
        url: `${getBaseUrl(
          (_a = authValue.props) == null ? void 0 : _a.hostUrl,
          (_b = authValue.props) == null ? void 0 : _b.proxyUrl
        )}/api/data/v9.2/${entityUrlPath}`,
        queryParams: {
          $select: entityprimaryNameAttribute
        },
        authentication: {
          type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
          token: authValue.access_token
        },
        headers: {
          Accept: "application/json",
          "OData-MaxVersion": "4.0",
          "OData-Version": "4.0",
          "Content-Type": "application/json"
        }
      }, { body } = await import_pieces_common2.httpClient.sendRequest(request);
      return {
        disabled: !1,
        options: body.value.map((val) => {
          var _a2;
          return {
            label: (_a2 = val[entityprimaryNameAttribute]) != null ? _a2 : val[entityPrimaryKey],
            value: val[entityPrimaryKey]
          };
        })
      };
    }
  }),
  entityFields: (isCreate = !0) => import_pieces_framework3.Property.DynamicProperties({
    auth: dynamicsCRMAuth,
    displayName: "Entity Fields",
    refreshers: ["auth", "entityType"],
    required: !0,
    props: async ({ auth, entityType }) => {
      var _a, _b, _c, _d, _e, _f;
      if (!auth) return {};
      if (!entityType) return {};
      let fields = {}, client = makeClient(
        auth
      ), typeRes = await client.fetchEntityTypeAttributes(
        entityType
      );
      if (!typeRes.value[0])
        return {
          disabled: !0,
          options: [],
          placeholder: "Please select entity type first."
        };
      let res = await client.fetchEntityAttributes(
        typeRes.value[0].LogicalName
      );
      for (let field of res.value)
        if (field.IsValidForCreate && ![
          "EntityName" /* ENTITY_NAME */,
          "Lookup" /* LOOKUP */,
          "Memo" /* MEMO */,
          "Money" /* MONEY */,
          "Owner" /* OWNER */,
          "Virtual" /* VIRTUAL */,
          "Uniqueidentifier" /* UNIQUE_IDENTIFIER */
        ].includes(field.AttributeType)) {
          let params = {
            displayName: (_c = (_b = (_a = field.DisplayName) == null ? void 0 : _a.UserLocalizedLabel) == null ? void 0 : _b.Label) != null ? _c : field.LogicalName,
            description: (_f = (_e = (_d = field.Description) == null ? void 0 : _d.UserLocalizedLabel) == null ? void 0 : _e.Label) != null ? _f : "",
            required: field.IsPrimaryName && isCreate
          };
          switch (field.AttributeType) {
            case "BigInt" /* BIGINT */:
            case "Decimal" /* DECIMAL */:
            case "Double" /* DOUBLE */:
            case "Integer" /* INTEGER */:
              fields[field.LogicalName] = import_pieces_framework3.Property.Number(params);
              break;
            case "DateTime" /* DATETIME */:
              fields[field.LogicalName] = import_pieces_framework3.Property.DateTime(params);
              break;
            case "Boolean" /* BOOLEAN */:
              fields[field.LogicalName] = import_pieces_framework3.Property.Checkbox(params);
              break;
            case "String" /* STRING */:
              fields[field.LogicalName] = import_pieces_framework3.Property.ShortText(params);
              break;
            case "Picklist" /* PICKLIST */:
            case "State" /* STATE */:
            case "Status" /* STATUS */: {
              let options = await client.fetchOptionFieldValues(
                typeRes.value[0].LogicalName,
                field.LogicalName,
                field.AttributeType
              );
              fields[field.LogicalName] = import_pieces_framework3.Property.StaticDropdown(__spreadProps(__spreadValues({}, params), {
                options: {
                  disabled: !1,
                  options
                }
              }));
              break;
            }
            default:
              break;
          }
        }
      return fields;
    }
  })
};

// ../ap/packages/pieces/community/microsoft-dynamics-crm/src/lib/actions/create-record.ts
var createRecordAction = (0, import_pieces_framework4.createAction)({
  auth: dynamicsCRMAuth,
  name: "dynamics_crm_create_record",
  displayName: "Create Record",
  description: "Creates a new record.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new record of a chosen Dynamics 365 / Dataverse entity type (e.g. accounts, contacts, leads), passing the field values for that entity. Pick the entity from its discovered entity-set name or map a raw entity-set name. Not idempotent: each call inserts a new record, so repeated calls create duplicates.",
    idempotent: !1
  },
  props: {
    entityType: DynamicsCRMCommon.entityType(
      "Select or map the entity for which you want to create the record."
    ),
    fields: DynamicsCRMCommon.entityFields(!0)
  },
  async run(context) {
    let { entityType, fields } = context.propsValue, entityUrlPath = entityType;
    return await makeClient(
      context.auth
    ).createRecord(entityUrlPath, fields);
  }
});

// ../ap/packages/pieces/community/microsoft-dynamics-crm/src/lib/actions/delete-record.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var deleteRecordAction = (0, import_pieces_framework5.createAction)({
  auth: dynamicsCRMAuth,
  name: "dynamics_crm_delete_record",
  displayName: "Delete Record",
  description: "Deletes an existing record.",
  audience: "both",
  aiMetadata: {
    description: "Permanently deletes a single record from a chosen Dynamics 365 / Dataverse entity type, identified by its record ID (the entity primary-key GUID). Use when an agent must remove a specific known record. Destructive and not idempotent: the first call removes the record and subsequent calls for the same ID fail since it no longer exists.",
    idempotent: !1
  },
  props: {
    entityType: DynamicsCRMCommon.entityType(
      "Select or map the entity name whose records you want to delete."
    ),
    recordId: DynamicsCRMCommon.recordId
  },
  async run(context) {
    let { entityType, recordId } = context.propsValue, entityUrlPath = entityType;
    return await makeClient(
      context.auth
    ).deleteRecord(entityUrlPath, recordId);
  }
});

// ../ap/packages/pieces/community/microsoft-dynamics-crm/src/lib/actions/get-record.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var getRecordAction = (0, import_pieces_framework6.createAction)({
  auth: dynamicsCRMAuth,
  name: "dynamics_crm_get_record",
  displayName: "Get Record",
  description: "Retrieves an existing record.",
  audience: "both",
  aiMetadata: {
    description: "Fetches a single record from a chosen Dynamics 365 / Dataverse entity type by its record ID (the entity primary-key GUID). Use to read a specific known record before updating, deleting, or referencing it. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    entityType: DynamicsCRMCommon.entityType(
      "Select or map the entity name whose records you want to retrieve."
    ),
    recordId: DynamicsCRMCommon.recordId
  },
  async run(context) {
    let { entityType, recordId } = context.propsValue, entityUrlPath = entityType;
    return await makeClient(
      context.auth
    ).getRecord(entityUrlPath, recordId);
  }
});

// ../ap/packages/pieces/community/microsoft-dynamics-crm/src/lib/actions/update-record.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var updateRecordAction = (0, import_pieces_framework7.createAction)({
  auth: dynamicsCRMAuth,
  name: "dynamics_crm_update_record",
  displayName: "Update Record",
  description: "Updates an existing record.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing record of a chosen Dynamics 365 / Dataverse entity type, identified by its record ID (the entity primary-key GUID), applying the supplied field values. Use to modify a specific known record rather than create one. Idempotent: re-sending the same field values to the same ID leaves the record in the same final state.",
    idempotent: !0
  },
  props: {
    entityType: DynamicsCRMCommon.entityType(
      "Select or map the entity for which you want to update the record."
    ),
    recordId: DynamicsCRMCommon.recordId,
    fields: DynamicsCRMCommon.entityFields(!1)
  },
  async run(context) {
    let { entityType, recordId, fields } = context.propsValue, entityUrlPath = entityType;
    return await makeClient(
      context.auth
    ).updatedRecord(entityUrlPath, recordId, fields);
  }
});

// ../ap/packages/pieces/community/microsoft-dynamics-crm/src/index.ts
function getBaseUrl(host, proxyUrl) {
  return proxyUrl && proxyUrl !== "" ? proxyUrl : host;
}
var microsoftDynamicsCrm = (0, import_pieces_framework8.createPiece)({
  displayName: "Microsoft Dynamics CRM",
  auth: dynamicsCRMAuth,
  description: "Customer relationship management software package developed by Microsoft.",
  minimumSupportedRelease: "0.27.1",
  logoUrl: "https://cdn.activepieces.com/pieces/microsoft-dynamics-crm.png",
  authors: ["kishanprmr"],
  categories: [import_pieces_framework9.PieceCategory.SALES_AND_CRM],
  actions: [
    createRecordAction,
    deleteRecordAction,
    getRecordAction,
    updateRecordAction,
    (0, import_pieces_common3.createCustomApiCallAction)({
      auth: dynamicsCRMAuth,
      baseUrl: (auth) => {
        let props = auth == null ? void 0 : auth.props;
        return props ? `${getBaseUrl(
          props.hostUrl,
          props.proxyUrl
        )}/api/data/v9.2` : "";
      },
      authMapping: async (auth) => ({
        Authorization: `Bearer  ${auth.access_token}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  getBaseUrl,
  microsoftDynamicsCrm
});
