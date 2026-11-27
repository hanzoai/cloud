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

// ../ap/packages/pieces/community/writesonic-bulk/src/index.ts
var index_exports = {};
__export(index_exports, {
  writesonicBulk: () => writesonicBulk
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework13 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/writesonic-bulk/src/lib/actions/blog-ideas.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/writesonic-bulk/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), writesonicBulkAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Writesonic Bulk API Key",
  description: `
To get your API Key:

1. Go to [app.writesonic.com](https://app.writesonic.com/)
2. Login to your Writesonic account
3. Hover over your display picture on the top-right corner to open the profile menu
4. Click on "API Dashboard"
5. Click on the switch to activate the API
6. Click on "Reveal API Key"
7. Copy and save your API key securely (never commit to public repositories)
`,
  required: !0
});

// ../ap/packages/pieces/community/writesonic-bulk/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), engineDropdownOptions = import_pieces_framework2.Property.StaticDropdown({
  displayName: "Engine",
  description: "Select the engine quality level",
  required: !0,
  defaultValue: "economy",
  options: {
    options: [
      { label: "Economy", value: "economy" },
      { label: "Average", value: "average" },
      { label: "Good", value: "good" },
      { label: "Premium", value: "premium" }
    ]
  }
}), languageDropdownOptions = import_pieces_framework2.Property.StaticDropdown({
  displayName: "Language",
  description: "Select the language",
  required: !0,
  defaultValue: "en",
  options: {
    options: [
      { label: "English", value: "en" },
      { label: "Dutch", value: "nl" },
      { label: "French", value: "fr" },
      { label: "German", value: "de" },
      { label: "Italian", value: "it" },
      { label: "Polish", value: "pl" },
      { label: "Spanish", value: "es" },
      { label: "Portuguese (Portugal)", value: "pt-pt" },
      { label: "Portuguese (Brazil)", value: "pt-br" },
      { label: "Russian", value: "ru" },
      { label: "Japanese", value: "ja" },
      { label: "Chinese", value: "zh" },
      { label: "Bulgarian", value: "bg" },
      { label: "Czech", value: "cs" },
      { label: "Danish", value: "da" },
      { label: "Greek", value: "el" },
      { label: "Hungarian", value: "hu" },
      { label: "Lithuanian", value: "lt" },
      { label: "Latvian", value: "lv" },
      { label: "Romanian", value: "ro" },
      { label: "Slovak", value: "sk" },
      { label: "Slovenian", value: "sl" },
      { label: "Swedish", value: "sv" },
      { label: "Finnish", value: "fi" },
      { label: "Estonian", value: "et" }
    ]
  }
}), toneofvoiceDropdown = import_pieces_framework2.Property.StaticDropdown({
  displayName: "Tone of Voice",
  description: "Select the tone of voice for the content",
  required: !1,
  defaultValue: "excited",
  options: {
    options: [
      { label: "Excited", value: "excited" },
      { label: "Professional", value: "professional" },
      { label: "Funny", value: "funny" },
      { label: "Encouraging", value: "encouraging" },
      { label: "Dramatic", value: "dramatic" },
      { label: "Witty", value: "witty" },
      { label: "Sarcastic", value: "sarcastic" },
      { label: "Engaging", value: "engaging" },
      { label: "Creative", value: "creative" }
    ]
  }
});

