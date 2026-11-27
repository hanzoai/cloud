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

// ../ap/packages/pieces/community/lucidya/src/index.ts
var index_exports = {};
__export(index_exports, {
  lucidya: () => lucidya
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/lucidya/src/lib/triggers/new-alert.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/lucidya/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/lucidya/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.lucidya.com";
async function makeRequest(apiKey, method, path, queryParams, body) {
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${BASE_URL}${path}`,
    headers: {
      "luc-authorization": apiKey,
      "Content-Type": "application/json"
    },
    queryParams,
    body
  })).body;
}

// ../ap/packages/pieces/community/lucidya/src/lib/auth.ts
var lucidyaAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `To get your API Key:

1. Log in to your Lucidya account
2. Navigate to **Settings**
3. Select **Lucidya API** from the left menu
4. Click **Generate API Key** and select "Social Listening API"
5. Copy and securely store your API token`,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await makeRequest(auth, import_pieces_common2.HttpMethod.GET, "/monitors_list", { page_id: "1" }), { valid: !0 };
    } catch (error) {
      return {
        valid: !1,
        error: "Invalid API Key"
      };
    }
  }
});

// ../ap/packages/pieces/community/lucidya/src/lib/triggers/new-alert.ts
var newAlertTrigger = (0, import_pieces_framework2.createTrigger)({
  auth: lucidyaAuth,
  name: "new_alert",
  displayName: "New Alert Notification",
  description: "Triggers when a new alert notification is generated (hourly or instant).",
  aiMetadata: {
    description: "Fires when Lucidya generates a new alert notification (either on an hourly schedule or instantly), delivered via a webhook configured on a Lucidya alert. The event payload represents the alert details, such as trending social activity, sentiment analysis, top engagers, influencers, hashtags, keywords, and volume-over-time metrics for the monitored topic."
  },
  props: {
    md: import_pieces_framework2.Property.MarkDown({
      value: `
To use this trigger, set up a webhook in Lucidya:

1. Navigate to the **Alerts** section in Lucidya
2. Click **Create an Alert** and fill in the alert details
3. Enable the **Webhook** feature
4. Paste the webhook URL below into the webhook URL field:
   \`\`\`text
   {{webhookUrl}}
   \`\`\`
5. Test the webhook URL before saving
6. Save your alert

**Note:** Webhook will be paused if no response is received for 15 days.
      `
    })
  },
  type: import_pieces_framework2.TriggerStrategy.WEBHOOK,
  sampleData: {
    Twitter__TrendingAlert__account_types: '{"account_types":[]}',
    Twitter__TrendingAlert__dialects_and_sub_dialects: '{"dialects":[{"name":"white","value":2}],"sub_dialects":[{"name":"white","value":2}]}',
    Twitter__TrendingAlert__header_alert_widget: '{"net_sentiment":"neutral","alert_level":"mild","verified_authors":1,"negative_rate":12.5,"total_posts":{"value":8,"date":"2023-09-11 07:00:00"},"influencer_authors":3,"mentions_in_total":8,"total_reached_tweets":null,"total_reached_accounts":null,"increase_in_post_interactions":0.0}',
    Twitter__TrendingAlert__sentiment_analysis: '{"sentiment_overtime":{"negative":[{"name":1694512800.0,"value":0.3333333333333333}],"positive":[{"name":1694512800.0,"value":0}],"neutral":[{"name":1694512800.0,"value":0.6666666666666666}]},"sentiment_piechart":[{"name":"positive","value":0},{"name":"negative","value":1},{"name":"neutral","value":2}]}',
    Twitter__TrendingAlert__top_engagers: '{"top_engagers":[{"id":"kah_aboud","name":"\u0639\u0628\u062F\u0627\u0644\u0631\u062D\u0645\u0646","data":"http://pbs.twimg.com/profile_images/1678432317407150081/G33l6zZ1_normal.jpg","user_id":1037466845241126912,"verified":"false","value":1}]}',
    Twitter__TrendingAlert__top_hashtags: '{"top_hashtags":[]}',
    Twitter__TrendingAlert__top_influencers: '{"top_influencers":[{"id":"mhmad95236811","name":"\u0637\u0627\u0631\u0642 \u0627\u0644\u0632\u0647\u0631\u0627\u0646\u064A","data":"http://pbs.twimg.com/profile_images/1693071068657745920/WWKnt9Lg_normal.jpg","verified":"false","user_id":1257804632568586243,"value":3769}]}',
    Twitter__TrendingAlert__top_keywords: '{"top_keywords":[{"id":"\u0627\u0644\u0639\u064A\u0646","stats_count":1},{"id":"\u0627\u0644\u0642\u062F\u0645","stats_count":1}]}',
    Twitter__TrendingAlert__top_verified_engagers: '{"top_verified_engagers":[]}',
    Twitter__TrendingAlert__volume_overtime: '{"posts_over_time":[{"name":1694512800.0,"value":8}],"total_posts_count":8}'
  },
  async onEnable() {
  },
  async onDisable() {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/lucidya/src/index.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var lucidya = (0, import_pieces_framework3.createPiece)({
  displayName: "Lucidya",
  description: "AI-powered social media analytics and customer experience management",
  auth: lucidyaAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/lucidya.png",
  categories: [import_pieces_framework4.PieceCategory.MARKETING],
  authors: ["onyedikachi-david"],
  actions: [],
  triggers: [newAlertTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  lucidya
});
