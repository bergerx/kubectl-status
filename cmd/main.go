package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"

	cc "github.com/ivanpirog/coloredcobra"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	_ "k8s.io/client-go/plugin/pkg/client/auth" // Initialize all known client auth plugins.
	"k8s.io/klog/v2"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
	"k8s.io/kubectl/pkg/util/completion"

	"github.com/bergerx/kubectl-status/pkg/plugin"
)

func main() {
	// Kubernetes uses UTC times, status shows times only in "... ago" format, so
	// setting the TZ to UTC is safe.
	_ = os.Setenv("TZ", "UTC")
	if err := RootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

var (
	longCmdMessage = `Display status for one or many resources

Prints human-friendly output that focuses on the status of the resources in kubernetes.

In most cases replacing a "kubectl get ..." with a "kubectl status ..." would be sufficient.

This plugin uses templates for well known api-conventions and has support for hardcoded resources,
not all resources are fully supported.`

	examplesMessage = `  # Show status of all pods in the current namespace
  kubectl status pods

  # Show status of all pods in all namespaces
  kubectl status pods --all-namespaces

  # Show status of all Deployments and StatefulSets in the current namespace
  kubectl status deploy,sts

  # Show status of all nodes
  kubectl status nodes

  # Show status of some pods
  kubectl status pod my-pod1 my-pod2

  # Same with previous
  kubectl status pod/my-pod1 pod/my-pod2

  # Show status of various resources
  kubectl status svc/my-svc1 pod/my-pod2

  # Show status of a particular deployment
  kubectl status deployment my-dep

  # Show deployments in the "v1" version of the "apps" API group.
  kubectl status deployments.v1.apps

  # Show status of nodes marked as master
  kubectl status node -l node-role.kubernetes.io/master
`
)

// This variable is populated by goreleaser
var version string

// RootCmd builds the kubectl-status cobra.Command. It owns a fresh *viper.Viper and
// plugin.RenderConfig for this invocation instead of reading the package-level viper singleton or
// pkg/plugin's Now/DurationRound/StartedAfterClause, so concurrent invocations (e.g. parallel
// tests) never share mutable state. cfgOpts let callers (tests) override RenderConfig's hooks
// before the command runs; production callers pass none.
func RootCmd(cfgOpts ...func(*plugin.RenderConfig)) *cobra.Command {
	v := viper.New()
	cfg := plugin.NewRenderConfig(v)
	for _, opt := range cfgOpts {
		opt(cfg)
	}
	cmd := &cobra.Command{
		Use:     "kubectl-status (TYPE[.VERSION][.GROUP] [NAME | -l label] | TYPE[.VERSION][.GROUP]/NAME ...) [flags]",
		Short:   "Display status for one or many resources",
		Long:    longCmdMessage,
		Example: examplesMessage,
		PreRun: func(cmd *cobra.Command, args []string) {
			v.AutomaticEnv()
			err := v.BindPFlags(cmd.Flags())
			if err != nil {
				cmd.PrintErr("error binding flags", err)
			}
		},
		SilenceUsage: true,
		Version:      version,
	}
	initColorCobra(cmd)
	configFlags := initFlags(cmd)
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	f := cmdutil.NewFactory(configFlags)
	cmd.ValidArgsFunction = completion.ResourceTypeAndNameCompletionFunc(f)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		klog.V(5).InfoS("running the cobra.Command ...")
		if err := checkErr(complete(f, v)); err != nil {
			return err
		}
		if err := checkErr(validate(v)); err != nil {
			return err
		}
		if b, _ := cmd.Flags().GetBool("test-hack"); b {
			v.Set("test-hack", true)
			cfg.DurationRound = func(_ interface{}) string { return "1m" }
		}
		ioStreams := genericiooptions.IOStreams{In: cmd.InOrStdin(), Out: cmd.OutOrStdout(), ErrOut: cmd.ErrOrStderr()}
		return checkErr(plugin.Run(f, ioStreams, args, cfg))
	}
	return cmd
}

