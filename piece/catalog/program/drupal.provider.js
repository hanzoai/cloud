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
var __objRest = (source, exclude) => {
  var target = {};
  for (var prop in source)
    __hasOwnProp.call(source, prop) && exclude.indexOf(prop) < 0 && (target[prop] = source[prop]);
  if (source != null && __getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(source))
      exclude.indexOf(prop) < 0 && __propIsEnum.call(source, prop) && (target[prop] = source[prop]);
  return target;
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

// ../ap/packages/pieces/community/drupal/src/index.ts
var index_exports = {};
__export(index_exports, {
  drupal: () => drupal2
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_framework13 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/drupal/src/lib/actions/services.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/drupal/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), markdownPropertyDescription = `
**Using Drupal's JSON:API**

Your Drupal site comes with JSON:API built-in. Authentication to access relevant parts requires the HTTP Basic Authentication module, which is also part of your Drupal site. Just ensure both are enabled and configure user authentication:

1. Enable the JSON:API and the HTTP Basic Authentication modules
2. Create a user account and give it the permissions you want Activepieces to have
3. Use that account's credentials for authentication

Provide the website URL in the format https://www.example.com.

For extra functionality, you can use the [Drupal Orchestration](https://www.drupal.org/project/orchestration) module.
`, drupalAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: markdownPropertyDescription,
  required: !0,
  props: {
    website_url: import_pieces_framework.Property.ShortText({
      displayName: "Website URL",
      required: !0,
      description: 'URL of your Drupal site without a trailing "/" character'
    }),
    username: import_pieces_framework.Property.ShortText({
      displayName: "Username",
      required: !0
    }),
    password: import_pieces_framework.Property.ShortText({
      displayName: "Password",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    let { website_url, username, password } = auth;
    if (!website_url || !username || !password)
      return {
        valid: !1,
        error: "Please fill all the fields [website_url, username, password]"
      };
    if (website_url.endsWith("/"))
      return {
        valid: !1,
        error: "The website URL must not end with a trailing slash."
      };
    try {
      let basicAuth = Buffer.from(`${username}:${password}`).toString("base64"), response = await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: website_url + "/jsonapi/user/user?filter[name]=" + username,
        headers: {
          Authorization: `Basic ${basicAuth}`,
          Accept: "application/vnd.api+json"
        }
      });
      if (response.status === 200) {
        let data = response.body.data;
        return {
          valid: data && data.length > 0 && data[0].attributes.name === username
        };
      }
      return {
        valid: !1,
        error: "Authentication failed. Please check your credentials."
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Connection failed: " + e.message
      };
    }
  }
});

// ../ap/packages/pieces/community/drupal/src/lib/actions/services.ts
var drupalCallServiceAction = (0, import_pieces_framework2.createAction)({
  auth: drupalAuth,
  name: "drupal-call-service",
  displayName: "Call Service",
  description: "Call a service on the Drupal site",
  audience: "both",
  aiMetadata: { description: "Executes a named server-side service (e.g. an ECA/orchestration action) exposed by the Drupal site, passing a dynamic configuration object whose fields are defined by the chosen service. Use to invoke custom Drupal-side logic that has no dedicated action here. The service must be discoverable via the site's orchestration endpoint; because it runs arbitrary server logic, treat each call as potentially side-effecting and not idempotent.", idempotent: !1 },
  props: {
    service: import_pieces_framework2.Property.Dropdown({
      displayName: "Service",
      description: "The service to call.",
      required: !0,
      refreshers: [],
      auth: drupalAuth,
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please authenticate first."
          };
        let { website_url, username, password } = auth.props;
        try {
          let response = await import_pieces_common2.httpClient.sendRequest({
            method: import_pieces_common2.HttpMethod.GET,
            url: website_url + "/orchestration/services",
            headers: {
              Authorization: `Basic ${Buffer.from(`${username}:${password}`).toString("base64")}`,
              Accept: "application/vnd.api+json"
            }
          });
          if (response.status === 200)
            return {
              disabled: !1,
              options: response.body.map((service) => ({
                label: service.label,
                description: service.description,
                value: service
              }))
            };
        } catch (e) {
          console.debug("Service error", e);
        }
        return {
          disabled: !0,
          options: [],
          placeholder: "Error processing services"
        };
      }
    }),
    config: import_pieces_framework2.Property.DynamicProperties({
      displayName: "Service configuration",
      refreshers: ["service"],
      required: !0,
      auth: drupalAuth,
      props: async ({ service }) => {
        let fields = {};
        return service.config.forEach((config) => {
          config.type === "boolean" ? fields[config.key] = import_pieces_framework2.Property.Checkbox({
            displayName: config.label,
            description: config.description,
            required: config.required,
            defaultValue: config.default_value
          }) : config.type === "integer" || config.type === "float" ? fields[config.key] = import_pieces_framework2.Property.Number({
            displayName: config.label,
            description: config.description,
            required: config.required,
            defaultValue: config.default_value
          }) : config.type === "timestamp" || config.type === "datetime_iso8601" ? fields[config.key] = import_pieces_framework2.Property.DateTime({
            displayName: config.label,
            description: config.description,
            required: config.required,
            defaultValue: config.default_value
          }) : config.options.length > 0 ? fields[config.key] = import_pieces_framework2.Property.StaticDropdown({
            displayName: config.label,
            description: config.description,
            required: config.required,
            defaultValue: config.default_value,
            options: {
              options: config.options.map((option) => ({
                label: option.name,
                value: option.key
              }))
            }
          }) : fields[config.key] = import_pieces_framework2.Property.ShortText({
            displayName: config.label,
            description: config.description,
            required: config.required,
            defaultValue: config.default_value
          });
        }), fields;
      }
    })
  },
  async run({ auth, propsValue }) {
    let { website_url, username, password } = auth.props, request = {
      method: import_pieces_common2.HttpMethod.POST,
      url: website_url + "/orchestration/service/execute",
      body: {
        id: propsValue.service.id,
        config: propsValue.config
      },
      headers: {
        Authorization: `Basic ${Buffer.from(`${username}:${password}`).toString("base64")}`,
        Accept: "application/vnd.api+json"
      }
    }, result = await import_pieces_common2.httpClient.sendRequest(request);
    return result.status === 200 || result.status === 202 ? result.body : result;
  }
});

