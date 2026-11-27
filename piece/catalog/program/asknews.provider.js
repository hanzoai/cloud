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

// ../ap/packages/pieces/community/asknews/src/index.ts
var index_exports = {};
__export(index_exports, {
  asknews: () => asknews
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework10 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/asknews/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), asknewsAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `
To obtain your AskNews API key:

1. Visit [AskNews](https://asknews.app)
2. Sign up or log in to your account
3. Click on your profile and navigate to 'Settings' > 'API Credentials'
4. Generate a new API key
5. Copy and paste the key here
`,
  required: !0
});

// ../ap/packages/pieces/community/asknews/src/lib/actions/search-news.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/asknews/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.asknews.app/v1";
async function makeRequest(apiKey, method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        Authorization: `Bearer ${apiKey}`,
        "Content-Type": "application/json"
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/asknews/src/lib/actions/search-news.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), searchNews = (0, import_pieces_framework2.createAction)({
  auth: asknewsAuth,
  name: "searchNews",
  classification: "SEARCH",
  displayName: "Search News",
  description: "Search for enriched real-time news articles from the past 48 hours or historical archive",
  audience: "both",
  aiMetadata: {
    description: "Searches enriched news articles via the AskNews API, defaulting to recent coverage but able to query the historical archive (60+ days) instead when historical is set. The query can be keywords, a question, or a paragraph, and the search method switches between keyword, semantic natural-language, or hybrid matching; filter by category and recency. Choose this to find or retrieve current/past news on a topic. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    query: import_pieces_framework2.Property.ShortText({
      displayName: "Query",
      description: "Search query - can be a phrase, keyword, question, or paragraph. Leave empty to search by filters only.",
      required: !0
    }),
    nArticles: import_pieces_framework2.Property.Number({
      displayName: "Number of Articles",
      description: "Number of articles to return",
      defaultValue: 10,
      required: !1
    }),
    method: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Search Method",
      description: 'Search method to use. "kw" for keyword matching, "nl" for semantic/natural language search, "both" for hybrid',
      options: {
        options: [
          { label: "Keyword (kw)", value: "kw" },
          { label: "Natural Language (nl)", value: "nl" },
          { label: "Both (kw + nl)", value: "both" }
        ]
      },
      defaultValue: "kw",
      required: !1
    }),
    returnType: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Return Type",
      description: 'Format of returned articles. "string" is prompt-optimized, "dicts" is structured data, "both" returns both',
      options: {
        options: [
          { label: "String (prompt-optimized)", value: "string" },
          { label: "Dicts (structured)", value: "dicts" },
          { label: "Both", value: "both" }
        ]
      },
      defaultValue: "string",
      required: !1
    }),
    historical: import_pieces_framework2.Property.Checkbox({
      displayName: "Search Historical Archive",
      description: "Search in historical archive (60+ days back) instead of just recent news (48 hours)",
      defaultValue: !1,
      required: !1
    }),
    hoursBack: import_pieces_framework2.Property.Number({
      displayName: "Hours Back",
      description: "Number of hours back to search from current time. Defaults to 24.",
      defaultValue: 24,
      required: !1
    }),
    categories: import_pieces_framework2.Property.StaticMultiSelectDropdown({
      displayName: "Categories",
      description: "Filter articles by news categories",
      options: {
        options: [
          { label: "Business", value: "Business" },
          { label: "Crime", value: "Crime" },
          { label: "Politics", value: "Politics" },
          { label: "Science", value: "Science" },
          { label: "Sports", value: "Sports" },
          { label: "Technology", value: "Technology" },
          { label: "Military", value: "Military" },
          { label: "Health", value: "Health" },
          { label: "Entertainment", value: "Entertainment" },
          { label: "Finance", value: "Finance" },
          { label: "Culture", value: "Culture" },
          { label: "Climate", value: "Climate" },
          { label: "Environment", value: "Environment" },
          { label: "World", value: "World" }
        ]
      },
      required: !1
    }),
    similarityScoreThreshold: import_pieces_framework2.Property.Number({
      displayName: "Similarity Score Threshold",
      description: "Minimum similarity score (0-1) for results. Lower = more lenient matches.",
      defaultValue: 0.5,
      required: !1
    })
  },
  async run(context) {
    let {
      query,
      nArticles,
      method,
      returnType,
      historical,
      hoursBack,
      categories,
      similarityScoreThreshold
    } = context.propsValue, queryParams = {
      query
    };
    nArticles && (queryParams.n_articles = nArticles), method && (queryParams.method = method), returnType && (queryParams.return_type = returnType), historical && (queryParams.historical = historical), hoursBack && (queryParams.hours_back = hoursBack), categories && categories.length > 0 && (queryParams.categories = categories), similarityScoreThreshold && (queryParams.similarity_score_threshold = similarityScoreThreshold);
    let queryString = new URLSearchParams(queryParams).toString();
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common2.HttpMethod.GET,
      `/news/search?${queryString}`
    );
  }
});

