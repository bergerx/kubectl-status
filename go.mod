module github.com/bergerx/kubectl-resource-status

go 1.12

require (
	cloud.google.com/go v0.37.2 // indirect
	github.com/evanphx/json-patch v4.5.0+incompatible // indirect
	github.com/fatih/color v1.7.0
	github.com/google/gofuzz v1.0.0 // indirect
	github.com/googleapis/gnostic v0.3.1 // indirect
	github.com/imdario/mergo v0.3.7 // indirect
	github.com/json-iterator/go v1.1.6 // indirect
	github.com/mailru/easyjson v0.0.0-20180730094502-03f2033d19d5 // indirect
	github.com/mattn/go-colorable v0.1.1 // indirect
	github.com/mattn/go-isatty v0.0.7 // indirect
	github.com/modern-go/reflect2 v1.0.1 // indirect
	github.com/onsi/ginkgo v1.8.0 // indirect
	github.com/onsi/gomega v1.5.0 // indirect
	github.com/peterbourgon/diskv v2.0.1+incompatible // indirect
	github.com/pkg/errors v0.8.1
	github.com/spf13/cobra v0.0.4
	github.com/spf13/viper v1.4.0
	github.com/tj/go-spin v1.1.0
	golang.org/x/crypto v0.0.0-20190701094942-4def268fd1a4 // indirect
	golang.org/x/net v0.0.0-20190812203447-cdfb69ac37fc // indirect
	golang.org/x/sys v0.0.0-20190804053845-51ab0e2deafa // indirect
	golang.org/x/text v0.3.2 // indirect
	k8s.io/api v0.0.0-20190313235455-40a48860b5ab // indirect
	k8s.io/apimachinery v0.0.0-20190313205120-d7deff9243b1
	k8s.io/cli-runtime v0.0.0-20190314001948-2899ed30580f
	k8s.io/client-go v11.0.0+incompatible
	k8s.io/klog v0.4.0 // indirect
	k8s.io/kube-openapi v0.0.0-20190816220812-743ec37842bf // indirect
	k8s.io/utils v0.0.0-20190809000727-6c36bc71fc4a // indirect
	sigs.k8s.io/kustomize v2.0.3+incompatible // indirect
)

replace github.com/gorilla/rpc v1.2.0+incompatible => github.com/gorilla/rpc v1.2.0 // https://github.com/gorilla/rpc/issues/65#issuecomment-518834577
