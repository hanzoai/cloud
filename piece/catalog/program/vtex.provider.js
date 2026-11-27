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
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key != "symbol" ? key + "" : key, value);

// ../ap/packages/pieces/community/vtex/src/index.ts
var index_exports = {};
__export(index_exports, {
  vtex: () => vtex,
  vtexAuth: () => vtexAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework17 = require("@activepieces/pieces-framework"), import_pieces_framework18 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/vtex/src/lib/actions/Brand/create-brand.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/vtex/src/lib/common/Brand.ts
var import_pieces_common = require("@activepieces/pieces-common");
var Brand = class {
  constructor(host, appKey, appToken) {
    __publicField(this, "baseURL");
    __publicField(this, "headers");
    this.baseURL = "https://" + host, this.headers = {
      "X-VTEX-API-AppKey": appKey,
      "X-VTEX-API-AppToken": appToken
    };
  }
  async getBrandById(brandID) {
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: this.baseURL + "/api/catalog_system/pvt/brand/" + brandID,
      headers: this.headers
    })).body;
  }
  async getBrandList() {
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: this.baseURL + "/api/catalog_system/pvt/brand/list",
      headers: this.headers
    })).body;
  }
  async createBrand(newBrandData) {
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: this.baseURL + "/api/catalog/pvt/brand",
      headers: this.headers,
      body: newBrandData
    })).body;
  }
  async updateBrand(brandID, updatedBrandData) {
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.PUT,
      url: this.baseURL + "/api/catalog/pvt/brand/" + brandID,
      headers: this.headers,
      body: updatedBrandData
    })).body;
  }
  async deleteBrand(brandID) {
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.DELETE,
      url: this.baseURL + "/api/catalog/pvt/brand/" + brandID,
      headers: this.headers
    })).body;
  }
};

// ../ap/packages/pieces/community/vtex/src/lib/actions/Brand/create-brand.ts
var createBrand = (0, import_pieces_framework.createAction)({
  auth: vtexAuth,
  name: "create-brand",
  displayName: "Create new Brand",
  description: "Create a new Brand to your catalog",
  audience: "both",
  aiMetadata: {
    description: "Create a new brand in a VTEX store catalog; only the brand name is required. Optionally pass a brand ID to set it explicitly. Not idempotent: each call creates a separate brand.",
    idempotent: !1
  },
  props: {
    Name: import_pieces_framework.Property.ShortText({
      displayName: "Name",
      required: !0
    }),
    SiteTitle: import_pieces_framework.Property.ShortText({
      displayName: "Site Title",
      required: !1
    }),
    LinkId: import_pieces_framework.Property.ShortText({
      displayName: "Link ID",
      required: !1
    }),
    Id: import_pieces_framework.Property.Number({
      displayName: "Brand ID",
      description: "Set the brand ID",
      required: !1
    }),
    Text: import_pieces_framework.Property.ShortText({
      displayName: "Text",
      required: !1
    }),
    MenuHome: import_pieces_framework.Property.Checkbox({
      displayName: "Menu Home",
      required: !1
    }),
    Keywords: import_pieces_framework.Property.ShortText({
      displayName: "Keywords",
      description: "Similar words",
      required: !1
    }),
    Active: import_pieces_framework.Property.Checkbox({
      displayName: "Active",
      required: !1
    }),
    Score: import_pieces_framework.Property.Number({
      displayName: "Score",
      required: !1
    })
  },
  async run(context) {
    let { hostUrl, appKey, appToken } = context.auth.props, brandData = __spreadValues({}, context.propsValue);
    return delete brandData.authentication, await new Brand(hostUrl, appKey, appToken).createBrand({
      Id: context.propsValue.Id,
      Name: context.propsValue.Name,
      Text: context.propsValue.Text,
      Keywords: context.propsValue.Keywords,
      SiteTitle: context.propsValue.SiteTitle,
      Active: context.propsValue.Active,
      MenuHome: context.propsValue.MenuHome,
      Score: context.propsValue.Score,
      LinkId: context.propsValue.LinkId
    });
  }
});

// ../ap/packages/pieces/community/vtex/src/lib/actions/Brand/delete-brand.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var deleteBrand = (0, import_pieces_framework2.createAction)({
  auth: vtexAuth,
  name: "delete-brand",
  displayName: "Delete Brand",
  description: "Delete a Brand in your catalog by it's id",
  audience: "both",
  aiMetadata: {
    description: "Delete a brand from a VTEX store catalog by its numeric brand ID. Destructive and irreversible. Idempotent in effect: once the brand is gone, repeating leaves it absent.",
    idempotent: !0
  },
  props: {
    brandId: import_pieces_framework2.Property.Number({
      displayName: "Brand ID",
      description: "The Brand ID",
      required: !0
    })
  },
  async run(context) {
    let { hostUrl, appKey, appToken } = context.auth.props, { brandId } = context.propsValue;
    return await new Brand(hostUrl, appKey, appToken).deleteBrand(brandId);
  }
});

