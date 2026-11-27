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

// ../ap/packages/pieces/community/pdf-co/src/index.ts
var index_exports = {};
__export(index_exports, {
  pdfCo: () => pdfCo
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework11 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/pdf-co/src/lib/actions/search-and-replace-text.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/pdf-co/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), pdfCoAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "To get your PDF.co API key please [click here to create your account](https://app.pdf.co/).",
  required: !0
});

// ../ap/packages/pieces/community/pdf-co/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), BASE_URL = "https://api.pdf.co/v1", commonProps = {
  fileName: import_pieces_framework2.Property.ShortText({
    displayName: "File Name",
    description: 'Desired name for the output PDF file (e.g., "result.pdf").',
    required: !1
  }),
  expiration: import_pieces_framework2.Property.Number({
    displayName: "Expiration Time in Minutes",
    description: "Set the expiration time for the output link in minutes (default is 60 i.e 60 minutes or 1 hour).",
    required: !1
  }),
  pdfPassword: import_pieces_framework2.Property.ShortText({
    displayName: "Source PDF Password",
    description: "Password if the source PDF is protected.",
    required: !1
  }),
  httpUsername: import_pieces_framework2.Property.ShortText({
    displayName: "HTTP Username",
    description: "HTTP auth username if required to access source url.",
    required: !1
  }),
  httpPassword: import_pieces_framework2.Property.ShortText({
    displayName: "HTTP Password",
    description: "HTTP auth password if required to access source url.",
    required: !1
  })
};

// ../ap/packages/pieces/community/pdf-co/src/lib/actions/search-and-replace-text.ts
var searchAndReplaceText = (0, import_pieces_framework3.createAction)({
  name: "search_and_replace_text",
  classification: "WRITE",
  displayName: "Search and Replace Text in PDF",
  description: "Search for specific text or patterns in a PDF and replace it with new text.",
  audience: "both",
  aiMetadata: {
    description: "Finds occurrences of given text in a source PDF (referenced by URL) and replaces them with new text, with optional case-sensitive matching or regular-expression patterns instead of literal search. Use when an agent needs to edit textual content in an existing document. Each call produces a new output PDF file and consumes credits, so it is not idempotent.",
    idempotent: !1
  },
  auth: pdfCoAuth,
  props: __spreadValues({
    url: import_pieces_framework3.Property.ShortText({
      displayName: "PDF URL",
      description: "URL to the source PDF file.",
      required: !0
    }),
    searchStrings: import_pieces_framework3.Property.Array({
      displayName: "Text to Locate",
      required: !0
    }),
    replaceStrings: import_pieces_framework3.Property.Array({
      displayName: "Replacement Text",
      required: !0
    }),
    caseSensitive: import_pieces_framework3.Property.Checkbox({
      displayName: "Case Sensitive",
      description: "Set to true for case-sensitive search, false otherwise.",
      required: !1,
      defaultValue: !0
    }),
    regex: import_pieces_framework3.Property.Checkbox({
      displayName: "Use Regular Expressions ?",
      description: "Set to true to use regular expressions for search texts.",
      required: !1,
      defaultValue: !1
    }),
    pages: import_pieces_framework3.Property.ShortText({
      displayName: "Pages",
      description: 'Comma-separated page numbers or ranges (e.g., "0,2,5-10"). Leave empty for all pages.',
      required: !1
    })
  }, commonProps),
  async run(context) {
    var _a;
    let { auth, propsValue } = context, {
      url,
      searchStrings,
      replaceStrings,
      caseSensitive,
      regex,
      pages,
      fileName,
      httpPassword,
      httpUsername,
      pdfPassword,
      expiration
    } = propsValue, requestBody = {
      url,
      searchStrings,
      replaceStrings,
      async: !1,
      caseSensitive,
      regex,
      pages,
      name: fileName,
      expiration,
      httppassword: httpPassword,
      httpusername: httpUsername,
      password: pdfPassword
    };
    try {
      let response = await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.POST,
        url: `${BASE_URL}/pdf/edit/replace-text`,
        headers: {
          "x-api-key": auth.secret_text,
          "Content-Type": "application/json"
        },
        body: requestBody
      });
      if (response.body.error) {
        let errorBody = response.body, errorMessage = `PDF.co API Error: Status ${errorBody.status}.`;
        throw errorBody.message ? errorMessage += ` Message: ${errorBody.message}.` : errorMessage += " An unspecified error occurred.", errorMessage += ` Check input parameters, API key, and PDF.co dashboard for more details. Raw response: ${JSON.stringify(
          errorBody
        )}`, new Error(errorMessage);
      }
      return response.body;
    } catch (error) {
      if (error instanceof import_pieces_common.HttpError) {
        let responseBody = (_a = error.response) == null ? void 0 : _a.body, detailedMessage = `HTTP Error calling PDF.co API: ${error.message}.`;
        throw responseBody && responseBody.message ? detailedMessage += ` Server message: ${responseBody.message}.` : responseBody && (detailedMessage += ` Server response: ${JSON.stringify(responseBody)}.`), new Error(detailedMessage);
      }
      throw error;
    }
  }
});

