This uses [Kustomize](https://github.com/kubernetes-sigs/kustomize) to manage
kubernetes yaml files.

## Usage

```
kustomize build base | kubectl apply -f -
```