// ../ap/packages/pieces/community/vtex/src/lib/actions/Brand/get-brand-by-id.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var getBrandById = (0, import_pieces_framework3.createAction)({
  auth: vtexAuth,
  name: "get-brand-by-id",
  displayName: "Get Brand By ID",
  description: "Find a Brand in your catalog by it's id",
  audience: "both",
  aiMetadata: {
    description: "Retrieve a single brand from a VTEX store catalog by its numeric brand ID. Use when you already know the brand ID and need its details. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    BrandId: import_pieces_framework3.Property.Number({
      displayName: "Brand ID",
      description: "The Brand ID",
      required: !0
    })
  },
  async run(context) {
    let { hostUrl, appKey, appToken } = context.auth.props, { BrandId } = context.propsValue;
    return await new Brand(hostUrl, appKey, appToken).getBrandById(BrandId);
  }
});

// ../ap/packages/pieces/community/vtex/src/lib/actions/Brand/get-brand-list.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var getBrandList = (0, import_pieces_framework4.createAction)({
  auth: vtexAuth,
  name: "get-brand-list",
  displayName: "Get Brand List",
  description: "Find all Brands in your catalog",
  audience: "both",
  aiMetadata: {
    description: "List all brands in a VTEX store catalog. Use to enumerate brands or to resolve a brand name to its ID before creating or updating products. Takes no input; read-only and idempotent.",
    idempotent: !0
  },
  props: {},
  async run(context) {
    let { hostUrl, appKey, appToken } = context.auth.props;
    return await new Brand(hostUrl, appKey, appToken).getBrandList();
  }
});

// ../ap/packages/pieces/community/vtex/src/lib/actions/Brand/update-brand.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var updateBrand = (0, import_pieces_framework5.createAction)({
  auth: vtexAuth,
  name: "update-brand",
  displayName: "Update Brand",
  description: "Update a Brand in your catalog",
  audience: "both",
  aiMetadata: {
    description: "Update an existing brand in a VTEX store catalog, identified by its brand ID. Replaces the brand record, so all core fields (name, site title, link ID, text, menu home, keywords, active, score) must be supplied. Idempotent: repeating with the same input leaves the brand in the same state.",
    idempotent: !0
  },
  props: {
    Id: import_pieces_framework5.Property.Number({
      displayName: "Brand ID",
      description: "Set the brand ID",
      required: !0
    }),
    Name: import_pieces_framework5.Property.ShortText({
      displayName: "Name",
      required: !0
    }),
    SiteTitle: import_pieces_framework5.Property.ShortText({
      displayName: "Site Title",
      required: !0
    }),
    LinkId: import_pieces_framework5.Property.ShortText({
      displayName: "Link ID",
      required: !0
    }),
    Text: import_pieces_framework5.Property.ShortText({
      displayName: "Text",
      required: !0
    }),
    MenuHome: import_pieces_framework5.Property.Checkbox({
      displayName: "Menu Home",
      required: !0
    }),
    Keywords: import_pieces_framework5.Property.ShortText({
      displayName: "Keywords",
      description: "Similar words",
      required: !0
    }),
    Active: import_pieces_framework5.Property.Checkbox({
      displayName: "Active",
      required: !0
    }),
    Score: import_pieces_framework5.Property.Number({
      displayName: "Score",
      required: !0
    })
  },
  async run(context) {
    let { hostUrl, appKey, appToken } = context.auth.props, {
      Id,
      Name,
      SiteTitle,
      LinkId,
      Text,
      MenuHome,
      Keywords,
      Active,
      Score
    } = context.propsValue, brandData = {
      Id,
      Name,
      SiteTitle,
      LinkId,
      Text,
      MenuHome,
      Keywords,
      Active,
      Score
    };
    return await new Brand(hostUrl, appKey, appToken).updateBrand(Id, brandData);
  }
});

