# doors

What an agent pays to call an operation, per door.

Every other lane measures work the machine does. This measures the tax on asking
it to. An agent's loop is call, read, decide, call again, so this number is
multiplied by every step of every task.

```
bench/doors/run.sh
```

The doors are not variants of one wire. Over REST the operation **is** the
address. Over MCP the address is one agent endpoint and the operation is a name
in the body. Over the op-call plane the name is in the path and the body is
binary. Same handler, same process, three envelopes — so the difference between
these rows is the envelope and nothing else.

Taken on an M-series laptop, 2026-09-11, `n=200` interleaved.

| door | min | p50 |
|---|---|---|
| REST | 0.27 ms | 0.51 ms |
| op-call plane | 0.28 ms | 0.53 ms |
| MCP | 0.30 ms | 0.56 ms |

**The envelope costs about 30 microseconds.** REST is fastest because the
operation is already the address; MCP pays for a name lookup and a JSON-RPC
frame. On a loop of a hundred tool calls that is three milliseconds, against a
model turn measured in seconds.

## How to read this, and how not to

**Interleaved, not one door then the next.** Measured in blocks, the three rows
disagreed about which door was fastest on every run: drift between phases on a
busy machine is larger than the difference being measured. Round-robin puts
every door in the same conditions each iteration. If you change that, the
comparison stops meaning anything.

**Trust `min`, not the tail.** The p90 on this laptop ranged from 0.94 ms to
10.87 ms across runs while `min` moved by 0.03 ms. The tail is measuring what
else the machine was doing. `min` is the closest thing here to the cost of the
envelope itself, and it is the only column whose ordering was stable across
every run.

**This is not a round-trip to a cloud.** Loopback, one process, no network, no
model, no authentication. It is the floor: what the server adds to a call before
anything real happens. Add your own network to it — and note that the same floor
is what an agent running beside the server on a laptop actually pays.

**Sub-millisecond is the point, not the ranking.** No door here is a slow path.
A design where the agent-facing door cost materially more than the REST one
would be a design that quietly penalises agents, and three rows within 30
microseconds is what says it does not.
