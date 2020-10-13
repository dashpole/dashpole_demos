# The OpenTelemetry Collector

In general, running the OpenTelemetry Collector as a kubernetes daemonset or sidecar container is referred to as an "Agent".  The goal of using an Agent is to offload telemetry as quickly as possible from your application so that telemetry is not lost if the application crashes or fails.  

## FAQ

### Why do we need to have an "Agent" deployment?

The centralized collector has to aggregate telemetry from all sources in the cluster.  As such, it can easily become a performance bottleneck when ingesting telemetry, and is optimized for throughput.  This means clients should generally send larger, infrequent batches of telemetry to the central collector service.  This is incompatible with our goal of offloading telemetry from the application as quickly as possible.  Using the Agent allows applications to send small, frequent batches of telemetry to the Agent, which can aggregate them into larger batches to send to the collector.

### What should "most" things running in Kubernetes clusters do today (kubernetes 1.19)?

There are two viable options today: The sidecar approach, and sending to a hostnetwork daemonset through the host IP.


The sidecar approach involves running an OpenTelemetry Agent container in each pod that emits telemetry.  See [`tracing_app/deploy/overlays/sidecar`](https://github.com/dashpole/dashpole_demos/tree/master/tracing_app/deploy/overlays/sidecar) for an example patch and configuration for adding a sidecar container to a deployment.  Sidecars can be expensive, and ties the deployment of your telemetry pipeline to the application, as they are located in the same pod.


The host IP approach involves running an agent on each node, and listening on the host's 55680 port.  This can be done either by using host network (see [`otel/agent/overlays/hostnetwork`](https://github.com/dashpole/dashpole_demos/tree/master/otel/agent/overlays/hostnetwork)), or by using a host port (see [`otel/agent/overlays/hostport`](https://github.com/dashpole/dashpole_demos/tree/master/otel/agent/overlays/hostport)).  However, unlike using the sidecar approach, your application must add additional configuration to detect the host's IP, and configure the application to send telemetry there.  See [`tracing_app/deploy/overlays/hostip`](https://github.com/dashpole/dashpole_demos/tree/master/tracing_app/deploy/overlays/hostip) for an example of how this could be done.

### Sidecar vs HostIP

The sidecar approach is generally simpler, as it doesn't require changes to the application.  It is also easier to size CPU and memory of the sidecar container to your applications' needs.


The HostIP approach provides better separation of concerns, and can make it easier for different teams to manage the application and the telemetry infrastructure.  It can be difficult to correctly size daemonsets, as telemetry output isn't neccessarily evenly spread across nodes in a cluster.

### When should I use the node local service?

Sending telemetry to a Node Local agent is generally the best way to run the agent with one giant caveat: **It isn't useable yet**.  Often, there are different teams managing the application and the telemetry pipeline.  With a node local service and daemonset, the telemetry team doesn't need to manage containers in the application team's pods, and can perform rollouts independent of the application pods.  It also still has the benifit of routing traffic locally, instead of to agent pods that could be on other nodes which would introduce additional failure modes and generate additional cross-node traffic.


The node local service makes use of the [Service Topology](https://kubernetes.io/docs/concepts/services-networking/service-topology/) kubernetes feature.  It allows you to ensure traffic sent to the agent service is routed only to the local agent, and not to agents on other nodes.  However, [Enabling the Service Topology Feature](https://kubernetes.io/docs/tasks/administer-cluster/enabling-service-topology/) is quite difficult, and at the time of writing is **not available in most hosted kubernetes offerings**.


Note that this API is still evolving.  Sig-Network seems likely to replace the `topologyKeys` api: https://github.com/kubernetes/enhancements/issues/2086 with something more targeted at node-local use-cases, which is currently planned to be alpha in 1.21.

### When using node local, should I prefer local, or require local?

One use-case for the agent is enriching telemetry with metadata about the host on which it runs, such as adding hostname.  If you are doing this, you should require local routing so that the correct hostname is attached to telemetry coming from that host.  However, if you are not adding metadata, you can take advantage of the additional redundancy offered by being able to fall back to other nodes in the cluster if the local agent is not available.

### When should I use hostnetwork?

Using hostnetwork on the agent and application allows the application to send traces directly to the agent on localhost.  However, using host network is generally not recommended in Kubernetes, as it means container ports can conflict and cause pods to fail to start.  In general, **If you are not already using hostnetwork for your application, you should not add it to make sending telemetry easier**.

### What about NodePort serivces?  Won't that do the same thing as node local?

A NodePort service is a service in which a port on the external IP of each node in the cluster routes traffic to a clusterIP service.  You should only use NodePort services to expose a service outside of the cluster, which we generally don't want to do with the agent.  If you manage to configure your application to send traffic to the nodeport service on your local node, this is **worse** than just sending traffic to a regular clusterIP service.