// ../ap/packages/pieces/community/drupal/src/lib/actions/create_entity.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/drupal/src/lib/common/jsonapi.ts
var import_pieces_common3 = require("@activepieces/pieces-common");
async function makeJsonApiRequest(auth, endpoint, method = import_pieces_common3.HttpMethod.GET, body) {
  let { website_url, username, password } = auth.props, headers = {
    Accept: "application/vnd.api+json"
  };
  if (username && password) {
    let basicAuth = Buffer.from(`${username}:${password}`).toString("base64");
    headers.Authorization = `Basic ${basicAuth}`;
  }
  body && (headers["Content-Type"] = "application/vnd.api+json");
  try {
    let response = await import_pieces_common3.httpClient.sendRequest({
      method,
      url: endpoint,
      headers,
      body: body ? JSON.stringify(body) : void 0
    });
    if (response.body && typeof response.body == "string")
      try {
        let cleanedBody = response.body.replace(/[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]/g, "");
        response.body = JSON.parse(cleanedBody);
      } catch (parseError) {
        console.warn("Failed to parse JSON response, returning raw body:", parseError);
      }
    return response;
  } catch (error) {
    throw console.error("JSON:API request failed:", { endpoint, method, error }), error;
  }
}
function toJsonApiFormat(entityType, bundle, data, id) {
  let resourceType = `${entityType}--${bundle}`, attributes = {}, relationships = {}, isRelationship = (value) => value && typeof value == "object" && "data" in value;
  for (let [key, value] of Object.entries(data))
    isRelationship(value) ? relationships[key] = value : attributes[key] = value;
  let resource = {
    type: resourceType,
    attributes
  };
  return id && (resource.id = id), Object.keys(relationships).length > 0 && (resource.relationships = relationships), { data: resource };
}
function fromJsonApiFormat(response) {
  if (!response.data) return null;
  try {
    return Array.isArray(response.data) ? response.data.filter((resource) => resource != null).map(convertJsonApiResource) : convertJsonApiResource(response.data);
  } catch (error) {
    return [];
  }
}
function convertJsonApiResource(resource) {
  if (!resource || typeof resource != "object")
    throw new Error("Invalid resource: resource is not an object");
  if (!resource.type)
    throw new Error('Invalid resource: missing required "type" field');
  let result = {
    id: resource.id,
    type: resource.type
  };
  if (resource.attributes && typeof resource.attributes == "object")
    try {
      Object.assign(result, resource.attributes);
    } catch (error) {
      throw new Error(`Failed to copy attributes: ${error}`);
    }
  if (resource.relationships && typeof resource.relationships == "object")
    try {
      for (let [key, value] of Object.entries(resource.relationships))
        result[key] = value;
    } catch (error) {
      throw new Error(`Failed to copy relationships: ${error}`);
    }
  return result;
}
function buildQueryParams(options) {
  let params = new URLSearchParams();
  if (options.filters)
    for (let [key, value] of Object.entries(options.filters))
      params.append(`filter[${key}]`, String(value));
  if (options.sort) {
    let sortParam = options.sortDirection === "ASC" ? options.sort : `-${options.sort}`;
    params.append("sort", sortParam);
  }
  if (options.fields && options.resourceType) {
    let fieldsParam = options.fields.join(",");
    params.append(`fields[${options.resourceType}]`, fieldsParam);
  }
  options.limit && options.limit > 0 && params.append("page[limit]", String(options.limit));
  let queryString = params.toString();
  return queryString ? `?${queryString}` : "";
}
var jsonApi = {
  /**
   * Fetch a single resource by full JSON:API path
   * @example jsonApi.get(auth, '/jsonapi/node/node--article/12345')
   */
  async get(auth, resourcePath) {
    let url = `${auth.props.website_url.replace(/\/+$/, "")}${resourcePath}`, result = await makeJsonApiRequest(auth, url, import_pieces_common3.HttpMethod.GET);
    if (result.status === 200)
      return fromJsonApiFormat(result.body);
    throw result.status === 404 ? new Error(`Resource not found: ${resourcePath}`) : result.status === 403 ? new Error(`Access denied: ${resourcePath}`) : new Error(`Failed to get resource: ${result.status}`);
  },
  /**
   * Fetch a collection of resources with optional query parameters
   * Follows pagination links if present to retrieve all requested data
   * @example jsonApi.list(auth, '/jsonapi/node/node--article', { sort: 'created', filters: { status: '1' } })
   */
  async list(auth, collectionPath, options) {
    var _a;
    let allEntities = [], query = options ? buildQueryParams(options) : "", url = `${auth.props.website_url.replace(/\/+$/, "")}${collectionPath}${query}`;
    do {
      let result = await makeJsonApiRequest(auth, url, import_pieces_common3.HttpMethod.GET);
      if (result.status !== 200)
        throw new Error(`Failed to list resources: ${result.status}`);
      if (!result.body)
        break;
      let parsedBody;
      if (typeof result.body == "string")
        try {
          parsedBody = JSON.parse(result.body);
        } catch (parseError) {
          console.warn("Skipping page due to corrupted data in Drupal database:", parseError), url = null;
          break;
        }
      else
        parsedBody = result.body;
      let response = parsedBody, entities = fromJsonApiFormat(response);
      Array.isArray(entities) ? allEntities.push(...entities) : entities && allEntities.push(entities);
      let nextLink = (_a = response.links) == null ? void 0 : _a.next;
      url = typeof nextLink == "string" ? nextLink : (nextLink == null ? void 0 : nextLink.href) || null;
    } while (url);
    return allEntities;
  },
  /**
   * Create a new resource with JSON:API formatted data
   * @example jsonApi.create(auth, '/jsonapi/node/node--article', jsonApiFormattedData)
   */
  async create(auth, collectionPath, jsonApiData) {
    let url = `${auth.props.website_url.replace(/\/+$/, "")}${collectionPath}`, result = await makeJsonApiRequest(auth, url, import_pieces_common3.HttpMethod.POST, jsonApiData);
    if (result.status === 201 || result.status === 200)
      return fromJsonApiFormat(result.body);
    if (result.status === 422) {
      let errorMsg = (result.body.errors || []).map((e) => e.detail || e.title).join(", ");
      throw new Error(`Validation failed: ${errorMsg}`);
    } else if (result.status === 403)
      throw new Error("Permission denied to create resource");
    throw new Error(`Failed to create resource: ${result.status}`);
  },
  /**
   * Update a resource with JSON:API formatted data
   * @example jsonApi.update(auth, '/jsonapi/node/node--article/12345', jsonApiFormattedData)
   */
  async update(auth, resourcePath, jsonApiData) {
    let url = `${auth.props.website_url.replace(/\/+$/, "")}${resourcePath}`, result = await makeJsonApiRequest(auth, url, import_pieces_common3.HttpMethod.PATCH, jsonApiData);
    if (result.status === 200)
      return fromJsonApiFormat(result.body);
    if (result.status === 422) {
      let errorMsg = (result.body.errors || []).map((e) => e.detail || e.title).join(", ");
      throw new Error(`Validation failed: ${errorMsg}`);
    } else {
      if (result.status === 404)
        throw new Error(`Resource not found: ${resourcePath}`);
      if (result.status === 403)
        throw new Error("Permission denied to update resource");
    }
    throw new Error(`Failed to update resource: ${result.status}`);
  },
  /**
   * Delete a resource
   * @example jsonApi.delete(auth, '/jsonapi/node/node--article/12345')
   */
  async delete(auth, resourcePath) {
    let url = `${auth.props.website_url.replace(/\/+$/, "")}${resourcePath}`, result = await makeJsonApiRequest(auth, url, import_pieces_common3.HttpMethod.DELETE);
    if (result.status === 204 || result.status === 200)
      return { success: !0, message: `Deleted resource: ${resourcePath}` };
    throw result.status === 404 ? new Error(`Resource not found: ${resourcePath}`) : result.status === 403 ? new Error("Permission denied to delete resource") : new Error(`Failed to delete resource: ${result.status}`);
  }
}, drupal = {
  /**
   * Get a single Drupal entity by entity type, bundle, and UUID
   * @example drupal.getEntity(auth, 'node', 'article', '12345-uuid')
   */
  async getEntity(auth, entityType, bundle, uuid) {
    let resourcePath = `/jsonapi/${entityType}/${bundle}/${uuid}`;
    return await jsonApi.get(auth, resourcePath);
  },
  /**
   * List Drupal entities with optional filtering, sorting, and field selection
   * @example drupal.listEntities(auth, 'node', 'article', { filters: { status: '1' }, sort: 'created' })
   */
  async listEntities(auth, entityType, bundle, options) {
    let collectionPath = `/jsonapi/${entityType}/${bundle}`, queryOptions = options ? __spreadProps(__spreadValues({}, options), {
      resourceType: `${entityType}--${bundle}`
    }) : void 0;
    return await jsonApi.list(auth, collectionPath, queryOptions);
  },
  /**
   * Create a new Drupal entity with simple object data (automatically converts to JSON:API format)
   * @example drupal.createEntity(auth, 'node', 'article', { title: 'My Article', body: 'Content...' })
   */
  async createEntity(auth, entityType, bundle, entityData) {
    let collectionPath = `/jsonapi/${entityType}/${bundle}`, jsonApiData = toJsonApiFormat(entityType, bundle, entityData);
    return await jsonApi.create(auth, collectionPath, jsonApiData);
  },
  /**
   * Update a Drupal entity with simple object data (automatically converts to JSON:API format)
   * @example drupal.updateEntity(auth, 'node', 'article', '12345-uuid', { title: 'Updated Title' })
   */
  async updateEntity(auth, entityType, bundle, uuid, entityData) {
    let resourcePath = `/jsonapi/${entityType}/${bundle}/${uuid}`, jsonApiData = toJsonApiFormat(entityType, bundle, entityData, uuid);
    return await jsonApi.update(auth, resourcePath, jsonApiData);
  },
  /**
   * Delete a Drupal entity
   * @example drupal.deleteEntity(auth, 'node', 'article', '12345-uuid')
   */
  async deleteEntity(auth, entityType, bundle, uuid) {
    let resourcePath = `/jsonapi/${entityType}/${bundle}/${uuid}`;
    return await jsonApi.delete(auth, resourcePath);
  }
};

