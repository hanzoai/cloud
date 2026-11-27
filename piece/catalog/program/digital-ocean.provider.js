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

// ../ap/packages/pieces/community/digital-ocean/src/index.ts
var index_exports = {};
__export(index_exports, {
  digitalOcean: () => digitalOcean
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common13 = require("@activepieces/pieces-common"), import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_framework15 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/digital-ocean/src/lib/common/auth.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/digital-ocean/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework"), BASE_URL = "https://api.digitalocean.com/v2";
function getAccessToken(auth) {
  switch (auth.type) {
    case import_pieces_framework.AppConnectionType.OAUTH2:
    case import_pieces_framework.AppConnectionType.CLOUD_OAUTH2:
    case import_pieces_framework.AppConnectionType.PLATFORM_OAUTH2:
      return auth.access_token;
    case import_pieces_framework.AppConnectionType.SECRET_TEXT:
      return auth.secret_text;
  }
}
function getAuthFromValue(auth) {
  if (typeof auth == "string")
    return {
      type: import_pieces_framework.AppConnectionType.SECRET_TEXT,
      secret_text: auth
    };
  let authObj = auth;
  if (authObj.access_token)
    return {
      type: import_pieces_framework.AppConnectionType.OAUTH2,
      access_token: authObj.access_token
    };
  throw new Error("Invalid authentication value");
}
async function digitalOceanApiCall({
  method,
  path,
  auth,
  query,
  body
}) {
  let queryParams = {};
  if (query)
    for (let [key, value] of Object.entries(query))
      value !== void 0 && (queryParams[key] = String(value));
  let request = {
    method,
    url: `${BASE_URL}${path}`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token: getAccessToken(auth)
    },
    headers: {
      "Content-Type": "application/json"
    },
    queryParams: Object.keys(queryParams).length > 0 ? queryParams : void 0,
    body
  };
  return (await import_pieces_common.httpClient.sendRequest(request)).body;
}

// ../ap/packages/pieces/community/digital-ocean/src/lib/common/auth.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), digitalOceanAuth = [
  import_pieces_framework2.PieceAuth.OAuth2({
    description: "Connect your DigitalOcean account using OAuth2.",
    authUrl: "https://cloud.digitalocean.com/v1/oauth/authorize",
    tokenUrl: "https://cloud.digitalocean.com/v1/oauth/token",
    required: !0,
    scope: [],
    validate: async ({ auth }) => {
      try {
        return await digitalOceanApiCall({
          method: import_pieces_common2.HttpMethod.GET,
          path: "/account",
          auth: {
            type: import_pieces_framework3.AppConnectionType.OAUTH2,
            access_token: auth.access_token
          }
        }), { valid: !0 };
      } catch (e) {
        return {
          valid: !1,
          error: e.message
        };
      }
    }
  }),
  import_pieces_framework2.PieceAuth.SecretText({
    displayName: "Personal Access Token",
    required: !0,
    description: "Generate a Personal Access Token from DigitalOcean Control Panel under API > Personal access tokens.",
    validate: async ({ auth }) => {
      try {
        return await digitalOceanApiCall({
          method: import_pieces_common2.HttpMethod.GET,
          path: "/account",
          auth: {
            type: import_pieces_framework3.AppConnectionType.SECRET_TEXT,
            secret_text: auth
          }
        }), { valid: !0 };
      } catch (e) {
        return {
          valid: !1,
          error: e.message
        };
      }
    }
  })
];

