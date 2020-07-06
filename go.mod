module github.com/tamalsaha/k-apply

go 1.14

require (
	github.com/appscode/go v0.0.0-20200323182826-54e98e09185a
	github.com/jonboulle/clockwork v0.2.0
	github.com/spf13/cobra v1.0.0
	k8s.io/api v0.18.5
	k8s.io/apimachinery v0.18.5
	k8s.io/cli-runtime v0.18.5
	k8s.io/client-go v0.18.5
	k8s.io/component-base v0.18.5
	k8s.io/klog v1.0.0
	k8s.io/kube-openapi v0.0.0-20200410145947-61e04a5be9a6
	k8s.io/kubectl v0.18.5
	k8s.io/utils v0.0.0-20200324210504-a9aa75ae1b89
	kmodules.xyz/client-go v0.0.0-20200630053911-20d035822d35
)

replace github.com/googleapis/gnostic => github.com/googleapis/gnostic v0.2.0

replace k8s.io/utils => k8s.io/utils v0.0.0-20200324210504-a9aa75ae1b89

replace k8s.io/kube-openapi => k8s.io/kube-openapi v0.0.0-20200410145947-61e04a5be9a6
