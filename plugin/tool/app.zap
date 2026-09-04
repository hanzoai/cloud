# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package tool

struct MCPListing {
    ID          text        @0
    Name        text        @8
    Vendor      text        @16
    Title       text        @24
    Description text        @32
    Repo        text        @40
    Site        text        @48
    Version     text        @56
    Transports  list<text>  @64
    Packages    list<bytes> @72
    Remotes     list<bytes> @80
    Registry    text        @88
    Synced      i64         @96
    Hidden      bool        @104
    Featured    bool        @105
    Official    bool        @106
    Logo        text        @112
}

struct MCPServer {
    ID         text @0
    Org        text @8
    Name       text @16
    URL        text @24
    AuthHeader text @32
    HasSecret  bool @40
    Listing    text @48
    Source     text @56
    CreatedAt  i64  @64
}

struct activationReq {
    Activate   list<text> @0
    Deactivate list<text> @8
}

struct activationSet {
    Enabled list<text> @0
}

struct authoredPluginList {
    Plugins list<bytes> @0
}

struct authoredSkillList {
    Skills list<bytes> @0
}

struct buildOut {
    Bytes     i64   @0
    Generated bool  @8
    Plugin    bytes @16
}

struct buildRequest {
    Name     text @0
    Provider text @8
    Source   text @16
    Spec     text @24
}

struct catalogQuery {
    Q        text @0
    Featured text @8
    Official text @16
    Limit    i64  @24
    Offset   i64  @32
}

struct createServerReq {
    Name       text @0
    URL        text @8
    Listing    text @16
    AuthHeader text @24
    Secret     text @32
}

struct curateReq {
    ID       text @0
    Hidden   bool @8
    Featured bool @9
    Official bool @10
    Logo     text @16
}

struct listingRef {
    ID text @0
}

struct mcpCatalog {
    Catalog list<bytes> @0
    Total   i64         @8
    Limit   i64         @16
    Offset  i64         @24
}

struct mcpCatalogSync {
    Added    i64  @0
    Updated  i64  @8
    Total    i64  @16
    Registry text @24
}

struct mcpServerList {
    Servers list<bytes> @0
}

struct pluginDeleted {
    Deleted text @0
}

struct pluginMountList {
    Plugins list<bytes> @0
}

struct pluginQuery {
    All text @0
}

struct pluginRef {
    ID text @0
}

struct serverRef {
    ID text @0
}

struct skillDeleted {
    Deleted text @0
}

struct skillIn {
    Name        text @0
    Description text @8
    Content     text @16
}

struct skillRef {
    ID text @0
}

struct skillWritten {
    Skill bytes @0
}

struct sourceQuery {
    Activated text @0
}

struct sourceToolList {
    Source text        @0
    Tools  list<bytes> @8
}

struct toolList {
    Tools list<bytes> @0
}

struct toolQuery {
    Source    text @0
    Activated text @8
}

