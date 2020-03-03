package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/bergerx/kubectl-resource-status/pkg/plugin"
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
		WithLatest()

	f := cmdutil.NewFactory(KubernetesConfigFlags)

	cmd := &cobra.Command{
		Use: "TODO-BINARY-NAME (TYPE[.VERSION][.GROUP] [NAME | -l label] | TYPE[.VERSION][.GROUP]/NAME ...) [flags]",
		//DisableFlagsInUseLine: true,
		Short:         "Display resource-status for one or many resources",
		Long:          `.`,
		SilenceErrors: true,
		SilenceUsage:  true,
		PreRun: func(cmd *cobra.Command, args []string) {
			viper.BindPFlags(cmd.Flags())
		},
		Run: func(cmd *cobra.Command, args []string) {
			cmdutil.CheckErr(Run(f, cmd, args))
		},
	}
	KubernetesConfigFlags.AddFlags(cmd.Flags())
	ResourceBuilderFlags.AddFlags(cmd.Flags())

	cobra.OnInitialize(initConfig)
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

func initConfig() {
	viper.AutomaticEnv()
}
