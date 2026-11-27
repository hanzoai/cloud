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

// ../ap/packages/pieces/community/jina-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  jinaAi: () => jinaAi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/jina-ai/src/lib/actions/extract-webpage-content.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/jina-ai/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common");
var JinaAICommon = {
  baseUrl: "https://api.jina.ai/v1",
  readerUrl: "https://r.jina.ai",
  readerSearchUrl: "https://s.jina.ai",
  euReaderUrl: "https://eu-r-beta.jina.ai",
  euReaderSearchUrl: "https://eu-s-beta.jina.ai",
  deepsearchUrl: "https://deepsearch.jina.ai/v1/chat/completions",
  classifierUrl: "https://api.jina.ai/v1/classify",
  classifierTrainUrl: "https://api.jina.ai/v1/train",
  async makeRequest({
    url,
    method,
    auth,
    body,
    headers = {}
  }) {
    let response = await import_pieces_common.httpClient.sendRequest({
      method,
      url,
      headers: __spreadValues({
        Authorization: `Bearer ${auth}`,
        "Content-Type": "application/json"
      }, headers),
      body
    });
    if (response.status < 200 || response.status >= 300)
      throw new Error(
        `Jina AI API returned an error: ${response.status} ${response.body}`
      );
    return response.body;
  }
};

