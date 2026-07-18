package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// platform builds a platform REST client from the resolved env + the global
// --platform-token flag. The token may be empty here; the client surfaces a
// precise error on first use.
func (e *Env) platform(gf *globalFlags) *Platform {
	return newPlatform(e.PlatformURL, e.platformToken(gf.platformToken))
}

// dashIfEmpty renders a string cell, "-" when empty.
func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// yesno renders a bool for a table cell.
func yesno(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// newTab returns a tabwriter writing to w with a 2-space gutter.
func newTab(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
}

// ---------------------------------------------------------------------------
// apps — the fleet drift board (GET /v1/paas/apps). Org-confined server-side by
// the IAM identity: a superadmin sees the whole fleet, an org-admin only its own.
// ---------------------------------------------------------------------------

func newAppsCmd(envOf func() *Env, gf *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apps",
		Short: "List/get the platform apps board (declared/running/latest/drift)",
	}

	var envFilter, healthFilter string
	var driftOnly bool

	list := &cobra.Command{
		Use:   "list",
		Short: "List apps with declared/running tags, health and drift",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e := envOf()
			res, err := e.platform(gf).Apps(cmd.Context(), AppsQuery{
				Env:    envFilter,
				Health: healthFilter,
				Drift:  driftOnly,
			})
			if err != nil {
				return err
			}
			return e.emit(res, func(w io.Writer) {
				tw := newTab(w)
				fmt.Fprintln(tw, "ORG\tAPP\tENV\tDECLARED\tRUNNING\tHEALTH\tDRIFT")
				for _, a := range res.Apps {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
						a.Org, a.App, a.Env, dashIfEmpty(a.DeclaredTag), dashIfEmpty(a.RunningTag),
						dashIfEmpty(a.Health), driftSeverity(a.Drift))
				}
				tw.Flush()
				fmt.Fprintf(w, "\n%d apps  (ok=%d yellow=%d red=%d)\n",
					res.Summary.Total, res.Summary.ByDrift["ok"],
					res.Summary.ByDrift["yellow"], res.Summary.ByDrift["red"])
			})
		},
	}
	list.Flags().StringVar(&envFilter, "env", "", "filter by env: main|test|dev")
	list.Flags().StringVar(&healthFilter, "health", "", "filter by health: green|yellow|red")
	list.Flags().BoolVar(&driftOnly, "drift", false, "only rows that are drifting")

	get := &cobra.Command{
		Use:   "get <app>",
		Short: "Get one app row by its CR name (production by default)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e := envOf()
			a, err := e.platform(gf).App(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return e.emit(a, func(w io.Writer) {
				tw := newTab(w)
				fmt.Fprintf(tw, "id:\t%s\n", a.ID)
				fmt.Fprintf(tw, "org:\t%s\n", a.Org)
				fmt.Fprintf(tw, "app:\t%s\n", a.App)
				fmt.Fprintf(tw, "env:\t%s\n", a.Env)
				fmt.Fprintf(tw, "repo:\t%s\n", a.Repo)
				fmt.Fprintf(tw, "registry:\t%s\n", a.Registry)
				fmt.Fprintf(tw, "declared:\t%s\n", dashIfEmpty(a.DeclaredTag))
				fmt.Fprintf(tw, "running:\t%s\n", dashIfEmpty(a.RunningTag))
				fmt.Fprintf(tw, "health:\t%s\n", dashIfEmpty(a.Health))
				fmt.Fprintf(tw, "phase:\t%s\n", dashIfEmpty(a.Phase))
				fmt.Fprintf(tw, "drift:\t%s\n", driftSeverity(a.Drift))
				fmt.Fprintf(tw, "cluster:\t%s\n", dashIfEmpty(a.Cluster))
				fmt.Fprintf(tw, "namespace:\t%s\n", dashIfEmpty(a.Namespace))
				if len(a.Endpoints) > 0 {
					fmt.Fprintf(tw, "endpoints:\t%s\n", strings.Join(a.Endpoints, ", "))
				}
				tw.Flush()
			})
		},
	}

	cmd.AddCommand(list, get)
	return cmd
}

// ---------------------------------------------------------------------------
// deploy — POST /v1/paas/apps/{app}/deploy: a zero-downtime rolling restart.
// ---------------------------------------------------------------------------

