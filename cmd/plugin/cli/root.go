package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"

	"k8s.io/klog/v2"

	"github.com/bergerx/kubectl-status/pkg/plugin"
)

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

func InitAndExecute() {
	if err := newCmdStatus().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// This variable is populated by goreleaser
var version string

func newCmdStatus() *cobra.Command {
	options := plugin.NewOptions()
	cmd := &cobra.Command{
		Use:     "kubectl-status (TYPE[.VERSION][.GROUP] [NAME | -l label] | TYPE[.VERSION][.GROUP]/NAME ...) [flags]",
		Short:   "Display status for one or many resources",
		Long:    longCmdMessage,
		Example: examplesMessage,
		PreRun: func(cmd *cobra.Command, args []string) {
			viper.BindPFlags(cmd.Flags())
		},
		Run: func(cmd *cobra.Command, args []string) {
			klog.V(5).InfoS("running the cobra.Command ...")
			cmdutil.CheckErr(validate(options))
			cmdutil.CheckErr(plugin.Run(context.TODO(), options, args, os.Stdout))
		},
		Version: versionString(),
	}
	flags := cmd.Flags()
	initKlog(flags)
	options.AddFlags(flags)
	cobra.OnInitialize(viper.AutomaticEnv)
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	return cmd
}

func initKlog(flags *pflag.FlagSet) {
	// We Follow https://github.com/kubernetes/community/blob/master/contributors/devel/sig-instrumentation/logging.md
	// for the logs.
	fs := flag.NewFlagSet("", flag.PanicOnError)
	klog.InitFlags(fs)
	flags.AddGoFlagSet(fs)
}

// versionString returns the version prefixed by 'v'
// or an empty string if no version has been populated by goreleaser.
// In this case, the --version flag will not be added by cobra.
func versionString() string {
	if len(version) == 0 {
		return ""
	}
	return "v" + version
}

func validate(o *plugin.Options) error {
	klog.V(5).InfoS("Validating cli options...")
	filenames := *o.ResourceBuilderFlags.FileNameFlags.Filenames
	if o.RenderOptions.Local && len(filenames) == 0 {
		return fmt.Errorf("when using --local, --filename must be provided")
	}
	return nil
}
