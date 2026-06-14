# Copyright 2025 The Aibrix Team.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# 	http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""FastAPI router for model lifecycle endpoints on the aibrix-runtime sidecar.

The ModelClaim controller uses these endpoints to activate/deactivate full
model engine processes, set kvcached budgets, and list locally managed runtime
models. The import chain is intentionally light: only protocol models and the
runtime implementation.
"""

import logging

from fastapi import APIRouter
from fastapi.responses import JSONResponse

from aibrix.openapi.protocol import (
    ActivateRuntimeModelRequest,
    ActivateRuntimeModelResponse,
    DeactivateRuntimeModelRequest,
    ListRuntimeModelsResponse,
    RuntimeModelInfo,
    SetKVBudgetRequest,
)
from aibrix.runtime.model_runtime import ModelNotFound, get_model_runtime

logger = logging.getLogger(__name__)

model_runtime_router = APIRouter()


@model_runtime_router.post("/v1/runtime/models/activate")
async def activate_runtime_model(request: ActivateRuntimeModelRequest):
    """Activate a model as its own kvcached-enabled engine process on this pod."""
    try:
        inst = get_model_runtime().activate(
            model_name=request.model_name,
            artifact_url=request.artifact_url,
            engine=request.engine,
            port=request.port,
            ipc_name=request.ipc_name or "",
            kv_min_bytes=request.kv_min_bytes,
            kv_max_bytes=request.kv_max_bytes,
            kv_policy=request.kv_policy,
            kv_shares=request.kv_shares,
            priority=request.priority,
            additional_config=request.additional_config,
        )
    except Exception as exc:  # surface activation failure to the controller
        logger.error(f"failed to activate model {request.model_name}: {exc}")
        return JSONResponse(
            status_code=500,
            content=ActivateRuntimeModelResponse(
                status="error", model_name=request.model_name, message=str(exc)
            ).model_dump(),
        )
    return JSONResponse(
        status_code=200,
        content=ActivateRuntimeModelResponse(
            status="success",
            model_name=inst.model_name,
            port=inst.port,
            ipc_name=inst.ipc_name,
        ).model_dump(),
    )


@model_runtime_router.post("/v1/runtime/models/deactivate")
async def deactivate_runtime_model(request: DeactivateRuntimeModelRequest):
    """Deactivate a model: warm (release KV), evict (sleep weights), or stop."""
    get_model_runtime().deactivate(
        request.model_name, mode=request.mode, sleep_level=request.sleep_level
    )
    return JSONResponse(content={"status": "success"}, status_code=200)


@model_runtime_router.post("/v1/runtime/models/kv_budget")
async def set_runtime_model_kv_budget(request: SetKVBudgetRequest):
    """Set a model's kvcached KV budget (the resource-manager actuator)."""
    try:
        get_model_runtime().set_kv_budget(
            request.model_name, request.kv_max_bytes, request.kv_min_bytes
        )
    except ModelNotFound:
        return JSONResponse(
            content={"error": f"model {request.model_name} not found"}, status_code=404
        )
    return JSONResponse(content={"status": "success"}, status_code=200)


@model_runtime_router.get("/v1/runtime/models")
async def list_runtime_models():
    """List models managed by this runtime sidecar, with KV accounting and
    sharing semantics. The node-level reclaim loop consumes this to rebuild
    each model's demand; kv_used/total are read live from the model's kvcached
    /dev/shm MemInfoStruct (zero when the segment is absent)."""
    from aibrix.runtime.model_runtime import instance_ready, read_kv_segment

    models = []
    for m in get_model_runtime().list_models():
        seg = read_kv_segment(m.ipc_name)
        total, used, prealloc = seg if seg else (0, 0, 0)
        models.append(
            RuntimeModelInfo(
                model_name=m.model_name,
                port=m.port,
                ipc_name=m.ipc_name,
                phase=m.phase,
                ready=instance_ready(m),
                kv_min_bytes=m.kv_min_bytes,
                kv_max_bytes=m.kv_max_bytes,
                kv_used_bytes=used + prealloc,
                kv_total_bytes=total,
                kv_policy=m.kv_policy,
                kv_shares=m.kv_shares,
                priority=m.priority,
            )
        )
    return JSONResponse(
        status_code=200,
        content=ListRuntimeModelsResponse(models=models).model_dump(),
    )
