"""AIBrix Chat BFF — FastAPI entry point."""

from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware

from config import settings
from routers import chat, conversations, health, images, models, video

app = FastAPI(
    title="AIBrix Chat API",
    version=settings.app_version,
    docs_url="/api/docs",
    redoc_url="/api/redoc",
)

# CORS
origins = [o.strip() for o in settings.cors_origins.split(",")]
app.add_middleware(
    CORSMiddleware,
    allow_origins=origins,
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)

# Mount routers
app.include_router(health.router)
app.include_router(models.router)
app.include_router(conversations.router)
app.include_router(chat.router)
app.include_router(images.router)
app.include_router(video.router)
