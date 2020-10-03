This uses [Kustomize](https://github.com/kubernetes-sigs/kustomize) to manage
kubernetes yaml files.

## Usage

### Before you begin, deploy the opentelemetry namespace
```
kubectl kustomize common/base | kubectl create -f -
```

### When you are all done, delete the namespace
```
kubectl delete ns opentelemetry
```

### Deploy the collector with the stdout exporter
```
kubectl kustomize collector/base | kubectl apply -f -
```

### Deploy just the agent daemonset with stdout exporter
```
kubectl kustomize agent/base | kubectl apply -f -
```

### Deploy change the agent to send to the collector, and use listen on host network
```
kubectl kustomize agent/overlays/hostnetwork | kubectl apply -f -
```

### Deploy change the agent to send to the collector, and use a service that requires node-local traffic routing

Note: This requires [enabling Kubernetes service topology](https://kubernetes.io/docs/tasks/administer-cluster/enabling-service-topology/).

```
kubectl kustomize agent/overlays/nodelocal | kubectl apply -f -
```

### Change the collector to send to jaeger
```
kubectl kustomize collector/overlays/jaeger | kubectl apply -f -
```

### Change the collector to send to zipkin
```
kubectl kustomize collector/overlays/zipkin | kubectl apply -f -
```