// ../ap/packages/pieces/community/drupal/src/lib/common/drupal-entities.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");
async function fetchEntityTypes(auth, context) {
  if (!auth || !auth.props.website_url)
    return {
      disabled: !0,
      options: [],
      placeholder: "Please configure authentication first"
    };
  let type = context === "editing" ? "form" : "view", data = (await makeJsonApiRequest(auth, `${auth.props.website_url}/jsonapi/entity_${type}_display/entity_${type}_display`, import_pieces_common4.HttpMethod.GET)).body.data || [], allowedEntityTypes = [];
  data.forEach((entityType) => {
    allowedEntityTypes.push(entityType.attributes.targetEntityType + "--" + entityType.attributes.bundle);
  });
  try {
    let response2 = await makeJsonApiRequest(auth, `${auth.props.website_url}/jsonapi`, import_pieces_common4.HttpMethod.GET);
    if (response2.status === 200) {
      let entityTypes = [], data2 = response2.body;
      if (data2.links) {
        for (let [key, value] of Object.entries(data2.links))
          if (key !== "self" && typeof value == "object" && value.href) {
            let parts = key.split("--");
            if (parts.length === 2) {
              let [entityType, bundle] = parts;
              if (allowedEntityTypes.includes(key)) {
                let bundleName = bundle.charAt(0).toUpperCase() + bundle.slice(1).replace(/_/g, " "), entityTypeName = entityType.charAt(0).toUpperCase() + entityType.slice(1).replace(/_/g, " "), label = `${bundleName} (${entityTypeName})`;
                entityTypes.push({
                  label,
                  value: {
                    id: key,
                    entity_type: entityType,
                    bundle
                  }
                });
              }
            }
          }
      }
      return {
        disabled: !1,
        options: entityTypes.sort((a, b) => a.label.localeCompare(b.label))
      };
    }
  } catch (e) {
    console.error("Failed to fetch entity types", e);
  }
  return {
    disabled: !0,
    options: [],
    placeholder: "Error loading entity types"
  };
}
async function fetchEntityTypesForReading(auth) {
  return auth ? await fetchEntityTypes(auth, "reading") : {
    disabled: !0,
    options: [],
    placeholder: "Please configure authentication first"
  };
}
async function fetchEntityTypesForEditing(auth) {
  return auth ? await fetchEntityTypes(auth, "editing") : {
    disabled: !0,
    options: [],
    placeholder: "Please configure authentication first"
  };
}
async function fetchEntityFormDisplay(auth, entityType, bundle) {
  try {
    let formDisplayId = `${entityType}.${bundle}.default`, response = await makeJsonApiRequest(
      auth,
      `${auth.props.website_url}/jsonapi/entity_form_display/entity_form_display?filter[drupal_internal__id]=${encodeURIComponent(formDisplayId)}`,
      import_pieces_common4.HttpMethod.GET
    );
    if (response.status === 200 && response.body) {
      let data = response.body.data;
      if (data && data.length > 0)
        return data[0].attributes.content || {};
    }
  } catch (e) {
    console.error("Failed to fetch form display", e);
  }
  return {};
}
async function fetchTextFormats(auth) {
  try {
    let response = await makeJsonApiRequest(
      auth,
      `${auth.props.website_url}/jsonapi/filter_format/filter_format`,
      import_pieces_common4.HttpMethod.GET
    );
    if (response.status === 200 && response.body)
      return (response.body.data || []).reduce((acc, format) => {
        let formatId = format.attributes.drupal_internal__format, formatName = format.attributes.name;
        return formatId && formatName && (acc[formatId] = formatName), acc;
      }, {});
  } catch (e) {
    console.error("Failed to fetch text formats", e);
  }
  return {};
}
async function fetchEntityFieldConfig(auth, entityType, bundle) {
  try {
    let response = await makeJsonApiRequest(
      auth,
      `${auth.props.website_url}/jsonapi/field_config/field_config?filter[entity_type]=${entityType}&filter[bundle]=${bundle}`,
      import_pieces_common4.HttpMethod.GET
    );
    if (response.status === 200 && response.body) {
      let fields = response.body.data || [], fieldConfig = {};
      return fields.forEach((field) => {
        let fieldName = field.attributes.field_name;
        fieldConfig[fieldName] = {
          label: field.attributes.label,
          required: field.attributes.required,
          fieldType: field.attributes.field_type
        };
      }), fieldConfig;
    }
  } catch (e) {
    console.error("Failed to fetch field config", e);
  }
  return {};
}
async function isEntitySupportingWorkflow(auth, entityType, bundle) {
  try {
    let response = await makeJsonApiRequest(
      auth,
      `${auth.props.website_url}/jsonapi/workflow/workflow`,
      import_pieces_common4.HttpMethod.GET
    );
    if (response.status === 200 && response.body) {
      let workflows = response.body.data || [], found = !1;
      return workflows.forEach((workflow) => {
        var _a, _b, _c;
        let attrs = (_a = workflow == null ? void 0 : workflow.attributes) != null ? _a : {}, typeSettings = (_b = attrs == null ? void 0 : attrs.type_settings) != null ? _b : {}, entityTypes = (_c = typeSettings == null ? void 0 : typeSettings.entity_types) != null ? _c : {};
        entityTypes && entityTypes[entityType].includes(bundle) && (found = !0);
      }), found;
    }
  } catch (e) {
  }
  return !1;
}
function isEditableFieldType(fieldType) {
  return [
    "string",
    "string_long",
    "text",
    "text_long",
    "text_with_summary",
    "integer",
    "decimal",
    "float",
    "boolean",
    "email",
    "telephone",
    "uri"
  ].includes(fieldType);
}
function getBaseFieldLabel(fieldName) {
  return {
    title: "Title",
    status: "Published",
    created: "Authored on",
    changed: "Changed",
    promote: "Promoted to front page",
    sticky: "Sticky at top of lists",
    name: "Name",
    mail: "Email address"
  }[fieldName] || fieldName;
}
async function getEditableFieldsWithLabels(auth, entityType, bundle, formDisplayContent) {
  let fieldConfig = await fetchEntityFieldConfig(auth, entityType, bundle), fields = [], baseFields = ["title", "name"];
  await isEntitySupportingWorkflow(auth, entityType, bundle) || baseFields.push("status");
  for (let [fieldName, config] of Object.entries(formDisplayContent))
    if (config && typeof config == "object" && config.type) {
      let configInfo = fieldConfig[fieldName];
      configInfo ? isEditableFieldType(configInfo.fieldType) && fields.push({
        name: fieldName,
        type: configInfo.fieldType,
        label: configInfo.label,
        required: configInfo.required,
        weight: config.weight || 0
      }) : baseFields.includes(fieldName) && fields.push({
        name: fieldName,
        type: fieldName === "status" ? "boolean" : "string",
        label: getBaseFieldLabel(fieldName),
        required: ["title", "name"].includes(fieldName),
        weight: config.weight || 0
      });
    }
  return fields.sort((a, b) => a.weight !== b.weight ? a.weight - b.weight : a.label.localeCompare(b.label));
}
async function buildFieldProperties(auth, entityType, isCreateAction = !1) {
  let properties = {};
  if (!entityType)
    return properties;
  try {
    let formDisplay = await fetchEntityFormDisplay(auth, entityType.entity_type, entityType.bundle), textFormats = await fetchTextFormats(auth), availableFields = await getEditableFieldsWithLabels(
      auth,
      entityType.entity_type,
      entityType.bundle,
      formDisplay
    );
    if (availableFields.length === 0)
      return properties.no_fields = import_pieces_framework3.Property.MarkDown({
        value: "No editable fields found for this entity type."
      }), properties;
    for (let field of availableFields) {
      let displayName = field.label, description = void 0, isRequired = field.required && isCreateAction;
      field.type === "text_with_summary" || field.type === "text_long" ? (properties[field.name] = import_pieces_framework3.Property.LongText({
        displayName,
        description,
        required: isRequired
      }), Object.keys(textFormats).length > 0 && (properties[`${field.name}_format`] = import_pieces_framework3.Property.StaticDropdown({
        displayName: `${displayName} Format`,
        required: !1,
        options: {
          options: Object.entries(textFormats).map(([key, name]) => ({
            label: String(name),
            value: key
          }))
        }
      }))) : field.type === "boolean" ? properties[field.name] = import_pieces_framework3.Property.Checkbox({
        displayName,
        description,
        required: isRequired
      }) : properties[field.name] = import_pieces_framework3.Property.ShortText({
        displayName,
        description,
        required: isRequired
      });
    }
  } catch (e) {
    console.error("Failed to generate field properties", e), properties.error = import_pieces_framework3.Property.MarkDown({
      value: "Failed to load fields. Please check your authentication and entity type selection."
    });
  }
  return properties;
}

