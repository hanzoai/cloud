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

// deref renders a *string for a table cell, "-" when nil/empty.
func deref(p *string) string {
	if p == nil || *p == "" {
		return "-"
	}
	return *p
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
// apps — the observe surface.
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
				Org:    e.Org, // empty == all (single-tenant default)
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
						a.Org, a.App, a.Env, deref(a.DeclaredTag), deref(a.RunningTag),
						deref(a.Health), driftSeverity(a.Drift))
				}
				tw.Flush()
				fmt.Fprintf(w, "\n%d apps  (ok=%d yellow=%d red=%d)\n",
					res.Summary.Total, res.Summary.ByDrift["ok"],
					res.Summary.ByDrift["yellow"], res.Summary.ByDrift["red"])
			})
		},
	}
	list.Flags().StringVar(&envFilter, "env", "", "filter by env: dev|test|main")
	list.Flags().StringVar(&healthFilter, "health", "", "filter by health: green|yellow|red")
	list.Flags().BoolVar(&driftOnly, "drift", false, "only rows that are drifting")

	get := &cobra.Command{
		Use:   "get <org/app/env>",
		Short: "Get one app row by its <org>/<app>/<env> id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e := envOf()
			a, err := e.platform(gf).App(cmd.Context(), args[0], e.Org)
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
				fmt.Fprintf(tw, "declared:\t%s\n", deref(a.DeclaredTag))
				fmt.Fprintf(tw, "running:\t%s\n", deref(a.RunningTag))
				fmt.Fprintf(tw, "latest:\t%s\n", deref(a.LatestTag))
				fmt.Fprintf(tw, "health:\t%s\n", deref(a.Health))
				fmt.Fprintf(tw, "drift:\t%s\n", driftSeverity(a.Drift))
				fmt.Fprintf(tw, "cluster:\t%s\n", deref(a.Cluster))
				fmt.Fprintf(tw, "namespace:\t%s\n", deref(a.Namespace))
				fmt.Fprintf(tw, "updated:\t%s\n", a.UpdatedAt)
				tw.Flush()
			})
		},
	}

	sync := &cobra.Command{
		Use:   "sync",
		Short: "Trigger an inventory refresh of the apps board",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e := envOf()
			if err := e.platform(gf).SyncApps(cmd.Context()); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "apps sync triggered")
			return nil
		},
	}

	cmd.AddCommand(list, get, sync)
	return cmd
}

// ---------------------------------------------------------------------------
// deploy — the drive surface (rolling restart, zero-downtime).
// ---------------------------------------------------------------------------

func newDeployCmd(envOf func() *Env, gf *globalFlags) *cobra.Command {
	var project, environment string
	cmd := &cobra.Command{
		Use:   "deploy <container>",
		Short: "Redeploy a container (rolling restart, zero-downtime)",
		Long: "Drive a platform redeploy: a rolling restart of the container's k8s\n" +
			"Deployment (re-pulls the image, recreates pods, zero downtime). Coordinates\n" +
			"are exact — org (--org/config), project (--project), env (--env) and the\n" +
			"container id (positional). This is the canonical PaaS-driven deploy.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e := envOf()
			org, err := e.requireOrg()
			if err != nil {
				return err
			}
			if project == "" || environment == "" {
				return fmt.Errorf("--project and --env are required (the container's project/environment ids)")
			}
			container := args[0]
			if err := e.platform(gf).Redeploy(cmd.Context(), org, project, environment, container); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "redeployed %s (org=%s project=%s env=%s)\n", container, org, project, environment)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project id")
	cmd.Flags().StringVar(&environment, "env", "", "environment id")
	return cmd
}

// ---------------------------------------------------------------------------
// clusters — dedicated DOKS cluster lifecycle.
// ---------------------------------------------------------------------------