// ../ap/packages/pieces/community/vtex/src/lib/actions/Category/get-category-by-id.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/vtex/src/lib/common/Category.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
var Category = class {
  constructor(host, appKey, appToken) {
    __publicField(this, "baseURL");
    __publicField(this, "headers");
    this.baseURL = "https://" + host, this.headers = {
      "X-VTEX-API-AppKey": appKey,
      "X-VTEX-API-AppToken": appToken
    };
  }
  async getCategory(CategoryID) {
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.GET,
      url: this.baseURL + "/api/catalog/pvt/category/" + (CategoryID || ""),
      headers: this.headers
    })).body;
  }
  async getCategoryTree(categoryLevels) {
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.GET,
      url: this.baseURL + "/api/catalog_system/pub/category/tree/" + categoryLevels,
      headers: this.headers
    })).body;
  }
  async createCategory(newCategoryData) {
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: this.baseURL + "/api/catalog/pvt/category",
      headers: this.headers,
      body: newCategoryData
    })).body;
  }
  async updateCategory(CategoryID, updatedCategoryData) {
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.PUT,
      url: this.baseURL + "/api/catalog/pvt/category/" + CategoryID,
      headers: this.headers,
      body: updatedCategoryData
    })).body;
  }
};

// ../ap/packages/pieces/community/vtex/src/lib/actions/Category/get-category-by-id.ts
var getCategoryById = (0, import_pieces_framework6.createAction)({
  auth: vtexAuth,
  name: "get-category-by-id",
  displayName: "Get Category",
  description: "Find a Category in your catalog by it's id",
  audience: "both",
  aiMetadata: {
    description: "Retrieve a VTEX store catalog category. Pass a category ID to fetch that single category, or omit it to fetch all categories. Use to look up a category or resolve a category ID needed when creating products. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    categoryId: import_pieces_framework6.Property.Number({
      displayName: "Category ID",
      description: "The Category ID",
      required: !1
    })
  },
  async run(context) {
    let { hostUrl, appKey, appToken } = context.auth.props, { categoryId } = context.propsValue;
    return await new Category(hostUrl, appKey, appToken).getCategory(categoryId);
  }
});

// ../ap/packages/pieces/community/vtex/src/lib/actions/Client/get-client-by-id.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/vtex/src/lib/common/Client.ts
var import_pieces_common3 = require("@activepieces/pieces-common");
var Client = class {
  constructor(host, appKey, appToken) {
    __publicField(this, "baseURL");
    __publicField(this, "headers");
    this.baseURL = "https://" + host, this.headers = {
      "X-VTEX-API-AppKey": appKey,
      "X-VTEX-API-AppToken": appToken
    };
  }
  async getClientById(clientID) {
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: this.baseURL + "/api/dataentities/CL/documents/" + clientID,
      headers: this.headers
    })).body;
  }
  async getClientList() {
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: this.baseURL + "/api/dataentities/CL/search",
      headers: this.headers
    })).body;
  }
};

// ../ap/packages/pieces/community/vtex/src/lib/actions/Client/get-client-by-id.ts
var getClientById = (0, import_pieces_framework7.createAction)({
  auth: vtexAuth,
  name: "get-client-by-id",
  displayName: "Get Client By ID",
  description: "Find a Client by Id",
  audience: "both",
  aiMetadata: {
    description: "Retrieve a single client (customer) from the VTEX store master data by its client ID. Use when you already know the client ID and need its details. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    clientId: import_pieces_framework7.Property.Number({
      displayName: "Client ID",
      description: "The Client ID",
      required: !0
    })
  },
  async run(context) {
    let { hostUrl, appKey, appToken } = context.auth.props, { clientId } = context.propsValue;
    return await new Client(hostUrl, appKey, appToken).getClientById(clientId);
  }
});

// ../ap/packages/pieces/community/vtex/src/lib/actions/Client/get-client-list.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var getClientList = (0, import_pieces_framework8.createAction)({
  auth: vtexAuth,
  name: "get-client-list",
  displayName: "Get Client List",
  description: "Find all Clients",
  audience: "both",
  aiMetadata: {
    description: "List all clients (customers) registered in the VTEX store master data. Use to enumerate customers. Takes no input; read-only and idempotent.",
    idempotent: !0
  },
  props: {},
  async run(context) {
    let { hostUrl, appKey, appToken } = context.auth.props;
    return await new Client(hostUrl, appKey, appToken).getClientList();
  }
});

// ../ap/packages/pieces/community/vtex/src/lib/actions/Order/get-order-by-id.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/vtex/src/lib/common/Order.ts
var import_pieces_common4 = require("@activepieces/pieces-common");
var Order = class {
  constructor(host, appKey, appToken) {
    __publicField(this, "baseURL");
    __publicField(this, "headers");
    this.baseURL = "https://" + host, this.headers = {
      "X-VTEX-API-AppKey": appKey,
      "X-VTEX-API-AppToken": appToken
    };
  }
  async getOrderById(OrderID) {
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.GET,
      url: this.baseURL + "/api/oms/pvt/orders/" + OrderID,
      headers: this.headers
    })).body;
  }
  async getOrderList(from, to) {
    let route = `/api/oms/pvt/orders?f_creationDate=creationDate:[${from.toISOString()} TO ${to.toISOString()}]`;
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.GET,
      url: this.baseURL + route,
      headers: this.headers
    })).body;
  }
};

