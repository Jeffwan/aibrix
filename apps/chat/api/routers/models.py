"""Model discovery endpoint."""

from fastapi import APIRouter

from models.schemas import ModelInfo, ModelListResponse
from services import gateway

router = APIRouter(prefix="/api", tags=["models"])


@router.get("/models", response_model=ModelListResponse)
async def list_models():
    raw_models = await gateway.list_models()

    models = []
    for m in raw_models:
        model_id = m.get("id", "")
        models.append(
            ModelInfo(
                id=model_id,
                name=model_id,
                owned_by=m.get("owned_by"),
            )
        )

    return ModelListResponse(models=models)