// ../ap/packages/pieces/community/jina-ai/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), markdownDescription = `
You can get your API key from [Jina AI](https://jina.ai).
`, jinaAiAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: markdownDescription,
  required: !0
});

// ../ap/packages/pieces/community/jina-ai/src/lib/actions/extract-webpage-content.ts
var extractWebpageContentAction = (0, import_pieces_framework2.createAction)({
  auth: jinaAiAuth,
  name: "extract_webpage_content",
  classification: "READ",
  displayName: "Extract Webpage Content",
  description: "Convert a URL into clean, LLM-friendly Markdown using the Reader API.",
  audience: "both",
  aiMetadata: {
    description: "Fetch a single webpage by URL via the Jina Reader API and return its content cleaned for LLM consumption, with optional markdown/HTML/text/screenshot output, CSS-selector targeting or exclusion, and link/image summaries. Pick this when you already have the target URL; to find pages by keyword first, use Web Search Summarization. Read-only fetch, so repeat calls are idempotent (results may be served from cache unless do-not-track is set).",
    idempotent: !0
  },
  props: {
    url: import_pieces_framework2.Property.ShortText({
      displayName: "URL",
      description: "The URL of the webpage to extract content from.",
      required: !0
    }),
    format: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Output Format",
      description: "Format of the extracted content - how the webpage should be returned.",
      required: !0,
      defaultValue: "default",
      options: {
        options: [
          { label: "Default", value: "default" },
          { label: "Markdown", value: "markdown" },
          { label: "HTML", value: "html" },
          { label: "Text", value: "text" },
          { label: "Screenshot", value: "screenshot" },
          { label: "Pageshot", value: "pageshot" }
        ]
      }
    }),
    remove_all_images: import_pieces_framework2.Property.Checkbox({
      displayName: "Remove All Images",
      description: "Remove all images from the response.",
      required: !1,
      defaultValue: !1
    }),
    links_summary: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Gather Links",
      description: 'Create a "Buttons & Links" section at the end to help downstream LLMs or web agents navigate the page.',
      required: !1,
      defaultValue: "none",
      options: {
        options: [
          { label: "None - Keep links inline", value: "none" },
          { label: "Dedup - List unique links at the end", value: "true" },
          { label: "All - List all links at the end", value: "all" }
        ]
      }
    }),
    images_summary: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Gather Images",
      description: 'Create an "Images" section at the end, giving downstream LLMs an overview of all visuals on the page.',
      required: !1,
      defaultValue: "none",
      options: {
        options: [
          { label: "None - Keep images inline", value: "none" },
          { label: "Dedup - List unique images at the end", value: "true" },
          { label: "All - List all images at the end", value: "all" }
        ]
      }
    }),
    do_not_track: import_pieces_framework2.Property.Checkbox({
      displayName: "Do Not Cache & Track!",
      description: "When enabled, the requested URL won't be cached and tracked on our server.",
      required: !1,
      defaultValue: !1
    }),
    iframe_extraction: import_pieces_framework2.Property.Checkbox({
      displayName: "iframe Extraction",
      description: "Processes content from all embedded iframes in the DOM tree.",
      required: !1,
      defaultValue: !1
    }),
    shadow_dom_extraction: import_pieces_framework2.Property.Checkbox({
      displayName: "Shadow DOM Extraction",
      description: "Extracts content from all Shadow DOM roots in the document.",
      required: !1,
      defaultValue: !1
    }),
    follow_redirect: import_pieces_framework2.Property.Checkbox({
      displayName: "Follow Redirect",
      description: "Choose whether to resolve to the final destination URL after following all redirects. Enable to follow the full redirect chain.",
      required: !1,
      defaultValue: !1
    }),
    eu_compliance: import_pieces_framework2.Property.Checkbox({
      displayName: "EU Compliance",
      description: "All infrastructure and data processing operations reside entirely within EU jurisdiction.",
      required: !1,
      defaultValue: !1
    }),
    json_response: import_pieces_framework2.Property.Checkbox({
      displayName: "JSON Response",
      description: "Return response in JSON format containing URL, title, content, and timestamp (if available).",
      required: !1,
      defaultValue: !1
    }),
    timeout_seconds: import_pieces_framework2.Property.Number({
      displayName: "Timeout Seconds",
      description: "Maximum page load wait time in seconds (0 means use default timeout, set a value if the default browser engine is too slow on simple webpages).",
      required: !1,
      defaultValue: 0
    }),
    css_selector: import_pieces_framework2.Property.LongText({
      displayName: "CSS Selector: Only",
      description: 'List of CSS selectors to target specific page elements (e.g., "body, .main-content, #article").',
      required: !1
    }),
    wait_for_selector: import_pieces_framework2.Property.LongText({
      displayName: "CSS Selector: Wait-For",
      description: 'CSS selectors to wait for before returning results (e.g., "#dynamic-content, .lazy-loaded").',
      required: !1
    }),
    exclude_selector: import_pieces_framework2.Property.LongText({
      displayName: "CSS Selector: Excluding",
      description: 'CSS selectors for elements to remove from the results (e.g., "header, footer, .ads, #sidebar").',
      required: !1
    })
  },
  async run(context) {
    let {
      url,
      format,
      remove_all_images,
      links_summary,
      images_summary,
      do_not_track,
      iframe_extraction,
      shadow_dom_extraction,
      follow_redirect,
      eu_compliance,
      json_response,
      timeout_seconds,
      css_selector,
      wait_for_selector,
      exclude_selector
    } = context.propsValue, { auth: apiKey } = context, finalUrl = `${eu_compliance ? JinaAICommon.euReaderUrl : JinaAICommon.readerUrl}/${encodeURIComponent(url)}`, headers = {};
    return format && format !== "default" && (headers["X-Return-Format"] = format), json_response && (headers.Accept = "application/json"), timeout_seconds && timeout_seconds > 0 && (headers["X-Timeout"] = timeout_seconds.toString()), css_selector && (headers["X-Target-Selector"] = css_selector), wait_for_selector && (headers["X-Wait-For-Selector"] = wait_for_selector), exclude_selector && (headers["X-Remove-Selector"] = exclude_selector), remove_all_images && (headers["X-Retain-Images"] = "none"), links_summary && links_summary !== "none" && (headers["X-With-Links-Summary"] = links_summary), images_summary && images_summary !== "none" && (headers["X-With-Images-Summary"] = images_summary), do_not_track && (headers.DNT = "1"), iframe_extraction && (headers["X-With-Iframe"] = "true"), shadow_dom_extraction && (headers["X-With-Shadow-Dom"] = "true"), follow_redirect && (headers["X-Base"] = "final"), await JinaAICommon.makeRequest({
      url: finalUrl,
      method: import_pieces_common2.HttpMethod.GET,
      auth: apiKey.secret_text,
      body: void 0,
      headers
    });
  }
});

// ../ap/packages/pieces/community/jina-ai/src/lib/actions/web-search-summarization.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var webSearchSummarizationAction = (0, import_pieces_framework3.createAction)({
  auth: jinaAiAuth,
  name: "web_search_summarization",
  classification: "SEARCH",
  displayName: "Web Search Summarization",
  description: "Perform a web search and retrieve summarized results using the Reader API.",
  audience: "both",
  aiMetadata: {
    description: "Run a web search through the Jina Reader search API and return the result list, optionally visiting every hit to include full page content; supports country/language/location targeting, in-site (single-domain) search, and pagination. Pick this to discover pages by keyword \u2014 use Extract Webpage Content for a known URL or DeepSearch Query for multi-step research questions. Read-only search, so repeat calls are idempotent.",
    idempotent: !0
  },
  props: {
    query: import_pieces_framework3.Property.ShortText({
      displayName: "Query",
      description: "The search query to perform.",
      required: !0
    }),
    read_full_content: import_pieces_framework3.Property.Checkbox({
      displayName: "Read Full Content of SERP",
      description: "Visit every URL in the search result and return the full content using Reader. Toggle on to enable more Reader-specific options.",
      required: !1,
      defaultValue: !1
    }),
    json_response: import_pieces_framework3.Property.Checkbox({
      displayName: "JSON Response",
      description: "The response will be in JSON format, containing the URL, title, content, and timestamp (if available). In Search mode, it returns a list of five entries, each following the described JSON structure.",
      required: !1,
      defaultValue: !1
    }),
    fetch_favicons: import_pieces_framework3.Property.Checkbox({
      displayName: "Fetch Favicons",
      description: "This will fetch the favicon of each URL in the SERP and include them in the response as image URI, useful for UI rendering.",
      required: !1,
      defaultValue: !1
    }),
    preferred_country: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Preferred Country",
      description: "The country to use for the search. It's a two-letter country code.",
      required: !1,
      defaultValue: "Default",
      options: {
        options: [
          { label: "Default", value: "Default" },
          { label: "United States", value: "US" },
          { label: "Canada", value: "CA" },
          { label: "Mexico", value: "MX" },
          { label: "United Kingdom", value: "GB" },
          { label: "Germany", value: "DE" },
          { label: "France", value: "FR" },
          { label: "Japan", value: "JP" },
          { label: "China", value: "CN" },
          { label: "India", value: "IN" },
          { label: "Brazil", value: "BR" },
          { label: "Australia", value: "AU" },
          { label: "Italy", value: "IT" },
          { label: "Spain", value: "ES" },
          { label: "South Korea", value: "KR" },
          { label: "Netherlands", value: "NL" },
          { label: "Switzerland", value: "CH" },
          { label: "Sweden", value: "SE" },
          { label: "Ireland", value: "IE" },
          { label: "Singapore", value: "SG" },
          { label: "Israel", value: "IL" },
          { label: "Saudi Arabia", value: "SA" },
          { label: "South Africa", value: "ZA" },
          { label: "United Arab Emirates", value: "AE" }
        ]
      }
    }),
    preferred_location: import_pieces_framework3.Property.ShortText({
      displayName: "Preferred Location",
      description: "From where you want the search query to originate. It is recommended to specify location at the city level in order to simulate a real user's search.",
      required: !1
    }),
    preferred_language: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Preferred Language",
      description: "The language to use for the search. It's a two-letter language code.",
      required: !1,
      defaultValue: "Default",
      options: {
        options: [
          { label: "Default", value: "Default" },
          { label: "English", value: "en" },
          { label: "Spanish", value: "es" },
          { label: "French", value: "fr" },
          { label: "German", value: "de" },
          { label: "Japanese", value: "ja" },
          { label: "Chinese", value: "zh-cn" },
          { label: "Hindi", value: "hi" },
          { label: "Portuguese", value: "pt" },
          { label: "Italian", value: "it" },
          { label: "Korean", value: "ko" },
          { label: "Dutch", value: "nl" },
          { label: "Arabic", value: "ar" },
          { label: "Swedish", value: "sv" },
          { label: "Hebrew", value: "iw" },
          { label: "Afrikaans", value: "af" }
        ]
      }
    }),
    pagination: import_pieces_framework3.Property.Number({
      displayName: "Pagination",
      description: "The result offset. It skips the given number of results. It's used for pagination.",
      required: !1,
      defaultValue: 1
    }),
    in_site_search: import_pieces_framework3.Property.ShortText({
      displayName: "In-site Search",
      description: "Returns the search results only from the specified website or domain. By default it searches the entire web.",
      required: !1
    }),
    bypass_cached_content: import_pieces_framework3.Property.Checkbox({
      displayName: "Bypass Cached Content",
      description: "Our API caches URL contents for a certain amount of time. Set it to true to ignore the cached result and fetch the content from the URL directly.",
      required: !1,
      defaultValue: !1
    }),
    eu_compliance: import_pieces_framework3.Property.Checkbox({
      displayName: "EU Compliance",
      description: "All infrastructure and data processing operations reside entirely within EU jurisdiction.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let {
      query,
      read_full_content,
      json_response,
      fetch_favicons,
      preferred_country,
      preferred_location,
      preferred_language,
      pagination,
      in_site_search,
      bypass_cached_content,
      eu_compliance
    } = context.propsValue, { auth: apiKey } = context, queryParams = new URLSearchParams();
    queryParams.append("q", query), pagination && pagination > 1 && queryParams.append("page", pagination.toString()), preferred_country && preferred_country !== "Default" && queryParams.append("gl", preferred_country), preferred_language && preferred_language !== "Default" && queryParams.append("hl", preferred_language), preferred_location && queryParams.append("location", preferred_location);
    let finalUrl = `${eu_compliance ? JinaAICommon.euReaderSearchUrl : JinaAICommon.readerSearchUrl}/?${queryParams.toString()}`, headers = {};
    return json_response && (headers.Accept = "application/json"), read_full_content ? headers["X-Engine"] = "direct" : headers["X-Respond-With"] = "no-content", fetch_favicons && (headers["X-With-Favicons"] = "true"), bypass_cached_content && (headers["X-No-Cache"] = "true"), in_site_search && (headers["X-Site"] = in_site_search), await JinaAICommon.makeRequest({
      url: finalUrl,
      method: import_pieces_common3.HttpMethod.GET,
      auth: apiKey.secret_text,
      headers
    });
  }
});

// ../ap/packages/pieces/community/jina-ai/src/lib/actions/deepsearch-query.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var deepSearchQueryAction = (0, import_pieces_framework4.createAction)({
  auth: jinaAiAuth,
  name: "deepsearch_query",
  classification: "READ",
  displayName: "DeepSearch Query",
  description: "Answer complex questions through iterative search, reading, and reasoning with the DeepSearch API.",
  audience: "both",
  aiMetadata: {
    description: "Answer a complex research question with Jina DeepSearch, which iteratively searches the web, reads pages, and reasons before responding; supports domain include/exclude lists, token budget, and an optional JSON schema for structured output. Pick this for multi-hop research questions rather than a plain keyword search (use Web Search Summarization for that); it is slower and more token-hungry than a single search. Read-only, so safe to retry, though answers may vary between runs.",
    idempotent: !0
  },
  props: {
    model: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Model",
      required: !0,
      defaultValue: "jina-deepsearch-v1",
      options: {
        options: [{ label: "jina-deepsearch-v1", value: "jina-deepsearch-v1" }]
      }
    }),
    prompt: import_pieces_framework4.Property.LongText({
      displayName: "Prompt",
      required: !0
    }),
    reasoning_effort: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Reasoning Effort",
      description: "Constrains effort on reasoning for reasoning models. Reducing reasoning effort can result in faster responses and fewer tokens used on reasoning in a response.",
      required: !1,
      defaultValue: "medium",
      options: {
        options: [
          { label: "Low", value: "low" },
          { label: "Medium", value: "medium" },
          { label: "High", value: "high" }
        ]
      }
    }),
    budget_tokens: import_pieces_framework4.Property.Number({
      displayName: "Budget Tokens",
      description: "This determines the maximum number of tokens allowed for DeepSearch process. Larger budgets can improve response quality by enabling more exhaustive search for complex queries.",
      required: !1,
      defaultValue: 0
    }),
    max_attempts: import_pieces_framework4.Property.Number({
      displayName: "Max Attempts",
      description: "The maximum number of retries for solving a problem in DeepSearch process. A larger value allows DeepSearch to retry solving the problem by using different reasoning approaches and strategies.",
      required: !1,
      defaultValue: 1
    }),
    no_direct_answer: import_pieces_framework4.Property.Checkbox({
      displayName: "No Direct Answer",
      description: "Forces the model to take further thinking/search steps even when the query seems trivial. Useful if you're using DeepSearch in scenarios where you're certain the query always needs DeepSearch, rather than for trivial questions.",
      required: !1,
      defaultValue: !1
    }),
    max_returned_urls: import_pieces_framework4.Property.Number({
      displayName: "Max Returned URLs",
      description: "The maximum number of URLs to include in the final answer/chunk. URLs are sorted by relevance and other important factors.",
      required: !1,
      defaultValue: 1
    }),
    response_format: import_pieces_framework4.Property.Json({
      displayName: "Structured Output",
      description: 'JSON schema for structured output format. Example: { "type": "json_schema", "json_schema": { "type": "object", "properties": { "numerical_answer_only": { "type": "number" } } } }',
      required: !1
    }),
    boost_hostnames: import_pieces_framework4.Property.LongText({
      displayName: "Good Domains",
      description: "A list of domains that are given a higher priority for content retrieval. Useful for domain-specific, high-quality sources that provide valuable content.",
      required: !1
    }),
    bad_hostnames: import_pieces_framework4.Property.LongText({
      displayName: "Bad Domains",
      description: "A list of domains to be strictly excluded from content retrieval. Typically used to filter out known spam, low-quality, or irrelevant websites.",
      required: !1
    }),
    only_hostnames: import_pieces_framework4.Property.LongText({
      displayName: "Only Domains",
      description: "A list of domains to be exclusively included in content retrieval. All other domains will be ignored. Useful for domain-specific searches.",
      required: !1
    })
  },
  async run(context) {
    let {
      model,
      reasoning_effort,
      budget_tokens,
      max_attempts,
      no_direct_answer,
      max_returned_urls,
      response_format,
      boost_hostnames,
      bad_hostnames,
      only_hostnames,
      prompt
    } = context.propsValue, { auth: apiKey } = context, requestBody = {
      model: model || "jina-deepsearch-v1"
    };
    if (requestBody.messages = [
      {
        role: "user",
        content: "Hi!"
      },
      {
        role: "assistant",
        content: "Hi, how can I help you?"
      },
      {
        role: "user",
        content: prompt
      }
    ], reasoning_effort && (requestBody.reasoning_effort = reasoning_effort), budget_tokens && (requestBody.budget_tokens = budget_tokens), max_attempts && (requestBody.max_attempts = max_attempts), no_direct_answer !== void 0 && (requestBody.no_direct_answer = no_direct_answer), max_returned_urls && (requestBody.max_returned_urls = max_returned_urls), response_format)
      try {
        let parsedFormat = typeof response_format == "string" ? JSON.parse(response_format) : response_format;
        requestBody.response_format = parsedFormat;
      } catch (error) {
      }
    return boost_hostnames && (requestBody.boost_hostnames = boost_hostnames.split(`
`).map((domain) => domain.trim()).filter((domain) => domain)), bad_hostnames && (requestBody.bad_hostnames = bad_hostnames.split(`
`).map((domain) => domain.trim()).filter((domain) => domain)), only_hostnames && (requestBody.only_hostnames = only_hostnames.split(`
`).map((domain) => domain.trim()).filter((domain) => domain)), (await JinaAICommon.makeRequest({
      url: JinaAICommon.deepsearchUrl,
      method: import_pieces_common4.HttpMethod.POST,
      auth: apiKey.secret_text,
      body: requestBody
    })).choices[0].message.content;
  }
});

// ../ap/packages/pieces/community/jina-ai/src/lib/actions/classify-content.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var classifyContentAction = (0, import_pieces_framework5.createAction)({
  auth: jinaAiAuth,
  name: "classify_content",
  classification: "READ",
  displayName: "Classify Text or Image",
  description: "Assign categories to text or images using the Classifier API (zero-shot/few-shot).",
  audience: "both",
  aiMetadata: {
    description: "Classify a single piece of text or an image URL into one of a provided set of labels using Jina AI zero-shot classification; returns the winning label. Pick this to categorize content against your own label set without training; if the input string is a URL it is treated as an image. Stateless inference, so repeat calls are idempotent. To fine-tune a reusable classifier on labeled examples instead, use Train Custom Classifier.",
    idempotent: !0
  },
  props: {
    model: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Model",
      description: "The model to use for classification.",
      required: !0,
      defaultValue: "jina-clip-v2",
      options: {
        options: [
          {
            label: "jina-clip-v2 - Multilingual multimodal embeddings for texts and images",
            value: "jina-clip-v2"
          },
          {
            label: "jina-embeddings-v3 - Frontier multilingual embedding model with SOTA performance",
            value: "jina-embeddings-v3"
          },
          {
            label: "jina-clip-v1 - Multimodal embedding models for images and English text",
            value: "jina-clip-v1"
          }
        ]
      }
    }),
    input: import_pieces_framework5.Property.LongText({
      displayName: "Text",
      description: "Text or image URL to classify. URLs will be treated as images, other strings as text.",
      required: !0
    }),
    labels: import_pieces_framework5.Property.Array({
      displayName: "Labels",
      description: "The labels to classify the content into.",
      required: !0
    })
  },
  async run(context) {
    let { model, input, labels } = context.propsValue, { auth: apiKey } = context;
    if (!input || input === "")
      throw new Error("Text input is required.");
    if (!labels || !Array.isArray(labels) || labels.length === 0)
      throw new Error("At least one label must be provided.");
    let inputArray = [
      !!input.trim().match(/^(https?|ftp|file|data):\/\/.+/i) ? { image: input } : { text: input }
    ];
    if (inputArray.length === 0)
      throw new Error("No valid inputs provided.");
    let requestBody = {
      model: model || "jina-clip-v2",
      input: inputArray,
      labels
    };
    return {
      label: (await JinaAICommon.makeRequest({
        url: JinaAICommon.classifierUrl,
        method: import_pieces_common5.HttpMethod.POST,
        auth: apiKey.secret_text,
        body: requestBody
      })).data[0].prediction
    };
  }
});

// ../ap/packages/pieces/community/jina-ai/src/lib/actions/train-custom-classifier.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var trainCustomClassifierAction = (0, import_pieces_framework6.createAction)({
  auth: jinaAiAuth,
  name: "train_custom_classifier",
  classification: "WRITE",
  displayName: "Train Custom Classifier",
  description: "Fine-tune a classifier with labeled examples for domain-specific tasks.",
  audience: "both",
  aiMetadata: {
    description: "Train a custom Jina AI classifier from an array of labeled text or image-URL examples, choosing the base embedding model, iteration count, and private/public visibility. Pick this only when zero-shot labeling (Classify Text or Image) is not accurate enough and you have labeled training data. Each call creates a new training job/classifier, so repeated calls are not idempotent.",
    idempotent: !1
  },
  props: {
    model: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Model",
      description: "The base model to use for training.",
      required: !0,
      defaultValue: "jina-clip-v2",
      options: {
        options: [
          {
            label: "jina-clip-v2 - Multilingual multimodal embeddings for texts and images",
            value: "jina-clip-v2"
          },
          {
            label: "jina-embeddings-v3 - Frontier multilingual embedding model with SOTA performance",
            value: "jina-embeddings-v3"
          },
          {
            label: "jina-clip-v1 - Multimodal embedding models for images and English text",
            value: "jina-clip-v1"
          }
        ]
      }
    }),
    access: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Access Level",
      description: "Visibility of the trained model.",
      required: !0,
      defaultValue: "private",
      options: {
        options: [
          { label: "Private", value: "private" },
          { label: "Public", value: "public" }
        ]
      }
    }),
    num_iters: import_pieces_framework6.Property.Number({
      displayName: "Number of Iterations",
      description: "Number of training iterations to perform.",
      required: !1,
      defaultValue: 10
    }),
    training_data: import_pieces_framework6.Property.Array({
      displayName: "Training Data",
      required: !0,
      properties: {
        type: import_pieces_framework6.Property.StaticDropdown({
          displayName: "Input Type",
          description: "Type of input either text or image URL.",
          required: !0,
          defaultValue: "text",
          options: {
            disabled: !1,
            options: [
              { label: "Text", value: "text" },
              { label: "Image", value: "image" }
            ]
          }
        }),
        input: import_pieces_framework6.Property.LongText({
          displayName: "Input",
          required: !0
        }),
        label: import_pieces_framework6.Property.ShortText({
          displayName: "Label",
          required: !0,
          description: "Label to associate with input."
        })
      }
    })
  },
  async run(context) {
    let { model, access, num_iters, training_data } = context.propsValue, { auth: apiKey } = context, parsedTrainingData = [];
    try {
      parsedTrainingData = typeof training_data == "string" ? JSON.parse(training_data) : training_data != null ? training_data : [];
    } catch (error) {
      throw new Error(
        "Invalid training data format. Must be a valid JSON array of labeled examples."
      );
    }
    if (!Array.isArray(parsedTrainingData) || parsedTrainingData.length === 0)
      throw new Error(
        "Training data must be a non-empty array of labeled examples, you can check an example at https://jina.ai/api-dashboard/classifier"
      );
    let trainingInput = parsedTrainingData.map((example) => {
      let { type, label, input } = example;
      if (!label)
        throw new Error('Each training example must have a "label" field.');
      if (!input)
        throw new Error(
          'Each training example must include an "input" value for either text or image.'
        );
      return type === "text" ? { label, text: input } : { label, image: input };
    }), requestBody = {
      model: model || "jina-clip-v2",
      access: access || "private",
      num_iters: num_iters || 10,
      input: trainingInput
    };
    return await JinaAICommon.makeRequest({
      url: JinaAICommon.classifierTrainUrl,
      method: import_pieces_common6.HttpMethod.POST,
      auth: apiKey.secret_text,
      body: requestBody
    });
  }
});

// ../ap/packages/pieces/community/jina-ai/src/index.ts
var jinaAi = (0, import_pieces_framework7.createPiece)({
  displayName: "Jina AI",
  description: "AI-powered web content extraction, search, and classification",
  auth: jinaAiAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/jinaai.jpeg",
  categories: [import_pieces_framework8.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["denieler"],
  actions: [
    extractWebpageContentAction,
    webSearchSummarizationAction,
    deepSearchQueryAction,
    classifyContentAction,
    trainCustomClassifierAction
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  jinaAi
});