// ../ap/packages/pieces/community/digital-ocean/src/lib/actions/list-domains.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), listDomains = (0, import_pieces_framework4.createAction)({
  auth: digitalOceanAuth,
  name: "list_domains",
  displayName: "List All Domains",
  description: "Retrieve a list of all domains in your account.",
  audience: "both",
  aiMetadata: { description: "Lists all DNS domains managed under the authenticated DigitalOcean account, paginated. Use to discover available domains or look one up by name before acting on it. Read-only and idempotent; supports per-page and page controls.", idempotent: !0 },
  props: {
    per_page: import_pieces_framework4.Property.Number({
      displayName: "Results Per Page",
      description: "Number of domains to return per page (1-200).",
      required: !1,
      defaultValue: 20
    }),
    page: import_pieces_framework4.Property.Number({
      displayName: "Page",
      description: "Which page of results to return.",
      required: !1,
      defaultValue: 1
    })
  },
  async run(context) {
    let { per_page, page } = context.propsValue;
    return await digitalOceanApiCall({
      method: import_pieces_common3.HttpMethod.GET,
      path: "/domains",
      auth: getAuthFromValue(context.auth),
      query: {
        per_page: per_page != null ? per_page : 20,
        page: page != null ? page : 1
      }
    });
  }
});

// ../ap/packages/pieces/community/digital-ocean/src/lib/actions/create-domain.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common"), createDomain = (0, import_pieces_framework5.createAction)({
  auth: digitalOceanAuth,
  name: "create_domain",
  displayName: "Create Domain",
  description: "Add a new domain to your DigitalOcean account.",
  audience: "both",
  aiMetadata: { description: "Registers a new DNS domain on the authenticated DigitalOcean account; optionally creates an apex A record when an IP address is supplied. Use when onboarding a domain to DigitalOcean DNS. Not idempotent \u2014 re-running with an existing domain name fails, and each successful call mutates account state.", idempotent: !1 },
  props: {
    name: import_pieces_framework5.Property.ShortText({
      displayName: "Domain Name",
      description: "The domain name (e.g., example.com).",
      required: !0
    }),
    ip_address: import_pieces_framework5.Property.ShortText({
      displayName: "IP Address",
      description: "Optional IP address to create an A record pointing to the apex domain.",
      required: !1
    })
  },
  async run(context) {
    let { name, ip_address } = context.propsValue, body = { name };
    return ip_address && (body.ip_address = ip_address), await digitalOceanApiCall({
      method: import_pieces_common4.HttpMethod.POST,
      path: "/domains",
      auth: getAuthFromValue(context.auth),
      body
    });
  }
});

// ../ap/packages/pieces/community/digital-ocean/src/lib/actions/get-domain.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common"), getDomain = (0, import_pieces_framework6.createAction)({
  auth: digitalOceanAuth,
  name: "get_domain",
  displayName: "Get Domain",
  description: "Retrieve details about a specific domain.",
  audience: "both",
  aiMetadata: { description: "Fetches details for a single DNS domain on the DigitalOcean account, identified by its exact domain name. Use when you already know the domain name and need its TTL or zone file. Read-only and idempotent.", idempotent: !0 },
  props: {
    domain_name: import_pieces_framework6.Property.ShortText({
      displayName: "Domain Name",
      description: "The domain name to retrieve (e.g., example.com).",
      required: !0
    })
  },
  async run(context) {
    let { domain_name } = context.propsValue;
    return await digitalOceanApiCall({
      method: import_pieces_common5.HttpMethod.GET,
      path: `/domains/${encodeURIComponent(domain_name)}`,
      auth: getAuthFromValue(context.auth)
    });
  }
});

// ../ap/packages/pieces/community/digital-ocean/src/lib/actions/delete-domain.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common"), deleteDomain = (0, import_pieces_framework7.createAction)({
  auth: digitalOceanAuth,
  name: "delete_domain",
  displayName: "Delete Domain",
  description: "Remove a domain from your DigitalOcean account.",
  audience: "both",
  aiMetadata: { description: "Permanently deletes a DNS domain (and its records) from the DigitalOcean account, identified by exact domain name. Use to tear down DNS for a domain you no longer manage. Destructive; effectively idempotent on the end state but the first call removes the domain and later calls on a missing domain fail.", idempotent: !1 },
  props: {
    domain_name: import_pieces_framework7.Property.ShortText({
      displayName: "Domain Name",
      description: "The domain name to delete (e.g., example.com).",
      required: !0
    })
  },
  async run(context) {
    let { domain_name } = context.propsValue;
    return await digitalOceanApiCall({
      method: import_pieces_common6.HttpMethod.DELETE,
      path: `/domains/${encodeURIComponent(domain_name)}`,
      auth: getAuthFromValue(context.auth)
    }), {
      success: !0,
      message: `Domain '${domain_name}' has been deleted.`
    };
  }
});

