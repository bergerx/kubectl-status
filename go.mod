module github.com/bergerx/kubectl-status

go 1.12

replace github.com/gorilla/rpc v1.2.0+incompatible => github.com/gorilla/rpc v1.2.0 // https://github.com/gorilla/rpc/issues/65#issuecomment-518834577

require (
	github.com/Masterminds/sprig/v3 v3.0.2
	github.com/armon/consul-api v0.0.0-20180202201655-eb2c6b5be1b6 // indirect
	github.com/coreos/go-etcd v2.0.0+incompatible // indirect
	github.com/cpuguy83/go-md2man v1.0.10 // indirect
	github.com/dustin/go-humanize v1.0.0
	github.com/fatih/color v1.9.0
	github.com/gophercloud/gophercloud v0.1.0 // indirect
	github.com/pkg/errors v0.9.1
	github.com/pmezard/go-difflib v1.0.0
	github.com/rakyll/statik v0.1.7
	github.com/spf13/cobra v1.1.1
	github.com/spf13/viper v1.7.0
	github.com/ugorji/go v1.1.4 // indirect
	github.com/ugorji/go/codec v0.0.0-20181204163529-d75b2dcb6bc8 // indirect
	github.com/xordataexchange/crypt v0.0.3-0.20170626215501-b2862e3d0a77 // indirect
	gonum.org/v1/netlib v0.0.0-20190331212654-76723241ea4e // indirect
	gopkg.in/check.v1 v1.0.0-20190902080502-41f04d3bba15 // indirect
	k8s.io/api v0.20.0
	k8s.io/apimachinery v0.20.0
	k8s.io/cli-runtime v0.20.0
	k8s.io/client-go v0.20.0
	k8s.io/klog v1.0.0 // indirect
	k8s.io/kubectl v0.20.0
	k8s.io/metrics v0.20.0
	sigs.k8s.io/structured-merge-diff v0.0.0-20190525122527-15d366b2352e // indirect
	sigs.k8s.io/yaml v1.2.0
)
