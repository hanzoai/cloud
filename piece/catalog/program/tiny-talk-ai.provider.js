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

// ../ap/packages/pieces/community/tiny-talk-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  tinyTalkAi: () => tinyTalkAi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/tiny-talk-ai/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), tinyTalkAiAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "You can obtain API key from [Dashboard Settings](https://dashboard.tinytalk.ai/)."
});

// ../ap/packages/pieces/community/tiny-talk-ai/src/lib/actions/ask-bot.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var askBotAction = (0, import_pieces_framework2.createAction)({
  name: "ask-bot",
  auth: tinyTalkAiAuth,
  displayName: "Ask Bot",
  description: "Sends message to selected bot.",
  audience: "both",
  aiMetadata: { description: "Sends a user message to a specific TinyTalk AI bot and returns the bot's generated reply as text. Choose this to query a configured conversational bot; requires the target bot's ID (from the bot's Details page) and the question prompt. Not idempotent: each call produces a fresh generation.", idempotent: !1 },
  props: {
    botId: import_pieces_framework2.Property.ShortText({
      displayName: "Bot ID",
      required: !0,
      description: "You can find this in your Bot Details page in the dashboard."
    }),
    prompt: import_pieces_framework2.Property.LongText({
      displayName: "Question",
      required: !0
    })
  },
  async run(context) {
    let { botId, prompt } = context.propsValue, response = await fetch("https://api.tinytalk.ai/v1/chat/completions", {
      method: "POST",
      headers: {
        "Api-Key": context.auth.secret_text,
        "Content-Type": "application/json"
      },
      body: JSON.stringify({
        botId,
        messages: [
          {
            role: "user",
            content: prompt
          }
        ]
      }),
      redirect: "follow"
    });
    if (!response.ok) {
      let errorData = await response.json();
      throw Error(errorData.message);
    }
    let stream = response.body;
    if (!stream)
      throw new Error("No stream returned from TinyTalk API");
    let reader = stream.getReader(), decoder = new TextDecoder(), fullText = "", done = !1;
    for (; !done; ) {
      let { value, done: doneReading } = await reader.read();
      if (done = doneReading, !value) continue;
      decoder.decode(value, { stream: !0 }).split(`
`).forEach((line) => {
        var _a, _b;
        if (line.startsWith("data: ") && !line.includes("[DONE]"))
          try {
            let json = JSON.parse(line.replace("data: ", "")), delta = (_b = (_a = json == null ? void 0 : json.choices) == null ? void 0 : _a[0]) == null ? void 0 : _b.delta;
            delta != null && delta.content && (fullText += delta.content, process.stdout.write(delta.content));
          } catch (e) {
          }
      });
    }
    return fullText;
  }
});

// ../ap/packages/pieces/community/tiny-talk-ai/src/index.ts
var tinyTalkAi = (0, import_pieces_framework3.createPiece)({
  displayName: "Tiny Talk AI",
  auth: tinyTalkAiAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/tiny-talk-ai.png",
  authors: ["kishanprmr"],
  actions: [askBotAction],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  tinyTalkAi
});
