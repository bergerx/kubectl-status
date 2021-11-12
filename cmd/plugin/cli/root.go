package cli

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
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
	if err := RootCmd().Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

// This variable is populated by goreleaser
var version string

func RootCmd() *cobra.Command {
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
			cmdutil.CheckErr(plugin.Run(options, args))
		},
		Version: versionString(),
	}
	// We Follow https://github.com/kubernetes/community/blob/master/contributors/devel/sig-instrumentation/logging.md
	// for the logs.
	fs := flag.NewFlagSet("", flag.PanicOnError)
	klog.InitFlags(fs)
	defer klog.Flush()
	cmd.Flags().AddGoFlagSet(fs)
	options.AddFlags(cmd.Flags())
	cmd.Flags().BoolVar(&options.RenderOptions.Local, "local", false,
		"Run the template against the provided yaml manifest. Need to be used with a --filename parameter. No request to apiserver is done.")
	cmd.Flags().BoolVar(&options.RenderOptions.IncludeOwners, "include-owners", true,
		"Follow the ownerReferences in the objects and render them as well.")
	cmd.Flags().BoolVar(&options.RenderOptions.IncludeEvents, "include-events", true,
		"Include events in the output.")
	cmd.Flags().BoolVar(&options.RenderOptions.IncludeMatchingServices, "include-matching-services", true,
		"Include Services matching the Pod in the output.")
	cmd.Flags().BoolVar(&options.RenderOptions.IncludeMatchingIngresses, "include-matching-ingresses", true,
		"Include Ingresses referencing the Service in the output.")
	cmd.Flags().BoolVar(&options.RenderOptions.IncludeApplicationDetails, "include-application-details", true,
		"This will include well known application metadata into the output.")
	cmd.Flags().BoolVar(&options.RenderOptions.IncludeRolloutDiffs, "include-rollout-diffs", false,
		"Include unified diff between stored revisions of Deployment, DaemonSet and StatefulSets.")
	cmd.Flags().BoolVar(&options.RenderOptions.Shallow, "shallow", false,
		"Render only the immediate object and disable all other --include-* flags. This will override any other flags.")
	cobra.OnInitialize(viper.AutomaticEnv)
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	return cmd
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