// ../ap/packages/pieces/community/vtex/src/lib/actions/Order/get-order-by-id.ts
var getOrderById = (0, import_pieces_framework9.createAction)({
  auth: vtexAuth,
  name: "get-order-by-id",
  displayName: "Get Order By ID",
  description: "Find a Order by Id",
  audience: "both",
  aiMetadata: {
    description: "Retrieve a single order from the VTEX store order management system (OMS) by its order ID. Use when you already know the order ID and need its details. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    OrderId: import_pieces_framework9.Property.Number({
      displayName: "Order ID",
      description: "The Order ID",
      required: !0
    })
  },
  async run(context) {
    let { hostUrl, appKey, appToken } = context.auth.props, { OrderId } = context.propsValue;
    return await new Order(hostUrl, appKey, appToken).getOrderById(OrderId);
  }
});

// ../ap/packages/pieces/community/vtex/src/lib/actions/Order/get-order-list.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var year = (/* @__PURE__ */ new Date()).getFullYear().toString(), getOrderList = (0, import_pieces_framework10.createAction)({
  auth: vtexAuth,
  name: "get-order-list",
  displayName: "Get Orders List",
  description: "Find Orders",
  audience: "both",
  aiMetadata: {
    description: "List orders from the VTEX store order management system (OMS) created within a date range, specified as from/to year (required) plus optional month and day. Use to find orders over a period rather than by a single ID. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    fromYear: import_pieces_framework10.Property.Number({
      displayName: "From (Year)",
      required: !0
    }),
    toYear: import_pieces_framework10.Property.ShortText({
      displayName: "To (Year)",
      defaultValue: year,
      required: !0
    }),
    fromMonth: import_pieces_framework10.Property.ShortText({
      displayName: "From (Month)",
      defaultValue: "01",
      required: !1
    }),
    toMonth: import_pieces_framework10.Property.ShortText({
      displayName: "To (Month)",
      defaultValue: "12",
      required: !1
    }),
    fromDay: import_pieces_framework10.Property.ShortText({
      displayName: "From (Day)",
      defaultValue: "01",
      required: !1
    }),
    toDay: import_pieces_framework10.Property.ShortText({
      displayName: "To (Day)",
      defaultValue: "28",
      required: !1
    })
  },
  async run(context) {
    let { hostUrl, appKey, appToken } = context.auth.props, { fromYear, toYear, fromMonth, toMonth, fromDay, toDay } = context.propsValue, order = new Order(hostUrl, appKey, appToken), fromDate = /* @__PURE__ */ new Date(
      `${fromYear}-${fromMonth || "01"}-${fromDay || "01"}`
    ), toDate = /* @__PURE__ */ new Date(`${toYear}-${toMonth || "12"}-${toDay || "28"}`);
    return await order.getOrderList(fromDate, toDate);
  }
});

// ../ap/packages/pieces/community/vtex/src/lib/actions/Product/create-product.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/vtex/src/lib/common/Product.ts
var import_pieces_common5 = require("@activepieces/pieces-common");
var Product = class {
  constructor(host, appKey, appToken) {
    __publicField(this, "baseURL");
    __publicField(this, "headers");
    this.baseURL = "https://" + host, this.headers = {
      "X-VTEX-API-AppKey": appKey,
      "X-VTEX-API-AppToken": appToken
    };
  }
  async getProductById(productID) {
    return (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.GET,
      url: this.baseURL + "/api/catalog/pvt/product/" + productID,
      headers: this.headers
    })).body;
  }
  async createProduct(newProductData) {
    return (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.POST,
      url: this.baseURL + "/api/catalog/pvt/product",
      headers: this.headers,
      body: newProductData
    })).body;
  }
  async updateProduct(productID, updatedProductData) {
    return (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.PUT,
      url: this.baseURL + "/api/catalog/pvt/product/" + productID,
      headers: this.headers,
      body: updatedProductData
    })).body;
  }
};