// ../ap/packages/pieces/community/asknews/src/lib/actions/generate-news-knowledge-graph.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), generateNewsKnowledgeGraph = (0, import_pieces_framework3.createAction)({
  auth: asknewsAuth,
  name: "generateNewsKnowledgeGraph",
  classification: "SEARCH",
  displayName: "Generate News Knowledge Graph",
  description: "Build a custom mega-news-knowledge graph with fully disambiguated entities and relationships",
  audience: "both",
  aiMetadata: {
    description: "Builds a news knowledge graph of disambiguated entities and their relationships from articles matching a natural-language query, via the AskNews API. Choose this when an agent needs structured entity/relationship extraction (optionally a visualization like mermaid/svg) rather than raw article text. Computes a fresh graph on each call from current matching news; safe to repeat.",
    idempotent: !0
  },
  props: {
    query: import_pieces_framework3.Property.ShortText({
      displayName: "Query",
      description: "Natural language query for building the graph",
      required: !0
    }),
    returnArticles: import_pieces_framework3.Property.Checkbox({
      displayName: "Return Articles",
      description: "Include articles in the response",
      defaultValue: !1,
      required: !1
    }),
    minClusterProbability: import_pieces_framework3.Property.Number({
      displayName: "Min Cluster Probability",
      description: "Minimum cluster probability for disambiguation (0-1)",
      defaultValue: 0.9,
      required: !1
    }),
    geoDiambiguation: import_pieces_framework3.Property.Checkbox({
      displayName: "Geo Disambiguation",
      description: "Use geographic disambiguation for entities",
      defaultValue: !1,
      required: !1
    }),
    queryMethod: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Query Method",
      description: "Search method for the query",
      defaultValue: "kw",
      required: !1,
      options: {
        options: [
          { label: "Keyword", value: "kw" },
          { label: "Natural Language", value: "nl" },
          { label: "Both", value: "both" }
        ]
      }
    }),
    nArticles: import_pieces_framework3.Property.Number({
      displayName: "Number of Articles",
      description: "Number of articles to use for building the graph",
      defaultValue: 10,
      required: !1
    }),
    historical: import_pieces_framework3.Property.Checkbox({
      displayName: "Search Historical",
      description: "Search historical archive instead of recent news",
      defaultValue: !1,
      required: !1
    }),
    hoursBack: import_pieces_framework3.Property.Number({
      displayName: "Hours Back",
      description: "Number of hours back to search",
      defaultValue: 24,
      required: !1
    }),
    categories: import_pieces_framework3.Property.StaticMultiSelectDropdown({
      displayName: "Categories",
      description: "Filter by news categories",
      required: !1,
      options: {
        options: [
          { label: "Business", value: "Business" },
          { label: "Technology", value: "Technology" },
          { label: "Science", value: "Science" },
          { label: "Politics", value: "Politics" },
          { label: "Sports", value: "Sports" },
          { label: "Entertainment", value: "Entertainment" },
          { label: "Health", value: "Health" },
          { label: "Finance", value: "Finance" }
        ]
      }
    }),
    visualizeWith: import_pieces_framework3.Property.ShortText({
      displayName: "Visualize With",
      description: "Request a visualization format (e.g., mermaid, svg)",
      required: !1
    })
  },
  async run(context) {
    let params = context.propsValue, requestBody = {
      query: params.query,
      return_articles: params.returnArticles,
      min_cluster_probability: params.minClusterProbability,
      geo_disambiguation: params.geoDiambiguation,
      filter_params: {
        method: params.queryMethod,
        n_articles: params.nArticles,
        historical: params.historical,
        hours_back: params.hoursBack,
        categories: params.categories
      },
      visualize_with: params.visualizeWith
    };
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common3.HttpMethod.POST,
      "/news/graph",
      requestBody
    );
  }
});

