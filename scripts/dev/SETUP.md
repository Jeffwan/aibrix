# Aibrix Metadata + Batch — Local Test Setups

Two complete setups for testing the Aibrix metadata server and batch backend
**without Docker**. Pick the one that matches what you're testing.

| | Setup A (local) | Setup B (Kubernetes) |
|---|---|---|
| Where it runs | OS processes | K8s cluster |
| Redis | `redis-server` process | redis pod (`config/metadata/redis.yaml`) |
| Object storage | local filesystem (`./.storage`) | minio pod |
| Inference engine | `fake_inference.py` process | `fake-vllm` pod |
| Metadata server | python process | metadata-service pod |
| Batch worker | in-process (BatchDriver standalone mode) | real K8s `Job` per batch |
| Tests pod / job lifecycle? | NO | YES |
| Tests OpenAI Files / Batches APIs? | YES | YES |
| Setup time | ~10s | ~60s |

The same OpenAI SDK e2e test (`tests/e2e/test_openai_sdk_batch.py`) and the
same Postman collection (`scripts/dev/aibrix-metadata.postman_collection.json`)
work against both — they only need `http://127.0.0.1:8090`.

---

## Prerequisites

```bash
# Python deps for the metadata server + tests
cd python/aibrix && pip install -e . && pip install openai pytest pytest-asyncio

# Setup A only:
sudo apt install redis-server   # or: brew install redis

# Setup B only:
# A working cluster (kind / minikube / EKS / GKE / k3d) + kubectl context
```

---

## Setup A — local processes (no Kubernetes)

### Start everything

```bash
./scripts/dev/start-local.sh
```

This launches:

1. `redis-server` on `:6379` (PID file `.run/redis.pid`)
2. Fake inference server on `:8000` (PID file `.run/fake_vllm.pid`)
3. Metadata server on `:8090` (PID file `.run/metadata.pid`)

All logs go to `.run/logs/{redis,fake_vllm,metadata}.log`.

### Observation checkpoints

| Step | What to observe | Healthy state |
|---|---|---|
| 1 redis | `redis-cli -p 6379 ping` | `PONG` |
| 2 fake vllm | `curl localhost:8000/health` | `{"status":"ok"}` |
| 2 fake vllm | `curl -s -X POST localhost:8000/v1/chat/completions -d '{"model":"x","messages":[]}' -H 'Content-Type: application/json'` | choices array returned |
| 3 metadata up | `curl localhost:8090/healthz` | `{"status":"ok"}` |
| 3 metadata redis | `curl localhost:8090/readyz` | `{"status":"ready"}` (proves Redis reachable) |
| 3 metadata wiring | `curl -s localhost:8090/status \| jq` | `batch_driver.available=true`, `kopf_operator.available=false` |
| 3 metadata logs | `tail -F .run/logs/metadata.log` | line `[metadata] inference endpoint -> http://127.0.0.1:8000` |
| jobs flowing | `tail -F .run/logs/metadata.log` after submitting a batch | `BatchJobState` transitions: `CREATED -> IN_PROGRESS -> FINALIZING -> COMPLETED` |
| storage on disk | `find .storage -type f \| head` | `*.jsonl` input + output files appear under bucket-style paths |
| redis state | `redis-cli -p 6379 keys '*'` | `aibrix:*` keys (metastore locks, user CRUD) |
| inference reaching fake | `tail -F .run/logs/fake_vllm.log` while batch runs | one log line per batch row |

### Stop / status

```bash
./scripts/dev/start-local.sh status
./scripts/dev/start-local.sh stop
```

### Limitations of Setup A

- **No K8s Job is rendered or scheduled.** Anything related to
  `JobManifestRenderer`, ConfigMap-backed templates, kopf operator, or pod
  status is bypassed (`batch_driver.kopf_operator.available=false`).
- `BatchJobStore` is disabled (`AIBRIX_BATCH_JOB_STORE_ENABLED=0`); jobs live in
  the in-memory `JobManager` pool. They vanish on restart — that's expected.
- The `/v1/models` endpoint will return an empty list (no K8s pods to discover).
- The legacy users endpoints (`POST /CreateUser` etc.) work because they only
  need Redis.

If any of those matter for what you're verifying — use Setup B.

---

## Setup B — Kubernetes (minio + metadata pod + fake vllm pod)

### Start everything

```bash
./scripts/dev/setup-k8s.sh
```

This applies, into the `aibrix-system` namespace:

1. `redis-master` deployment + service (from `config/metadata/redis.yaml`)
2. `minio` deployment + service + bucket-creation Job (from
   `scripts/dev/k8s/minio.yaml`) + `s3-credentials` secret
3. `fake-vllm` deployment + service (from `scripts/dev/k8s/fake-vllm.yaml`)
4. `metadata-service` deployment + service + RBAC, with the S3-env patch on
   so it talks to minio (from `config/metadata/` via kustomize overlay)
5. Port-forwards `svc/metadata-service 8090:8090` to your laptop

### Observation checkpoints