// ../ap/packages/pieces/community/pdf-co/src/lib/actions/add-text-to-pdf.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var addTextToPdf = (0, import_pieces_framework4.createAction)({
  name: "add_text_to_pdf",
  classification: "WRITE",
  displayName: "Add Text to PDF",
  description: "Adds text to PDF.",
  audience: "both",
  aiMetadata: {
    description: "Stamps a text annotation onto a source PDF (referenced by URL) at the given x/y coordinates, with optional font, size, color, and styling. Use when an agent needs to write text onto an existing document. Each call produces a new output PDF file and consumes credits, so it is not idempotent.",
    idempotent: !1
  },
  auth: pdfCoAuth,
  props: __spreadValues({
    url: import_pieces_framework4.Property.ShortText({
      displayName: "Source PDF URL",
      description: "URL of the PDF file to modify.",
      required: !0
    }),
    text: import_pieces_framework4.Property.LongText({
      displayName: "Text to Add",
      required: !0
    }),
    xCoordinate: import_pieces_framework4.Property.Number({
      displayName: "X Coordinate",
      required: !0
    }),
    yCoordinate: import_pieces_framework4.Property.Number({
      displayName: "Y Coordinate",
      required: !0
    }),
    fontSize: import_pieces_framework4.Property.Number({
      displayName: "Font Size",
      required: !1
    }),
    color: import_pieces_framework4.Property.ShortText({
      displayName: "Color",
      defaultValue: "#000000",
      required: !1
    }),
    fontBold: import_pieces_framework4.Property.Checkbox({
      displayName: "Bold Font ?",
      required: !1
    }),
    fontStrikeout: import_pieces_framework4.Property.Checkbox({
      displayName: "Stikeout Font ?",
      required: !1
    }),
    fontUnderline: import_pieces_framework4.Property.Checkbox({
      displayName: "Underline Font ?",
      required: !1
    }),
    fontName: import_pieces_framework4.Property.ShortText({
      displayName: "Font Name",
      defaultValue: "Arial",
      required: !1
    }),
    pages: import_pieces_framework4.Property.ShortText({
      displayName: "Target Pages",
      description: 'Specify page indices as comma-separated values or ranges to process (e.g. "0, 1, 2-" or "1, 2, 3-7").',
      required: !1
    }),
    textBoxHeight: import_pieces_framework4.Property.Number({
      displayName: "Text Box Height",
      required: !1
    }),
    textBoxWidth: import_pieces_framework4.Property.Number({
      displayName: "Text Box Width",
      required: !1
    }),
    textBoxAlignment: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Text Box Alignment",
      required: !1,
      defaultValue: "left",
      options: {
        disabled: !1,
        options: [
          { label: "left", value: "left" },
          { label: "right", value: "right" },
          { label: "center", value: "center" }
        ]
      }
    })
  }, commonProps),
  async run(context) {
    var _a;
    let { auth, propsValue } = context, {
      url,
      xCoordinate,
      yCoordinate,
      fontSize,
      fontName,
      fontBold,
      fontStrikeout,
      fontUnderline,
      color,
      pages,
      textBoxAlignment,
      textBoxHeight,
      textBoxWidth,
      text,
      fileName,
      pdfPassword,
      httpPassword,
      httpUsername,
      expiration
    } = propsValue, requestBody = {
      url,
      annotations: [{
        x: xCoordinate,
        y: yCoordinate,
        text,
        type: "text",
        color,
        pages,
        width: textBoxWidth,
        height: textBoxHeight,
        alignment: textBoxAlignment,
        size: fontSize,
        fontName,
        fontBold,
        fontStrikeout,
        fontUnderline
      }],
      async: !1,
      name: fileName,
      expiration,
      httppassword: httpPassword,
      httpusername: httpUsername,
      password: pdfPassword
    };
    try {
      let response = await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.POST,
        url: `${BASE_URL}/pdf/edit/add`,
        headers: {
          "x-api-key": auth.secret_text,
          "Content-Type": "application/json"
        },
        body: requestBody
      });
      if (response.body.error) {
        let errorBody = response.body, errorMessage = `PDF.co API Error (Add Text): Status ${errorBody.status}.`;
        throw errorBody.message ? errorMessage += ` Message: ${errorBody.message}.` : errorMessage += " An unspecified error occurred.", errorMessage += ` Raw response: ${JSON.stringify(errorBody)}`, new Error(errorMessage);
      }
      let successBody = response.body;
      return {
        outputUrl: successBody.url,
        pageCount: successBody.pageCount,
        outputName: successBody.name,
        creditsUsed: successBody.credits,
        remainingCredits: successBody.remainingCredits
      };
    } catch (error) {
      if (error instanceof import_pieces_common2.HttpError) {
        let responseBody = (_a = error.response) == null ? void 0 : _a.body, detailedMessage = `HTTP Error calling PDF.co API (Add Text): ${error.message}.`;
        throw responseBody && responseBody.message ? detailedMessage += ` Server message: ${responseBody.message}.` : responseBody && (detailedMessage += ` Server response: ${JSON.stringify(responseBody)}.`), new Error(detailedMessage);
      }
      throw error;
    }
  }
});

