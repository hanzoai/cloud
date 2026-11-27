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
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key != "symbol" ? key + "" : key, value);

// ../ap/packages/pieces/community/serp-api/src/index.ts
var index_exports = {};
__export(index_exports, {
  serpApi: () => serpApi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework23 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/serp-api/src/lib/actions/google-news-search.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/serp-api/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/serp-api/src/lib/types/index.ts
var SerpApiEngine = /* @__PURE__ */ ((SerpApiEngine2) => (SerpApiEngine2.GOOGLE = "google", SerpApiEngine2.GOOGLE_NEWS = "google_news", SerpApiEngine2.YOUTUBE = "youtube", SerpApiEngine2.GOOGLE_TRENDS = "google_trends", SerpApiEngine2.GOOGLE_MAPS = "google_maps", SerpApiEngine2.GOOGLE_SCHOLAR = "google_scholar", SerpApiEngine2.GOOGLE_IMAGES = "google_images", SerpApiEngine2.GOOGLE_SHOPPING = "google_shopping", SerpApiEngine2.GOOGLE_JOBS = "google_jobs", SerpApiEngine2.GOOGLE_LENS = "google_lens", SerpApiEngine2.GOOGLE_LOCAL_SERVICES = "google_local_services", SerpApiEngine2.APPLE_APP_STORE = "apple_app_store", SerpApiEngine2.GOOGLE_PLAY = "google_play", SerpApiEngine2.YELP = "yelp", SerpApiEngine2.WALMART = "walmart", SerpApiEngine2.BING = "bing", SerpApiEngine2.DUCKDUCKGO = "duckduckgo", SerpApiEngine2))(SerpApiEngine || {});

// ../ap/packages/pieces/community/serp-api/src/lib/utils/validators.ts
var VALIDATION_CONSTANTS = {
  MAX_RESULTS: 100,
  MIN_RESULTS: 1,
  MAX_START_POSITION: 990,
  MIN_START_POSITION: 0,
  VALID_DEVICE_TYPES: ["desktop", "mobile", "tablet"],
  VALID_SAFE_SEARCH: ["active", "off"],
  VALID_BOOLEAN_STRINGS: ["true", "false"],
  VALID_FILTER_VALUES: ["0", "1"],
  VALID_UPLOAD_DATE: ["any", "hour", "today", "week", "month", "year"],
  MAX_GOOGLE_DOMAIN_LENGTH: 50
}, SerpApiValidator = class {
  // Validates an API key format and structure
  static validateApiKey(apiKey) {
    let errors = [];
    return apiKey ? typeof apiKey != "string" && errors.push("API key must be a string") : errors.push("API key is required"), {
      isValid: errors.length === 0,
      errors
    };
  }
  /**
   * Validates a search query
   */
  static validateQuery(query) {
    let errors = [];
    return query ? typeof query != "string" ? errors.push("Search query must be a string") : query.trim().length === 0 && errors.push("Search query cannot be empty or only whitespace") : errors.push("Search query is required"), {
      isValid: errors.length === 0,
      errors
    };
  }
  /**
   * Validates language code
   */
  static validateLanguageCode(language) {
    let errors = [];
    return language !== void 0 && (typeof language != "string" ? errors.push("Language code must be a string") : language.length !== 2 ? errors.push("Language code must be exactly 2 characters (ISO 639-1 format)") : /^[a-z]{2}$/.test(language) || errors.push("Language code must contain only lowercase letters")), {
      isValid: errors.length === 0,
      errors
    };
  }
  /**
   * Validates country code
   */
  static validateCountryCode(country) {
    let errors = [];
    return country !== void 0 && (typeof country != "string" ? errors.push("Country code must be a string") : country.length !== 2 ? errors.push("Country code must be exactly 2 characters (ISO 3166-1 alpha-2 format)") : /^[a-z]{2}$/.test(country) || errors.push("Country code must contain only lowercase letters")), {
      isValid: errors.length === 0,
      errors
    };
  }
  /**
   * Validates number of results
   */
  static validateNumResults(num) {
    let errors = [];
    return num !== void 0 && (typeof num != "number" ? errors.push("Number of results must be a number") : Number.isInteger(num) ? num < VALIDATION_CONSTANTS.MIN_RESULTS ? errors.push(`Number of results must be at least ${VALIDATION_CONSTANTS.MIN_RESULTS}`) : num > VALIDATION_CONSTANTS.MAX_RESULTS && errors.push(`Number of results cannot exceed ${VALIDATION_CONSTANTS.MAX_RESULTS}`) : errors.push("Number of results must be an integer")), {
      isValid: errors.length === 0,
      errors
    };
  }
  /**
   * Validates start position for pagination
   */
  static validateStartPosition(start) {
    let errors = [];
    return start !== void 0 && (typeof start != "number" ? errors.push("Start position must be a number") : Number.isInteger(start) ? start < VALIDATION_CONSTANTS.MIN_START_POSITION ? errors.push("Start position cannot be negative") : start > VALIDATION_CONSTANTS.MAX_START_POSITION && errors.push(`Start position cannot exceed ${VALIDATION_CONSTANTS.MAX_START_POSITION}`) : errors.push("Start position must be an integer")), {
      isValid: errors.length === 0,
      errors
    };
  }
  /**
   * Validates device type
   */
  static validateDeviceType(device) {
    let errors = [];
    return device !== void 0 && (typeof device != "string" ? errors.push("Device type must be a string") : VALIDATION_CONSTANTS.VALID_DEVICE_TYPES.includes(device) || errors.push(`Device type must be one of: ${VALIDATION_CONSTANTS.VALID_DEVICE_TYPES.join(", ")}`)), {
      isValid: errors.length === 0,
      errors
    };
  }
  /**
   * Validates safe search setting
   */
  static validateSafeSearch(safe) {
    let errors = [];
    return safe !== void 0 && (typeof safe != "string" ? errors.push("Safe search setting must be a string") : VALIDATION_CONSTANTS.VALID_SAFE_SEARCH.includes(safe) || errors.push(`Safe search must be one of: ${VALIDATION_CONSTANTS.VALID_SAFE_SEARCH.join(", ")}`)), {
      isValid: errors.length === 0,
      errors
    };
  }
  /**
   * Validates location parameter
   */
  static validateLocation(location) {
    let errors = [];
    return location !== void 0 && (typeof location != "string" ? errors.push("Location must be a string") : location.trim().length === 0 && errors.push("Location cannot be empty or only whitespace")), {
      isValid: errors.length === 0,
      errors
    };
  }
  /**
   * Validates Google domain parameter
   */
  static validateGoogleDomain(googleDomain) {
    let errors = [];
    return googleDomain !== void 0 && (typeof googleDomain != "string" ? errors.push("Google domain must be a string") : googleDomain.trim().length === 0 ? errors.push("Google domain cannot be empty or only whitespace") : googleDomain.length > VALIDATION_CONSTANTS.MAX_GOOGLE_DOMAIN_LENGTH ? errors.push(`Google domain is too long (maximum ${VALIDATION_CONSTANTS.MAX_GOOGLE_DOMAIN_LENGTH} characters)`) : /^[a-zA-Z0-9.-]+$/.test(googleDomain) || errors.push("Google domain contains invalid characters (only alphanumeric, dots, and dashes allowed)")), {
      isValid: errors.length === 0,
      errors
    };
  }
  /**
   * Validates boolean string parameters (no_cache, async)
   */
  static validateBooleanString(value, fieldName = "parameter") {
    let errors = [];
    return value !== void 0 && (typeof value != "string" ? errors.push(`${fieldName} must be a string`) : VALIDATION_CONSTANTS.VALID_BOOLEAN_STRINGS.includes(value) || errors.push(`${fieldName} must be one of: ${VALIDATION_CONSTANTS.VALID_BOOLEAN_STRINGS.join(", ")}`)), {
      isValid: errors.length === 0,
      errors
    };
  }
  /**
   * Validates filter parameter
   */
  static validateFilter(filter) {
    let errors = [];
    return filter !== void 0 && (typeof filter != "string" ? errors.push("Filter must be a string") : VALIDATION_CONSTANTS.VALID_FILTER_VALUES.includes(filter) || errors.push(`Filter must be one of: ${VALIDATION_CONSTANTS.VALID_FILTER_VALUES.join(", ")}`)), {
      isValid: errors.length === 0,
      errors
    };
  }
  /**
   * Validates YouTube sort parameter
   */
  static validateYouTubeSort(sortBy) {
    let errors = [];
    return sortBy !== void 0 && typeof sortBy != "string" && errors.push("Sort parameter must be a string"), {
      isValid: errors.length === 0,
      errors
    };
  }
  /**
   * Validates YouTube duration parameter
   */
  static validateYouTubeDuration(duration) {
    let errors = [];
    return duration !== void 0 && typeof duration != "string" && errors.push("Duration parameter must be a string"), {
      isValid: errors.length === 0,
      errors
    };
  }
  /**
   * Validates YouTube quality parameter
   */
  static validateYouTubeQuality(quality) {
    let errors = [];
    return quality !== void 0 && typeof quality != "string" && errors.push("Quality parameter must be a string"), {
      isValid: errors.length === 0,
      errors
    };
  }
  /**
   * Validates YouTube upload date parameter
   */
  static validateUploadDate(uploadDate) {
    let errors = [];
    return uploadDate !== void 0 && (typeof uploadDate != "string" ? errors.push("Upload date parameter must be a string") : VALIDATION_CONSTANTS.VALID_UPLOAD_DATE.includes(uploadDate) || errors.push(`Upload date parameter must be one of: ${VALIDATION_CONSTANTS.VALID_UPLOAD_DATE.join(", ")}`)), {
      isValid: errors.length === 0,
      errors
    };
  }
  /**
   * Validates mutual exclusivity of no_cache and async parameters
   */
  static validateCacheAsyncMutualExclusivity(noCache, async) {
    let errors = [];
    return noCache === "true" && async === "true" && errors.push("no_cache and async parameters cannot both be set to true"), {
      isValid: errors.length === 0,
      errors
    };
  }
  /**
   * Validates a complete SerpApi configuration
   */
  static validateConfig(config) {
    let allErrors = [];
    if (!config.api_key)
      allErrors.push("API key is required");
    else {
      let apiKeyValidation = this.validateApiKey(config.api_key);
      allErrors.push(...apiKeyValidation.errors);
    }
    if (config.engine ? Object.values(SerpApiEngine).includes(config.engine) || allErrors.push(`Engine must be one of: ${Object.values(SerpApiEngine).join(", ")}`) : allErrors.push("Engine is required"), config.hl) {
      let languageValidation = this.validateLanguageCode(config.hl);
      allErrors.push(...languageValidation.errors);
    }
    if (config.gl) {
      let countryValidation = this.validateCountryCode(config.gl);
      allErrors.push(...countryValidation.errors);
    }
    if (config.num) {
      let numValidation = this.validateNumResults(config.num);
      allErrors.push(...numValidation.errors);
    }
    if (config.start) {
      let startValidation = this.validateStartPosition(config.start);
      allErrors.push(...startValidation.errors);
    }
    let configAny = config;
    if (configAny.location) {
      let locationValidation = this.validateLocation(configAny.location);
      allErrors.push(...locationValidation.errors);
    }
    if (configAny.google_domain) {
      let googleDomainValidation = this.validateGoogleDomain(configAny.google_domain);
      allErrors.push(...googleDomainValidation.errors);
    }
    if (configAny.no_cache) {
      let noCacheValidation = this.validateBooleanString(configAny.no_cache, "no_cache");
      allErrors.push(...noCacheValidation.errors);
    }
    if (configAny.async) {
      let asyncValidation = this.validateBooleanString(configAny.async, "async");
      allErrors.push(...asyncValidation.errors);
    }
    if (configAny.filter) {
      let filterValidation = this.validateFilter(configAny.filter);
      allErrors.push(...filterValidation.errors);
    }
    if (configAny.safe) {
      let safeValidation = this.validateSafeSearch(configAny.safe);
      allErrors.push(...safeValidation.errors);
    }
    if (configAny.device) {
      let deviceValidation = this.validateDeviceType(configAny.device);
      allErrors.push(...deviceValidation.errors);
    }
    let mutualExclusivityValidation = this.validateCacheAsyncMutualExclusivity(
      configAny.no_cache,
      configAny.async
    );
    return allErrors.push(...mutualExclusivityValidation.errors), {
      isValid: allErrors.length === 0,
      errors: allErrors
    };
  }
  /**
   * Sanitizes and normalizes input parameters
   */
  static sanitizeConfig(config) {
    let sanitized = __spreadValues({}, config);
    sanitized.hl && (sanitized.hl = sanitized.hl.toLowerCase().trim()), sanitized.gl && (sanitized.gl = sanitized.gl.toLowerCase().trim()), sanitized.api_key && (sanitized.api_key = sanitized.api_key.trim());
    let sanitizedAny = sanitized;
    if (sanitizedAny.location && (sanitizedAny.location = sanitizedAny.location.trim()), sanitizedAny.google_domain && (sanitizedAny.google_domain = sanitizedAny.google_domain.toLowerCase().trim()), sanitizedAny.no_cache && (sanitizedAny.no_cache = sanitizedAny.no_cache.toLowerCase().trim()), sanitizedAny.async && (sanitizedAny.async = sanitizedAny.async.toLowerCase().trim()), sanitizedAny.filter && (sanitizedAny.filter = sanitizedAny.filter.trim()), sanitizedAny.safe && (sanitizedAny.safe = sanitizedAny.safe.toLowerCase().trim()), sanitizedAny.device && (sanitizedAny.device = sanitizedAny.device.toLowerCase().trim()), sanitized.num && typeof sanitized.num == "string") {
      let parsed = parseInt(sanitized.num, 10);
      isNaN(parsed) || (sanitized.num = parsed);
    }
    if (sanitized.start && typeof sanitized.start == "string") {
      let parsed = parseInt(sanitized.start, 10);
      isNaN(parsed) || (sanitized.start = parsed);
    }
    return Object.keys(sanitized).forEach((key) => {
      sanitized[key] === void 0 && delete sanitized[key];
    }), sanitized;
  }
  /**
   * Validates and sanitizes config, throwing error if invalid
   */
  static validateAndSanitize(config) {
    let sanitized = this.sanitizeConfig(config), validation = this.validateConfig(sanitized);
    if (!validation.isValid)
      throw new Error(
        `Configuration validation failed: ${validation.errors.join(", ")}`
      );
    return sanitized;
  }
};

// ../ap/packages/pieces/community/serp-api/src/lib/services/serp-api-client.ts
var import_pieces_common = require("@activepieces/pieces-common");
function createErrorFromResponse(statusCode, response, message) {
  let defaultMessage = message || "API request failed", finalMessage = (response == null ? void 0 : response.error) || (response == null ? void 0 : response.message) || defaultMessage, error = new Error(finalMessage);
  switch (error.statusCode = statusCode, error.response = response, statusCode) {
    case 401:
      error.code = "SERP_API_AUTH_ERROR";
      break;
    case 400:
      error.code = "SERP_API_VALIDATION_ERROR";
      break;
    case 402:
      error.code = "SERP_API_QUOTA_EXCEEDED_ERROR";
      break;
    case 408:
      error.code = "SERP_API_TIMEOUT_ERROR";
      break;
    case 429:
      error.code = "SERP_API_RATE_LIMIT_ERROR", error.retryAfter = (response == null ? void 0 : response.retry_after) || (response == null ? void 0 : response.retryAfter);
      break;
    default:
      error.code = "SERP_API_RESPONSE_ERROR";
      break;
  }
  return error;
}
function createNetworkError(originalError) {
  let error = new Error(`Network error: ${originalError.message}`);
  return error.code = "SERP_API_NETWORK_ERROR", error.originalError = originalError.message, error;
}
function createTimeoutError(timeout) {
  let error = new Error(`Request timed out after ${timeout}ms`);
  return error.code = "SERP_API_TIMEOUT_ERROR", error.statusCode = 408, error.timeout = timeout, error;
}
var SerpApiClient = class {
  // Creates a new SerpApi client instance
  constructor(config = {}) {
    __publicField(this, "baseUrl");
    __publicField(this, "defaultTimeout");
    __publicField(this, "defaultRetries");
    __publicField(this, "defaultRetryDelay");
    __publicField(this, "enableLogging");
    this.baseUrl = config.baseUrl || "https://serpapi.com/search", this.defaultTimeout = config.defaultTimeout || 3e4, this.defaultRetries = config.defaultRetries || 3, this.defaultRetryDelay = config.defaultRetryDelay || 1e3, this.enableLogging = config.enableLogging || !1;
  }
  // Executes a search request with comprehensive error handling and retry logic
  async executeSearch(config, options = {}) {
    let validatedConfig = SerpApiValidator.validateAndSanitize(config), requestOptions = {
      timeout: options.timeout || this.defaultTimeout,
      retries: options.retries || this.defaultRetries,
      retryDelay: options.retryDelay || this.defaultRetryDelay
    };
    return this.executeWithRetry(validatedConfig, requestOptions);
  }
  // Validates API key by making a test request
  async validateApiKey(apiKey) {
    try {
      let testConfig = {
        api_key: apiKey,
        engine: "google" /* GOOGLE */,
        q: "test",
        num: 1
      };
      return await this.makeHttpRequest(testConfig, { timeout: 1e4, retries: 1, retryDelay: 0 }), !0;
    } catch (error) {
      if (error.code === "SERP_API_AUTH_ERROR")
        return !1;
      throw error;
    }
  }
  // Executes request with retry logic
  async executeWithRetry(config, options) {
    var _a;
    let lastError = null;
    for (let attempt = 1; attempt <= options.retries + 1; attempt++)
      try {
        let response = await this.makeHttpRequest(config, options);
        return this.enableLogging && console.log(`SerpApi request successful on attempt ${attempt}:`, {
          engine: config.engine,
          query: config.q,
          responseTime: (_a = response.search_metadata) == null ? void 0 : _a.total_time_taken
        }), response;
      } catch (error2) {
        if (lastError = error2, this.shouldNotRetry(error2))
          throw error2;
        if (attempt <= options.retries) {
          let delay = this.calculateRetryDelay(attempt, options.retryDelay);
          this.enableLogging && console.warn(`SerpApi request failed on attempt ${attempt}, retrying in ${delay}ms:`, {
            error: error2.message,
            attempt,
            maxRetries: options.retries
          }), await this.sleep(delay);
        }
      }
    let error = new Error("Request failed after all retry attempts");
    throw error.code = "SERP_API_RESPONSE_ERROR", error.statusCode = 500, error.attempts = options.retries + 1, lastError || error;
  }
  // Makes the actual HTTP request to SerpApi
  async makeHttpRequest(config, options) {
    var _a;
    try {
      let queryParams = this.buildQueryParams(config), httpRequest = {
        method: import_pieces_common.HttpMethod.GET,
        url: this.baseUrl,
        queryParams,
        timeout: options.timeout
      };
      this.enableLogging && console.log("Making SerpApi request:", {
        url: this.baseUrl,
        params: this.sanitizeParamsForLogging(queryParams)
      });
      let response = await import_pieces_common.httpClient.sendRequest(httpRequest);
      if ((_a = response.body) != null && _a.error)
        throw createErrorFromResponse(
          response.status || 400,
          response.body,
          response.body.error
        );
      return this.transformResponse(response.body);
    } catch (error) {
      throw this.handleHttpError(error);
    }
  }
  // Builds query parameters from config, filtering undefined values
  buildQueryParams(config) {
    let params = __spreadValues({}, config), cleanParams = {};
    for (let [key, value] of Object.entries(params))
      value != null && (cleanParams[key] = String(value));
    return cleanParams;
  }
  // Transforms raw API response into structured format
  transformResponse(rawResponse) {
    return {
      data: rawResponse
    };
  }
  // Handles and transforms HTTP errors into SerpApi errors
  handleHttpError(error) {
    return error.message.includes("timeout") || error.message.includes("ETIMEDOUT") ? createTimeoutError(this.defaultTimeout) : error.message.includes("ENOTFOUND") || error.message.includes("ECONNREFUSED") ? createNetworkError(error) : error.status ? createErrorFromResponse(
      error.status,
      error.body,
      error.message
    ) : createNetworkError(error);
  }
  // Determines if an error should not be retried
  shouldNotRetry(error) {
    let errorCode = error.code;
    return errorCode === "SERP_API_AUTH_ERROR" || errorCode === "SERP_API_VALIDATION_ERROR" || errorCode === "SERP_API_QUOTA_EXCEEDED_ERROR";
  }
  // Calculates retry delay with exponential backoff
  calculateRetryDelay(attempt, baseDelay) {
    return baseDelay * Math.pow(2, attempt - 1);
  }
  // Sleep utility for retry delays
  sleep(ms) {
    return new Promise((resolve) => setTimeout(resolve, ms));
  }
  // Sanitizes parameters for logging (removes sensitive data)
  sanitizeParamsForLogging(params) {
    let sanitized = __spreadValues({}, params);
    return sanitized.api_key && (sanitized.api_key = "***REDACTED***"), sanitized;
  }
  // Returns client configuration information
  getClientInfo() {
    return {
      baseUrl: this.baseUrl,
      defaultTimeout: this.defaultTimeout,
      defaultRetries: this.defaultRetries,
      defaultRetryDelay: this.defaultRetryDelay,
      enableLogging: this.enableLogging
    };
  }
};

// ../ap/packages/pieces/community/serp-api/src/lib/auth.ts
var serpApiAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "You can obtain your API key from [Dashboard](https://serpapi.com/dashboard).",
  required: !0,
  validate: async ({ auth }) => {
    try {
      let formatValidation = SerpApiValidator.validateApiKey(auth);
      return formatValidation.isValid ? await new SerpApiClient({
        defaultTimeout: 1e4,
        defaultRetries: 1
      }).validateApiKey(auth) ? {
        valid: !0
      } : {
        valid: !1,
        error: "Invalid API key. Please check your SerpApi API key and ensure it has sufficient credits."
      } : {
        valid: !1,
        error: `Invalid API key format: ${formatValidation.errors.join(", ")}`
      };
    } catch (error) {
      let errorMessage = error instanceof Error ? error.message : "Unknown error occurred";
      return errorMessage.includes("timeout") ? {
        valid: !1,
        error: "API validation timed out. Please check your network connection and try again."
      } : errorMessage.includes("network") || errorMessage.includes("ENOTFOUND") ? {
        valid: !1,
        error: "Network error occurred. Please check your internet connection."
      } : {
        valid: !1,
        error: `API key validation failed: ${errorMessage}`
      };
    }
  }
});

