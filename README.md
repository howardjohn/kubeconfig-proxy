# kubeconfig-proxy

`kubeconfig-proxy` is a small tool to speed up `kubectl` when connecting to remote clusters.
Because it is a stateless CLI tool, each `kubectl` invocation requires a full roundtrip TLS handshake to the API server.
For local usage this is no problem, but for remote clusters like EKS/GKE/etc, these often are >1s.

`kubeconfig-proxy` rewrites a `kubeconfig` to instead point to a local persistent server (exposed over `localhost:64443`) which proxies
to the original API server, maintaining persistent connections to it.

This results in dramatic improvements in latency.
Below shows an example, connecting to an EKS cluster, showing a 7x improvement.
This is to a nearby region which has ~40ms latency -- the impact may be greater for more remote regions.

```shell
$ hyperfine 'kubectl get pods --context eks' 'kubectl get pods --context eks-kubeconfig-proxy'
Benchmark 1: kubectl get pods --context eks
  Time (mean ± σ):     763.0 ms ±  24.8 ms    [User: 168.1 ms, System: 45.6 ms]
  Range (min … max):   734.0 ms … 818.9 ms    10 runs

Benchmark 2: kubectl get pods --context eks-kubeconfig-proxy
  Time (mean ± σ):     103.6 ms ±   6.2 ms    [User: 94.1 ms, System: 21.8 ms]
  Range (min … max):    93.8 ms … 124.3 ms    30 runs

Summary
  kubectl get pods --context eks-kubeconfig-proxy ran
    7.36 ± 0.50 times faster than kubectl get pods --context eks
```

## Usage

First, run the server:

```shell
$ kubeconfig-proxy server
```

Then