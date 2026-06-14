# Copyright 2024 The Aibrix Team.
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

from typing import Dict, List, Optional

from pydantic import BaseModel, ConfigDict, Field


class NoExtraBaseModel(BaseModel):
    # The class does not allow extra fields
    model_config = ConfigDict(extra="forbid")


class NoProtectedBaseModel(BaseModel):
    # The class does not allow extra fields
    model_config = ConfigDict(extra="forbid", protected_namespaces=())


class ErrorResponse(NoExtraBaseModel):
    object: str = "error"
    message: str
    type: str
    param: Optional[str] = None
    code: int


class LoadLoraAdapterRequest(NoExtraBaseModel):
    lora_name: str
    lora_path: str


class UnloadLoraAdapterRequest(NoExtraBaseModel):
    lora_name: str
    lora_int_id: Optional[int] = Field(default=None)


class DownloadModelRequest(NoProtectedBaseModel):
    model_uri: str
    local_dir: Optional[str] = None
    model_name: Optional[str] = None
    download_extra_config: Optional[Dict] = None


class ModelStatusCard(NoProtectedBaseModel):
    model_name: str
    model_root_path: str
    source: str
    model_status: str


class ListModelRequest(NoExtraBaseModel):
    local_dir: str


class ListModelResponse(NoExtraBaseModel):
    object: str = "list"
    data: List[ModelStatusCard] = Field(default_factory=list)


# Runtime API Protocol for Artifact Delegation


class LoadLoraAdapterRuntimeRequest(NoExtraBaseModel):
    """Request to load LoRA adapter with artifact delegation via runtime."""

    lora_name: str
    artifact_url: str  # Original URL (s3://, gs://, huggingface://, etc.)
    credentials_secret: Optional[str] = Field(
        default=None, description="Kubernetes secret name containing credentials"
    )
    credentials: Optional[Dict[str, str]] = Field(
        default=None, description="Direct credentials for artifact download"
    )
    additional_config: Optional[Dict[str, str]] = Field(
        default=None, description="Additional configuration for artifact download"
    )
    local_dir: Optional[str] = Field(
        default="/tmp/aibrix/adapters",
        description="Local directory for downloaded artifacts",
    )


class LoadLoraAdapterRuntimeResponse(NoExtraBaseModel):
    """Response from runtime after loading adapter."""

    status: str  # "success" or "error"
    message: str
    local_path: Optional[str] = Field(
        default=None, description="Local path where artifact was downloaded"
    )
    engine_response: Optional[Dict] = Field(
        default=None, description="Response from inference engine"
    )


class UnloadLoraAdapterRuntimeRequest(NoExtraBaseModel):
    """Request to unload LoRA adapter with optional cleanup via runtime."""

    lora_name: str
    cleanup_local: bool = Field(
        default=True, description="Whether to delete local artifact files"
    )


# --------------------------------------------------------------------------- #
# Runtime model lifecycle protocol (engine <-> control-plane co-design)
# --------------------------------------------------------------------------- #
class ActivateRuntimeModelRequest(NoProtectedBaseModel):
    """Bring a model online as its own kvcached-enabled engine process."""

    model_name: str
    artifact_url: str
    engine: str = "vllm"
    port: int = Field(default=0, description="0 lets the agent pick a free port")
    ipc_name: Optional[str] = Field(
        default=None,
        description="kvcached KVCACHED_IPC_NAME; agent derives one if empty",
    )
    kv_min_bytes: int = 0
    kv_max_bytes: int = 0
    # KV-sharing semantics (mirror the CRD's Spec.KV / Spec.Priority). The agent
    # stores them per instance so the node-level reclaim loop can rebuild each
    # model's kv_reclaim.ModelDemand without reading CRDs.
    kv_policy: str = "Guaranteed"  # "Exclusive" | "Shared" | "Guaranteed"
    kv_shares: int = 1
    priority: int = 0
    credentials: Optional[Dict[str, str]] = None
    additional_config: Optional[Dict[str, str]] = None


class ActivateRuntimeModelResponse(NoProtectedBaseModel):
    status: str  # "success" | "error"
    model_name: str
    port: int = 0
    ipc_name: str = ""
    message: Optional[str] = None


class DeactivateRuntimeModelRequest(NoProtectedBaseModel):
    """Tear a model down: warm (release KV), evict (sleep weights), or stop."""

    model_name: str
    mode: str = "stop"  # "warm" | "evict" | "stop"
    sleep_level: int = 1


class SetKVBudgetRequest(NoProtectedBaseModel):
    """Set a model's kvcached KV budget ceiling/floor (the reclaim actuator)."""

    model_name: str
    kv_max_bytes: int
    kv_min_bytes: int = 0


class RuntimeModelInfo(NoProtectedBaseModel):
    model_name: str
    port: int
    ipc_name: str
    phase: str
    # Whether the engine can serve right now (a /health probe). The controller
    # gates routability on this: a model's warm-pod annotation stays at the
    # non-routable marker (port 0) until ready, so requests never hit a
    # still-booting engine.
    ready: bool = False
    # KV accounting + sharing semantics, consumed by the node-level reclaim loop
    # (which rebuilds kv_reclaim.ModelDemand from this listing). kv_used/total
    # come from the model's kvcached /dev/shm MemInfoStruct and are zero when
    # the segment is absent (mock engine, or engine still starting).
    kv_min_bytes: int = 0
    kv_max_bytes: int = 0
    kv_used_bytes: int = 0
    kv_total_bytes: int = 0
    kv_policy: str = "Guaranteed"
    kv_shares: int = 1
    priority: int = 0


class ListRuntimeModelsResponse(NoProtectedBaseModel):
    models: List[RuntimeModelInfo]
