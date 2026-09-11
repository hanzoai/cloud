# egress

What leaves the machine.

`self/` measures whether you can have the software. This measures whether having
it means anyone else hears about it. The binary is started with no
configuration, every door is asked for an operation sixty times, and the whole
time two observers watch what it connects to.

```
bench/egress/run.sh
```

Taken on an M-series laptop, 2026-09-11.

| | measured |
|---|---|
| attempts to leave | **0** |
| peers off this machine | **0** |
| listeners seen | 3 |
| control: the catcher saw | 1 attempt from one caller |
| control: sampling saw | 1 peer |

## Two observers, because one of them can miss

**A listener standing in for the internet.** Go's `net/http` honours
`HTTP_PROXY` and `HTTPS_PROXY`, so a process pointed at `catcher.py` reaches
that socket instead of anywhere real. Every attempt is recorded with what it
asked for — `CONNECT host:443` for TLS, the absolute URL for plain HTTP, the raw
bytes for anything that is not HTTP. It answers nothing: a caller that phones
home is recorded whether or not the call would have worked. Timing cannot defeat
it, because the connection is either accepted or it never happened.

**Its sockets, sampled.** The catcher only sees a client that honours those
variables, so the process is also watched directly with `lsof`, ten times a
second. That observer **can** miss — a connection that opens and closes between
two samples is invisible.

And it does miss. The run reports `connections caught by sampling` and on this
machine it reads **0**, for traffic the run itself generated and knows was
there: sixty loopback calls, none of them alive long enough to land in a sample.
That row is in the output to keep the sampled zero from being read as proof. It
is corroboration. The catcher is the measurement.

## The controls

A zero from an observer that cannot see is not evidence, so the lane ends by
pointing each observer at something that does reach out, and **fails** if either
reports nothing. `listeners seen` is the third: three listening sockets are how
the run knows it was watching the server rather than an empty process id.

## What this does and does not say

**It says nothing left, under this configuration, while serving this.** Thirteen
operations over three doors, with no account, no key and no subsystem
configured. That is the shape most people run first and it is the shape a
competitor's hosted product cannot offer at all.

**It is not a claim about every configuration.** A deployment that enables a
subsystem with an upstream — a model provider, an object store, a mail relay —
connects to it, on purpose, and should. The claim here is narrow and checkable:
nothing in the default path phones anyone, including us.

**It is not an audit of the binary.** It observes behaviour for the length of a
run. A call made once a day, or on a trigger this run does not pull, is outside
what a twelve-second window can see. The source is the answer to that question,
and `self/` is the row that says you have it.
