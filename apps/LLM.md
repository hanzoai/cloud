# LLM.md — apps

Guidance for apps. Moved from the root LLM.md; cross-cutting rules stay in the root.

## The typed migration: THE PLAYBOOK (start here before typing anything)

Worked end to end on `apps/agents/targets.go` (5 ops). Follow it and a partition
is mechanical; skip it and you will rediscover fourteen failure modes the hard way.

### The recipe

1. **A receiver, not a closure.** `type xOps struct{ s *cloud.Service[state] }`
   and every op a METHOD (`o.registerTarget`). A TypedHandler takes no service
   parameter, and a method value is the only bound form `cmd/zipdoc` can lift
   prose from — a closure returned by a helper is a call expression with nothing
   to read.

2. **Declare on the GROUP the subsystem already has** —
   `g := app.Group("/v1/agents")`, then `zip.Post(g, "/targets", o.register)`.
   The op's path is the prefix composed with the leaf, which is the identity every
   projection keys on, and zipdoc (zip v1.18.3+) resolves the prefix the same way,
   so the doc comments reach the document and the tool list. If it cannot resolve
   the router — a group built somewhere it cannot see — it FAILS naming the call
   rather than filing the prose under a path that does not exist. (Before v1.18.3
   it filed group-declared ops under the bare leaf and the prose vanished from
   both surfaces, silently. If you are on an older zip, bump; do not work around
   it.)

3. **Install `cloud.Bridge()` on the subsystem's group, before the leaves.** A
   typed op receives only a context, so the validated org has to be parked there.
   fiber runs middleware in registration order — one installed after its leaves
   never runs. Untyped handlers read identity off the request and hide this.

