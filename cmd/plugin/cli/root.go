package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/kubectl/pkg/cmd/util"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"

	"github.com/bergerx/kubectl-status/pkg/plugin"
)

var (
	longCmdMessage = `Display status for one or many resources

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

`
)

func InitAndExecute() {
	if err := RootCmd().Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func RootCmd() *cobra.Command {
	clientGetter := genericclioptions.NewConfigFlags(false)
	cmd := &cobra.Command{
		Use:   "kubectl-status (TYPE[.VERSION][.GROUP] [NAME | -l label] | TYPE[.VERSION][.GROUP]/NAME ...) [flags]",
		Short: "Display status for one or many resources",
		Long:  longCmdMessage,
		PreRun: func(cmd *cobra.Command, args []string) {
			viper.BindPFlags(cmd.Flags())
		},
		Run: func(cmd *cobra.Command, args []string) {
			cmdutil.CheckErr(run(clientGetter, cmd, args))
		},
	}
	clientGetter.AddFlags(cmd.Flags())
	genericclioptions.
		NewResourceBuilderFlags().
		WithAll(false).
		WithAllNamespaces(false).
		WithFile(false).
		WithLabelSelector("").
		WithFieldSelector("").
		WithLatest().
		AddFlags(cmd.Flags())
	cmd.Flags().BoolP("test", "t", false,
		"run the template against the provided yaml manifest. Need to be used with a --filename parameter. No request to apiserver is done.")

	cobra.OnInitialize(viper.AutomaticEnv)
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	return cmd
}

// Complete takes the command arguments and factory and infers any remaining options.
func run(clientGetter *genericclioptions.ConfigFlags, cmd *cobra.Command, args []string) error {
	if err := runPlugin(clientGetter, cmd, args); err != nil {
		return cmdutil.UsageErrorf(cmd, err.Error())
	}
	return nil
}

func runPlugin(clientGetter *genericclioptions.ConfigFlags, cmd *cobra.Command, args []string) error {
	filenames := util.GetFlagStringSlice(cmd, "filename")
	if util.GetFlagBool(cmd, "test") {
		return runAgainstFile(filenames)
	}
	return runAgainstCluster(clientGetter, cmd, args, filenames)
}

func runAgainstFile(filenames []string) error {
	if len(filenames) != 1 {
		return errors.New("when using --test, exactly one --filename must be provided")
	}
	filename := filenames[0]
	out, err := plugin.RenderFile(filename)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func runAgainstCluster(clientGetter *genericclioptions.ConfigFlags, cmd *cobra.Command, args []string, filenames []string) error {
	q, err := GetResourceStatusQuery(clientGetter, cmd, args, filenames)
	if err != nil {
		return err
	}
	return utilerrors.NewAggregate(q.PrintRenderedQueriedResources())
}

func GetResourceStatusQuery(clientGetter *genericclioptions.ConfigFlags, cmd *cobra.Command, args []string, filenames []string) (*plugin.ResourceStatusQuery, error) {
	allNamespaces := util.GetFlagBool(cmd, "all-namespaces")
	namespace, enforceNamespace, err := clientGetter.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return nil, errors.WithMessage(err, "Failed getting namespace")
	}
	selector := util.GetFlagString(cmd, "selector")
	fieldSelector := util.GetFlagString(cmd, "field-selector")
	q := plugin.NewResourceStatusQuery(
		clientGetter,
		namespace,
		allNamespaces,
		enforceNamespace,
		filenames,
		selector,
		fieldSelector,
		args,
	)

	return q, nil
}