func newDeployCmd(envOf func() *Env, gf *globalFlags) *cobra.Command {
	var environment string
	cmd := &cobra.Command{
		Use:   "deploy <app>",
		Short: "Redeploy an app (rolling restart, zero-downtime)",
		Long: "Drive a platform redeploy: a rolling restart of the app's k8s Deployment\n" +
			"(re-pulls the declared image, recreates pods, zero downtime). The app is the\n" +
			"operator App CR name; the org comes from your IAM identity (org-scoped). Use\n" +
			"--env to target test/dev (default: production). This is the canonical\n" +
			"PaaS-driven deploy — a TAG change is still a git commit.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e := envOf()
			res, err := e.platform(gf).Redeploy(cmd.Context(), args[0], environment)
			if err != nil {
				return err
			}
			return e.emit(res, func(w io.Writer) {
				fmt.Fprintf(w, "restarted %s (namespace=%s env=%s at %s)\n",
					res.App, res.Namespace, dashIfEmpty(res.Env), res.RestartedAt)
			})
		},
	}
	cmd.Flags().StringVar(&environment, "env", "", "lifecycle env: main|test|dev (default production)")
	return cmd
}

// ---------------------------------------------------------------------------
// clusters — GET /v1/clusters: the org's compute fleet (Visor-managed + BYO),
// tenant-scoped server-side by the IAM identity.
// ---------------------------------------------------------------------------

func newClustersCmd(envOf func() *Env, gf *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "clusters",
		Aliases: []string{"cluster"},
		Short:   "List the org's clusters (managed + BYO)",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List the org's clusters",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e := envOf()
			cs, err := e.platform(gf).Clusters(cmd.Context())
			if err != nil {
				return err
			}
			return e.emit(cs, func(w io.Writer) {
				tw := newTab(w)
				fmt.Fprintln(tw, "NAME\tID\tREGION\tSTATUS\tKIND\tNODES\tSIZE\tGPUS")
				for _, c := range cs {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
						c.Name, dashIfEmpty(c.ID()), dashIfEmpty(c.Region), dashIfEmpty(c.Status),
						dashIfEmpty(c.Kind), c.NodeCount, dashIfEmpty(c.NodeSize), gpuCell(c))
				}
				tw.Flush()
				if len(cs) == 0 {
					fmt.Fprintln(w, "(no clusters)")
				}
			})
		},
	}

	get := &cobra.Command{
		Use:   "get <cluster-id>",
		Short: "Show one cluster",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e := envOf()
			cs, err := e.platform(gf).Clusters(cmd.Context())
			if err != nil {
				return err
			}
			for _, c := range cs {
				if c.ID() == args[0] || c.Name == args[0] {
					return e.emit(c, func(w io.Writer) { printCluster(w, c) })
				}
			}
			return fmt.Errorf("cluster %q not found", args[0])
		},
	}

	cmd.AddCommand(list, get)
	return cmd
}

// gpuCell renders the live GPU inventory of a cluster ("-" when none).
func gpuCell(c Cluster) string {
	var parts []string
	if c.NvidiaGPU > 0 {
		parts = append(parts, fmt.Sprintf("%d nvidia", c.NvidiaGPU))
	}
	if c.AmdGPU > 0 {
		parts = append(parts, fmt.Sprintf("%d amd", c.AmdGPU))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, "+")
}

func printCluster(w io.Writer, c Cluster) {
	tw := newTab(w)
	fmt.Fprintf(tw, "id:\t%s\n", dashIfEmpty(c.ID()))
	fmt.Fprintf(tw, "name:\t%s\n", c.Name)
	fmt.Fprintf(tw, "region:\t%s\n", dashIfEmpty(c.Region))
	fmt.Fprintf(tw, "status:\t%s\n", dashIfEmpty(c.Status))
	fmt.Fprintf(tw, "kind:\t%s\n", dashIfEmpty(c.Kind))
	fmt.Fprintf(tw, "nodeCount:\t%d\n", c.NodeCount)
	fmt.Fprintf(tw, "nodeSize:\t%s\n", dashIfEmpty(c.NodeSize))
	fmt.Fprintf(tw, "gpus:\t%s\n", gpuCell(c))
	fmt.Fprintf(tw, "created:\t%s\n", dashIfEmpty(c.CreatedAt))
	for _, np := range c.NodePools {
		fmt.Fprintf(tw, "pool:\t%s (%s x%d, autoscale=%s)\n", np.Name, np.Size, np.Count, yesno(np.AutoScale))
	}
	tw.Flush()
}

