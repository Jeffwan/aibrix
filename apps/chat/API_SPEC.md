# AIBrix Chat — API Interface Specification

## Overview

The AIBrix Chat application follows a three-layer architecture:

```
┌─────────────┐      ┌─────────────────┐      ┌──────────────────┐
│  Frontend    │ SSE  │   BFF (FastAPI)  │ HTTP │  AIBrix Gateway  │
│  React SPA   ├─────►│   apps/chat/api  ├─────►│  + Model Pods    │
└─────────────┘      └─────────────────┘      └──────────────────┘
```

- **Frontend** talks only to the BFF. Never calls AIBrix gateway directly.
- **BFF** handles application logic: conversation management, prompt assembly, SSE streaming.
- **AIBrix Gateway** handles infrastructure: model routing, load balancing, replica management.

### Key Design Decision: Conversation-Oriented API

The BFF exposes a **conversation-oriented API** (not an OpenAI passthrough). The frontend sends only the latest user message; the server manages the full conversation history and translates it to OpenAI-compatible format when calling the gateway.

This avoids the redundancy of three layers all speaking raw OpenAI API, and enables server-side features like conversation persistence, auto-titling, and message branching.

---

## Configuration

| Variable | Description | Default |
|---|---|---|
| `AIBRIX_GATEWAY_URL` | AIBrix gateway base URL | `http://localhost:8888` |
| `API_KEY` | API key for gateway auth | `""` |
| `CORS_ORIGINS` | Allowed CORS origins (comma-separated) | `http://localhost:5173` |

---

## API Endpoints

### 1. Conversations (CRUD)

**`POST /api/conversations`** — Create a new conversation

```json
{"model": "deepseek-v3", "title": "New Chat"}
```

Response: full `Conversation` object with `id`, `title`, `messages: []`, timestamps.

**`GET /api/conversations`** — List all conversations

Response: array of `ConversationSummary` objects (id, title, model, message_count, timestamps).

**`GET /api/conversations/{id}`** — Get conversation with full message history

**`PATCH /api/conversations/{id}`** — Update conversation title

```json
{"title": "KV Cache Discussion"}
```

**`DELETE /api/conversations/{id}`** — Delete a conversation

---

### 2. Chat Completions

**`POST /api/conversations/{id}/completions`**

The primary endpoint. Frontend sends only the latest message; the server builds the full history.

**Request:**
```json
{
  "message": "Explain KV cache offloading.",
  "model": "deepseek-v3",
  "temperature": 0.7,
  "max_tokens": 2048,
  "stream": true,
  "system_prompt": "You are a helpful assistant."
}
```

For vision (image + text), `message` is a content array:
```json
{
  "message": [
    {"type": "text", "text": "Describe this image."},
    {"type": "image_url", "image_url": {"url": "data:image/png;base64,..."}}
  ],
  "model": "qwen-vl",
  "stream": true
}
```

**Response (SSE stream):**
```
data: {"event": "text_delta", "delta": "KV cache"}
data: {"event": "text_delta", "delta": " offloading is"}
data: {"event": "text_delta", "delta": " a technique..."}
data: {"event": "done", "finish_reason": "stop", "usage": {"prompt_tokens": 42, "completion_tokens": 156}}
```

**Response (non-streaming):**
```json
{
  "id": "chatcmpl-abc123",
  "conversation_id": "conv-xyz",
  "model": "deepseek-v3",
  "message": {
    "id": "msg-123",
    "role": "assistant",
    "content": "KV cache offloading is...",
    "model": "deepseek-v3",
    "created_at": "2024-01-01T00:00:00Z"
  },
  "usage": {"prompt_tokens": 42, "completion_tokens": 156}
}
```

**BFF Logic:**
1. Look up conversation, append user message
2. Build full message history (with system prompt)
3. Forward to `AIBRIX_GATEWAY_URL/v1/chat/completions` in OpenAI format
4. Stream SSE events back to frontend
5. Store assistant response in conversation
6. On error, send `{"event": "error", "message": "..."}`

---

### 3. Image Generation (Stub)

**`POST /api/image/generate`**

```json
{
  "prompt": "a futuristic data center",
  "model": "flux-1-dev",
  "enhance_prompt": true,
  "size": "1024x1024",
  "n": 1
}
```

Currently returns an SSE error event indicating image generation is not yet available.

---

### 4. Video Generation (Stub)

**`POST /api/video/generate`**

```json
{
  "prompt": "a drone flying over mountains",
  "model": "hunyuan-video",
  "duration": 5,
  "resolution": "720p"
}
```

**`GET /api/video/status/{job_id}`** — Check video job status

Currently returns stub responses. Will be implemented when AIBrix supports video generation models.

---

### 5. Model Discovery

**`GET /api/models`**

Lists available models from the AIBrix gateway.

```json
{
  "models": [
    {"id": "deepseek-v3", "name": "deepseek-v3", "capabilities": ["text"], "owned_by": "vllm"},
    {"id": "qwen-vl", "name": "qwen-vl", "capabilities": ["text"], "owned_by": "vllm"}
  ]
}
```

---

### 6. Health Check

**`GET /api/health`**

```json
{
  "status": "ok",
  "version": "0.1.0",
  "gateway_reachable": true
}
```

---

## Data Model

### Conversation
```
id: str (UUID)
title: str
model: str | null
messages: Message[]
created_at: str (ISO 8601)
updated_at: str (ISO 8601)
```

### Message
```
id: str (UUID)
role: "user" | "assistant" | "system"
content: str | ContentBlock[]
parent_id: str | null (for tree-structured branching)
model: str | null
created_at: str (ISO 8601)
```

Messages form a tree via `parent_id`. This enables editing a message and branching the conversation (like Claude.ai and LibreChat).

---

## SSE Event Types

| Event | Source | Description |
|---|---|---|
| `text_delta` | Chat | Incremental text token |
| `done` | Chat | Stream finished, includes usage |
| `prompt_enhanced` | Image/Video | LLM-rewritten prompt |
| `generating` | Image | Image generation progress |
| `image_done` | Image | Generated image URL |
| `job_queued` | Video | Job accepted, position in queue |
| `progress` | Video | Generation progress + ETA |
| `video_done` | Video | Generated video URL |
| `error` | All | Error with message |

---

## Error Format

```json
{
  "error": {
    "code": "model_not_found",
    "message": "Model 'nonexistent' is not available.",
    "status": 404
  }
}
```