// ../ap/packages/pieces/community/serp-api/src/lib/constants/countries.ts
var COUNTRY_OPTIONS = [
  { label: "us (United States)", value: "us" },
  { label: "af (Afghanistan)", value: "af" },
  { label: "al (Albania)", value: "al" },
  { label: "dz (Algeria)", value: "dz" },
  { label: "as (American Samoa)", value: "as" },
  { label: "ad (Andorra)", value: "ad" },
  { label: "ao (Angola)", value: "ao" },
  { label: "ai (Anguilla)", value: "ai" },
  { label: "aq (Antarctica)", value: "aq" },
  { label: "ag (Antigua and Barbuda)", value: "ag" },
  { label: "ar (Argentina)", value: "ar" },
  { label: "am (Armenia)", value: "am" },
  { label: "aw (Aruba)", value: "aw" },
  { label: "au (Australia)", value: "au" },
  { label: "at (Austria)", value: "at" },
  { label: "az (Azerbaijan)", value: "az" },
  { label: "bs (Bahamas)", value: "bs" },
  { label: "bh (Bahrain)", value: "bh" },
  { label: "bd (Bangladesh)", value: "bd" },
  { label: "bb (Barbados)", value: "bb" },
  { label: "by (Belarus)", value: "by" },
  { label: "be (Belgium)", value: "be" },
  { label: "bz (Belize)", value: "bz" },
  { label: "bj (Benin)", value: "bj" },
  { label: "bm (Bermuda)", value: "bm" },
  { label: "bt (Bhutan)", value: "bt" },
  { label: "bo (Bolivia)", value: "bo" },
  { label: "ba (Bosnia and Herzegovina)", value: "ba" },
  { label: "bw (Botswana)", value: "bw" },
  { label: "bv (Bouvet Island)", value: "bv" },
  { label: "br (Brazil)", value: "br" },
  { label: "io (British Indian Ocean Territory)", value: "io" },
  { label: "bn (Brunei Darussalam)", value: "bn" },
  { label: "bg (Bulgaria)", value: "bg" },
  { label: "bf (Burkina Faso)", value: "bf" },
  { label: "bi (Burundi)", value: "bi" },
  { label: "kh (Cambodia)", value: "kh" },
  { label: "cm (Cameroon)", value: "cm" },
  { label: "ca (Canada)", value: "ca" },
  { label: "cv (Cape Verde)", value: "cv" },
  { label: "ky (Cayman Islands)", value: "ky" },
  { label: "cf (Central African Republic)", value: "cf" },
  { label: "td (Chad)", value: "td" },
  { label: "cl (Chile)", value: "cl" },
  { label: "cn (China)", value: "cn" },
  { label: "cx (Christmas Island)", value: "cx" },
  { label: "cc (Cocos (Keeling) Islands)", value: "cc" },
  { label: "co (Colombia)", value: "co" },
  { label: "km (Comoros)", value: "km" },
  { label: "cg (Congo)", value: "cg" },
  { label: "cd (Congo, the Democratic Republic of the)", value: "cd" },
  { label: "ck (Cook Islands)", value: "ck" },
  { label: "cr (Costa Rica)", value: "cr" },
  { label: "ci (Cote D'ivoire)", value: "ci" },
  { label: "hr (Croatia)", value: "hr" },
  { label: "cu (Cuba)", value: "cu" },
  { label: "cy (Cyprus)", value: "cy" },
  { label: "cz (Czech Republic)", value: "cz" },
  { label: "dk (Denmark)", value: "dk" },
  { label: "dj (Djibouti)", value: "dj" },
  { label: "dm (Dominica)", value: "dm" },
  { label: "do (Dominican Republic)", value: "do" },
  { label: "ec (Ecuador)", value: "ec" },
  { label: "eg (Egypt)", value: "eg" },
  { label: "sv (El Salvador)", value: "sv" },
  { label: "gq (Equatorial Guinea)", value: "gq" },
  { label: "er (Eritrea)", value: "er" },
  { label: "ee (Estonia)", value: "ee" },
  { label: "et (Ethiopia)", value: "et" },
  { label: "fk (Falkland Islands (Malvinas))", value: "fk" },
  { label: "fo (Faroe Islands)", value: "fo" },
  { label: "fj (Fiji)", value: "fj" },
  { label: "fi (Finland)", value: "fi" },
  { label: "fr (France)", value: "fr" },
  { label: "gf (French Guiana)", value: "gf" },
  { label: "pf (French Polynesia)", value: "pf" },
  { label: "tf (French Southern Territories)", value: "tf" },
  { label: "ga (Gabon)", value: "ga" },
  { label: "gm (Gambia)", value: "gm" },
  { label: "ge (Georgia)", value: "ge" },
  { label: "de (Germany)", value: "de" },
  { label: "gh (Ghana)", value: "gh" },
  { label: "gi (Gibraltar)", value: "gi" },
  { label: "gr (Greece)", value: "gr" },
  { label: "gl (Greenland)", value: "gl" },
  { label: "gd (Grenada)", value: "gd" },
  { label: "gp (Guadeloupe)", value: "gp" },
  { label: "gu (Guam)", value: "gu" },
  { label: "gt (Guatemala)", value: "gt" },
  { label: "gg (Guernsey)", value: "gg" },
  { label: "gn (Guinea)", value: "gn" },
  { label: "gw (Guinea-Bissau)", value: "gw" },
  { label: "gy (Guyana)", value: "gy" },
  { label: "ht (Haiti)", value: "ht" },
  { label: "hm (Heard Island and Mcdonald Islands)", value: "hm" },
  { label: "va (Holy See (Vatican City State))", value: "va" },
  { label: "hn (Honduras)", value: "hn" },
  { label: "hk (Hong Kong)", value: "hk" },
  { label: "hu (Hungary)", value: "hu" },
  { label: "is (Iceland)", value: "is" },
  { label: "in (India)", value: "in" },
  { label: "id (Indonesia)", value: "id" },
  { label: "ir (Iran, Islamic Republic of)", value: "ir" },
  { label: "iq (Iraq)", value: "iq" },
  { label: "ie (Ireland)", value: "ie" },
  { label: "im (Isle of Man)", value: "im" },
  { label: "il (Israel)", value: "il" },
  { label: "it (Italy)", value: "it" },
  { label: "je (Jersey)", value: "je" },
  { label: "jm (Jamaica)", value: "jm" },
  { label: "jp (Japan)", value: "jp" },
  { label: "jo (Jordan)", value: "jo" },
  { label: "kz (Kazakhstan)", value: "kz" },
  { label: "ke (Kenya)", value: "ke" },
  { label: "ki (Kiribati)", value: "ki" },
  { label: "kp (Korea, Democratic People's Republic of)", value: "kp" },
  { label: "kr (Korea, Republic of)", value: "kr" },
  { label: "kw (Kuwait)", value: "kw" },
  { label: "kg (Kyrgyzstan)", value: "kg" },
  { label: "la (Lao People's Democratic Republic)", value: "la" },
  { label: "lv (Latvia)", value: "lv" },
  { label: "lb (Lebanon)", value: "lb" },
  { label: "ls (Lesotho)", value: "ls" },
  { label: "lr (Liberia)", value: "lr" },
  { label: "ly (Libyan Arab Jamahiriya)", value: "ly" },
  { label: "li (Liechtenstein)", value: "li" },
  { label: "lt (Lithuania)", value: "lt" },
  { label: "lu (Luxembourg)", value: "lu" },
  { label: "mo (Macao)", value: "mo" },
  { label: "mk (Macedonia, the Former Yugosalv Republic of)", value: "mk" },
  { label: "mg (Madagascar)", value: "mg" },
  { label: "mw (Malawi)", value: "mw" },
  { label: "my (Malaysia)", value: "my" },
  { label: "mv (Maldives)", value: "mv" },
  { label: "ml (Mali)", value: "ml" },
  { label: "mt (Malta)", value: "mt" },
  { label: "mh (Marshall Islands)", value: "mh" },
  { label: "mq (Martinique)", value: "mq" },
  { label: "mr (Mauritania)", value: "mr" },
  { label: "mu (Mauritius)", value: "mu" },
  { label: "yt (Mayotte)", value: "yt" },
  { label: "mx (Mexico)", value: "mx" },
  { label: "fm (Micronesia, Federated States of)", value: "fm" },
  { label: "md (Moldova, Republic of)", value: "md" },
  { label: "mc (Monaco)", value: "mc" },
  { label: "mn (Mongolia)", value: "mn" },
  { label: "me (Montenegro)", value: "me" },
  { label: "ms (Montserrat)", value: "ms" },
  { label: "ma (Morocco)", value: "ma" },
  { label: "mz (Mozambique)", value: "mz" },
  { label: "mm (Myanmar)", value: "mm" },
  { label: "na (Namibia)", value: "na" },
  { label: "nr (Nauru)", value: "nr" },
  { label: "np (Nepal)", value: "np" },
  { label: "nl (Netherlands)", value: "nl" },
  { label: "an (Netherlands Antilles)", value: "an" },
  { label: "nc (New Caledonia)", value: "nc" },
  { label: "nz (New Zealand)", value: "nz" },
  { label: "ni (Nicaragua)", value: "ni" },
  { label: "ne (Niger)", value: "ne" },
  { label: "ng (Nigeria)", value: "ng" },
  { label: "nu (Niue)", value: "nu" },
  { label: "nf (Norfolk Island)", value: "nf" },
  { label: "mp (Northern Mariana Islands)", value: "mp" },
  { label: "no (Norway)", value: "no" },
  { label: "om (Oman)", value: "om" },
  { label: "pk (Pakistan)", value: "pk" },
  { label: "pw (Palau)", value: "pw" },
  { label: "ps (Palestinian Territory, Occupied)", value: "ps" },
  { label: "pa (Panama)", value: "pa" },
  { label: "pg (Papua New Guinea)", value: "pg" },
  { label: "py (Paraguay)", value: "py" },
  { label: "pe (Peru)", value: "pe" },
  { label: "ph (Philippines)", value: "ph" },
  { label: "pn (Pitcairn)", value: "pn" },
  { label: "pl (Poland)", value: "pl" },
  { label: "pt (Portugal)", value: "pt" },
  { label: "pr (Puerto Rico)", value: "pr" },
  { label: "qa (Qatar)", value: "qa" },
  { label: "re (Reunion)", value: "re" },
  { label: "ro (Romania)", value: "ro" },
  { label: "ru (Russian Federation)", value: "ru" },
  { label: "rw (Rwanda)", value: "rw" },
  { label: "sh (Saint Helena)", value: "sh" },
  { label: "kn (Saint Kitts and Nevis)", value: "kn" },
  { label: "lc (Saint Lucia)", value: "lc" },
  { label: "pm (Saint Pierre and Miquelon)", value: "pm" },
  { label: "vc (Saint Vincent and the Grenadines)", value: "vc" },
  { label: "ws (Samoa)", value: "ws" },
  { label: "sm (San Marino)", value: "sm" },
  { label: "st (Sao Tome and Principe)", value: "st" },
  { label: "sa (Saudi Arabia)", value: "sa" },
  { label: "sn (Senegal)", value: "sn" },
  { label: "rs (Serbia)", value: "rs" },
  { label: "sc (Seychelles)", value: "sc" },
  { label: "sl (Sierra Leone)", value: "sl" },
  { label: "sg (Singapore)", value: "sg" },
  { label: "sk (Slovakia)", value: "sk" },
  { label: "si (Slovenia)", value: "si" },
  { label: "sb (Solomon Islands)", value: "sb" },
  { label: "so (Somalia)", value: "so" },
  { label: "za (South Africa)", value: "za" },
  { label: "gs (South Georgia and the South Sandwich Islands)", value: "gs" },
  { label: "es (Spain)", value: "es" },
  { label: "lk (Sri Lanka)", value: "lk" },
  { label: "sd (Sudan)", value: "sd" },
  { label: "sr (Suriname)", value: "sr" },
  { label: "sj (Svalbard and Jan Mayen)", value: "sj" },
  { label: "sz (Swaziland)", value: "sz" },
  { label: "se (Sweden)", value: "se" },
  { label: "ch (Switzerland)", value: "ch" },
  { label: "sy (Syrian Arab Republic)", value: "sy" },
  { label: "tw (Taiwan, Province of China)", value: "tw" },
  { label: "tj (Tajikistan)", value: "tj" },
  { label: "tz (Tanzania, United Republic of)", value: "tz" },
  { label: "th (Thailand)", value: "th" },
  { label: "tl (Timor-Leste)", value: "tl" },
  { label: "tg (Togo)", value: "tg" },
  { label: "tk (Tokelau)", value: "tk" },
  { label: "to (Tonga)", value: "to" },
  { label: "tt (Trinidad and Tobago)", value: "tt" },
  { label: "tn (Tunisia)", value: "tn" },
  { label: "tr (Turkiye)", value: "tr" },
  { label: "tm (Turkmenistan)", value: "tm" },
  { label: "tc (Turks and Caicos Islands)", value: "tc" },
  { label: "tv (Tuvalu)", value: "tv" },
  { label: "ug (Uganda)", value: "ug" },
  { label: "ua (Ukraine)", value: "ua" },
  { label: "ae (United Arab Emirates)", value: "ae" },
  { label: "uk (United Kingdom)", value: "uk" },
  { label: "gb (United Kingdom)", value: "gb" },
  { label: "um (United States Minor Outlying Islands)", value: "um" },
  { label: "uy (Uruguay)", value: "uy" },
  { label: "uz (Uzbekistan)", value: "uz" },
  { label: "vu (Vanuatu)", value: "vu" },
  { label: "ve (Venezuela)", value: "ve" },
  { label: "vn (Viet Nam)", value: "vn" },
  { label: "vg (Virgin Islands, British)", value: "vg" },
  { label: "vi (Virgin Islands, U.S.)", value: "vi" },
  { label: "wf (Wallis and Futuna)", value: "wf" },
  { label: "eh (Western Sahara)", value: "eh" },
  { label: "ye (Yemen)", value: "ye" },
  { label: "zm (Zambia)", value: "zm" },
  { label: "zw (Zimbabwe)", value: "zw" }
];

