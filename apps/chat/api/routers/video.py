"""Video generation endpoint (stub)."""

from __future__ import annotations

import json

from fastapi import APIRouter
from sse_starlette.sse import EventSourceResponse

from models.schemas import VideoGenerateRequest

router = APIRouter(prefix="/api/video", tags=["video"])


@router.post("/generate")
async def generate_video(req: VideoGenerateRequest):
    """Stub: video generation will be implemented when
    AIBrix supports video generation models (e.g. HunyuanVideo).
    """
    return EventSourceResponse(_stub_stream(req))


async def _stub_stream(req: VideoGenerateRequest):
    if req.enhance_prompt:
        yield {
            "data": json.dumps(
                {"event": "prompt_enhanced", "prompt": req.prompt}
            )
        }
    yield {
        "data": json.dumps(
            {
                "event": "error",
                "message": "Video generation is not yet available. "
                "Configure a video generation model in AIBrix to enable this feature.",
            }
        )
    }


@router.get("/status/{job_id}")
async def video_status(job_id: str):
    """Stub: check video job status."""
    return {
        "job_id": job_id,
        "status": "not_implemented",
        "message": "Video generation is not yet available.",
    }
