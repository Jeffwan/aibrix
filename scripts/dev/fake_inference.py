"""Tiny fake inference server for local Aibrix batch testing.

Provides minimal OpenAI-compatible responses for the four endpoints the batch
driver uses: /v1/chat/completions, /v1/completions, /v1/embeddings, /v1/rerank.
No tokens are loaded; replies are deterministic and fast.

Run:
    python scripts/dev/fake_inference.py --port 8000
"""

import argparse
import time
import uuid

import uvicorn
from fastapi import FastAPI, Request

app = FastAPI(title="aibrix-fake-inference")


@app.get("/health")
async def health():
    return {"status": "ok"}


@app.post("/v1/chat/completions")
async def chat_completions(req: Request):
    body = await req.json()
    model = body.get("model", "fake-model")
    return {
        "id": f"chatcmpl-{uuid.uuid4().hex[:12]}",
        "object": "chat.completion",
        "created": int(time.time()),
        "model": model,
        "choices": [
            {
                "index": 0,
                "message": {"role": "assistant", "content": "fake reply"},
                "finish_reason": "stop",
            }
        ],
        "usage": {"prompt_tokens": 10, "completion_tokens": 3, "total_tokens": 13},
    }


@app.post("/v1/completions")
async def completions(req: Request):
    body = await req.json()
    return {
        "id": f"cmpl-{uuid.uuid4().hex[:12]}",
        "object": "text_completion",
        "created": int(time.time()),
        "model": body.get("model", "fake-model"),
        "choices": [{"text": " fake completion", "index": 0, "finish_reason": "stop"}],
        "usage": {"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7},
    }


@app.post("/v1/embeddings")
async def embeddings(req: Request):
    body = await req.json()
    return {
        "object": "list",
        "data": [{"object": "embedding", "index": 0, "embedding": [0.0] * 8}],
        "model": body.get("model", "fake-embed"),
        "usage": {"prompt_tokens": 4, "total_tokens": 4},
    }


@app.post("/v1/rerank")
async def rerank(req: Request):
    body = await req.json()
    docs = body.get("documents", [])
    return {
        "model": body.get("model", "fake-rerank"),
        "results": [
            {"index": i, "relevance_score": 1.0 - 0.1 * i}
            for i in range(len(docs))
        ],
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8000)
    args = parser.parse_args()
    uvicorn.run(app, host=args.host, port=args.port, log_level="warning")


if __name__ == "__main__":
    main()