// ../ap/packages/pieces/community/digital-ocean/src/lib/actions/list-droplets.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common"), listDroplets = (0, import_pieces_framework8.createAction)({
  auth: digitalOceanAuth,
  name: "list_droplets",
  displayName: "List All Droplets",
  description: "Retrieve a list of all Droplets in your account.",
  audience: "both",
  aiMetadata: { description: "Lists Droplets (virtual machines) on the authenticated DigitalOcean account, paginated. Returns all Droplets by default, or filters to a single tag, an exact name, or a Droplet type (standard vs. GPU); tag filtering cannot be combined with name or type. Use to discover Droplets or resolve a Droplet id before acting on it. Read-only and idempotent.", idempotent: !0 },
  props: {
    per_page: import_pieces_framework8.Property.Number({
      displayName: "Results Per Page",
      description: "Number of Droplets to return per page (1-200).",
      required: !1,
      defaultValue: 20
    }),
    page: import_pieces_framework8.Property.Number({
      displayName: "Page",
      description: "Which page of results to return.",
      required: !1,
      defaultValue: 1
    }),
    tag_name: import_pieces_framework8.Property.ShortText({
      displayName: "Tag Name",
      description: "Filter Droplets by a specific tag. Cannot be combined with Name or Type.",
      required: !1
    }),
    name: import_pieces_framework8.Property.ShortText({
      displayName: "Name",
      description: "Filter by exact Droplet name (case-insensitive). Cannot be combined with Tag Name.",
      required: !1
    }),
    type: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Type",
      description: "Filter by Droplet type. Cannot be combined with Tag Name.",
      required: !1,
      options: {
        options: [
          { label: "Standard Droplets", value: "droplets" },
          { label: "GPU Droplets", value: "gpus" }
        ]
      }
    })
  },
  async run(context) {
    let { per_page, page, tag_name, name, type } = context.propsValue, query = {
      per_page: per_page != null ? per_page : 20,
      page: page != null ? page : 1
    };
    return tag_name && (query.tag_name = tag_name), name && (query.name = name), type && (query.type = type), await digitalOceanApiCall({
      method: import_pieces_common7.HttpMethod.GET,
      path: "/droplets",
      auth: getAuthFromValue(context.auth),
      query
    });
  }
});

// ../ap/packages/pieces/community/digital-ocean/src/lib/actions/get-droplet.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var import_pieces_common8 = require("@activepieces/pieces-common"), getDroplet = (0, import_pieces_framework9.createAction)({
  auth: digitalOceanAuth,
  name: "get_droplet",
  displayName: "Get Droplet",
  description: "Retrieve details about a specific Droplet.",
  audience: "both",
  aiMetadata: { description: "Fetches full details (status, region, size, networks, tags) for a single Droplet on the DigitalOcean account, identified by its numeric Droplet id. Use when you have a Droplet id and need its current state. Read-only and idempotent.", idempotent: !0 },
  props: {
    droplet_id: import_pieces_framework9.Property.Dropdown({
      displayName: "Droplet",
      description: "Select the Droplet to retrieve.",
      required: !0,
      refreshers: [],
      auth: digitalOceanAuth,
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await digitalOceanApiCall({
          method: import_pieces_common8.HttpMethod.GET,
          path: "/droplets",
          auth: getAuthFromValue(auth),
          query: { per_page: 200 }
        })).droplets.map((droplet) => ({
          label: droplet.name,
          value: droplet.id
        }))
      } : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    })
  },
  async run(context) {
    let { droplet_id } = context.propsValue;
    return await digitalOceanApiCall({
      method: import_pieces_common8.HttpMethod.GET,
      path: `/droplets/${droplet_id}`,
      auth: getAuthFromValue(context.auth)
    });
  }
});