// ../ap/packages/pieces/community/writesonic-bulk/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.writesonic.com/v2/business";
async function makeRequest(api_key, method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        "X-API-KEY": `${api_key.secret_text}`,
        "Content-Type": "application/json"
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/writesonic-bulk/src/lib/actions/blog-ideas.ts
var blogIdeas = (0, import_pieces_framework3.createAction)({
  auth: writesonicBulkAuth,
  name: "blogIdeas",
  displayName: "Blog Ideas",
  description: "Generate blog article ideas based on a topic or keyword",
  audience: "both",
  aiMetadata: { description: "Generates AI-written blog article ideas for a given topic or keyword via Writesonic, optionally biased toward an extra keyword and engine/language/copy-count settings. Use when an agent needs candidate blog headlines or angles to brainstorm content. Each call produces fresh generated text and is billed, so it is not idempotent.", idempotent: !1 },
  props: {
    topic: import_pieces_framework3.Property.ShortText({
      displayName: "Topic",
      description: "The topic or keyword to generate blog ideas for",
      required: !0
    }),
    keyword: import_pieces_framework3.Property.ShortText({
      displayName: "Keyword",
      description: "A keyword to include in the blog introduction",
      required: !1
    }),
    engine: engineDropdownOptions,
    language: languageDropdownOptions,
    num_copies: import_pieces_framework3.Property.Number({
      displayName: "Number of Copies",
      description: "Number of blog ideas to generate (1-5)",
      required: !0,
      defaultValue: 1
    })
  },
  async run(context) {
    let payload = {
      topic: context.propsValue.topic
    };
    context.propsValue.keyword && (payload.keyword = context.propsValue.keyword);
    let queryParams = new URLSearchParams({
      engine: context.propsValue.engine,
      language: context.propsValue.language,
      num_copies: context.propsValue.num_copies.toString()
    });
    return await makeRequest(
      context.auth,
      import_pieces_common2.HttpMethod.POST,
      `/content/blog-ideas?${queryParams.toString()}`,
      payload
    );
  }
});

// ../ap/packages/pieces/community/writesonic-bulk/src/lib/actions/blog-intros.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var blogIntros = (0, import_pieces_framework4.createAction)({
  auth: writesonicBulkAuth,
  name: "blogIntros",
  displayName: "Blog Intros",
  description: "Generate enticing blog article introductions",
  audience: "both",
  aiMetadata: { description: "Generates AI-written blog article introductions from a blog title or topic via Writesonic, controlled by engine/language/copy-count settings. Use when an agent needs an opening paragraph to kick off a blog post. Each call produces fresh generated text and is billed, so it is not idempotent.", idempotent: !1 },
  props: {
    blog_title: import_pieces_framework4.Property.ShortText({
      displayName: "Blog title",
      description: "The blog topic or title to generate an introduction for",
      required: !0
    }),
    engine: engineDropdownOptions,
    language: languageDropdownOptions,
    num_copies: import_pieces_framework4.Property.Number({
      displayName: "Number of Copies",
      description: "Number of blog ideas to generate (1-5)",
      required: !0,
      defaultValue: 1
    })
  },
  async run(context) {
    let payload = {
      blog_title: context.propsValue.blog_title
    }, queryParams = new URLSearchParams({
      engine: context.propsValue.engine,
      language: context.propsValue.language,
      num_copies: context.propsValue.num_copies.toString()
    });
    return await makeRequest(
      context.auth,
      import_pieces_common3.HttpMethod.POST,
      `/content/blog-intros?${queryParams.toString()}`,
      payload
    );
  }
});

// ../ap/packages/pieces/community/writesonic-bulk/src/lib/actions/blog-outlines.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var blogOutlines = (0, import_pieces_framework5.createAction)({
  auth: writesonicBulkAuth,
  name: "blogOutlines",
  displayName: "Blog Outlines",
  description: "Generate detailed article outlines for better content writing",
  audience: "both",
  aiMetadata: { description: "Generates a detailed AI-written blog outline via Writesonic from a blog title plus an existing introduction (both required), controlled by engine/language/copy-count settings. Use when an agent needs a structured section breakdown to draft a full article. Each call produces fresh generated text and is billed, so it is not idempotent.", idempotent: !1 },
  props: {
    blog_title: import_pieces_framework5.Property.ShortText({
      displayName: "Blog Title",
      description: "The blog topic or title to generate an outline for",
      required: !0
    }),
    blog_intro: import_pieces_framework5.Property.LongText({
      displayName: "Blog Introduction",
      description: "The introduction of the blog to base the outline on",
      required: !0
    }),
    engine: engineDropdownOptions,
    language: languageDropdownOptions,
    num_copies: import_pieces_framework5.Property.Number({
      displayName: "Number of Copies",
      description: "Number of blog ideas to generate (1-5)",
      required: !0,
      defaultValue: 1
    })
  },
  async run(context) {
    let payload = {
      blog_title: context.propsValue.blog_title,
      blog_intro: context.propsValue.blog_intro
    }, queryParams = new URLSearchParams({
      engine: context.propsValue.engine,
      language: context.propsValue.language,
      num_copies: context.propsValue.num_copies.toString()
    });
    return await makeRequest(
      context.auth,
      import_pieces_common4.HttpMethod.POST,
      `/content/blog-outlines?${queryParams.toString()}`,
      payload
    );
  }
});

