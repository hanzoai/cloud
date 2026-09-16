/**
 * digest.mjs — what the frozen configuration was frozen AGAINST.
 *
 * `ablations/frozen-<facts>.json` recorded a commit hash. A commit hash does not
 * survive a rebase: the one in the LoCoMo record, `33d0f8749bd1`, is not an
 * ancestor of this history and cannot be checked out from it, while the paper
 * tables print it as the provenance of every number on the page. A stamp that
 * does not resolve is worse than no stamp, because it reads as verified.
 *
 * So the stamp is the CONTENT of the files that decide the numbers, which is the
 * shape mab/freeze.mjs already uses and the shape that survives history being
 * rewritten. The commit stays beside it — it is useful when it happens to
 * resolve — but it is no longer the thing anyone checks.
 *
 * The list is deliberately short, and sweep.mjs is deliberately NOT on it. These
 * three files ARE the retrieval engine — the generators, their scorer, the second
 * hop — and a change in any of them can move a row. The sweep only chose the
 * weights, and the weights it chose are values in this same record; stamping it
 * would report drift on rows an edit there cannot reach, and a signal that fires
 * when nothing moved is one people learn to ignore.
 *
 * The list is an ARGUMENT, not a constant, because the code lane has the same
 * hole and a different engine. Its stamps are orphaned the same way — commits
 * 337022ce8e37 and ddc076f5dd05 exist in the object store and are ancestors of
 * nothing. One function, two callers, each naming the files that decide its own
 * rows.
 */
import { createHash } from 'node:crypto'
import { readFileSync, existsSync } from 'node:fs'

export const ENGINE = ['context.mjs', 'rank.mjs', 'score.mjs']

/** The code lane's engine, relative to bench/brain/. */
export const CODE_ENGINE = ['../code/context-code.mjs', 'metrics.mjs', 'llm.mjs']

/** sha256 of each engine file, plus one digest over all of them in order. */
export function digest(here = new URL('.', import.meta.url), files = ENGINE) {
  const code = {}
  const all = createHash('sha256')
  for (const f of files) {
    const p = new URL(`./${f}`, here)
    if (!existsSync(p)) continue
    const h = createHash('sha256').update(readFileSync(p)).digest('hex')
    code[f] = h
    all.update(f).update(h)
  }
  return { code, engine: all.digest('hex').slice(0, 12) }
}

/**
 * What a reader needs to know about a frozen record: whether the engine that
 * produced it is the engine on disk, said as a value rather than an assurance.
 */
export function check(frozen, here, files = ENGINE) {
  const now = digest(here, files)
  if (!frozen?.engine) return { state: 'unstamped', engine: now.engine }
  if (frozen.engine === now.engine) return { state: 'match', engine: now.engine }
  const moved = files.filter((f) => frozen.code?.[f] !== now.code[f])
  return { state: 'drift', engine: now.engine, was: frozen.engine, moved }
}
