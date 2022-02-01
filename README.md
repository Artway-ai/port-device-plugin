# Port-Device-Plugin
A Kubernetes device plugin to allocate host port.

It is

* HostPort allocator
* Kubernetes device plugin

## Why this ?

While kubernetes provide podip/service to handle network staff, hostnetwork is favorable for performance critical scenario. 
For example, deep learning training job requires the capability of RDMA to accelerate the training process which depends on the hostnetwork.
To solve the conflict of ports in the same host, I propose this implementation to manager the ports.

## Goals

* allocate FREE ports for pods

## Non Goals

* allocate preset port while can be done directly

## Usage

Declare resource requirement `github.com/port: 2` in the resources section of Pod.

```
apiVersion: v1
kind: Pod
metadata:
  generateName: port-
spec:
  restartPolicy: OnFailure
  containers:
  - image: centos:7
    name: pod
    command: ["sleep"]
    args: ["1000"]
    resources:
      limits:
        github.com/port: 2
```

Then `2` free ports will be allocated/assigned to the pod with environ
```
HOST_PORTS=8005,8004
```
and directory
```
/var/run/port-container-devices/8004
/var/run/port-container-devices/8005
```

## Deployment

Modify launch args if needed,

```
args: ["--min", "8000", "--max", "9000"]
```

Then install directly

```
kuebctl apply -f deploy/deploy.yaml
```

## Further consideration

Some consideration list below,

* available port may be checked by importing docker client, which is expensive without introducing any stability for the result
* a list of contiguous ports can be allocated by implementing `GetPreferredAllocation`, no strong argument for me to prefer contiguous ports then randomized ones