// ../ap/packages/pieces/community/pdf-co/src/lib/actions/add-image-to-pdf.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var addImageToPdf = (0, import_pieces_framework5.createAction)({
  name: "add_image_to_pdf",
  classification: "WRITE",
  displayName: "Add Image to PDF",
  description: "Add image to a PDF document.",
  audience: "both",
  aiMetadata: {
    description: "Overlays an image (referenced by URL) onto a source PDF (referenced by URL) at the given x/y coordinates, optionally on specific pages. Use when an agent needs to stamp a logo, signature, or other graphic onto an existing document. Each call produces a new output PDF file and consumes credits, so it is not idempotent.",
    idempotent: !1
  },
  auth: pdfCoAuth,
  props: __spreadValues({
    url: import_pieces_framework5.Property.ShortText({
      displayName: "Source PDF URL",
      description: "URL of the PDF file to modify.",
      required: !0
    }),
    imageUrl: import_pieces_framework5.Property.ShortText({
      displayName: "Image URL",
      required: !0
    }),
    xCoordinate: import_pieces_framework5.Property.Number({
      displayName: "X Coordinate",
      description: "X coordinate (from top-left corner) to place the image.",
      required: !0
    }),
    yCoordinate: import_pieces_framework5.Property.Number({
      displayName: "Y Coordinate",
      required: !0,
      description: "Y coordinate (from top-left corner) to place the image."
    }),
    width: import_pieces_framework5.Property.Number({
      displayName: "Width",
      description: "Optional width for the image on the PDF (in points). Aspect ratio is kept by default.",
      required: !1
    }),
    height: import_pieces_framework5.Property.Number({
      displayName: "Height",
      description: "Optional height for the image on the PDF (in points). Aspect ratio is kept by default.",
      required: !1
    }),
    pages: import_pieces_framework5.Property.ShortText({
      displayName: "Target Pages",
      description: 'Specify page indices as comma-separated values or ranges to process (e.g. "0, 1, 2-" or "1, 2, 3-7").',
      required: !1
    })
  }, commonProps),
  async run(context) {
    var _a;
    let { auth, propsValue } = context, {
      url,
      imageUrl,
      fileName,
      pdfPassword,
      xCoordinate,
      pages,
      yCoordinate,
      width,
      height,
      expiration,
      httpPassword,
      httpUsername
    } = propsValue, requestBody = {
      url,
      images: [{
        url: imageUrl,
        x: xCoordinate,
        y: yCoordinate,
        pages,
        height,
        width
      }],
      async: !1,
      name: fileName,
      expiration,
      httppassword: httpPassword,
      httpusername: httpUsername,
      password: pdfPassword,
      inline: !1
    };
    try {
      let response = await import_pieces_common3.httpClient.sendRequest({
        method: import_pieces_common3.HttpMethod.POST,
        url: `${BASE_URL}/pdf/edit/add`,
        headers: {
          "x-api-key": auth.secret_text,
          "Content-Type": "application/json"
        },
        body: requestBody
      });
      if (console.log(JSON.stringify(response, null, 2)), response.body.error) {
        let errorBody = response.body, errorMessage = `PDF.co API Error (Add Image): Status ${errorBody.status}.`;
        throw errorBody.message ? errorMessage += ` Message: ${errorBody.message}.` : errorMessage += " An unspecified error occurred.", errorMessage += ` Raw response: ${JSON.stringify(errorBody)}`, new Error(errorMessage);
      }
      let successBody = response.body;
      return {
        outputUrl: successBody.url,
        pageCount: successBody.pageCount,
        outputName: successBody.name,
        creditsUsed: successBody.credits,
        remainingCredits: successBody.remainingCredits
      };
    } catch (error) {
      if (error instanceof import_pieces_common3.HttpError) {
        let responseBody = (_a = error.response) == null ? void 0 : _a.body, detailedMessage = `HTTP Error calling PDF.co API (Add Image): ${error.message}.`;
        throw responseBody && responseBody.message ? detailedMessage += ` Server message: ${responseBody.message}.` : responseBody && (detailedMessage += ` Server response: ${JSON.stringify(responseBody)}.`), new Error(detailedMessage);
      }
      throw error;
    }
  }
});