// ../ap/packages/pieces/community/serp-api/src/lib/constants/languages.ts
var LANGUAGE_OPTIONS = [
  { label: "English", value: "en" },
  { label: "Afrikaans", value: "af" },
  { label: "Akan", value: "ak" },
  { label: "Albanian", value: "sq" },
  { label: "Amharic", value: "am" },
  { label: "Arabic", value: "ar" },
  { label: "Armenian", value: "hy" },
  { label: "Azerbaijani", value: "az" },
  { label: "Basque", value: "eu" },
  { label: "Belarusian", value: "be" },
  { label: "Bemba", value: "bem" },
  { label: "Bengali", value: "bn" },
  { label: "Bihari", value: "bh" },
  { label: "Bosnian", value: "bs" },
  { label: "Breton", value: "br" },
  { label: "Bulgarian", value: "bg" },
  { label: "Bhutanese", value: "bt" },
  { label: "Cambodian", value: "km" },
  { label: "Catalan", value: "ca" },
  { label: "Cherokee", value: "chr" },
  { label: "Chichewa", value: "ny" },
  { label: "Chinese (Simplified)", value: "zh-cn" },
  { label: "Chinese (Traditional)", value: "zh-tw" },
  { label: "Corsican", value: "co" },
  { label: "Croatian", value: "hr" },
  { label: "Czech", value: "cs" },
  { label: "Danish", value: "da" },
  { label: "Dutch", value: "nl" },
  { label: "Esperanto", value: "eo" },
  { label: "Estonian", value: "et" },
  { label: "Ewe", value: "ee" },
  { label: "Faroese", value: "fo" },
  { label: "Filipino", value: "tl" },
  { label: "Finnish", value: "fi" },
  { label: "French", value: "fr" },
  { label: "Frisian", value: "fy" },
  { label: "Ga", value: "gaa" },
  { label: "Galician", value: "gl" },
  { label: "Georgian", value: "ka" },
  { label: "German", value: "de" },
  { label: "Greek", value: "el" },
  { label: "Greenlandic", value: "kl" },
  { label: "Guarani", value: "gn" },
  { label: "Gujarati", value: "gu" },
  { label: "Haitian Creole", value: "ht" },
  { label: "Hausa", value: "ha" },
  { label: "Hawaiian", value: "haw" },
  { label: "Hebrew", value: "he" },
  { label: "Hindi", value: "hi" },
  { label: "Hungarian", value: "hu" },
  { label: "Icelandic", value: "is" },
  { label: "Igbo", value: "ig" },
  { label: "Indonesian", value: "id" },
  { label: "Interlingua", value: "ia" },
  { label: "Irish", value: "ga" },
  { label: "Italian", value: "it" },
  { label: "Japanese", value: "ja" },
  { label: "Javanese", value: "jw" },
  { label: "Kannada", value: "kn" },
  { label: "Kazakh", value: "kk" },
  { label: "Kinyarwanda", value: "rw" },
  { label: "Kirundi", value: "rn" },
  { label: "Kongo", value: "kg" },
  { label: "Korean", value: "ko" },
  { label: "Krio (Sierra Leone)", value: "kri" },
  { label: "Kurdish", value: "ku" },
  { label: "Kurdish (Soran\xEE)", value: "ckb" },
  { label: "Kyrgyz", value: "ky" },
  { label: "Laothian", value: "lo" },
  { label: "Latin", value: "la" },
  { label: "Latvian", value: "lv" },
  { label: "Lingala", value: "ln" },
  { label: "Lithuanian", value: "lt" },
  { label: "Lozi", value: "loz" },
  { label: "Luganda", value: "lg" },
  { label: "Luo", value: "ach" },
  { label: "Macedonian", value: "mk" },
  { label: "Malagasy", value: "mg" },
  { label: "Malay", value: "ms" },
  { label: "Malayalam", value: "ml" },
  { label: "Maltese", value: "mt" },
  { label: "Maldives", value: "mv" },
  { label: "Maori", value: "mi" },
  { label: "Marathi", value: "mr" },
  { label: "Mauritian Creole", value: "mfe" },
  { label: "Moldavian", value: "mo" },
  { label: "Mongolian", value: "mn" },
  { label: "Montenegrin", value: "sr-me" },
  { label: "Myanmar", value: "my" },
  { label: "Nepali", value: "ne" },
  { label: "Nigerian Pidgin", value: "pcm" },
  { label: "Northern Sotho", value: "nso" },
  { label: "Norwegian", value: "no" },
  { label: "Norwegian (Nynorsk)", value: "nn" },
  { label: "Occitan", value: "oc" },
  { label: "Oriya", value: "or" },
  { label: "Oromo", value: "om" },
  { label: "Pashto", value: "ps" },
  { label: "Persian", value: "fa" },
  { label: "Polish", value: "pl" },
  { label: "Portuguese", value: "pt" },
  { label: "Portuguese (Brazil)", value: "pt-br" },
  { label: "Portuguese (Portugal)", value: "pt-pt" },
  { label: "Punjabi", value: "pa" },
  { label: "Quechua", value: "qu" },
  { label: "Romanian", value: "ro" },
  { label: "Romansh", value: "rm" },
  { label: "Runyakitara", value: "nyn" },
  { label: "Russian", value: "ru" },
  { label: "Samoa", value: "ws" },
  { label: "Scots Gaelic", value: "gd" },
  { label: "Serbian", value: "sr" },
  { label: "Serbo-Croatian", value: "sh" },
  { label: "Sesotho", value: "st" },
  { label: "Setswana", value: "tn" },
  { label: "Seychellois Creole", value: "crs" },
  { label: "Shona", value: "sn" },
  { label: "Sindhi", value: "sd" },
  { label: "Sinhalese", value: "si" },
  { label: "Slovak", value: "sk" },
  { label: "Slovenian", value: "sl" },
  { label: "Somali", value: "so" },
  { label: "Spanish", value: "es" },
  { label: "Spanish (Latin American)", value: "es-419" },
  { label: "Sundanese", value: "su" },
  { label: "Swahili", value: "sw" },
  { label: "Swedish", value: "sv" },
  { label: "Tajik", value: "tg" },
  { label: "Tamil", value: "ta" },
  { label: "Tatar", value: "tt" },
  { label: "Telugu", value: "te" },
  { label: "Thai", value: "th" },
  { label: "Tigrinya", value: "ti" },
  { label: "Tonga", value: "to" },
  { label: "Tshiluba", value: "lua" },
  { label: "Tumbuka", value: "tum" },
  { label: "Turkish", value: "tr" },
  { label: "Turkmen", value: "tk" },
  { label: "Twi", value: "tw" },
  { label: "Uighur", value: "ug" },
  { label: "Ukrainian", value: "uk" },
  { label: "Urdu", value: "ur" },
  { label: "Uzbek", value: "uz" },
  { label: "Vanuatu", value: "vu" },
  { label: "Vietnamese", value: "vi" },
  { label: "Welsh", value: "cy" },
  { label: "Wolof", value: "wo" },
  { label: "Xhosa", value: "xh" },
  { label: "Yiddish", value: "yi" },
  { label: "Yoruba", value: "yo" },
  { label: "Zulu", value: "zu" }
];

// ../ap/packages/pieces/community/serp-api/src/lib/output-schemas.ts
var serpApiWebOrganicResultFields = [
  { key: "position", label: "Position", format: "number" },
  { key: "title", label: "Title" },
  { key: "link", label: "Link", format: "url" },
  { key: "displayed_link", label: "Displayed Link" },
  { key: "snippet", label: "Snippet" },
  { key: "favicon", label: "Favicon", format: "image" }
], searchAppleAppStoreOutputSchema = {
  fields: [
    { key: "success", label: "Success", format: "boolean" },
    {
      key: "data",
      label: "Data",
      children: [
        {
          key: "search_information",
          label: "Search Information",
          children: [{ key: "results_count", label: "Results Count", format: "number" }]
        },
        {
          key: "organic_results",
          label: "Apps",
          labelKey: "title",
          listItems: [
            { key: "position", label: "Position", format: "number" },
            { key: "id", label: "App ID" },
            { key: "title", label: "Title" },
            { key: "link", label: "App Store Link", format: "url" },
            { key: "bundle_id", label: "Bundle ID" },
            { key: "version", label: "Version" },
            { key: "description", label: "Description" },
            { key: "age_rating", label: "Age Rating" },
            { key: "release_date", label: "Release Date" },
            { key: "seller_link", label: "Seller Link", format: "url" },
            {
              key: "price",
              label: "Price",
              children: [{ key: "type", label: "Price Type" }]
            },
            {
              key: "rating",
              label: "Ratings",
              listItems: [
                { key: "type", label: "Rating Type" },
                { key: "rating", label: "Rating", format: "number" },
                { key: "count", label: "Rating Count", format: "number" }
              ]
            },
            {
              key: "genres",
              label: "Genres",
              listItems: [{ key: "name", label: "Genre Name" }]
            },
            {
              key: "developer",
              label: "Developer",
              children: [
                { key: "name", label: "Developer Name" },
                { key: "id", label: "Developer ID" },
                { key: "link", label: "Developer Link", format: "url" }
              ]
            }
          ]
        }
      ]
    }
  ]
}, searchDuckduckgoOutputSchema = {
  fields: [
    { key: "success", label: "Success", format: "boolean" },
    {
      key: "data",
      label: "Data",
      children: [
        {
          key: "organic_results",
          label: "Results",
          labelKey: "title",
          listItems: [
            { key: "position", label: "Position", format: "number" },
            { key: "title", label: "Title" },
            { key: "link", label: "Link", format: "url" },
            { key: "snippet", label: "Snippet" },
            { key: "date", label: "Date" },
            { key: "favicon", label: "Favicon", format: "image" },
            {
              key: "sitelinks",
              label: "Sitelinks",
              labelKey: "title",
              listItems: [
                { key: "title", label: "Title" },
                { key: "link", label: "Link", format: "url" },
                { key: "snippet", label: "Snippet" }
              ]
            }
          ]
        },
        {
          key: "related_searches",
          label: "Related Searches",
          labelKey: "query",
          listItems: [
            { key: "query", label: "Query" },
            { key: "link", label: "Link", format: "url" }
          ]
        }
      ]
    }
  ]
}, searchGoogleMapsOutputSchema = {
  fields: [
    { key: "success", label: "Success", format: "boolean" },
    {
      key: "data",
      label: "Data",
      children: [
        {
          key: "local_results",
          label: "Places",
          labelKey: "title",
          listItems: [
            { key: "position", label: "Position", format: "number" },
            { key: "title", label: "Title" },
            { key: "place_id", label: "Place ID" },
            { key: "data_id", label: "Data ID" },
            { key: "data_cid", label: "Data CID" },
            { key: "rating", label: "Rating", format: "number" },
            { key: "reviews", label: "Review Count", format: "number" },
            { key: "price", label: "Price Range" },
            { key: "type", label: "Primary Type" },
            { key: "address", label: "Address" },
            { key: "phone", label: "Phone" },
            { key: "website", label: "Website", format: "url" },
            { key: "hours", label: "Hours" },
            {
              key: "gps_coordinates",
              label: "GPS Coordinates",
              children: [
                { key: "latitude", label: "Latitude", format: "number" },
                { key: "longitude", label: "Longitude", format: "number" }
              ]
            }
          ]
        }
      ]
    }
  ]
}, searchBingOutputSchema = {
  fields: [
    { key: "success", label: "Success", format: "boolean" },
    {
      key: "data",
      label: "Data",
      children: [
        {
          key: "search_information",
          label: "Search Information",
          children: [{ key: "total_results", label: "Total Results", format: "number" }]
        },
        {
          key: "organic_results",
          label: "Results",
          labelKey: "title",
          listItems: [
            { key: "position", label: "Position", format: "number" },
            { key: "title", label: "Title" },
            { key: "link", label: "Link", format: "url" },
            { key: "displayed_link", label: "Displayed Link" },
            { key: "snippet", label: "Snippet" },
            { key: "thumbnail", label: "Thumbnail", format: "image" }
          ]
        }
      ]
    }
  ]
}, googleTrendsRankedListFields = [
  { key: "value", label: "Trend Label" },
  { key: "extracted_value", label: "Extracted Value", format: "number" },
  { key: "link", label: "Google Trends Link", format: "url" }
], searchGoogleTrendsAiOutputSchema = {
  fields: [
    { key: "success", label: "Success", format: "boolean" },
    {
      key: "data",
      label: "Data",
      children: [
        {
          key: "interest_over_time",
          label: "Interest Over Time",
          children: [
            {
              key: "timeline_data",
              label: "Timeline",
              labelKey: "date",
              listItems: [
                { key: "date", label: "Date" },
                { key: "timestamp", label: "Timestamp" },
                {
                  key: "values",
                  label: "Values",
                  labelKey: "query",
                  listItems: [
                    { key: "query", label: "Query" },
                    { key: "value", label: "Value" },
                    { key: "extracted_value", label: "Extracted Value", format: "number" }
                  ]
                }
              ]
            }
          ]
        },
        {
          key: "related_topics",
          label: "Related Topics",
          children: [
            {
              key: "top",
              label: "Top Topics",
              labelKey: "value",
              listItems: [
                {
                  key: "topic",
                  label: "Topic",
                  children: [
                    { key: "title", label: "Title" },
                    { key: "type", label: "Type" }
                  ]
                },
                ...googleTrendsRankedListFields
              ]
            },
            {
              key: "rising",
              label: "Rising Topics",
              labelKey: "value",
              listItems: [
                {
                  key: "topic",
                  label: "Topic",
                  children: [
                    { key: "title", label: "Title" },
                    { key: "type", label: "Type" }
                  ]
                },
                ...googleTrendsRankedListFields
              ]
            }
          ]
        },
        {
          key: "related_queries",
          label: "Related Queries",
          children: [
            {
              key: "top",
              label: "Top Queries",
              labelKey: "query",
              listItems: [{ key: "query", label: "Query" }, ...googleTrendsRankedListFields]
            },
            {
              key: "rising",
              label: "Rising Queries",
              labelKey: "query",
              listItems: [{ key: "query", label: "Query" }, ...googleTrendsRankedListFields]
            }
          ]
        },
        {
          key: "compared_breakdown_by_region",
          label: "Compared Breakdown By Region",
          labelKey: "location",
          listItems: [
            { key: "geo", label: "Geo Code" },
            { key: "location", label: "Location" },
            { key: "max_value_index", label: "Max Value Index", format: "number" },
            {
              key: "values",
              label: "Values",
              labelKey: "query",
              listItems: [
                { key: "query", label: "Query" },
                { key: "value", label: "Value" },
                { key: "extracted_value", label: "Extracted Value", format: "number" }
              ]
            }
          ]
        },
        {
          key: "interest_by_region",
          label: "Interest By Region",
          labelKey: "location",
          listItems: [
            { key: "geo", label: "Geo Code" },
            { key: "location", label: "Location" },
            { key: "max_value_index", label: "Max Value Index", format: "number" },
            { key: "value", label: "Value" },
            { key: "extracted_value", label: "Extracted Value", format: "number" }
          ]
        }
      ]
    }
  ]
}, searchGoogleJobsOutputSchema = {
  fields: [
    { key: "success", label: "Success", format: "boolean" },
    {
      key: "data",
      label: "Data",
      children: [
        {
          key: "jobs_results",
          label: "Jobs",
          labelKey: "title",
          listItems: [
            { key: "title", label: "Job Title" },
            { key: "company_name", label: "Company" },
            { key: "location", label: "Location" },
            { key: "via", label: "Source" },
            { key: "description", label: "Description" },
            { key: "thumbnail", label: "Company Logo", format: "image" },
            { key: "job_id", label: "Job ID" },
            {
              key: "detected_extensions",
              label: "Details",
              children: [
                { key: "posted_at", label: "Posted At" },
                { key: "schedule_type", label: "Schedule Type" },
                { key: "qualifications", label: "Qualifications" }
              ]
            },
            {
              key: "apply_options",
              label: "Apply Options",
              labelKey: "title",
              listItems: [
                { key: "title", label: "Source" },
                { key: "link", label: "Apply Link", format: "url" }
              ]
            }
          ]
        },
        {
          key: "serpapi_pagination",
          label: "Pagination",
          children: [{ key: "next_page_token", label: "Next Page Token" }]
        }
      ]
    }
  ]
}, searchGoogleWebAiOutputSchema = {
  fields: [
    { key: "success", label: "Success", format: "boolean" },
    {
      key: "data",
      label: "Data",
      children: [
        {
          key: "search_information",
          label: "Search Information",
          children: [
            { key: "total_results", label: "Total Results", format: "number" },
            { key: "time_taken_displayed", label: "Time Taken (s)", format: "number" }
          ]
        },
        {
          key: "organic_results",
          label: "Results",
          labelKey: "title",
          listItems: serpApiWebOrganicResultFields
        },
        {
          key: "related_questions",
          label: "Related Questions",
          labelKey: "question",
          listItems: [
            { key: "question", label: "Question" },
            { key: "snippet", label: "Snippet" },
            { key: "link", label: "Link", format: "url" }
          ]
        },
        {
          key: "related_searches",
          label: "Related Searches",
          labelKey: "query",
          listItems: [
            { key: "query", label: "Query" },
            { key: "link", label: "Link", format: "url" }
          ]
        }
      ]
    }
  ]
}, searchGoogleLensOutputSchema = {
  fields: [
    { key: "success", label: "Success", format: "boolean" },
    {
      key: "data",
      label: "Data",
      children: [
        {
          key: "ai_overview",
          label: "AI Overview",
          children: [{ key: "page_token", label: "AI Overview Page Token" }]
        },
        {
          key: "visual_matches",
          label: "Visual Matches",
          labelKey: "title",
          listItems: [
            { key: "position", label: "Position", format: "number" },
            { key: "title", label: "Title" },
            { key: "link", label: "Link", format: "url" },
            { key: "source", label: "Source" },
            { key: "thumbnail", label: "Thumbnail", format: "image" }
          ]
        },
        {
          key: "knowledge_graph",
          label: "Knowledge Graph",
          children: [
            { key: "title", label: "Title" },
            { key: "subtitle", label: "Subtitle" },
            { key: "link", label: "Link", format: "url" },
            { key: "thumbnail", label: "Thumbnail", format: "image" }
          ]
        }
      ]
    }
  ]
}, searchYoutubeAiOutputSchema = {
  fields: [
    { key: "success", label: "Success", format: "boolean" },
    {
      key: "data",
      label: "Data",
      children: [
        {
          key: "video_results",
          label: "Videos",
          labelKey: "title",
          listItems: [
            { key: "title", label: "Title" },
            { key: "link", label: "Link", format: "url" },
            { key: "video_id", label: "Video ID" },
            { key: "published_date", label: "Published" },
            { key: "views", label: "Views", format: "number" },
            { key: "length", label: "Length" },
            { key: "description", label: "Description" },
            {
              key: "channel",
              label: "Channel",
              children: [
                { key: "name", label: "Channel Name" },
                { key: "link", label: "Channel Link", format: "url" },
                { key: "verified", label: "Verified", format: "boolean" },
                { key: "thumbnail", label: "Channel Thumbnail", format: "image" }
              ]
            },
            {
              key: "thumbnail",
              label: "Thumbnail",
              children: [{ key: "static", label: "Thumbnail Image", format: "image" }]
            }
          ]
        },
        {
          key: "shorts_results",
          label: "Shorts",
          listItems: [
            { key: "position_on_page", label: "Position On Page", format: "number" },
            {
              key: "shorts",
              label: "Shorts",
              labelKey: "title",
              listItems: [
                { key: "title", label: "Title" },
                { key: "link", label: "Link", format: "url" },
                { key: "thumbnail", label: "Thumbnail", format: "image" },
                { key: "views", label: "Views", format: "number" }
              ]
            }
          ]
        }
      ]
    }
  ]
}, searchGoogleScholarOutputSchema = {
  fields: [
    { key: "success", label: "Success", format: "boolean" },
    {
      key: "data",
      label: "Data",
      children: [
        {
          key: "organic_results",
          label: "Results",
          labelKey: "title",
          listItems: [
            { key: "position", label: "Position", format: "number" },
            { key: "title", label: "Title" },
            { key: "result_id", label: "Result ID" },
            { key: "link", label: "Link", format: "url" },
            { key: "snippet", label: "Snippet" },
            {
              key: "publication_info",
              label: "Publication Info",
              children: [{ key: "summary", label: "Summary" }]
            },
            {
              key: "resources",
              label: "Resources",
              labelKey: "title",
              listItems: [
                { key: "title", label: "Source" },
                { key: "file_format", label: "File Format" },
                { key: "link", label: "Link", format: "url" }
              ]
            },
            {
              key: "inline_links",
              label: "Links",
              children: [
                {
                  key: "cited_by",
                  label: "Cited By",
                  children: [
                    { key: "total", label: "Citation Count", format: "number" },
                    { key: "link", label: "Citations Link", format: "url" }
                  ]
                },
                {
                  key: "versions",
                  label: "Versions",
                  children: [
                    { key: "total", label: "Version Count", format: "number" },
                    { key: "link", label: "Versions Link", format: "url" }
                  ]
                }
              ]
            }
          ]
        }
      ]
    }
  ]
}, searchWalmartOutputSchema = {
  fields: [
    { key: "success", label: "Success", format: "boolean" },
    {
      key: "data",
      label: "Data",
      children: [
        {
          key: "organic_results",
          label: "Products",
          labelKey: "title",
          listItems: [
            { key: "us_item_id", label: "Item ID" },
            { key: "product_id", label: "Product ID" },
            { key: "title", label: "Title" },
            { key: "thumbnail", label: "Thumbnail", format: "image" },
            { key: "rating", label: "Rating", format: "number" },
            { key: "reviews", label: "Review Count", format: "number" },
            { key: "seller_name", label: "Seller Name" },
            { key: "out_of_stock", label: "Out Of Stock", format: "boolean" },
            { key: "free_shipping", label: "Free Shipping", format: "boolean" },
            { key: "product_page_url", label: "Product Page", format: "url" },
            {
              key: "primary_offer",
              label: "Offer",
              children: [
                { key: "offer_price", label: "Price", format: "currency" },
                { key: "min_price", label: "Minimum Price", format: "currency" }
              ]
            }
          ]
        }
      ]
    }
  ]
}, searchGoogleImagesOutputSchema = {
  fields: [
    { key: "success", label: "Success", format: "boolean" },
    {
      key: "data",
      label: "Data",
      children: [
        {
          key: "images_results",
          label: "Images",
          labelKey: "title",
          listItems: [
            { key: "position", label: "Position", format: "number" },
            { key: "title", label: "Title" },
            { key: "link", label: "Source Page Link", format: "url" },
            { key: "source", label: "Source" },
            { key: "original", label: "Original Image", format: "image" },
            { key: "original_width", label: "Width", format: "number" },
            { key: "original_height", label: "Height", format: "number" },
            { key: "thumbnail", label: "Thumbnail", format: "image" }
          ]
        },
        {
          key: "related_searches",
          label: "Related Searches",
          labelKey: "query",
          listItems: [{ key: "query", label: "Query" }]
        }
      ]
    }
  ]
}, searchGoogleNewsAiOutputSchema = {
  fields: [
    { key: "success", label: "Success", format: "boolean" },
    {
      key: "data",
      label: "Data",
      children: [
        {
          key: "news_results",
          label: "News",
          labelKey: "title",
          listItems: [
            { key: "position", label: "Position", format: "number" },
            { key: "title", label: "Title" },
            { key: "link", label: "Link", format: "url" },
            { key: "thumbnail", label: "Thumbnail", format: "image" },
            { key: "date", label: "Date" },
            { key: "iso_date", label: "ISO Date", format: "datetime" },
            {
              key: "source",
              label: "Source",
              children: [
                { key: "name", label: "Source Name" },
                { key: "icon", label: "Source Icon", format: "image" }
              ]
            }
          ]
        }
      ]
    }
  ]
}, searchYelpOutputSchema = {
  fields: [
    { key: "success", label: "Success", format: "boolean" },
    {
      key: "data",
      label: "Data",
      children: [
        {
          key: "organic_results",
          label: "Businesses",
          labelKey: "title",
          listItems: [
            { key: "position", label: "Position", format: "number" },
            { key: "title", label: "Title" },
            { key: "link", label: "Link", format: "url" },
            { key: "price", label: "Price Range" },
            { key: "rating", label: "Rating", format: "number" },
            { key: "reviews", label: "Review Count", format: "number" },
            { key: "neighborhoods", label: "Neighborhood" },
            { key: "open_state", label: "Open State" },
            { key: "snippet", label: "Snippet" },
            { key: "thumbnail", label: "Thumbnail", format: "image" },
            {
              key: "categories",
              label: "Categories",
              labelKey: "title",
              listItems: [
                { key: "title", label: "Category" },
                { key: "link", label: "Link", format: "url" }
              ]
            }
          ]
        }
      ]
    }
  ]
}, searchGooglePlayOutputSchema = {
  fields: [
    { key: "success", label: "Success", format: "boolean" },
    {
      key: "data",
      label: "Data",
      children: [
        {
          key: "organic_results",
          label: "Result Groups",
          listItems: [
            {
              key: "items",
              label: "Apps",
              labelKey: "title",
              listItems: [
                { key: "title", label: "Title" },
                { key: "link", label: "Store Link", format: "url" },
                { key: "product_id", label: "Product ID" },
                { key: "rating", label: "Rating", format: "number" },
                { key: "author", label: "Developer" },
                { key: "category", label: "Category" },
                { key: "downloads", label: "Downloads" },
                { key: "description", label: "Description" },
                { key: "thumbnail", label: "Thumbnail", format: "image" }
              ]
            }
          ]
        }
      ]
    }
  ]
}, searchGoogleShoppingOutputSchema = {
  fields: [
    { key: "success", label: "Success", format: "boolean" },
    {
      key: "data",
      label: "Data",
      children: [
        {
          key: "shopping_results",
          label: "Products",
          labelKey: "title",
          listItems: [
            { key: "position", label: "Position", format: "number" },
            { key: "title", label: "Title" },
            { key: "product_id", label: "Product ID" },
            { key: "source", label: "Source" },
            { key: "price", label: "Price" },
            { key: "extracted_price", label: "Extracted Price", format: "number" },
            { key: "old_price", label: "Old Price" },
            { key: "extracted_old_price", label: "Extracted Old Price", format: "number" },
            { key: "rating", label: "Rating", format: "number" },
            { key: "reviews", label: "Review Count", format: "number" },
            { key: "snippet", label: "Snippet" },
            { key: "thumbnail", label: "Thumbnail", format: "image" },
            { key: "tag", label: "Tag" },
            { key: "delivery", label: "Delivery" }
          ]
        }
      ]
    }
  ]
}, searchGoogleLocalServicesOutputSchema = {
  fields: [
    { key: "success", label: "Success", format: "boolean" },
    {
      key: "data",
      label: "Data",
      children: [
        {
          key: "local_ads",
          label: "Local Service Ads",
          labelKey: "title",
          listItems: [
            { key: "title", label: "Business Name" },
            { key: "link", label: "Profile Link", format: "url" },
            { key: "rating", label: "Rating", format: "number" },
            { key: "reviews", label: "Review Count", format: "number" },
            { key: "phone", label: "Phone" },
            { key: "type", label: "Service Type" },
            { key: "service_area", label: "Service Area" },
            { key: "years_in_business", label: "Years In Business", format: "number" },
            { key: "thumbnail", label: "Thumbnail", format: "image" },
            {
              key: "hours",
              label: "Hours",
              children: [{ key: "currently", label: "Currently" }]
            }
          ]
        }
      ]
    }
  ]
}, googleSearchOutputSchema = searchGoogleWebAiOutputSchema, googleNewsSearchOutputSchema = searchGoogleNewsAiOutputSchema, youtubeSearchOutputSchema = searchYoutubeAiOutputSchema, googleTrendsSearchOutputSchema = searchGoogleTrendsAiOutputSchema;