// ../ap/packages/pieces/community/vtex/src/lib/actions/Product/create-product.ts
var createProduct = (0, import_pieces_framework11.createAction)({
  auth: vtexAuth,
  name: "create-product",
  displayName: "Create New Product",
  description: "Create a new product in your catalog",
  audience: "both",
  aiMetadata: {
    description: "Create a new product in a VTEX store catalog, requiring name, title, description, and an existing brand ID and category ID. Optionally pass a product ID to set it explicitly. Not idempotent: each call creates a separate product.",
    idempotent: !1
  },
  props: {
    Name: import_pieces_framework11.Property.ShortText({
      displayName: "Name",
      required: !0
    }),
    Title: import_pieces_framework11.Property.ShortText({
      displayName: "Title",
      required: !0
    }),
    Description: import_pieces_framework11.Property.ShortText({
      displayName: "Description",
      required: !0
    }),
    BrandId: import_pieces_framework11.Property.Number({
      displayName: "Brand ID",
      required: !0
    }),
    CategoryId: import_pieces_framework11.Property.Number({
      displayName: "Category ID",
      required: !0
    }),
    LinkId: import_pieces_framework11.Property.ShortText({
      displayName: "Link ID",
      required: !1
    }),
    RefId: import_pieces_framework11.Property.ShortText({
      displayName: "Ref ID",
      required: !1
    }),
    Id: import_pieces_framework11.Property.Number({
      displayName: "Product ID",
      description: "Set the product ID",
      required: !1
    }),
    IsVisible: import_pieces_framework11.Property.Checkbox({
      displayName: "Is Visible",
      required: !1
    }),
    DescriptionShort: import_pieces_framework11.Property.ShortText({
      displayName: "Short Description",
      required: !1
    }),
    ReleaseDate: import_pieces_framework11.Property.ShortText({
      displayName: "Release Date",
      required: !1
    }),
    KeyWords: import_pieces_framework11.Property.ShortText({
      displayName: "Key Words",
      description: "Similar words",
      required: !1
    }),
    IsActive: import_pieces_framework11.Property.Checkbox({
      displayName: "Is Active",
      required: !1
    }),
    TaxCode: import_pieces_framework11.Property.ShortText({
      displayName: "Tax Code",
      required: !1
    }),
    MetaTagDescription: import_pieces_framework11.Property.ShortText({
      displayName: "Metatag description",
      required: !1
    }),
    ShowWithoutStock: import_pieces_framework11.Property.Checkbox({
      displayName: "Show Without Stock",
      required: !1
    }),
    Score: import_pieces_framework11.Property.Number({
      displayName: "Score",
      required: !1
    })
  },
  async run(context) {
    let { hostUrl, appKey, appToken } = context.auth.props, product = new Product(hostUrl, appKey, appToken), productData = {
      Name: context.propsValue.Name,
      Title: context.propsValue.Title,
      Description: context.propsValue.Description,
      BrandId: context.propsValue.BrandId,
      CategoryId: context.propsValue.CategoryId,
      LinkId: context.propsValue.LinkId,
      RefId: context.propsValue.RefId,
      Id: context.propsValue.Id,
      IsVisible: context.propsValue.IsVisible,
      DescriptionShort: context.propsValue.DescriptionShort,
      ReleaseDate: context.propsValue.ReleaseDate,
      KeyWords: context.propsValue.KeyWords,
      IsActive: context.propsValue.IsActive,
      TaxCode: context.propsValue.TaxCode,
      MetaTagDescription: context.propsValue.MetaTagDescription,
      ShowWithoutStock: context.propsValue.ShowWithoutStock,
      Score: context.propsValue.Score
    };
    return await product.createProduct(productData);
  }
});

// ../ap/packages/pieces/community/vtex/src/lib/actions/Product/get-product-by-id.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var getProductById = (0, import_pieces_framework12.createAction)({
  auth: vtexAuth,
  name: "get-product-by-id",
  displayName: "Get Product By ID",
  description: "Find a product in your catalog by it's id",
  audience: "both",
  aiMetadata: {
    description: "Retrieve a single catalog product from a VTEX store by its numeric product ID. Use to look up product details when you already know the product ID. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    productId: import_pieces_framework12.Property.Number({
      displayName: "Product ID",
      description: "The product ID",
      required: !0
    })
  },
  async run(context) {
    let { hostUrl, appKey, appToken } = context.auth.props, { productId } = context.propsValue;
    return await new Product(hostUrl, appKey, appToken).getProductById(productId);
  }
});