// ../ap/packages/pieces/community/asknews/src/lib/actions/get-article-by-id.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common"), getArticleById = (0, import_pieces_framework4.createAction)({
  auth: asknewsAuth,
  name: "getArticleById",
  classification: "READ",
  displayName: "Get Article by ID",
  description: "Retrieve a single article or multiple articles by their UUID(s)",
  audience: "both",
  aiMetadata: {
    description: "Fetches one or more news articles by their AskNews UUID(s). Choose this when you already hold article UUIDs (e.g. from a prior search) and need their full enriched records. Requires at least one valid UUID. Read-only lookup and idempotent.",
    idempotent: !0
  },
  props: {
    articleIds: import_pieces_framework4.Property.Array({
      displayName: "Article ID(s)",
      description: "One or more article UUIDs to fetch.",
      required: !0
    })
  },
  async run(context) {
    let { articleIds } = context.propsValue, endpoint = `/news?${articleIds.map((id) => `article_ids=${encodeURIComponent(id)}`).join("&")}`;
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common4.HttpMethod.GET,
      endpoint
    );
  }
});

// ../ap/packages/pieces/community/asknews/src/lib/actions/asknews-chat-completion.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common"), asknewsChatCompletion = (0, import_pieces_framework5.createAction)({
  auth: asknewsAuth,
  name: "asknewsChatCompletion",
  classification: "READ",
  displayName: "Chat Completion",
  description: "Get chat completions from a news-infused AI assistant powered by OpenAI API",
  audience: "both",
  aiMetadata: {
    description: "Sends a user message (with an optional system prompt) to AskNews\u2019 chat-completion endpoint, returning an LLM answer grounded in real-time news with optional inline citations and references. Choose this for natural-language Q&A about current events rather than retrieving raw articles. Generative and non-deterministic, so repeated calls may return different text.",
    idempotent: !1
  },
  props: {
    userMessage: import_pieces_framework5.Property.LongText({
      displayName: "User Message",
      description: "The user message to send to the chat model",
      required: !0
    }),
    model: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Model",
      description: "The model to use for chat completion",
      defaultValue: "gpt-4o-mini",
      required: !1,
      options: {
        options: [
          { label: "GPT-4o", value: "gpt-4o" },
          { label: "GPT-4o Mini", value: "gpt-4o-mini" },
          { label: "GPT-4.1", value: "gpt-4.1-2025-04-14" },
          { label: "Claude 3.5 Sonnet", value: "claude-3-5-sonnet-latest" }
        ]
      }
    }),
    temperature: import_pieces_framework5.Property.Number({
      displayName: "Temperature",
      description: "Sampling temperature (0-2, higher = more creative)",
      defaultValue: 0.9,
      required: !1
    }),
    maxTokens: import_pieces_framework5.Property.Number({
      displayName: "Max Tokens",
      description: "Maximum number of tokens in response",
      defaultValue: 9999,
      required: !1
    }),
    journalistMode: import_pieces_framework5.Property.Checkbox({
      displayName: "Journalist Mode",
      description: "Activate AP styling, citations, and fair reporting prompting",
      defaultValue: !0,
      required: !1
    }),
    inlineCitations: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Inline Citations",
      description: "How to format citations in results",
      defaultValue: "markdown_link",
      required: !1,
      options: {
        options: [
          { label: "Markdown Links", value: "markdown_link" },
          { label: "Numbered", value: "numbered" },
          { label: "None", value: "none" }
        ]
      }
    }),
    appendReferences: import_pieces_framework5.Property.Checkbox({
      displayName: "Append References",
      description: "Append full references at the end",
      defaultValue: !0,
      required: !1
    }),
    asknewsWatermark: import_pieces_framework5.Property.Checkbox({
      displayName: "Include AskNews Watermark",
      description: "Include AskNews branding in response",
      defaultValue: !0,
      required: !1
    }),
    conversationalAwareness: import_pieces_framework5.Property.Checkbox({
      displayName: "Conversational Awareness",
      description: "Enable context awareness for conversational flow",
      defaultValue: !0,
      required: !1
    }),
    systemPrompt: import_pieces_framework5.Property.LongText({
      displayName: "System Prompt",
      description: "Optional custom system prompt for the model",
      required: !1,
      defaultValue: "You are a helpful AI assistant with access to real-time news and current information."
    })
  },
  async run(context) {
    let {
      userMessage,
      model,
      temperature,
      maxTokens,
      journalistMode,
      inlineCitations,
      appendReferences,
      asknewsWatermark,
      conversationalAwareness,
      systemPrompt
    } = context.propsValue, requestBody = {
      model,
      messages: [
        {
          role: "system",
          content: systemPrompt
        },
        {
          role: "user",
          content: userMessage
        }
      ],
      temperature,
      max_tokens: maxTokens,
      journalist_mode: journalistMode,
      inline_citations: inlineCitations,
      append_references: appendReferences,
      asknews_watermark: asknewsWatermark,
      conversational_awareness: conversationalAwareness
    };
    return (await makeRequest(
      context.auth.secret_text,
      import_pieces_common5.HttpMethod.POST,
      "/openai/chat/completions",
      requestBody
    )).choices[0].message.content;
  }
});

