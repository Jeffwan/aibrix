"""HTTP client for communicating with the AIBrix gateway."""

from __future__ import annotations

import json
from collections.abc import AsyncIterator
from typing import Any

import httpx

from config import settings


def _get_headers() -> dict[str, str]:
    headers = {"Content-Type": "application/json"}
    if settings.api_key:
        headers["Authorization"] = f"Bearer {settings.api_key}"
    return headers


async def check_health() -> bool:
    """Check if the gateway is reachable."""
    try:
        async with httpx.AsyncClient(timeout=5.0) as client:
            resp = await client.get(
                f"{settings.aibrix_gateway_url}/v1/models",
                headers=_get_headers(),
            )
            return resp.status_code == 200
    except httpx.HTTPError:
        return False


async def list_models() -> list[dict]:
    """Fetch available models from the gateway."""
    try:
        async with httpx.AsyncClient(timeout=10.0) as client:
            resp = await client.get(
                f"{settings.aibrix_gateway_url}/v1/models",
                headers=_get_headers(),
            )
            resp.raise_for_status()
            data = resp.json()
            return data.get("data", [])
    except httpx.HTTPError:
        return []


async def chat_completion(
    messages: list[dict],
    model: str,
    temperature: float = 0.7,
    max_tokens: int = 2048,
    stream: bool = True,
) -> httpx.Response:
    """Send a chat completion request to the gateway (non-streaming)."""
    payload = {
        "model": model,
        "messages": messages,
        "temperature": temperature,
        "max_tokens": max_tokens,
        "stream": False,
    }
    async with httpx.AsyncClient(timeout=120.0) as client:
        resp = await client.post(
            f"{settings.aibrix_gateway_url}/v1/chat/completions",
            json=payload,
            headers=_get_headers(),
        )
        return resp


async def chat_completion_stream(
    messages: list[dict],
    model: str,
    temperature: float = 0.7,
    max_tokens: int = 2048,
) -> AsyncIterator[str]:
    """Stream chat completions from the gateway, yielding SSE lines."""
    payload = {
        "model": model,
        "messages": messages,
        "temperature": temperature,
        "max_tokens": max_tokens,
        "stream": True,
    }
    async with httpx.AsyncClient(timeout=120.0) as client:
        async with client.stream(
            "POST",
            f"{settings.aibrix_gateway_url}/v1/chat/completions",
            json=payload,
            headers=_get_headers(),
        ) as resp:
            resp.raise_for_status()
            async for line in resp.aiter_lines():
                if line.startswith("data: "):
                    data = line[6:]
                    if data.strip() == "[DONE]":
                        yield json.dumps({"event": "done"})
                        return
                    try:
                        chunk = json.loads(data)
                        delta = chunk.get("choices", [{}])[0].get("delta", {})
                        content = delta.get("content")
                        if content:
                            yield json.dumps(
                                {"event": "text_delta", "delta": content}
                            )
                        # Check for finish_reason
                        finish = chunk.get("choices", [{}])[0].get("finish_reason")
                        if finish:
                            usage = chunk.get("usage")
                            yield json.dumps(
                                {"event": "done", "finish_reason": finish, "usage": usage}
                            )
                    except json.JSONDecodeError:
                        continue