// ../ap/packages/pieces/community/vtex/src/lib/actions/Product/update-product.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var updateProduct = (0, import_pieces_framework13.createAction)({
  auth: vtexAuth,
  name: "Update-product",
  displayName: "Update a Product",
  description: "Update a product in your catalog",
  audience: "both",
  aiMetadata: {
    description: "Update an existing catalog product in a VTEX store, identified by its product ID. Requires the full set of core fields (name, title, description, brand ID, category ID, department ID) since it replaces the product record. Idempotent: repeating with the same input leaves the product in the same state.",
    idempotent: !0
  },
  props: {
    productId: import_pieces_framework13.Property.Number({
      displayName: "Product ID",
      description: "Set the product ID",
      required: !0
    }),
    Name: import_pieces_framework13.Property.ShortText({
      displayName: "Name",
      required: !0
    }),
    Title: import_pieces_framework13.Property.ShortText({
      displayName: "Title",
      required: !0
    }),
    Description: import_pieces_framework13.Property.ShortText({
      displayName: "Description",
      required: !0
    }),
    BrandId: import_pieces_framework13.Property.Number({
      displayName: "Brand ID",
      required: !0
    }),
    CategoryId: import_pieces_framework13.Property.Number({
      displayName: "Category ID",
      required: !0
    }),
    DepartmentId: import_pieces_framework13.Property.Number({
      displayName: "DepartmentId",
      required: !0
    }),
    LinkId: import_pieces_framework13.Property.ShortText({
      displayName: "Link ID",
      required: !1
    }),
    RefId: import_pieces_framework13.Property.ShortText({
      displayName: "Ref ID",
      required: !1
    }),
    BrandName: import_pieces_framework13.Property.ShortText({
      displayName: "Brand Name",
      required: !1
    }),
    IsVisible: import_pieces_framework13.Property.Checkbox({
      displayName: "Is Visible",
      required: !1
    }),
    DescriptionShort: import_pieces_framework13.Property.ShortText({
      displayName: "Short Description",
      required: !1
    }),
    ReleaseDate: import_pieces_framework13.Property.ShortText({
      displayName: "Release Date",
      required: !1
    }),
    KeyWords: import_pieces_framework13.Property.ShortText({
      displayName: "Key Words",
      description: "Similar words",
      required: !1
    }),
    IsActive: import_pieces_framework13.Property.Checkbox({
      displayName: "Is Active",
      required: !1
    }),
    TaxCode: import_pieces_framework13.Property.ShortText({
      displayName: "Tax Code",
      required: !1
    }),
    MetaTagDescription: import_pieces_framework13.Property.ShortText({
      displayName: "Metatag description",
      required: !1
    }),
    ShowWithoutStock: import_pieces_framework13.Property.Checkbox({
      displayName: "Show Without Stock",
      required: !1
    }),
    Score: import_pieces_framework13.Property.Number({
      displayName: "Score",
      required: !1
    })
  },
  async run(context) {
    let { hostUrl, appKey, appToken } = context.auth.props, _a = context.propsValue, { productId } = _a, restProps = __objRest(_a, ["productId"]), updatedProduct = {
      productId,
      Name: restProps.Name,
      Title: restProps.Title,
      Description: restProps.Description,
      BrandId: restProps.BrandId,
      CategoryId: restProps.CategoryId,
      DepartmentId: restProps.DepartmentId,
      LinkId: restProps.LinkId,
      RefId: restProps.RefId,
      BrandName: restProps.BrandName,
      IsVisible: restProps.IsVisible,
      DescriptionShort: restProps.DescriptionShort,
      ReleaseDate: restProps.ReleaseDate,
      KeyWords: restProps.KeyWords,
      IsActive: restProps.IsActive,
      TaxCode: restProps.TaxCode,
      MetaTagDescription: restProps.MetaTagDescription,
      ShowWithoutStock: restProps.ShowWithoutStock,
      Score: restProps.Score
    };
    return await new Product(hostUrl, appKey, appToken).updateProduct(productId, updatedProduct);
  }
});

// ../ap/packages/pieces/community/vtex/src/lib/actions/SKU-File/create-sku-file.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/vtex/src/lib/common/SKU-File.ts
var import_pieces_common6 = require("@activepieces/pieces-common");
var SkuFile = class {
  constructor(host, appKey, appToken) {
    __publicField(this, "baseURL");
    __publicField(this, "headers");
    this.baseURL = "https://" + host, this.headers = {
      "X-VTEX-API-AppKey": appKey,
      "X-VTEX-API-AppToken": appToken
    };
  }
  async getSkuFilesBySkuId(skuID) {
    let route = `/api/catalog/pvt/stockkeepingunit/${skuID}/file/`;
    return (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.GET,
      url: this.baseURL + route,
      headers: this.headers
    })).body;
  }
  async createSkuFile(skuID, newSkuFileData) {
    let route = `/api/catalog/pvt/stockkeepingunit/${skuID}/file`;
    return (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.POST,
      url: this.baseURL + route,
      headers: this.headers,
      body: newSkuFileData
    })).body;
  }
  async updateSkuFile(skuID, skuFileID, updatedSkuFileData) {
    let route = `/api/catalog/pvt/stockkeepingunit/${skuID}/file/${skuFileID}`;
    return (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.PUT,
      url: this.baseURL + route,
      headers: this.headers,
      body: updatedSkuFileData
    })).body;
  }
};

