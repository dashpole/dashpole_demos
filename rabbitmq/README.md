# Running 

Requires configconnector to be enabled.

Install the rabbitmq operator:
```sh
kubectl apply -f https://github.com/rabbitmq/cluster-operator/releases/latest/download/cluster-operator.yml
```

Install monitoring and dashboard artifacts:
```sh
kubectl kustomize | kubectl apply  -f -
```

To generate data in the graphs: 
```sh
username="$(kubectl get secret rabbitmq-default-user -o jsonpath='{.data.username}' | base64 --decode)"
password="$(kubectl get secret rabbitmq-default-user -o jsonpath='{.data.password}' | base64 --decode)"
service="$(kubectl get service rabbitmq jsonpath='{.spec.clusterIP}')"
kubectl run perf-test --image=pivotalrabbitmq/perf-test -- --uri amqp://$username:$password@$service
```