// ../ap/packages/pieces/community/digital-ocean/src/lib/actions/create-droplet.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var import_pieces_common9 = require("@activepieces/pieces-common"), createDroplet = (0, import_pieces_framework10.createAction)({
  auth: digitalOceanAuth,
  name: "create_droplet",
  displayName: "Create Droplet",
  description: "Create a new Droplet or multiple Droplets.",
  audience: "both",
  aiMetadata: { description: "Provisions one or more new Droplets (virtual machines) on the DigitalOcean account. The creation mode switches between a single Droplet (uses the single name) and a batch of up to 10 (uses the names list); size and image are required. Use to spin up compute. Not idempotent \u2014 each call launches new billable Droplets.", idempotent: !1 },
  props: {
    creation_mode: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Creation Mode",
      description: "Create a single Droplet or multiple Droplets at once.",
      required: !0,
      defaultValue: "single",
      options: {
        options: [
          { label: "Single Droplet", value: "single" },
          { label: "Multiple Droplets (up to 10)", value: "multiple" }
        ]
      }
    }),
    name: import_pieces_framework10.Property.ShortText({
      displayName: "Droplet Name",
      description: "Name for the Droplet (e.g., example.com).",
      required: !0
    }),
    names: import_pieces_framework10.Property.Array({
      displayName: "Droplet Names",
      description: "Names for multiple Droplets (up to 10).",
      required: !0
    }),
    region: import_pieces_framework10.Property.Dropdown({
      displayName: "Region",
      description: "Region to deploy the Droplet.",
      required: !1,
      refreshers: [],
      auth: digitalOceanAuth,
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await digitalOceanApiCall({
          method: import_pieces_common9.HttpMethod.GET,
          path: "/regions",
          auth: getAuthFromValue(auth)
        })).regions.filter((r) => r.available).map((region) => ({
          label: region.name,
          value: region.slug
        }))
      } : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    size: import_pieces_framework10.Property.Dropdown({
      displayName: "Size",
      description: "Droplet size (CPU, RAM, disk).",
      required: !0,
      refreshers: [],
      auth: digitalOceanAuth,
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await digitalOceanApiCall({
          method: import_pieces_common9.HttpMethod.GET,
          path: "/sizes",
          auth: getAuthFromValue(auth)
        })).sizes.filter((s) => s.available).map((size) => ({
          label: `${size.description} ($${size.price_monthly}/mo)`,
          value: size.slug
        }))
      } : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    image: import_pieces_framework10.Property.Dropdown({
      displayName: "Image",
      description: "Base image for the Droplet.",
      required: !0,
      refreshers: [],
      auth: digitalOceanAuth,
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await digitalOceanApiCall({
          method: import_pieces_common9.HttpMethod.GET,
          path: "/images",
          auth: getAuthFromValue(auth),
          query: { type: "distribution", per_page: 200 }
        })).images.map((image) => {
          var _a;
          return {
            label: `${image.distribution} - ${image.name}`,
            value: (_a = image.slug) != null ? _a : image.id
          };
        })
      } : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    ssh_keys: import_pieces_framework10.Property.MultiSelectDropdown({
      displayName: "SSH Keys",
      description: "SSH keys to embed in the Droplet.",
      required: !1,
      refreshers: [],
      auth: digitalOceanAuth,
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await digitalOceanApiCall({
          method: import_pieces_common9.HttpMethod.GET,
          path: "/account/keys",
          auth: getAuthFromValue(auth)
        })).ssh_keys.map((key) => ({
          label: key.name,
          value: key.id
        }))
      } : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    vpc_uuid: import_pieces_framework10.Property.Dropdown({
      displayName: "VPC",
      description: "VPC network for the Droplet.",
      required: !1,
      refreshers: [],
      auth: digitalOceanAuth,
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await digitalOceanApiCall({
          method: import_pieces_common9.HttpMethod.GET,
          path: "/vpcs",
          auth: getAuthFromValue(auth)
        })).vpcs.map((vpc) => ({
          label: `${vpc.name} (${vpc.region})`,
          value: vpc.id
        }))
      } : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    }),
    backups: import_pieces_framework10.Property.Checkbox({
      displayName: "Enable Backups",
      description: "Enable automated backups.",
      required: !1,
      defaultValue: !1
    }),
    ipv6: import_pieces_framework10.Property.Checkbox({
      displayName: "Enable IPv6",
      description: "Enable IPv6 networking.",
      required: !1,
      defaultValue: !1
    }),
    monitoring: import_pieces_framework10.Property.Checkbox({
      displayName: "Enable Monitoring",
      description: "Install the DigitalOcean monitoring agent.",
      required: !1,
      defaultValue: !1
    }),
    tags: import_pieces_framework10.Property.Array({
      displayName: "Tags",
      description: "Tags to apply to the Droplet.",
      required: !1
    }),
    user_data: import_pieces_framework10.Property.LongText({
      displayName: "User Data",
      description: "Cloud-init script or user data (max 64 KiB).",
      required: !1
    }),
    with_droplet_agent: import_pieces_framework10.Property.Checkbox({
      displayName: "Install Droplet Agent",
      description: "Install agent for web console access.",
      required: !1,
      defaultValue: !0
    })
  },
  async run(context) {
    let {
      creation_mode,
      name,
      names,
      region,
      size,
      image,
      ssh_keys,
      vpc_uuid,
      backups,
      ipv6,
      monitoring,
      tags,
      user_data,
      with_droplet_agent
    } = context.propsValue, body = {
      size,
      image
    };
    return creation_mode === "single" ? body.name = name : body.names = names, region && (body.region = region), ssh_keys && ssh_keys.length > 0 && (body.ssh_keys = ssh_keys), vpc_uuid && (body.vpc_uuid = vpc_uuid), backups !== void 0 && (body.backups = backups), ipv6 !== void 0 && (body.ipv6 = ipv6), monitoring !== void 0 && (body.monitoring = monitoring), tags && tags.length > 0 && (body.tags = tags), user_data && (body.user_data = user_data), with_droplet_agent !== void 0 && (body.with_droplet_agent = with_droplet_agent), await digitalOceanApiCall({
      method: import_pieces_common9.HttpMethod.POST,
      path: "/droplets",
      auth: getAuthFromValue(context.auth),
      body
    });
  }
});