// ../ap/packages/pieces/community/drupal/src/lib/actions/create_entity.ts
var drupalCreateEntityAction = (0, import_pieces_framework4.createAction)({
  auth: drupalAuth,
  name: "drupal-create-entity",
  displayName: "Create Entity",
  description: "Create a new entity in Drupal with smart field discovery and validation",
  audience: "both",
  aiMetadata: { description: "Creates a new content entity (node, taxonomy term, user, etc.) of a chosen entity type and bundle in Drupal via JSON:API. Available fields are discovered from the selected entity type, and at least one field value must be supplied. Use when adding new content; each call creates a distinct entity, so it is not idempotent.", idempotent: !1 },
  props: {
    entity_type: import_pieces_framework4.Property.Dropdown({
      auth: drupalAuth,
      displayName: "Entity Type",
      description: "Choose the type of content to create.",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => fetchEntityTypesForEditing(auth)
    }),
    entity_fields: import_pieces_framework4.Property.DynamicProperties({
      auth: drupalAuth,
      displayName: "Entity Fields",
      description: "Fill in the content fields. Available fields depend on the entity type selected above.",
      required: !1,
      refreshers: ["entity_type"],
      props: async (propsValue) => {
        let { auth, entity_type } = propsValue;
        return auth ? buildFieldProperties(auth, entity_type, !0) : {
          disabled: !0,
          options: [],
          placeholder: "Please configure authentication first"
        };
      }
    })
  },
  async run({ auth, propsValue }) {
    let entityInfo = propsValue.entity_type, fieldsData = propsValue.entity_fields, fieldsToCreate = {}, processedFormatFields = /* @__PURE__ */ new Set();
    for (let [key, value] of Object.entries(fieldsData))
      if (!(value == null || value === "" || processedFormatFields.has(key)))
        if (key.endsWith("_format")) {
          let textFieldName = key.replace("_format", ""), textValue = fieldsData[textFieldName];
          textValue && (fieldsToCreate[textFieldName] = {
            value: textValue,
            format: value
          }, processedFormatFields.add(textFieldName)), processedFormatFields.add(key);
        } else {
          let formatKey = `${key}_format`, formatValue = fieldsData[formatKey];
          formatValue && formatValue !== "undefined" ? (fieldsToCreate[key] = {
            value,
            format: formatValue
          }, processedFormatFields.add(formatKey)) : fieldsToCreate[key] = value;
        }
    if (Object.keys(fieldsToCreate).length === 0)
      throw new Error("At least one field must be provided to create an entity");
    return await drupal.createEntity(
      auth,
      entityInfo.entity_type,
      entityInfo.bundle,
      fieldsToCreate
    );
  }
});

