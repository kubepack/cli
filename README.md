# cli

```console
kubectl create -f https://raw.githubusercontent.com/kubepack/application/k-1.18.3/config/crd/bases/app.k8s.io_applications.v1.yaml
export HELM_DRIVER=apps
```

```console
$ kubectl pack apply-chart he11 kubepack-bundles/hello --set autoscaling.enabled=true

$ kubectl get applications
```