// fatalMu serializes installing and consuming cmdutil's process-global fatal handler
// (BehaviorOnFatal + CheckErr). We keep routing errors through CheckErr for its user-friendly
// formatting (e.g. NoResourceMatchError phrasing, the "error: " prefix some e2e assertions pin
// on) rather than returning them raw. The lock is only held around the install-and-consume step in
// checkErr, not around whatever produced err, so it doesn't serialize concurrent RootCmd().Execute()
// calls' actual work (e.g. plugin.Run) -- only this narrow reformatting step, which is what made
// BehaviorOnFatal a process-global race in the first place: two concurrent invocations installing
// their own closure could catch each other's fatal error instead of their own.
var fatalMu sync.Mutex

// checkErr runs err through cmdutil.CheckErr's formatting (installing a scoped BehaviorOnFatal
// handler instead of letting CheckErr's default os.Exit tear down the process) and returns the
// formatted error instead of exiting, so RunE can propagate it normally. The handler is restored
// to the default before returning, so it doesn't leak into unrelated later calls.
func checkErr(err error) error {
	if err == nil {
		return nil
	}
	fatalMu.Lock()
	defer fatalMu.Unlock()
	// restore under lock, before Unlock, so a waiting caller's handler isn't clobbered
	defer cmdutil.DefaultBehaviorOnFatal()
	var out error
	cmdutil.BehaviorOnFatal(func(msg string, _ int) {
		out = errors.New(msg)
	})
	cmdutil.CheckErr(err)
	return out
}

func initFlags(cmd *cobra.Command) *genericclioptions.ConfigFlags {
	flags := cmd.Flags()
	initKlog(flags)
	// copied from kubectl.pkg.cmd as is
	configFlags := genericclioptions.NewConfigFlags(true).
		WithDeprecatedPasswordFlag().
		WithDiscoveryBurst(300).
		WithDiscoveryQPS(50.0)
	configFlags.AddFlags(flags)
	resourceBuilderFlags := genericclioptions.NewResourceBuilderFlags().
		WithAll(false).
		WithAllNamespaces(false).
		WithFile(false).
		WithLabelSelector("").
		WithFieldSelector("").
		WithLatest()
	resourceBuilderFlags.AddFlags(flags)
	addRenderFlags(flags)
	if ok, _ := flags.GetBool("help-all"); !ok {
		hideNoisyFlags(flags)
	}
	return configFlags
}

func initColorCobra(cmd *cobra.Command) {
	cc.Init(&cc.Config{
		RootCmd:         cmd,
		Headings:        cc.HiCyan + cc.Bold + cc.Underline,
		Commands:        cc.HiYellow + cc.Bold,
		Example:         cc.Italic,
		ExecName:        cc.Bold,
		Flags:           cc.Bold,
		NoExtraNewlines: true,
		NoBottomNewline: true,
	})
}

func hideNoisyFlags(flags *pflag.FlagSet) {
	flagsToHide := []string{
		"add_dir_header", "as-uid", "alsologtostderr", "as", "as-group", "cache-dir",
		"certificate-authority", "client-certificate", "client-key", "cluster", "context", "insecure-skip-tls-verify",
		"kubeconfig", "log_backtrace_at", "log_dir", "log_file", "log_file_max_size", "logtostderr", "one_output",
		"password", "request-timeout", "server", "skip_headers", "skip_log_headers", "stderrthreshold",
		"tls-server-name", "token", "user", "username", "vmodule", "test-hack",
	}
	for _, flagName := range flagsToHide {
		flags.Lookup(flagName).Hidden = true
	}
}

func initKlog(flags *pflag.FlagSet) {
	// We Follow https://github.com/kubernetes/community/blob/master/contributors/devel/sig-instrumentation/logging.md
	// for the logs.
	fs := flag.NewFlagSet("", flag.PanicOnError)
	klog.InitFlags(fs)
	defer klog.Flush()
	flags.AddGoFlagSet(fs)
}