// ../ap/packages/pieces/community/serp-api/src/lib/actions/google-news-search.ts
var googleNewsSearch = (0, import_pieces_framework2.createAction)({
  auth: serpApiAuth,
  name: "google_news_search",
  classification: "SEARCH",
  displayName: "Google News Search",
  description: "Track recent news articles for keywords or brands to monitor media mentions and trending topics.",
  audience: "human",
  aiMetadata: { description: "Searches Google News via SerpApi for recent articles matching a query. Use to monitor brand or topic mentions in the news, surface breaking coverage, or gather current headlines, scoped by language and country. Read-only and idempotent; requires the query and a SerpApi API key.", idempotent: !0 },
  outputSchema: googleNewsSearchOutputSchema,
  props: {
    query: import_pieces_framework2.Property.ShortText({
      displayName: "Search Query",
      description: "News search query",
      required: !0
    }),
    hl: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Host Language (hl)",
      description: "Language for search results",
      required: !1,
      defaultValue: "en",
      options: {
        options: LANGUAGE_OPTIONS
      }
    }),
    gl: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Country (gl)",
      description: "Country for search results",
      required: !1,
      options: {
        options: COUNTRY_OPTIONS
      }
    }),
    no_cache: import_pieces_framework2.Property.StaticDropdown({
      displayName: "No Cache",
      description: "Force fresh results",
      required: !1,
      defaultValue: "false",
      options: {
        options: [
          { label: "false (Allow cache)", value: "false" },
          { label: "true (Force fresh results)", value: "true" }
        ]
      }
    }),
    async: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Async",
      description: "Submit search asynchronously",
      required: !1,
      defaultValue: "false",
      options: {
        options: [
          { label: "false (Synchronous)", value: "false" },
          { label: "true (Asynchronous)", value: "true" }
        ]
      }
    }),
    output: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Output",
      description: "Output format for results",
      required: !1,
      defaultValue: "json",
      options: {
        options: [
          { label: "JSON (Structured)", value: "json" },
          { label: "HTML (Raw)", value: "html" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    try {
      let client = new SerpApiClient({
        defaultTimeout: 3e4,
        defaultRetries: 3,
        defaultRetryDelay: 1e3,
        enableLogging: !1
      }), searchConfig = {
        api_key: auth.secret_text,
        engine: "google_news" /* GOOGLE_NEWS */,
        q: propsValue.query,
        hl: propsValue.hl,
        gl: propsValue.gl
      };
      propsValue.gl && (searchConfig.gl = propsValue.gl), propsValue.no_cache && (searchConfig.no_cache = propsValue.no_cache), propsValue.async && (searchConfig.async = propsValue.async), propsValue.output && (searchConfig.output = propsValue.output);
      let response = await client.executeSearch(searchConfig);
      return __spreadValues({
        success: !0
      }, response);
    } catch (error) {
      return {
        success: !1,
        error: error instanceof Error ? error.message : "Unknown error occurred",
        error_type: error instanceof Error ? error.constructor.name : "UnknownError",
        timestamp: (/* @__PURE__ */ new Date()).toISOString(),
        search_query: propsValue.query
      };
    }
  }
});

// ../ap/packages/pieces/community/serp-api/src/lib/actions/google-search.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/serp-api/src/lib/constants/google-domains.ts
var GOOGLE_DOMAIN_OPTIONS = [
  { label: "google.com (United States)", value: "google.com" },
  { label: "google.ad (Andorra)", value: "google.ad" },
  { label: "google.ae (United Arab Emirates)", value: "google.ae" },
  { label: "google.al (Albania)", value: "google.al" },
  { label: "google.am (Armenia)", value: "google.am" },
  { label: "google.as (American Samoa)", value: "google.as" },
  { label: "google.at (Austria)", value: "google.at" },
  { label: "google.az (Azerbaijan)", value: "google.az" },
  { label: "google.ba (Bosnia and Herzegovina)", value: "google.ba" },
  { label: "google.be (Belgium)", value: "google.be" },
  { label: "google.bf (Burkina Faso)", value: "google.bf" },
  { label: "google.bg (Bulgaria)", value: "google.bg" },
  { label: "google.bi (Burundi)", value: "google.bi" },
  { label: "google.bj (Benin)", value: "google.bj" },
  { label: "google.bs (Bahamas)", value: "google.bs" },
  { label: "google.bt (Bhutan)", value: "google.bt" },
  { label: "google.by (Belarus)", value: "google.by" },
  { label: "google.ca (Canada)", value: "google.ca" },
  { label: "google.cd (Democratic Republic of the Congo)", value: "google.cd" },
  { label: "google.cf (Central African Republic)", value: "google.cf" },
  { label: "google.cg (Republic of the Congo)", value: "google.cg" },
  { label: "google.ch (Switzerland)", value: "google.ch" },
  { label: "google.ci (Ivory Coast)", value: "google.ci" },
  { label: "google.cl (Chile)", value: "google.cl" },
  { label: "google.cm (Cameroon)", value: "google.cm" },
  { label: "google.co.ao (Angola)", value: "google.co.ao" },
  { label: "google.co.bw (Botswana)", value: "google.co.bw" },
  { label: "google.co.ck (Cook Islands)", value: "google.co.ck" },
  { label: "google.co.cr (Costa Rica)", value: "google.co.cr" },
  { label: "google.co.id (Indonesia)", value: "google.co.id" },
  { label: "google.co.il (Israel)", value: "google.co.il" },
  { label: "google.co.in (India)", value: "google.co.in" },
  { label: "google.co.jp (Japan)", value: "google.co.jp" },
  { label: "google.co.ke (Kenya)", value: "google.co.ke" },
  { label: "google.co.kr (South Korea)", value: "google.co.kr" },
  { label: "google.co.ls (Lesotho)", value: "google.co.ls" },
  { label: "google.co.ma (Morocco)", value: "google.co.ma" },
  { label: "google.co.mz (Mozambique)", value: "google.co.mz" },
  { label: "google.co.nz (New Zealand)", value: "google.co.nz" },
  { label: "google.co.th (Thailand)", value: "google.co.th" },
  { label: "google.co.tz (Tanzania)", value: "google.co.tz" },
  { label: "google.co.ug (Uganda)", value: "google.co.ug" },
  { label: "google.co.uk (United Kingdom)", value: "google.co.uk" },
  { label: "google.co.uz (Uzbekistan)", value: "google.co.uz" },
  { label: "google.co.ve (Venezuela)", value: "google.co.ve" },
  { label: "google.co.vi (United States Virgin Islands)", value: "google.co.vi" },
  { label: "google.co.za (South Africa)", value: "google.co.za" },
  { label: "google.co.zm (Zambia)", value: "google.co.zm" },
  { label: "google.co.zw (Zimbabwe)", value: "google.co.zw" },
  { label: "google.com.af (Afghanistan)", value: "google.com.af" },
  { label: "google.com.ag (Antigua and Barbuda)", value: "google.com.ag" },
  { label: "google.com.ai (Anguilla)", value: "google.com.ai" },
  { label: "google.com.ar (Argentina)", value: "google.com.ar" },
  { label: "google.com.au (Australia)", value: "google.com.au" },
  { label: "google.com.bd (Bangladesh)", value: "google.com.bd" },
  { label: "google.com.bh (Bahrain)", value: "google.com.bh" },
  { label: "google.com.bn (Brunei)", value: "google.com.bn" },
  { label: "google.com.bo (Bolivia)", value: "google.com.bo" },
  { label: "google.com.br (Brazil)", value: "google.com.br" },
  { label: "google.com.bz (Belize)", value: "google.com.bz" },
  { label: "google.com.co (Colombia)", value: "google.com.co" },
  { label: "google.com.cu (Cuba)", value: "google.com.cu" },
  { label: "google.com.cy (Cyprus)", value: "google.com.cy" },
  { label: "google.com.do (Dominican Republic)", value: "google.com.do" },
  { label: "google.com.ec (Ecuador)", value: "google.com.ec" },
  { label: "google.com.eg (Egypt)", value: "google.com.eg" },
  { label: "google.com.et (Ethiopia)", value: "google.com.et" },
  { label: "google.com.fj (Fiji)", value: "google.com.fj" },
  { label: "google.com.gh (Ghana)", value: "google.com.gh" },
  { label: "google.com.gi (Gibraltar)", value: "google.com.gi" },
  { label: "google.com.gt (Guatemala)", value: "google.com.gt" },
  { label: "google.com.hk (Hong Kong)", value: "google.com.hk" },
  { label: "google.com.jm (Jamaica)", value: "google.com.jm" },
  { label: "google.com.kh (Cambodia)", value: "google.com.kh" },
  { label: "google.com.kw (Kuwait)", value: "google.com.kw" },
  { label: "google.com.lb (Lebanon)", value: "google.com.lb" },
  { label: "google.com.ly (Libya)", value: "google.com.ly" },
  { label: "google.com.mm (Myanmar)", value: "google.com.mm" },
  { label: "google.com.mt (Malta)", value: "google.com.mt" },
  { label: "google.com.mx (Mexico)", value: "google.com.mx" },
  { label: "google.com.my (Malaysia)", value: "google.com.my" },
  { label: "google.com.na (Namibia)", value: "google.com.na" },
  { label: "google.com.ng (Nigeria)", value: "google.com.ng" },
  { label: "google.com.ni (Nicaragua)", value: "google.com.ni" },
  { label: "google.com.np (Nepal)", value: "google.com.np" },
  { label: "google.com.om (Oman)", value: "google.com.om" },
  { label: "google.com.pa (Panama)", value: "google.com.pa" },
  { label: "google.com.pe (Peru)", value: "google.com.pe" },
  { label: "google.com.pg (Papua New Guinea)", value: "google.com.pg" },
  { label: "google.com.ph (Philippines)", value: "google.com.ph" },
  { label: "google.com.pk (Pakistan)", value: "google.com.pk" },
  { label: "google.com.pr (Puerto Rico)", value: "google.com.pr" },
  { label: "google.com.py (Paraguay)", value: "google.com.py" },
  { label: "google.com.qa (Qatar)", value: "google.com.qa" },
  { label: "google.com.sa (Saudi Arabia)", value: "google.com.sa" },
  { label: "google.com.sb (Solomon Islands)", value: "google.com.sb" },
  { label: "google.com.sg (Singapore)", value: "google.com.sg" },
  { label: "google.com.sl (Sierra Leone)", value: "google.com.sl" },
  { label: "google.com.sv (El Salvador)", value: "google.com.sv" },
  { label: "google.com.tj (Tajikistan)", value: "google.com.tj" },
  { label: "google.com.tr (Turkey)", value: "google.com.tr" },
  { label: "google.com.tw (Taiwan)", value: "google.com.tw" },
  { label: "google.com.ua (Ukraine)", value: "google.com.ua" },
  { label: "google.com.uy (Uruguay)", value: "google.com.uy" },
  { label: "google.com.vc (Saint Vincent and the Grenadines)", value: "google.com.vc" },
  { label: "google.com.vn (Vietnam)", value: "google.com.vn" },
  { label: "google.cv (Cape Verde)", value: "google.cv" },
  { label: "google.cz (Czech Republic)", value: "google.cz" },
  { label: "google.de (Germany)", value: "google.de" },
  { label: "google.dj (Djibouti)", value: "google.dj" },
  { label: "google.dk (Denmark)", value: "google.dk" },
  { label: "google.dm (Dominica)", value: "google.dm" },
  { label: "google.dz (Algeria)", value: "google.dz" },
  { label: "google.ee (Estonia)", value: "google.ee" },
  { label: "google.es (Spain)", value: "google.es" },
  { label: "google.fi (Finland)", value: "google.fi" },
  { label: "google.fm (Micronesia, Federated States of)", value: "google.fm" },
  { label: "google.fr (France)", value: "google.fr" },
  { label: "google.ga (Gabon)", value: "google.ga" },
  { label: "google.ge (Georgia)", value: "google.ge" },
  { label: "google.gl (Greenland)", value: "google.gl" },
  { label: "google.gm (Gambia)", value: "google.gm" },
  { label: "google.gp (Guadeloupe)", value: "google.gp" },
  { label: "google.gr (Greece)", value: "google.gr" },
  { label: "google.gy (Guyana)", value: "google.gy" },
  { label: "google.hn (Honduras)", value: "google.hn" },
  { label: "google.hr (Croatia)", value: "google.hr" },
  { label: "google.ht (Haiti)", value: "google.ht" },
  { label: "google.hu (Hungary)", value: "google.hu" },
  { label: "google.ie (Ireland)", value: "google.ie" },
  { label: "google.iq (Iraq)", value: "google.iq" },
  { label: "google.is (Iceland)", value: "google.is" },
  { label: "google.it (Italy)", value: "google.it" },
  { label: "google.je (Jersey)", value: "google.je" },
  { label: "google.jo (Jordan)", value: "google.jo" },
  { label: "google.kg (Kyrgyzstan)", value: "google.kg" },
  { label: "google.ki (Kiribati)", value: "google.ki" },
  { label: "google.kz (Kazakhstan)", value: "google.kz" },
  { label: "google.la (Laos)", value: "google.la" },
  { label: "google.li (Liechtenstein)", value: "google.li" },
  { label: "google.lk (Sri Lanka)", value: "google.lk" },
  { label: "google.lt (Lithuania)", value: "google.lt" },
  { label: "google.lu (Luxembourg)", value: "google.lu" },
  { label: "google.lv (Latvia)", value: "google.lv" },
  { label: "google.md (Moldova)", value: "google.md" },
  { label: "google.mg (Madagascar)", value: "google.mg" },
  { label: "google.mk (Macedonia)", value: "google.mk" },
  { label: "google.ml (Mali)", value: "google.ml" },
  { label: "google.mn (Mongolia)", value: "google.mn" },
  { label: "google.ms (Montserrat)", value: "google.ms" },
  { label: "google.mu (Mauritius)", value: "google.mu" },
  { label: "google.mv (Maldives)", value: "google.mv" },
  { label: "google.mw (Malawi)", value: "google.mw" },
  { label: "google.ne (Niger)", value: "google.ne" },
  { label: "google.nl (Netherlands)", value: "google.nl" },
  { label: "google.no (Norway)", value: "google.no" },
  { label: "google.nr (Nauru)", value: "google.nr" },
  { label: "google.nu (Niue)", value: "google.nu" },
  { label: "google.pl (Poland)", value: "google.pl" },
  { label: "google.ps (Palestine)", value: "google.ps" },
  { label: "google.pt (Portugal)", value: "google.pt" },
  { label: "google.ro (Romania)", value: "google.ro" },
  { label: "google.rs (Serbia)", value: "google.rs" },
  { label: "google.ru (Russia)", value: "google.ru" },
  { label: "google.rw (Rwanda)", value: "google.rw" },
  { label: "google.sc (Seychelles)", value: "google.sc" },
  { label: "google.se (Sweden)", value: "google.se" },
  { label: "google.sh (Saint Helena, Ascension and Tristan da Cunha)", value: "google.sh" },
  { label: "google.si (Slovenia)", value: "google.si" },
  { label: "google.sk (Slovakia)", value: "google.sk" },
  { label: "google.sm (San Marino)", value: "google.sm" },
  { label: "google.sn (Senegal)", value: "google.sn" },
  { label: "google.so (Somalia)", value: "google.so" },
  { label: "google.sr (Suriname)", value: "google.sr" },
  { label: "google.td (Chad)", value: "google.td" },
  { label: "google.tg (Togo)", value: "google.tg" },
  { label: "google.tk (Tokelau)", value: "google.tk" },
  { label: "google.tl (Timor-Leste)", value: "google.tl" },
  { label: "google.tm (Turkmenistan)", value: "google.tm" },
  { label: "google.tn (Tunisia)", value: "google.tn" },
  { label: "google.to (Tonga)", value: "google.to" },
  { label: "google.tt (Trinidad and Tobago)", value: "google.tt" },
  { label: "google.vg (British Virgin Islands)", value: "google.vg" },
  { label: "google.vu (Vanuatu)", value: "google.vu" },
  { label: "google.ws (Samoa)", value: "google.ws" }
];