// ../ap/packages/pieces/community/asknews/src/lib/actions/search-stories.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common"), searchStories = (0, import_pieces_framework6.createAction)({
  auth: asknewsAuth,
  name: "searchStories",
  classification: "SEARCH",
  displayName: "Search Stories (Narrative Clusters)",
  description: "Filter and search for top news narratives and story clusters",
  audience: "both",
  aiMetadata: {
    description: "Searches AskNews for top news narratives and story clusters (grouped multi-article events) rather than individual articles. The query is optional: with no query it returns top trending stories filtered by category/sort, and with a query it filters to matching narratives via keyword, semantic, or hybrid matching. Choose this for the big-picture narrative view over a topic. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    query: import_pieces_framework6.Property.ShortText({
      displayName: "Query",
      description: "Natural language or keyword query for search",
      required: !1
    }),
    method: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Search Method",
      description: "nl: semantic search, kw: keyword search, both: combined",
      required: !1,
      defaultValue: "kw",
      options: {
        options: [
          { label: "Keyword", value: "kw" },
          { label: "Natural Language", value: "nl" },
          { label: "Both", value: "both" }
        ]
      }
    }),
    categories: import_pieces_framework6.Property.StaticMultiSelectDropdown({
      displayName: "Categories",
      description: "Filter stories by categories",
      required: !1,
      options: {
        options: [
          { label: "Politics", value: "Politics" },
          { label: "Economy", value: "Economy" },
          { label: "Finance", value: "Finance" },
          { label: "Science", value: "Science" },
          { label: "Technology", value: "Technology" },
          { label: "Sports", value: "Sports" },
          { label: "Climate", value: "Climate" },
          { label: "Environment", value: "Environment" },
          { label: "Culture", value: "Culture" },
          { label: "Entertainment", value: "Entertainment" },
          { label: "Business", value: "Business" },
          { label: "Health", value: "Health" },
          { label: "International", value: "International" }
        ]
      }
    }),
    expand_updates: import_pieces_framework6.Property.Checkbox({
      displayName: "Expand Updates",
      description: "Include story updates in results",
      required: !1,
      defaultValue: !1
    }),
    sort_by: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Sort By",
      description: "Sort results by field",
      required: !1,
      defaultValue: "published",
      options: {
        options: [
          { label: "Published Date", value: "published" },
          { label: "Coverage", value: "coverage" },
          { label: "Sentiment", value: "sentiment" },
          { label: "Relevance", value: "relevance" }
        ]
      }
    })
  },
  async run(context) {
    var _a;
    let params = context.propsValue, queryParams = new URLSearchParams();
    params.query && queryParams.append("query", params.query), params.method && queryParams.append("method", params.method), (_a = params.categories) != null && _a.length && params.categories.forEach(
      (cat) => queryParams.append("categories", cat)
    ), params.expand_updates && queryParams.append("expand_updates", "true"), params.sort_by && queryParams.append("sort_by", params.sort_by);
    let endpoint = `/stories?${queryParams.toString()}`;
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common6.HttpMethod.GET,
      endpoint
    );
  }
});