// ../ap/packages/pieces/community/vtex/src/lib/actions/SKU-File/create-sku-file.ts
var createSkuFile = (0, import_pieces_framework14.createAction)({
  auth: vtexAuth,
  name: "create-sku-file",
  displayName: "Create New Sku File",
  description: "Create a new SKU File to your catalog",
  audience: "both",
  aiMetadata: {
    description: "Attach an image file to an existing SKU in a VTEX store catalog, identified by the SKU ID, from a publicly accessible image URL. Requires the image URL, name, and whether it is the main image. Not idempotent: each call attaches another image to the SKU.",
    idempotent: !1
  },
  props: {
    SkuId: import_pieces_framework14.Property.Number({
      displayName: "Sku ID",
      description: "Set the Sku ID",
      required: !0
    }),
    Url: import_pieces_framework14.Property.ShortText({
      displayName: "Image Url",
      required: !0
    }),
    Name: import_pieces_framework14.Property.ShortText({
      displayName: "Image Name",
      required: !0
    }),
    IsMain: import_pieces_framework14.Property.Checkbox({
      displayName: "Is Main Image",
      required: !0
    }),
    Label: import_pieces_framework14.Property.ShortText({
      displayName: "Image Label",
      required: !1
    }),
    Text: import_pieces_framework14.Property.ShortText({
      displayName: "Image Name",
      description: "General text of the image",
      required: !1
    })
  },
  async run(context) {
    let { hostUrl, appKey, appToken } = context.auth.props;
    return await new SkuFile(hostUrl, appKey, appToken).createSkuFile(context.propsValue.SkuId, {
      Url: context.propsValue.Url,
      Name: context.propsValue.Name,
      IsMain: context.propsValue.IsMain,
      Label: context.propsValue.Label,
      Text: context.propsValue.Text
    });
  }
});

// ../ap/packages/pieces/community/vtex/src/lib/actions/SKU/create-sku.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/vtex/src/lib/common/SKU.ts
var import_pieces_common7 = require("@activepieces/pieces-common");
var Sku = class {
  constructor(host, appKey, appToken) {
    __publicField(this, "baseURL");
    __publicField(this, "headers");
    this.baseURL = "https://" + host, this.headers = {
      "X-VTEX-API-AppKey": appKey,
      "X-VTEX-API-AppToken": appToken
    };
  }
  async getSkuById(skuID) {
    return (await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.GET,
      url: this.baseURL + "/api/catalog/pvt/stockkeepingunit/" + skuID,
      headers: this.headers
    })).body;
  }
  async getSkuListByProductId(productID) {
    return (await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.GET,
      url: this.baseURL + "/api/catalog_system/pvt/sku/stockkeepingunitByProductId/" + productID,
      headers: this.headers
    })).body;
  }
  async createSku(newSkuData) {
    return (await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.POST,
      url: this.baseURL + "/api/catalog/pvt/stockkeepingunit",
      headers: this.headers,
      body: newSkuData
    })).body;
  }
  async updateSku(skuID, newSkuData) {
    return (await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.PUT,
      url: this.baseURL + "/api/catalog/pvt/stockkeepingunit/" + skuID,
      headers: this.headers,
      body: newSkuData
    })).body;
  }
};

