# Getting self-observability metrics

```bash
kubectl get --raw /api/v1/namespaces/opentelemetry/pods/otel-collector-0:8888/proxy/metrics
```