// ../ap/packages/pieces/community/serp-api/src/lib/actions/google-search.ts
var googleSearch = (0, import_pieces_framework3.createAction)({
  auth: serpApiAuth,
  name: "google_search",
  classification: "SEARCH",
  displayName: "Google Search",
  description: "Retrieves organic search results for specific keywords with advanced filtering options for SEO monitoring and competitor analysis.",
  audience: "human",
  aiMetadata: { description: "Runs a Google web search via SerpApi and returns organic results for a query. Use to look up current web information, check rankings, or research a topic with controls for location, language, country, device, and pagination. Read-only and idempotent; requires the search query and a SerpApi API key.", idempotent: !0 },
  outputSchema: googleSearchOutputSchema,
  props: {
    query: import_pieces_framework3.Property.ShortText({
      displayName: "Search Query",
      description: "Search query to execute",
      required: !0
    }),
    location_search: import_pieces_framework3.Property.ShortText({
      displayName: "Location Search",
      description: "Type location name to search",
      required: !1
    }),
    location: import_pieces_framework3.Property.Dropdown({
      auth: serpApiAuth,
      displayName: "Location",
      description: "Geographic location for results",
      required: !1,
      refreshers: ["location_search"],
      options: async ({ auth, location_search }) => {
        if (!auth || !location_search)
          return {
            disabled: !0,
            options: [],
            placeholder: "Type a location to search..."
          };
        try {
          let response = await import_pieces_common2.httpClient.sendRequest({
            method: import_pieces_common2.HttpMethod.GET,
            url: "https://serpapi.com/locations.json",
            queryParams: {
              q: location_search,
              limit: "10"
            }
          });
          return response.body && Array.isArray(response.body) ? {
            disabled: !1,
            options: response.body.map((location) => ({
              label: `${location.name}${location.country ? `, ${location.country}` : ""}`,
              value: location.canonical_name || location.name
            }))
          } : {
            disabled: !1,
            options: [],
            placeholder: "No locations found"
          };
        } catch (error) {
          return console.error("Error fetching locations:", error), {
            disabled: !1,
            options: [],
            placeholder: "Error searching locations. Please check your API key."
          };
        }
      }
    }),
    hl: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Host Language (hl)",
      description: "Language for search results",
      required: !1,
      defaultValue: "en",
      options: {
        options: LANGUAGE_OPTIONS
      }
    }),
    google_domain: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Google Domain",
      description: "Google domain to use",
      required: !1,
      defaultValue: "google.com",
      options: {
        options: GOOGLE_DOMAIN_OPTIONS
      }
    }),
    gl: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Country (gl)",
      description: "Country for search results",
      required: !1,
      options: {
        options: COUNTRY_OPTIONS
      }
    }),
    no_cache: import_pieces_framework3.Property.StaticDropdown({
      displayName: "No Cache",
      description: "Force fresh results",
      required: !1,
      defaultValue: "false",
      options: {
        options: [
          { label: "false (Allow cache)", value: "false" },
          { label: "true (Force fresh results)", value: "true" }
        ]
      }
    }),
    async: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Async",
      description: "Submit search asynchronously",
      required: !1,
      defaultValue: "false",
      options: {
        options: [
          { label: "false (Synchronous)", value: "false" },
          { label: "true (Asynchronous)", value: "true" }
        ]
      }
    }),
    num_results: import_pieces_framework3.Property.Number({
      displayName: "Number of Results",
      description: "Number of results to return (1-100)",
      required: !1,
      defaultValue: 10
    }),
    start: import_pieces_framework3.Property.Number({
      displayName: "Start Position",
      description: "Starting position for pagination (0-990)",
      required: !1,
      defaultValue: 0
    }),
    safe: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Safe Search",
      description: "Safe search filtering level",
      required: !1,
      defaultValue: "active",
      options: {
        options: [
          { label: "Active (Enabled)", value: "active" },
          { label: "Off (Disabled)", value: "off" }
        ]
      }
    }),
    device: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Device Type",
      description: "Device type for search simulation",
      required: !1,
      defaultValue: "desktop",
      options: {
        options: [
          { label: "Desktop", value: "desktop" },
          { label: "Mobile", value: "mobile" },
          { label: "Tablet", value: "tablet" }
        ]
      }
    }),
    filter: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Result Filter",
      description: "Filter duplicate results",
      required: !1,
      defaultValue: "0",
      options: {
        options: [
          { label: "Show similar results", value: "0" },
          { label: "Hide similar results", value: "1" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    try {
      let client = new SerpApiClient({
        defaultTimeout: 3e4,
        defaultRetries: 3,
        defaultRetryDelay: 1e3,
        enableLogging: !1
      }), searchConfig = {
        api_key: auth.secret_text,
        engine: "google" /* GOOGLE */,
        q: propsValue.query,
        hl: propsValue.hl,
        num: propsValue.num_results,
        start: propsValue.start,
        safe: propsValue.safe,
        device: propsValue.device,
        filter: propsValue.filter
      };
      propsValue.location && (searchConfig.location = propsValue.location), propsValue.google_domain && (searchConfig.google_domain = propsValue.google_domain), propsValue.gl && (searchConfig.gl = propsValue.gl), propsValue.no_cache && (searchConfig.no_cache = propsValue.no_cache), propsValue.async && (searchConfig.async = propsValue.async);
      let response = await client.executeSearch(searchConfig, {
        timeout: 3e4,
        retries: 3,
        retryDelay: 1e3
      });
      return __spreadValues({
        success: !0
      }, response);
    } catch (error) {
      return {
        success: !1,
        error: error instanceof Error ? error.message : "Unknown error occurred",
        error_type: error instanceof Error ? error.constructor.name : "UnknownError",
        timestamp: (/* @__PURE__ */ new Date()).toISOString(),
        search_query: propsValue.query
      };
    }
  }
});

// ../ap/packages/pieces/community/serp-api/src/lib/actions/google-trends-search.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var googleTrendsSearch = (0, import_pieces_framework4.createAction)({
  auth: serpApiAuth,
  name: "google_trends_search",
  classification: "SEARCH",
  displayName: "Google Trends Search",
  description: "Discover trending keywords over time to inform content strategy and market research with geographic insights.",
  audience: "human",
  aiMetadata: { description: "Queries Google Trends via SerpApi for interest in a keyword over time and across regions. Use to gauge a topic's popularity trajectory, compare geographic interest, or find related/rising queries for content and market research. Read-only and idempotent; requires the query, a SerpApi API key, and a chosen data type (interest over time, by region, related topics, or related queries).", idempotent: !0 },
  outputSchema: googleTrendsSearchOutputSchema,
  props: {
    query: import_pieces_framework4.Property.ShortText({
      displayName: "Search Query",
      description: 'The keyword or topic to analyze trends for (e.g., "artificial intelligence", "cryptocurrency", "climate change")',
      required: !0
    }),
    hl: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Host Language (hl)",
      description: "Language for search results",
      required: !1,
      defaultValue: "en",
      options: {
        options: [
          { label: "English", value: "en" },
          { label: "Afrikaans", value: "af" },
          { label: "Akan", value: "ak" },
          { label: "Albanian", value: "sq" },
          { label: "Amharic", value: "am" },
          { label: "Arabic", value: "ar" },
          { label: "Armenian", value: "hy" },
          { label: "Azerbaijani", value: "az" },
          { label: "Basque", value: "eu" },
          { label: "Belarusian", value: "be" },
          { label: "Bemba", value: "bem" },
          { label: "Bengali", value: "bn" },
          { label: "Bihari", value: "bh" },
          { label: "Bosnian", value: "bs" },
          { label: "Breton", value: "br" },
          { label: "Bulgarian", value: "bg" },
          { label: "Bhutanese", value: "bt" },
          { label: "Cambodian", value: "km" },
          { label: "Catalan", value: "ca" },
          { label: "Cherokee", value: "chr" },
          { label: "Chichewa", value: "ny" },
          { label: "Chinese (Simplified)", value: "zh-cn" },
          { label: "Chinese (Traditional)", value: "zh-tw" },
          { label: "Corsican", value: "co" },
          { label: "Croatian", value: "hr" },
          { label: "Czech", value: "cs" },
          { label: "Danish", value: "da" },
          { label: "Dutch", value: "nl" },
          { label: "Esperanto", value: "eo" },
          { label: "Estonian", value: "et" },
          { label: "Ewe", value: "ee" },
          { label: "Faroese", value: "fo" },
          { label: "Filipino", value: "tl" },
          { label: "Finnish", value: "fi" },
          { label: "French", value: "fr" },
          { label: "Frisian", value: "fy" },
          { label: "Ga", value: "gaa" },
          { label: "Galician", value: "gl" },
          { label: "Georgian", value: "ka" },
          { label: "German", value: "de" },
          { label: "Greek", value: "el" },
          { label: "Greenlandic", value: "kl" },
          { label: "Guarani", value: "gn" },
          { label: "Gujarati", value: "gu" },
          { label: "Haitian Creole", value: "ht" },
          { label: "Hausa", value: "ha" },
          { label: "Hawaiian", value: "haw" },
          { label: "Hebrew", value: "he" },
          { label: "Hindi", value: "hi" },
          { label: "Hungarian", value: "hu" },
          { label: "Icelandic", value: "is" },
          { label: "Igbo", value: "ig" },
          { label: "Indonesian", value: "id" },
          { label: "Interlingua", value: "ia" },
          { label: "Irish", value: "ga" },
          { label: "Italian", value: "it" },
          { label: "Japanese", value: "ja" },
          { label: "Javanese", value: "jw" },
          { label: "Kannada", value: "kn" },
          { label: "Kazakh", value: "kk" },
          { label: "Kinyarwanda", value: "rw" },
          { label: "Kirundi", value: "rn" },
          { label: "Kongo", value: "kg" },
          { label: "Korean", value: "ko" },
          { label: "Krio (Sierra Leone)", value: "kri" },
          { label: "Kurdish", value: "ku" },
          { label: "Kurdish (Soran\xEE)", value: "ckb" },
          { label: "Kyrgyz", value: "ky" },
          { label: "Laothian", value: "lo" },
          { label: "Latin", value: "la" },
          { label: "Latvian", value: "lv" },
          { label: "Lingala", value: "ln" },
          { label: "Lithuanian", value: "lt" },
          { label: "Lozi", value: "loz" },
          { label: "Luganda", value: "lg" },
          { label: "Luo", value: "ach" },
          { label: "Macedonian", value: "mk" },
          { label: "Malagasy", value: "mg" },
          { label: "Malay", value: "ms" },
          { label: "Malayalam", value: "ml" },
          { label: "Maltese", value: "mt" },
          { label: "Maldives", value: "mv" },
          { label: "Maori", value: "mi" },
          { label: "Marathi", value: "mr" },
          { label: "Mauritian Creole", value: "mfe" },
          { label: "Moldavian", value: "mo" },
          { label: "Mongolian", value: "mn" },
          { label: "Montenegrin", value: "sr-me" },
          { label: "Myanmar", value: "my" },
          { label: "Nepali", value: "ne" },
          { label: "Nigerian Pidgin", value: "pcm" },
          { label: "Northern Sotho", value: "nso" },
          { label: "Norwegian", value: "no" },
          { label: "Norwegian (Nynorsk)", value: "nn" },
          { label: "Occitan", value: "oc" },
          { label: "Oriya", value: "or" },
          { label: "Oromo", value: "om" },
          { label: "Pashto", value: "ps" },
          { label: "Persian", value: "fa" },
          { label: "Polish", value: "pl" },
          { label: "Portuguese", value: "pt" },
          { label: "Portuguese (Brazil)", value: "pt-br" },
          { label: "Portuguese (Portugal)", value: "pt-pt" },
          { label: "Punjabi", value: "pa" },
          { label: "Quechua", value: "qu" },
          { label: "Romanian", value: "ro" },
          { label: "Romansh", value: "rm" },
          { label: "Runyakitara", value: "nyn" },
          { label: "Russian", value: "ru" },
          { label: "Samoa", value: "ws" },
          { label: "Scots Gaelic", value: "gd" },
          { label: "Serbian", value: "sr" },
          { label: "Serbo-Croatian", value: "sh" },
          { label: "Sesotho", value: "st" },
          { label: "Setswana", value: "tn" },
          { label: "Seychellois Creole", value: "crs" },
          { label: "Shona", value: "sn" },
          { label: "Sindhi", value: "sd" },
          { label: "Sinhalese", value: "si" },
          { label: "Slovak", value: "sk" },
          { label: "Slovenian", value: "sl" },
          { label: "Somali", value: "so" },
          { label: "Spanish", value: "es" },
          { label: "Spanish (Latin American)", value: "es-419" },
          { label: "Sundanese", value: "su" },
          { label: "Swahili", value: "sw" },
          { label: "Swedish", value: "sv" },
          { label: "Tajik", value: "tg" },
          { label: "Tamil", value: "ta" },
          { label: "Tatar", value: "tt" },
          { label: "Telugu", value: "te" },
          { label: "Thai", value: "th" },
          { label: "Tigrinya", value: "ti" },
          { label: "Tonga", value: "to" },
          { label: "Tshiluba", value: "lua" },
          { label: "Tumbuka", value: "tum" },
          { label: "Turkish", value: "tr" },
          { label: "Turkmen", value: "tk" },
          { label: "Twi", value: "tw" },
          { label: "Uighur", value: "ug" },
          { label: "Ukrainian", value: "uk" },
          { label: "Urdu", value: "ur" },
          { label: "Uzbek", value: "uz" },
          { label: "Vanuatu", value: "vu" },
          { label: "Vietnamese", value: "vi" },
          { label: "Welsh", value: "cy" },
          { label: "Wolof", value: "wo" },
          { label: "Xhosa", value: "xh" },
          { label: "Yiddish", value: "yi" },
          { label: "Yoruba", value: "yo" },
          { label: "Zulu", value: "zu" }
        ]
      }
    }),
    geo: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Geographic Location",
      description: "Location from where you want the search to originate.",
      required: !1,
      defaultValue: "",
      options: {
        options: [
          { label: "Worldwide", value: "" },
          { label: "Afghanistan", value: "AF" },
          { label: "Albania", value: "AL" },
          { label: "Algeria", value: "DZ" },
          { label: "American Samoa", value: "AS" },
          { label: "Andorra", value: "AD" },
          { label: "Angola", value: "AO" },
          { label: "Anguilla", value: "AI" },
          { label: "Antarctica", value: "AQ" },
          { label: "Antigua & Barbuda", value: "AG" },
          { label: "Argentina", value: "AR" },
          { label: "Armenia", value: "AM" },
          { label: "Aruba", value: "AW" },
          { label: "Australia", value: "AU" },
          { label: "Austria", value: "AT" },
          { label: "Azerbaijan", value: "AZ" },
          { label: "Bahamas", value: "BS" },
          { label: "Bahrain", value: "BH" },
          { label: "Bangladesh", value: "BD" },
          { label: "Barbados", value: "BB" },
          { label: "Belarus", value: "BY" },
          { label: "Belgium", value: "BE" },
          { label: "Belize", value: "BZ" },
          { label: "Benin", value: "BJ" },
          { label: "Bermuda", value: "BM" },
          { label: "Bhutan", value: "BT" },
          { label: "Bolivia", value: "BO" },
          { label: "Bosnia & Herzegovina", value: "BA" },
          { label: "Botswana", value: "BW" },
          { label: "Brazil", value: "BR" },
          { label: "British Virgin Islands", value: "VG" },
          { label: "Brunei", value: "BN" },
          { label: "Bulgaria", value: "BG" },
          { label: "Burkina Faso", value: "BF" },
          { label: "Burundi", value: "BI" },
          { label: "Cambodia", value: "KH" },
          { label: "Cameroon", value: "CM" },
          { label: "Canada", value: "CA" },
          { label: "Cape Verde", value: "CV" },
          { label: "Cayman Islands", value: "KY" },
          { label: "Central African Republic", value: "CF" },
          { label: "Chad", value: "TD" },
          { label: "Chile", value: "CL" },
          { label: "China", value: "CN" },
          { label: "Colombia", value: "CO" },
          { label: "Comoros", value: "KM" },
          { label: "Congo - Brazzaville", value: "CG" },
          { label: "Congo - Kinshasa", value: "CD" },
          { label: "Cook Islands", value: "CK" },
          { label: "Costa Rica", value: "CR" },
          { label: "Croatia", value: "HR" },
          { label: "Cuba", value: "CU" },
          { label: "Cyprus", value: "CY" },
          { label: "Czechia", value: "CZ" },
          { label: "C\xF4te d'Ivoire", value: "CI" },
          { label: "Denmark", value: "DK" },
          { label: "Djibouti", value: "DJ" },
          { label: "Dominica", value: "DM" },
          { label: "Dominican Republic", value: "DO" },
          { label: "Ecuador", value: "EC" },
          { label: "Egypt", value: "EG" },
          { label: "El Salvador", value: "SV" },
          { label: "Equatorial Guinea", value: "GQ" },
          { label: "Eritrea", value: "ER" },
          { label: "Estonia", value: "EE" },
          { label: "Eswatini", value: "SZ" },
          { label: "Ethiopia", value: "ET" },
          { label: "Falkland Islands", value: "FK" },
          { label: "Faroe Islands", value: "FO" },
          { label: "Fiji", value: "FJ" },
          { label: "Finland", value: "FI" },
          { label: "France", value: "FR" },
          { label: "French Guiana", value: "GF" },
          { label: "French Polynesia", value: "PF" },
          { label: "Gabon", value: "GA" },
          { label: "Gambia", value: "GM" },
          { label: "Georgia", value: "GE" },
          { label: "Germany", value: "DE" },
          { label: "Ghana", value: "GH" },
          { label: "Gibraltar", value: "GI" },
          { label: "Greece", value: "GR" },
          { label: "Greenland", value: "GL" },
          { label: "Grenada", value: "GD" },
          { label: "Guadeloupe", value: "GP" },
          { label: "Guam", value: "GU" },
          { label: "Guatemala", value: "GT" },
          { label: "Guernsey", value: "GG" },
          { label: "Guinea", value: "GN" },
          { label: "Guinea-Bissau", value: "GW" },
          { label: "Guyana", value: "GY" },
          { label: "Haiti", value: "HT" },
          { label: "Honduras", value: "HN" },
          { label: "Hong Kong SAR China", value: "HK" },
          { label: "Hungary", value: "HU" },
          { label: "Iceland", value: "IS" },
          { label: "India", value: "IN" },
          { label: "Indonesia", value: "ID" },
          { label: "Iran", value: "IR" },
          { label: "Iraq", value: "IQ" },
          { label: "Ireland", value: "IE" },
          { label: "Isle of Man", value: "IM" },
          { label: "Israel", value: "IL" },
          { label: "Italy", value: "IT" },
          { label: "Jamaica", value: "JM" },
          { label: "Japan", value: "JP" },
          { label: "Jersey", value: "JE" },
          { label: "Jordan", value: "JO" },
          { label: "Kazakhstan", value: "KZ" },
          { label: "Kenya", value: "KE" },
          { label: "Kiribati", value: "KI" },
          { label: "Kuwait", value: "KW" },
          { label: "Kyrgyzstan", value: "KG" },
          { label: "Laos", value: "LA" },
          { label: "Latvia", value: "LV" },
          { label: "Lebanon", value: "LB" },
          { label: "Lesotho", value: "LS" },
          { label: "Liberia", value: "LR" },
          { label: "Libya", value: "LY" },
          { label: "Liechtenstein", value: "LI" },
          { label: "Lithuania", value: "LT" },
          { label: "Luxembourg", value: "LU" },
          { label: "Macao SAR China", value: "MO" },
          { label: "Madagascar", value: "MG" },
          { label: "Malawi", value: "MW" },
          { label: "Malaysia", value: "MY" },
          { label: "Maldives", value: "MV" },
          { label: "Mali", value: "ML" },
          { label: "Malta", value: "MT" },
          { label: "Marshall Islands", value: "MH" },
          { label: "Martinique", value: "MQ" },
          { label: "Mauritania", value: "MR" },
          { label: "Mauritius", value: "MU" },
          { label: "Mayotte", value: "YT" },
          { label: "Mexico", value: "MX" },
          { label: "Micronesia", value: "FM" },
          { label: "Moldova", value: "MD" },
          { label: "Monaco", value: "MC" },
          { label: "Mongolia", value: "MN" },
          { label: "Montenegro", value: "ME" },
          { label: "Montserrat", value: "MS" },
          { label: "Morocco", value: "MA" },
          { label: "Mozambique", value: "MZ" },
          { label: "Myanmar (Burma)", value: "MM" },
          { label: "Namibia", value: "NA" },
          { label: "Nauru", value: "NR" },
          { label: "Nepal", value: "NP" },
          { label: "Netherlands", value: "NL" },
          { label: "New Caledonia", value: "NC" },
          { label: "New Zealand", value: "NZ" },
          { label: "Nicaragua", value: "NI" },
          { label: "Niger", value: "NE" },
          { label: "Nigeria", value: "NG" },
          { label: "Niue", value: "NU" },
          { label: "Norfolk Island", value: "NF" },
          { label: "North Korea", value: "KP" },
          { label: "North Macedonia", value: "MK" },
          { label: "Northern Mariana Islands", value: "MP" },
          { label: "Norway", value: "NO" },
          { label: "Oman", value: "OM" },
          { label: "Pakistan", value: "PK" },
          { label: "Palau", value: "PW" },
          { label: "Palestinian Territories", value: "PS" },
          { label: "Panama", value: "PA" },
          { label: "Papua New Guinea", value: "PG" },
          { label: "Paraguay", value: "PY" },
          { label: "Peru", value: "PE" },
          { label: "Philippines", value: "PH" },
          { label: "Pitcairn Islands", value: "PN" },
          { label: "Poland", value: "PL" },
          { label: "Portugal", value: "PT" },
          { label: "Puerto Rico", value: "PR" },
          { label: "Qatar", value: "QA" },
          { label: "Romania", value: "RO" },
          { label: "Russia", value: "RU" },
          { label: "Rwanda", value: "RW" },
          { label: "R\xE9union", value: "RE" },
          { label: "Samoa", value: "WS" },
          { label: "San Marino", value: "SM" },
          { label: "Saudi Arabia", value: "SA" },
          { label: "Senegal", value: "SN" },
          { label: "Serbia", value: "RS" },
          { label: "Seychelles", value: "SC" },
          { label: "Sierra Leone", value: "SL" },
          { label: "Singapore", value: "SG" },
          { label: "Sint Maarten", value: "SX" },
          { label: "Slovakia", value: "SK" },
          { label: "Slovenia", value: "SI" },
          { label: "Solomon Islands", value: "SB" },
          { label: "Somalia", value: "SO" },
          { label: "South Africa", value: "ZA" },
          { label: "South Korea", value: "KR" },
          { label: "South Sudan", value: "SS" },
          { label: "Spain", value: "ES" },
          { label: "Sri Lanka", value: "LK" },
          { label: "St. Barth\xE9lemy", value: "BL" },
          { label: "St. Helena", value: "SH" },
          { label: "St. Kitts & Nevis", value: "KN" },
          { label: "St. Lucia", value: "LC" },
          { label: "St. Martin", value: "MF" },
          { label: "St. Pierre & Miquelon", value: "PM" },
          { label: "St. Vincent & Grenadines", value: "VC" },
          { label: "Sudan", value: "SD" },
          { label: "Suriname", value: "SR" },
          { label: "Svalbard & Jan Mayen", value: "SJ" },
          { label: "Sweden", value: "SE" },
          { label: "Switzerland", value: "CH" },
          { label: "Syria", value: "SY" },
          { label: "S\xE3o Tom\xE9 & Pr\xEDncipe", value: "ST" },
          { label: "Taiwan", value: "TW" },
          { label: "Tajikistan", value: "TJ" },
          { label: "Tanzania", value: "TZ" },
          { label: "Thailand", value: "TH" },
          { label: "Timor-Leste", value: "TL" },
          { label: "Togo", value: "TG" },
          { label: "Tokelau", value: "TK" },
          { label: "Tonga", value: "TO" },
          { label: "Trinidad & Tobago", value: "TT" },
          { label: "Tunisia", value: "TN" },
          { label: "Turkey", value: "TR" },
          { label: "Turkmenistan", value: "TM" },
          { label: "Turks & Caicos Islands", value: "TC" },
          { label: "Tuvalu", value: "TV" },
          { label: "U.S. Outlying Islands", value: "UM" },
          { label: "U.S. Virgin Islands", value: "VI" },
          { label: "Uganda", value: "UG" },
          { label: "Ukraine", value: "UA" },
          { label: "United Arab Emirates", value: "AE" },
          { label: "United Kingdom", value: "GB" },
          { label: "United States", value: "US" },
          { label: "Uruguay", value: "UY" },
          { label: "Uzbekistan", value: "UZ" },
          { label: "Vanuatu", value: "VU" },
          { label: "Vatican City", value: "VA" },
          { label: "Venezuela", value: "VE" },
          { label: "Vietnam", value: "VN" },
          { label: "Wallis & Futuna", value: "WF" },
          { label: "Western Sahara", value: "EH" },
          { label: "Yemen", value: "YE" },
          { label: "Zambia", value: "ZM" },
          { label: "Zimbabwe", value: "ZW" }
        ]
      }
    }),
    data_type: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Data Type",
      description: "Type of trends data to retrieve",
      required: !1,
      defaultValue: "TIMESERIES",
      options: {
        options: [
          { label: "Interest Over Time", value: "TIMESERIES" },
          { label: "Compared breakdown by region", value: "GEO_MAP" },
          { label: "Interest by Region", value: "GEO_MAP_0" },
          { label: "Related Topics", value: "RELATED_TOPICS" },
          { label: "Related Queries", value: "RELATED_QUERIES" }
        ]
      }
    }),
    time_period: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Time Period",
      description: "Time range for the trends data",
      required: !1,
      defaultValue: "today 12-m",
      options: {
        options: [
          { label: "Past Hour", value: "now 1-H" },
          { label: "Past 4 Hours", value: "now 4-H" },
          { label: "Past Day", value: "now 1-d" },
          { label: "Past 7 Days", value: "now 7-d" },
          { label: "Past 30 Days", value: "today 1-m" },
          { label: "Past 90 Days", value: "today 3-m" },
          { label: "Past 12 Months", value: "today 12-m" },
          { label: "Past 5 Years", value: "today 5-y" },
          { label: "Since 2004", value: "all" }
        ]
      }
    }),
    category: import_pieces_framework4.Property.Number({
      displayName: "Category",
      description: "Google Trends category ID (0 for all categories)",
      required: !1,
      defaultValue: 0
    }),
    property_filter: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Property Filter (grop)",
      description: "Filter by Google property",
      required: !1,
      defaultValue: "",
      options: {
        options: [
          { label: "Web Search", value: "" },
          { label: "Image Search", value: "images" },
          { label: "News Search", value: "news" },
          { label: "Google Shopping", value: "froogle" },
          { label: "YouTube Search", value: "youtube" }
        ]
      }
    }),
    no_cache: import_pieces_framework4.Property.StaticDropdown({
      displayName: "No Cache",
      description: "Force SerpApi to fetch the Google results even if a cached version is already present.",
      required: !1,
      defaultValue: "false",
      options: {
        options: [
          { label: "false (Allow cache)", value: "false" },
          { label: "true (Force fresh results)", value: "true" }
        ]
      }
    }),
    async: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Async",
      description: "Defines the way you want to submit your search to SerpApi. It can be set to false (default) to open an HTTP connection and keep it open until you got your search results, or true to just submit your search to SerpApi and retrieve them later.",
      required: !1,
      defaultValue: "false",
      options: {
        options: [
          { label: "false (Synchronous)", value: "false" },
          { label: "true (Asynchronous)", value: "true" }
        ]
      }
    }),
    output: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Output",
      description: "Defines the final output format you want.",
      required: !1,
      defaultValue: "json",
      options: {
        options: [
          { label: "JSON (Structured)", value: "json" },
          { label: "HTML (Raw)", value: "html" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    try {
      let client = new SerpApiClient({
        defaultTimeout: 3e4,
        defaultRetries: 3,
        defaultRetryDelay: 1e3,
        enableLogging: !1
      }), searchConfig = {
        api_key: auth.secret_text,
        engine: "google_trends" /* GOOGLE_TRENDS */,
        q: propsValue.query,
        hl: propsValue.hl,
        data_type: propsValue.data_type,
        geo: propsValue.geo,
        date: propsValue.time_period,
        cat: propsValue.category
      };
      propsValue.property_filter && (searchConfig.gprop = propsValue.property_filter), propsValue.no_cache && (searchConfig.no_cache = propsValue.no_cache), propsValue.async && (searchConfig.async = propsValue.async), propsValue.output && (searchConfig.output = propsValue.output);
      let response = await client.executeSearch(searchConfig);
      return __spreadValues({
        success: !0
      }, response);
    } catch (error) {
      return {
        success: !1,
        error: error instanceof Error ? error.message : "Unknown error occurred",
        error_type: error instanceof Error ? error.constructor.name : "UnknownError",
        timestamp: (/* @__PURE__ */ new Date()).toISOString(),
        search_query: propsValue.query
      };
    }
  }
});

