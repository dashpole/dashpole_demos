This uses [Kustomize](https://github.com/kubernetes-sigs/kustomize) to manage
kubernetes yaml files.

## Usage

### Deploy the basic jaeger all-in-one
```
kubectl kustomize base | kubectl apply -f -
```

## Accessing the UI
```
# get the query service
kubectl get svc -n jaeger query -o jsonpath="{.status.loadBalancer.ingress[0].ip}"
```
Navigate to that IP in your browser
