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

// ../ap/packages/pieces/community/okta/src/index.ts
var index_exports = {};
__export(index_exports, {
  okta: () => okta
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_framework15 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/okta/src/lib/common/common.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework"), oktaAuth = import_pieces_framework.PieceAuth.CustomAuth({
  required: !0,
  props: {
    domain: import_pieces_framework2.Property.ShortText({
      displayName: "Okta Domain",
      description: "Your Okta organization domain (e.g., https://dev-12345.okta.com or dev-12345.okta.com)",
      required: !0
    }),
    apiToken: import_pieces_framework2.Property.ShortText({
      displayName: "API Token",
      description: "Your Okta API token (from Admin \u2192 Security \u2192 API \u2192 Tokens)",
      required: !0
    })
  }
});
async function makeOktaRequest(auth, endpoint, method = import_pieces_common.HttpMethod.GET, body) {
  let apiToken = auth.apiToken, domain = auth.domain;
  if (!domain)
    throw new Error("Okta domain is required");
  return !domain.startsWith("https://") && !domain.startsWith("http://") && (domain = `https://${domain}`), domain = domain.replace(/\/$/, ""), await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${domain}/api/v1${endpoint}`,
    headers: {
      Authorization: `SSWS ${apiToken}`,
      "Content-Type": "application/json",
      Accept: "application/json"
    },
    body
  });
}
var userIdDropdown = (groupusers = !1) => import_pieces_framework2.Property.Dropdown({
  auth: oktaAuth,
  displayName: "User",
  description: "Select a user",
  required: !0,
  refreshers: ["auth", "groupId"],
  options: async ({ auth, groupId }) => {
    try {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "select auth first"
        };
      if (groupusers && !groupId)
        return {
          disabled: !0,
          options: [],
          placeholder: "please select groupId first"
        };
      let path = groupusers ? `/groups/${groupId}/users` : "/users", users = await (await makeOktaRequest(
        auth,
        path,
        import_pieces_common.HttpMethod.GET,
        void 0
      )).body;
      return console.log(JSON.stringify(users, null, 2)), Array.isArray(users) ? {
        disabled: !1,
        options: users.map((user) => ({
          label: user.profile.firstName + " " + user.profile.lastName + " (" + user.profile.email + ")",
          value: user.id
        }))
      } : {
        disabled: !0,
        options: [],
        placeholder: "No users found"
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Error fetching users"
      };
    }
  }
}), groupIdDropdown = import_pieces_framework2.Property.Dropdown({
  auth: oktaAuth,
  displayName: "Group",
  description: "Select a group",
  required: !0,
  refreshers: ["auth"],
  options: async ({ auth }) => {
    try {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "No groups found"
        };
      let groups = await (await makeOktaRequest(
        auth,
        "/groups",
        import_pieces_common.HttpMethod.GET
      )).body;
      return Array.isArray(groups) ? {
        disabled: !1,
        options: groups.map((group) => ({
          label: group.profile.name,
          value: group.id
        }))
      } : {
        disabled: !0,
        options: [],
        placeholder: "No groups found"
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Error fetching groups"
      };
    }
  }
});

// ../ap/packages/pieces/community/okta/src/lib/actions/create-user.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common"), createUserAction = (0, import_pieces_framework3.createAction)({
  auth: oktaAuth,
  name: "create_user",
  displayName: "Create User",
  description: "Creates a user without credentials and sends account creation prompt via email",
  audience: "both",
  aiMetadata: { description: "Creates a new user account in Okta from a first name, last name, and email (login defaults to the email). Use to provision a person into the org; optionally activates and sends the account-setup email when Send Email is enabled. Not idempotent \u2014 each call creates another user, so guard against duplicates by checking for an existing account first.", idempotent: !1 },
  props: {
    firstName: import_pieces_framework3.Property.ShortText({
      displayName: "First Name",
      description: "User first name",
      required: !0
    }),
    lastName: import_pieces_framework3.Property.ShortText({
      displayName: "Last Name",
      description: "User last name",
      required: !0
    }),
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email",
      description: "User email address",
      required: !0
    }),
    login: import_pieces_framework3.Property.ShortText({
      displayName: "Login",
      description: "User login (typically same as email)",
      required: !1
    }),
    mobilePhone: import_pieces_framework3.Property.ShortText({
      displayName: "Mobile Phone",
      description: "User mobile phone number",
      required: !1
    }),
    sendEmail: import_pieces_framework3.Property.Checkbox({
      displayName: "Send Email",
      description: "Send account creation email to user",
      required: !1,
      defaultValue: !0
    })
  },
  async run(context) {
    let userData = {
      profile: {
        firstName: context.propsValue.firstName,
        lastName: context.propsValue.lastName,
        email: context.propsValue.email,
        login: context.propsValue.login || context.propsValue.email
      }
    }, queryParam = context.propsValue.sendEmail ? "?activate=true" : "";
    return (await makeOktaRequest(
      context.auth,
      `/users${queryParam}`,
      import_pieces_common2.HttpMethod.POST,
      userData
    )).body;
  }
});

// ../ap/packages/pieces/community/okta/src/lib/actions/activate-user.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), activateUserAction = (0, import_pieces_framework4.createAction)({
  auth: oktaAuth,
  name: "activate_user",
  displayName: "Activate User",
  description: "Activate a previously deactivated or pending user",
  audience: "both",
  aiMetadata: { description: "Transitions an existing Okta user (identified by user ID) to the ACTIVE lifecycle state, optionally emailing them an activation link. Use to re-enable a deactivated or pending account. Idempotent on end-state \u2014 re-running leaves the user active, though it may re-send the activation email if that option is on.", idempotent: !0 },
  props: {
    userId: userIdDropdown(),
    sendEmail: import_pieces_framework4.Property.Checkbox({
      displayName: "Send Email",
      description: "Send activation email to user",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let userId = context.propsValue.userId, sendEmail = context.propsValue.sendEmail ? "true" : "false";
    return (await makeOktaRequest(
      context.auth,
      `/users/${userId}/lifecycle/activate?sendEmail=${sendEmail}`,
      import_pieces_common3.HttpMethod.POST
    )).body;
  }
});

// ../ap/packages/pieces/community/okta/src/lib/actions/deactivate-user.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common"), deactivateUserAction = (0, import_pieces_framework5.createAction)({
  auth: oktaAuth,
  name: "deactivate_user",
  displayName: "Deactivate User",
  description: "Deactivate (disable) a user in Okta",
  audience: "both",
  aiMetadata: { description: "Transitions an existing Okta user (by user ID) to the DEACTIVATED lifecycle state, disabling their access while preserving the account. Use to offboard or temporarily disable someone. Idempotent on end-state \u2014 re-running leaves the user deactivated, though it may re-send the notification email if that option is on.", idempotent: !0 },
  props: {
    userId: userIdDropdown(),
    sendEmail: import_pieces_framework5.Property.Checkbox({
      displayName: "Send Email",
      description: "Send deactivation email to user",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let userId = context.propsValue.userId, sendEmail = context.propsValue.sendEmail ? "true" : "false";
    return (await makeOktaRequest(
      context.auth,
      `/users/${userId}/lifecycle/deactivate?sendEmail=${sendEmail}`,
      import_pieces_common4.HttpMethod.POST
    )).body;
  }
});

// ../ap/packages/pieces/community/okta/src/lib/actions/suspend-user.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common"), suspendUserAction = (0, import_pieces_framework6.createAction)({
  auth: oktaAuth,
  name: "suspend_user",
  displayName: "Suspend User",
  description: "Temporarily suspend a user in Okta",
  audience: "both",
  aiMetadata: { description: "Transitions an existing Okta user (by user ID) to the SUSPENDED lifecycle state, temporarily blocking sign-in without deactivating the account. Use for a reversible hold; reverse it with the unsuspend/activate flow. Idempotent on end-state \u2014 re-running leaves the user suspended.", idempotent: !0 },
  props: {
    userId: userIdDropdown()
  },
  async run(context) {
    let userId = context.propsValue.userId;
    return (await makeOktaRequest(
      context.auth,
      `/users/${userId}/lifecycle/suspend`,
      import_pieces_common5.HttpMethod.POST
    )).body;
  }
});

// ../ap/packages/pieces/community/okta/src/lib/actions/add-user-to-group.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common"), addUserToGroupAction = (0, import_pieces_framework7.createAction)({
  auth: oktaAuth,
  name: "add_user_to_group",
  displayName: "Add User to Group",
  description: "Add a user to a specific Okta group",
  audience: "both",
  aiMetadata: { description: "Adds an existing user to a specific Okta group, identified by user ID and group ID. Use to grant group-based access or roles. Idempotent \u2014 adding a user who is already a member leaves membership unchanged.", idempotent: !0 },
  props: {
    userId: userIdDropdown(),
    groupId: groupIdDropdown
  },
  async run(context) {
    let userId = context.propsValue.userId, groupId = context.propsValue.groupId, response = await makeOktaRequest(
      context.auth,
      `/groups/${groupId}/users/${userId}`,
      import_pieces_common6.HttpMethod.PUT
    );
    return {
      success: !0,
      userId,
      groupId,
      message: "User added to group"
    };
  }
});

// ../ap/packages/pieces/community/okta/src/lib/actions/remove-user-from-group.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common"), removeUserFromGroupAction = (0, import_pieces_framework8.createAction)({
  auth: oktaAuth,
  name: "remove_user_from_group",
  displayName: "Remove User from Group",
  description: "Remove a user from an Okta group",
  audience: "both",
  aiMetadata: { description: "Removes a user from a specific Okta group, identified by group ID and user ID. Use to revoke group-based access or roles. Idempotent \u2014 removing a user who is not a member leaves membership unchanged.", idempotent: !0 },
  props: {
    groupId: groupIdDropdown,
    userId: userIdDropdown(!0)
  },
  async run(context) {
    let userId = context.propsValue.userId, groupId = context.propsValue.groupId, response = await makeOktaRequest(
      context.auth,
      `/groups/${groupId}/users/${userId}`,
      import_pieces_common7.HttpMethod.DELETE
    );
    return {
      success: !0,
      userId,
      groupId,
      message: "User removed from group"
    };
  }
});

// ../ap/packages/pieces/community/okta/src/lib/actions/update-user.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var import_pieces_common8 = require("@activepieces/pieces-common");
var updateUserAction = (0, import_pieces_framework9.createAction)({
  auth: oktaAuth,
  name: "update_user",
  displayName: "Update User",
  description: "Update user profile information",
  audience: "both",
  aiMetadata: { description: "Updates an existing Okta user profile (by user ID) with any supplied fields \u2014 first/last name, email, mobile phone, plus arbitrary custom attributes via a JSON object; only provided fields are sent. Use to edit a known account. Idempotent \u2014 applying the same values again yields the same profile state.", idempotent: !0 },
  props: {
    userId: userIdDropdown(),
    firstName: import_pieces_framework9.Property.ShortText({
      displayName: "First Name",
      description: "Updated first name",
      required: !1
    }),
    lastName: import_pieces_framework9.Property.ShortText({
      displayName: "Last Name",
      description: "Updated last name",
      required: !1
    }),
    email: import_pieces_framework9.Property.ShortText({
      displayName: "Email",
      description: "Updated email address",
      required: !1
    }),
    mobilePhone: import_pieces_framework9.Property.ShortText({
      displayName: "Mobile Phone",
      description: "Updated mobile phone number",
      required: !1
    }),
    customAttributes: import_pieces_framework9.Property.Json({
      displayName: "Custom Attributes",
      description: "JSON object with custom profile attributes",
      required: !1
    })
  },
  async run(context) {
    let userId = context.propsValue.userId, userData = {
      profile: {}
    };
    return context.propsValue.firstName && (userData.profile.firstName = context.propsValue.firstName), context.propsValue.lastName && (userData.profile.lastName = context.propsValue.lastName), context.propsValue.email && (userData.profile.email = context.propsValue.email), context.propsValue.mobilePhone && (userData.profile.mobilePhone = context.propsValue.mobilePhone), context.propsValue.customAttributes && (userData.profile = __spreadValues(__spreadValues({}, userData.profile), context.propsValue.customAttributes)), (await makeOktaRequest(
      context.auth,
      `/users/${userId}`,
      import_pieces_common8.HttpMethod.POST,
      userData
    )).body;
  }
});

// ../ap/packages/pieces/community/okta/src/lib/actions/find-user-by-email.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var import_pieces_common9 = require("@activepieces/pieces-common"), findUserByEmailAction = (0, import_pieces_framework10.createAction)({
  auth: oktaAuth,
  name: "find_user_by_email",
  displayName: "Find User by Email",
  description: "Look up an Okta user by their email address",
  audience: "both",
  aiMetadata: { description: "Searches Okta for a user by exact email address and returns the first match. Use as a read-only lookup to resolve an email to an Okta user (e.g. to obtain a user ID before a lifecycle or group action). Idempotent \u2014 returns a not-found indicator when no user matches.", idempotent: !0 },
  props: {
    domain: import_pieces_framework10.Property.ShortText({
      displayName: "Okta Domain",
      description: "Your Okta organization domain",
      required: !0
    }),
    email: import_pieces_framework10.Property.ShortText({
      displayName: "Email",
      description: "The user email address",
      required: !0
    })
  },
  async run(context) {
    let email = context.propsValue.email, response = await makeOktaRequest(
      context.auth,
      `/users?search=profile.email eq "${email}"`,
      import_pieces_common9.HttpMethod.GET,
      context.propsValue.domain
    );
    return response.body && response.body.length > 0 ? response.body[0] : {
      success: !1,
      message: `No user found with email: ${email}`,
      data: []
    };
  }
});

// ../ap/packages/pieces/community/okta/src/lib/actions/find-group-by-name.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var findGroupByNameAction = (0, import_pieces_framework11.createAction)({
  auth: oktaAuth,
  name: "find_group_by_name",
  displayName: "Find Group by Name",
  description: "Search for an Okta group by name",
  audience: "both",
  aiMetadata: { description: "Searches Okta groups by name (substring query) and returns matching groups. Use as a read-only lookup to resolve a group name to its ID before a membership action. Idempotent.", idempotent: !0 },
  props: {
    groupName: import_pieces_framework11.Property.ShortText({
      displayName: "Group Name",
      description: "The group name to search for",
      required: !0
    })
  },
  async run(context) {
    let groupName = context.propsValue.groupName;
    return (await makeOktaRequest(
      context.auth,
      `/groups?q=${encodeURIComponent(groupName)}`
    )).body;
  }
});

// ../ap/packages/pieces/community/okta/src/lib/triggers/new-event.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var import_pieces_common10 = require("@activepieces/pieces-common"), import_pieces_framework13 = require("@activepieces/pieces-framework"), newEventTrigger = (0, import_pieces_framework12.createTrigger)({
  auth: oktaAuth,
  name: "new_event",
  displayName: "New Event",
  description: "Fires when a new Okta event is generated",
  aiMetadata: {
    description: "Fires via an Okta Event Hook when a system event occurs in the org, emitting one item per matching event. By default it covers user lifecycle changes (create/activate/deactivate/suspend/unsuspend/delete), group membership add/remove, group lifecycle, and session start/end; if specific Event Types are selected, only those fire. Use to react in real time to identity and access changes in Okta."
  },
  type: import_pieces_framework12.TriggerStrategy.WEBHOOK,
  props: {
    eventTypes: import_pieces_framework12.Property.StaticMultiSelectDropdown({
      displayName: "Event Types to Monitor",
      description: "Select which event types to trigger on",
      required: !1,
      options: {
        options: [
          { label: "User Created", value: "user.lifecycle.create" },
          { label: "User Activated", value: "user.lifecycle.activate" },
          { label: "User Deactivated", value: "user.lifecycle.deactivate" },
          { label: "User Suspended", value: "user.lifecycle.suspend" },
          { label: "User Unsuspended", value: "user.lifecycle.unsuspend" },
          { label: "User Deleted", value: "user.lifecycle.delete.completed" },
          { label: "User Added to Group", value: "group.user_membership.add" },
          {
            label: "User Removed from Group",
            value: "group.user_membership.remove"
          },
          { label: "Group Created", value: "group.lifecycle.create" },
          { label: "Group Deleted", value: "group.lifecycle.delete" },
          { label: "User Login", value: "user.session.start" },
          { label: "User Logout", value: "user.session.end" }
        ]
      }
    }),
    hookName: import_pieces_framework12.Property.ShortText({
      displayName: "Hook Name",
      description: "Name for the Okta Event Hook (optional)",
      required: !1,
      defaultValue: "Activepieces Webhook"
    })
  },
  handshakeConfiguration: {
    strategy: import_pieces_framework13.WebhookHandshakeStrategy.HEADER_PRESENT,
    paramName: "x-okta-verification-challenge"
  },
  async onHandshake(context) {
    let challengeValue = context.payload.headers["x-okta-verification-challenge"];
    return challengeValue ? (console.log("Okta verification challenge received:", challengeValue), {
      status: 200,
      body: {
        verification: challengeValue
      },
      headers: {
        "Content-Type": "application/json"
      }
    }) : {
      status: 400,
      body: { error: "No verification challenge found" }
    };
  },
  async onEnable(context) {
    var _a, _b;
    try {
      let eventTypes = context.propsValue.eventTypes || [], hookName = context.propsValue.hookName || "Webhook", existingHook = (_a = (await makeOktaRequest(
        context.auth,
        "/eventHooks",
        import_pieces_common10.HttpMethod.GET
      )).body) == null ? void 0 : _a.find(
        (hook) => {
          var _a2, _b2;
          return hook.name === hookName && ((_b2 = (_a2 = hook.channel) == null ? void 0 : _a2.config) == null ? void 0 : _b2.uri) === context.webhookUrl;
        }
      ), hookId;
      if (existingHook)
        hookId = existingHook.id;
      else {
        let eventHookPayload = {
          name: hookName,
          events: {
            type: "EVENT_TYPE",
            items: eventTypes.length > 0 ? eventTypes : [
              "user.lifecycle.create",
              "user.lifecycle.activate",
              "user.lifecycle.deactivate",
              "user.lifecycle.suspend",
              "user.lifecycle.unsuspend",
              "user.lifecycle.update",
              "user.lifecycle.delete",
              "group.user_membership.add",
              "group.user_membership.remove",
              "group.lifecycle.create",
              "group.lifecycle.update",
              "group.lifecycle.delete",
              "user.session.start",
              "user.session.end",
              "user.authentication.auth_failed"
            ]
          },
          channel: {
            type: "HTTP",
            version: "1.0.0",
            config: {
              uri: context.webhookUrl,
              method: "POST"
            }
          }
        }, response2 = await makeOktaRequest(
          context.auth,
          "/eventHooks",
          import_pieces_common10.HttpMethod.POST,
          eventHookPayload
        );
        if (!((_b = response2.body) != null && _b.id))
          throw new Error(
            "Failed to create event hook: " + JSON.stringify(response2.body)
          );
        hookId = response2.body.id, console.log(`Created new event hook: ${hookId}`);
      }
      let response = await makeOktaRequest(
        context.auth,
        `/eventHooks/${hookId}/lifecycle/verify`,
        import_pieces_common10.HttpMethod.POST
      );
      if (response.status !== 200)
        throw new Error(
          "Failed to verify event hook: " + JSON.stringify(response.body)
        );
    } catch (error) {
      throw console.error("Error creating Okta event hook:", error), new Error(`Failed to setup Okta event hook: ${error}`);
    }
  },
  async onDisable(context) {
    try {
      let hookId = await context.store.get("hookId");
      if (hookId) {
        try {
          await makeOktaRequest(
            context.auth,
            `/eventHooks/${hookId}/lifecycle/deactivate`,
            import_pieces_common10.HttpMethod.POST
          ), console.log(`Deactivated event hook: ${hookId}`);
        } catch (deactivateError) {
          console.warn("Failed to deactivate event hook:", deactivateError);
        }
        try {
          await makeOktaRequest(
            context.auth,
            `/eventHooks/${hookId}`,
            import_pieces_common10.HttpMethod.DELETE
          ), console.log(`Deleted event hook: ${hookId}`);
        } catch (deleteError) {
          console.warn("Failed to delete event hook:", deleteError);
        }
      }
      await context.store.delete("hookId");
    } catch (error) {
      console.error("Error cleaning up Okta event hook:", error);
    }
  },
  async run(context) {
    var _a;
    let payload = context.payload.body;
    console.log("firstfdsdfsdf", JSON.stringify(payload));
    let configuredEventTypes = context.propsValue.eventTypes || [];
    if (!((_a = payload.data) != null && _a.events) || !Array.isArray(payload.data.events))
      return console.log("No events found in payload, skipping"), [];
    let filteredEvents = [];
    for (let event of payload.data.events) {
      if (configuredEventTypes.length > 0 && !configuredEventTypes.includes(event.eventType)) {
        console.log(
          `Event type ${event.eventType} not in configured types, skipping`
        );
        continue;
      }
      filteredEvents.push(event);
    }
    return filteredEvents;
  },
  async test(context) {
    try {
      return (await makeOktaRequest(
        context.auth,
        "/logs?limit=1",
        import_pieces_common10.HttpMethod.GET
      )).body || [];
    } catch (error) {
      return console.error("Test error:", error), [];
    }
  },
  sampleData: {
    eventId: "evt_123456789",
    timestamp: (/* @__PURE__ */ new Date()).toISOString(),
    version: "0",
    severity: "INFO",
    eventType: "user.lifecycle.create",
    displayMessage: "User created: user@example.com",
    actor: {
      id: "admin123",
      type: "User",
      alternateId: "admin@example.com",
      displayName: "Admin User"
    },
    outcome: {
      result: "SUCCESS",
      reason: ""
    },
    target: [
      {
        id: "user123",
        type: "User",
        alternateId: "user@example.com",
        displayName: "New User"
      }
    ]
  }
});

// ../ap/packages/pieces/community/okta/src/index.ts
var import_pieces_common11 = require("@activepieces/pieces-common"), okta = (0, import_pieces_framework14.createPiece)({
  displayName: "Okta",
  auth: oktaAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/okta.png",
  authors: ["Ani-4x", "sanket-a11y"],
  categories: [import_pieces_framework15.PieceCategory.PRODUCTIVITY],
  actions: [
    createUserAction,
    activateUserAction,
    deactivateUserAction,
    suspendUserAction,
    addUserToGroupAction,
    removeUserFromGroupAction,
    updateUserAction,
    findUserByEmailAction,
    findGroupByNameAction,
    (0, import_pieces_common11.createCustomApiCallAction)({
      baseUrl: (auth) => `${auth == null ? void 0 : auth.props.domain}/api/v1`,
      auth: oktaAuth,
      authMapping: async (auth) => ({
        Authorization: `SSWS ${auth.props.apiToken}`
      })
    })
  ],
  triggers: [newEventTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  okta
});