// ../ap/packages/pieces/community/writesonic-bulk/src/lib/actions/content-rephraser.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var contentRephraser = (0, import_pieces_framework6.createAction)({
  auth: writesonicBulkAuth,
  name: "contentRephraser",
  displayName: "Content Rephraser",
  description: "Rephrase content in a different voice and style to appeal to different readers",
  audience: "both",
  aiMetadata: { description: "Rewrites supplied text into a different voice and style via Writesonic, with an optional tone-of-voice setting and engine/language/copy-count controls. Use when an agent needs to reword existing content rather than generate from a topic. Each call produces fresh generated text and is billed, so it is not idempotent.", idempotent: !1 },
  props: {
    content_to_rephrase: import_pieces_framework6.Property.LongText({
      displayName: "Original Content",
      description: "The content you want to rephrase",
      required: !0
    }),
    tone_of_voice: toneofvoiceDropdown,
    engine: engineDropdownOptions,
    language: languageDropdownOptions,
    num_copies: import_pieces_framework6.Property.Number({
      displayName: "Number of Copies",
      description: "Number of blog ideas to generate (1-5)",
      required: !0,
      defaultValue: 1
    })
  },
  async run(context) {
    let payload = {
      content_to_rephrase: context.propsValue.content_to_rephrase
    };
    context.propsValue.tone_of_voice && (payload.tone_of_voice = context.propsValue.tone_of_voice);
    let queryParams = new URLSearchParams({
      engine: context.propsValue.engine,
      language: context.propsValue.language,
      num_copies: context.propsValue.num_copies.toString()
    });
    return await makeRequest(
      context.auth,
      import_pieces_common5.HttpMethod.POST,
      `/content/content-rephrases?${queryParams.toString()}`,
      payload
    );
  }
});

// ../ap/packages/pieces/community/writesonic-bulk/src/lib/actions/content-shorten.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var contentShorten = (0, import_pieces_framework7.createAction)({
  auth: writesonicBulkAuth,
  name: "contentShorten",
  displayName: "Content Shorten",
  description: "Shorten your content in a different voice and style to appeal to different readers",
  audience: "both",
  aiMetadata: { description: "Condenses supplied text into a shorter version via Writesonic, controlled by engine/language/copy-count settings. Use when an agent needs to trim or compress existing content rather than expand or generate it. Each call produces fresh generated text and is billed, so it is not idempotent.", idempotent: !1 },
  props: {
    content_to_shorten: import_pieces_framework7.Property.LongText({
      displayName: "Original Content",
      description: "The content you want to shorten",
      required: !0
    }),
    engine: engineDropdownOptions,
    language: languageDropdownOptions,
    num_copies: import_pieces_framework7.Property.Number({
      displayName: "Number of Copies",
      description: "Number of blog ideas to generate (1-5)",
      required: !0,
      defaultValue: 1
    })
  },
  async run(context) {
    let payload = {
      content_to_shorten: context.propsValue.content_to_shorten
    }, queryParams = new URLSearchParams({
      engine: context.propsValue.engine,
      language: context.propsValue.language,
      num_copies: context.propsValue.num_copies.toString()
    });
    return await makeRequest(
      context.auth,
      import_pieces_common6.HttpMethod.POST,
      `/content/content-shorten?${queryParams.toString()}`,
      payload
    );
  }
});

