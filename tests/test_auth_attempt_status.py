"""A key the provider rejects shows up in error.flexrouter.attempts with the
provider's 401, like every other HTTP failure, not a blank status."""
import httpx
import respx
from fastapi.testclient import TestClient

from flexrouter.config import FlexConfig, ModelConfig, ProviderConfig


@respx.mock
def test_a_rejected_key_reports_its_401(tmp_path, monkeypatch):
    cfg = FlexConfig(
        tiers={"smart": [ModelConfig(provider="alpha", model="big", score=99,
                                     rpm=60, tpm=60000)]},
        providers={"alpha": ProviderConfig(base_url="https://alpha.test/v1",
                                           api_keys=["k"])},
        state_dir=str(tmp_path / "state"),
    )
    monkeypatch.setattr("flexrouter._router.load_config", lambda _p: cfg)
    respx.post("https://alpha.test/v1/chat/completions").mock(
        return_value=httpx.Response(401, json={"error": {"message": "Invalid API key"}}))
    from flexrouter import app as app_mod
    app_mod.state.router = None
    client = TestClient(app_mod.create_app(str(tmp_path / "config.yaml")))

    r = client.post("/v1/chat/completions", json={
        "model": "smart", "messages": [{"role": "user", "content": "hi"}]})

    attempts = r.json()["error"]["flexrouter"]["attempts"]
    assert [a["status"] for a in attempts] == [401]