// ../ap/packages/pieces/community/pdf-co/src/lib/actions/convert-html-to-pdf.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var convertHtmlToPdf = (0, import_pieces_framework6.createAction)({
  name: "convert_html_to_pdf",
  classification: "WRITE",
  displayName: "Convert HTML to PDF",
  description: "Convert HTML code into a downloadable PDF document.",
  audience: "both",
  aiMetadata: {
    description: "Renders a block of HTML into a new PDF document, with optional paper size, orientation, margins, header/footer, and media-type controls. Use when an agent needs to turn HTML markup into a downloadable PDF. Each call generates a new output file and consumes credits, so it is not idempotent.",
    idempotent: !1
  },
  auth: pdfCoAuth,
  props: {
    html: import_pieces_framework6.Property.LongText({
      displayName: "HTML Content",
      description: "The HTML code to convert to PDF.",
      required: !0
    }),
    name: import_pieces_framework6.Property.ShortText({
      displayName: "Output File Name",
      description: 'Desired name for the output PDF file (e.g., "result.pdf").',
      required: !1
    }),
    margins: import_pieces_framework6.Property.ShortText({
      displayName: "Margins",
      description: 'CSS style margins (e.g., "10px", "5mm 5mm 5mm 5mm" for top, right, bottom, left).',
      required: !1
    }),
    paperSize: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Paper Size",
      description: "Select a paper size. For custom sizes, input the value directly (e.g., '200mm 300mm') if your desired size isn't listed. Refer to PDF.co docs.",
      required: !1,
      options: {
        disabled: !1,
        placeholder: "Select paper size or input custom",
        options: [
          { label: "A4 (Default)", value: "A4" },
          { label: "Letter", value: "Letter" },
          { label: "Legal", value: "Legal" },
          { label: "Tabloid", value: "Tabloid" },
          { label: "Ledger", value: "Ledger" },
          { label: "A0", value: "A0" },
          { label: "A1", value: "A1" },
          { label: "A2", value: "A2" },
          { label: "A3", value: "A3" },
          { label: "A5", value: "A5" },
          { label: "A6", value: "A6" }
        ]
      }
    }),
    orientation: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Orientation",
      description: "Set page orientation.",
      required: !1,
      options: {
        disabled: !1,
        placeholder: "Portrait (Default)",
        options: [
          { label: "Portrait (Default)", value: "Portrait" },
          { label: "Landscape", value: "Landscape" }
        ]
      }
    }),
    printBackground: import_pieces_framework6.Property.Checkbox({
      displayName: "Print Background ?",
      description: "Set to true to print background graphics and colors (default is true).",
      required: !1,
      defaultValue: !0
    }),
    mediaType: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Media Type",
      description: "CSS media type to emulate.",
      required: !1,
      options: {
        disabled: !1,
        placeholder: "print (Default)",
        options: [
          { label: "print (Default)", value: "print" },
          { label: "screen", value: "screen" },
          { label: "none", value: "none" }
        ]
      }
    }),
    header: import_pieces_framework6.Property.LongText({
      displayName: "Header HTML",
      description: "HTML content for the page header.",
      required: !1
    }),
    footer: import_pieces_framework6.Property.LongText({
      displayName: "Footer HTML",
      description: "HTML content for the page footer.",
      required: !1
    }),
    doNotWaitFullLoad: import_pieces_framework6.Property.Checkbox({
      displayName: "Do not wait till full page load ?",
      required: !1
    }),
    expiration: import_pieces_framework6.Property.Number({
      displayName: "Output Link Expiration (minutes)",
      description: "Set the expiration time for the output link in minutes (default is 60).",
      required: !1
    }),
    profiles: import_pieces_framework6.Property.Json({
      displayName: "Profiles",
      description: "JSON object for additional configurations.",
      required: !1
    })
  },
  async run(context) {
    var _a;
    let { auth, propsValue } = context, requestBody = {
      html: propsValue.html,
      async: !1,
      DoNotWaitFullLoad: propsValue.doNotWaitFullLoad
    };
    propsValue.name !== void 0 && propsValue.name !== "" && (requestBody.name = propsValue.name), propsValue.margins !== void 0 && propsValue.margins !== "" && (requestBody.margins = propsValue.margins), propsValue.paperSize !== void 0 && (requestBody.paperSize = propsValue.paperSize), propsValue.orientation !== void 0 && (requestBody.orientation = propsValue.orientation), propsValue.printBackground !== void 0 && (requestBody.printBackground = propsValue.printBackground), propsValue.mediaType !== void 0 && (requestBody.mediaType = propsValue.mediaType), propsValue.header !== void 0 && propsValue.header !== "" && (requestBody.header = propsValue.header), propsValue.footer !== void 0 && propsValue.footer !== "" && (requestBody.footer = propsValue.footer), propsValue.expiration !== void 0 && (requestBody.expiration = propsValue.expiration), propsValue.profiles !== void 0 && typeof propsValue.profiles == "object" && propsValue.profiles !== null && (requestBody.profiles = propsValue.profiles);
    try {
      let response = await import_pieces_common4.httpClient.sendRequest({
        method: import_pieces_common4.HttpMethod.POST,
        url: `${BASE_URL}/pdf/convert/from/html`,
        headers: {
          "x-api-key": auth.secret_text,
          "Content-Type": "application/json"
        },
        body: requestBody
      });
      if (response.body.error) {
        let errorBody = response.body, errorMessage = `PDF.co API Error (Convert HTML to PDF): Status ${errorBody.status}.`;
        throw errorBody.message ? errorMessage += ` Message: ${errorBody.message}.` : errorMessage += " An unspecified error occurred.", errorMessage += ` Raw response: ${JSON.stringify(errorBody)}`, new Error(errorMessage);
      }
      let successBody = response.body;
      return {
        outputUrl: successBody.url,
        pageCount: successBody.pageCount,
        outputName: successBody.name,
        creditsUsed: successBody.credits,
        remainingCredits: successBody.remainingCredits
      };
    } catch (error) {
      if (error instanceof import_pieces_common4.HttpError) {
        let responseBody = (_a = error.response) == null ? void 0 : _a.body, detailedMessage = `HTTP Error calling PDF.co API (Convert HTML to PDF): ${error.message}.`;
        throw responseBody && responseBody.message ? detailedMessage += ` Server message: ${responseBody.message}.` : responseBody && (detailedMessage += ` Server response: ${JSON.stringify(responseBody)}.`), new Error(detailedMessage);
      }
      throw error;
    }
  }
});