func newClustersCmd(envOf func() *Env, gf *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "clusters",
		Aliases: []string{"cluster"},
		Short:   "Provision/list/select dedicated DOKS clusters",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List the org's dedicated clusters",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e := envOf()
			org, err := e.requireOrg()
			if err != nil {
				return err
			}
			cs, err := e.platform(gf).Clusters(cmd.Context(), org)
			if err != nil {
				return err
			}
			return e.emit(cs, func(w io.Writer) {
				tw := newTab(w)
				fmt.Fprintln(tw, "NAME\tID\tREGION\tSTATUS\tPHASE\tACTIVE\tOPERATOR\tBASELINE")
				for _, c := range cs {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
						c.Name, c.DoksClusterID, c.Region, c.Status, c.Phase,
						yesno(c.Active), yesno(c.OperatorInstalled), yesno(c.BaselineInstalled))
				}
				tw.Flush()
				if len(cs) == 0 {
					fmt.Fprintln(w, "(no dedicated clusters)")
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
			org, err := e.requireOrg()
			if err != nil {
				return err
			}
			cs, err := e.platform(gf).Clusters(cmd.Context(), org)
			if err != nil {
				return err
			}
			for _, c := range cs {
				if c.DoksClusterID == args[0] || c.Name == args[0] {
					return e.emit(c, func(w io.Writer) { printCluster(w, c) })
				}
			}
			return fmt.Errorf("cluster %q not found in org %s", args[0], org)
		},
	}

	var region, nodeSize string
	var ha bool
	var nodeCount int
	create := &cobra.Command{
		Use:   "create",
		Short: "Provision a new dedicated DOKS cluster for the org",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e := envOf()
			org, err := e.requireOrg()
			if err != nil {
				return err
			}
			c, err := e.platform(gf).ProvisionCluster(cmd.Context(), org, ProvisionReq{
				Region: region, HA: ha, NodeSize: nodeSize, NodeCount: nodeCount,
			})
			if err != nil {
				return err
			}
			return e.emit(c, func(w io.Writer) {
				fmt.Fprintf(w, "provisioning cluster %s (%s)\n", c.Name, c.DoksClusterID)
				printCluster(w, *c)
			})
		},
	}
	create.Flags().StringVar(&region, "region", "", "DO region (default sfo3)")
	create.Flags().BoolVar(&ha, "ha", false, "highly-available control plane")
	create.Flags().StringVar(&nodeSize, "node-size", "", "node size slug (e.g. s-2vcpu-4gb)")
	create.Flags().IntVar(&nodeCount, "node-count", 0, "node count")

	var shared bool
	selectCmd := &cobra.Command{
		Use:   "select <cluster-id>",
		Short: "Set the org's active deploy target (or --shared to revert)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e := envOf()
			org, err := e.requireOrg()
			if err != nil {
				return err
			}
			var clusterID *string
			switch {
			case shared:
				clusterID = nil
			case len(args) == 1:
				clusterID = &args[0]
			default:
				return fmt.Errorf("give a cluster id, or --shared to revert to the shared cluster")
			}
			t, err := e.platform(gf).SelectTarget(cmd.Context(), org, clusterID)
			if err != nil {
				return err
			}
			return e.emit(t, func(w io.Writer) { printTarget(w, t) })
		},
	}
	selectCmd.Flags().BoolVar(&shared, "shared", false, "revert to the shared cluster")

	installBaseline := &cobra.Command{
		Use:   "install-baseline <cluster-id>",
		Short: "Install the hanzo-operator + per-tenant baseline on a cluster",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e := envOf()
			org, err := e.requireOrg()
			if err != nil {
				return err
			}
			if err := e.platform(gf).InstallBaseline(cmd.Context(), org, args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "baseline install requested for %s\n", args[0])
			return nil
		},
	}

	target := &cobra.Command{
		Use:   "target",
		Short: "Show the org's current resolved deploy target",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e := envOf()
			org, err := e.requireOrg()
			if err != nil {
				return err
			}
			t, err := e.platform(gf).Target(cmd.Context(), org)
			if err != nil {
				return err
			}
			return e.emit(t, func(w io.Writer) { printTarget(w, t) })
		},
	}

	cmd.AddCommand(list, get, create, selectCmd, installBaseline, target)
	return cmd
}

func printCluster(w io.Writer, c Cluster) {
	tw := newTab(w)
	fmt.Fprintf(tw, "id:\t%s\n", c.DoksClusterID)
	fmt.Fprintf(tw, "name:\t%s\n", c.Name)
	fmt.Fprintf(tw, "region:\t%s\n", c.Region)
	fmt.Fprintf(tw, "status:\t%s\n", c.Status)
	fmt.Fprintf(tw, "phase:\t%s\n", c.Phase)
	fmt.Fprintf(tw, "active:\t%s\n", yesno(c.Active))
	fmt.Fprintf(tw, "operatorInstalled:\t%s\n", yesno(c.OperatorInstalled))
	fmt.Fprintf(tw, "baselineInstalled:\t%s\n", yesno(c.BaselineInstalled))
	fmt.Fprintf(tw, "endpoint:\t%s\n", deref(c.Endpoint))
	fmt.Fprintf(tw, "k8sVersion:\t%s\n", deref(c.K8sVersion))
	fmt.Fprintf(tw, "created:\t%s\n", c.CreatedAt)
	if c.BaselineError != nil && *c.BaselineError != "" {
		fmt.Fprintf(tw, "baselineError:\t%s\n", *c.BaselineError)
	}
	tw.Flush()
}

func printTarget(w io.Writer, t *Target) {
	tw := newTab(w)
	kind := "shared"
	if t.Dedicated {
		kind = "dedicated"
	}
	fmt.Fprintf(tw, "cluster:\t%s\n", t.Cluster)
	fmt.Fprintf(tw, "kind:\t%s\n", kind)
	for ns, env := range t.Namespaces {
		fmt.Fprintf(tw, "namespace:\t%s -> %s\n", ns, env)
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

// ---------------------------------------------------------------------------
// k8s — deploy-target helpers.
// ---------------------------------------------------------------------------

func newK8sCmd(envOf func() *Env, gf *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "k8s",
		Short: "Kubernetes deploy-target helpers",
	}
	target := &cobra.Command{
		Use:   "target",
		Short: "Show the org's current resolved deploy target (cluster + namespaces)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e := envOf()
			org, err := e.requireOrg()
			if err != nil {
				return err
			}
			t, err := e.platform(gf).Target(cmd.Context(), org)
			if err != nil {
				return err
			}
			return e.emit(t, func(w io.Writer) { printTarget(w, t) })
		},
	}
	cmd.AddCommand(target)
	return cmd
}
