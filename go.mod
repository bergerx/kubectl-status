module github.com/bergerx/kubectl-status

go 1.12

require (
	github.com/Masterminds/sprig/v3 v3.2.2
	github.com/dustin/go-humanize v1.0.0
	github.com/fatih/color v1.13.0
	github.com/pkg/errors v0.9.1
	github.com/pmezard/go-difflib v1.0.0
	github.com/rakyll/statik v0.1.7
	github.com/spf13/cast v1.4.1
	github.com/spf13/cobra v1.4.0
	github.com/spf13/pflag v1.0.5
	github.com/spf13/viper v1.11.0
	k8s.io/api v0.23.6
	k8s.io/apimachinery v0.23.6
	k8s.io/cli-runtime v0.23.5
	k8s.io/client-go v0.23.5
	k8s.io/klog/v2 v2.60.1
	k8s.io/kubectl v0.23.5
	k8s.io/utils v0.0.0-20220210201930-3a6ce19ff2f9 // indirect
	sigs.k8s.io/cli-utils v0.29.4
	sigs.k8s.io/yaml v1.3.0
)