// ../ap/packages/pieces/community/serp-api/src/lib/actions/youtube-search.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var youtubeSearch = (0, import_pieces_framework5.createAction)({
  auth: serpApiAuth,
  name: "youtube_search",
  classification: "SEARCH",
  displayName: "YouTube Search",
  description: "Retrieve top video content results from YouTube for specific keywords or topics with advanced filtering.",
  audience: "human",
  aiMetadata: { description: "Searches YouTube via SerpApi for videos matching a query. Use to find video content on a topic, discover channels, or research what is being published, scoped by language and country. Read-only and idempotent; requires the search query and a SerpApi API key.", idempotent: !0 },
  outputSchema: youtubeSearchOutputSchema,
  props: {
    query: import_pieces_framework5.Property.ShortText({
      displayName: "Search Query",
      description: "YouTube search query",
      required: !0
    }),
    hl: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Host Language (hl)",
      description: "Language for search results",
      required: !1,
      defaultValue: "en",
      options: {
        options: LANGUAGE_OPTIONS
      }
    }),
    gl: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Country (gl)",
      description: "Country for search results",
      required: !1,
      options: {
        options: COUNTRY_OPTIONS
      }
    }),
    no_cache: import_pieces_framework5.Property.StaticDropdown({
      displayName: "No Cache",
      description: "Force fresh results",
      required: !1,
      defaultValue: "false",
      options: {
        options: [
          { label: "false (Allow cache)", value: "false" },
          { label: "true (Force fresh results)", value: "true" }
        ]
      }
    }),
    async: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Async",
      description: "Submit search asynchronously",
      required: !1,
      defaultValue: "false",
      options: {
        options: [
          { label: "false (Synchronous)", value: "false" },
          { label: "true (Asynchronous)", value: "true" }
        ]
      }
    }),
    output: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Output",
      description: "Output format for results",
      required: !1,
      defaultValue: "json",
      options: {
        options: [
          { label: "JSON (Structured)", value: "json" },
          { label: "HTML (Raw)", value: "html" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    try {
      let client = new SerpApiClient({
        defaultTimeout: 3e4,
        defaultRetries: 3,
        defaultRetryDelay: 1e3,
        enableLogging: !1
      }), searchConfig = {
        api_key: auth.secret_text,
        engine: "youtube" /* YOUTUBE */,
        search_query: propsValue.query,
        hl: propsValue.hl,
        gl: propsValue.gl
      };
      propsValue.gl && (searchConfig.gl = propsValue.gl), propsValue.no_cache && (searchConfig.no_cache = propsValue.no_cache), propsValue.async && (searchConfig.async = propsValue.async), propsValue.output && (searchConfig.output = propsValue.output);
      let response = await client.executeSearch(searchConfig);
      return __spreadValues({
        success: !0
      }, response);
    } catch (error) {
      return {
        success: !1,
        error: error instanceof Error ? error.message : "Unknown error occurred",
        error_type: error instanceof Error ? error.constructor.name : "UnknownError",
        timestamp: (/* @__PURE__ */ new Date()).toISOString(),
        search_query: propsValue.query
      };
    }
  }
});

// ../ap/packages/pieces/community/serp-api/src/lib/actions/search-google-web-ai.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var searchGoogleWebAi = (0, import_pieces_framework6.createAction)({
  auth: serpApiAuth,
  name: "search_google_web_ai",
  classification: "SEARCH",
  displayName: "Google Search (Agent)",
  description: "Run a Google web search and return organic results for a query.",
  audience: "ai",
  aiMetadata: {
    description: "Search the web with Google and get ranked organic results for a query. Use for general web lookups, current information, rankings, or topic research. For news pick Search Google News, for videos Search YouTube, for products Search Google Shopping.",
    idempotent: !0
  },
  outputSchema: searchGoogleWebAiOutputSchema,
  props: {
    query: import_pieces_framework6.Property.ShortText({
      displayName: "Search Query",
      description: 'The web search query to execute, e.g. "best electric cars 2024".',
      required: !0
    }),
    location: import_pieces_framework6.Property.ShortText({
      displayName: "Location",
      description: 'Free-text geographic location to originate the search from, e.g. "Austin, Texas, United States". SerpApi resolves it server-side. Optional.',
      required: !1
    }),
    hl: import_pieces_framework6.Property.ShortText({
      displayName: "Language (hl)",
      description: 'Two-character lowercase ISO 639-1 language code, e.g. "en", "es", "fr". Optional.',
      required: !1
    }),
    gl: import_pieces_framework6.Property.ShortText({
      displayName: "Country (gl)",
      description: 'Two-character lowercase ISO 3166-1 country code, e.g. "us", "gb", "de". Optional.',
      required: !1
    }),
    device: import_pieces_framework6.Property.ShortText({
      displayName: "Device",
      description: 'Device to simulate: one of "desktop", "mobile", or "tablet". Optional.',
      required: !1
    }),
    safe: import_pieces_framework6.Property.ShortText({
      displayName: "Safe Search",
      description: 'Safe-search level: "active" or "off". Optional.',
      required: !1
    }),
    num_results: import_pieces_framework6.Property.Number({
      displayName: "Number of Results",
      description: "Number of results to return (1-100). Optional.",
      required: !1
    }),
    start: import_pieces_framework6.Property.Number({
      displayName: "Start Position",
      description: "Zero-based offset for pagination (0-990). Optional.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    try {
      let client = new SerpApiClient({
        defaultTimeout: 3e4,
        defaultRetries: 3,
        defaultRetryDelay: 1e3,
        enableLogging: !1
      }), searchConfig = {
        api_key: auth.secret_text,
        engine: "google" /* GOOGLE */,
        q: propsValue.query,
        hl: propsValue.hl || void 0,
        gl: propsValue.gl || void 0,
        num: propsValue.num_results,
        start: propsValue.start,
        safe: propsValue.safe ? propsValue.safe : void 0,
        device: propsValue.device ? propsValue.device : void 0
      };
      propsValue.location && (searchConfig.location = propsValue.location);
      let response = await client.executeSearch(searchConfig);
      return __spreadValues({
        success: !0
      }, response);
    } catch (error) {
      return {
        success: !1,
        error: error instanceof Error ? error.message : "Unknown error occurred",
        error_type: error instanceof Error ? error.constructor.name : "UnknownError",
        timestamp: (/* @__PURE__ */ new Date()).toISOString(),
        search_query: propsValue.query
      };
    }
  }
});

// ../ap/packages/pieces/community/serp-api/src/lib/actions/search-google-news-ai.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var searchGoogleNewsAi = (0, import_pieces_framework7.createAction)({
  auth: serpApiAuth,
  name: "search_google_news_ai",
  classification: "SEARCH",
  displayName: "Google News Search (Agent)",
  description: "Search Google News for recent articles matching a query.",
  audience: "ai",
  aiMetadata: {
    description: "Search Google News for recent news articles matching a query. Use to get the latest news headlines about a company, brand, person, or topic, monitor press and media mentions, or track breaking news coverage. For general web results pick Search Google instead.",
    idempotent: !0
  },
  outputSchema: searchGoogleNewsAiOutputSchema,
  props: {
    query: import_pieces_framework7.Property.ShortText({
      displayName: "Search Query",
      description: 'The news search query, e.g. "renewable energy policy".',
      required: !0
    }),
    hl: import_pieces_framework7.Property.ShortText({
      displayName: "Language (hl)",
      description: 'Two-character lowercase ISO 639-1 language code, e.g. "en", "es", "fr". Optional.',
      required: !1
    }),
    gl: import_pieces_framework7.Property.ShortText({
      displayName: "Country (gl)",
      description: 'Two-character lowercase ISO 3166-1 country code, e.g. "us", "gb", "de". Optional.',
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    try {
      let client = new SerpApiClient({
        defaultTimeout: 3e4,
        defaultRetries: 3,
        defaultRetryDelay: 1e3,
        enableLogging: !1
      }), searchConfig = {
        api_key: auth.secret_text,
        engine: "google_news" /* GOOGLE_NEWS */,
        q: propsValue.query,
        hl: propsValue.hl || void 0,
        gl: propsValue.gl || void 0
      }, response = await client.executeSearch(searchConfig);
      return __spreadValues({
        success: !0
      }, response);
    } catch (error) {
      return {
        success: !1,
        error: error instanceof Error ? error.message : "Unknown error occurred",
        error_type: error instanceof Error ? error.constructor.name : "UnknownError",
        timestamp: (/* @__PURE__ */ new Date()).toISOString(),
        search_query: propsValue.query
      };
    }
  }
});

