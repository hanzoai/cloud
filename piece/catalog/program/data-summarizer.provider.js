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

// ../ap/packages/pieces/core/data-summarizer/src/index.ts
var index_exports = {};
__export(index_exports, {
  dataSummarizer: () => dataSummarizer
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/core/data-summarizer/src/lib/actions/calculate-average.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/core/data-summarizer/src/lib/common.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_framework2 = require("@activepieces/pieces-framework"), common = {
  note: import_pieces_framework.Property.MarkDown({
    value: "If you'd like to use the values with a previous step, click the (X) first, and then select the step you want to use."
  }),
  validateArray: function(values) {
    let newValues = values.map((value, index) => checkValueIsNumber(value, index));
    return newValues.every((value) => value.error === null) ? {
      hasError: !1,
      values: newValues.map((value) => value.value)
    } : {
      hasError: !0,
      error: {
        message: "The following values are not numbers",
        errors: newValues.filter((value) => !(0, import_pieces_framework2.isNil)(value)).map((value) => value.error)
      }
    };
  }
}, checkValueIsNumber = function(value, location) {
  let parsedValue = Number(value);
  return Number.isNaN(parsedValue) ? {
    error: {
      value,
      location
    },
    value: -1
  } : {
    error: null,
    value: Number(value)
  };
};

// ../ap/packages/pieces/core/data-summarizer/src/lib/actions/calculate-average.ts
var calculateAverage = (0, import_pieces_framework3.createAction)({
  audience: "both",
  name: "calculateAverage",
  displayName: "Calculate Average",
  description: "Calculates the average of a list of values.",
  aiMetadata: { description: "Computes the arithmetic mean of an array of numeric values. Use when you need the average of a collected list; prefer Calculate Sum for a total, Find Min and Max for the extremes, or Count Uniques for distinct-value counts. Every element must be coercible to a number (numeric strings are accepted) or the action throws listing the offending values and their positions, and an empty list yields NaN; read-only and idempotent.", idempotent: !0 },
  props: {
    note: common.note,
    values: import_pieces_framework3.Property.Array({
      displayName: "Values",
      required: !0
    })
  },
  async run({ propsValue }) {
    let result = common.validateArray(propsValue.values);
    if (result.hasError)
      throw new Error(JSON.stringify(result.error));
    return {
      average: result.values.reduce((acc, value) => acc + value, 0) / result.values.length
    };
  }
});

// ../ap/packages/pieces/core/data-summarizer/src/lib/actions/calculate-sum.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var calculateSum = (0, import_pieces_framework4.createAction)({
  audience: "both",
  name: "calculateSum",
  displayName: "Calculate Sum",
  description: "Calculates the sum of a list of values.",
  aiMetadata: { description: "Adds up an array of numeric values and returns the total. Use when you need a running total or aggregate of a collected list; prefer Calculate Average for the mean, Find Min and Max for the extremes, or Count Uniques for distinct-value counts. Every element must be coercible to a number (numeric strings are accepted) or the action throws listing the offending values and their positions; read-only and idempotent.", idempotent: !0 },
  props: {
    note: common.note,
    values: import_pieces_framework4.Property.Array({
      displayName: "Values",
      required: !0
    })
  },
  async run({ propsValue }) {
    let result = common.validateArray(propsValue.values);
    if (result.hasError)
      throw new Error(JSON.stringify(result.error));
    return {
      sum: result.values.reduce((acc, value) => acc + value, 0)
    };
  }
});

// ../ap/packages/pieces/core/data-summarizer/src/lib/actions/count-uniques.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_framework6 = require("@activepieces/pieces-framework"), countUniques = (0, import_pieces_framework5.createAction)({
  audience: "both",
  name: "countUniques",
  displayName: "Count Uniques",
  description: "Counts the number of unique values for multiple fields",
  aiMetadata: { description: "Counts how many distinct entries an array contains, comparing whole entries when Fields is left empty or only the chosen object keys when Fields is supplied. Use for cardinality or de-duplication checks; prefer Calculate Sum, Calculate Average, or Find Min and Max for numeric aggregation. Values need not be numeric, but in field mode any entry that is not an object, or an object holding none of the chosen keys, collapses into one shared bucket; read-only and idempotent.", idempotent: !0 },
  props: {
    note: common.note,
    values: import_pieces_framework5.Property.Array({
      displayName: "Values",
      required: !0
    }),
    fieldsExplanation: import_pieces_framework5.Property.MarkDown({
      value: "If the data you're passing in is an object, you can specify certain fields to filter on. The object will be discarded if the fields don't exist. Otherwise, leave fields empty."
    }),
    fields: import_pieces_framework5.Property.Array({
      displayName: "Fields",
      required: !1
    })
  },
  async run({ propsValue }) {
    let values = propsValue.values, unknownFields = propsValue.fields != null && propsValue.fields.length > 0 ? propsValue.fields : null, fields = validateFields(unknownFields);
    return {
      numUniques: numUniques(values, fields)
    };
  }
});
function validateFields(fields) {
  return !(0, import_pieces_framework6.isNil)(fields) && Array.isArray(fields) && fields.every((value) => typeof value == "string") ? fields : null;
}
function numUniques(values, fields = null) {
  if ((0, import_pieces_framework6.isNil)(fields))
    return new Set(values.map((value) => JSON.stringify(value))).size;
  let newValues = values.map((value) => {
    let obj = {};
    if (typeof value != "object")
      return obj;
    for (let key in value)
      fields.includes(key) && (obj[key] = value[key]);
    return obj;
  });
  return new Set(newValues.map((value) => JSON.stringify(value))).size;
}

// ../ap/packages/pieces/core/data-summarizer/src/lib/actions/get-min-max.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var getMinMax = (0, import_pieces_framework7.createAction)({
  audience: "both",
  name: "getMinMax",
  displayName: "Find Min and Max",
  description: "Get the smallest and greatest values from a list of numeric values.",
  aiMetadata: { description: "Returns both the smallest and the largest number from an array of numeric values in a single call. Use when you need the range or the extremes of a collected list; prefer Calculate Sum for a total, Calculate Average for the mean, or Count Uniques for distinct-value counts. Every element must be coercible to a number (numeric strings are accepted) or the action throws listing the offending values and their positions, and an empty list returns max -Infinity and min Infinity; read-only and idempotent.", idempotent: !0 },
  props: {
    note: common.note,
    values: import_pieces_framework7.Property.Array({
      displayName: "Values",
      required: !0
    })
  },
  async run({ propsValue }) {
    let result = common.validateArray(propsValue.values);
    if (result.hasError)
      throw new Error(JSON.stringify(result.error));
    return {
      max: Math.max(...result.values),
      min: Math.min(...result.values)
    };
  }
});

// ../ap/packages/pieces/core/data-summarizer/src/index.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), dataSummarizer = (0, import_pieces_framework8.createPiece)({
  displayName: "Data Summarizer",
  auth: import_pieces_framework8.PieceAuth.None(),
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/data-summarizer.svg",
  authors: ["tahboubali"],
  actions: [calculateAverage, calculateSum, countUniques, getMinMax],
  triggers: [],
  categories: [import_pieces_framework9.PieceCategory.CORE]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  dataSummarizer
});