// ../ap/packages/pieces/community/asknews/src/index.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/asknews/src/lib/actions/create-a-newsletter.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common"), createANewsletter = (0, import_pieces_framework7.createAction)({
  auth: asknewsAuth,
  name: "createANewsletter",
  classification: "WRITE",
  displayName: "Create a Newsletter",
  description: "Create an automated newsletter powered by news and AI",
  audience: "both",
  aiMetadata: {
    description: "Creates a new automated AskNews newsletter that recurrently generates and emails AI-written news digests on a cron schedule, defined by a natural-language query. Choose this to set up a recurring news email; requires a name, query, cron expression, model, and sender, plus a Resend API key to actually send. Not idempotent \u2014 each call creates a separate newsletter.",
    idempotent: !1
  },
  props: {
    name: import_pieces_framework7.Property.ShortText({
      displayName: "Newsletter Name",
      description: "Name of the newsletter",
      required: !0
    }),
    query: import_pieces_framework7.Property.LongText({
      displayName: "Query",
      description: "Natural language query to define newsletter content",
      required: !0
    }),
    cron: import_pieces_framework7.Property.ShortText({
      displayName: "Cron Schedule",
      description: 'Cron expression for newsletter schedule (e.g., "0 0 * * *" for daily at midnight UTC)',
      required: !0
    }),
    model: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Model",
      description: "AI model to use for newsletter generation",
      defaultValue: "gpt-4o",
      required: !0,
      options: {
        options: [
          { label: "GPT-4o", value: "gpt-4o" },
          { label: "GPT-4o Mini", value: "gpt-4o-mini" },
          { label: "Claude 3.5 Sonnet", value: "claude-3-5-sonnet-latest" },
          { label: "Meta Llama 3.1 405B", value: "meta-llama/Meta-Llama-3.1-405B-Instruct" },
          { label: "Meta Llama 3.3 70B", value: "meta-llama/Meta-Llama-3.3-70B-Instruct" }
        ]
      }
    }),
    sender: import_pieces_framework7.Property.ShortText({
      displayName: "Sender Email",
      description: "Email address of the newsletter sender",
      required: !0
    }),
    subject: import_pieces_framework7.Property.ShortText({
      displayName: "Subject",
      description: "Newsletter email subject (auto-generated if not provided)",
      required: !1
    }),
    logoUrl: import_pieces_framework7.Property.ShortText({
      displayName: "Logo URL",
      description: "URL of the newsletter logo",
      required: !1
    }),
    replyTo: import_pieces_framework7.Property.ShortText({
      displayName: "Reply-To Email",
      description: "Email address for replies (defaults to sender if not provided)",
      required: !1
    }),
    audienceId: import_pieces_framework7.Property.ShortText({
      displayName: "Audience ID",
      description: "Existing audience ID (creates new audience if not provided)",
      required: !1
    }),
    resendApiKey: import_pieces_framework7.Property.ShortText({
      displayName: "Resend API Key",
      description: "API key for Resend email service (required to send newsletter)",
      required: !1
    }),
    isPublic: import_pieces_framework7.Property.Checkbox({
      displayName: "Is Public",
      description: "Make newsletter publicly discoverable",
      defaultValue: !0,
      required: !1
    }),
    isActive: import_pieces_framework7.Property.Checkbox({
      displayName: "Is Active",
      description: "Activate the newsletter immediately",
      defaultValue: !0,
      required: !1
    }),
    expiresAt: import_pieces_framework7.Property.ShortText({
      displayName: "Expiration Date",
      description: "Newsletter expiration date (ISO format: YYYY-MM-DDTHH:MM:SS)",
      required: !1
    })
  },
  async run(context) {
    let { name, query, cron, model, sender, subject, logoUrl, replyTo, audienceId, resendApiKey, isPublic, isActive, expiresAt } = context.propsValue, requestBody = {
      name,
      query,
      cron,
      model,
      sender,
      subject,
      logo_url: logoUrl,
      reply_to: replyTo,
      audience_id: audienceId,
      resend_api_key: resendApiKey,
      public: isPublic,
      active: isActive,
      expires_at: expiresAt
    };
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common7.HttpMethod.POST,
      "/chat/newsletters",
      requestBody
    );
  }
});

