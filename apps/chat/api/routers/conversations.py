"""Conversation CRUD endpoints."""

from fastapi import APIRouter, HTTPException
from pydantic import BaseModel

from models.schemas import Conversation, ConversationSummary
from services.conversation import store

router = APIRouter(prefix="/api/conversations", tags=["conversations"])


class CreateConversationRequest(BaseModel):
    model: str | None = None
    title: str = "New Chat"


class UpdateTitleRequest(BaseModel):
    title: str


@router.post("", response_model=Conversation, status_code=201)
async def create_conversation(req: CreateConversationRequest):
    return store.create(model=req.model, title=req.title)


@router.get("", response_model=list[ConversationSummary])
async def list_conversations():
    return store.list_all()


@router.get("/{conversation_id}", response_model=Conversation)
async def get_conversation(conversation_id: str):
    conv = store.get(conversation_id)
    if conv is None:
        raise HTTPException(status_code=404, detail="Conversation not found")
    return conv


@router.patch("/{conversation_id}", response_model=Conversation)
async def update_conversation(conversation_id: str, req: UpdateTitleRequest):
    conv = store.update_title(conversation_id, req.title)
    if conv is None:
        raise HTTPException(status_code=404, detail="Conversation not found")
    return conv


@router.delete("/{conversation_id}", status_code=204)
async def delete_conversation(conversation_id: str):
    if not store.delete(conversation_id):
        raise HTTPException(status_code=404, detail="Conversation not found")