// ../ap/packages/pieces/community/pdf-co/src/lib/actions/extract-text-from-pdf.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var extractTextFromPdf = (0, import_pieces_framework7.createAction)({
  name: "extract_text_from_pdf",
  classification: "READ",
  displayName: "Extract Plain Text from PDF",
  description: "Extracts plain text content from a PDF document.",
  audience: "both",
  aiMetadata: {
    description: "Extracts plain text content from a source PDF (referenced by URL), optionally limited to specific pages. Use when an agent needs the raw text of a document for reading or downstream processing. The call only reads the input and returns the text directly, so it is idempotent.",
    idempotent: !0
  },
  auth: pdfCoAuth,
  props: {
    url: import_pieces_framework7.Property.ShortText({
      displayName: "Source PDF URL",
      description: "URL of the PDF file to extract text from.",
      required: !0
    }),
    pages: import_pieces_framework7.Property.ShortText({
      displayName: "Pages",
      description: 'Comma-separated page numbers or ranges (e.g., "0,2,5-10"). Leave empty for all pages.',
      required: !1
    }),
    password: commonProps.pdfPassword,
    outputName: commonProps.fileName,
    httpUsername: commonProps.httpUsername,
    httpPassword: commonProps.httpPassword
  },
  async run(context) {
    var _a;
    let { auth, propsValue } = context, {
      url,
      pages,
      password,
      outputName,
      httpPassword,
      httpUsername
    } = propsValue, requestBody = {
      url,
      async: !1,
      httpusername: httpUsername,
      httppassword: httpPassword,
      inline: !0
      // Get text directly in response.body.body
    };
    pages !== void 0 && pages !== "" && (requestBody.pages = pages), password !== void 0 && password !== "" && (requestBody.password = password), outputName !== void 0 && outputName !== "" && (requestBody.name = outputName);
    try {
      let response = await import_pieces_common5.httpClient.sendRequest({
        method: import_pieces_common5.HttpMethod.POST,
        url: `${BASE_URL}/pdf/convert/to/text-simple`,
        headers: {
          "x-api-key": auth.secret_text,
          "Content-Type": "application/json"
        },
        body: requestBody
      });
      if (response.body.error) {
        let errorBody = response.body, errorMessage = `PDF.co API Error (Extract Text): Status ${errorBody.status}.`;
        throw errorBody.message ? errorMessage += ` Message: ${errorBody.message}.` : errorMessage += " An unspecified error occurred.", errorMessage += ` Raw response: ${JSON.stringify(errorBody)}`, new Error(errorMessage);
      }
      let successBody = response.body;
      return {
        extractedText: successBody.body,
        pageCount: successBody.pageCount,
        outputName: successBody.name,
        creditsUsed: successBody.credits,
        remainingCredits: successBody.remainingCredits
      };
    } catch (error) {
      if (error instanceof import_pieces_common5.HttpError) {
        let responseBody = (_a = error.response) == null ? void 0 : _a.body, detailedMessage = `HTTP Error calling PDF.co API (Extract Text): ${error.message}.`;
        throw responseBody && responseBody.message ? detailedMessage += ` Server message: ${responseBody.message}.` : responseBody && (detailedMessage += ` Server response: ${JSON.stringify(responseBody)}.`), new Error(detailedMessage);
      }
      throw error;
    }
  }
});

