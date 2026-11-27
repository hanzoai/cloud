# LLM.md — apps/sites

Guidance for apps/sites. Moved from the root LLM.md; cross-cutting rules stay in the root.

### Deep links on a site (`_fallback.json`)

`apps/sites/fallback.go`: a bundle may declare `{"/chat": "chat.html"}` at its
root. A page-shaped path no file answers gets the page of its longest declared
prefix, by whole segment; an extension, an undeclared path or a bad table stays

404. hanzo.ai's `scripts/serve.mjs` and dev rewrites read the same file.
