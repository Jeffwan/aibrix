"""Chat completions endpoint — the core SSE streaming route."""

from __future__ import annotations

import json

from fastapi import APIRouter, HTTPException
from fastapi.responses import JSONResponse
from sse_starlette.sse import EventSourceResponse

from models.schemas import CompletionRequest, CompletionResponse, Message
from services.conversation import store
from services import gateway

router = APIRouter(tags=["chat"])


@router.post("/api/conversations/{conversation_id}/completions")
async def chat_completions(conversation_id: str, req: CompletionRequest):
    """Send a message and get a response. Streams via SSE when stream=True.

    The frontend sends only the latest user message.
    The server builds the full history from the conversation and
    forwards it to the AIBrix gateway.
    """
    conv = store.get(conversation_id)
    if conv is None:
        raise HTTPException(status_code=404, detail="Conversation not found")

    # Update conversation model if changed
    if conv.model != req.model:
        conv.model = req.model

    # Store the user message
    user_msg = Message(role="user", content=req.message)
    store.add_message(conversation_id, user_msg)

    # Build full message history for the gateway
    messages = store.get_messages_for_gateway(
        conversation_id, system_prompt=req.system_prompt
    )

    if req.stream:
        return EventSourceResponse(_stream_response(
            conversation_id=conversation_id,
            messages=messages,
            model=req.model,
            temperature=req.temperature,
            max_tokens=req.max_tokens,
        ))
    else:
        return await _non_stream_response(
            conversation_id=conversation_id,
            messages=messages,
            model=req.model,
            temperature=req.temperature,
            max_tokens=req.max_tokens,
        )


async def _stream_response(
    conversation_id: str,
    messages: list[dict],
    model: str,
    temperature: float,
    max_tokens: int,
):
    """Generator that yields SSE events from the gateway stream."""
    collected_content = []

    try:
        async for event_data in gateway.chat_completion_stream(
            messages=messages,
            model=model,
            temperature=temperature,
            max_tokens=max_tokens,
        ):
            parsed = json.loads(event_data)
            if parsed.get("event") == "text_delta":
                collected_content.append(parsed["delta"])
            yield {"data": event_data}

        # Store the assistant response
        full_content = "".join(collected_content)
        if full_content:
            assistant_msg = Message(role="assistant", content=full_content, model=model)
            store.add_message(conversation_id, assistant_msg)

    except Exception as e:
        error_event = json.dumps({"event": "error", "message": str(e)})
        yield {"data": error_event}


async def _non_stream_response(
    conversation_id: str,
    messages: list[dict],
    model: str,
    temperature: float,
    max_tokens: int,
) -> JSONResponse:
    """Non-streaming: call gateway and return full response."""
    try:
        resp = await gateway.chat_completion(
            messages=messages,
            model=model,
            temperature=temperature,
            max_tokens=max_tokens,
            stream=False,
        )
        resp.raise_for_status()
        data = resp.json()

        content = data["choices"][0]["message"]["content"]
        usage = data.get("usage")

        # Store assistant message
        assistant_msg = Message(role="assistant", content=content, model=model)
        store.add_message(conversation_id, assistant_msg)

        response = CompletionResponse(
            id=data.get("id", ""),
            conversation_id=conversation_id,
            model=model,
            message=assistant_msg,
            usage=usage,
        )
        return JSONResponse(content=response.model_dump())

    except Exception as e:
        raise HTTPException(status_code=502, detail=f"Gateway error: {e}")