// ../ap/packages/pieces/community/pdf-co/src/lib/actions/convert-pdf-to-structured-format.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var convertPdfToStructuredFormat = (0, import_pieces_framework8.createAction)({
  name: "convert_pdf_to_structured_format",
  classification: "READ",
  displayName: "Convert PDF to JSON/CSV/XML",
  description: "Convert PDF content into structured formats (JSON, CSV, or XML).",
  audience: "both",
  aiMetadata: {
    description: "Converts a source PDF (referenced by URL) into structured data, with the output format selectable between JSON, CSV, or XML. Optional OCR language handles scanned documents. Use when an agent needs machine-readable data extracted from a PDF. Each call generates a new output file and consumes credits, so it is not idempotent.",
    idempotent: !1
  },
  auth: pdfCoAuth,
  props: __spreadValues({
    url: import_pieces_framework8.Property.ShortText({
      displayName: "Source PDF URL",
      description: "URL of the PDF file to convert.",
      required: !0
    }),
    outputFormat: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Output Format",
      description: "Select the desired structured output format.",
      required: !0,
      options: {
        disabled: !1,
        options: [
          { label: "JSON", value: "json" },
          { label: "CSV", value: "csv" },
          { label: "XML", value: "xml" }
        ]
      }
    }),
    pages: import_pieces_framework8.Property.ShortText({
      displayName: "Pages",
      description: 'Comma-separated page numbers or ranges (e.g., "0,2,5-10"). Leave empty for all pages.',
      required: !1
    }),
    lang: import_pieces_framework8.Property.ShortText({
      displayName: "OCR Language",
      description: 'Language for OCR if processing scanned documents (e.g., "eng", "deu", "eng+deu"). See PDF.co docs for list.',
      required: !1
    }),
    profiles: import_pieces_framework8.Property.Json({
      displayName: "Profiles",
      description: "JSON object for additional configurations.",
      required: !1
    })
  }, commonProps),
  async run(context) {
    var _a;
    let { auth, propsValue } = context, {
      url,
      outputFormat,
      pages,
      lang,
      pdfPassword,
      fileName,
      httpPassword,
      httpUsername,
      expiration,
      profiles
    } = propsValue, endpoint = "";
    switch (outputFormat) {
      case "json":
        endpoint = `${BASE_URL}/pdf/convert/to/json2`;
        break;
      case "csv":
        endpoint = `${BASE_URL}/pdf/convert/to/csv`;
        break;
      case "xml":
        endpoint = `${BASE_URL}/pdf/convert/to/xml`;
        break;
      default:
        throw new Error(`Unsupported output format: ${outputFormat}`);
    }
    let requestBody = {
      url,
      async: !1,
      httppassword: httpPassword,
      httpusername: httpUsername,
      inline: !1
      // Ensure we get the URL to the output file
    };
    pages !== void 0 && pages !== "" && (requestBody.pages = pages), lang !== void 0 && lang !== "" && (requestBody.lang = lang), pdfPassword !== void 0 && pdfPassword !== "" && (requestBody.password = pdfPassword), fileName !== void 0 && fileName !== "" && (requestBody.name = fileName), expiration !== void 0 && (requestBody.expiration = expiration), profiles !== void 0 && typeof profiles == "object" && profiles !== null && (requestBody.profiles = profiles);
    try {
      let response = await import_pieces_common6.httpClient.sendRequest({
        method: import_pieces_common6.HttpMethod.POST,
        url: endpoint,
        headers: {
          "x-api-key": auth.secret_text,
          "Content-Type": "application/json"
        },
        body: requestBody
      });
      if (response.body.error) {
        let errorBody = response.body, errorMessage = `PDF.co API Error (Convert PDF to ${outputFormat.toUpperCase()}): Status ${errorBody.status}.`;
        throw errorBody.message ? errorMessage += ` Message: ${errorBody.message}.` : errorMessage += " An unspecified error occurred.", errorMessage += ` Raw response: ${JSON.stringify(errorBody)}`, new Error(errorMessage);
      }
      return response.body;
    } catch (error) {
      if (error instanceof import_pieces_common6.HttpError) {
        let responseBody = (_a = error.response) == null ? void 0 : _a.body, detailedMessage = `HTTP Error calling PDF.co API (Convert PDF to ${outputFormat.toUpperCase()}): ${error.message}.`;
        throw responseBody && responseBody.message ? detailedMessage += ` Server message: ${responseBody.message}.` : responseBody && (detailedMessage += ` Server response: ${JSON.stringify(responseBody)}.`), new Error(detailedMessage);
      }
      throw error;
    }
  }
});

