// Cross-language ZAP wire check, driven by the REAL @zap-proto runtime console
// ships. Two modes:
//
//   node xcheck.mjs req  <method> <superjsonPayload> <promiseID>
//     -> prints the hex of the exact bytes console's @zap-proto/web client puts
//        on the WebSocket for one call (rpc envelope wrapping the inner
//        {method,payload} ZapRequest). Go's rpc.ParseRequest + parseZapRequest
//        must decode these.
//
//   node xcheck.mjs reply <hexFrame>
//     -> decodes a reply frame Go produced (rpc.BuildResponse wrapping the inner
//        ZapReply) using the real parseResponse + the ZapReply struct offsets,
//        and prints JSON {promiseID, ok, status, result, errorJson}.
//
// Uses console's installed @zap-proto/zap so the bytes are byte-identical to
// what the live browser client emits.
import {
  Builder,
  Message,
  StructView,
  buildRequest,
  parseResponse,
} from '/Users/a/work/hanzo/console/node_modules/@zap-proto/zap/dist/index.js'

const METHOD_RPC = 1

// Inner ZapRequest { method @0 :Text, payload @8 :Text } size 16 — mirrors
// console/src/lib/zap/transport.ts newZapRequest.
function newZapRequest(method, payload) {
  const b = new Builder(256)
  const ob = b.startObject(16)
  ob.setText(0, method)
  ob.setText(8, payload)
  ob.finishAsRoot()
  return b.finish()
}

// Inner ZapReply { ok @0, status @4, result @8, errorJson @16 } size 24.
class ZapReply extends StructView {
  static wrap(bytes) {
    const root = Message.parse(bytes).root()
    return new ZapReply(root.data, root.offset)
  }
  get ok() { return this.bool(0) }
  get status() { return this.u32(4) }
  get result() { return this.text(8) }
  get errorJson() { return this.text(16) }
}

const hex = (u8) => Buffer.from(u8).toString('hex')
const unhex = (s) => new Uint8Array(Buffer.from(s, 'hex'))

const [mode, ...args] = process.argv.slice(2)

if (mode === 'req') {
  const [method, payload, promiseID] = args
  const inner = newZapRequest(method, payload ?? '')
  // This is exactly conn.bootstrap.call(METHOD_RPC, { payload: inner }):
  const frame = buildRequest({
    method: METHOD_RPC,
    promiseID: Number(promiseID ?? 1),
    target: 0,
    cap: new Uint8Array(0),
    payload: inner,
  })
  process.stdout.write(hex(frame))
} else if (mode === 'reply') {
  const [hexFrame] = args
  const resp = parseResponse(unhex(hexFrame))
  const reply = ZapReply.wrap(resp.body)
  process.stdout.write(
    JSON.stringify({
      promiseID: resp.promiseID,
      status: resp.status,
      ok: reply.ok,
      replyStatus: reply.status,
      result: reply.result,
      errorJson: reply.errorJson,
    }),
  )
} else {
  process.stderr.write('usage: xcheck.mjs req <method> <payload> <promiseID> | reply <hexFrame>\n')
  process.exit(2)
}