// ../ap/packages/pieces/community/drupal/src/lib/actions/list_entities.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var drupalListEntitiesAction = (0, import_pieces_framework5.createAction)({
  auth: drupalAuth,
  name: "drupal-list-entities",
  displayName: "List Entities",
  description: "List entities from Drupal using JSON:API",
  audience: "both",
  aiMetadata: { description: "Lists/retrieves entities of a chosen type and bundle from Drupal via JSON:API, with optional filtering by published status, sorting, and a result limit (limit 0 returns all entities, following pagination). Use to query or scan existing content rather than fetch one known item. Read-only and idempotent.", idempotent: !0 },
  props: {
    entity_type: import_pieces_framework5.Property.Dropdown({
      displayName: "Entity Type",
      description: "Choose what type of content to list.",
      required: !0,
      refreshers: [],
      auth: drupalAuth,
      options: async ({ auth }) => fetchEntityTypesForReading(auth)
    }),
    published_status: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Published Status",
      description: "Filter by publication status",
      required: !1,
      defaultValue: "all",
      options: {
        options: [
          { label: "All", value: "all" },
          { label: "Published only", value: "published" },
          { label: "Unpublished only", value: "unpublished" }
        ]
      }
    }),
    sort_by: import_pieces_framework5.Property.DynamicProperties({
      displayName: "Sort Options",
      description: "Choose how to sort the entities",
      required: !1,
      refreshers: ["entity_type"],
      auth: drupalAuth,
      props: async (propsValue) => {
        let entityInfo = propsValue.entity_type;
        if (!entityInfo) return {};
        let sortOptions = [];
        return entityInfo.entity_type === "taxonomy_term" ? sortOptions = [
          { label: "Updated date", value: "changed" },
          { label: "Name", value: "name" }
        ] : entityInfo.entity_type === "user" ? sortOptions = [
          { label: "Creation date", value: "created" },
          { label: "Updated date", value: "changed" },
          { label: "Name", value: "name" }
        ] : sortOptions = [
          { label: "Creation date", value: "created" },
          { label: "Updated date", value: "changed" },
          { label: "Title", value: "title" }
        ], {
          sort_field: import_pieces_framework5.Property.StaticDropdown({
            displayName: "Sort By",
            required: !1,
            defaultValue: sortOptions[0].value,
            options: { options: sortOptions }
          })
        };
      }
    }),
    sort_direction: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Sort Direction",
      required: !1,
      defaultValue: "DESC",
      options: {
        options: [
          { label: "Newest first", value: "DESC" },
          { label: "Oldest first", value: "ASC" }
        ]
      }
    }),
    limit: import_pieces_framework5.Property.Number({
      displayName: "Limit",
      description: "Maximum number of entities to retrieve (0 = all entities)",
      required: !0,
      defaultValue: 50
    }),
    output_options: import_pieces_framework5.Property.DynamicProperties({
      displayName: "Output Options",
      required: !1,
      refreshers: ["entity_type"],
      auth: drupalAuth,
      props: async (propsValue) => {
        let entityInfo = propsValue.entity_type;
        return entityInfo ? entityInfo.entity_type === "node" ? {
          minimal_output: import_pieces_framework5.Property.Checkbox({
            displayName: "Minimal Output",
            description: "Return only basic fields (UUID, title, status, dates) instead of all entity data",
            required: !1,
            defaultValue: !0
          })
        } : {} : {};
      }
    })
  },
  async run({ auth, propsValue }) {
    var _a;
    let entityInfo = propsValue.entity_type, filters = {};
    propsValue.published_status === "published" ? filters.status = "1" : propsValue.published_status === "unpublished" && (filters.status = "0");
    let fields, outputOptions = propsValue.output_options;
    entityInfo.entity_type === "node" && (outputOptions != null && outputOptions.minimal_output) ? fields = ["id", "title", "status", "created", "changed", "drupal_internal__nid", "path"] : entityInfo.entity_type === "taxonomy_term" ? fields = ["id", "name", "changed", "created", "path", "drupal_internal__tid", "status"] : entityInfo.entity_type === "user" && (fields = ["id", "name", "mail", "created", "changed", "status"]);
    let sortField = (_a = propsValue.sort_by) == null ? void 0 : _a.sort_field, entities = await drupal.listEntities(
      auth,
      entityInfo.entity_type,
      entityInfo.bundle,
      {
        filters,
        sort: sortField,
        sortDirection: propsValue.sort_direction,
        fields,
        limit: propsValue.limit
      }
    ), removeType = (entity) => {
      let _a2 = entity, { type } = _a2;
      return __objRest(_a2, ["type"]);
    };
    return entities = Array.isArray(entities) ? entities.map(removeType) : removeType(entities), {
      entities: Array.isArray(entities) ? entities : [entities],
      count: Array.isArray(entities) ? entities.length : 1
    };
  }
});