// ../ap/packages/pieces/community/writesonic-bulk/src/lib/actions/facebook-ads.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var facebookAds = (0, import_pieces_framework8.createAction)({
  auth: writesonicBulkAuth,
  name: "facebookAds",
  displayName: "Facebook Ads",
  description: "Generate Facebook ad copies that make your ads truly stand out",
  audience: "both",
  aiMetadata: { description: "Generates AI-written Facebook ad copy via Writesonic from a product name, with optional description, occasion, and promotion details plus engine/language/copy-count controls. Use when an agent needs social ad creative for a product. Each call produces fresh generated text and is billed, so it is not idempotent.", idempotent: !1 },
  props: {
    product_name: import_pieces_framework8.Property.ShortText({
      displayName: "Product Name",
      description: "The name of the product or service to advertise",
      required: !0
    }),
    product_description: import_pieces_framework8.Property.LongText({
      displayName: "Product Description",
      description: "A brief description of the product or service",
      required: !1
    }),
    occasion: import_pieces_framework8.Property.ShortText({
      displayName: "Occasion",
      description: 'The occasion for the ad (e.g.,"Black Friday", holiday, sale event)',
      required: !1
    }),
    promotion: import_pieces_framework8.Property.ShortText({
      displayName: "Promotion",
      description: "Details about any promotion or discount",
      required: !1
    }),
    engine: engineDropdownOptions,
    language: languageDropdownOptions,
    num_copies: import_pieces_framework8.Property.Number({
      displayName: "Number of Copies",
      description: "Number of blog ideas to generate (1-5)",
      required: !0,
      defaultValue: 1
    })
  },
  async run(context) {
    let payload = {
      product_name: context.propsValue.product_name,
      product_description: context.propsValue.product_description,
      occasion: context.propsValue.occasion,
      promotion: context.propsValue.promotion
    }, queryParams = new URLSearchParams({
      engine: context.propsValue.engine,
      language: context.propsValue.language,
      num_copies: context.propsValue.num_copies.toString()
    });
    return await makeRequest(
      context.auth,
      import_pieces_common7.HttpMethod.POST,
      `/content/facebook-ads?${queryParams.toString()}`,
      payload
    );
  }
});

// ../ap/packages/pieces/community/writesonic-bulk/src/lib/actions/generate-product-descriptions.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var generateProductDescriptions = (0, import_pieces_framework9.createAction)({
  auth: writesonicBulkAuth,
  name: "generateProductDescriptions",
  displayName: "Generate Product Descriptions",
  description: "Generate authentic product descriptions that compel, inspire, and influence",
  audience: "both",
  aiMetadata: { description: "Generates AI-written product descriptions via Writesonic from a product name and its key characteristics (both required), with optional primary/secondary keywords, tone of voice, and engine/language/copy-count controls. Use when an agent needs ecommerce or catalog copy for a product. Each call produces fresh generated text and is billed, so it is not idempotent.", idempotent: !1 },
  props: {
    product_name: import_pieces_framework9.Property.ShortText({
      displayName: "Product Name",
      description: "The name of the product",
      required: !0
    }),
    product_characteristics: import_pieces_framework9.Property.LongText({
      displayName: "Product Characteristics",
      description: "Key characteristics and specifications of the product",
      required: !0
    }),
    primary_keyword: import_pieces_framework9.Property.ShortText({
      displayName: "Primary Keyword",
      description: "The main keyword to include in the product description",
      required: !1
    }),
    secondary_keywords: import_pieces_framework9.Property.LongText({
      displayName: "Secondary Keywords",
      description: "Additional keywords to include in the product description, separated by commas",
      required: !1
    }),
    tone_of_voice: toneofvoiceDropdown,
    engine: engineDropdownOptions,
    language: languageDropdownOptions,
    num_copies: import_pieces_framework9.Property.Number({
      displayName: "Number of Copies",
      description: "Number of blog ideas to generate (1-5)",
      required: !0,
      defaultValue: 1
    })
  },
  async run(context) {
    let payload = {
      product_name: context.propsValue.product_name,
      product_characteristics: context.propsValue.product_characteristics
    };
    context.propsValue.primary_keyword && (payload.primary_keyword = context.propsValue.primary_keyword), context.propsValue.secondary_keywords && (payload.secondary_keywords = context.propsValue.secondary_keywords), context.propsValue.tone_of_voice && (payload.tone_of_voice = context.propsValue.tone_of_voice);
    let queryParams = new URLSearchParams({
      engine: context.propsValue.engine,
      language: context.propsValue.language,
      num_copies: context.propsValue.num_copies.toString()
    });
    return await makeRequest(
      context.auth,
      import_pieces_common8.HttpMethod.POST,
      `/content/product-descriptions?${queryParams.toString()}`,
      payload
    );
  }
});

