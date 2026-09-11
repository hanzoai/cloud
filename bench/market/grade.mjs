/**
 * How a period is graded. One reader per outcome metric, and a bench names the
 * reader it is read by.
 *
 * A reader answers two questions about one period, and it answers them at the
 * same moment for the same reason: both are facts about how the work landed, and
 * neither is knowable when the work is dispatched.
 *
 *   value  the outcome — what the market did with what the arm produced
 *   done   how many of the period's tasks are complete
 *
 * COMPLETION IS NOT DISPATCH. A task that was dispatched, ran, cost money and
 * produced nothing the grader can read is not complete, and neither is one the
 * budget refused. This is the column that makes a bench honest: a system that
 * spends its period's cap on the first task skips the rest, and pays for it here
 * rather than in a footnote. `Run.answered` in /v1/research is the sum of these,
 * and `completion` is derived from it against the tasks the bench planned.
 *
 * A reader reaches the world only through the platform it is handed — the live
 * cloud, or the dry stand-in. It holds no credential and opens no socket.
 */

/** The readers, by the name a bench's `outcome.reader` uses. */
export const readers = {
  'merged-changes': {
    metric: 'merged_changes',
    describe:
      'Changes the arm proposed in this period that a maintainer had merged by the time the window closed. ' +
      'A task is complete when it proposed a change whose checks ran — merged or not, because a rejected ' +
      'change is work done and a refused task is not.',
    async read({ platform, tasks }) {
      const ids = tasks.map((t) => t.artifact).filter(Boolean)
      const state = new Map((await platform.changes(ids)).map((c) => [c.id, c]))
      let value = 0, done = 0
      for (const t of tasks) {
        const c = t.artifact ? state.get(t.artifact) : null
        if (c && c.checks !== 'unknown') done++
        if (c?.merged) value++
      }
      return { value, done, detail: { proposed: ids.length, read: state.size } }
    },
  },
}

/** A reader by name, or a refusal naming the ones there are. */
export function readerOf(name) {
  const r = readers[name]
  if (!r) throw new Error(`no reader called ${name}; there are ${Object.keys(readers).join(', ') || 'none'}`)
  return r
}
