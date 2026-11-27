// Package cron is scheduled work that survives a restart: declare the schedule,
// then watch every run.
//
// It is the platform's ONE cron system — durable schedules on the embedded
// hanzoai/tasks engine (cloud.EmbeddedTasks), replacing every k8s CronJob.
// There are no tickers here and no bespoke scheduler — the engine
// owns time (its 5s sweep fires due schedules; runs are durable workflows
// visible in the Tasks console at console.hanzo.ai/tasks and tasks.hanzo.ai).
//
// Entries are DATA, not code: a ConfigMap in CRON_NAMESPACE labeled
// cron.hanzo.ai/enabled="true" declares one schedule —
//
//	data:
//	  schedule: "30 1 * * *"        # 5/6-field cron
//	  job.yaml: |                   # EITHER: a batchv1 Job manifest to run
//	    apiVersion: batch/v1
//	    kind: Job
//	    ...
//	  poke.json: |                  # OR: a plane poke — an op this binary serves,
//	    {"app":"commerce","op":"billing_renew"}    # asked as the platform, through
//	                                # its client in planeOps (workflows.go)
//	  poke.json: |                  # OR: an HTTP poke, for endpoints outside it
//	    {"url":"http://pricing.hanzo.svc:8080/v1/sync",
//	     "method":"POST","bearerEnv":"PRICING_API_KEY"}
//
// so adding/changing/removing platform cron is a universe git edit — zero
// cloud code, except that a plane poke's op must be one cron holds a client for.
// A reconcile workflow (itself a durable schedule, cadence
// reconcileEvery) diffs ConfigMaps against registered schedules; the fire-time
// activity re-reads its ConfigMap fresh, so payload edits apply on the next
// tick without waiting for a reconcile.
//
// Schedules live in the CRON_ORG org shard (default "hanzo") in namespace
// "default", so platform admins see and manage them in the org-scoped Tasks
// UI. Workers subscribe org-agnostically on (namespace, queue); the engine
// routes each run's state back to the owning shard via the task token's org.
//
// Each run's outcome is also written into the ConfigMap cron-status (statusMap,
// metrics.go): cron-<entry> is the last run and cron-<entry>.failed the last
// failure, with why a Job failed. Anyone who can read the namespace's ConfigMaps,
// Hanzo CD included, can read it; the Tasks UI and the logs need a platform admin.
package cron