// ../ap/packages/pieces/community/pdf-co/src/lib/actions/extract-tables-from-pdf.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var extractTablesFromPdf = (0, import_pieces_framework9.createAction)({
  name: "extract_tables_from_pdf",
  classification: "READ",
  displayName: "Extract Tables from PDF (using Template)",
  description: "Extracts table data from a PDF using a predefined PDF.co Document Parser template.",
  audience: "both",
  aiMetadata: {
    description: "Extracts table data from a source PDF (referenced by URL) using a predefined PDF.co Document Parser template; requires a Template ID created beforehand in the PDF.co dashboard. Use when an agent needs structured tables from a document whose layout a template already describes. The call only reads the input and returns the parsed tables, so it is idempotent.",
    idempotent: !0
  },
  auth: pdfCoAuth,
  // Inherits auth from the piece
  props: __spreadValues({
    url: import_pieces_framework9.Property.ShortText({
      displayName: "Source PDF URL",
      description: "URL of the PDF file to extract tables from.",
      required: !0
    }),
    templateId: import_pieces_framework9.Property.ShortText({
      displayName: "Template ID",
      description: "The ID of your Document Parser template (created in PDF.co dashboard) designed to extract the table(s).",
      required: !0
    }),
    pages: import_pieces_framework9.Property.ShortText({
      displayName: "Pages",
      description: 'Comma-separated page numbers or ranges (e.g., "0,2,5-10"). Overrides template settings if provided.',
      required: !1
    }),
    profiles: import_pieces_framework9.Property.Json({
      displayName: "Profiles",
      description: "JSON object for additional configurations.",
      required: !1
    })
  }, commonProps),
  async run(context) {
    var _a;
    let { auth, propsValue } = context, {
      url,
      templateId,
      pages,
      pdfPassword,
      fileName,
      httpPassword,
      httpUsername,
      expiration,
      profiles
    } = propsValue, requestBody = {
      url,
      templateId,
      httppassword: httpPassword,
      httpusername: httpUsername,
      async: !1,
      inline: !0,
      // Get the parsed data directly
      outputFormat: "JSON"
      // We want JSON to process tables
    };
    pages !== void 0 && pages !== "" && (requestBody.pages = pages), pdfPassword !== void 0 && pdfPassword !== "" && (requestBody.password = pdfPassword), fileName !== void 0 && fileName !== "" && (requestBody.name = fileName), expiration !== void 0 && (requestBody.expiration = expiration), profiles !== void 0 && typeof profiles == "object" && profiles !== null && (requestBody.profiles = profiles);
    try {
      let response = await import_pieces_common7.httpClient.sendRequest({
        method: import_pieces_common7.HttpMethod.POST,
        url: `${BASE_URL}/pdf/documentparser`,
        headers: {
          "x-api-key": auth.secret_text,
          "Content-Type": "application/json"
        },
        body: requestBody
      });
      if (response.body.error) {
        let errorBody = response.body, errorMessage = `PDF.co API Error (Extract Tables): Status ${errorBody.status}.`;
        throw errorBody.message ? errorMessage += ` Message: ${errorBody.message}.` : errorMessage += " An unspecified error occurred.", errorMessage += ` Raw response: ${JSON.stringify(errorBody)}`, new Error(errorMessage);
      }
      let successBody = response.body;
      return {
        extractedTables: successBody.body.objects.filter((obj) => obj.objectType === "table"),
        // Array of table objects found by the template
        templateNameUsed: successBody.body.templateName
      };
    } catch (error) {
      if (error instanceof import_pieces_common7.HttpError) {
        let responseBody = (_a = error.response) == null ? void 0 : _a.body, detailedMessage = `HTTP Error calling PDF.co API (Extract Tables): ${error.message}.`;
        throw responseBody && responseBody.message ? detailedMessage += ` Server message: ${responseBody.message}.` : responseBody && (detailedMessage += ` Server response: ${JSON.stringify(responseBody)}.`), new Error(detailedMessage);
      }
      throw error;
    }
  }
});

