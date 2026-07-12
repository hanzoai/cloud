// Copyright © 2026 Hanzo AI. MIT License.

// Package cron is the platform's ONE cron system: durable schedules on the
// embedded hanzoai/tasks engine (cloud.EmbeddedTasks) replacing every k8s
// CronJob. There are no tickers here and no bespoke scheduler — the engine
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
//	  poke.json: |                  # OR: an HTTP poke
//	    {"url":"http://pricing.hanzo.svc:8080/v1/sync",
//	     "method":"POST","bearerEnv":"PRICING_API_KEY"}
//
// so adding/changing/removing platform cron is a universe git edit — zero
// cloud code. A reconcile workflow (itself a durable schedule, cadence
// reconcileEvery) diffs ConfigMaps against registered schedules; the fire-time
// activity re-reads its ConfigMap fresh, so payload edits apply on the next
// tick without waiting for a reconcile.
//
// Schedules live in the CRON_ORG org shard (default "hanzo") in namespace
// "default", so platform admins see and manage them in the org-scoped Tasks
// UI. Workers subscribe org-agnostically on (namespace, queue); the engine
// routes each run's state back to the owning shard via the task token's org.
package cron

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/hanzoai/cloud"
	tasksclient "github.com/hanzoai/tasks/pkg/sdk/client"
	tasksworker "github.com/hanzoai/tasks/pkg/sdk/worker"
	tasksengine "github.com/hanzoai/tasks/pkg/tasks"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

const (
	// taskQueue is this workflow family's own queue (<service>-<purpose>).
	taskQueue = "cloud-cron"
	// namespace is the tasks namespace platform cron lives in, inside the
	// CRON_ORG shard.
	namespace = "default"
	// idPrefix marks the schedules this package owns. Reconcile only ever
	// touches ids under the prefix, so it can never delete anything a user
	// or another subsystem registered.
	idPrefix = "cron-"
	// reconcileID is the self-schedule that keeps entries in sync with git.
	reconcileID = idPrefix + "reconcile"
	// reconcileEvery is the reconcile cadence — the worst-case delay for a
	// ConfigMap add/remove/schedule-change to take effect.
	reconcileEvery = "*/5 * * * *"
	// engineWait bounds how long Mount's background starter waits for
	// cloud.EmbeddedTasks (wired after MountAll) before giving up.
	engineWait = 5 * time.Minute
)

// org resolves the org shard platform cron lives in. Default "hanzo": the
// admin org of the one multi-brand cloud deployment (white-label hosts are
// hostnames of it, not separate clouds).
func org() string {
	if v := os.Getenv("CRON_ORG"); v != "" {
		return v
	}
	return "hanzo"
}

// Mount registers the durable platform cron. The engine is wired after
// MountAll (durable.go), so the actual start happens in a bounded background
// wait; until then nothing is scheduled. Fail-soft: no engine or no k8s API
// leaves the subsystem idle (ConfigMap-less environments simply have zero
// entries), never blocks boot.
func Mount(app *zip.App, deps cloud.Deps) error {
	if deps.Logger == nil {
		return fmt.Errorf("cron.Mount: nil deps.Logger")
	}
	log := deps.Logger.New("subsystem", "cron")
	go start(context.Background(), log)
	return nil
}

// start waits for the shared engine, then wires the ONE cron plane:
// namespace registration, the worker, one inline reconcile (boot
// convergence), and the reconcile self-schedule.
func start(ctx context.Context, log luxlog.Logger) {
	eng := waitEngine(ctx)
	if eng == nil {
		log.Warn("tasks engine never became ready — platform cron idle", "waited", engineWait)
		return
	}
	view := eng.View(org())
	if err := view.RegisterNamespace(tasksengine.Namespace{
		NamespaceInfo: tasksengine.NamespaceInfo{Name: namespace},
	}); err != nil {
		log.Error("cron: register namespace", "err", err)
		return
	}

	cli, err := tasksclient.Dial(tasksclient.Options{
		HostPort:  fmt.Sprintf("127.0.0.1:%d", eng.ZAPPort()),
		Namespace: namespace,
	})
	if err != nil {
		log.Error("cron: dial engine", "err", err)
		return
	}
	w := tasksworker.New(cli, taskQueue, tasksworker.Options{})
	w.RegisterWorkflow(PokeWorkflow)
	w.RegisterWorkflow(JobWorkflow)
	w.RegisterWorkflow(ReconcileWorkflow)
	w.RegisterActivity(PokeActivity)
	w.RegisterActivity(RunJobActivity)
	w.RegisterActivity(ReconcileActivity)
	if err := w.Start(); err != nil {
		cli.Close()
		log.Error("cron: worker start", "err", err)
		return
	}

	k := defaultKube()
	setWiring(k, log)
	if err := reconcile(ctx, view, k, log); err != nil {
		log.Warn("cron: boot reconcile", "err", err)
	}
	if err := upsertSchedule(view, tasksengine.Schedule{
		ScheduleId: reconcileID,
		Namespace:  namespace,
		Spec:       tasksengine.ScheduleSpec{CronString: []string{reconcileEvery}},
		Action: tasksengine.ScheduleAction{
			WorkflowType: tasksengine.TypeRef{Name: "ReconcileWorkflow"},
			TaskQueue:    taskQueue,
		},
	}); err != nil {
		log.Error("cron: reconcile self-schedule", "err", err)
		return
	}
	log.Info("platform cron live on the durable tasks engine",
		"org", org(), "namespace", namespace, "queue", taskQueue, "reconcile", reconcileEvery)
}