// ../ap/packages/pieces/community/writesonic-bulk/src/lib/actions/google-ads.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var googleAds = (0, import_pieces_framework10.createAction)({
  auth: writesonicBulkAuth,
  name: "googleAds",
  displayName: "Google Ads",
  description: "Generate quality ads that rank in search results and drive more traffic",
  audience: "both",
  aiMetadata: { description: "Generates AI-written Google search ad copy via Writesonic from a product name, with optional description and target search term plus engine/language/copy-count controls. Use when an agent needs paid-search ad creative for a product. Each call produces fresh generated text and is billed, so it is not idempotent.", idempotent: !1 },
  props: {
    product_name: import_pieces_framework10.Property.ShortText({
      displayName: "Product Name",
      description: "The name of the product or service to advertise",
      required: !0
    }),
    product_description: import_pieces_framework10.Property.LongText({
      displayName: "Product Description",
      description: "A brief description of the product or service",
      required: !1
    }),
    search_term: import_pieces_framework10.Property.ShortText({
      displayName: "Search Term",
      description: "The search term or query to target in the Google Ads campaign",
      required: !1
    }),
    engine: engineDropdownOptions,
    language: languageDropdownOptions,
    num_copies: import_pieces_framework10.Property.Number({
      displayName: "Number of Copies",
      description: "Number of blog ideas to generate (1-5)",
      required: !0,
      defaultValue: 1
    })
  },
  async run(context) {
    let payload = {
      product_name: context.propsValue.product_name,
      product_description: context.propsValue.product_description,
      search_term: context.propsValue.search_term
    }, queryParams = new URLSearchParams({
      engine: context.propsValue.engine,
      language: context.propsValue.language,
      num_copies: context.propsValue.num_copies.toString()
    });
    return await makeRequest(
      context.auth,
      import_pieces_common9.HttpMethod.POST,
      `/content/google-ads?${queryParams.toString()}`,
      payload
    );
  }
});

// ../ap/packages/pieces/community/writesonic-bulk/src/lib/actions/landing-page-headlines.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var landingPageHeadlines = (0, import_pieces_framework11.createAction)({
  auth: writesonicBulkAuth,
  name: "landingPageHeadlines",
  displayName: "Landing Page Headlines",
  description: "Generate unique and catchy headlines perfect for your product or service",
  audience: "both",
  aiMetadata: { description: "Generates AI-written landing page headlines via Writesonic from a product name, with optional description and engine/language/copy-count controls. Use when an agent needs hero or marketing headlines for a product or service page. Each call produces fresh generated text and is billed, so it is not idempotent.", idempotent: !1 },
  props: {
    product_name: import_pieces_framework11.Property.ShortText({
      displayName: "Product Name",
      description: "The name of the product or service",
      required: !0
    }),
    product_description: import_pieces_framework11.Property.LongText({
      displayName: "Product Description",
      description: "A brief description of the product or service",
      required: !1
    }),
    engine: engineDropdownOptions,
    language: languageDropdownOptions,
    num_copies: import_pieces_framework11.Property.Number({
      displayName: "Number of Copies",
      description: "Number of blog ideas to generate (1-5)",
      required: !0,
      defaultValue: 1
    })
  },
  async run(context) {
    let payload = {
      product_name: context.propsValue.product_name,
      product_description: context.propsValue.product_description
    }, queryParams = new URLSearchParams({
      engine: context.propsValue.engine,
      language: context.propsValue.language,
      num_copies: context.propsValue.num_copies.toString()
    });
    return await makeRequest(
      context.auth,
      import_pieces_common10.HttpMethod.POST,
      `/content/landing-page-headlines?${queryParams.toString()}`,
      payload
    );
  }
});

