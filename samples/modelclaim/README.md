# ModelClaim high-density runtime pool

These examples show two `ModelClaim` objects attached to one warm GPU runtime
pool. Each claim is started by the `aibrix-runtime` sidecar as a separate
kvcached-enabled engine process; the gateway routes by the served model name.

```bash
kubectl apply -f samples/modelclaim/warm-runtime-pool.yaml
kubectl apply -f samples/modelclaim/modelclaims.yaml

kubectl get modelclaims -w
kubectl get pods -l pool.aibrix.ai/name=b300-pool-a -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.metadata.annotations}{"\n"}{end}'
```

The controller writes `modelclaim.aibrix.ai/<claim-name>` annotations with
`{"model":"<served-name>","port":<engine-port>}`. `port:0` means the claim is
known but not yet routable, so the gateway waits briefly and then returns a
retryable `503` if it does not become routable in time.