// ../ap/packages/pieces/community/pdf-co/src/lib/actions/add-barcode-to-pdf.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var barcodeTypes = [
  { label: "QR Code (Default)", value: "QRCode" },
  { label: "DataMatrix", value: "DataMatrix" },
  { label: "Code 128", value: "Code128" },
  { label: "Code 39", value: "Code39" },
  { label: "PDF417", value: "PDF417" },
  { label: "EAN-13", value: "EAN13" },
  { label: "UPC-A", value: "UPCA" }
], addBarcodeToPdf = (0, import_pieces_framework10.createAction)({
  name: "add_barcode_to_pdf",
  classification: "WRITE",
  displayName: "Add Barcode to PDF",
  description: "Generate a barcode image and add it to a specific location on a PDF.",
  audience: "both",
  aiMetadata: {
    description: "Generates a barcode (QR, DataMatrix, Code 128/39, PDF417, EAN-13, or UPC-A) and stamps it onto a source PDF (referenced by URL) at the given x/y coordinates. Use when an agent needs to embed a scannable code into an existing document. Each call produces a new output PDF file and consumes credits, so it is not idempotent.",
    idempotent: !1
  },
  auth: pdfCoAuth,
  props: __spreadValues({
    sourcePdfUrl: import_pieces_framework10.Property.ShortText({
      displayName: "Source PDF URL",
      description: "URL of the PDF file to add the barcode to.",
      required: !0
    }),
    barcodeValue: import_pieces_framework10.Property.ShortText({
      displayName: "Barcode Value",
      description: "The text or data to encode in the barcode.",
      required: !0
    }),
    barcodeType: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Barcode Type",
      description: "Select the type of barcode to generate.",
      required: !0,
      options: { disabled: !1, options: barcodeTypes, placeholder: "Select Barcode Type" }
    }),
    x: import_pieces_framework10.Property.Number({
      displayName: "X Coordinate",
      description: "X coordinate (from top-left corner) to place the barcode.",
      required: !0
    }),
    y: import_pieces_framework10.Property.Number({
      displayName: "Y Coordinate",
      description: "Y coordinate (from top-left corner) to place the barcode.",
      required: !0
    }),
    width: import_pieces_framework10.Property.Number({
      displayName: "Width (optional)",
      description: "Optional width for the barcode image on the PDF (in points). Aspect ratio is kept by default.",
      required: !1
    }),
    height: import_pieces_framework10.Property.Number({
      displayName: "Height (optional)",
      description: "Optional height for the barcode image on the PDF (in points). Aspect ratio is kept by default.",
      required: !1
    }),
    pages: import_pieces_framework10.Property.ShortText({
      displayName: "Pages",
      description: 'Comma-separated page numbers or ranges to add the barcode (e.g., "0,2,5-10"). Leave empty for all pages.',
      required: !1
    })
  }, commonProps),
  async run(context) {
    var _a, _b;
    let { auth, propsValue } = context, {
      sourcePdfUrl,
      barcodeValue,
      barcodeType,
      x,
      y,
      width,
      height,
      pages,
      fileName,
      pdfPassword,
      httpPassword,
      httpUsername,
      expiration
    } = propsValue, barcodeImageUrl = "", generateBarcodeBody = {
      value: barcodeValue,
      type: barcodeType,
      async: !1,
      inline: !1
      // Need the URL
    };
    try {
      let generateResponse = await import_pieces_common8.httpClient.sendRequest({
        method: import_pieces_common8.HttpMethod.POST,
        url: `${BASE_URL}/barcode/generate`,
        headers: {
          "x-api-key": auth.secret_text,
          "Content-Type": "application/json"
        },
        body: generateBarcodeBody
      });
      if (generateResponse.body.error) {
        let errorBody = generateResponse.body;
        throw new Error(
          `PDF.co Barcode Generation Error: Status ${errorBody.status}. ${errorBody.message || "Unknown error."}`
        );
      }
      if (barcodeImageUrl = generateResponse.body.url, !barcodeImageUrl)
        throw new Error("Failed to get barcode image URL from PDF.co response.");
    } catch (error) {
      if (error instanceof import_pieces_common8.HttpError) {
        let responseBody = (_a = error.response) == null ? void 0 : _a.body;
        throw new Error(
          `HTTP Error generating barcode: ${error.message}. ${responseBody != null && responseBody.message ? "Server message: " + responseBody.message : "Raw response: " + JSON.stringify(responseBody)}`
        );
      }
      throw error;
    }
    let addImageBody = {
      url: sourcePdfUrl,
      images: [{
        url: barcodeImageUrl,
        x,
        y,
        width,
        height,
        pages
      }],
      async: !1,
      inline: !1,
      // Get final PDF URL
      name: fileName,
      expiration,
      httppassword: httpPassword,
      httpusername: httpUsername,
      password: pdfPassword
    };
    try {
      let addResponse = await import_pieces_common8.httpClient.sendRequest({
        method: import_pieces_common8.HttpMethod.POST,
        url: `${BASE_URL}/pdf/edit/add`,
        headers: {
          "x-api-key": auth.secret_text,
          "Content-Type": "application/json"
        },
        body: addImageBody
      });
      if (addResponse.body.error) {
        let errorBody = addResponse.body;
        throw new Error(
          `PDF.co Add Image Error: Status ${errorBody.status}. ${errorBody.message || "Unknown error."}`
        );
      }
      return addResponse.body;
    } catch (error) {
      if (error instanceof import_pieces_common8.HttpError) {
        let responseBody = (_b = error.response) == null ? void 0 : _b.body;
        throw new Error(
          `HTTP Error adding barcode image to PDF: ${error.message}. ${responseBody != null && responseBody.message ? "Server message: " + responseBody.message : "Raw response: " + JSON.stringify(responseBody)}`
        );
      }
      throw error;
    }
  }
});

// ../ap/packages/pieces/community/pdf-co/src/index.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var pdfCo = (0, import_pieces_framework11.createPiece)({
  displayName: "PDF.co",
  description: "Automate PDF conversion, editing, extraction",
  categories: [import_pieces_framework12.PieceCategory.PRODUCTIVITY, import_pieces_framework12.PieceCategory.CONTENT_AND_FILES],
  logoUrl: "https://cdn.activepieces.com/pieces/pdf-co.png",
  auth: pdfCoAuth,
  authors: ["onyedikachi-david", "kishanprmr"],
  actions: [
    addBarcodeToPdf,
    addImageToPdf,
    addTextToPdf,
    convertHtmlToPdf,
    convertPdfToStructuredFormat,
    extractTablesFromPdf,
    extractTextFromPdf,
    searchAndReplaceText
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  pdfCo
});
