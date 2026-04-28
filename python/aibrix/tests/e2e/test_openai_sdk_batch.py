# Copyright 2024 The Aibrix Team.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0

"""End-to-end test of the Aibrix metadata server using the official `openai`
Python SDK against a real running service.

Targets the same API surface as ``test_batch_api.py`` but exercises it through
the SDK so we catch any field-name / serialization drift the hand-rolled httpx
tests miss.

Prereq (one of):
  Setup A: ./scripts/dev/start-local.sh
  Setup B: ./scripts/dev/setup-k8s.sh

Run:
  AIBRIX_BASE_URL=http://127.0.0.1:8090/v1 \\
      pytest python/aibrix/tests/e2e/test_openai_sdk_batch.py -v -s
"""

from __future__ import annotations

import io
import json
import os
import time
from typing import Any, Dict, List

import pytest

openai = pytest.importorskip("openai")

BASE_URL = os.getenv("AIBRIX_BASE_URL", "http://127.0.0.1:8090/v1")
API_KEY = os.getenv("AIBRIX_API_KEY", "sk-aibrix-local")
TIMEOUT_S = float(os.getenv("AIBRIX_BATCH_POLL_TIMEOUT", "120"))
POLL_INTERVAL_S = float(os.getenv("AIBRIX_BATCH_POLL_INTERVAL", "2"))

# Aibrix BatchProfile/template names used in Setup B (configured via
# config/metadata/job_template_patch.yaml). For Setup A neither is required
# because no K8s job is rendered.
AIBRIX_TEMPLATE = os.getenv("AIBRIX_BATCH_TEMPLATE", "")
AIBRIX_PROFILE = os.getenv("AIBRIX_BATCH_PROFILE", "")

ENDPOINT_BODIES: Dict[str, Dict[str, Any]] = {
    "/v1/chat/completions": {
        "model": "gpt-3.5-turbo-0125",
        "messages": [
            {"role": "system", "content": "You are a helpful assistant."},
            {"role": "user", "content": "ping"},
        ],
        "max_tokens": 16,
    },
    "/v1/completions": {
        "model": "gpt-3.5-turbo-0125",
        "prompt": "Once upon a time",
        "max_tokens": 8,
    },
    "/v1/embeddings": {
        "model": "text-embedding-ada-002",
        "input": "hello world",
    },
    "/v1/rerank": {
        "model": "reranker-v1",
        "query": "deep learning",
        "documents": ["a", "b"],
    },
}


def _make_jsonl(endpoint: str, n: int) -> bytes:
    body = ENDPOINT_BODIES[endpoint]
    lines: List[str] = []
    for i in range(n):
        lines.append(
            json.dumps(
                {
                    "custom_id": f"req-{i + 1}",
                    "method": "POST",
                    "url": endpoint,
                    "body": body,
                }
            )
        )
    return ("\n".join(lines) + "\n").encode()


@pytest.fixture(scope="module")
def client():
    import httpx

    # Skip cleanly if the metadata server is not running.
    try:
        r = httpx.get(BASE_URL.rsplit("/v1", 1)[0] + "/readyz", timeout=2.0)
        if r.status_code != 200:
            pytest.skip(f"metadata not ready ({r.status_code}) at {BASE_URL}")
    except Exception as e:
        pytest.skip(f"metadata unreachable at {BASE_URL}: {e}")

    return openai.OpenAI(base_url=BASE_URL, api_key=API_KEY)


def _create_batch(client, file_id: str, endpoint: str):
    extra: Dict[str, Any] = {}
    if AIBRIX_TEMPLATE or AIBRIX_PROFILE:
        # Aibrix-specific extension carried through the OpenAI SDK's
        # extra_body channel (the SDK pass-through path).
        aibrix_ext: Dict[str, str] = {}
        if AIBRIX_TEMPLATE:
            aibrix_ext["model_template"] = AIBRIX_TEMPLATE
        if AIBRIX_PROFILE:
            aibrix_ext["profile"] = AIBRIX_PROFILE
        extra["extra_body"] = {"aibrix": aibrix_ext}

    return client.batches.create(
        input_file_id=file_id,
        endpoint=endpoint,
        completion_window="24h",
        metadata={"created_by": "e2e_test"},
        **extra,
    )


def _poll_until_terminal(client, batch_id: str):
    deadline = time.time() + TIMEOUT_S
    last_status = None
    while time.time() < deadline:
        b = client.batches.retrieve(batch_id)
        if b.status != last_status:
            print(f"[poll] {batch_id} -> {b.status} counts={b.request_counts}")
            last_status = b.status
        if b.status in ("completed", "failed", "expired", "cancelled"):
            return b
        time.sleep(POLL_INTERVAL_S)
    pytest.fail(f"batch {batch_id} did not finish within {TIMEOUT_S}s; last={last_status}")


# ---------- tests ----------------------------------------------------------


def test_files_upload_list_retrieve_delete(client):
    payload = _make_jsonl("/v1/chat/completions", 3)
    f = client.files.create(file=("input.jsonl", io.BytesIO(payload)), purpose="batch")
    assert f.id.startswith("file-") or f.id  # accept any non-empty id

    listed = client.files.list()
    assert any(item.id == f.id for item in listed.data)

    got = client.files.retrieve(f.id)
    assert got.id == f.id
    assert got.purpose == "batch"

    body = client.files.content(f.id).read()
    assert body == payload

    client.files.delete(f.id)


@pytest.mark.parametrize(
    "endpoint",
    list(ENDPOINT_BODIES.keys()),
)
def test_batch_full_lifecycle(client, endpoint):
    payload = _make_jsonl(endpoint, 2)
    f = client.files.create(file=("input.jsonl", io.BytesIO(payload)), purpose="batch")

    batch = _create_batch(client, f.id, endpoint)
    assert batch.id
    assert batch.endpoint == endpoint
    assert batch.input_file_id == f.id

    final = _poll_until_terminal(client, batch.id)
    assert final.status == "completed", f"batch did not complete: {final}"
    assert final.request_counts.completed == 2
    assert final.request_counts.failed == 0
    assert final.output_file_id, "missing output_file_id"

    out = client.files.content(final.output_file_id).read().decode()
    out_lines = [json.loads(l) for l in out.strip().splitlines()]
    assert len(out_lines) == 2
    custom_ids = sorted(l["custom_id"] for l in out_lines)
    assert custom_ids == ["req-1", "req-2"]


def test_batch_list_pagination(client):
    listed = client.batches.list(limit=5)
    assert hasattr(listed, "data")


def test_batch_cancel(client):
    payload = _make_jsonl("/v1/chat/completions", 1)
    f = client.files.create(file=("input.jsonl", io.BytesIO(payload)), purpose="batch")
    b = _create_batch(client, f.id, "/v1/chat/completions")

    cancelled = client.batches.cancel(b.id)
    assert cancelled.status in ("cancelling", "cancelled", "completed")
    # Not asserting cancelled-status reaches terminal; some backends finish too
    # fast for cancel to be observable. The 200 from the API is the contract.