// ../ap/packages/pieces/community/vtex/src/lib/actions/SKU/create-sku.ts
var createSku = (0, import_pieces_framework15.createAction)({
  auth: vtexAuth,
  name: "create-sku",
  displayName: "Create New Sku",
  description: "Create a new SKU to your catalog",
  audience: "both",
  aiMetadata: {
    description: "Create a new SKU (product variation) in a VTEX store catalog, associated with an existing product ID. Requires the SKU name and packaging dimensions/weight. Optionally pass a SKU ID to set it explicitly. Not idempotent: each call creates a separate SKU.",
    idempotent: !1
  },
  props: {
    ProductId: import_pieces_framework15.Property.Number({
      displayName: "Product ID",
      description: "ID of the product to be associated with this SKU",
      required: !0
    }),
    Name: import_pieces_framework15.Property.ShortText({
      displayName: "SKU Name",
      description: "Name the variation of the product",
      required: !0
    }),
    PackagedHeight: import_pieces_framework15.Property.Number({
      displayName: "Package Height",
      required: !0
    }),
    PackagedLength: import_pieces_framework15.Property.Number({
      displayName: "Package Length",
      required: !0
    }),
    PackagedWidth: import_pieces_framework15.Property.Number({
      displayName: "Package Width",
      required: !0
    }),
    PackagedWeightKg: import_pieces_framework15.Property.Number({
      displayName: "Package Weight (Kg)",
      required: !0
    }),
    IsActive: import_pieces_framework15.Property.Checkbox({
      displayName: "Is Active",
      required: !1
    }),
    IsKit: import_pieces_framework15.Property.Checkbox({
      displayName: "Is Kit",
      required: !1
    }),
    Id: import_pieces_framework15.Property.Number({
      displayName: "Sku ID",
      description: "Set the Sku ID",
      required: !1
    })
  },
  async run(context) {
    let { hostUrl, appKey, appToken } = context.auth.props;
    return await new Sku(hostUrl, appKey, appToken).createSku({
      ProductId: context.propsValue.ProductId,
      Name: context.propsValue.Name,
      PackagedHeight: context.propsValue.PackagedHeight,
      PackagedLength: context.propsValue.PackagedLength,
      PackagedWidth: context.propsValue.PackagedWidth,
      PackagedWeightKg: context.propsValue.PackagedWeightKg,
      IsActive: context.propsValue.IsActive,
      IsKit: context.propsValue.IsKit,
      Id: context.propsValue.Id
    });
  }
});

// ../ap/packages/pieces/community/vtex/src/lib/actions/SKU/get-sku-by-product-id.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var getSkuByProductId = (0, import_pieces_framework16.createAction)({
  auth: vtexAuth,
  name: "get-sku-by-product-id",
  displayName: "Get SKU By Product ID",
  description: "Find a Sku in your catalog by a Product id",
  audience: "both",
  aiMetadata: {
    description: "List the SKUs (product variations) associated with a given product in a VTEX store catalog, identified by the numeric product ID. Use to enumerate a product's variants. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    productId: import_pieces_framework16.Property.Number({
      displayName: "Product ID",
      description: "The Product ID",
      required: !0
    })
  },
  async run(context) {
    let { hostUrl, appKey, appToken } = context.auth.props, { productId } = context.propsValue;
    return await new Sku(hostUrl, appKey, appToken).getSkuListByProductId(productId);
  }
});

// ../ap/packages/pieces/community/vtex/src/index.ts
var markdownDescription = `
**Host Url**: The VTEX store host (e.g \`{{accountName}}.{{environment}}.com\`)
**App Key** and **App Token**: To get your app key and app token, follow the steps below:
1. Go to your vtex account on **Account Settings** -> **Account** -> **Security**
2. Click on **Generate access key and secret**
4. Copy the access key as your **App Key** and the secret is your **App Token**.
`, vtexAuth = import_pieces_framework17.PieceAuth.CustomAuth({
  description: markdownDescription,
  props: {
    hostUrl: import_pieces_framework17.Property.ShortText({
      displayName: "Host Url",
      description: "{accountName}.{environment}.com",
      required: !0
    }),
    appKey: import_pieces_framework17.PieceAuth.SecretText({
      displayName: "App Key",
      description: "VTEX App Key",
      required: !0
    }),
    appToken: import_pieces_framework17.PieceAuth.SecretText({
      displayName: "App Token",
      description: "VTEX App Token",
      required: !0
    })
  },
  required: !0
}), vtex = (0, import_pieces_framework17.createPiece)({
  displayName: "VTEX",
  description: "Unified commerce platform",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/vtex.png",
  categories: [import_pieces_framework18.PieceCategory.COMMERCE],
  authors: ["Willianwg", "kishanprmr", "MoShizzle", "AbdulTheActivePiecer", "khaledmashaly", "abuaboud"],
  auth: vtexAuth,
  actions: [
    getProductById,
    createProduct,
    updateProduct,
    getBrandList,
    getBrandById,
    createBrand,
    updateBrand,
    deleteBrand,
    getCategoryById,
    getSkuByProductId,
    createSku,
    createSkuFile,
    getClientList,
    getClientById,
    getOrderById,
    getOrderList,
    (0, import_pieces_common8.createCustomApiCallAction)({
      baseUrl: (auth) => `https://${auth == null ? void 0 : auth.props.hostUrl}`,
      auth: vtexAuth,
      authMapping: async (auth) => ({
        "X-VTEX-API-AppKey": auth.props.appKey,
        "X-VTEX-API-AppToken": auth.props.appToken
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  vtex,
  vtexAuth
});