interface tool {
    # Deregisters one of the caller org's external MCP servers, so its
    # tools leave the registry. Scoped to the caller's org, so an id belonging to
    # another tenant is a 404 and not a delete. Answers 204 with no body; a server
    # this org does not have is 404.
    delete_tool_mcp_servers_by_id(req: serverRef)
    # Removes one of the caller org's built plugins, so the
    # runtime can no longer load it. Scoped to the caller's org, so an id belonging
    # to another tenant answers 404 and is not deleted.
    delete_tool_plugins_authored_by_id(req: pluginRef) returns (rep: pluginDeleted)
    # Removes one of the caller org's authored skills. Scoped to the
    # caller's org, so an id belonging to another tenant is never reached. Removing
    # what is not there is not an error — the caller's intent is "gone", and it is.
    delete_tool_skills_by_id(req: skillRef) returns (rep: skillDeleted)
    # Lists every tool the caller's org and project can reach, from every
    # source, each flagged with whether it is activated. This is the discovery
    # surface: one flat set of names spanning connector actions, user functions,
    # zap-service routes, agents, skills and the org's own external MCP servers,
    # deduplicated by name so the highest-precedence source wins a collision. It
    # lists; it does not call — dispatch is POST /v1/tool/call.
    get_tool(req: toolQuery) returns (rep: toolList)
    # Reports which tools are switched on for the caller's org and
    # project. Activation is what makes a tool dispatchable and what makes it visible
    # to an agent, so this is the set the MCP tool list is drawn from — every other
    # tool in the registry is discoverable but refused at call time.
    get_tool_activation() returns (rep: activationSet)
    # Lists the MCP servers the public registries publish, as we hold
    # them: our canonical copy of registry.modelcontextprotocol.io, plus what we
    # decided about each entry.
    # This is the SHELF an org picks from. A listing with a streamable-http endpoint
    # can be enabled as-is — POST /v1/tool/mcp/servers with its id — and its tools then
    # join the org's tool plane and the fleet's MCP server. A listing that only ships a
    # stdio package needs a process to run it, which is why the transports are on
    # every entry rather than implied.
    # Hidden entries are absent: they are the ones we took off the shelf. A platform
    # SuperAdmin sees them, because the same query answers "what is on the shelf" and
    # "what is in the catalog" and two queries would drift apart.
    # It is PAGED — 50 by default, 200 at most. The public registry publishes tens of
    # thousands of servers, so an unbounded answer is a twenty-megabyte response and a
    # storefront that renders in a minute. total is the whole match, not the page.
    get_tool_catalog(req: catalogQuery) returns (rep: mcpCatalog)
    # Returns one catalog entry in full: the publisher's description, its
    # repository and site, every package form with the runtime that launches it, and
    # every hosted endpoint. It is what a branding page renders, and what tells a
    # caller whether the listing can be enabled here and now (a streamable-http
    # remote) or needs somewhere to run first (a stdio package).
    # A HIDDEN listing is not served to an org — a shelf that renders what it does
    # not list would be a way around the shelf — but is served to a SuperAdmin, who
    # is the one deciding whether to put it back.
    get_tool_catalog_by_id(req: listingRef) returns (rep: MCPListing)
    # Lists the external MCP servers the caller's org has registered.
    # Each record carries the URL and the name of the header its credential is
    # injected into; the credential VALUE lives only in KMS and is never returned,
    # so hasSecret is the whole of what this surface says about it.
    get_tool_mcp_servers() returns (rep: mcpServerList)
    # Reports what this deployment actually mounted: every subsystem the
    # composition root declared and whether it is switched on. A plugin here is
    # MOUNTED CODE that extends the deployment's own surface — not a tool an agent
    # calls — so this is an inventory and not a tool source. It is read off the same
    # boot snapshot every traced request resolves its subsystem label against, so it
    # cannot drift from what is serving. Enabled-only by default, because a caller
    # asking what this deployment can do wants what is running; ?all=true adds the
    # configured-but-off ones.
    get_tool_plugins(req: pluginQuery) returns (rep: pluginMountList)
    # Lists the plugins the caller's org BUILT, newest first,
    # each with the TypeScript as authored. That is a different set with a different
    # lifecycle from GET /v1/tool/plugins, which reports the subsystems this deployment
    # mounted. The bundled CommonJS the runtime executes is never included, and
    # neither is any credential — a plugin names the connectors provider it needs and
    # reads the credential from ctx.auth at run time.
    get_tool_plugins_authored() returns (rep: authoredPluginList)
    # Lists the skills the caller's org can reach — the brand's embedded
    # catalogue plus the org's own authored ones — with each one's activation flag.
    # A skill is discovery and activation metadata attached to an agent, never called
    # directly, so every entry here is non-dispatchable. It is GET /v1/tool narrowed
    # to one source, not a second store: a name a caller sees here is the same entry,
    # with the same activation state, that discovery reports.
    get_tool_skills(req: sourceQuery) returns (rep: sourceToolList)
    # Lists the caller org's OWN skills with their SKILL.md
    # bodies. GET /v1/tool/skills is the registry view — the brand's catalogue plus this
    # org's, with activation flags and no bodies; this is the EDITABLE set, so it
    # carries the content that view omits and nothing the org did not write.
    get_tool_skills_authored() returns (rep: authoredSkillList)
    # Sets what WE say about one catalog entry — hidden, featured,
    # official, logo — and answers with the stored listing. SuperAdmin only; every
    # other caller is refused.
    # Curation is the half of a catalog row a sync cannot write, and this is the only
    # thing that writes it. The upstream half is never editable here: a description
    # that disagreed with the publisher's would be a fork of their listing, and the
    # next sync would silently undo it.
    patch_tool_catalog_by_id(req: curateReq) returns (rep: MCPListing)
    # Pulls the public MCP registry into our canonical copy and reports
    # what changed. SuperAdmin only; every other caller is refused.
    # It is IDEMPOTENT: a listing is keyed by the publisher's own reverse-DNS name,
    # so a second pass over an unchanged registry rewrites the same rows and reports
    # added=0, updated=0. It never deletes — a listing that vanishes upstream may be
    # one an org has already enabled, and dropping its description would not drop its
    # server. And it never touches CURATION: hidden, featured, an admin-set official
    # and a logo survive every sync, because the write does not name those columns.
    post_tool_catalog_sync() returns (rep: mcpCatalogSync)
    # Gives the caller's org one more external MCP server, so its tools
    # join the org's tool plane and the fleet's MCP server. It is the ONE way an org
    # gains a server, whether it typed the URL in or enabled a catalog listing: both
    # write the SAME record, and `source` says which it was. A second registration
    # path would be a second place for a server to exist, and then a second place to
    # forget to check the credential.
    # The credential VALUE is sealed in KMS under a per-org ref; the row keeps only
    # the URL, the header name to inject it into, and a has-secret flag — so a secret
    # with no KMS configured is refused 503 rather than stored in the clear. The URL
    # is SSRF-validated here and re-checked by the dialer at connect time, which is
    # the DNS-rebinding defense.
    # Enabling a listing the org already enabled REVISES that server rather than
    # adding a near-duplicate beside it, so a retried enable is the same one server.
    # Answers 201 with the stored record.
    post_tool_mcp_servers(req: createServerReq) returns (rep: MCPServer)
    # Builds and stores one plugin for the caller's org. The 201 carries
    # the bundle's size, whether a model wrote the source, and the plugin as stored.
    # Post `source` to build TypeScript as-is, or `spec` — an OpenAPI document or
    # plain prose describing the endpoints — to have one generated; the generated
    # source comes back in the answer, so a caller reads what will run before it
    # runs. Exactly one of the two, and `name` must be one lowercase path segment;
    # both or neither is 400.
    # COMPILING IS THE GATE. The source goes through the same pipeline the committed
    # connectors do — esbuild to one CommonJS program, then compiled in the goja
    # runtime that will actually execute it — and anything that fails is rejected and
    # NEVER stored. So a plugin in the store is one this deployment has already
    # loaded once, not one a model claimed was fine. A failed build answers 422
    # carrying the diagnostics a caller needs to fix it: the bundler's error
    # (`detail`), the source that failed, and whether the model wrote it.
    # CREDENTIALS ARE NOT PART OF A PLUGIN. A plugin names the connectors `provider`
    # it needs and reads that credential from `ctx.auth` at run time, under KMS
    # custody. Source that carries something key-shaped is REFUSED rather than
    # silently persisted — a scrubbed key looks like it worked.
    post_tool_plugins_build(req: buildRequest) returns (rep: buildOut)
    # Adds or revises one of the caller org's own skills, and answers 201
    # with the stored record. The id is derived from the name, so writing the same
    # name again REVISES that skill rather than accumulating near-duplicates that
    # would then collide in the registry. An org's skills are private to it by
    # construction — they live in a different store from the brand's embedded
    # catalogue and have no path into the public gallery — and a brand skill always
    # wins a name collision against an org's.
    post_tool_skills(req: skillIn) returns (rep: skillWritten)
    # Switches tools on and off for the caller's org and project, and
    # answers with the resulting activated set. It is the ONE write path that turns
    # skills, plugins and connectors into callable tools — an unactivated tool is
    # listed by discovery but refused 403 at dispatch. Activate is applied before
    # Deactivate, so a name in both lists ends up off. More than 256 toggles in one
    # request is refused 413.
    put_tool_activation(req: activationReq) returns (rep: activationSet)
}

# ---------------------------------------------------------------------
# 18 op(s) here. What follows is what this schema does not carry.
#
# blocked (2) — the op is absent; the field has no wire form:
#   post_tool_call  toolCall.Arguments  map[string]interface {}  (map)
#   post_tool_call  toolResult.Result  interface {}  (any)
#
# opaque (11) — crosses, arrives without its name:
#   MCPListing.Packages  tools.MCPPackage (list element)
#   MCPListing.Remotes  tools.MCPRemote (list element)
#   authoredPluginList.Plugins  tools.AuthoredPlugin (list element)
#   authoredSkillList.Skills  tools.Skill (list element)
#   buildOut.Plugin  tools.AuthoredPlugin
#   mcpCatalog.Catalog  tools.MCPListing (list element)
#   mcpServerList.Servers  tools.MCPServer (list element)
#   pluginMountList.Plugins  tools.pluginMount (list element)
#   skillWritten.Skill  tools.Skill
#   sourceToolList.Tools  tools.Tool (list element)
#   toolList.Tools  tools.Tool (list element)