| Step | What to run | Healthy state |
|---|---|---|
| pods up | `kubectl -n aibrix-system get pods` | all Running, READY 1/1 |
| redis | `kubectl -n aibrix-system exec deploy/redis-master -- redis-cli ping` | `PONG` |
| minio | `kubectl -n aibrix-system logs job/minio-mkbucket` | `Bucket created successfully aibrix` |
| minio | `kubectl -n aibrix-system port-forward svc/minio 9001:9001` then open browser | console login `aibrix` / `aibrix-secret`, `aibrix` bucket present |
| fake vllm | `kubectl -n aibrix-system exec deploy/fake-vllm -- curl -s localhost:8000/health` | `{"status":"ok"}` |
| metadata env | `kubectl -n aibrix-system describe deploy/metadata-service \| grep -A1 -E 'STORAGE_AWS\|REDIS_HOST'` | shows the S3 env-vars from the patch + Redis host |
| metadata up | `curl localhost:8090/readyz` | `{"status":"ready"}` |
| metadata wiring | `curl -s localhost:8090/status \| jq` | `kopf_operator.available=true`, `batch_driver.available=true` |
| metadata logs | `kubectl -n aibrix-system logs deploy/metadata-service -f` | `Metadata store initialized: ...redis-master:6379`, `BatchJobStore enabled` |
| **batch job created** | submit a batch via Postman, then `kubectl -n aibrix-system get jobs -w` | a new `batch-*` Job appears |
| **batch worker pod** | `kubectl -n aibrix-system get pods \| grep batch-` | pod runs to `Completed` |
| **worker logs** | `kubectl -n aibrix-system logs <batch-pod> -c worker` | reads input from minio, calls fake-vllm, writes output back |
| **output in minio** | mc / browser console | new objects under `aibrix/batches/...` and `aibrix/<file_id>` |
| **state machine** | `curl -s localhost:8090/v1/batches/{id}` repeatedly | `validating → in_progress → finalizing → completed` |

### Tear down

```bash
./scripts/dev/setup-k8s.sh teardown    # deletes the namespace
```

### Common gotchas

- **`metadata-service` CrashLoopBackOff** → 99% of the time it can't reach
  Redis. Check `kubectl logs` for `Metadata store ping ...`. Confirm
  `redis-master` service exists in `aibrix-system`.
- **Jobs stay `validating` forever** → the `aibrix-model-deployment-templates`
  / `aibrix-batch-profiles` ConfigMaps are missing. Apply your template/profile
  ConfigMaps under namespace `aibrix-system` first, or create with
  `kubectl create configmap aibrix-model-deployment-templates --from-file=...`.
- **Worker pod ImagePullBackOff** → the rendered Job uses an image you don't
  have. Override it in your BatchProfile / ModelDeploymentTemplate to point
  at `fake-vllm` for testing.
- **`/v1/models` returns nothing** → there are no pods labelled
  `model.aibrix.ai/name=<name>` in your cluster. Expected unless you deployed
  a model.

---

## Running the OpenAI SDK e2e against either setup

```bash
# Setup A (no aibrix template needed):
AIBRIX_BASE_URL=http://127.0.0.1:8090/v1 \
  pytest python/aibrix/tests/e2e/test_openai_sdk_batch.py -v -s

# Setup B (template+profile required):
AIBRIX_BASE_URL=http://127.0.0.1:8090/v1 \
AIBRIX_BATCH_TEMPLATE=my-template \
AIBRIX_BATCH_PROFILE=my-profile \
  pytest python/aibrix/tests/e2e/test_openai_sdk_batch.py -v -s
```

The test itself skips cleanly with a clear message if `:8090/readyz` is not
responding, so you can wire it into CI without flakiness on cold start.

---

## Using the Postman collection

1. Import `scripts/dev/aibrix-metadata.postman_collection.json` into Postman.
2. Set collection variable `base_url=http://127.0.0.1:8090` (default already).
3. For Setup B, also set `model_template` and `profile` to whatever names you
   registered in the `aibrix-model-deployment-templates` and
   `aibrix-batch-profiles` ConfigMaps. For Setup A, leave them blank.
4. Run requests in this order:
   - `Health > GET /readyz`
   - `Files > POST /v1/files (upload jsonl)` — attach a JSONL file. The
     test script auto-saves the returned `id` into `{{file_id}}`.
   - `Batches > POST /v1/batches (create)` — uses `{{file_id}}`. Auto-saves
     `{{batch_id}}`.
   - `Batches > GET /v1/batches/{batch_id}` — poll. Auto-saves
     `{{output_file_id}}` once present.
   - `Files > GET /v1/files/{file_id}/content` — set `{{file_id}}` to the
     output id and download.

A minimal `input.jsonl` you can drop next to Postman:

```jsonl
{"custom_id":"req-1","method":"POST","url":"/v1/chat/completions","body":{"model":"gpt-3.5-turbo","messages":[{"role":"user","content":"hi"}],"max_tokens":8}}
{"custom_id":"req-2","method":"POST","url":"/v1/chat/completions","body":{"model":"gpt-3.5-turbo","messages":[{"role":"user","content":"world"}],"max_tokens":8}}
```

---

## Quick decision guide

- **Just smoke-testing the HTTP API or front-end integration** → Setup A.
- **Verifying the K8s Job rendering, ConfigMap-driven templates, kopf operator,
  S3 round-trip, BatchJobStore persistence** → Setup B.
- **Writing a regression test that runs in CI** → start it against Setup A
  first (no cluster needed), promote to Setup B once you have a kind cluster
  in CI.