// waitEngine polls for the shared engine wired by serve.go after MountAll.
func waitEngine(ctx context.Context) *tasksengine.Embedded {
	deadline := time.Now().Add(engineWait)
	for {
		if eng := cloud.EmbeddedTasks(); eng != nil {
			return eng
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// reconcile converges the engine's cron-owned schedules onto the ConfigMap
// set: upsert entries whose spec drifted, delete entries whose ConfigMap is
// gone. Only ids under idPrefix (minus the reconcile self-schedule) are ever
// touched. Upserts are drift-gated: CreateSchedule rewrites CreateTime (the
// fire anchor), so rewriting an unchanged daily entry every reconcile would
// push its next fire forever into the future.
func reconcile(ctx context.Context, view tasksengine.View, k kube, log luxlog.Logger) error {
	entries, err := k.listEnabled(ctx)
	if err != nil {
		return fmt.Errorf("list cron configmaps: %w", err)
	}
	desired := make(map[string]tasksengine.Schedule, len(entries))
	for _, e := range entries {
		desired[idPrefix+e.Name] = scheduleFor(e)
	}

	existing, err := view.ListSchedules(namespace)
	if err != nil {
		return fmt.Errorf("list schedules: %w", err)
	}
	for _, s := range existing {
		if len(s.ScheduleId) < len(idPrefix) || s.ScheduleId[:len(idPrefix)] != idPrefix || s.ScheduleId == reconcileID {
			continue
		}
		want, ok := desired[s.ScheduleId]
		if !ok {
			if err := view.DeleteSchedule(namespace, s.ScheduleId); err != nil {
				log.Warn("cron: delete stale schedule", "id", s.ScheduleId, "err", err)
			} else {
				log.Info("cron: schedule removed (configmap gone)", "id", s.ScheduleId)
			}
			continue
		}
		if !scheduleDrifted(s, want) {
			delete(desired, s.ScheduleId)
		}
	}
	for id, s := range desired {
		if err := view.CreateSchedule(s); err != nil {
			log.Warn("cron: upsert schedule", "id", id, "err", err)
			continue
		}
		log.Info("cron: schedule registered", "id", id, "cron", s.Spec.CronString)
	}
	return nil
}

// scheduleFor maps a ConfigMap entry onto its engine schedule. The action
// input carries ONLY the entry name — the fire-time activity re-reads the
// ConfigMap, so payload edits apply without re-registration.
func scheduleFor(e entry) tasksengine.Schedule {
	wf := "JobWorkflow"
	if e.Kind == kindPoke {
		wf = "PokeWorkflow"
	}
	return tasksengine.Schedule{
		ScheduleId: idPrefix + e.Name,
		Namespace:  namespace,
		Spec:       tasksengine.ScheduleSpec{CronString: []string{e.Schedule}},
		Action: tasksengine.ScheduleAction{
			WorkflowType: tasksengine.TypeRef{Name: wf},
			TaskQueue:    taskQueue,
			Input:        []any{EntryInput{Name: e.Name}},
		},
	}
}

// scheduleDrifted reports whether the registered schedule no longer matches
// the desired spec/action (the only fields reconcile owns).
func scheduleDrifted(have, want tasksengine.Schedule) bool {
	if len(have.Spec.CronString) != 1 || have.Spec.CronString[0] != want.Spec.CronString[0] {
		return true
	}
	return have.Action.WorkflowType.Name != want.Action.WorkflowType.Name ||
		have.Action.TaskQueue != want.Action.TaskQueue
}

// upsertSchedule writes s only when absent or drifted (same anchor-reset
// rationale as reconcile).
func upsertSchedule(view tasksengine.View, s tasksengine.Schedule) error {
	if cur, ok, _ := view.DescribeSchedule(s.Namespace, s.ScheduleId); ok && cur != nil && !scheduleDrifted(*cur, s) {
		return nil
	}
	return view.CreateSchedule(s)
}