// ../ap/packages/pieces/community/writesonic-bulk/src/lib/actions/sentence-expander.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var sentenceExpander = (0, import_pieces_framework12.createAction)({
  auth: writesonicBulkAuth,
  name: "sentenceExpander",
  displayName: "Sentence Expander",
  description: "Expand short sentences into more descriptive and interesting ones",
  audience: "both",
  aiMetadata: { description: "Expands a short sentence into more descriptive text via Writesonic, with optional tone of voice and keyword plus engine/language/copy-count controls. Use when an agent needs to elaborate or lengthen a brief sentence rather than shorten or generate from scratch. Each call produces fresh generated text and is billed, so it is not idempotent.", idempotent: !1 },
  props: {
    content_to_expand: import_pieces_framework12.Property.LongText({
      displayName: "Sentence",
      description: "The sentence you want to expand",
      required: !0
    }),
    tone_of_voice: toneofvoiceDropdown,
    keyword: import_pieces_framework12.Property.ShortText({
      displayName: "Keyword",
      description: "A keyword to include in the expanded sentence",
      required: !1
    }),
    engine: engineDropdownOptions,
    language: languageDropdownOptions,
    num_copies: import_pieces_framework12.Property.Number({
      displayName: "Number of Copies",
      description: "Number of blog ideas to generate (1-5)",
      required: !0,
      defaultValue: 1
    })
  },
  async run(context) {
    let payload = {
      content_to_expand: context.propsValue.content_to_expand
    };
    context.propsValue.tone_of_voice && (payload.tone_of_voice = context.propsValue.tone_of_voice), context.propsValue.keyword && (payload.keyword = context.propsValue.keyword);
    let queryParams = new URLSearchParams({
      engine: context.propsValue.engine,
      language: context.propsValue.language,
      num_copies: context.propsValue.num_copies.toString()
    });
    return await makeRequest(
      context.auth,
      import_pieces_common11.HttpMethod.POST,
      `/content/sentence-expand?${queryParams.toString()}`,
      payload
    );
  }
});

// ../ap/packages/pieces/community/writesonic-bulk/src/index.ts
var import_pieces_common12 = require("@activepieces/pieces-common"), import_pieces_framework14 = require("@activepieces/pieces-framework");
var writesonicBulk = (0, import_pieces_framework13.createPiece)({
  displayName: "Writesonic",
  auth: writesonicBulkAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/writesonic-bulk.png",
  authors: ["sanket-a11y"],
  categories: [
    import_pieces_framework14.PieceCategory.ARTIFICIAL_INTELLIGENCE,
    import_pieces_framework14.PieceCategory.CONTENT_AND_FILES
  ],
  description: "Writesonic AI-powered writing assistant",
  actions: [
    blogIdeas,
    blogIntros,
    blogOutlines,
    contentRephraser,
    contentShorten,
    facebookAds,
    generateProductDescriptions,
    googleAds,
    landingPageHeadlines,
    sentenceExpander,
    (0, import_pieces_common12.createCustomApiCallAction)({
      auth: writesonicBulkAuth,
      baseUrl: () => BASE_URL,
      authMapping: async (auth) => ({
        "X-API-KEY": `${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  writesonicBulk
});
