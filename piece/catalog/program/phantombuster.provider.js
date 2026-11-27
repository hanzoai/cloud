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

// ../ap/packages/pieces/community/phantombuster/src/index.ts
var index_exports = {};
__export(index_exports, {
  phantombuster: () => phantombuster
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/phantombuster/src/lib/actions/launch-phantom.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/phantombuster/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/phantombuster/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.phantombuster.com/api/v2";
async function makeRequest(apiKey, method, path, body) {
  try {
    return console.log(`${BASE_URL}${path}`), (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        "X-Phantombuster-Key": `${apiKey}`,
        "Content-Type": "application/json"
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/phantombuster/src/lib/common/auth.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), authHelpDescription = `
1. Login to your Pushbullet Dashboard.
2. Go to **https://phantombuster.com/workspace-settings**.
3. change to the **API Keys** tab and **Add API Key**.
4. Copy the API Key to the clipboard and paste it.
`, phantombusterAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Token",
  description: authHelpDescription,
  required: !0,
  validate: async (auth) => {
    try {
      return await makeRequest(
        { secret_text: auth.auth, type: import_pieces_framework2.AppConnectionType.SECRET_TEXT },
        import_pieces_common2.HttpMethod.GET,
        "/agents/fetch-all"
      ), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API key or insufficient permissions"
      };
    }
  }
});

// ../ap/packages/pieces/community/phantombuster/src/lib/actions/launch-phantom.ts
var import_pieces_common4 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/phantombuster/src/lib/common/props.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common");
var agentIdDropdown = import_pieces_framework3.Property.Dropdown({
  auth: phantombusterAuth,
  displayName: "Agent",
  description: "Select the Phantombuster agent to use",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => auth ? {
    disabled: !1,
    options: (await makeRequest(
      auth,
      import_pieces_common3.HttpMethod.GET,
      "/agents/fetch-all"
    )).map((agent) => ({
      label: agent.name + " " + agent.createdAt,
      value: agent.id
    }))
  } : {
    disabled: !0,
    options: [],
    placeholder: "Please configure the authentication first"
  }
});

// ../ap/packages/pieces/community/phantombuster/src/lib/actions/launch-phantom.ts
var launchPhantom = (0, import_pieces_framework4.createAction)({
  auth: phantombusterAuth,
  name: "launchPhantom",
  displayName: "Launch Phantom",
  description: "Launch a Phantombuster agent and add it to the execution queue",
  audience: "both",
  aiMetadata: {
    description: "Queues a Phantombuster agent (selected by id) to run, optionally passing a launch argument and a max-retries count. Use this to trigger a scraping or automation phantom. Each call starts a new execution, so it is not idempotent. By default it returns immediately after queuing; enable wait-for-output to block and poll until the run finishes (or errors), returning the run output instead.",
    idempotent: !1
  },
  props: {
    agentId: agentIdDropdown,
    waitForOutput: import_pieces_framework4.Property.Checkbox({
      displayName: "Wait for Output",
      description: "Wait until the agent execution completes and output is available",
      required: !1,
      defaultValue: !1
    }),
    argument: import_pieces_framework4.Property.Object({
      displayName: "Argument",
      description: "Optional argument to pass to the agent",
      required: !1
    }),
    maxRetries: import_pieces_framework4.Property.Number({
      displayName: "Max Retries",
      description: "Maximum number of retries if the agent fails",
      required: !1
    })
  },
  async run(context) {
    let body = {
      id: context.propsValue.agentId
    };
    context.propsValue.argument !== void 0 && (body.argument = context.propsValue.argument), context.propsValue.maxRetries !== void 0 && (body.maxRetries = context.propsValue.maxRetries);
    let launchResponse = await makeRequest(
      context.auth,
      import_pieces_common4.HttpMethod.POST,
      "/agents/launch",
      body
    );
    if (context.propsValue.waitForOutput) {
      let output = null, pollInterval = 5e3, isCompleted = !1;
      for (; !isCompleted; ) {
        try {
          let outputResponse = await makeRequest(
            context.auth,
            import_pieces_common4.HttpMethod.GET,
            `/agents/fetch-output?id=${context.propsValue.agentId}`,
            void 0
          );
          if (console.log(outputResponse), outputResponse) {
            output = outputResponse;
            let status = outputResponse.status;
            (status === "finished" || status === "error" || status === "unknown") && (isCompleted = !0);
          }
        } catch (error) {
        }
        isCompleted || await new Promise((resolve) => setTimeout(resolve, pollInterval));
      }
      return output;
    }
    return launchResponse;
  }
});

// ../ap/packages/pieces/community/phantombuster/src/lib/triggers/new-output.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_framework6 = require("@activepieces/pieces-framework"), newOutput = (0, import_pieces_framework5.createTrigger)({
  auth: phantombusterAuth,
  name: "newOutput",
  displayName: "New Output",
  description: "Trigger when a new output is generated by a Phantombuster agent ",
  aiMetadata: {
    description: "Fires when a Phantombuster agent finishes a run and posts its result to the configured webhook, representing a completed agent execution (with its exit status and result object) for the selected agent."
  },
  props: {
    agentId: agentIdDropdown,
    markdown: import_pieces_framework5.Property.MarkDown({
      variant: import_pieces_framework6.MarkdownVariant.INFO,
      value: `## Webhook Configuration

To use this trigger, you need to configure a webhook URL in your Phantombuster agent:

### Steps to Configure:
1. Go to your Phantombuster agent page
2. Click on **Settings**
3. Select **Advanced Notification Settings**
4. Input the webhook URL in the webhook field:
\`\`\`
{{webhookUrl}}
\`\`\`

### Supported Exit Messages (Status Values):
- \`finished\` - Agent completed successfully (exitCode: 0)
- \`killed\` - Agent was killed
- \`global timeout\` - Global timeout reached
- \`org timeout\` - Organization timeout reached
- \`agent timeout\` - Agent timeout reached
- \`unknown\` - Unknown status

### Important Notes:
- Maximum webhook timeout: 11 seconds
- Webhooks accept POST requests only
- Maximum 2 redirections allowed
- Response body is ignored (you don't need to respond with data)

[Learn more about webhooks](https://hub.phantombuster.com/docs/using-webhooks)`
    })
  },
  sampleData: {
    agentId: "5027055349780535",
    agentName: "Test Script",
    containerId: "3358014727012763",
    script: "test_script_50516785467.js",
    scriptOrg: "phantombuster",
    branch: "test-branch-0545107204",
    launchDuration: 121,
    runDuration: 1850,
    resultObject: {},
    exitMessage: "finished",
    exitCode: 0
  },
  type: import_pieces_framework5.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let output = context.payload.body;
    return console.log(output), output.agentId != context.propsValue.agentId ? [] : [output];
  }
});

// ../ap/packages/pieces/community/phantombuster/src/index.ts
var import_pieces_common5 = require("@activepieces/pieces-common");
var import_pieces_framework8 = require("@activepieces/pieces-framework"), phantombuster = (0, import_pieces_framework7.createPiece)({
  displayName: "PhantomBuster",
  auth: phantombusterAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/phantombuster.png",
  description: "Automate your web scraping and web automation tasks",
  categories: [import_pieces_framework8.PieceCategory.MARKETING, import_pieces_framework8.PieceCategory.SALES_AND_CRM],
  authors: ["sanket-a11y"],
  actions: [
    launchPhantom,
    (0, import_pieces_common5.createCustomApiCallAction)({
      auth: phantombusterAuth,
      baseUrl: () => BASE_URL,
      authMapping: async (auth) => ({
        "X-Phantombuster-Key": `${auth.secret_text}`,
        "Content-Type": "application/json"
      })
    })
  ],
  triggers: [newOutput]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  phantombuster
});
