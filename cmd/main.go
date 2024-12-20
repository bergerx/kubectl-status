package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	cc "github.com/ivanpirog/coloredcobra"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	_ "k8s.io/client-go/plugin/pkg/client/auth" // Initialize all known client auth plugins.
	"k8s.io/klog/v2"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"

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

func RootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "kubectl-status (TYPE[.VERSION][.GROUP] [NAME | -l label] | TYPE[.VERSION][.GROUP]/NAME ...) [flags]",
		Short:   "Display status for one or many resources",
		Long:    longCmdMessage,
		Example: examplesMessage,
		PreRun: func(cmd *cobra.Command, args []string) {
			viper.AutomaticEnv()
			err := viper.BindPFlags(cmd.Flags())
			if err != nil {
				cmd.PrintErr("error binding flags", err)
			}
		},
		SilenceUsage: true,
		Version:      version,
	}
	initColorCobra(cmd)
	configFlags := initFlags(cmd)
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	f := cmdutil.NewFactory(configFlags)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		klog.V(5).InfoS("running the cobra.Command ...")
		var err error
		cmdutil.BehaviorOnFatal(func(msg string, i int) {
			err = errors.New(msg)
		})
		cmdutil.CheckErr(complete(f))
		cmdutil.CheckErr(validate())
		if b, _ := cmd.Flags().GetBool("test-hack"); b {
			viper.Set("test-hack", true)
			plugin.SetDurationRound(func(_ interface{}) string { return "1m" })
		}
		ioStreams := genericiooptions.IOStreams{In: cmd.InOrStdin(), Out: cmd.OutOrStdout(), ErrOut: cmd.ErrOrStderr()}
		cmdutil.CheckErr(plugin.Run(f, ioStreams, args))
		return err
	}
	return cmd
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
	flags.Bool("include-matching-services", true,
		"Include Services matching the Pod in the output.")
	flags.Bool("include-matching-ingresses", true,
		"Include Ingresses referencing the Service in the output.")
	flags.Bool("include-application-details", true,
		"This will include well known application metadata into the output.")
	flags.Bool("include-rollout-diffs", false,
		"Include unified diff between stored revisions of Deployment, DaemonSet and StatefulSets.")
	flags.Bool("include-volumes", false,
		"Include volume relates information.")
	flags.Bool("include-managed-fields", true,
		"Include managed field details in the output.")
	flags.Bool("include-node-lease", false,
		"Include node lease details.")
	flags.Bool("include-node-kubelet-api-summary", true,
		"Include Kubelet API stats/summary in the output.")
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
	flags.Bool("test-hack", false,
		"helper flag for tests, e.g. always report 1m for any time duration, 1.1.1.1 for IPs, etc.")
}

func isBoolConfigExplicitlySetToTrue(key string) bool {
	return viper.IsSet(key) && viper.GetBool(key)
}

func complete(f cmdutil.Factory) error {
	klog.V(5).InfoS("Complete options...")
	err := setNamespace(f)
	if err != nil {
		return err
	}
	if viper.GetBool("shallow") {
		allowExplicitIncludesOnly()
	}
	if viper.GetBool("deep") {
		enableAllIncludes()
	}
	return nil
}

func setNamespace(f cmdutil.Factory) error {
	if viper.GetBool("all-namespaces") {
		viper.Set("namespace", "")
		return nil
	}
	namespace, _, err := f.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return fmt.Errorf("can't determine namespace")
	}
	viper.Set("namespace", namespace)
	return nil
}

func allowExplicitIncludesOnly() {
	for key, val := range viper.AllSettings() {
		if strings.HasPrefix(key, "include") {
			switch val.(type) {
			case bool:
				if !isBoolConfigExplicitlySetToTrue(key) {
					viper.Set(key, false)
				}
			}
		}
	}
}

func enableAllIncludes() {
	for key, val := range viper.AllSettings() {
		if strings.HasPrefix(key, "include") {
			switch val.(type) {
			case bool:
				viper.Set(key, true)
			}
		}
	}
}

func validate() error {
	klog.V(5).InfoS("Validating cli options...")
	if viper.GetBool("shallow") && viper.GetBool("deep") {
		return fmt.Errorf("--shallow and --deep are mutually exclusive")
	}
	if viper.GetBool("local") && len(viper.GetStringSlice("filename")) == 0 {
		return fmt.Errorf("when using --local, --filename must be provided")
	}
	return nil
}