// ../ap/packages/pieces/community/digital-ocean/src/lib/actions/delete-droplet.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var import_pieces_common10 = require("@activepieces/pieces-common"), deleteDroplet = (0, import_pieces_framework11.createAction)({
  auth: digitalOceanAuth,
  name: "delete_droplet",
  displayName: "Delete Droplet",
  description: "Delete an existing Droplet.",
  audience: "both",
  aiMetadata: { description: "Permanently destroys a single Droplet (virtual machine) on the DigitalOcean account, identified by its numeric Droplet id. Use to tear down compute you no longer need. Destructive; the first call removes the Droplet and later calls on a missing id fail.", idempotent: !1 },
  props: {
    droplet_id: import_pieces_framework11.Property.Dropdown({
      displayName: "Droplet",
      description: "Select the Droplet to delete.",
      required: !0,
      refreshers: [],
      auth: digitalOceanAuth,
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await digitalOceanApiCall({
          method: import_pieces_common10.HttpMethod.GET,
          path: "/droplets",
          auth: getAuthFromValue(auth),
          query: { per_page: 200 }
        })).droplets.map((droplet) => ({
          label: droplet.name,
          value: droplet.id
        }))
      } : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    })
  },
  async run(context) {
    let { droplet_id } = context.propsValue;
    return await digitalOceanApiCall({
      method: import_pieces_common10.HttpMethod.DELETE,
      path: `/droplets/${droplet_id}`,
      auth: getAuthFromValue(context.auth)
    }), {
      success: !0,
      message: `Droplet with ID '${droplet_id}' has been deleted.`
    };
  }
});

