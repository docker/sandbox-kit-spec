"""Official Mem0 API with the Kit's MCP transport mounted at /mcp."""

from contextlib import asynccontextmanager

from main import app
from mcp_tools import create_mcp_app

mcp_app = create_mcp_app()
api_lifespan = app.router.lifespan_context


@asynccontextmanager
async def lifespan(application):
    async with api_lifespan(application):
        async with mcp_app.router.lifespan_context(mcp_app):
            yield


app.router.lifespan_context = lifespan


async def mcp_transport(scope, receive, send):
    await mcp_app(scope, receive, send)


# The API's concrete routes win first. The catch-all mount then serves
# the MCP app's /mcp route without changing the official REST surface.
app.mount("/", mcp_transport)
