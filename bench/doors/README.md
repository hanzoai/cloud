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
binary. Over ZAP there is no HTTP at all — the op is called on a connection that
speaks the protocol this server is built on. Same handler, same process, four
envelopes, so the difference between these rows is the envelope and nothing
else.

Apple M1 Max, 10 cores, `n=200` interleaved, four samples. The `min` and `p50`
of each:

| door | min | p50 |
|---|---|---|
| **ZAP** | **0.13 · 0.10 · 0.12 · 0.09** | **0.31 · 0.22 · 0.24 · 0.19** |
| REST | 0.14 · 0.11 · 0.12 · 0.11 | 0.33 · 0.27 · 0.27 · 0.21 |
| MCP | 0.15 · 0.13 · 0.13 · 0.11 | 0.35 · 0.28 · 0.28 · 0.22 |
| op-call plane | 0.14 · 0.12 · 0.13 · 0.11 | 0.36 · 0.28 · 0.28 · 0.23 |

**ZAP is the fastest door on every sample, and it is the only one that is not
HTTP.** It leads on `min` in all four and on `p50` in all four, by more than the
three HTTP doors differ from each other. Among those three the ordering is REST,
then MCP and the plane within noise of one another: REST is first because the
operation is already the address, and MCP pays for a name lookup and a JSON-RPC
frame.

All four are under a quarter of a millisecond at the median. On a loop of a
hundred tool calls the spread between the best and worst is three hundredths of
a second, against a model turn measured in seconds.

## The client was in the old numbers

An earlier version of this table was measured with Python's `urllib` and read
0.27–0.39 ms at `min` where the same doors now read 0.09–0.15. That difference
is the client, not the server, and it was silently in every row.

It had to go for the fourth door anyway: ZAP is not HTTP, so a script cannot
speak it, and timing three doors from Python and one from Go would have put the
client's language into the comparison rather than the envelope. The harness is
one Go program calling all four round-robin, sharing one `http.Client` across the
three HTTP doors so connection reuse is not a difference between them either.

## How to read this, and how not to

**Interleaved, not one door then the next.** Measured in blocks, the three rows
disagreed about which door was fastest on every run: drift between phases on a
busy machine is larger than the difference being measured. Round-robin puts
every door in the same conditions each iteration. If you change that, the
comparison stops meaning anything.

**Trust `min` first.** The p90 has ranged from 0.35 ms to 10.87 ms across runs on
this laptop while `min` moved by hundredths — the tail measures what else the
machine was doing. Under the Python client the p50's ordering changed run to
run too; under one Go client it has not, which is itself evidence that some of
that instability was the client.

**This is not a round-trip to a cloud.** Loopback, one process, no network, no
model, no authentication. It is the floor: what the server adds to a call before
anything real happens. Add your own network to it — and note that the same floor
is what an agent running beside the server on a laptop actually pays.

**Sub-millisecond is the point, not the ranking.** No door here is a slow path.
A design where the agent-facing door cost materially more than the REST one
would be a design that quietly penalises agents, and three rows within 30
microseconds is what says it does not.