// ../ap/packages/pieces/community/digital-ocean/src/lib/actions/list-database-clusters.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var import_pieces_common11 = require("@activepieces/pieces-common"), listDatabaseClusters = (0, import_pieces_framework12.createAction)({
  auth: digitalOceanAuth,
  name: "list_database_clusters",
  displayName: "List Database Clusters",
  description: "Retrieve a list of all database clusters in your account.",
  audience: "both",
  aiMetadata: { description: "Lists managed database clusters on the authenticated DigitalOcean account, optionally filtered to a single tag (empty tag returns all). Use to discover clusters or resolve a cluster id before acting on it. Read-only and idempotent.", idempotent: !0 },
  props: {
    tag_name: import_pieces_framework12.Property.ShortText({
      displayName: "Tag Name",
      description: "Filter database clusters by a specific tag.",
      required: !1
    })
  },
  async run(context) {
    let { tag_name } = context.propsValue, query = {};
    return tag_name && (query.tag_name = tag_name), await digitalOceanApiCall({
      method: import_pieces_common11.HttpMethod.GET,
      path: "/databases",
      auth: getAuthFromValue(context.auth),
      query: Object.keys(query).length > 0 ? query : void 0
    });
  }
});

// ../ap/packages/pieces/community/digital-ocean/src/lib/actions/list-database-events.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var import_pieces_common12 = require("@activepieces/pieces-common"), listDatabaseEvents = (0, import_pieces_framework13.createAction)({
  auth: digitalOceanAuth,
  name: "list_database_events",
  displayName: "List Database Events",
  description: "Retrieve all event logs for a database cluster.",
  audience: "both",
  aiMetadata: { description: "Retrieves the event log (configuration changes, maintenance, scaling) for one managed database cluster on the DigitalOcean account, identified by its cluster UUID. Use to audit or inspect recent activity on a specific cluster. Read-only and idempotent.", idempotent: !0 },
  props: {
    database_cluster_uuid: import_pieces_framework13.Property.Dropdown({
      displayName: "Database Cluster",
      description: "Select the database cluster.",
      required: !0,
      refreshers: [],
      auth: digitalOceanAuth,
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await digitalOceanApiCall({
          method: import_pieces_common12.HttpMethod.GET,
          path: "/databases",
          auth: getAuthFromValue(auth)
        })).databases.map((db) => ({
          label: `${db.name} (${db.engine})`,
          value: db.id
        }))
      } : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      }
    })
  },
  async run(context) {
    let { database_cluster_uuid } = context.propsValue;
    return await digitalOceanApiCall({
      method: import_pieces_common12.HttpMethod.GET,
      path: `/databases/${database_cluster_uuid}/events`,
      auth: getAuthFromValue(context.auth)
    });
  }
});

// ../ap/packages/pieces/community/digital-ocean/src/index.ts
var digitalOcean = (0, import_pieces_framework14.createPiece)({
  displayName: "DigitalOcean",
  auth: digitalOceanAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/digital-ocean.png",
  description: "Cloud infrastructure provider for developers.",
  categories: [import_pieces_framework15.PieceCategory.DEVELOPER_TOOLS],
  authors: ["onyedikachi-david"],
  actions: [
    listDomains,
    createDomain,
    getDomain,
    deleteDomain,
    listDroplets,
    getDroplet,
    createDroplet,
    deleteDroplet,
    listDatabaseClusters,
    listDatabaseEvents,
    (0, import_pieces_common13.createCustomApiCallAction)({
      baseUrl: () => "https://api.digitalocean.com/v2",
      auth: digitalOceanAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${typeof auth == "string" ? auth : auth.access_token}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  digitalOcean
});
