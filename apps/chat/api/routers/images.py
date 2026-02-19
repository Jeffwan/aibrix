"""Image generation endpoint (stub)."""

from __future__ import annotations

import json

from fastapi import APIRouter
from sse_starlette.sse import EventSourceResponse

from models.schemas import ImageGenerateRequest

router = APIRouter(prefix="/api/image", tags=["image"])


@router.post("/generate")
async def generate_image(req: ImageGenerateRequest):
    """Stub: image generation will be implemented when
    AIBrix supports image generation models (e.g. FLUX, SD3).
    """
    return EventSourceResponse(_stub_stream(req))


async def _stub_stream(req: ImageGenerateRequest):
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
                "message": "Image generation is not yet available. "
                "Configure an image generation model in AIBrix to enable this feature.",
            }
        )
    }
