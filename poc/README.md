### Agent Connection Proof of Concept

This branch contains a PoC for NGF connecting to and sending config to a separate nginx instance, managed by nginx-agent.

A few things to note about this PoC:
- only for OSS
- only supports a single nginx instance
- no resilience through restarts or broken connections
- NGF must be installed first, then the agent
- rough code, just to get things working

To build the agent v3 binary:

```shell
git clone git@github.com:nginx/agent.git
git checkout v3
GOARCH=<your-arch> GOOS=linux make build
```

Copy the built binary into `nginx-gateway-fabric/build` directory.

To build the agent/nginx image, run:

```makefile
make build-oss-agent-image
```

This will create the `nginx-agent:latest` image that you can load to your kind cluster.

Build, load, and install NGF from this branch. Then you can deploy `nginx.yaml` which will create the data plane Deployment. From there, you should be able to create your Gateway resources and see the config show up in the agent.

Known issues:
- agent doesn't yet support reading stub_status metrics via a unix socket
- agent doesn't yet support a way for control plane to request current file state (this is needed for when control plane restarts)
- agent needs to add hostname to ContainerInfo when connecting so we know which agent has connected
- other various notes have been added as comments in relevant parts of the code relating to implementation that we'll need when we build this for real


Extra notes for implementation:
- likely need to have all nginx instances report metrics back to control plane, which is scraped by prometheus
  - this is because agent only sends metrics to a configured endpoint; whereas if we want prometheus to scrape automatically, we'd need agent to have the prometheus exporter
- whenever NGF sees an nginx instance become Ready, we send its config to it (whether a new or restarted pod, doesn't matter)
  - some identifier like label/annotation that tells us that we own that instance
  - another identifier that links that instance to its Gateway resource. This allows us to build the correct config for it from the Graph
- if a single nginx Deployment is scaled, we should ensure that all instances for that Deployment receive the same config (and aren't treated as "different")
- if no nginx instances exist, don't send any config (not even base config)
- NGF should check if a connection exists first before sending the config (all replicas, even non-leaders, should be able to send the config because the agent may connect to a non-leader)
- Our eventHandler (or graph or something) will need to keep track of all agent Pods so that we can properly map nginx config to the appropriate Pod, as well as to the appropriate agent grpc connection, which should include the hostname (Pod name) of the connecting agent
