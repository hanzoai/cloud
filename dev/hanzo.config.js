// A local project, declared. This is the whole file — everything else about the
// stack is derived or already default.
//
// The cloud that serves it is the SAME binary that serves api.hanzo.ai: one
// host, every capability, each one started on the first request that reaches it.
// Nothing here selects capabilities, because selecting them would be a second
// way to say what a request already says.
export default {
  // The name is the address: this project answers at <name>.localhost.
  name: 'demo',

  // The organization that owns it. Seeded orgs are `hanzo` and `acme`.
  org: 'hanzo',

  // What to serve, relative to this file.
  serve: './public',

  // The port the local edge listens on.
  port: 28080,
};