// ../ap/packages/pieces/community/asknews/src/lib/actions/update-a-newsletter.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var import_pieces_common8 = require("@activepieces/pieces-common"), updateANewsletter = (0, import_pieces_framework8.createAction)({
  auth: asknewsAuth,
  name: "updateANewsletter",
  classification: "WRITE",
  displayName: "Update a Newsletter",
  description: "Update an existing automated newsletter",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing AskNews newsletter identified by its UUID, applying only the fields you supply (query, schedule, model, sender, active/public flags, etc.) and leaving the rest unchanged. Choose this to edit or activate/deactivate a newsletter you already created. Requires the newsletter UUID. Repeating the same update yields the same end state.",
    idempotent: !0
  },
  props: {
    newsletterId: import_pieces_framework8.Property.ShortText({
      displayName: "Newsletter ID",
      description: "UUID of the newsletter to update",
      required: !0
    }),
    name: import_pieces_framework8.Property.ShortText({
      displayName: "Newsletter Name",
      description: "Name of the newsletter",
      required: !1
    }),
    query: import_pieces_framework8.Property.LongText({
      displayName: "Query",
      description: "Natural language query to define newsletter content",
      required: !1
    }),
    cron: import_pieces_framework8.Property.ShortText({
      displayName: "Cron Schedule",
      description: 'Cron expression for newsletter schedule (e.g., "0 0 * * *" for daily at midnight UTC)',
      required: !1
    }),
    model: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Model",
      description: "AI model to use for newsletter generation",
      required: !1,
      options: {
        options: [
          { label: "GPT-4o", value: "gpt-4o" },
          { label: "GPT-4o Mini", value: "gpt-4o-mini" },
          { label: "Claude 3.5 Sonnet", value: "claude-3-5-sonnet-latest" },
          {
            label: "Meta Llama 3.3 70B",
            value: "meta-llama/Meta-Llama-3.3-70B-Instruct"
          }
        ]
      }
    }),
    subject: import_pieces_framework8.Property.ShortText({
      displayName: "Subject",
      description: "Newsletter email subject",
      required: !1
    }),
    sender: import_pieces_framework8.Property.ShortText({
      displayName: "Sender Email",
      description: "Email address of the newsletter sender",
      required: !1
    }),
    logoUrl: import_pieces_framework8.Property.ShortText({
      displayName: "Logo URL",
      description: "URL of the newsletter logo",
      required: !1
    }),
    replyTo: import_pieces_framework8.Property.ShortText({
      displayName: "Reply-To Email",
      description: "Email address for replies",
      required: !1
    }),
    audienceId: import_pieces_framework8.Property.ShortText({
      displayName: "Audience ID",
      description: "Audience ID for the newsletter",
      required: !1
    }),
    resendApiKey: import_pieces_framework8.Property.ShortText({
      displayName: "Resend API Key",
      description: "API key for Resend email service",
      required: !1
    }),
    isPublic: import_pieces_framework8.Property.Checkbox({
      displayName: "Is Public",
      description: "Make newsletter publicly discoverable",
      required: !1
    }),
    isActive: import_pieces_framework8.Property.Checkbox({
      displayName: "Is Active",
      description: "Activate or deactivate the newsletter",
      required: !1
    }),
    expiresAt: import_pieces_framework8.Property.ShortText({
      displayName: "Expiration Date",
      description: "Newsletter expiration date (ISO format: YYYY-MM-DDTHH:MM:SS)",
      required: !1
    })
  },
  async run(context) {
    let {
      newsletterId,
      name,
      query,
      cron,
      model,
      subject,
      sender,
      logoUrl,
      replyTo,
      audienceId,
      resendApiKey,
      isPublic,
      isActive,
      expiresAt
    } = context.propsValue, requestBody = {};
    return name !== void 0 && name !== "" && (requestBody.name = name), query !== void 0 && query !== "" && (requestBody.query = query), cron !== void 0 && cron !== "" && (requestBody.cron = cron), model !== void 0 && model !== "" && (requestBody.model = model), subject !== void 0 && subject !== "" && (requestBody.subject = subject), sender !== void 0 && sender !== "" && (requestBody.sender = sender), logoUrl !== void 0 && logoUrl !== "" && (requestBody.logo_url = logoUrl), replyTo !== void 0 && replyTo !== "" && (requestBody.reply_to = replyTo), audienceId !== void 0 && audienceId !== "" && (requestBody.audience_id = audienceId), resendApiKey !== void 0 && resendApiKey !== "" && (requestBody.resend_api_key = resendApiKey), isPublic !== void 0 && (requestBody.public = isPublic), isActive !== void 0 && (requestBody.active = isActive), expiresAt !== void 0 && expiresAt !== "" && (requestBody.expires_at = expiresAt), await makeRequest(
      context.auth.secret_text,
      import_pieces_common8.HttpMethod.PUT,
      `/chat/newsletters/${newsletterId}`,
      requestBody
    );
  }
});

