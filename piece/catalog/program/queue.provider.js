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

// ../ap/packages/pieces/community/queue/src/index.ts
var index_exports = {};
__export(index_exports, {
  queue: () => queue
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/queue/src/lib/actions/push-to-queue.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/queue/src/lib/common.ts
function constructQueueName(queueName, testing) {
  return `_queue_rmAPlFmX0s_${testing ? "testing_" : ""}${queueName}`;
}
function formatStorageError(e) {
  let size = e.maxStorageSizeInBytes;
  return new Error(JSON.stringify({
    message: `Queue write operation failed. The total size of the queue has exceeded the maximum limit of ${Math.floor(size / 1024)} kb.`,
    details: {
      maxSizeInKb: Math.floor(size / 1024),
      suggestion: "Consider consume faster from the queue or reduce the size of the items being added to the queue."
    }
  }));
}

// ../ap/packages/pieces/community/queue/src/lib/actions/push-to-queue.ts
var notes = `**Note:**
- You can push items from other flows. The queue name should be unique across all flows.
- The testing step work in isolation and doesn't affect the actual queue after publishing.
`, pushToQueue = (0, import_pieces_framework.createAction)({
  audience: "both",
  name: "push-to-queue",
  classification: "WRITE",
  description: "Push item to queue",
  aiMetadata: { description: "Appends one or more items to the end of a named project-scoped FIFO queue, creating the queue on first use; the queue name is a plain string shared across the whole project, so any flow using the same name writes to the same queue. Use it to buffer or throttle work for later consumption, then read it back with Pull items from queue, or discard everything with Clear queue. Not idempotent: each call appends the items again, and the write fails once the accumulated queue exceeds the project store size limit.", idempotent: !1 },
  displayName: "Push to Queue",
  props: {
    info: import_pieces_framework.Property.MarkDown({
      value: notes
    }),
    queueName: import_pieces_framework.Property.ShortText({
      displayName: "Queue Name",
      required: !0
    }),
    items: import_pieces_framework.Property.Array({
      displayName: "Items",
      required: !0
    })
  },
  async run(context) {
    return push({ store: context.store, queueName: context.propsValue.queueName, items: context.propsValue.items, testing: !1 });
  },
  async test(context) {
    return push({ store: context.store, queueName: context.propsValue.queueName, items: context.propsValue.items, testing: !0 });
  }
});
async function push({ store, queueName, items, testing }) {
  let key = constructQueueName(queueName, testing), updatedQueueItems = [...await store.get(key, import_pieces_framework.StoreScope.PROJECT) || [], ...items];
  try {
    return await store.put(key, updatedQueueItems, import_pieces_framework.StoreScope.PROJECT);
  } catch (e) {
    throw (e == null ? void 0 : e.name) === "StorageLimitError" ? formatStorageError(e) : e;
  }
}

// ../ap/packages/pieces/community/queue/src/lib/actions/pull-from-queue.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var notes2 = `**Note:**
- You can pull items from other flows. The queue name should be unique across all flows.
- The testing step work in isolation and doesn't affect the actual queue after publishing.
`, pullFromQueue = (0, import_pieces_framework2.createAction)({
  audience: "both",
  name: "pull-from-queue",
  classification: "DESTRUCTIVE",
  description: "Pull items from queue",
  aiMetadata: { description: "Removes and returns the first N items of a named project-scoped FIFO queue and writes the remainder back, so it is a destructive consume rather than a peek - the returned items are gone from the queue, and fewer than requested come back when the queue holds less. Use it to drain work buffered by Push to Queue; prefer Clear queue when the aim is to empty the queue without reading the items. Not idempotent: every call consumes a further batch.", idempotent: !1 },
  displayName: "Pull items from queue",
  props: {
    info: import_pieces_framework2.Property.MarkDown({
      value: notes2
    }),
    queueName: import_pieces_framework2.Property.ShortText({
      displayName: "Queue Name",
      required: !0
    }),
    numOfItems: import_pieces_framework2.Property.Number({
      displayName: "Number of items",
      required: !0
    })
  },
  async run(context) {
    return await poll({ store: context.store, queueName: context.propsValue.queueName, numOfItems: context.propsValue.numOfItems, testing: !1 });
  },
  async test(context) {
    return await poll({ store: context.store, queueName: context.propsValue.queueName, numOfItems: context.propsValue.numOfItems, testing: !0 });
  }
});
async function poll({ store, queueName, numOfItems, testing }) {
  let key = constructQueueName(queueName, testing), allItems = await store.get(key, import_pieces_framework2.StoreScope.PROJECT) || [], neededItems = allItems.splice(0, numOfItems);
  try {
    await store.put(key, allItems, import_pieces_framework2.StoreScope.PROJECT);
  } catch (e) {
    throw (e == null ? void 0 : e.name) === "StorageLimitError" ? formatStorageError(e) : e;
  }
  return neededItems;
}

// ../ap/packages/pieces/community/queue/src/lib/actions/clear-queue.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var notes3 = `**Note:**
- This deletes all items inside the queue permanently.
- The testing step work in isolation and doesn't affect the actual queue after publishing.
`, clearQueue = (0, import_pieces_framework3.createAction)({
  audience: "both",
  name: "clear-queue",
  classification: "DESTRUCTIVE",
  description: "Clears all items inside a queue",
  aiMetadata: { description: "Permanently deletes a named project-scoped queue and every item stored in it, keyed by the exact queue name. Use it to reset a queue wholesale; prefer Pull items from queue when the buffered items still need to be read and processed. Idempotent: the queue ends up empty however many times it runs, and clearing an already-empty or never-created queue still succeeds.", idempotent: !0 },
  displayName: "Clear queue",
  props: {
    info: import_pieces_framework3.Property.MarkDown({
      value: notes3
    }),
    queueName: import_pieces_framework3.Property.ShortText({
      displayName: "Queue Name",
      required: !0
    })
  },
  async run(context) {
    let queueName = constructQueueName(context.propsValue.queueName, !1);
    return await context.store.delete(queueName, import_pieces_framework3.StoreScope.PROJECT), {
      success: !0
    };
  },
  async test(context) {
    let queueName = constructQueueName(context.propsValue.queueName, !0);
    return await context.store.delete(queueName, import_pieces_framework3.StoreScope.PROJECT), {
      success: !0
    };
  }
});

// ../ap/packages/pieces/community/queue/src/index.ts
var queue = (0, import_pieces_framework4.createPiece)({
  displayName: "Queue",
  description: "A piece that allows you to push items into a queue, providing a way to throttle requests or process data in a First-In-First-Out (FIFO) manner.",
  auth: import_pieces_framework4.PieceAuth.None(),
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/new-core/queue.svg",
  authors: ["AbdullahBitar"],
  actions: [pushToQueue, pullFromQueue, clearQueue],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  queue
});
