// Read a project's declaration and print it as JSON.
//
// A JS config is EVALUATED, not parsed, which is the point: a project can
// compute a name from the branch or read an env var, and gets a real language
// to do it in rather than a template syntax that grows one feature at a time.
// The cost is that reading it needs a JS runtime, which is why this is the only
// place that does — everything downstream sees JSON.
import { pathToFileURL } from 'node:url';
import { resolve } from 'node:path';

const file = resolve(process.argv[2] ?? 'hanzo.config.js');
const mod = await import(pathToFileURL(file).href);
const cfg = mod.default ?? mod.config ?? mod;

// Defaults live HERE and nowhere else, so a field absent from the file and a
// field absent from the schema are the same thing to every reader downstream.
const out = {
  name: cfg.name,
  org: cfg.org ?? 'hanzo',
  serve: cfg.serve ?? './public',
  port: Number(cfg.port ?? 18080),
};
if (!out.name) { console.error('hanzo.config.js: `name` is required — it is the address the project answers at'); process.exit(1); }
if (!/^[a-z0-9][a-z0-9-]*$/.test(out.name)) { console.error(`hanzo.config.js: name "${out.name}" is not a hostname label`); process.exit(1); }
process.stdout.write(JSON.stringify(out));
