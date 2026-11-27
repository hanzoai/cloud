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

// ../ap/packages/pieces/community/cyberark/src/index.ts
var index_exports = {};
__export(index_exports, {
  cyberark: () => cyberark
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework23 = require("@activepieces/pieces-framework"), import_pieces_framework24 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/cyberark/src/lib/actions/create-user.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/cyberark/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), cyberarkAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: "CyberArk PVWA Authentication",
  props: {
    serverUrl: import_pieces_framework.Property.ShortText({
      displayName: "PVWA Server URL",
      description: "The PVWA server URL (e.g., https://pvwa-server)",
      required: !0
    }),
    username: import_pieces_framework.Property.ShortText({
      displayName: "Username",
      description: "CyberArk username",
      required: !0
    }),
    password: import_pieces_framework.Property.ShortText({
      displayName: "Password",
      description: "CyberArk password",
      required: !0
    })
  },
  required: !0
});

// ../ap/packages/pieces/community/cyberark/src/lib/actions/create-user.ts
var import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/cyberark/src/lib/common/auth-helper.ts
var import_pieces_common = require("@activepieces/pieces-common");
async function getAuthToken(auth) {
  let baseUrl = auth.props.serverUrl.replace(/\/$/, "");
  try {
    let response = await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: `${baseUrl}/PasswordVault/API/auth/Cyberark/Logon/`,
      headers: {
        "Content-Type": "application/json"
      },
      body: {
        username: auth.props.username,
        password: auth.props.password
      }
    });
    if (response.status === 200 && response.body) {
      let token = response.body;
      if (typeof token == "string" && (token = token.replace(/^["']|["']$/g, "").trim()), typeof token == "string" && token.length > 0)
        return {
          token,
          serverUrl: baseUrl
        };
      throw new Error("Invalid token received from authentication");
    } else
      throw new Error(`Authentication failed with status: ${response.status}. Response: ${JSON.stringify(response.body)}`);
  } catch (error) {
    throw error instanceof Error ? new Error(`Failed to authenticate: ${error.message}`) : new Error(`Failed to authenticate: ${String(error)}`);
  }
}

// ../ap/packages/pieces/community/cyberark/src/lib/actions/create-user.ts
var createUser = (0, import_pieces_framework2.createAction)({
  auth: cyberarkAuth,
  name: "create_user",
  displayName: "Create User",
  description: "Creates a new user in the CyberArk Vault",
  audience: "both",
  aiMetadata: {
    description: "Provisions a new user account in the CyberArk Vault with the given username, user type, authentication method, vault authorizations, and optional profile details. Use to onboard a Vault user. Not idempotent: each call creates another user, and a duplicate username will fail.",
    idempotent: !1
  },
  props: {
    username: import_pieces_framework2.Property.ShortText({
      displayName: "Username",
      description: "The name of the user (max 128 characters)",
      required: !0
    }),
    userType: import_pieces_framework2.Property.ShortText({
      displayName: "User Type",
      description: "The user type according to license",
      required: !1,
      defaultValue: "EPVUser"
    }),
    initialPassword: import_pieces_framework2.Property.ShortText({
      displayName: "Initial Password",
      description: "Password for first-time login (max 39 characters)",
      required: !1
    }),
    authenticationMethod: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Authentication Method",
      description: "The authentication method for login",
      required: !1,
      options: {
        options: [
          { label: "CyberArk", value: "AuthTypePass" },
          { label: "Radius", value: "AuthTypeRadius" },
          { label: "LDAP", value: "AuthTypeLDAP" }
        ]
      },
      defaultValue: "AuthTypePass"
    }),
    allowedAuthenticationMethods: import_pieces_framework2.Property.StaticMultiSelectDropdown({
      displayName: "Allowed Authentication Methods",
      description: "Non-Vault authentication methods the user can use",
      required: !1,
      options: {
        options: [
          { label: "SAML", value: "SAML" },
          { label: "PKI", value: "PKI" }
        ]
      }
    }),
    location: import_pieces_framework2.Property.ShortText({
      displayName: "Location",
      description: "Vault location for user creation (must start with \\\\)",
      required: !1,
      defaultValue: "\\\\"
    }),
    enableUser: import_pieces_framework2.Property.Checkbox({
      displayName: "Enable User",
      description: "Whether the user will be enabled upon creation",
      required: !1,
      defaultValue: !0
    }),
    changePassOnNextLogon: import_pieces_framework2.Property.Checkbox({
      displayName: "Change Password on Next Logon",
      description: "User must change password on next login",
      required: !1,
      defaultValue: !0
    }),
    passwordNeverExpires: import_pieces_framework2.Property.Checkbox({
      displayName: "Password Never Expires",
      description: "Password will not expire unless user changes it",
      required: !1,
      defaultValue: !1
    }),
    description: import_pieces_framework2.Property.LongText({
      displayName: "Description",
      description: "Notes and comments (max 99 characters)",
      required: !1
    }),
    vaultAuthorization: import_pieces_framework2.Property.StaticMultiSelectDropdown({
      displayName: "Vault Authorization",
      description: "User permissions",
      required: !1,
      options: {
        options: [
          { label: "Add Safes", value: "AddSafes" },
          { label: "Audit Users", value: "AuditUsers" },
          { label: "Add Update Users", value: "AddUpdateUsers" },
          { label: "Reset Users Passwords", value: "ResetUsersPasswords" },
          { label: "Activate Users", value: "ActivateUsers" },
          { label: "Add Network Areas", value: "AddNetworkAreas" },
          { label: "Manage Directory Mapping", value: "ManageDirectoryMapping" },
          { label: "Manage Server File Categories", value: "ManageServerFileCategories" },
          { label: "Backup All Safes", value: "BackupAllSafes" },
          { label: "Restore All Safes", value: "RestoreAllSafes" }
        ]
      }
    }),
    firstName: import_pieces_framework2.Property.ShortText({
      displayName: "First Name",
      description: "First name (max 29 characters)",
      required: !1
    }),
    middleName: import_pieces_framework2.Property.ShortText({
      displayName: "Middle Name",
      description: "Middle name (max 29 characters)",
      required: !1
    }),
    lastName: import_pieces_framework2.Property.ShortText({
      displayName: "Last Name",
      description: "Last name (max 29 characters)",
      required: !1
    }),
    homeEmail: import_pieces_framework2.Property.ShortText({
      displayName: "Home Email",
      description: "Home email address (max 319 characters)",
      required: !1
    }),
    businessEmail: import_pieces_framework2.Property.ShortText({
      displayName: "Business Email",
      description: "Business email address (max 319 characters)",
      required: !1
    }),
    homeNumber: import_pieces_framework2.Property.ShortText({
      displayName: "Home Phone",
      description: "Home phone number (max 24 characters)",
      required: !1
    }),
    businessNumber: import_pieces_framework2.Property.ShortText({
      displayName: "Business Phone",
      description: "Business phone number (max 24 characters)",
      required: !1
    }),
    cellularNumber: import_pieces_framework2.Property.ShortText({
      displayName: "Cellular Phone",
      description: "Cellular phone number (max 24 characters)",
      required: !1
    }),
    faxNumber: import_pieces_framework2.Property.ShortText({
      displayName: "Fax Number",
      description: "Fax number (max 24 characters)",
      required: !1
    }),
    pagerNumber: import_pieces_framework2.Property.ShortText({
      displayName: "Pager Number",
      description: "Pager number (max 24 characters)",
      required: !1
    }),
    expiryDate: import_pieces_framework2.Property.DateTime({
      displayName: "Expiry Date",
      description: "Date when the user expires",
      required: !1
    }),
    unAuthorizedInterfaces: import_pieces_framework2.Property.StaticMultiSelectDropdown({
      displayName: "Unauthorized Interfaces",
      description: "Interfaces the user cannot access",
      required: !1,
      options: {
        options: [
          { label: "PSM", value: "PSM" },
          { label: "PSMP", value: "PSMP" },
          { label: "PVWA", value: "PVWA" },
          { label: "WINCLIENT", value: "WINCLIENT" },
          { label: "PTA", value: "PTA" },
          { label: "PACLI", value: "PACLI" },
          { label: "HTTPGW", value: "HTTPGW" },
          { label: "EVD", value: "EVD" },
          { label: "PIMSu", value: "PIMSu" },
          { label: "AIMApp", value: "AIMApp" },
          { label: "CPM", value: "CPM" },
          { label: "PVWAApp", value: "PVWAApp" },
          { label: "PSMApp", value: "PSMApp" },
          { label: "AppPrv", value: "AppPrv" },
          { label: "PSMPApp", value: "PSMPApp" }
        ]
      }
    }),
    workStreet: import_pieces_framework2.Property.ShortText({
      displayName: "Work Street",
      description: "Business street address (max 29 characters)",
      required: !1
    }),
    workCity: import_pieces_framework2.Property.ShortText({
      displayName: "Work City",
      description: "Business city (max 19 characters)",
      required: !1
    }),
    workState: import_pieces_framework2.Property.ShortText({
      displayName: "Work State",
      description: "Business state (max 19 characters)",
      required: !1
    }),
    workZip: import_pieces_framework2.Property.ShortText({
      displayName: "Work ZIP",
      description: "Business ZIP code (max 19 characters)",
      required: !1
    }),
    workCountry: import_pieces_framework2.Property.ShortText({
      displayName: "Work Country",
      description: "Business country (max 19 characters)",
      required: !1
    }),
    street: import_pieces_framework2.Property.ShortText({
      displayName: "Home Street",
      description: "Home street address (max 29 characters)",
      required: !1
    }),
    city: import_pieces_framework2.Property.ShortText({
      displayName: "Home City",
      description: "Home city (max 19 characters)",
      required: !1
    }),
    state: import_pieces_framework2.Property.ShortText({
      displayName: "Home State",
      description: "Home state (max 19 characters)",
      required: !1
    }),
    zip: import_pieces_framework2.Property.ShortText({
      displayName: "Home ZIP",
      description: "Home ZIP code (max 19 characters)",
      required: !1
    }),
    country: import_pieces_framework2.Property.ShortText({
      displayName: "Home Country",
      description: "Home country (max 19 characters)",
      required: !1
    }),
    title: import_pieces_framework2.Property.ShortText({
      displayName: "Title",
      description: "Professional title (max 49 characters)",
      required: !1
    }),
    organization: import_pieces_framework2.Property.ShortText({
      displayName: "Organization",
      description: "Organization name (max 49 characters)",
      required: !1
    }),
    department: import_pieces_framework2.Property.ShortText({
      displayName: "Department",
      description: "Department (max 49 characters)",
      required: !1
    }),
    profession: import_pieces_framework2.Property.ShortText({
      displayName: "Profession",
      description: "Profession (max 49 characters)",
      required: !1
    }),
    homePage: import_pieces_framework2.Property.ShortText({
      displayName: "Home Page",
      description: "Personal website (max 319 characters)",
      required: !1
    }),
    otherEmail: import_pieces_framework2.Property.ShortText({
      displayName: "Other Email",
      description: "Additional email address (max 319 characters)",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c;
    let authData = await getAuthToken(context.auth), requestBody = {
      username: context.propsValue.username,
      userType: context.propsValue.userType || "EPVUser",
      enableUser: (_a = context.propsValue.enableUser) != null ? _a : !0,
      changePassOnNextLogon: (_b = context.propsValue.changePassOnNextLogon) != null ? _b : !0,
      passwordNeverExpires: (_c = context.propsValue.passwordNeverExpires) != null ? _c : !1
    };
    context.propsValue.initialPassword && (requestBody.initialPassword = context.propsValue.initialPassword), context.propsValue.authenticationMethod && (requestBody.authenticationMethod = context.propsValue.authenticationMethod), context.propsValue.allowedAuthenticationMethods && (requestBody.allowedAuthenticationMethods = context.propsValue.allowedAuthenticationMethods), context.propsValue.location && (requestBody.location = context.propsValue.location), context.propsValue.expiryDate && (requestBody.expiryDate = Math.floor(new Date(context.propsValue.expiryDate).getTime() / 1e3)), context.propsValue.unAuthorizedInterfaces && (requestBody.unAuthorizedInterfaces = context.propsValue.unAuthorizedInterfaces), context.propsValue.description && (requestBody.description = context.propsValue.description), context.propsValue.vaultAuthorization && (requestBody.vaultAuthorization = context.propsValue.vaultAuthorization);
    let personalDetails = {};
    context.propsValue.firstName && (personalDetails.firstName = context.propsValue.firstName), context.propsValue.middleName && (personalDetails.middleName = context.propsValue.middleName), context.propsValue.lastName && (personalDetails.lastName = context.propsValue.lastName), context.propsValue.street && (personalDetails.street = context.propsValue.street), context.propsValue.city && (personalDetails.city = context.propsValue.city), context.propsValue.state && (personalDetails.state = context.propsValue.state), context.propsValue.zip && (personalDetails.zip = context.propsValue.zip), context.propsValue.country && (personalDetails.country = context.propsValue.country), context.propsValue.title && (personalDetails.title = context.propsValue.title), context.propsValue.organization && (personalDetails.organization = context.propsValue.organization), context.propsValue.department && (personalDetails.department = context.propsValue.department), context.propsValue.profession && (personalDetails.profession = context.propsValue.profession), Object.keys(personalDetails).length > 0 && (requestBody.personalDetails = personalDetails);
    let internet = {};
    context.propsValue.homePage && (internet.homePage = context.propsValue.homePage), context.propsValue.homeEmail && (internet.homeEmail = context.propsValue.homeEmail), context.propsValue.businessEmail && (internet.businessEmail = context.propsValue.businessEmail), context.propsValue.otherEmail && (internet.otherEmail = context.propsValue.otherEmail), Object.keys(internet).length > 0 && (requestBody.internet = internet);
    let phones = {};
    context.propsValue.homeNumber && (phones.homeNumber = context.propsValue.homeNumber), context.propsValue.businessNumber && (phones.businessNumber = context.propsValue.businessNumber), context.propsValue.cellularNumber && (phones.cellularNumber = context.propsValue.cellularNumber), context.propsValue.faxNumber && (phones.faxNumber = context.propsValue.faxNumber), context.propsValue.pagerNumber && (phones.pagerNumber = context.propsValue.pagerNumber), Object.keys(phones).length > 0 && (requestBody.phones = phones);
    let businessAddress = {};
    context.propsValue.workStreet && (businessAddress.workStreet = context.propsValue.workStreet), context.propsValue.workCity && (businessAddress.workCity = context.propsValue.workCity), context.propsValue.workState && (businessAddress.workState = context.propsValue.workState), context.propsValue.workZip && (businessAddress.workZip = context.propsValue.workZip), context.propsValue.workCountry && (businessAddress.workCountry = context.propsValue.workCountry), Object.keys(businessAddress).length > 0 && (requestBody.businessAddress = businessAddress);
    try {
      let response = await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.POST,
        url: `${authData.serverUrl}/PasswordVault/API/Users`,
        headers: {
          "Content-Type": "application/json",
          Authorization: authData.token
        },
        body: requestBody
      });
      return response.status === 201 ? {
        success: !0,
        user: response.body
      } : {
        success: !1,
        error: `Failed to create user. Status: ${response.status}`,
        details: response.body
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to create user",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/cyberark/src/lib/actions/update-user.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/cyberark/src/lib/common/user-dropdown.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var userIdDropdown = import_pieces_framework3.Property.Dropdown({
  displayName: "User",
  description: "Select a user from the Vault (shows username, ID, type, and status)",
  required: !0,
  refreshers: [],
  auth: cyberarkAuth,
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please authenticate first",
        options: []
      };
    try {
      let authData = await getAuthToken(auth), response = await import_pieces_common3.httpClient.sendRequest({
        method: import_pieces_common3.HttpMethod.GET,
        url: `${authData.serverUrl}/PasswordVault/API/Users?pageSize=1000`,
        headers: {
          "Content-Type": "application/json",
          Authorization: authData.token
        }
      });
      return response.status === 200 && ((_a = response.body) != null && _a.Users) ? {
        disabled: !1,
        options: response.body.Users.map((user) => ({
          label: `${user.username} (ID: ${user.id}) - ${user.userType}${user.componentUser ? " [Component]" : ""}${user.suspended ? " [Suspended]" : ""}${user.enabled ? "" : " [Disabled]"}`,
          value: user.id.toString()
        }))
      } : {
        disabled: !0,
        placeholder: "Failed to load users",
        options: []
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Error loading users",
        options: []
      };
    }
  }
});

// ../ap/packages/pieces/community/cyberark/src/lib/actions/update-user.ts
var updateUser = (0, import_pieces_framework4.createAction)({
  auth: cyberarkAuth,
  name: "update_user",
  displayName: "Update User",
  description: "Updates an existing Vault user (except Master and Batch built-in users)",
  audience: "both",
  aiMetadata: {
    description: "Updates the properties of an existing Vault user identified by user ID \u2014 including username, user type, enabled/suspended state, authentication methods, vault authorizations, and profile details. Use to modify an already-provisioned user; cannot target the built-in Master or Batch users. Idempotent: re-sending the same field values leaves the user in the same state.",
    idempotent: !0
  },
  props: {
    userId: userIdDropdown,
    username: import_pieces_framework4.Property.ShortText({
      displayName: "Username",
      description: "The name of the user (max 128 characters)",
      required: !0
    }),
    userType: import_pieces_framework4.Property.ShortText({
      displayName: "User Type",
      description: "The user type according to license",
      required: !1,
      defaultValue: "EPVUser"
    }),
    enableUser: import_pieces_framework4.Property.Checkbox({
      displayName: "Enable User",
      description: "Whether the user is enabled",
      required: !1,
      defaultValue: !0
    }),
    changePassOnNextLogon: import_pieces_framework4.Property.Checkbox({
      displayName: "Change Password on Next Logon",
      description: "User must change password on next login",
      required: !1,
      defaultValue: !0
    }),
    passwordNeverExpires: import_pieces_framework4.Property.Checkbox({
      displayName: "Password Never Expires",
      description: "Password will not expire unless user changes it",
      required: !1,
      defaultValue: !1
    }),
    suspended: import_pieces_framework4.Property.Checkbox({
      displayName: "Suspended",
      description: "Whether the user is suspended",
      required: !1,
      defaultValue: !1
    }),
    expiryDate: import_pieces_framework4.Property.DateTime({
      displayName: "Expiry Date",
      description: "Date when the user expires",
      required: !1
    }),
    userActivityLogRetentionDays: import_pieces_framework4.Property.Number({
      displayName: "Activity Log Retention Days",
      description: "Days to retain user activity records (default: 90)",
      required: !1,
      defaultValue: 90
    }),
    authenticationMethod: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Authentication Method",
      description: "The authentication method for login",
      required: !1,
      options: {
        options: [
          { label: "CyberArk", value: "AuthTypePass" },
          { label: "Radius", value: "AuthTypeRadius" },
          { label: "LDAP", value: "AuthTypeLDAP" }
        ]
      },
      defaultValue: "AuthTypePass"
    }),
    allowedAuthenticationMethods: import_pieces_framework4.Property.StaticMultiSelectDropdown({
      displayName: "Allowed Authentication Methods",
      description: "Non-Vault authentication methods the user can use",
      required: !1,
      options: {
        options: [
          { label: "SAML", value: "SAML" },
          { label: "PKI", value: "PKI" }
        ]
      }
    }),
    unAuthorizedInterfaces: import_pieces_framework4.Property.StaticMultiSelectDropdown({
      displayName: "Unauthorized Interfaces",
      description: "CyberArk interfaces this user cannot access",
      required: !1,
      options: {
        options: [
          { label: "PIMSU", value: "PIMSU" },
          { label: "PSM", value: "PSM" },
          { label: "PSMP", value: "PSMP" },
          { label: "PVWA", value: "PVWA" },
          { label: "WINCLIENT", value: "WINCLIENT" },
          { label: "PTA", value: "PTA" },
          { label: "PACLI", value: "PACLI" },
          { label: "HTTPGW", value: "HTTPGW" },
          { label: "EVD", value: "EVD" },
          { label: "PIMSu", value: "PIMSu" },
          { label: "AIMApp", value: "AIMApp" },
          { label: "CPM", value: "CPM" },
          { label: "PVWAApp", value: "PVWAApp" },
          { label: "PSMApp", value: "PSMApp" },
          { label: "AppPrv", value: "AppPrv" },
          { label: "PSMPApp", value: "PSMPApp" }
        ]
      }
    }),
    location: import_pieces_framework4.Property.ShortText({
      displayName: "Location",
      description: "Vault location (must start with \\\\)",
      required: !1,
      defaultValue: "\\\\"
    }),
    distinguishedName: import_pieces_framework4.Property.ShortText({
      displayName: "Distinguished Name",
      description: "Distinguished name for PKI authentication",
      required: !1
    }),
    password: import_pieces_framework4.Property.ShortText({
      displayName: "Password",
      description: "New password for the user (max 39 characters)",
      required: !1
    }),
    description: import_pieces_framework4.Property.LongText({
      displayName: "Description",
      description: "Notes and comments (max 99 characters)",
      required: !1
    }),
    vaultAuthorization: import_pieces_framework4.Property.StaticMultiSelectDropdown({
      displayName: "Vault Authorization",
      description: "User permissions",
      required: !1,
      options: {
        options: [
          { label: "Add Safes", value: "AddSafes" },
          { label: "Audit Users", value: "AuditUsers" },
          { label: "Add Update Users", value: "AddUpdateUsers" },
          { label: "Reset Users Passwords", value: "ResetUsersPasswords" },
          { label: "Activate Users", value: "ActivateUsers" },
          { label: "Add Network Areas", value: "AddNetworkAreas" },
          {
            label: "Manage Directory Mapping",
            value: "ManageDirectoryMapping"
          },
          {
            label: "Manage Server File Categories",
            value: "ManageServerFileCategories"
          },
          { label: "Backup All Safes", value: "BackupAllSafes" },
          { label: "Restore All Safes", value: "RestoreAllSafes" }
        ]
      }
    }),
    firstName: import_pieces_framework4.Property.ShortText({
      displayName: "First Name",
      description: "First name (max 29 characters)",
      required: !1
    }),
    middleName: import_pieces_framework4.Property.ShortText({
      displayName: "Middle Name",
      description: "Middle name (max 29 characters)",
      required: !1
    }),
    lastName: import_pieces_framework4.Property.ShortText({
      displayName: "Last Name",
      description: "Last name (max 29 characters)",
      required: !1
    }),
    title: import_pieces_framework4.Property.ShortText({
      displayName: "Title",
      description: "Title (max 49 characters)",
      required: !1
    }),
    organization: import_pieces_framework4.Property.ShortText({
      displayName: "Organization",
      description: "Organization (max 49 characters)",
      required: !1
    }),
    department: import_pieces_framework4.Property.ShortText({
      displayName: "Department",
      description: "Department (max 49 characters)",
      required: !1
    }),
    profession: import_pieces_framework4.Property.ShortText({
      displayName: "Profession",
      description: "Profession (max 49 characters)",
      required: !1
    }),
    street: import_pieces_framework4.Property.ShortText({
      displayName: "Street",
      description: "Street address (max 29 characters)",
      required: !1
    }),
    city: import_pieces_framework4.Property.ShortText({
      displayName: "City",
      description: "City (max 19 characters)",
      required: !1
    }),
    state: import_pieces_framework4.Property.ShortText({
      displayName: "State",
      description: "State (max 19 characters)",
      required: !1
    }),
    zip: import_pieces_framework4.Property.ShortText({
      displayName: "ZIP Code",
      description: "ZIP code (max 19 characters)",
      required: !1
    }),
    country: import_pieces_framework4.Property.ShortText({
      displayName: "Country",
      description: "Country (max 19 characters)",
      required: !1
    }),
    workStreet: import_pieces_framework4.Property.ShortText({
      displayName: "Work Street",
      description: "Work street address (max 29 characters)",
      required: !1
    }),
    workCity: import_pieces_framework4.Property.ShortText({
      displayName: "Work City",
      description: "Work city (max 19 characters)",
      required: !1
    }),
    workState: import_pieces_framework4.Property.ShortText({
      displayName: "Work State",
      description: "Work state (max 19 characters)",
      required: !1
    }),
    workZip: import_pieces_framework4.Property.ShortText({
      displayName: "Work ZIP",
      description: "Work ZIP code (max 19 characters)",
      required: !1
    }),
    workCountry: import_pieces_framework4.Property.ShortText({
      displayName: "Work Country",
      description: "Work country (max 19 characters)",
      required: !1
    }),
    homePage: import_pieces_framework4.Property.ShortText({
      displayName: "Home Page",
      description: "Home page URL (max 319 characters)",
      required: !1
    }),
    homeEmail: import_pieces_framework4.Property.ShortText({
      displayName: "Home Email",
      description: "Home email address (max 319 characters)",
      required: !1
    }),
    businessEmail: import_pieces_framework4.Property.ShortText({
      displayName: "Business Email",
      description: "Business email address (max 319 characters)",
      required: !1
    }),
    otherEmail: import_pieces_framework4.Property.ShortText({
      displayName: "Other Email",
      description: "Other email address (max 319 characters)",
      required: !1
    }),
    homeNumber: import_pieces_framework4.Property.ShortText({
      displayName: "Home Phone",
      description: "Home phone number (max 24 characters)",
      required: !1
    }),
    businessNumber: import_pieces_framework4.Property.ShortText({
      displayName: "Business Phone",
      description: "Business phone number (max 24 characters)",
      required: !1
    }),
    cellularNumber: import_pieces_framework4.Property.ShortText({
      displayName: "Cellular Phone",
      description: "Cellular phone number (max 24 characters)",
      required: !1
    }),
    faxNumber: import_pieces_framework4.Property.ShortText({
      displayName: "Fax Number",
      description: "Fax number (max 24 characters)",
      required: !1
    }),
    pagerNumber: import_pieces_framework4.Property.ShortText({
      displayName: "Pager Number",
      description: "Pager number (max 24 characters)",
      required: !1
    }),
    loginFromHour: import_pieces_framework4.Property.DateTime({
      displayName: "Login From Hour",
      description: "Starting time when user can log in",
      required: !1
    }),
    loginToHour: import_pieces_framework4.Property.DateTime({
      displayName: "Login To Hour",
      description: "Ending time when user can log in",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d;
    let authData = await getAuthToken(context.auth), requestBody = {
      id: parseInt(context.propsValue.userId),
      username: context.propsValue.username,
      userType: context.propsValue.userType || "EPVUser",
      enableUser: (_a = context.propsValue.enableUser) != null ? _a : !0,
      changePassOnNextLogon: (_b = context.propsValue.changePassOnNextLogon) != null ? _b : !0,
      passwordNeverExpires: (_c = context.propsValue.passwordNeverExpires) != null ? _c : !1,
      suspended: (_d = context.propsValue.suspended) != null ? _d : !1,
      source: "CyberArk",
      componentUser: !1,
      location: context.propsValue.location || "\\\\"
    };
    context.propsValue.expiryDate && (requestBody.expiryDate = Math.floor(new Date(context.propsValue.expiryDate).getTime() / 1e3)), context.propsValue.loginFromHour && (requestBody.loginFromHour = Math.floor(new Date(context.propsValue.loginFromHour).getTime() / 1e3)), context.propsValue.loginToHour && (requestBody.loginToHour = Math.floor(new Date(context.propsValue.loginToHour).getTime() / 1e3)), context.propsValue.userActivityLogRetentionDays !== void 0 && (requestBody.userActivityLogRetentionDays = context.propsValue.userActivityLogRetentionDays), context.propsValue.authenticationMethod && (requestBody.authenticationMethod = [context.propsValue.authenticationMethod]), context.propsValue.allowedAuthenticationMethods && (requestBody.allowedAuthenticationMethods = context.propsValue.allowedAuthenticationMethods), context.propsValue.unAuthorizedInterfaces && (requestBody.unAuthorizedInterfaces = context.propsValue.unAuthorizedInterfaces), context.propsValue.distinguishedName && (requestBody.distinguishedName = context.propsValue.distinguishedName), context.propsValue.password && (requestBody.password = context.propsValue.password), context.propsValue.description && (requestBody.description = context.propsValue.description), context.propsValue.vaultAuthorization && (requestBody.vaultAuthorization = context.propsValue.vaultAuthorization);
    let personalDetails = {};
    context.propsValue.firstName && (personalDetails.firstName = context.propsValue.firstName), context.propsValue.middleName && (personalDetails.middleName = context.propsValue.middleName), context.propsValue.lastName && (personalDetails.lastName = context.propsValue.lastName), context.propsValue.title && (personalDetails.title = context.propsValue.title), context.propsValue.organization && (personalDetails.organization = context.propsValue.organization), context.propsValue.department && (personalDetails.department = context.propsValue.department), context.propsValue.profession && (personalDetails.profession = context.propsValue.profession), context.propsValue.street && (personalDetails.street = context.propsValue.street), context.propsValue.city && (personalDetails.city = context.propsValue.city), context.propsValue.state && (personalDetails.state = context.propsValue.state), context.propsValue.zip && (personalDetails.zip = context.propsValue.zip), context.propsValue.country && (personalDetails.country = context.propsValue.country), Object.keys(personalDetails).length > 0 && (requestBody.personalDetails = personalDetails);
    let businessAddress = {};
    context.propsValue.workStreet && (businessAddress.workStreet = context.propsValue.workStreet), context.propsValue.workCity && (businessAddress.workCity = context.propsValue.workCity), context.propsValue.workState && (businessAddress.workState = context.propsValue.workState), context.propsValue.workZip && (businessAddress.workZip = context.propsValue.workZip), context.propsValue.workCountry && (businessAddress.workCountry = context.propsValue.workCountry), Object.keys(businessAddress).length > 0 && (requestBody.businessAddress = businessAddress);
    let internet = {};
    context.propsValue.homePage && (internet.homePage = context.propsValue.homePage), context.propsValue.homeEmail && (internet.homeEmail = context.propsValue.homeEmail), context.propsValue.businessEmail && (internet.businessEmail = context.propsValue.businessEmail), context.propsValue.otherEmail && (internet.otherEmail = context.propsValue.otherEmail), Object.keys(internet).length > 0 && (requestBody.internet = internet);
    let phones = {};
    context.propsValue.homeNumber && (phones.homeNumber = context.propsValue.homeNumber), context.propsValue.businessNumber && (phones.businessNumber = context.propsValue.businessNumber), context.propsValue.cellularNumber && (phones.cellularNumber = context.propsValue.cellularNumber), context.propsValue.faxNumber && (phones.faxNumber = context.propsValue.faxNumber), context.propsValue.pagerNumber && (phones.pagerNumber = context.propsValue.pagerNumber), Object.keys(phones).length > 0 && (requestBody.phones = phones);
    try {
      let response = await import_pieces_common4.httpClient.sendRequest({
        method: import_pieces_common4.HttpMethod.PUT,
        url: `${authData.serverUrl}/PasswordVault/API/Users/${context.propsValue.userId}/`,
        headers: {
          "Content-Type": "application/json",
          Authorization: authData.token
        },
        body: requestBody
      });
      return response.status === 200 ? {
        success: !0,
        user: response.body
      } : {
        success: !1,
        error: `Failed to update user. Status: ${response.status}`,
        details: response.body
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to update user",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/cyberark/src/lib/actions/delete-user.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common");
var deleteUser = (0, import_pieces_framework5.createAction)({
  auth: cyberarkAuth,
  name: "delete_user",
  displayName: "Delete User",
  description: "Deletes a specific user in the Vault (requires Add/Update Users authorization)",
  audience: "both",
  aiMetadata: {
    description: "Permanently deletes a Vault user identified by user ID; requires Add/Update Users authorization and an explicit confirmation flag before it will proceed. Use to deprovision a user. Effectively idempotent: once the user is gone, repeating the call has no further effect (it targets a stable ID, not a create).",
    idempotent: !0
  },
  props: {
    userId: userIdDropdown,
    confirmDeletion: import_pieces_framework5.Property.Checkbox({
      displayName: "Confirm Deletion",
      description: "Check this box to confirm you want to delete the selected user. This action cannot be undone.",
      required: !0,
      defaultValue: !1
    })
  },
  async run(context) {
    let authData = await getAuthToken(context.auth);
    if (!context.propsValue.confirmDeletion)
      return {
        success: !1,
        error: "Deletion not confirmed",
        message: "Please check the confirmation box to proceed with user deletion."
      };
    try {
      let response = await import_pieces_common5.httpClient.sendRequest({
        method: import_pieces_common5.HttpMethod.DELETE,
        url: `${authData.serverUrl}/PasswordVault/API/Users/${context.propsValue.userId}/`,
        headers: {
          "Content-Type": "application/json",
          Authorization: authData.token
        }
      });
      return response.status === 204 || response.status === 200 ? {
        success: !0,
        message: `User ${context.propsValue.userId} deleted successfully`
      } : {
        success: !1,
        error: `Failed to delete user. Status: ${response.status}`,
        details: response.body
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to delete user",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/cyberark/src/lib/actions/activate-user.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common");
var activateUser = (0, import_pieces_framework6.createAction)({
  auth: cyberarkAuth,
  name: "activate_user",
  displayName: "Activate User",
  description: "Activates an existing user who was suspended after entering incorrect credentials multiple times",
  audience: "both",
  aiMetadata: {
    description: "Reactivates a Vault user identified by user ID who was suspended after too many failed login attempts. Use to lift an automatic lockout. Idempotent: activating an already-active user leaves it active.",
    idempotent: !0
  },
  props: {
    userId: userIdDropdown
  },
  async run(context) {
    let authData = await getAuthToken(context.auth);
    try {
      let response = await import_pieces_common6.httpClient.sendRequest({
        method: import_pieces_common6.HttpMethod.POST,
        url: `${authData.serverUrl}/PasswordVault/API/Users/${context.propsValue.userId}/Activate/`,
        headers: {
          "Content-Type": "application/json",
          Authorization: authData.token
        }
      });
      return response.status === 200 || response.status === 204 ? {
        success: !0,
        message: `User ${context.propsValue.userId} activated successfully`
      } : {
        success: !1,
        error: `Failed to activate user. Status: ${response.status}`,
        details: response.body
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to activate user",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/cyberark/src/lib/actions/enable-user.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common");
var enableUser = (0, import_pieces_framework7.createAction)({
  auth: cyberarkAuth,
  name: "enable_user",
  displayName: "Enable User",
  description: "Enables a specific user in the Vault",
  audience: "both",
  aiMetadata: {
    description: "Enables a Vault user identified by user ID so they can authenticate and access the Vault. Use to restore access to a disabled account. Idempotent: enabling an already-enabled user leaves it enabled.",
    idempotent: !0
  },
  props: {
    userId: userIdDropdown
  },
  async run(context) {
    let authData = await getAuthToken(context.auth);
    try {
      let response = await import_pieces_common7.httpClient.sendRequest({
        method: import_pieces_common7.HttpMethod.POST,
        url: `${authData.serverUrl}/PasswordVault/API/Users/${context.propsValue.userId}/enable/`,
        headers: {
          "Content-Type": "application/json",
          Authorization: authData.token
        }
      });
      return response.status === 200 || response.status === 204 ? {
        success: !0,
        message: `User ${context.propsValue.userId} enabled successfully`
      } : {
        success: !1,
        error: `Failed to enable user. Status: ${response.status}`,
        details: response.body
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to enable user",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/cyberark/src/lib/actions/disable-user.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var import_pieces_common8 = require("@activepieces/pieces-common");
var disableUser = (0, import_pieces_framework8.createAction)({
  auth: cyberarkAuth,
  name: "disable_user",
  displayName: "Disable User",
  description: "Disables a specific user in the Vault",
  audience: "both",
  aiMetadata: {
    description: "Disables a Vault user identified by user ID so they can no longer authenticate, without deleting the account. Use to temporarily revoke access. Idempotent: disabling an already-disabled user leaves it disabled.",
    idempotent: !0
  },
  props: {
    userId: userIdDropdown
  },
  async run(context) {
    let authData = await getAuthToken(context.auth);
    try {
      let response = await import_pieces_common8.httpClient.sendRequest({
        method: import_pieces_common8.HttpMethod.POST,
        url: `${authData.serverUrl}/PasswordVault/API/Users/${context.propsValue.userId}/disable/`,
        headers: {
          "Content-Type": "application/json",
          Authorization: authData.token
        }
      });
      return response.status === 200 || response.status === 204 ? {
        success: !0,
        message: `User ${context.propsValue.userId} disabled successfully`
      } : {
        success: !1,
        error: `Failed to disable user. Status: ${response.status}`,
        details: response.body
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to disable user",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/cyberark/src/lib/actions/find-user.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var import_pieces_common9 = require("@activepieces/pieces-common");
var findUser = (0, import_pieces_framework9.createAction)({
  auth: cyberarkAuth,
  name: "find_user",
  displayName: "Find User",
  description: "Returns a list of existing users in the Vault based on filter criteria (requires Audit users permissions)",
  audience: "both",
  aiMetadata: {
    description: "Searches and lists Vault users, optionally narrowing by filter (userType, componentUser, userName), free-text search, user type, sort, and pagination; with no criteria it returns all users. Use to look up a user ID or discover existing users before acting on them. Requires Audit Users permissions. Idempotent read-only lookup.",
    idempotent: !0
  },
  props: {
    filter: import_pieces_framework9.Property.ShortText({
      displayName: "Filter",
      description: "Filter users by userType, componentUser, or userName",
      required: !1
    }),
    search: import_pieces_framework9.Property.ShortText({
      displayName: "Search",
      description: "Search by username, firstname, or lastname",
      required: !1
    }),
    sort: import_pieces_framework9.Property.ShortText({
      displayName: "Sort",
      description: "Sort by property (username, source, userType, location, lastname, firstname, middlename) followed by asc/desc",
      required: !1
    }),
    extendedDetails: import_pieces_framework9.Property.Checkbox({
      displayName: "Extended Details",
      description: "Returns additional user details including groups and userDN for LDAP users",
      required: !1,
      defaultValue: !1
    }),
    pageOffset: import_pieces_framework9.Property.Number({
      displayName: "Page Offset",
      description: "Offset the first user returned in results",
      required: !1,
      defaultValue: 0
    }),
    pageSize: import_pieces_framework9.Property.Number({
      displayName: "Page Size",
      description: "Maximum number of users to return (used with offset)",
      required: !1,
      defaultValue: 0
    }),
    componentUser: import_pieces_framework9.Property.Checkbox({
      displayName: "Component User Only",
      description: "Filter to show only component users",
      required: !1
    }),
    userType: import_pieces_framework9.Property.StaticDropdown({
      displayName: "User Type",
      description: "Filter by specific user type",
      required: !1,
      options: {
        options: [
          { label: "EPV User", value: "EPVUser" },
          { label: "Built-in Admins", value: "Built-InAdmins" },
          { label: "LDAP User", value: "LDAP" },
          { label: "CPM", value: "CPM" },
          { label: "PVWA", value: "PVWA" },
          { label: "PSM", value: "PSM" },
          { label: "App Provider", value: "AppProvider" },
          { label: "Other (specify in Filter)", value: "" }
        ]
      }
    })
  },
  async run(context) {
    let authData = await getAuthToken(context.auth), queryParams = new URLSearchParams();
    context.propsValue.filter && queryParams.append("filter", context.propsValue.filter), context.propsValue.search && queryParams.append("search", context.propsValue.search), context.propsValue.sort && queryParams.append("sort", context.propsValue.sort), context.propsValue.extendedDetails && queryParams.append("ExtendedDetails", "true"), context.propsValue.pageOffset !== void 0 && context.propsValue.pageOffset > 0 && queryParams.append("pageOffset", context.propsValue.pageOffset.toString()), context.propsValue.pageSize !== void 0 && context.propsValue.pageSize > 0 && queryParams.append("pageSize", context.propsValue.pageSize.toString()), context.propsValue.componentUser !== void 0 && queryParams.append("componentUser", context.propsValue.componentUser.toString()), context.propsValue.userType && context.propsValue.userType !== "" && queryParams.append("userType", context.propsValue.userType);
    let queryString = queryParams.toString(), url = queryString ? `${authData.serverUrl}/PasswordVault/API/Users?${queryString}` : `${authData.serverUrl}/PasswordVault/API/Users`;
    try {
      let response = await import_pieces_common9.httpClient.sendRequest({
        method: import_pieces_common9.HttpMethod.GET,
        url,
        headers: {
          "Content-Type": "application/json",
          Authorization: authData.token
        }
      });
      return response.status === 200 ? {
        success: !0,
        users: response.body.Users || [],
        total: response.body.Total || 0,
        queryUsed: {
          url,
          parameters: Object.fromEntries(queryParams.entries())
        }
      } : {
        success: !1,
        error: `Failed to find users. Status: ${response.status}`,
        details: response.body
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to find users",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/cyberark/src/lib/actions/add-member-to-group.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var import_pieces_common12 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/cyberark/src/lib/common/group-dropdown.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var groupIdDropdown = import_pieces_framework10.Property.Dropdown({
  auth: cyberarkAuth,
  displayName: "Group",
  description: "Select a group from the Vault",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please authenticate first",
        options: []
      };
    try {
      let authData = await getAuthToken(auth), response = await import_pieces_common10.httpClient.sendRequest({
        method: import_pieces_common10.HttpMethod.GET,
        url: `${authData.serverUrl}/PasswordVault/API/UserGroups`,
        headers: {
          "Content-Type": "application/json",
          Authorization: authData.token
        }
      });
      return response.status === 200 && ((_a = response.body) != null && _a.value) ? {
        disabled: !1,
        options: response.body.value.map((group) => ({
          label: `${group.groupName} (ID: ${group.id}) - ${group.groupType}`,
          value: group.id.toString()
        }))
      } : {
        disabled: !0,
        placeholder: "Failed to load groups",
        options: []
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Error loading groups",
        options: []
      };
    }
  }
});

// ../ap/packages/pieces/community/cyberark/src/lib/common/member-dropdown.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var memberIdDropdown = import_pieces_framework11.Property.Dropdown({
  displayName: "Member",
  description: "Select a Vault user or enter LDAP group name to add to the group",
  required: !0,
  refreshers: [],
  auth: cyberarkAuth,
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please authenticate first",
        options: []
      };
    try {
      let authData = await getAuthToken(auth), response = await import_pieces_common11.httpClient.sendRequest({
        method: import_pieces_common11.HttpMethod.GET,
        url: `${authData.serverUrl}/PasswordVault/API/Users?pageSize=1000`,
        headers: {
          "Content-Type": "application/json",
          Authorization: authData.token
        }
      });
      return response.status === 200 && ((_a = response.body) != null && _a.Users) ? {
        disabled: !1,
        options: response.body.Users.map((user) => ({
          label: `${user.username} (ID: ${user.id}) - ${user.userType}${user.componentUser ? " [Component]" : ""}${user.suspended ? " [Suspended]" : ""}${user.enabled ? "" : " [Disabled]"}`,
          value: user.username
        }))
      } : {
        disabled: !0,
        placeholder: "Failed to load users",
        options: []
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Error loading users",
        options: []
      };
    }
  }
});

// ../ap/packages/pieces/community/cyberark/src/lib/actions/add-member-to-group.ts
var addMemberToGroup = (0, import_pieces_framework12.createAction)({
  auth: cyberarkAuth,
  name: "add_member_to_group",
  displayName: "Add Member to Group",
  description: "Adds a user as a member to an existing Vault group (requires Add/Update users permissions)",
  audience: "both",
  aiMetadata: {
    description: "Adds a member to an existing Vault user group, where the member is either a Vault user or a domain user (domain mode requires the domain DNS name). Use to grant group-based permissions. Requires Add/Update Users permissions. Effectively idempotent on the membership keyed by group and member, though re-adding an existing member may surface an error.",
    idempotent: !0
  },
  props: {
    groupId: groupIdDropdown,
    memberId: memberIdDropdown,
    memberType: import_pieces_framework12.Property.StaticDropdown({
      displayName: "Member Type",
      description: "The type of user being added to the Vault group",
      required: !0,
      options: {
        options: [
          { label: "Vault User", value: "Vault" },
          { label: "Domain User", value: "domain" }
        ]
      },
      defaultValue: "Vault"
    }),
    domainName: import_pieces_framework12.Property.ShortText({
      displayName: "Domain Name",
      description: "The DNS address of the domain (required if memberType is domain)",
      required: !1
    })
  },
  async run(context) {
    let authData = await getAuthToken(context.auth);
    if (!context.propsValue.memberId)
      return {
        success: !1,
        error: "Member ID is required. Please select or enter a member."
      };
    if (context.propsValue.memberType === "domain" && !context.propsValue.domainName)
      return {
        success: !1,
        error: 'Domain name is required when member type is "domain"'
      };
    let requestBody = {
      memberId: context.propsValue.memberId,
      memberType: context.propsValue.memberType
    };
    context.propsValue.domainName && (requestBody.domainName = context.propsValue.domainName);
    try {
      let response = await import_pieces_common12.httpClient.sendRequest({
        method: import_pieces_common12.HttpMethod.POST,
        url: `${authData.serverUrl}/PasswordVault/API/UserGroups/${context.propsValue.groupId}/Members/`,
        headers: {
          "Content-Type": "application/json",
          Authorization: authData.token
        },
        body: requestBody
      });
      return response.status === 201 || response.status === 200 ? {
        success: !0,
        member: response.body,
        message: `Successfully added ${context.propsValue.memberId} to group ${context.propsValue.groupId}`
      } : {
        success: !1,
        error: `Failed to add member to group. Status: ${response.status}`,
        details: response.body
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to add member to group",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/cyberark/src/lib/actions/remove-member-from-group.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var import_pieces_common13 = require("@activepieces/pieces-common");
var removeMemberFromGroup = (0, import_pieces_framework13.createAction)({
  auth: cyberarkAuth,
  name: "remove_member_from_group",
  displayName: "Remove Member from Group",
  description: "Removes a specific user from a user group in the Vault",
  audience: "both",
  aiMetadata: {
    description: "Removes a member from a Vault user group, identified by group ID and member ID. Use to revoke a user's group-based permissions. Effectively idempotent: once removed, repeating the call has no further effect on the membership.",
    idempotent: !0
  },
  props: {
    groupId: groupIdDropdown,
    memberId: memberIdDropdown
  },
  async run(context) {
    let authData = await getAuthToken(context.auth);
    if (!context.propsValue.memberId)
      return {
        success: !1,
        error: "Member ID is required. Please select or enter a member."
      };
    try {
      let response = await import_pieces_common13.httpClient.sendRequest({
        method: import_pieces_common13.HttpMethod.DELETE,
        url: `${authData.serverUrl}/PasswordVault/API/UserGroups/${context.propsValue.groupId}/Members/${encodeURIComponent(String(context.propsValue.memberId))}/`,
        headers: {
          "Content-Type": "application/json",
          Authorization: authData.token
        }
      });
      return response.status === 204 || response.status === 200 ? {
        success: !0,
        message: `Successfully removed ${context.propsValue.memberId} from group ${context.propsValue.groupId}`
      } : {
        success: !1,
        error: `Failed to remove member from group. Status: ${response.status}`,
        details: response.body
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to remove member from group",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/cyberark/src/lib/actions/get-password-value.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var import_pieces_common15 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/cyberark/src/lib/common/account-dropdown.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_common14 = require("@activepieces/pieces-common");
var accountIdDropdown = import_pieces_framework14.Property.Dropdown({
  auth: cyberarkAuth,
  displayName: "Account",
  description: "Select an account from the Vault",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please authenticate first",
        options: []
      };
    try {
      let authData = await getAuthToken(auth), response = await import_pieces_common14.httpClient.sendRequest({
        method: import_pieces_common14.HttpMethod.GET,
        url: `${authData.serverUrl}/PasswordVault/API/Accounts?limit=1000`,
        headers: {
          "Content-Type": "application/json",
          Authorization: authData.token
        }
      });
      return response.status === 200 && ((_a = response.body) != null && _a.value) ? {
        disabled: !1,
        options: response.body.value.map((account) => {
          var _a2, _b, _c, _d, _e;
          return {
            label: `${(_b = (_a2 = account.userName) != null ? _a2 : account.name) != null ? _b : account.id}@${(_c = account.address) != null ? _c : "N/A"} (${(_d = account.platformId) != null ? _d : "N/A"} - ${(_e = account.safeName) != null ? _e : "N/A"})`,
            value: account.id.toString()
          };
        })
      } : {
        disabled: !0,
        placeholder: "Failed to load accounts",
        options: []
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Error loading accounts",
        options: []
      };
    }
  }
});

// ../ap/packages/pieces/community/cyberark/src/lib/actions/get-password-value.ts
var getPasswordValue = (0, import_pieces_framework15.createAction)({
  auth: cyberarkAuth,
  name: "get_password_value",
  displayName: "Get Password Value",
  description: "Retrieves the password or SSH key of an existing account identified by its Account ID",
  audience: "both",
  aiMetadata: {
    description: "Retrieves the secret (password or SSH key) of a stored CyberArk account by its account ID, optionally for a specific version and with an audit reason/ticket reference. Use to fetch a credential for downstream use; the retrieval itself does not change the stored secret, so repeating it returns the same value (it is logged for audit each time).",
    idempotent: !0
  },
  props: {
    accountId: accountIdDropdown,
    reason: import_pieces_framework15.Property.ShortText({
      displayName: "Reason",
      description: "The reason for retrieving the password/SSH key",
      required: !1
    }),
    ticketingSystemName: import_pieces_framework15.Property.ShortText({
      displayName: "Ticketing System Name",
      description: "The name of the Ticketing System",
      required: !1
    }),
    ticketId: import_pieces_framework15.Property.ShortText({
      displayName: "Ticket ID",
      description: "The ticket ID of the ticketing system",
      required: !1
    }),
    version: import_pieces_framework15.Property.Number({
      displayName: "Version",
      description: "The version number of the required password. If there are no previous versions, the current password/key version is returned.",
      required: !1
    }),
    actionType: import_pieces_framework15.Property.StaticDropdown({
      displayName: "Action Type",
      description: "The action this password will be used for",
      required: !1,
      options: {
        options: [
          { label: "Show", value: "show" },
          { label: "Copy", value: "copy" },
          { label: "Connect", value: "connect" }
        ]
      }
    }),
    isUse: import_pieces_framework15.Property.Checkbox({
      displayName: "Is Use",
      description: "Internal parameter (for PSM for SSH only)",
      required: !1,
      defaultValue: !1
    }),
    machine: import_pieces_framework15.Property.ShortText({
      displayName: "Machine",
      description: "The address of the remote machine to connect to",
      required: !1
    })
  },
  async run(context) {
    let authData = await getAuthToken(context.auth), requestBody = __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({}, context.propsValue.reason && { reason: context.propsValue.reason }), context.propsValue.ticketingSystemName && { TicketingSystemName: context.propsValue.ticketingSystemName }), context.propsValue.ticketId && { TicketId: context.propsValue.ticketId }), context.propsValue.version != null && { Version: context.propsValue.version }), context.propsValue.actionType && { ActionType: context.propsValue.actionType }), context.propsValue.isUse != null && { isUse: context.propsValue.isUse }), context.propsValue.machine && { Machine: context.propsValue.machine });
    try {
      let response = await import_pieces_common15.httpClient.sendRequest({
        method: import_pieces_common15.HttpMethod.POST,
        url: `${authData.serverUrl}/PasswordVault/API/Accounts/${context.propsValue.accountId}/Password/Retrieve/`,
        headers: {
          "Content-Type": "application/json",
          Authorization: authData.token
        },
        body: requestBody
      });
      return response.status === 200 ? {
        success: !0,
        password: response.body
      } : {
        success: !1,
        error: `Failed to retrieve password. Status: ${response.status}`,
        details: response.body
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to retrieve password",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/cyberark/src/lib/actions/retrieve-private-ssh-key.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var import_pieces_common16 = require("@activepieces/pieces-common");
var retrievePrivateSSHKey = (0, import_pieces_framework16.createAction)({
  auth: cyberarkAuth,
  name: "retrieve_private_ssh_key",
  displayName: "Retrieve Private SSH Key",
  description: "Retrieves a private SSH key file from an existing account identified by its Account ID",
  audience: "both",
  aiMetadata: {
    description: "Retrieves the private SSH key secret of a stored CyberArk account by its account ID, optionally for a specific version and with an audit reason/ticket reference. Use to fetch an SSH private key for downstream use; the retrieval does not alter the stored key, so repeating it returns the same value (logged for audit each time).",
    idempotent: !0
  },
  props: {
    accountId: accountIdDropdown,
    reason: import_pieces_framework16.Property.ShortText({
      displayName: "Reason",
      description: "The reason for retrieving the private SSH key",
      required: !1
    }),
    ticketingSystemName: import_pieces_framework16.Property.ShortText({
      displayName: "Ticketing System Name",
      description: "The name of the ticketing system",
      required: !1
    }),
    ticketId: import_pieces_framework16.Property.ShortText({
      displayName: "Ticket ID",
      description: "The ticket ID defined in the ticketing system",
      required: !1
    }),
    version: import_pieces_framework16.Property.Number({
      displayName: "Version",
      description: "The version number of the required SSH key. Must be a positive number. If left empty or the value does not exist, the current SSH key version is returned.",
      required: !1
    }),
    actionType: import_pieces_framework16.Property.StaticDropdown({
      displayName: "Action Type",
      description: "The action this SSH key is used for",
      required: !1,
      options: {
        options: [
          { label: "Download", value: "download" }
        ]
      }
    }),
    isUse: import_pieces_framework16.Property.Checkbox({
      displayName: "Is Use",
      description: "Internal parameter (for use of PSMP only)",
      required: !1,
      defaultValue: !1
    }),
    machine: import_pieces_framework16.Property.ShortText({
      displayName: "Machine",
      description: "The address of the remote machine to connect to using the SSH key",
      required: !1
    })
  },
  async run(context) {
    let authData = await getAuthToken(context.auth), requestBody = __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({}, context.propsValue.reason && { reason: context.propsValue.reason }), context.propsValue.ticketingSystemName && { TicketingSystemName: context.propsValue.ticketingSystemName }), context.propsValue.ticketId && { TicketId: context.propsValue.ticketId }), context.propsValue.version != null && { Version: context.propsValue.version }), context.propsValue.actionType && { ActionType: context.propsValue.actionType }), context.propsValue.isUse != null && { isUse: context.propsValue.isUse }), context.propsValue.machine && { Machine: context.propsValue.machine });
    try {
      let response = await import_pieces_common16.httpClient.sendRequest({
        method: import_pieces_common16.HttpMethod.POST,
        url: `${authData.serverUrl}/PasswordVault/API/Accounts/${context.propsValue.accountId}/Secret/Retrieve/`,
        headers: {
          "Content-Type": "application/json",
          Authorization: authData.token
        },
        body: requestBody
      });
      return response.status === 200 ? {
        success: !0,
        sshKey: response.body
      } : {
        success: !1,
        error: `Failed to retrieve SSH key. Status: ${response.status}`,
        details: response.body
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to retrieve SSH key",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/cyberark/src/lib/actions/change-credentials-in-vault.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework");
var import_pieces_common17 = require("@activepieces/pieces-common");
var changeCredentialsInVault = (0, import_pieces_framework17.createAction)({
  auth: cyberarkAuth,
  name: "change_credentials_in_vault",
  displayName: "Change Credentials in the Vault",
  description: "Sets account credentials and changes them in the Vault. This will not affect credentials on the target device.",
  audience: "both",
  aiMetadata: {
    description: "Sets a new credential value for a stored CyberArk account (by account ID) and updates it in the Vault only \u2014 it does NOT change the credential on the target device, so the Vault may end up out of sync with the managed system. Use when you need to record a known credential in the Vault. Not idempotent: each call writes a new Vault password version.",
    idempotent: !1
  },
  props: {
    accountId: accountIdDropdown,
    newCredentials: import_pieces_framework17.Property.ShortText({
      displayName: "New Credentials",
      description: "The new account credentials that will be allocated to the account in the Vault. Leading and trailing white spaces will be automatically removed.",
      required: !0
    })
  },
  async run(context) {
    let authData = await getAuthToken(context.auth);
    try {
      let response = await import_pieces_common17.httpClient.sendRequest({
        method: import_pieces_common17.HttpMethod.POST,
        url: `${authData.serverUrl}/PasswordVault/API/Accounts/${context.propsValue.accountId}/Password/Update/`,
        headers: {
          "Content-Type": "application/json",
          Authorization: authData.token
        },
        body: {
          NewCredentials: context.propsValue.newCredentials
        }
      });
      return response.status === 200 ? {
        success: !0,
        details: response.body
      } : {
        success: !1,
        error: `Failed to change credentials. Status: ${response.status}`,
        details: response.body
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to change credentials",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/cyberark/src/lib/actions/verify-credentials-bulk.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework");
var import_pieces_common18 = require("@activepieces/pieces-common");
var verifyCredentialsBulk = (0, import_pieces_framework18.createAction)({
  auth: cyberarkAuth,
  name: "verify_credentials_bulk",
  displayName: "Verify Credentials in Bulk",
  description: "Marks multiple accounts for verification by the CPM",
  audience: "both",
  aiMetadata: {
    description: "Flags a list of CyberArk accounts (by account ID) for credential verification by the CPM, which checks that the stored credential still matches the target device. Use to trigger a verification sweep across many accounts at once. Effectively idempotent: it only marks the accounts for verification rather than mutating any credential.",
    idempotent: !0
  },
  props: {
    accountIds: import_pieces_framework18.Property.Array({
      displayName: "Account IDs",
      description: "List of unique account IDs to verify",
      required: !0
    })
  },
  async run(context) {
    let authData = await getAuthToken(context.auth), bulkItems = context.propsValue.accountIds.map(
      (accountId) => ({ accountId })
    );
    try {
      let response = await import_pieces_common18.httpClient.sendRequest({
        method: import_pieces_common18.HttpMethod.POST,
        url: `${authData.serverUrl}/PasswordVault/API/Accounts/Verify/Bulk`,
        headers: {
          "Content-Type": "application/json",
          Authorization: authData.token
        },
        body: {
          bulkItems
        }
      });
      return response.status === 200 ? {
        success: !0,
        details: response.body
      } : {
        success: !1,
        error: `Failed to verify credentials in bulk. Status: ${response.status}`,
        details: response.body
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to verify credentials in bulk",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/cyberark/src/lib/actions/change-credentials-bulk.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework");
var import_pieces_common19 = require("@activepieces/pieces-common");
var changeCredentialsBulk = (0, import_pieces_framework19.createAction)({
  auth: cyberarkAuth,
  name: "change_credentials_bulk",
  displayName: "Change Credentials Immediately in Bulk",
  description: "Marks multiple accounts for an immediate credentials change by the CPM to a new random value",
  audience: "both",
  aiMetadata: {
    description: "Marks a list of CyberArk accounts (by account ID) for an immediate CPM credential change to a new random value on both the Vault and the target device, optionally rotating every account in the same account group. Use to force a rotation across many accounts at once. Not idempotent: each call triggers another rotation to a fresh random secret.",
    idempotent: !1
  },
  props: {
    accountIds: import_pieces_framework19.Property.Array({
      displayName: "Account IDs",
      description: "List of unique account IDs to change credentials for",
      required: !0
    }),
    changeEntireGroup: import_pieces_framework19.Property.Checkbox({
      displayName: "Change Entire Group",
      description: "Whether the CPM changes the credentials for all accounts in the same account group. Only applies to accounts that belong to an account group.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    var _a;
    let authData = await getAuthToken(context.auth), changeEntireGroup = (_a = context.propsValue.changeEntireGroup) != null ? _a : !1, bulkItems = context.propsValue.accountIds.map(
      (accountId) => ({
        accountId,
        ChangeEntireGroup: changeEntireGroup
      })
    );
    try {
      let response = await import_pieces_common19.httpClient.sendRequest({
        method: import_pieces_common19.HttpMethod.POST,
        url: `${authData.serverUrl}/PasswordVault/API/Accounts/Change/Bulk`,
        headers: {
          "Content-Type": "application/json",
          Authorization: authData.token
        },
        body: {
          bulkItems
        }
      });
      return response.status === 200 ? {
        success: !0,
        details: response.body
      } : {
        success: !1,
        error: `Failed to change credentials in bulk. Status: ${response.status}`,
        details: response.body
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to change credentials in bulk",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/cyberark/src/lib/actions/set-next-password-bulk.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework");
var import_pieces_common20 = require("@activepieces/pieces-common");
var setNextPasswordBulk = (0, import_pieces_framework20.createAction)({
  auth: cyberarkAuth,
  name: "set_next_password_bulk",
  displayName: "Set Next Password in Bulk",
  description: "Sets multiple accounts' credentials to use for the next CPM change",
  audience: "both",
  aiMetadata: {
    description: "Sets the specific credential value the CPM will apply on the next change for a list of CyberArk accounts (each item carries an account ID, an optional changeImmediately flag, and the new credentials). Use to pre-stage a known password for accounts rather than letting the CPM pick a random one. Not idempotent: each call writes a new pending/applied credential and, when changeImmediately is set, triggers a change.",
    idempotent: !1
  },
  props: {
    bulkItems: import_pieces_framework20.Property.Array({
      displayName: "Bulk Items",
      description: "List of account items. Each item should be a JSON object with accountId (required), changeImmediately (optional), and newCredentials (optional).",
      required: !0
    })
  },
  async run(context) {
    let authData = await getAuthToken(context.auth), bulkItems = context.propsValue.bulkItems.map((item) => typeof item == "string" ? JSON.parse(item) : item);
    try {
      let response = await import_pieces_common20.httpClient.sendRequest({
        method: import_pieces_common20.HttpMethod.POST,
        url: `${authData.serverUrl}/PasswordVault/API/Accounts/SetNextPassword/Bulk`,
        headers: {
          "Content-Type": "application/json",
          Authorization: authData.token
        },
        body: {
          bulkItems
        }
      });
      return response.status === 200 ? {
        success: !0,
        details: response.body
      } : {
        success: !1,
        error: `Failed to set next password in bulk. Status: ${response.status}`,
        details: response.body
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to set next password in bulk",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/cyberark/src/lib/actions/change-credentials-in-vault-bulk.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework");
var import_pieces_common21 = require("@activepieces/pieces-common");
var changeCredentialsInVaultBulk = (0, import_pieces_framework21.createAction)({
  auth: cyberarkAuth,
  name: "change_credentials_in_vault_bulk",
  displayName: "Change Credentials in the Vault in Bulk",
  description: "Sets credentials for multiple accounts and changes them in the Vault. This does not affect credentials on the target device.",
  audience: "both",
  aiMetadata: {
    description: "Sets new credential values for a list of CyberArk accounts (each item carries an account ID and optional new credentials) and updates them in the Vault only \u2014 it does NOT change the credentials on the target devices, so the Vault may end up out of sync. Use to record known credentials for many accounts at once. Not idempotent: each call writes a new Vault password version.",
    idempotent: !1
  },
  props: {
    bulkItems: import_pieces_framework21.Property.Array({
      displayName: "Bulk Items",
      description: "List of account items. Each item should be a JSON object with accountId (required) and newCredentials (optional).",
      required: !0
    })
  },
  async run(context) {
    let authData = await getAuthToken(context.auth), bulkItems = context.propsValue.bulkItems.map((item) => typeof item == "string" ? JSON.parse(item) : item);
    try {
      let response = await import_pieces_common21.httpClient.sendRequest({
        method: import_pieces_common21.HttpMethod.POST,
        url: `${authData.serverUrl}/PasswordVault/API/Accounts/Password/Update/Bulk`,
        headers: {
          "Content-Type": "application/json",
          Authorization: authData.token
        },
        body: {
          bulkItems
        }
      });
      return response.status === 200 ? {
        success: !0,
        details: response.body
      } : {
        success: !1,
        error: `Failed to change credentials in vault in bulk. Status: ${response.status}`,
        details: response.body
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to change credentials in vault in bulk",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/cyberark/src/lib/actions/reconcile-credentials-bulk.ts
var import_pieces_framework22 = require("@activepieces/pieces-framework");
var import_pieces_common22 = require("@activepieces/pieces-common");
var reconcileCredentialsBulk = (0, import_pieces_framework22.createAction)({
  auth: cyberarkAuth,
  name: "reconcile_credentials_bulk",
  displayName: "Reconcile Credentials in Bulk",
  description: "Marks multiple accounts for automatic reconciliation by the CPM",
  audience: "both",
  aiMetadata: {
    description: "Flags a list of CyberArk accounts (by account ID) for automatic reconciliation by the CPM, which resets the credential on both the Vault and the target device using the reconciliation account. Use to recover accounts whose stored and device credentials have diverged. Effectively idempotent: it only marks the accounts for reconciliation rather than mutating a credential per call.",
    idempotent: !0
  },
  props: {
    accountIds: import_pieces_framework22.Property.Array({
      displayName: "Account IDs",
      description: "List of unique account IDs to reconcile",
      required: !0
    })
  },
  async run(context) {
    let authData = await getAuthToken(context.auth), bulkItems = context.propsValue.accountIds.map(
      (accountId) => ({ accountId })
    );
    try {
      let response = await import_pieces_common22.httpClient.sendRequest({
        method: import_pieces_common22.HttpMethod.POST,
        url: `${authData.serverUrl}/PasswordVault/API/Accounts/Reconcile/Bulk`,
        headers: {
          "Content-Type": "application/json",
          Authorization: authData.token
        },
        body: {
          bulkItems
        }
      });
      return response.status === 200 ? {
        success: !0,
        details: response.body
      } : {
        success: !1,
        error: `Failed to reconcile credentials in bulk. Status: ${response.status}`,
        details: response.body
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to reconcile credentials in bulk",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/cyberark/src/index.ts
var cyberark = (0, import_pieces_framework23.createPiece)({
  displayName: "CyberArk",
  description: "Manage users, groups, and access controls in CyberArk Privileged Access Management",
  auth: cyberarkAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/cyberark.png",
  authors: ["fortunamide", "onyedikachi-david"],
  categories: [import_pieces_framework24.PieceCategory.PRODUCTIVITY],
  actions: [
    createUser,
    updateUser,
    deleteUser,
    activateUser,
    enableUser,
    disableUser,
    findUser,
    addMemberToGroup,
    removeMemberFromGroup,
    getPasswordValue,
    retrievePrivateSSHKey,
    changeCredentialsInVault,
    verifyCredentialsBulk,
    changeCredentialsBulk,
    setNextPasswordBulk,
    changeCredentialsInVaultBulk,
    reconcileCredentialsBulk
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  cyberark
});