// ---------------------------------------------------------------------------
// build — platform-native build (runner fabric) enqueue.
// ---------------------------------------------------------------------------

func newBuildCmd(envOf func() *Env, gf *globalFlags) *cobra.Command {
	var br BuildReq
	var buildToken string
	cmd := &cobra.Command{
		Use:   "build <repo>",
		Short: "Enqueue a platform-native build on the runner fabric (no GitHub builders)",
		Long: "Enqueue a build on the platform's native runner fabric. Builds and pushes\n" +
			"the named image at a SHA; on completion the platform patches the operator\n" +
			"Service CR (build-job → deploy). Requires a live registered runner for the\n" +
			"target pool (409 otherwise).",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e := envOf()
			if len(args) == 1 {
				br.Repo = args[0]
			}
			if br.Repo == "" || br.SHA == "" || br.Image == "" {
				return fmt.Errorf("--repo (or positional), --sha and --image are required")
			}
			// The platform build muscle clones an https git URL; accept the
			// idiomatic `owner/name` shorthand and expand it to GitHub (the host
			// for every hanzoai/luxfi/zooai repo). A full URL passes through.
			br.Repo = normalizeRepoURL(br.Repo)
			if br.OrganizationID == "" {
				br.OrganizationID = e.Org // optional; server defaults to DEFAULT_BUILD_ORG_ID
			}
			job, err := e.platform(gf).EnqueueBuild(cmd.Context(), br, e.buildToken(buildToken))
			if err != nil {
				return err
			}
			return e.emit(job, func(w io.Writer) {
				tw := newTab(w)
				fmt.Fprintf(tw, "buildJobId:\t%s\n", job.BuildJobID)
				fmt.Fprintf(tw, "status:\t%s\n", job.Status)
				fmt.Fprintf(tw, "runnerPool:\t%s\n", job.RunnerPool)
				fmt.Fprintf(tw, "image:\t%s\n", job.Image)
				fmt.Fprintf(tw, "target:\t%s\n", job.Target)
				tw.Flush()
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&br.Repo, "repo", "", "owner/name (e.g. hanzoai/pricing)")
	f.StringVar(&br.SHA, "sha", "", "commit SHA to build")
	f.StringVar(&br.Image, "image", "", "image to build+push (e.g. ghcr.io/hanzoai/pricing:<tag>)")
	f.StringVar(&br.Branch, "branch", "", "branch (default main)")
	f.StringVar(&br.Dockerfile, "dockerfile", "", "Dockerfile path")
	f.StringVar(&br.Context, "context", "", "build context")
	f.StringVar(&br.DockerTarget, "target", "", "Docker build stage (--target)")
	f.StringVar(&br.OS, "os", "", "linux|darwin|windows (default linux)")
	f.StringVar(&br.Arch, "arch", "", "amd64|arm64 (default amd64)")
	f.StringVar(&buildToken, "build-token", "", "platform build-enqueue token (else env/credential store)")
	return cmd
}

// normalizeRepoURL expands the idiomatic `owner/name` shorthand to a full GitHub
// https URL (the platform build muscle clones https), and leaves an explicit URL
// (http/https/git/ssh scheme, or a scp-style git@host:owner/name) untouched. Only
// a bare single-segment `owner/name` — two path parts, no scheme, no host — is
// expanded; anything else is the caller's explicit choice and passes through.
func normalizeRepoURL(repo string) string {
	r := strings.TrimSpace(repo)
	if r == "" {
		return r
	}
	// Already a URL or scp-style remote → leave as-is.
	if strings.Contains(r, "://") || strings.Contains(r, "@") {
		return r
	}
	// Bare owner/name (exactly two non-empty segments, no host dot in the first).
	parts := strings.Split(strings.Trim(r, "/"), "/")
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" && !strings.Contains(parts[0], ".") {
		return "https://github.com/" + parts[0] + "/" + parts[1]
	}
	return r
}
