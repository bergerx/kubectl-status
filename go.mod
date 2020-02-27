module github.com/bergerx/kubectl-resource-status

go 1.12

replace github.com/gorilla/rpc v1.2.0+incompatible => github.com/gorilla/rpc v1.2.0 // https://github.com/gorilla/rpc/issues/65#issuecomment-518834577

require (
	github.com/Masterminds/sprig/v3 v3.0.2
	github.com/fatih/color v1.9.0
	github.com/spf13/cobra v0.0.6
	github.com/spf13/viper v1.6.2
	golang.org/x/sys v0.0.0-20200223170610-d5e6a3e2c0ae
	k8s.io/apimachinery v0.17.3
	k8s.io/cli-runtime v0.17.3
	k8s.io/client-go v0.17.3
	k8s.io/kubectl v0.17.3
)