// ../ap/packages/pieces/community/serp-api/src/lib/actions/search-youtube-ai.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var searchYoutubeAi = (0, import_pieces_framework8.createAction)({
  auth: serpApiAuth,
  name: "search_youtube_ai",
  classification: "SEARCH",
  displayName: "YouTube Search (Agent)",
  description: "Search YouTube for videos matching a query.",
  audience: "ai",
  aiMetadata: {
    description: "Search YouTube for videos about a topic or query. Use to find videos, discover channels, look up video content, or research what is being published about a subject. For news articles pick Search Google News.",
    idempotent: !0
  },
  outputSchema: searchYoutubeAiOutputSchema,
  props: {
    query: import_pieces_framework8.Property.ShortText({
      displayName: "Search Query",
      description: 'The YouTube search query, e.g. "how to bake sourdough".',
      required: !0
    }),
    hl: import_pieces_framework8.Property.ShortText({
      displayName: "Language (hl)",
      description: 'Two-character lowercase ISO 639-1 language code, e.g. "en", "es", "fr". Optional.',
      required: !1
    }),
    gl: import_pieces_framework8.Property.ShortText({
      displayName: "Country (gl)",
      description: 'Two-character lowercase ISO 3166-1 country code, e.g. "us", "gb", "de". Optional.',
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    try {
      let client = new SerpApiClient({
        defaultTimeout: 3e4,
        defaultRetries: 3,
        defaultRetryDelay: 1e3,
        enableLogging: !1
      }), searchConfig = {
        api_key: auth.secret_text,
        engine: "youtube" /* YOUTUBE */,
        search_query: propsValue.query,
        hl: propsValue.hl || void 0,
        gl: propsValue.gl || void 0
      }, response = await client.executeSearch(searchConfig);
      return __spreadValues({
        success: !0
      }, response);
    } catch (error) {
      return {
        success: !1,
        error: error instanceof Error ? error.message : "Unknown error occurred",
        error_type: error instanceof Error ? error.constructor.name : "UnknownError",
        timestamp: (/* @__PURE__ */ new Date()).toISOString(),
        search_query: propsValue.query
      };
    }
  }
});

// ../ap/packages/pieces/community/serp-api/src/lib/actions/search-google-trends-ai.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var searchGoogleTrendsAi = (0, import_pieces_framework9.createAction)({
  auth: serpApiAuth,
  name: "search_google_trends_ai",
  classification: "SEARCH",
  displayName: "Google Trends Search (Agent)",
  description: "Query Google Trends for interest in a keyword over time and across regions.",
  audience: "ai",
  aiMetadata: {
    description: `Look up Google Trends data for how search interest in a keyword or topic changes over time and by region. Use to gauge a topic's popularity trajectory, compare geographic interest, or find related and rising queries. Choose the data type: "TIMESERIES" (interest over time), "GEO_MAP" (interest by region), "RELATED_TOPICS", or "RELATED_QUERIES".`,
    idempotent: !0
  },
  outputSchema: searchGoogleTrendsAiOutputSchema,
  props: {
    query: import_pieces_framework9.Property.ShortText({
      displayName: "Search Query",
      description: 'The keyword or topic to analyze trends for, e.g. "artificial intelligence".',
      required: !0
    }),
    data_type: import_pieces_framework9.Property.ShortText({
      displayName: "Data Type",
      description: 'Type of trends data: one of "TIMESERIES" (interest over time, default), "GEO_MAP" (interest by region), "RELATED_TOPICS", or "RELATED_QUERIES". Optional.',
      required: !1
    }),
    geo: import_pieces_framework9.Property.ShortText({
      displayName: "Geographic Location",
      description: 'Uppercase two-letter region code to scope the trend to, e.g. "US", "GB", "JP". Leave empty for worldwide. Optional.',
      required: !1
    }),
    date: import_pieces_framework9.Property.ShortText({
      displayName: "Time Period",
      description: 'Trends date range token, e.g. "today 12-m" (past 12 months), "today 5-y", "now 7-d", or "all". Optional.',
      required: !1
    }),
    category: import_pieces_framework9.Property.Number({
      displayName: "Category",
      description: "Google Trends category id (0 for all categories). Optional.",
      required: !1
    }),
    gprop: import_pieces_framework9.Property.ShortText({
      displayName: "Property Filter",
      description: 'Google property to filter by: empty for web, or "images", "news", "froogle" (shopping), "youtube". Optional.',
      required: !1
    }),
    hl: import_pieces_framework9.Property.ShortText({
      displayName: "Language (hl)",
      description: 'Two-character lowercase ISO 639-1 language code, e.g. "en", "es", "fr". Optional.',
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    try {
      let client = new SerpApiClient({
        defaultTimeout: 3e4,
        defaultRetries: 3,
        defaultRetryDelay: 1e3,
        enableLogging: !1
      }), searchConfig = {
        api_key: auth.secret_text,
        engine: "google_trends" /* GOOGLE_TRENDS */,
        q: propsValue.query,
        hl: propsValue.hl || void 0,
        data_type: propsValue.data_type ? propsValue.data_type : void 0,
        geo: propsValue.geo || void 0,
        date: propsValue.date || void 0,
        cat: propsValue.category || void 0
      };
      propsValue.gprop && (searchConfig.gprop = propsValue.gprop);
      let response = await client.executeSearch(searchConfig);
      return __spreadValues({
        success: !0
      }, response);
    } catch (error) {
      return {
        success: !1,
        error: error instanceof Error ? error.message : "Unknown error occurred",
        error_type: error instanceof Error ? error.constructor.name : "UnknownError",
        timestamp: (/* @__PURE__ */ new Date()).toISOString(),
        search_query: propsValue.query
      };
    }
  }
});

// ../ap/packages/pieces/community/serp-api/src/lib/actions/search-google-maps.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var searchGoogleMaps = (0, import_pieces_framework10.createAction)({
  auth: serpApiAuth,
  name: "search_google_maps",
  classification: "SEARCH",
  displayName: "Search Google Maps",
  description: "Find local businesses and places on Google Maps for a query.",
  audience: "ai",
  aiMetadata: {
    description: "Search Google Maps for local businesses and places matching a query: names, ratings, reviews, addresses, phone numbers, opening hours, and GPS coordinates. Use to find businesses, restaurants, or services in an area. Pass `ll` to anchor the search to a map center; `ll` is effectively required once you paginate with `start`.",
    idempotent: !0
  },
  outputSchema: searchGoogleMapsOutputSchema,
  props: {
    query: import_pieces_framework10.Property.ShortText({
      displayName: "Search Query",
      description: 'What to search for, e.g. "coffee shops" or "Pizza Hut".',
      required: !0
    }),
    ll: import_pieces_framework10.Property.ShortText({
      displayName: "Map Center (ll)",
      description: 'GPS coordinates of the map center as "@latitude,longitude,zoom", e.g. "@40.7455096,-74.0083012,14z". Required once you paginate with Start. Optional otherwise.',
      required: !1
    }),
    start: import_pieces_framework10.Property.Number({
      displayName: "Start Position",
      description: "Zero-based offset for pagination (multiples of 20). Requires Map Center (ll). Optional.",
      required: !1
    }),
    hl: import_pieces_framework10.Property.ShortText({
      displayName: "Language (hl)",
      description: 'Two-character lowercase ISO 639-1 language code, e.g. "en", "es". Optional.',
      required: !1
    }),
    gl: import_pieces_framework10.Property.ShortText({
      displayName: "Country (gl)",
      description: 'Two-character lowercase ISO 3166-1 country code, e.g. "us", "gb". Optional.',
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    try {
      let client = new SerpApiClient({
        defaultTimeout: 3e4,
        defaultRetries: 3,
        defaultRetryDelay: 1e3,
        enableLogging: !1
      }), searchConfig = {
        api_key: auth.secret_text,
        engine: "google_maps" /* GOOGLE_MAPS */,
        q: propsValue.query,
        hl: propsValue.hl || void 0,
        gl: propsValue.gl || void 0,
        start: propsValue.start
      };
      propsValue.ll && (searchConfig.ll = propsValue.ll);
      let response = await client.executeSearch(searchConfig);
      return __spreadValues({
        success: !0
      }, response);
    } catch (error) {
      return {
        success: !1,
        error: error instanceof Error ? error.message : "Unknown error occurred",
        error_type: error instanceof Error ? error.constructor.name : "UnknownError",
        timestamp: (/* @__PURE__ */ new Date()).toISOString(),
        search_query: propsValue.query
      };
    }
  }
});

// ../ap/packages/pieces/community/serp-api/src/lib/actions/search-google-scholar.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var searchGoogleScholar = (0, import_pieces_framework11.createAction)({
  auth: serpApiAuth,
  name: "search_google_scholar",
  classification: "SEARCH",
  displayName: "Search Google Scholar",
  description: "Search academic papers and citations on Google Scholar for a query.",
  audience: "ai",
  aiMetadata: {
    description: "Search Google Scholar for academic papers, scholarly articles, and citations matching a query. Use to research literature, find citations, or gather academic sources. Restrict by publication year with `as_ylo`/`as_yhi`.",
    idempotent: !0
  },
  outputSchema: searchGoogleScholarOutputSchema,
  props: {
    query: import_pieces_framework11.Property.ShortText({
      displayName: "Search Query",
      description: 'The academic search query, e.g. "transformer neural networks".',
      required: !0
    }),
    num_results: import_pieces_framework11.Property.Number({
      displayName: "Number of Results",
      description: "Number of results to return (1-20). Optional.",
      required: !1
    }),
    start: import_pieces_framework11.Property.Number({
      displayName: "Start Position",
      description: "Zero-based offset for pagination (multiples of 10). Optional.",
      required: !1
    }),
    as_ylo: import_pieces_framework11.Property.Number({
      displayName: "From Year",
      description: "Lower bound of the publication year range, e.g. 2018. Optional.",
      required: !1
    }),
    as_yhi: import_pieces_framework11.Property.Number({
      displayName: "To Year",
      description: "Upper bound of the publication year range, e.g. 2024. Optional.",
      required: !1
    }),
    hl: import_pieces_framework11.Property.ShortText({
      displayName: "Language (hl)",
      description: 'Two-character lowercase ISO 639-1 language code, e.g. "en", "es". Optional.',
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    try {
      let client = new SerpApiClient({
        defaultTimeout: 3e4,
        defaultRetries: 3,
        defaultRetryDelay: 1e3,
        enableLogging: !1
      }), searchConfig = {
        api_key: auth.secret_text,
        engine: "google_scholar" /* GOOGLE_SCHOLAR */,
        q: propsValue.query,
        hl: propsValue.hl || void 0,
        num: propsValue.num_results,
        start: propsValue.start,
        as_ylo: propsValue.as_ylo,
        as_yhi: propsValue.as_yhi
      }, response = await client.executeSearch(searchConfig);
      return __spreadValues({
        success: !0
      }, response);
    } catch (error) {
      return {
        success: !1,
        error: error instanceof Error ? error.message : "Unknown error occurred",
        error_type: error instanceof Error ? error.constructor.name : "UnknownError",
        timestamp: (/* @__PURE__ */ new Date()).toISOString(),
        search_query: propsValue.query
      };
    }
  }
});

// ../ap/packages/pieces/community/serp-api/src/lib/actions/search-google-images.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var searchGoogleImages = (0, import_pieces_framework12.createAction)({
  auth: serpApiAuth,
  name: "search_google_images",
  classification: "SEARCH",
  displayName: "Search Google Images",
  description: "Discover images on Google Images for a query.",
  audience: "ai",
  aiMetadata: {
    description: "Search Google Images for pictures and photos matching a query. Use to discover images on a topic, find a full-resolution image URL, or locate the page an image came from. Page through results with the page index.",
    idempotent: !0
  },
  outputSchema: searchGoogleImagesOutputSchema,
  props: {
    query: import_pieces_framework12.Property.ShortText({
      displayName: "Search Query",
      description: 'What images to search for, e.g. "golden gate bridge".',
      required: !0
    }),
    num_results: import_pieces_framework12.Property.Number({
      displayName: "Number of Results",
      description: "Number of image results to return (up to 100). Optional.",
      required: !1
    }),
    ijn: import_pieces_framework12.Property.Number({
      displayName: "Page Index",
      description: "Zero-based page index for image pagination (0 = first page, 1 = second, ...). Optional.",
      required: !1
    }),
    hl: import_pieces_framework12.Property.ShortText({
      displayName: "Language (hl)",
      description: 'Two-character lowercase ISO 639-1 language code, e.g. "en", "es". Optional.',
      required: !1
    }),
    gl: import_pieces_framework12.Property.ShortText({
      displayName: "Country (gl)",
      description: 'Two-character lowercase ISO 3166-1 country code, e.g. "us", "gb". Optional.',
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    try {
      let client = new SerpApiClient({
        defaultTimeout: 3e4,
        defaultRetries: 3,
        defaultRetryDelay: 1e3,
        enableLogging: !1
      }), searchConfig = {
        api_key: auth.secret_text,
        engine: "google_images" /* GOOGLE_IMAGES */,
        q: propsValue.query,
        hl: propsValue.hl || void 0,
        gl: propsValue.gl || void 0,
        num: propsValue.num_results,
        ijn: propsValue.ijn
      }, response = await client.executeSearch(searchConfig);
      return __spreadValues({
        success: !0
      }, response);
    } catch (error) {
      return {
        success: !1,
        error: error instanceof Error ? error.message : "Unknown error occurred",
        error_type: error instanceof Error ? error.constructor.name : "UnknownError",
        timestamp: (/* @__PURE__ */ new Date()).toISOString(),
        search_query: propsValue.query
      };
    }
  }
});

// ../ap/packages/pieces/community/serp-api/src/lib/actions/search-google-shopping.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var searchGoogleShopping = (0, import_pieces_framework13.createAction)({
  auth: serpApiAuth,
  name: "search_google_shopping",
  classification: "SEARCH",
  displayName: "Search Google Shopping",
  description: "Find products on Google Shopping for a query.",
  audience: "ai",
  aiMetadata: {
    description: "Search Google Shopping for products matching a query, to compare prices, merchants, and ratings across stores. Use for product research or price comparison before buying. Paginate with Start (preferred on this engine).",
    idempotent: !0
  },
  outputSchema: searchGoogleShoppingOutputSchema,
  props: {
    query: import_pieces_framework13.Property.ShortText({
      displayName: "Search Query",
      description: 'The product to search for, e.g. "wireless headphones".',
      required: !0
    }),
    start: import_pieces_framework13.Property.Number({
      displayName: "Start Position",
      description: "Zero-based offset for pagination (multiples of 60). Optional.",
      required: !1
    }),
    hl: import_pieces_framework13.Property.ShortText({
      displayName: "Language (hl)",
      description: 'Two-character lowercase ISO 639-1 language code, e.g. "en", "es". Optional.',
      required: !1
    }),
    gl: import_pieces_framework13.Property.ShortText({
      displayName: "Country (gl)",
      description: 'Two-character lowercase ISO 3166-1 country code, e.g. "us", "gb". Optional.',
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    try {
      let client = new SerpApiClient({
        defaultTimeout: 3e4,
        defaultRetries: 3,
        defaultRetryDelay: 1e3,
        enableLogging: !1
      }), searchConfig = {
        api_key: auth.secret_text,
        engine: "google_shopping" /* GOOGLE_SHOPPING */,
        q: propsValue.query,
        hl: propsValue.hl || void 0,
        gl: propsValue.gl || void 0,
        start: propsValue.start
      }, response = await client.executeSearch(searchConfig);
      return __spreadValues({
        success: !0
      }, response);
    } catch (error) {
      return {
        success: !1,
        error: error instanceof Error ? error.message : "Unknown error occurred",
        error_type: error instanceof Error ? error.constructor.name : "UnknownError",
        timestamp: (/* @__PURE__ */ new Date()).toISOString(),
        search_query: propsValue.query
      };
    }
  }
});

// ../ap/packages/pieces/community/serp-api/src/lib/actions/search-google-jobs.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var searchGoogleJobs = (0, import_pieces_framework14.createAction)({
  auth: serpApiAuth,
  name: "search_google_jobs",
  classification: "SEARCH",
  displayName: "Search Google Jobs",
  description: "Find job listings on Google Jobs for a query.",
  audience: "ai",
  aiMetadata: {
    description: 'Search Google Jobs for job listings, job openings, and vacancies matching a role, title, or keyword. Use to find open positions or job postings, optionally narrowed with a free-text Location; set Listing Type to "1" for remote / work-from-home roles. Paginate with the next page token from a prior response.',
    idempotent: !0
  },
  outputSchema: searchGoogleJobsOutputSchema,
  props: {
    query: import_pieces_framework14.Property.ShortText({
      displayName: "Search Query",
      description: 'The job role to search for, e.g. "software engineer".',
      required: !0
    }),
    location: import_pieces_framework14.Property.ShortText({
      displayName: "Location",
      description: 'Free-text location, e.g. "New York, NY". SerpApi resolves it server-side. Optional.',
      required: !1
    }),
    ltype: import_pieces_framework14.Property.ShortText({
      displayName: "Listing Type",
      description: 'Set to "1" to return only work-from-home jobs. Optional.',
      required: !1
    }),
    next_page_token: import_pieces_framework14.Property.ShortText({
      displayName: "Next Page Token",
      description: "Pagination token from the `next_page_token` field of a previous response. Optional.",
      required: !1
    }),
    hl: import_pieces_framework14.Property.ShortText({
      displayName: "Language (hl)",
      description: 'Two-character lowercase ISO 639-1 language code, e.g. "en", "es". Optional.',
      required: !1
    }),
    gl: import_pieces_framework14.Property.ShortText({
      displayName: "Country (gl)",
      description: 'Two-character lowercase ISO 3166-1 country code, e.g. "us", "gb". Optional.',
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    try {
      let client = new SerpApiClient({
        defaultTimeout: 3e4,
        defaultRetries: 3,
        defaultRetryDelay: 1e3,
        enableLogging: !1
      }), searchConfig = {
        api_key: auth.secret_text,
        engine: "google_jobs" /* GOOGLE_JOBS */,
        q: propsValue.query,
        hl: propsValue.hl || void 0,
        gl: propsValue.gl || void 0
      };
      propsValue.location && (searchConfig.location = propsValue.location), propsValue.ltype && (searchConfig.ltype = propsValue.ltype), propsValue.next_page_token && (searchConfig.next_page_token = propsValue.next_page_token);
      let response = await client.executeSearch(searchConfig);
      return __spreadValues({
        success: !0
      }, response);
    } catch (error) {
      return {
        success: !1,
        error: error instanceof Error ? error.message : "Unknown error occurred",
        error_type: error instanceof Error ? error.constructor.name : "UnknownError",
        timestamp: (/* @__PURE__ */ new Date()).toISOString(),
        search_query: propsValue.query
      };
    }
  }
});

