"""The dashboard and /api take no key, so a web page the owner visits must
not be able to drive them from the owner's browser: reading the request log,
or POSTing /api/config to point a provider's base_url at itself and collect
that provider's key on the next request.
"""
from fastapi.testclient import TestClient

from flexrouter.app import create_app
from flexrouter.config import FlexConfig, ModelConfig, ProviderConfig
from flexrouter.overrides import load_overrides


def _client(tmp_path, monkeypatch):
    cfg = FlexConfig(
        tiers={"smart": [ModelConfig(provider="alpha", model="big", score=99,
                                     rpm=60, tpm=60000)]},
        providers={"alpha": ProviderConfig(base_url="https://alpha.test/v1",
                                           api_keys=["k"])},
        state_dir=str(tmp_path / "state"),
    )
    monkeypatch.setattr("flexrouter._router.load_config", lambda _p: cfg)
    from flexrouter import app as app_mod
    app_mod.state.router = None
    return TestClient(create_app(str(tmp_path / "config.yaml")),
                      base_url="http://localhost:4891")


EVIL = {"Origin": "https://evil.example"}
REPOINT = '{"providers": {"alpha": {"base_url": "https://evil.example/v1"}}}'


def test_another_site_cannot_repoint_a_provider(tmp_path, monkeypatch):
    # text/plain is what a page can send without a CORS preflight.
    r = _client(tmp_path, monkeypatch).post(
        "/api/config", content=REPOINT,
        headers={**EVIL, "Content-Type": "text/plain"})
    assert r.status_code == 403
    assert "providers" not in load_overrides()


def test_another_site_cannot_read_the_request_log(tmp_path, monkeypatch):
    r = _client(tmp_path, monkeypatch).get("/api/requests", headers=EVIL)
    assert r.status_code == 403


def test_another_site_cannot_post_a_dashboard_form(tmp_path, monkeypatch):
    r = _client(tmp_path, monkeypatch).post(
        "/settings/failover_budget_seconds", data={"value": "1"}, headers=EVIL)
    assert r.status_code == 403


def test_another_local_port_counts_as_another_site(tmp_path, monkeypatch):
    r = _client(tmp_path, monkeypatch).get(
        "/api/requests", headers={"Origin": "http://localhost:3000"})
    assert r.status_code == 403


def test_a_dns_rebound_name_is_refused_even_when_it_matches_host(tmp_path, monkeypatch):
    r = _client(tmp_path, monkeypatch).get(
        "/api/requests",
        headers={"Origin": "http://rebind.example:4891", "Host": "rebind.example:4891"})
    assert r.status_code == 403


def test_the_dashboard_itself_still_works(tmp_path, monkeypatch):
    client = _client(tmp_path, monkeypatch)
    own = {"Origin": "http://localhost:4891"}
    assert client.get("/api/requests", headers=own).status_code == 200
    r = client.post("/api/config", content=REPOINT,
                    headers={**own, "Content-Type": "application/json"})
    assert r.status_code == 200


def test_requests_without_an_origin_are_not_from_another_site(tmp_path, monkeypatch):
    # curl, scripts, and a plain link to the dashboard send no Origin.
    assert _client(tmp_path, monkeypatch).get("/api/requests").status_code == 200


def test_v1_stays_open_to_browser_clients_on_other_origins(tmp_path, monkeypatch):
    client = _client(tmp_path, monkeypatch)
    assert client.get("/v1/models", headers=EVIL).status_code == 200
    assert client.get("/models", headers=EVIL).status_code == 200
