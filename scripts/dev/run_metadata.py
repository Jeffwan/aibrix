"""Local Aibrix metadata server launcher (no Kubernetes).

Bypasses the production main() so we can:
- Skip kubeconfig loading entirely.
- Inject INFERENCE_ENGINE_ENDPOINT into the BatchDriver in standalone mode
  (the production build_app() does not pass this through).

Run after starting Redis and (optionally) the fake inference server:

    INFERENCE_ENGINE_ENDPOINT=http://127.0.0.1:8000 \\
    REDIS_HOST=127.0.0.1 REDIS_PORT=6379 \\
    STORAGE_TYPE=local LOCAL_STORAGE_PATH=./.storage \\
    python scripts/dev/run_metadata.py --port 8090
"""

import argparse
import os

import uvicorn

from aibrix.batch.job_driver import ProxyInferenceEngineClient
from aibrix.metadata.app import build_app


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", default="0.0.0.0")
    parser.add_argument("--port", type=int, default=8090)
    parser.add_argument(
        "--enable-fastapi-docs", action="store_true", default=True,
        help="Mount /docs (default: on for local dev)",
    )
    cli = parser.parse_args()

    args = argparse.Namespace(
        enable_fastapi_docs=cli.enable_fastapi_docs,
        disable_batch_api=False,
        disable_file_api=False,
        enable_k8s_job=False,
        e2e_test=True,
    )

    app = build_app(args)

    # Wire fake/real inference endpoint into the standalone batch driver.
    endpoint = os.getenv("INFERENCE_ENGINE_ENDPOINT")
    if endpoint and hasattr(app.state, "batch_driver"):
        app.state.batch_driver._inference_client = ProxyInferenceEngineClient(endpoint)
        print(f"[metadata] inference endpoint -> {endpoint}")
    else:
        print("[metadata] no INFERENCE_ENGINE_ENDPOINT set; using echo client")

    uvicorn.run(app, host=cli.host, port=cli.port)


if __name__ == "__main__":
    main()