// ../ap/packages/pieces/community/drupal/src/lib/actions/get_entity.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var drupalGetEntityAction = (0, import_pieces_framework6.createAction)({
  auth: drupalAuth,
  name: "drupal-get-entity",
  displayName: "Get Entity",
  description: "Retrieve a single entity by UUID",
  audience: "both",
  aiMetadata: { description: "Retrieves a single Drupal entity by its entity type, bundle, and UUID via JSON:API. Use when the exact UUID is known; to find entities by criteria use List Entities instead. Read-only and idempotent.", idempotent: !0 },
  props: {
    entity_type: import_pieces_framework6.Property.Dropdown({
      displayName: "Entity Type",
      description: "Choose the type of content to retrieve.",
      required: !0,
      refreshers: [],
      auth: drupalAuth,
      options: async ({ auth }) => fetchEntityTypesForReading(auth)
    }),
    entity_uuid: import_pieces_framework6.Property.ShortText({
      displayName: "Entity UUID",
      description: "The unique identifier (UUID) of the specific content item to retrieve.",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let entityInfo = propsValue.entity_type;
    return await drupal.getEntity(
      auth,
      entityInfo.entity_type,
      entityInfo.bundle,
      propsValue.entity_uuid
    );
  }
});

// ../ap/packages/pieces/community/drupal/src/lib/actions/update_entity.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var drupalUpdateEntityAction = (0, import_pieces_framework7.createAction)({
  auth: drupalAuth,
  name: "drupal-update-entity",
  displayName: "Update Entity",
  description: "Update an existing entity in Drupal with smart field discovery and validation",
  audience: "both",
  aiMetadata: { description: "Updates an existing Drupal entity identified by its entity type, bundle, and UUID via a JSON:API PATCH, changing only the fields you provide (empty fields are left untouched); at least one field value is required. Use to modify known content. Idempotent: re-applying the same field values to the same UUID yields the same end state.", idempotent: !0 },
  props: {
    entity_type: import_pieces_framework7.Property.Dropdown({
      auth: drupalAuth,
      displayName: "Entity Type",
      description: "Select the entity type and bundle",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => fetchEntityTypesForEditing(auth)
    }),
    entity_uuid: import_pieces_framework7.Property.ShortText({
      displayName: "Entity UUID",
      description: "The UUID of the entity to update",
      required: !0
    }),
    entity_fields: import_pieces_framework7.Property.DynamicProperties({
      auth: drupalAuth,
      displayName: "Entity Fields",
      description: "Update the values for the entity fields (only provide values for fields you want to change)",
      required: !1,
      refreshers: ["entity_type"],
      props: async (propsValue) => {
        let { auth, entity_type } = propsValue;
        return auth ? buildFieldProperties(auth, entity_type, !1) : {
          disabled: !0,
          options: [],
          placeholder: "Please configure authentication first"
        };
      }
    })
  },
  async run({ auth, propsValue }) {
    let entityInfo = propsValue.entity_type, fieldsData = propsValue.entity_fields, fieldsToUpdate = {}, processedFormatFields = /* @__PURE__ */ new Set();
    for (let [key, value] of Object.entries(fieldsData))
      if (!(value == null || value === "" || processedFormatFields.has(key)))
        if (key.endsWith("_format")) {
          let textFieldName = key.replace("_format", ""), textValue = fieldsData[textFieldName];
          textValue && (fieldsToUpdate[textFieldName] = {
            value: textValue,
            format: value
          }, processedFormatFields.add(textFieldName)), processedFormatFields.add(key);
        } else {
          let formatKey = `${key}_format`, formatValue = fieldsData[formatKey];
          formatValue && formatValue !== void 0 && formatValue !== null && formatValue !== "" ? (fieldsToUpdate[key] = {
            value,
            format: formatValue
          }, processedFormatFields.add(formatKey)) : fieldsToUpdate[key] = value;
        }
    if (Object.keys(fieldsToUpdate).length === 0)
      throw new Error("No fields provided to update");
    return await drupal.updateEntity(
      auth,
      entityInfo.entity_type,
      entityInfo.bundle,
      propsValue.entity_uuid,
      fieldsToUpdate
    );
  }
});

