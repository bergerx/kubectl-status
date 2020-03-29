package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/bergerx/kubectl-status/pkg/plugin"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
)

func RootCmd() *cobra.Command {
	KubernetesConfigFlags := genericclioptions.
		NewConfigFlags(false)
	ResourceBuilderFlags := genericclioptions.
		NewResourceBuilderFlags().
		WithAll(false).
		WithAllNamespaces(false).
		WithFile(false).
		WithLabelSelector("").
		WithFieldSelector("").
		WithLatest()

	f := cmdutil.NewFactory(KubernetesConfigFlags)

	cmd := &cobra.Command{
		Use:   "kubectl-status (TYPE[.VERSION][.GROUP] [NAME | -l label] | TYPE[.VERSION][.GROUP]/NAME ...) [flags]",
		Short: "Display status for one or many resources",
		Long: `Display status for one or many resources

 Prints human-friendly output that focuses on the status of the resources in kubernetes.

 In most cases replacing a "kubectl get ..." with a "kubectl status ..." would be sufficient.

 This plugin uses templates for well known api-conventions and has support for hardcoded resources,
not all resources are fully supported.

Examples:
  # Show status of all pods in the current namespace
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

`,
		PreRun: func(cmd *cobra.Command, args []string) {
			viper.BindPFlags(cmd.Flags())
		},
		Run: func(cmd *cobra.Command, args []string) {
			cmdutil.CheckErr(Run(f, cmd, args))
		},
	}
	KubernetesConfigFlags.AddFlags(cmd.Flags())
	ResourceBuilderFlags.AddFlags(cmd.Flags())
	var x bool
	cmd.Flags().BoolVarP(&x, "test", "t", false, "Run the template against the provided yaml manifest. Need to be used with a --filename parameter. No request to apiserver is done.")

	cobra.OnInitialize(viper.AutomaticEnv)
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	return cmd
}

// Complete takes the command arguments and factory and infers any remaining options.
func Run(f cmdutil.Factory, cmd *cobra.Command, args []string) error {
	if err := plugin.RunPlugin(f, cmd, args); err != nil {
		return cmdutil.UsageErrorf(cmd, err.Error())
	}
	return nil
}

func InitAndExecute() {
	if err := RootCmd().Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