// ../ap/packages/pieces/community/asknews/src/lib/triggers/alert-for-query.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var import_pieces_common9 = require("@activepieces/pieces-common"), alertForQuery = (0, import_pieces_framework9.createTrigger)({
  auth: asknewsAuth,
  name: "alertForQuery",
  classification: "READ",
  displayName: "Alert for Query",
  description: "Trigger when a new alert is created for a specific news query",
  aiMetadata: {
    description: 'Fires when an AskNews alert matches a monitored natural-language news query on its cron schedule (or on every scheduled check if "Always Trigger" is enabled), delivering the matching alert payload. Represents a newly detected news development matching the query of interest.'
  },
  props: {
    query: import_pieces_framework9.Property.LongText({
      displayName: "Alert Query",
      description: 'Query to monitor (e.g., "I want to be alerted if there is a protest in paris")',
      required: !0
    }),
    cron: import_pieces_framework9.Property.ShortText({
      displayName: "Cron Schedule",
      description: 'Cron schedule for alert checks (5-7 fields, e.g., "0 * * * *" for every hour, or "0 0 * * * America/New_York")',
      required: !0
    }),
    model: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Model",
      description: "AI model to use for alert checking",
      defaultValue: "gpt-4o-mini",
      required: !1,
      options: {
        options: [
          { label: "GPT-4o Mini", value: "gpt-4o-mini" },
          { label: "GPT-4o", value: "gpt-4o" },
          {
            label: "Llama 3.1 8B",
            value: "meta-llama/Meta-Llama-3.1-8B-Instruct"
          }
        ]
      }
    }),
    alwaysTrigger: import_pieces_framework9.Property.Checkbox({
      displayName: "Always Trigger",
      description: "Skip alert check and trigger immediately on schedule",
      defaultValue: !1,
      required: !1
    }),
    repeat: import_pieces_framework9.Property.Checkbox({
      displayName: "Repeat",
      description: "Repeat alert after each trigger (disable for one-time alert)",
      defaultValue: !0,
      required: !1
    }),
    title: import_pieces_framework9.Property.ShortText({
      displayName: "Alert Title",
      description: "Optional title for the alert",
      required: !1
    })
  },
  sampleData: {},
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    var _a;
    let { auth, propsValue, webhookUrl } = context, params = propsValue, alertRequestBody = {
      query: params.query,
      cron: params.cron,
      model: params.model,
      title: params.title,
      always_trigger: params.alwaysTrigger,
      repeat: params.repeat,
      sources: [
        {
          identifier: "asknews"
        }
      ],
      triggers: [
        {
          action: "webhook",
          params: {
            url: webhookUrl,
            headers: {},
            payload: {}
          }
        }
      ],
      active: !0
    }, response = await makeRequest(
      auth.secret_text,
      import_pieces_common9.HttpMethod.POST,
      "/chat/alerts",
      alertRequestBody
    );
    await ((_a = context.store) == null ? void 0 : _a.put(`asknews_alert_id_${webhookUrl}`, response.id));
  },
  async onDisable(context) {
    var _a;
    let { auth, webhookUrl } = context, alertId = await ((_a = context.store) == null ? void 0 : _a.get(`asknews_alert_id_${webhookUrl}`));
    alertId && (await makeRequest(
      auth.secret_text,
      import_pieces_common9.HttpMethod.DELETE,
      `/chat/alerts/${alertId}`,
      {}
    ), await context.store.delete(`asknews_alert_id_${webhookUrl}`));
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/asknews/src/index.ts
var asknews = (0, import_pieces_framework10.createPiece)({
  displayName: "AskNews",
  auth: asknewsAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/asknews.png",
  authors: ["sanket-a11y"],
  categories: [import_pieces_framework11.PieceCategory.COMMERCE],
  description: "AskNews piece allows you to search and retrieve enriched real-time and historical news articles using AskNews API.",
  actions: [
    searchNews,
    generateNewsKnowledgeGraph,
    getArticleById,
    asknewsChatCompletion,
    searchStories,
    createANewsletter,
    updateANewsletter,
    (0, import_pieces_common10.createCustomApiCallAction)({
      auth: asknewsAuth,
      baseUrl: () => "https://api.asknews.com/v1",
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [alertForQuery]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  asknews
});