func addRenderFlags(flags *pflag.FlagSet) {
	flags.Bool("local", false,
		"Run the template against the provided yaml manifest. Need to be used with a --filename parameter. No request to apiserver is done.")
	flags.Bool("include-owners", false,
		"Follow the ownerReferences in the objects and render them as well.")
	flags.Bool("include-events", true,
		"Include events in the output.")
	flags.Bool("include-matching-ingresses", true,
		"Include Ingresses referencing the Service in the output.")
	flags.Bool("include-matching-routes", true,
		"Include Gateway API routes (HTTPRoute, GRPCRoute, TCPRoute, UDPRoute, TLSRoute) referencing the Service in the output.")
	flags.Bool("include-application-details", true,
		"This will include well known application metadata into the output.")
	flags.Bool("include-rollout-diffs", false,
		"Include unified diff between stored revisions of Deployment, DaemonSet and StatefulSets.")
	flags.Bool("include-all-volumes", false,
		"Include config-only volumes (configMap/secret/projected/downwardAPI) and per-container volume mount lists.")
	flags.Bool("include-managed-fields", true,
		"Include managed field details in the output.")
	flags.Bool("include-node-lease", false,
		"Include node lease details.")
	flags.Bool("include-node-kubelet-api-summary", true,
		"Include Kubelet API healthz, configz and stats/summary in the output.")
	flags.Bool("include-node-detailed-usage", true,
		"Include details about Pods' resource usage on a node. Does lots of queries against API Server and causes dramatic slow down.")
	flags.Bool("shallow", false,
		"Set all --include-* flags to false and let user selectively enable them.")
	flags.Bool("deep", false,
		"Set all --include-* flags to true and let user selectively disable them.")
	flags.BoolP("watch", "w", false,
		"After listing/getting the requested object, watch for changes.")
	flags.Bool("help-all", false,
		"Show all available flags.")
	flags.String("color", "auto",
		"One of 'auto', 'never' or 'always'.")
	flags.Bool("absolute-time", false,
		"Show absolute timestamps instead of relative durations.")
	flags.Bool("test-hack", false,
		"helper flag for tests, e.g. always report 1m for any time duration, 1.1.1.1 for IPs, etc.")
}

func isBoolConfigExplicitlySetToTrue(v *viper.Viper, key string) bool {
	return v.IsSet(key) && v.GetBool(key)
}

func isBoolConfigExplicitlySetToFalse(v *viper.Viper, key string) bool {
	return v.IsSet(key) && !v.GetBool(key)
}

func complete(f cmdutil.Factory, v *viper.Viper) error {
	klog.V(5).InfoS("Complete options...")
	err := setNamespace(f, v)
	if err != nil {
		return err
	}
	if v.GetBool("shallow") {
		allowExplicitIncludesOnly(v)
	}
	if v.GetBool("deep") {
		enableAllIncludes(v)
	}
	return nil
}

func setNamespace(f cmdutil.Factory, v *viper.Viper) error {
	if v.GetBool("all-namespaces") {
		v.Set("namespace", "")
		return nil
	}
	namespace, _, err := f.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return fmt.Errorf("can't determine namespace")
	}
	v.Set("namespace", namespace)
	return nil
}

func allowExplicitIncludesOnly(v *viper.Viper) {
	for key, val := range v.AllSettings() {
		if strings.HasPrefix(key, "include") {
			switch val.(type) {
			case bool:
				if !isBoolConfigExplicitlySetToTrue(v, key) {
					v.Set(key, false)
				}
			}
		}
	}
}

func enableAllIncludes(v *viper.Viper) {
	for key, val := range v.AllSettings() {
		if strings.HasPrefix(key, "include") {
			switch val.(type) {
			case bool:
				if !isBoolConfigExplicitlySetToFalse(v, key) {
					v.Set(key, true)
				}
			}
		}
	}
}

func validate(v *viper.Viper) error {
	klog.V(5).InfoS("Validating cli options...")
	if v.GetBool("shallow") && v.GetBool("deep") {
		return fmt.Errorf("--shallow and --deep are mutually exclusive")
	}
	if v.GetBool("local") && len(v.GetStringSlice("filename")) == 0 {
		return fmt.Errorf("when using --local, --filename must be provided")
	}
	return nil
}
