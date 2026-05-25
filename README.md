# Example Resource Driver for Dynamic Resource Allocation (DRA)

This repository contains an example resource driver for use with the [Dynamic
Resource Allocation
(DRA)](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/)
feature of Kubernetes.

It is intended to demonstrate best-practices for how to construct a DRA
resource driver and wrap it in a [helm chart](https://helm.sh/). It can be used
as a starting point for implementing a driver for your own set of resources.

## Quickstart and Demo

Before diving into the details of how this example driver is constructed, it's
useful to run through a quick demo of it in action.

The driver itself provides access to a set of mock memory throughput devices, and this demo
walks through the process of building and installing the driver followed by
running a set of workloads that consume these memory throughput.

The procedure below has been tested and verified on both Linux and Mac.

### Prerequisites

* [GNU Make 3.81+](https://www.gnu.org/software/make/)
* [GNU Tar 1.34+](https://www.gnu.org/software/tar/)
* [docker v20.10+ (including buildx)](https://docs.docker.com/engine/install/) or [Podman v4.9+](https://podman.io/docs/installation)
* [kind v0.17.0+](https://kind.sigs.k8s.io/docs/user/quick-start/)
* [helm v3.7.0+](https://helm.sh/docs/intro/install/)
* [kubectl v1.18+](https://kubernetes.io/docs/reference/kubectl/)

### Demo
We start by first cloning this repository and `cd`ing into it. All of the
scripts and example Pod specs used in this demo are contained here, so take a
moment to browse through the various files and see what's available:
```
git clone https://github.com/kubernetes-sigs/dra-memory-driver.git
cd dra-memory-driver
```

**Note**: The scripts will automatically use either `docker`, or `podman` as the container tool command, whichever
can be found in the PATH. To override this behavior, set `CONTAINER_TOOL` environment variable either by calling
`export CONTAINER_TOOL=docker`, or by prepending `CONTAINER_TOOL=docker` to a script
(e.g. `CONTAINER_TOOL=docker ./path/to/script.sh`). Keep in mind that building Kind images currently requires Docker.

From here we will build the image for the example resource driver:
```bash
./demo/build-driver.sh
```

And create a `kind` cluster to run it in:
```bash
./demo/clusters/kind/create-cluster.sh
```

Once the cluster has been created successfully, double check everything is
coming up as expected:
```console
$ kubectl get pod -A
NAMESPACE            NAME                                                               READY   STATUS    RESTARTS   AGE
kube-system          coredns-5d78c9869d-6jrx9                                           1/1     Running   0          1m
kube-system          coredns-5d78c9869d-dpr8p                                           1/1     Running   0          1m
kube-system          etcd-dra-memory-driver-cluster-control-plane                      1/1     Running   0          1m
kube-system          kindnet-g88bv                                                      1/1     Running   0          1m
kube-system          kindnet-msp95                                                      1/1     Running   0          1m
kube-system          kube-apiserver-dra-memory-driver-cluster-control-plane            1/1     Running   0          1m
kube-system          kube-controller-manager-dra-memory-driver-cluster-control-plane   1/1     Running   0          1m
kube-system          kube-proxy-kgz4z                                                   1/1     Running   0          1m
kube-system          kube-proxy-x6fnd                                                   1/1     Running   0          1m
kube-system          kube-scheduler-dra-memory-driver-cluster-control-plane            1/1     Running   0          1m
local-path-storage   local-path-provisioner-7dbf974f64-9jmc7                            1/1     Running   0          1m
```

The validating admission webhook is disabled by default. To enable it, install cert-manager and its CRDs, then
set the `webhook.enabled=true` value when the dra-memory-driver chart is installed.
```bash
helm install \
  --repo https://charts.jetstack.io \
  --version v1.20.2 \
  --create-namespace \
  --namespace cert-manager \
  --wait \
  --set crds.enabled=true \
  cert-manager \
  cert-manager
```
More options for installing cert-manager can be found in [their docs](https://cert-manager.io/docs/installation/)

And then install the example resource driver via `helm`.
```bash
helm upgrade -i \
  --create-namespace \
  --namespace dra-memory-driver \
  dra-memory-driver \
  deployments/helm/dra-memory-driver
```

Double check the driver components have come up successfully:
```console
$ kubectl get pod -n dra-memory-driver
NAME                                                  READY   STATUS    RESTARTS   AGE
dra-memory-driver-kubeletplugin-qwmbl                1/1     Running   0          1m
```

And show the initial state of available Mmemory throughput on the worker node:
```
$ kubectl get resourceslice -o yaml
apiVersion: v1
items:
- apiVersion: resource.k8s.io/v1
  kind: ResourceSlice
  metadata:
    creationTimestamp: "2026-05-15T10:38:06Z"
    generateName: 00000-mem.example.com-dra-memory-driver-cluster-worker-
    generation: 1
    name: 00000-mem.example.com-dra-memory-driver-cluster-worker-jgchw
    ownerReferences:
    - apiVersion: v1
      controller: true
      kind: Node
      name: dra-memory-driver-cluster-worker
      uid: 6271d5f1-ce8e-4bf0-ad6e-18a544f110f9
    resourceVersion: "38388"
    uid: 30abf292-f0ad-4361-b8e7-839864983811
  spec:
    devices:
    - allowMultipleAllocations: true
      attributes:
        numa:
          int: 0
      capacity:
        mem:
          value: 10Gi
      name: numa-0
    - allowMultipleAllocations: true
      attributes:
        numa:
          int: 1
      capacity:
        mem:
          value: 20Gi
      name: numa-1
    - allowMultipleAllocations: true
      attributes:
        numa:
          int: 2
      capacity:
        mem:
          value: 30Gi
      name: numa-2
    - allowMultipleAllocations: true
      attributes:
        numa:
          int: 3
      capacity:
        mem:
          value: 40Gi
      name: numa-3
    driver: mem.example.com
    nodeName: dra-memory-driver-cluster-worker
    pool:
      generation: 1
      name: dra-memory-driver-cluster-worker
      resourceSliceCount: 1
kind: List
metadata:
  resourceVersion: ""
```

Next, deploy four example apps that demonstrate how `ResourceClaim`s,
`ResourceClaimTemplate`s, and custom objects can be used to
select and configure resources in various ways:
```bash
kubectl apply --filename=demo/basic-resourceclaimtemplate.yaml
```

And verify that they are coming up successfully:
```console
$ kubectl get pod -A
NAMESPACE                              NAME   READY   STATUS              RESTARTS   AGE
...
basic-resourceclaimtemplate            pod0   0/1     Pending             0          2s
...
```

Use your favorite editor to look through each of the `basic-*.yaml`
files and see what they are doing.

Then dump the logs of each app to verify that memory throughput were allocated to them
according to these semantics:
```bash
for ns in basic-resourceclaimtemplate; do \
  echo "${ns}:"
  for pod in $(kubectl get pod -n ${ns} --output=jsonpath='{.items[*].metadata.name}'); do \
    for ctr in $(kubectl get pod -n ${ns} ${pod} -o jsonpath='{.spec.containers[*].name}'); do \
      echo "${pod} ${ctr}:"
      kubectl logs -n ${ns} ${pod} -c ${ctr}| grep -E "MEM_DEVICE*"
    done
  done
  echo ""
done
```

This should produce output similar to the following:
```bash
basic-resourceclaimtemplate:
pod0 ctr0:
declare -x MEM_DEVICE_NUMA="0"
declare -x MEM_DEVICE_NUMA_0_RESOURCE_CLAIM="55a08c2e-abf0-4481-ac40-bedc5ee6ee90"
declare -x MEM_DEVICE_NUMA_0_THROUGHPUT_="1Gi"
pod1 ctr0:
declare -x MEM_DEVICE_NUMA="0"
declare -x MEM_DEVICE_NUMA_0_RESOURCE_CLAIM="a869a9e9-250b-4baa-a08d-8b649aaa94ac"
declare -x MEM_DEVICE_NUMA_0_THROUGHPUT_="1Gi"
```

### Clean Up

Once you have verified everything is running correctly, delete all of the
example apps:
```bash
kubectl delete --wait=false --filename=demo/basic-resourceclaimtemplate.yaml
```

And wait for them to terminate:
```console
$ kubectl get pod -A
NAMESPACE                              NAME   READY   STATUS        RESTARTS   AGE
...
basic-resourceclaimtemplate            pod0   1/1     Terminating   0          31m
basic-resourceclaimtemplate            pod1   1/1     Terminating   0          31m
...
```

Finally, you can run the following to cleanup your environment and delete the
`kind` cluster started previously:
```bash
./demo/clusters/kind/delete-cluster.sh
```

## Device Profiles

The example driver can manage several different kinds of devices to demonstrate
a variety of DRA features. The functionality for each kind of device is
organized into a "profile." Only one profile is active at a time for a given
instance of the example driver, though the example driver may be installed
multiple times in the same cluster with different active profiles. See the Helm
chart's `deviceProfile` value in values.yaml for available profiles.

For driver developers, this pattern is specific to the example driver and not
intended to be a recommendation for all DRA drivers. Other drivers will likely
be simpler by implementing their logic more directly than through an
abstraction like the example driver's profiles.

## Anatomy of a DRA resource driver

TBD

## Code Organization

TBD

## Best Practices

TBD

## References

For more information on the DRA Kubernetes feature and developing custom resource drivers, see the following resources:

* [Dynamic Resource Allocation in Kubernetes](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/)
* TBD

## Community, discussion, contribution, and support

Learn how to engage with the Kubernetes community on the [community page](http://kubernetes.io/community/).

You can reach the maintainers of this project at:

- [Slack](https://slack.k8s.io/)
- [Mailing List](https://groups.google.com/a/kubernetes.io/g/dev)

### Code of conduct

Participation in the Kubernetes community is governed by the [Kubernetes Code of Conduct](code-of-conduct.md).

[owners]: https://git.k8s.io/community/contributors/guide/owners.md
[Creative Commons 4.0]: https://git.k8s.io/website/LICENSE
