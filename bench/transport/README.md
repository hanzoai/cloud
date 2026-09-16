# transport

What an agent pays to call an operation, per transport.

Every other lane measures work the machine does. This measures the tax on asking
it to. An agent's loop is call, read, decide, call again, so this number is
multiplied by every step of every task.

```
bench/transport/run.sh
```

The transports are not variants of one wire. Over REST the operation **is** the
address. Over MCP the address is one agent endpoint and the operation is a name
in the body. Over the op-call plane the name is in the path and the body is
binary. Over ZAP there is no HTTP at all — the op is called on a connection that
speaks the protocol this server is built on. Same handler, same process, four
envelopes, so the difference between these rows is the envelope and nothing
else.

Apple M1 Max, 10 cores, `n=200` interleaved, three samples of each phase.

| transport | min | p50 | p50 against REST, same phase |
|---|---|---|---|
| **ZAP unix** | **0.07–0.09** | **0.14–0.18** | **0.67 · 0.75 · 0.67** |
| ZAP tcp | 0.08–0.12 | 0.19–0.24 | 0.86 · 0.87 · 0.90 |
| REST | 0.10–0.13 | 0.21–0.28 | 1.00 |
| MCP | 0.10–0.14 | 0.21–0.25 | 0.96 · 1.09 · 1.05 |
| op-call plane | 0.09–0.12 | 0.19–0.23 | 1.00 · 1.00 · 1.00 |

**ZAP is the fastest transport, and the socket is faster than the port.** Over a unix
socket it answers in about two thirds of REST's median; over loopback TCP, about
nine tenths. Both lead every HTTP transport in every sample. Among the three HTTP
transports the differences are inside the noise: REST is nominally first because the
operation is already the address, and MCP nominally last for a name lookup and a
JSON-RPC frame, but they trade places between runs.

**Read the last column, not the absolutes.** A process serves one ZAP address,
so the socket is a second phase rather than a fifth row, and the machine moves
between phases — REST's own median ranged 0.21 to 0.28 ms across these runs. The
three HTTP transports are measured again in the second phase precisely to carry that
drift, and the ratio to REST is what survives it. On the ratio the ordering is
the same in all three samples.

All of it is under a third of a millisecond at the median. On a loop of a
hundred tool calls the spread between the best and worst transport is about a
hundredth of a second, against a model turn measured in seconds — so the
practical reading is still to pick the transport that fits the caller. What the ZAP
rows buy is the one in-process-adjacent path, and the socket is where that shows.

## The client was in the old numbers

An earlier version of this table was measured with Python's `urllib` and read
0.27–0.39 ms at `min` where the same transports now read 0.09–0.15. That difference
is the client, not the server, and it was silently in every row.

It had to go for the fourth transport anyway: ZAP is not HTTP, so a script cannot
speak it, and timing three transports from Python and one from Go would have put the
client's language into the comparison rather than the envelope. The harness is
one Go program calling all four round-robin, sharing one `http.Client` across the
three HTTP transports so connection reuse is not a difference between them either.

## How to read this, and how not to

**Interleaved, not one transport then the next.** Measured in blocks, the three rows
disagreed about which transport was fastest on every run: drift between phases on a
busy machine is larger than the difference being measured. Round-robin puts
every transport in the same conditions each iteration. If you change that, the
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

**Sub-millisecond is the point, not the ranking.** No transport here is a slow path.
A design where the agent-facing transport cost materially more than the REST one
would be a design that quietly penalises agents, and three rows within 30
microseconds is what says it does not.
