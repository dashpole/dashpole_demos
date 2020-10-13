# dashpole_demos

A collection of my personal demonstration examples.

See [`jaeger/`](https://github.com/dashpole/dashpole_demos/tree/master/jaeger) for a simple jaeger deployment.


See [`zipkin/`](https://github.com/dashpole/dashpole_demos/tree/master/zipkin) for a simple zipkin deployment.


See [`prometheus/`](https://github.com/dashpole/dashpole_demos/tree/master/prometheus) for a simple prometheus server deployment.


See [`otel/`](https://github.com/dashpole/dashpole_demos/tree/master/otel) for all opentelemetry deployments.


Recommended reading: See [`otel/agent/`](https://github.com/dashpole/dashpole_demos/tree/master/otel/agent) for descriptions of each method of deploying the agent.


See [`tracing_app/`](https://github.com/dashpole/dashpole_demos/tree/master/tracing_app) for an example application, written in Go, which can be configured to send traces a variety of ways.The container images will have to be rebuilt, pushed to your own repo, and updated in the [base manifest](https://github.com/dashpole/dashpole_demos/tree/master/otel/tracing_app/deploy/base/deployment.yaml) in order for these demos to work in your cluster.