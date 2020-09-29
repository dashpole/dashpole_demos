This uses [Kustomize](https://github.com/kubernetes-sigs/kustomize) to manage
kubernetes yaml files.

## Usage

### Before you begin, deploy the opentelemetry namespace
```
kustomize build common/base | kubectl create -f -
```

### When you are all done, delete the namespace
```
kubectl delete ns opentelemetry
```

### Deploy just the agent daemonset with stdout exporter
```
kustomize build agent/base | kubectl apply -f -
```

### Deploy the collector with the stdout exporter
```
kustomize build collector/base | kubectl apply -f -
```

### Deploy change the agent to send to the collector
```
kustomize build agent/overlays/collector | kubectl apply -f -
```

### Change the collector to send to jaeger
```
kustomize build collector/overlays/jaeger | kubectl apply -f -
```