// ../ap/packages/pieces/community/serp-api/src/lib/actions/search-google-lens.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var searchGoogleLens = (0, import_pieces_framework15.createAction)({
  auth: serpApiAuth,
  name: "search_google_lens",
  classification: "SEARCH",
  displayName: "Search Google Lens",
  description: "Run a reverse-image (visual) search on Google Lens for an image URL.",
  audience: "ai",
  aiMetadata: {
    description: "Run a Google Lens reverse image search on a publicly accessible image URL to find visual matches for a picture. Use to identify what is in an image, find where an image appears online, or find visually similar items. Optionally refine with a text query.",
    idempotent: !0
  },
  outputSchema: searchGoogleLensOutputSchema,
  props: {
    url: import_pieces_framework15.Property.ShortText({
      displayName: "Image URL",
      description: 'Publicly accessible URL of the image to analyze, e.g. "https://example.com/photo.jpg".',
      required: !0
    }),
    query: import_pieces_framework15.Property.ShortText({
      displayName: "Text Refinement",
      description: "Optional text query to refine the visual search.",
      required: !1
    }),
    country: import_pieces_framework15.Property.ShortText({
      displayName: "Country",
      description: 'Two-letter country code for results, e.g. "us", "gb". Optional.',
      required: !1
    }),
    type: import_pieces_framework15.Property.ShortText({
      displayName: "Search Type",
      description: 'Lens search type, e.g. "all" (default), "products", or "visual_matches". Optional.',
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    try {
      let client = new SerpApiClient({
        defaultTimeout: 3e4,
        defaultRetries: 3,
        defaultRetryDelay: 1e3,
        enableLogging: !1
      }), searchConfig = {
        api_key: auth.secret_text,
        engine: "google_lens" /* GOOGLE_LENS */,
        url: propsValue.url
      };
      propsValue.query && (searchConfig.q = propsValue.query), propsValue.country && (searchConfig.country = propsValue.country), propsValue.type && (searchConfig.type = propsValue.type);
      let response = await client.executeSearch(searchConfig);
      return __spreadValues({
        success: !0
      }, response);
    } catch (error) {
      return {
        success: !1,
        error: error instanceof Error ? error.message : "Unknown error occurred",
        error_type: error instanceof Error ? error.constructor.name : "UnknownError",
        timestamp: (/* @__PURE__ */ new Date()).toISOString(),
        image_url: propsValue.url
      };
    }
  }
});

// ../ap/packages/pieces/community/serp-api/src/lib/actions/search-google-local-services.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var searchGoogleLocalServices = (0, import_pieces_framework16.createAction)({
  auth: serpApiAuth,
  name: "search_google_local_services",
  classification: "SEARCH",
  displayName: "Search Google Local Services",
  description: "Find local-services-ads providers for a service category in a region.",
  audience: "ai",
  aiMetadata: {
    description: "Search Google Local Services Ads for vetted local service providers (plumbers, electricians, cleaners, and similar trades) matching a query. Requires a Data CID identifying the geographic region; this is an opaque id with no resolver in this piece, so it must be supplied by the caller.",
    idempotent: !0
  },
  outputSchema: searchGoogleLocalServicesOutputSchema,
  props: {
    query: import_pieces_framework16.Property.ShortText({
      displayName: "Search Query",
      description: 'The service category to search for, e.g. "plumber" or "electrician".',
      required: !0
    }),
    data_cid: import_pieces_framework16.Property.ShortText({
      displayName: "Data CID",
      description: "Opaque region identifier (data_cid) for the local-services area. Must be supplied by the caller; this piece has no resolver for it.",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    try {
      let client = new SerpApiClient({
        defaultTimeout: 3e4,
        defaultRetries: 3,
        defaultRetryDelay: 1e3,
        enableLogging: !1
      }), searchConfig = {
        api_key: auth.secret_text,
        engine: "google_local_services" /* GOOGLE_LOCAL_SERVICES */,
        q: propsValue.query,
        data_cid: propsValue.data_cid
      }, response = await client.executeSearch(searchConfig);
      return __spreadValues({
        success: !0
      }, response);
    } catch (error) {
      return {
        success: !1,
        error: error instanceof Error ? error.message : "Unknown error occurred",
        error_type: error instanceof Error ? error.constructor.name : "UnknownError",
        timestamp: (/* @__PURE__ */ new Date()).toISOString(),
        search_query: propsValue.query
      };
    }
  }
});

// ../ap/packages/pieces/community/serp-api/src/lib/actions/search-apple-app-store.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework");
var searchAppleAppStore = (0, import_pieces_framework17.createAction)({
  auth: serpApiAuth,
  name: "search_apple_app_store",
  classification: "SEARCH",
  displayName: "Search Apple App Store",
  description: "Search the iOS Apple App Store for apps matching a term.",
  audience: "ai",
  aiMetadata: {
    description: "Search the Apple App Store for iOS apps matching a name or keyword. Use to discover iOS apps, look up a specific app, or check an app's developer, rating, and price. For Android apps use Search Google Play instead.",
    idempotent: !0
  },
  outputSchema: searchAppleAppStoreOutputSchema,
  props: {
    term: import_pieces_framework17.Property.ShortText({
      displayName: "Search Term",
      description: 'The app or keyword to search for, e.g. "meditation".',
      required: !0
    }),
    num_results: import_pieces_framework17.Property.Number({
      displayName: "Number of Results",
      description: "Number of results to return. Optional.",
      required: !1
    }),
    country: import_pieces_framework17.Property.ShortText({
      displayName: "Country",
      description: 'Two-letter App Store country/storefront code, e.g. "us", "gb". Optional.',
      required: !1
    }),
    lang: import_pieces_framework17.Property.ShortText({
      displayName: "Language",
      description: 'Language code for results, e.g. "en-us". Optional.',
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    try {
      let client = new SerpApiClient({
        defaultTimeout: 3e4,
        defaultRetries: 3,
        defaultRetryDelay: 1e3,
        enableLogging: !1
      }), searchConfig = {
        api_key: auth.secret_text,
        engine: "apple_app_store" /* APPLE_APP_STORE */,
        term: propsValue.term,
        num: propsValue.num_results
      };
      propsValue.country && (searchConfig.country = propsValue.country), propsValue.lang && (searchConfig.lang = propsValue.lang);
      let response = await client.executeSearch(searchConfig);
      return __spreadValues({
        success: !0
      }, response);
    } catch (error) {
      return {
        success: !1,
        error: error instanceof Error ? error.message : "Unknown error occurred",
        error_type: error instanceof Error ? error.constructor.name : "UnknownError",
        timestamp: (/* @__PURE__ */ new Date()).toISOString(),
        search_term: propsValue.term
      };
    }
  }
});

// ../ap/packages/pieces/community/serp-api/src/lib/actions/search-google-play.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework");
var searchGooglePlay = (0, import_pieces_framework18.createAction)({
  auth: serpApiAuth,
  name: "search_google_play",
  classification: "SEARCH",
  displayName: "Search Google Play",
  description: "Search the Google Play store for apps, games, movies, or books.",
  audience: "ai",
  aiMetadata: {
    description: 'Search the Google Play store for Android apps, games, movies, or books matching a query. Use to discover Android apps or other Play content; set Store to "apps" (default), "games", "movies", or "books". For iOS apps use Search Apple App Store instead.',
    idempotent: !0
  },
  outputSchema: searchGooglePlayOutputSchema,
  props: {
    query: import_pieces_framework18.Property.ShortText({
      displayName: "Search Query",
      description: 'What to search for, e.g. "puzzle games".',
      required: !0
    }),
    store: import_pieces_framework18.Property.ShortText({
      displayName: "Store Section",
      description: 'Store section: "apps" (default), "games", "movies", or "books". Optional.',
      required: !1
    }),
    apps_category: import_pieces_framework18.Property.ShortText({
      displayName: "Apps Category",
      description: 'Optional Google Play apps category filter, e.g. "GAME_PUZZLE".',
      required: !1
    }),
    next_page_token: import_pieces_framework18.Property.ShortText({
      displayName: "Next Page Token",
      description: "Pagination token from the `next_page_token` field of a previous response. Optional.",
      required: !1
    }),
    hl: import_pieces_framework18.Property.ShortText({
      displayName: "Language (hl)",
      description: 'Two-character lowercase ISO 639-1 language code, e.g. "en", "es". Optional.',
      required: !1
    }),
    gl: import_pieces_framework18.Property.ShortText({
      displayName: "Country (gl)",
      description: 'Two-character lowercase ISO 3166-1 country code, e.g. "us", "gb". Optional.',
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    try {
      let client = new SerpApiClient({
        defaultTimeout: 3e4,
        defaultRetries: 3,
        defaultRetryDelay: 1e3,
        enableLogging: !1
      }), searchConfig = {
        api_key: auth.secret_text,
        engine: "google_play" /* GOOGLE_PLAY */,
        q: propsValue.query,
        hl: propsValue.hl || void 0,
        gl: propsValue.gl || void 0
      };
      propsValue.store && (searchConfig.store = propsValue.store), propsValue.apps_category && (searchConfig.apps_category = propsValue.apps_category), propsValue.next_page_token && (searchConfig.next_page_token = propsValue.next_page_token);
      let response = await client.executeSearch(searchConfig);
      return __spreadValues({
        success: !0
      }, response);
    } catch (error) {
      return {
        success: !1,
        error: error instanceof Error ? error.message : "Unknown error occurred",
        error_type: error instanceof Error ? error.constructor.name : "UnknownError",
        timestamp: (/* @__PURE__ */ new Date()).toISOString(),
        search_query: propsValue.query
      };
    }
  }
});

// ../ap/packages/pieces/community/serp-api/src/lib/actions/search-yelp.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework");
var searchYelp = (0, import_pieces_framework19.createAction)({
  auth: serpApiAuth,
  name: "search_yelp",
  classification: "SEARCH",
  displayName: "Search Yelp",
  description: "Find businesses and reviews on Yelp for a category and location.",
  audience: "ai",
  aiMetadata: {
    description: "Search Yelp for local businesses matching a description in a location: restaurants, shops, and services with ratings, review counts, categories, and price levels. Use to find well-rated businesses nearby or in a named city. Both what to search (Find Description) and where (Find Location) are required.",
    idempotent: !0
  },
  outputSchema: searchYelpOutputSchema,
  props: {
    find_desc: import_pieces_framework19.Property.ShortText({
      displayName: "Find Description",
      description: 'What to search for, e.g. "italian restaurants" or "auto repair".',
      required: !0
    }),
    find_loc: import_pieces_framework19.Property.ShortText({
      displayName: "Find Location",
      description: 'Where to search, e.g. "San Francisco, CA".',
      required: !0
    }),
    sortby: import_pieces_framework19.Property.ShortText({
      displayName: "Sort By",
      description: 'Sort order: "recommended" (default), "rating", or "review_count". Optional.',
      required: !1
    }),
    attrs: import_pieces_framework19.Property.ShortText({
      displayName: "Attributes",
      description: 'Attribute filters, e.g. "RestaurantsPriceRange2.2". Optional.',
      required: !1
    }),
    start: import_pieces_framework19.Property.Number({
      displayName: "Start Position",
      description: "Zero-based offset for pagination (multiples of 10). Optional.",
      required: !1
    }),
    yelp_domain: import_pieces_framework19.Property.ShortText({
      displayName: "Yelp Domain",
      description: 'Yelp domain to use, e.g. "yelp.com" (default). Optional.',
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    try {
      let client = new SerpApiClient({
        defaultTimeout: 3e4,
        defaultRetries: 3,
        defaultRetryDelay: 1e3,
        enableLogging: !1
      }), searchConfig = {
        api_key: auth.secret_text,
        engine: "yelp" /* YELP */,
        find_desc: propsValue.find_desc,
        find_loc: propsValue.find_loc,
        start: propsValue.start
      };
      propsValue.sortby && (searchConfig.sortby = propsValue.sortby), propsValue.attrs && (searchConfig.attrs = propsValue.attrs), propsValue.yelp_domain && (searchConfig.yelp_domain = propsValue.yelp_domain);
      let response = await client.executeSearch(searchConfig);
      return __spreadValues({
        success: !0
      }, response);
    } catch (error) {
      return {
        success: !1,
        error: error instanceof Error ? error.message : "Unknown error occurred",
        error_type: error instanceof Error ? error.constructor.name : "UnknownError",
        timestamp: (/* @__PURE__ */ new Date()).toISOString(),
        find_desc: propsValue.find_desc,
        find_loc: propsValue.find_loc
      };
    }
  }
});

// ../ap/packages/pieces/community/serp-api/src/lib/actions/search-walmart.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework");
var searchWalmart = (0, import_pieces_framework20.createAction)({
  auth: serpApiAuth,
  name: "search_walmart",
  classification: "SEARCH",
  displayName: "Search Walmart",
  description: "Find products on Walmart for a query.",
  audience: "ai",
  aiMetadata: {
    description: "Search Walmart for products matching a query, to check prices, ratings, and availability. Use to look up Walmart product listings for price checks or stock research. Filter by price range and minimum rating, sort, and paginate by page.",
    idempotent: !0
  },
  outputSchema: searchWalmartOutputSchema,
  props: {
    query: import_pieces_framework20.Property.ShortText({
      displayName: "Search Query",
      description: 'The product to search for, e.g. "office chair".',
      required: !0
    }),
    page: import_pieces_framework20.Property.Number({
      displayName: "Page",
      description: "Page number for pagination (1-based). Optional.",
      required: !1
    }),
    sort: import_pieces_framework20.Property.ShortText({
      displayName: "Sort By",
      description: 'Sort order, e.g. "price_low", "price_high", "best_seller", "rating_high". Optional.',
      required: !1
    }),
    min_price: import_pieces_framework20.Property.Number({
      displayName: "Minimum Price",
      description: "Lowest product price to include. Optional.",
      required: !1
    }),
    max_price: import_pieces_framework20.Property.Number({
      displayName: "Maximum Price",
      description: "Highest product price to include. Optional.",
      required: !1
    }),
    min_rating: import_pieces_framework20.Property.Number({
      displayName: "Minimum Rating",
      description: "Lowest average rating to include (e.g. 4). Optional.",
      required: !1
    }),
    store_id: import_pieces_framework20.Property.ShortText({
      displayName: "Store ID",
      description: "Walmart store id to scope availability. Optional.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    try {
      let client = new SerpApiClient({
        defaultTimeout: 3e4,
        defaultRetries: 3,
        defaultRetryDelay: 1e3,
        enableLogging: !1
      }), searchConfig = {
        api_key: auth.secret_text,
        engine: "walmart" /* WALMART */,
        query: propsValue.query,
        page: propsValue.page,
        min_price: propsValue.min_price,
        max_price: propsValue.max_price,
        min_rating: propsValue.min_rating
      };
      propsValue.sort && (searchConfig.sort = propsValue.sort), propsValue.store_id && (searchConfig.store_id = propsValue.store_id);
      let response = await client.executeSearch(searchConfig);
      return __spreadValues({
        success: !0
      }, response);
    } catch (error) {
      return {
        success: !1,
        error: error instanceof Error ? error.message : "Unknown error occurred",
        error_type: error instanceof Error ? error.constructor.name : "UnknownError",
        timestamp: (/* @__PURE__ */ new Date()).toISOString(),
        search_query: propsValue.query
      };
    }
  }
});

// ../ap/packages/pieces/community/serp-api/src/lib/actions/search-bing.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework");
var searchBing = (0, import_pieces_framework21.createAction)({
  auth: serpApiAuth,
  name: "search_bing",
  classification: "SEARCH",
  displayName: "Search Bing",
  description: "Run a Bing web search for a query.",
  audience: "ai",
  aiMetadata: {
    description: "Search the web with Bing and get ranked organic results for a query. Use as an alternative web search engine to cross-check or supplement Google web results. Paginate with Count (results per page) and First (offset of the first result).",
    idempotent: !0
  },
  outputSchema: searchBingOutputSchema,
  props: {
    query: import_pieces_framework21.Property.ShortText({
      displayName: "Search Query",
      description: 'The web search query to execute, e.g. "climate change report".',
      required: !0
    }),
    cc: import_pieces_framework21.Property.ShortText({
      displayName: "Country Code (cc)",
      description: 'Two-letter country code to scope results, e.g. "us", "gb". Optional.',
      required: !1
    }),
    mkt: import_pieces_framework21.Property.ShortText({
      displayName: "Market (mkt)",
      description: 'Market locale in language-country form, e.g. "en-US", "de-DE". Optional.',
      required: !1
    }),
    count: import_pieces_framework21.Property.Number({
      displayName: "Count",
      description: "Number of results to return per page. Optional.",
      required: !1
    }),
    first: import_pieces_framework21.Property.Number({
      displayName: "First Result Offset",
      description: "One-based index of the first result to return, for pagination. Optional.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    try {
      let client = new SerpApiClient({
        defaultTimeout: 3e4,
        defaultRetries: 3,
        defaultRetryDelay: 1e3,
        enableLogging: !1
      }), searchConfig = {
        api_key: auth.secret_text,
        engine: "bing" /* BING */,
        q: propsValue.query,
        count: propsValue.count,
        first: propsValue.first
      };
      propsValue.cc && (searchConfig.cc = propsValue.cc), propsValue.mkt && (searchConfig.mkt = propsValue.mkt);
      let response = await client.executeSearch(searchConfig);
      return __spreadValues({
        success: !0
      }, response);
    } catch (error) {
      return {
        success: !1,
        error: error instanceof Error ? error.message : "Unknown error occurred",
        error_type: error instanceof Error ? error.constructor.name : "UnknownError",
        timestamp: (/* @__PURE__ */ new Date()).toISOString(),
        search_query: propsValue.query
      };
    }
  }
});

// ../ap/packages/pieces/community/serp-api/src/lib/actions/search-duckduckgo.ts
var import_pieces_framework22 = require("@activepieces/pieces-framework");
var searchDuckduckgo = (0, import_pieces_framework22.createAction)({
  auth: serpApiAuth,
  name: "search_duckduckgo",
  classification: "SEARCH",
  displayName: "Search DuckDuckGo",
  description: "Run a DuckDuckGo web search for a query.",
  audience: "ai",
  aiMetadata: {
    description: "Search the web with DuckDuckGo and get ranked organic results for a query. Use as a privacy-oriented alternative web search engine to cross-check or supplement Google and Bing web results. Scope results to a region with the Region code.",
    idempotent: !0
  },
  outputSchema: searchDuckduckgoOutputSchema,
  props: {
    query: import_pieces_framework22.Property.ShortText({
      displayName: "Search Query",
      description: 'The web search query to execute, e.g. "open source databases".',
      required: !0
    }),
    kl: import_pieces_framework22.Property.ShortText({
      displayName: "Region (kl)",
      description: 'Region locale in country-language form, e.g. "us-en", "uk-en", "de-de". Optional.',
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    try {
      let client = new SerpApiClient({
        defaultTimeout: 3e4,
        defaultRetries: 3,
        defaultRetryDelay: 1e3,
        enableLogging: !1
      }), searchConfig = {
        api_key: auth.secret_text,
        engine: "duckduckgo" /* DUCKDUCKGO */,
        q: propsValue.query
      };
      propsValue.kl && (searchConfig.kl = propsValue.kl);
      let response = await client.executeSearch(searchConfig);
      return __spreadValues({
        success: !0
      }, response);
    } catch (error) {
      return {
        success: !1,
        error: error instanceof Error ? error.message : "Unknown error occurred",
        error_type: error instanceof Error ? error.constructor.name : "UnknownError",
        timestamp: (/* @__PURE__ */ new Date()).toISOString(),
        search_query: propsValue.query
      };
    }
  }
});

// ../ap/packages/pieces/community/serp-api/src/index.ts
var serpApi = (0, import_pieces_framework23.createPiece)({
  displayName: "SerpApi",
  description: "Search Google, YouTube, News, and Trends with powerful filtering and analysis capabilities",
  auth: serpApiAuth,
  minimumSupportedRelease: "0.86.4",
  logoUrl: "https://cdn.activepieces.com/pieces/serp-api.png",
  authors: ["AnkitSharmaOnGithub"],
  actions: [
    googleSearch,
    googleNewsSearch,
    youtubeSearch,
    googleTrendsSearch,
    searchGoogleWebAi,
    searchGoogleNewsAi,
    searchYoutubeAi,
    searchGoogleTrendsAi,
    searchGoogleMaps,
    searchGoogleScholar,
    searchGoogleImages,
    searchGoogleShopping,
    searchGoogleJobs,
    searchGoogleLens,
    searchGoogleLocalServices,
    searchAppleAppStore,
    searchGooglePlay,
    searchYelp,
    searchWalmart,
    searchBing,
    searchDuckduckgo
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  serpApi
});
