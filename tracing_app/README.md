This uses [Kustomize](https://github.com/kubernetes-sigs/kustomize) to manage
kubernetes yaml files.

## Before you begin: building and pushing the demo App

```
REPO=FOO
docker build . -t $REPO/tracing_demo:latest
docker push $REPO/tracing_demo:latest
```

update the image in deploy/base/deployment.yaml to use your image.

## Deploying the demo app

### Send traces directly to the collector 
```
kubectl kustomize overlays/collector-service | kubectl apply -f -
```

### Send traces to a sidecar container, which forwards to the collector
```
kubectl kustomize overlays/sidecar | kubectl apply -f -
```

### Run the application on the host network, and sent traces to the local agent
```
kubectl kustomize overlays/hostnetwork-agent | kubectl apply -f -
```

### Send traces to the agent service, which can run with node local service topology
```
kubectl kustomize overlays/agent-service | kubectl apply -f -
```

## Triggering trace collection by querying the demo app
```
SVC=$(kubectl get svc -n demo demo -o jsonpath="{.status.loadBalancer.ingress[0].ip}")
curl $SVC:80/
```