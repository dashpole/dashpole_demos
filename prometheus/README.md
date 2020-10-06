This uses [Kustomize](https://github.com/kubernetes-sigs/kustomize) to manage
kubernetes yaml files.

## Usage

### Deploy the basic prometheus server
```
kubectl kustomize base | kubectl apply -f -
```

## Accessing the UI
```
# get the query service
kubectl get svc -n prometheus prometheus -o jsonpath="{.status.loadBalancer.ingress[0].ip}"
```
Navigate to <IP>:9090 in your browser
