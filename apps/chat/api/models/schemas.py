"""Pydantic models for request/response schemas."""

from __future__ import annotations

import uuid
from datetime import datetime, timezone
from typing import Any

from pydantic import BaseModel, Field


# ── Messages ───────────────────────────────────────────────


class MessageContent(BaseModel):
    """A single content block inside a message (text or image_url)."""

    type: str  # "text" or "image_url"
    text: str | None = None
    image_url: dict[str, str] | None = None


class Message(BaseModel):
    """Stored message in a conversation."""

    id: str = Field(default_factory=lambda: str(uuid.uuid4()))
    role: str  # "user", "assistant", "system"
    content: str | list[MessageContent]
    parent_id: str | None = None
    model: str | None = None
    created_at: str = Field(
        default_factory=lambda: datetime.now(timezone.utc).isoformat()
    )


# ── Conversations ──────────────────────────────────────────


class Conversation(BaseModel):
    """A conversation with its messages."""

    id: str = Field(default_factory=lambda: str(uuid.uuid4()))
    title: str = "New Chat"
    messages: list[Message] = Field(default_factory=list)
    model: str | None = None
    created_at: str = Field(
        default_factory=lambda: datetime.now(timezone.utc).isoformat()
    )
    updated_at: str = Field(
        default_factory=lambda: datetime.now(timezone.utc).isoformat()
    )


class ConversationSummary(BaseModel):
    """Lightweight view returned when listing conversations."""

    id: str
    title: str
    model: str | None = None
    message_count: int
    created_at: str
    updated_at: str


# ── Chat Completion Request ────────────────────────────────


class CompletionRequest(BaseModel):
    """Frontend sends only the new message; server manages history."""

    message: str | list[MessageContent]
    model: str
    temperature: float = 0.7
    max_tokens: int = 2048
    stream: bool = True
    system_prompt: str | None = None


# ── Chat Completion Response (non-streaming) ───────────────


class CompletionResponse(BaseModel):
    id: str
    conversation_id: str
    model: str
    message: Message
    usage: dict[str, int] | None = None


# ── Image Generation ───────────────────────────────────────


class ImageGenerateRequest(BaseModel):
    prompt: str
    model: str = "flux-1-dev"
    enhance_prompt: bool = False
    size: str = "1024x1024"
    n: int = 1
    source_image: str | None = None  # base64 for editing


# ── Video Generation ───────────────────────────────────────


class VideoGenerateRequest(BaseModel):
    prompt: str
    model: str = "hunyuan-video"
    enhance_prompt: bool = False
    duration: int = 5
    resolution: str = "720p"


# ── Models ─────────────────────────────────────────────────


class ModelInfo(BaseModel):
    id: str
    name: str | None = None
    capabilities: list[str] = Field(default_factory=lambda: ["text"])
    owned_by: str | None = None


class ModelListResponse(BaseModel):
    models: list[ModelInfo]


# ── Health ─────────────────────────────────────────────────


class HealthResponse(BaseModel):
    status: str
    version: str
    gateway_reachable: bool


# ── Errors ─────────────────────────────────────────────────


class ErrorDetail(BaseModel):
    code: str
    message: str
    status: int


class ErrorResponse(BaseModel):
    error: ErrorDetail