// ../ap/packages/pieces/community/drupal/src/lib/actions/delete_entity.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var drupalDeleteEntityAction = (0, import_pieces_framework8.createAction)({
  auth: drupalAuth,
  name: "drupal-delete-entity",
  displayName: "Delete Entity",
  description: "Delete an entity from Drupal",
  audience: "both",
  aiMetadata: { description: "Permanently deletes a Drupal entity identified by its entity type, bundle, and UUID via JSON:API. Use to remove known content. Not idempotent: the first call deletes the entity and a repeat call for the same UUID fails because the resource no longer exists.", idempotent: !1 },
  props: {
    entity_type: import_pieces_framework8.Property.Dropdown({
      displayName: "Entity Type",
      description: "Choose the type of content to delete.",
      required: !0,
      refreshers: [],
      auth: drupalAuth,
      options: async ({ auth }) => fetchEntityTypesForReading(auth)
    }),
    entity_uuid: import_pieces_framework8.Property.ShortText({
      displayName: "Entity UUID",
      description: "The unique identifier (UUID) of the specific content item to delete.",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let entityInfo = propsValue.entity_type;
    return await drupal.deleteEntity(
      auth,
      entityInfo.entity_type,
      entityInfo.bundle,
      propsValue.entity_uuid
    );
  }
});

// ../ap/packages/pieces/community/drupal/src/lib/triggers/polling-id.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var polling = {
  strategy: import_pieces_common5.DedupeStrategy.LAST_ITEM,
  items: async ({ auth, propsValue, lastItemId }) => {
    lastItemId == null && (lastItemId = "0"), console.debug("Polling by ID", propsValue.name, lastItemId);
    let { website_url, username, password } = auth.props, body = {
      name: propsValue.name,
      id: lastItemId
    };
    return (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.POST,
      url: website_url + "/orchestration/poll",
      body,
      headers: {
        Authorization: `Basic ${Buffer.from(`${username}:${password}`).toString("base64")}`,
        Accept: "application/vnd.api+json"
      }
    })).body.reverse().map((item) => ({
      id: item.id,
      data: item.data
    }));
  }
}, drupalPollingId = (0, import_pieces_framework9.createTrigger)({
  auth: drupalAuth,
  name: "drupalPollingId",
  displayName: "Polling by ID",
  description: "A trigger that polls the Drupal site by ID.",
  aiMetadata: {
    description: "Fires for each new item pushed to a named Drupal orchestration poll, tracked by an incrementing item ID so only items newer than the last seen ID are emitted. Fires once per new item; the poll name must match the name configured on the Drupal side (e.g. an ECA poll event)."
  },
  props: {
    name: import_pieces_framework9.Property.ShortText({
      displayName: "Name",
      description: "The name identifies the poll. It must be unique. It will be used to identify the poll in the Drupal site, e.g. if you use ECA to respond to this poll, you need to use the same name in the configuration of its poll event.",
      required: !0
    })
  },
  sampleData: {},
  type: import_pieces_framework9.TriggerStrategy.POLLING,
  async test(context) {
    let { auth, propsValue, store, files } = context;
    return await import_pieces_common5.pollingHelper.test(polling, { store, auth, propsValue, files });
  },
  async onEnable(context) {
    let { auth, propsValue, store } = context;
    await import_pieces_common5.pollingHelper.onEnable(polling, { store, auth, propsValue });
  },
  async onDisable(context) {
    let { auth, propsValue, store } = context;
    await import_pieces_common5.pollingHelper.onDisable(polling, { store, auth, propsValue });
  },
  async run(context) {
    let { auth, propsValue, store, files } = context;
    return await import_pieces_common5.pollingHelper.poll(polling, { store, auth, propsValue, files });
  }
});