4. **Identity is NEVER an In field.** `principal.OrgFrom(ctx)` for the tenant, and
   `principal.ValidatedFrom(ctx)` when the plane has no tenant at all and the gate
   is only "is this caller signed in" (a deployment-global read — `apps/engine`'s
   shared runtime, o11y's infra-health probe). `cloud.Bridge` parks BOTH in one
   expression, so they are always set together. An In field is caller-supplied, so
   a tenant key read from one is a cross-tenant read the caller asserted for
   itself. If you need more than those two (admin-ness lives in a header), that is
   `cloud.Request(ctx)` — and it is PINNED, so add your file to
   `allowedRequestUses` with a justification or the gate fails. Note which way
   they differ before choosing: `OrgFrom` composes validated-ness AND an org, so
   it refuses a signed-in caller whose token names no home org (a machine token) —
   right for a plane with rows to scope, wrong for one without.

5. **Preserve the wire, exactly.** Same JSON shapes, same statuses. `//go:generate
   go run github.com/zap-proto/zip/cmd/zipdoc` in the package, then
   `make -C apps/<app> describe`.

6. **Doc comments are product surface.** They ship to the OpenAPI `description`
   AND the MCP tool description — a model picks a tool by reading them. An
   `Example:` line becomes the request example. Write them true.
   The `summary` is the first sentence, and zip finds it by looking for a period
   followed by whitespace. It once looked for `". "` alone, so a first sentence
   ending at a LINE BREAK was not found and the summary fell back to the first
   line, cutting mid-sentence; zip v1.18.11's `sentenceEnd` accepts `\n`, `\t` and
   `\r` too, so a sentence may now wrap (verified: `GET /v1/plans/resolve/{id}`
   publishes a four-line first sentence whole). Still prefer a short one — the
   summary is what a tool list shows.

       python3 - <<'EOF'
       import json,glob
       for f in sorted(glob.glob('plugin/*/openapi.json')):
           d=json.load(open(f))
           for n,v in (d.get('components',{}).get('schemas',{}) or {}).items():
               for p,s in ((v.get('properties') or {}) if isinstance(v,dict) else {}).items():
                   if isinstance(s,dict) and s.get('$ref','').endswith('/'+p) and p[:1].isupper():
                       print(f,n,'->',p)
       EOF

### Statuses

`zip.WithStatus(201)` declares an UNCONDITIONAL success status and keys the
document's `responses`. Use it for every op that always answers 201/202.

**The conditional class — the worked example.** `registerTarget` answers 200 on
an idempotent re-link and 201 on first registration. That is correct REST and
`WithStatus` cannot express it (one declaration, one status). Those routes stay
**typed-but-shimmed**: keep `cloud.Created(ctx)` on the create branch only. Do
NOT bend the route to fit the declaration, and do NOT invent a third mechanism —
zip is getting multi-status `responses`, and these convert when it lands.

### The failure modes, all found the hard way

       grep -rE '\.(Get|Post|Put|Delete)\("' \
         "$(go env GOMODCACHE)/github.com/hanzoai/authz@v1.10.15/serve/"

3. **Verify what CI actually invokes before trusting a gate you add to a make
   target.** cloud's CI never ran `make test` — no `.github/workflows`, and
   `hanzo.yml` names steps directly. A gate added to `make test` protected
   nobody. `hanzo.yml` calls `check` now.

4. **`git status --porcelain`, not `git diff`.** A NEW app produces a NEW
   UNTRACKED subset, invisible to a diff — the failure that matters most is the
   one a diff cannot see.

5. **The schema namespace is FLAT, and typing is what makes you enter it.** A
   typed op's Go type name IS its schema name across the WHOLE fleet, and
   `openapi.Compose` refuses one name with two shapes ("every generated SDK would
   bind whichever it read last"). An UNTYPED route contributes no schema at all,
   so the collision does not exist until you type — and it surfaces at the weave,
   not at the compiler. `apps/ingress` named its list envelope `serviceList`, which
   is already `apps/admin`'s launch board, and the weave refused the whole package.
   The fix is to qualify the VALUE with the product the namespace cannot carry
   (`ingressServices`), not to rename admin's. It recurred immediately: guide's
   `Step` (a journey step) collided with marketing's `Step` (a step of a drip
   sequence), and guide — whose schema was not yet published — is the one that
   yielded, to `JourneyStep`. Check before you name, comparing SHAPES and not just
   names, because two apps may share a name when they agree:

       python3 - <<'EOF'
       import json,glob,os,collections
       s=collections.defaultdict(dict)
       for f in glob.glob('plugin/*/openapi.json'):
           d=json.load(open(f))
           for n,v in (d.get('components',{}) or {}).get('schemas',{}).items():
               s[n][os.path.basename(os.path.dirname(f))]=json.dumps(v,sort_keys=True)
       for n,a in sorted(s.items()):
           if len(a)>1 and len(set(a.values()))>1: print(n, sorted(a))
       EOF

   Domain nouns can stay unqualified while they are unique — the weave is the gate
   when they stop being. Generic ones are the standing hazard: `agents` publishes
   `Spec`/`Metrics`/`GPU`, `plugins` publishes `Status`/`Result`/`Host`/`Time`.

   **The check above cannot see the dangerous half, because a name that publishes
   NOTHING is not in it.** A typed op's In whose fields are all path params
   publishes no request body, so its Go type name never enters the namespace — and
   the collision sleeps until someone adds one body field to that op, at which
   point the weave refuses the whole package for a clash that reads as if it came
   from the OTHER app. Live instance, found at the esign pass and fixed before it
   could fire: `apps/company` publishes `esignCompleteIn` (`{signed: bool}`, for
   `POST /v1/company/esign/complete`) and `apps/esign` had declared a Go type of
   the same name and a wholly different shape. Compare DECLARED names against
   PUBLISHED ones, not published against published:

       python3 - <<'EOF'
       import json,glob,os,re,collections
       pub=collections.defaultdict(set)
       for f in glob.glob('plugin/*/openapi.json'):
           a=os.path.basename(os.path.dirname(f))
           for n in (json.load(open(f)).get('components',{}) or {}).get('schemas',{}): pub[n].add(a)
       for d in sorted(glob.glob('apps/*/')):
           a=d.split('/')[1]; names=set()
           for g in glob.glob(d+'*.go'):
               if not g.endswith('_test.go'): names|=set(re.findall(r'^type (\w+) ',open(g).read(),re.M))
           for n in sorted(names):
               if pub.get(n,set())-{a}: print(a,n,'also published by',sorted(pub[n]-{a}))
       EOF

   It reports the benign case too — three apps declare an empty `noInput` and the
   weave permits identical shapes — so read the SHAPES before renaming.

6. **A route going typed RENAMES its operationId**, `_by_id` → `_id`
   (`delete_v1_ingress_routes_by_id` → `delete_v1_ingress_routes_id`): the untyped
   projection derives the id from the route pattern, a typed op from zip's
   `defaultOpID`. It is not the wire — no status, body or field name moves — but
   it IS the generated SDK's method name, so an SDK regenerated after the migration
   renames those methods. `apps/agents/targets.go` already shows both forms side by
   side on one prefix (its typed `..._targets_id` next to its untyped
   `..._targets_by_id_claim`). Take the rename; do NOT pin it back with
   `WithOperationID`, which would make one app's ids a special case.

       python3 - <<'EOF'
       import json,glob
       for f in glob.glob('plugin/*/openapi.json'):
           d=json.load(open(f)); sc=d.get('components',{}).get('schemas',{})
           for p,ops in d['paths'].items():
               for m,op in ops.items():
                   if m.upper() not in ('POST','PUT','PATCH') or 'requestBody' not in op: continue
                   n=(op['requestBody'].get('content',{}).get('application/json',{})
                      .get('schema',{}) or {}).get('$ref','').split('/')[-1]
                   pr=set((sc.get(n,{}).get('properties') or {}).keys())
                   pa={q['name'] for q in op.get('parameters',[]) if q.get('in')=='path'}
                   if pr<=pa: print(f, m.upper(), p, n, sorted(pr), sorted(pa))
       EOF

8. **An `any`-valued map publishes a schema that is not thin but FALSE.** zip's
   `schemaOf` has no `reflect.Interface` case, so `map[string]any` falls to the
   default and projects `additionalProperties: {"type": "object"}` — an assertion
   that every VALUE is a JSON object. `apps/framework`'s own tests refute it on
   the way past: a document reads back `{"subject": "Ship framework",
   "docstatus": 0}`, a string and a number. `openapi.yaml` carries the claim in 15
   places. This is a WORSE failure than #78 and the bodyless POST above, and the
   difference is the one that matters: those UNDER-describe a true wire, this one
   describes a false one, so an SDK regenerated from the golden types a document
   `Dict[str, Dict]` — a shape that cannot hold one. hanzoai/openapi's authored
   master gets it right (`framework_Document`: `additionalProperties: true`), so
   the two documents genspec joins disagree about the same value today. The fix is
   one case in one function — an unconstrained element is an OPEN schema (`true`),
   not an object.

        python3 - <<'EOF'
        import json,glob,os,collections
        n=collections.Counter(); tot=0
        for f in sorted(glob.glob('plugin/*/openapi.json')):
            a=os.path.basename(os.path.dirname(f))
            for p,ops in json.load(open(f)).get('paths',{}).items():
                for m,op in ops.items():
                    s=isinstance(op,dict) and op.get('summary') or ''
                    if '\n' in s: n[a]+=1; tot+=1
        print(tot, n.most_common())
        EOF

    Unlike #7 and #78, the wire is not involved at all — this is prose quality
    on the surface SDK users and models read. **Fix it in `firstSentence`, with
    one whitespace collapse.** Do NOT reflow 215 doc comments so sentence one
    fits a 100-column line: that is the easy fix, it leaves the class alive for
    the next op anybody writes, and it makes cloud a special case of a general
    bug. (Playbook step 6's "keep sentence one on one line" is the WORKAROUND for
    this, not the rule — it was written when zip's fallback could cut a summary
    mid-sentence, which v1.18.6 no longer does.)

11. **A typed op on a fiber WILDCARD does not mis-name a parameter — it refuses
    the whole document.** Five refusals in this file say a bound In field and the
    published `{wildcardN}` parameter "cannot agree", which reads like a cosmetic
    loss. It is not. zip's own `Template` (v1.31.0 address.go:61) rewrites only
    `:name` segments, so the typed registry publishes the path VERBATIM — `/v1/dns/*`,
    `/v1/kms/secrets/+` — while cloud's router reading names the segment
    `{wildcard1}` (openapi/openapi.go:795-811). `Fold` then looks the op up by zip's
    spelling, finds no live route at that key, and returns "the registry and the
    router disagree about its path" (openapi/openapi.go:687), so `make -C apps/<x>
    describe` fails outright and the app publishes NOTHING. Two tests now RUN it
    rather than assert it (`TestTheWildcardCannotBeATypedOp` in apps/kms,
    `TestTheEndpointCannotBeATypedOp` in apps/dns): each registers a typed op on a
    wildcard and requires the refusal, so the day zip agrees the tests go red and
    say so. **The yielding side is zip's** — one function — because changing
    cloud's spelling would move every `{wildcardN}` path already published across
    five subsystems.

12. **Two of the three classes this file spends the most words on are CLOSED in
    zip v1.31.0, and the refusals written against v1.18.x must be re-read before
    they are believed.** `WithStatus` now takes a SET of codes and the ANSWER
    picks one (`StatusCoder`, typed.go:174-208), which retires the
    multi-status gap (#78) and the "typed-but-shimmed" `cloud.Created` workaround:
    `apps/index`'s health probe answers 200 or 503 over ONE shape by declaring
    both. `WithResponseHeader` + `HeaderCoder` (typed.go:230-260) put a response
    header in the document instead of a middleware side channel. A declared
    NON-2xx carrying the op's own typed body is explicitly supported too, which is
    most of the apps/ml and apps/tools "domain body on a refusal" class — those
    should be re-derived, not inherited. **v1.31.3 closes the third**: `HTTPError.Detail`
    (ctx.go:249) carries RFC 9457 extension members, MERGED under the envelope,
    so a refusal answers with a domain SHAPE instead of a sentence — which is the
    "non-2xx with a domain body" class apps/ml, apps/company, apps/provisioning
    and apps/tools were each refused for. The envelope is written LAST, so a
    domain key called `status` cannot displace the refusal's own; that ordering is
    the half worth a test rather than the feature. `apps/tools` is the worked
    example — see its row in tranche E. What did NOT move, and is what actually
    decides a relay: a typed op's only response path is `c.JSON(out)`
    (typed.go:558) and fiber writes `application/json; charset=utf-8` over
    anything a header coder set (fiber v3 res.go:501-514).

13. **A typed op declared on the GROUP and one declared on the APP with an
    absolute path are the SAME ROUTE — and only the first can carry prose.**
    `registerTyped` composes `scope.Prefix` with the leaf and registers on the
    concrete `*zip.App` either way (`scope.OpScope`, scope.go:298), so
    `zip.Get(g, "/health")` and `zip.Get(app, "/v1/index/health")` produce one
    identical route on one identical router. The absolute form is what an UNTYPED
    handler needs — that one lands on the group NODE — and it is the only form
    `cmd/zipdoc` cannot resolve: it fails naming the call rather than filing prose
    under a path that does not exist. So the "register health absolutely, the
    group does not survive mount composition" idiom (apps/index, clients/esign,
    clients/kms) applies to raw handlers and must be DROPPED when the route is
    typed, or the op ships with no description at all.

14. **The fleet has THREE copies of one client: an error that carries its own
    status and bytes, written back by a middleware installed ahead of the
    leaves.** `cloud.deniedErr` + `DenyEnvelope` (the money wire's 402/503),
    `goja.BundleErr` + `Envelope` (a JS bundle's own envelope) and now
    `index.fault` + `envelope` (the Meilisearch dialect's `{message, code, type,
    link}`, which a client BRANCHES on). All three have the same shape down to the
    `Unwrap` that keeps the refusal honest off the HTTP path. It is the general
    answer to "a typed op's only refusal is a returned error, which zip renders
    flat", so the fourth instance should not be written: ONE type in package
    cloud, carrying (status, bytes, sentence), with the three current owners
    supplying their own constructor. Not done here because retiring the money and
    bundle copies touches captable, dataroom, pricing and ml, which is a different
    commit.
    **The general answer arrived instead, one layer down**: `zip.HTTPError.Detail`
    (v1.31.3) lets a RETURNED error carry its own members, so the client these three
    hand-rolled — status, bytes and a sentence, written back by a middleware ahead
    of the leaves — is what the framework does. A fourth instance is now a refusal
    to reach for the framework rather than a missing type in package cloud, and the
    three that exist are convertible one at a time: each keeps its vocabulary as a
    Detail map and drops its envelope middleware.
    **AND THE MONEY ONE WAS ALREADY DONE, which is the finding worth more than the
    capability.** `cloud.Denied` + `DenyEnvelope` (resource_billing.go) let a typed
    op refuse in the fleet's own `{"error":{"code","message"}}` contract, and
    `serve.go:327` installs the envelope APP-WIDE — so every binary already had it.
    Four packages nonetheless carried a refusal saying a typed op's error "can only
    leave through zip's errorHandler as a flat {status,code,error}", each written
    before that client existed and none re-read after: `apps/company` (`/payment`),
    `apps/ml` (the model create), `apps/provisioning` (all seven creates) and
    `apps/tools`. Two are closed here and the wire is byte-preserved — company's
    `TestPaymentDenialWire` and ml's `TestComputeCreate_RefusesUnfundedOrg` both
    assert the NESTED body and both pass through the typed op unchanged. **Read a
    refusal against the code before believing it**, and note the shape of how this
    one rotted: the fix landed somewhere else and nothing carried it back, so four
    ledgers went on citing a limitation the fleet had already removed. A package's
    own harness must install what `Serve` installs (`cloud.Bridge`, and now
    `cloud.DenyEnvelope`) or a converted op refuses in zip's shape in tests only.

11. **Two `Group(prefix)` calls are two APPS, so middleware installed on the first
    never reaches a leaf registered on the second.** This is failure mode #3's
    sibling and it fails the same silent way — not "installed after its leaves"
    but "installed on a different group". zip's `Group` returns a NEW App included
    at that prefix (`compose.go:325`: "A group is not a third kind of thing. It is
    an App with a prefix"), and `Use` is scoped to the App it was called on. The
    doc says so deliberately: the environment is "a function of WHERE a thing is
    written rather than WHEN", which DIVERGES from fiber, where one flat list and
    registration order decide it. So the fiber intuition — install it early, it
    covers everything at that prefix afterwards — is wrong here.
    It has already cost the repo once: `apps/books` found "every route in this file
    answered money WITHOUT `Cache-Control: no-store` while the ops in books.go
    carried it" (scan.go:50-60), and fixed it by installing `noStore` on each
    router. The esign pass hit it again for the same reason books did — cmd/zipdoc
    can only resolve a prefix it can READ in the file, so a registrar that takes a
    group as a PARAMETER fails generation, which pushes each file to build its own
    group and each group to need its own middleware. There `goja.Envelope()` sat on
    the group carrying `/health` while the twelve real ops were on a second one, so
    every bundle refusal came back in zip's flat `{status,code,error}` instead of
    the bundle's own `{"error":…}` — a route that worked perfectly and answered in
    the wrong vocabulary. The fix is ONE group per subsystem, in the file that
    declares the ops, with every `Use` on it; where a surface must register in two
    stages (esign serves `/health` even when its document plane never opened), pass
    the STATE and branch inside the one registrar rather than splitting the group.
    Find the candidates, then read each one — several are deliberate, using
    `.Group("/v1")` purely as an ADDRESS with `With()` carrying the chain onto the
    leaf (apps/company, apps/validators, apps/sync), which installs nothing at the
    shared prefix and is legal precisely because it does not:

        python3 - <<'EOF'
        import re,glob,collections
        h=collections.defaultdict(lambda: collections.defaultdict(list))
        for f in glob.glob('apps/**/*.go',recursive=True):
            if f.endswith('_test.go'): continue
            for i,l in enumerate(open(f,errors='ignore'),1):
                m=re.search(r'\.Group\("([^"]+)"',l)
                if m: h[f.split('/')[1]][m.group(1)].append(f+':'+str(i))
        for a,pref in sorted(h.items()):
            for p,at in sorted(pref.items()):
                if len(at)>1: print(a,p,at)
        EOF
