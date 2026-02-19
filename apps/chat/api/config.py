from pydantic_settings import BaseSettings


class Settings(BaseSettings):
    # AIBrix gateway
    aibrix_gateway_url: str = "http://localhost:8888"
    api_key: str = ""

    # CORS
    cors_origins: str = "http://localhost:5173"

    # App
    app_version: str = "0.1.0"

    model_config = {"env_prefix": "", "case_sensitive": False}


settings = Settings()