// ../ap/packages/pieces/community/drupal/src/lib/triggers/polling-timestamp.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var polling2 = {
  strategy: import_pieces_common6.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => {
    lastFetchEpochMS == null && (lastFetchEpochMS = 0), console.debug("Polling by timestamp", propsValue.name, lastFetchEpochMS);
    let { website_url, username, password } = auth.props, body = {
      name: propsValue.name,
      timestamp: lastFetchEpochMS / 1e3
    };
    return (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.POST,
      url: website_url + "/orchestration/poll",
      body,
      headers: {
        Authorization: `Basic ${Buffer.from(`${username}:${password}`).toString("base64")}`,
        Accept: "application/vnd.api+json"
      }
    })).body.map((item) => ({
      epochMilliSeconds: item.timestamp * 1e3,
      data: item.data
    }));
  }
}, drupalPollingTimestamp = (0, import_pieces_framework10.createTrigger)({
  auth: drupalAuth,
  name: "drupalPollingTimestamp",
  displayName: "Polling by timestamp",
  description: "A trigger that polls the Drupal site by timestamp.",
  aiMetadata: {
    description: "Fires for each new item pushed to a named Drupal orchestration poll, tracked by timestamp so only items created after the last poll are emitted. Use the time-based variant when items are dated rather than sequentially numbered; the poll name must match the name configured on the Drupal side (e.g. an ECA poll event)."
  },
  props: {
    name: import_pieces_framework10.Property.ShortText({
      displayName: "Name",
      description: "The name identifies the poll. It must be unique. It will be used to identify the poll in the Drupal site, e.g. if you use ECA to respond to this poll, you need to use the same name in the configuration of its poll event.",
      required: !0
    })
  },
  sampleData: {},
  type: import_pieces_framework10.TriggerStrategy.POLLING,
  async test(context) {
    let { auth, propsValue, store, files } = context;
    return await import_pieces_common6.pollingHelper.test(polling2, { store, auth, propsValue, files });
  },
  async onEnable(context) {
    let { auth, propsValue, store } = context;
    await import_pieces_common6.pollingHelper.onEnable(polling2, { store, auth, propsValue });
  },
  async onDisable(context) {
    let { auth, propsValue, store } = context;
    await import_pieces_common6.pollingHelper.onDisable(polling2, { store, auth, propsValue });
  },
  async run(context) {
    let { auth, propsValue, store, files } = context;
    return await import_pieces_common6.pollingHelper.poll(polling2, { store, auth, propsValue, files });
  }
});

// ../ap/packages/pieces/community/drupal/src/lib/triggers/webhook.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework11 = require("@activepieces/pieces-framework");
var webhookStoreKey = (id) => `_drupal_webhook_trigger_${id}`, drupalWebhook = (0, import_pieces_framework11.createTrigger)({
  auth: drupalAuth,
  name: "drupalWebhook",
  displayName: "Webhook",
  description: "A webhook that the Drupal site can call to trigger a flow.",
  aiMetadata: {
    description: "Fires in real time whenever the Drupal site calls the registered webhook URL (e.g. from an ECA action), emitting the request body sent by Drupal. Use for push-based events instead of polling; the webhook is registered on enable under the unique name supplied, which must match the name referenced on the Drupal side."
  },
  props: {
    id: import_pieces_framework11.Property.ShortText({
      displayName: "Name",
      description: "This name identifies the webhook. It must be unique. It will be used to identify the webhook in the Drupal site, e.g. if you use ECA to call this webhook, you will find this name in the list of available webhooks.",
      required: !0
    })
  },
  sampleData: {},
  type: import_pieces_framework11.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let { website_url, username, password } = context.auth.props, body = {
      id: context.propsValue.id,
      webHookUrl: context.webhookUrl
    }, response = await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.POST,
      url: website_url + "/orchestration/webhook/register",
      body,
      headers: {
        Authorization: `Basic ${Buffer.from(`${username}:${password}`).toString("base64")}`,
        Accept: "application/vnd.api+json"
      }
    });
    await context.store.put(webhookStoreKey(context.propsValue.id), response.body);
  },
  async onDisable(context) {
    let { website_url, username, password } = context.auth.props, webhook = await context.store.get(webhookStoreKey(context.propsValue.id));
    webhook && await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.POST,
      url: website_url + "/orchestration/webhook/unregister",
      body: webhook,
      headers: {
        Authorization: `Basic ${Buffer.from(`${username}:${password}`).toString("base64")}`,
        Accept: "application/vnd.api+json"
      }
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/drupal/src/index.ts
var drupal2 = (0, import_pieces_framework12.createPiece)({
  displayName: "Drupal",
  auth: drupalAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/drupal.png",
  categories: [
    import_pieces_framework13.PieceCategory.BUSINESS_INTELLIGENCE,
    import_pieces_framework13.PieceCategory.COMMERCE,
    import_pieces_framework13.PieceCategory.CONTENT_AND_FILES,
    import_pieces_framework13.PieceCategory.FORMS_AND_SURVEYS,
    import_pieces_framework13.PieceCategory.MARKETING
  ],
  authors: ["dbuytaert", "jurgenhaas"],
  actions: [
    drupalCallServiceAction,
    drupalCreateEntityAction,
    drupalListEntitiesAction,
    drupalGetEntityAction,
    drupalUpdateEntityAction,
    drupalDeleteEntityAction
  ],
  triggers: [drupalPollingId, drupalPollingTimestamp, drupalWebhook]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  drupal
});
