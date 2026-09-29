"""A preferred model whose credit or plan quota ran out comes back on its
own: it is tried again after `needs_you_recheck_minutes` (30 by default),
instead of waiting for the owner to press Retry."""
import httpx
import respx
from fastapi.testclient import TestClient

from flexrouter.config import FlexConfig, ModelConfig, ProviderConfig
from flexrouter.status import StatusStore

OK = {"choices": [{"message": {"role": "assistant", "content": "hi"}}],
      "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}}


def _client(tmp_path, monkeypatch, **settings):
    cfg = FlexConfig(
        tiers={"main": [
            ModelConfig(provider="alpha", model="best", score=95, rpm=600, tpm=10**6),
            ModelConfig(provider="beta", model="backup", score=50, rpm=600, tpm=10**6),
        ]},
        providers={"alpha": ProviderConfig(base_url="https://alpha.test/v1", api_keys=["a"]),
                   "beta": ProviderConfig(base_url="https://beta.test/v1", api_keys=["b"])},
        state_dir=str(tmp_path / "state"),
        **settings,
    )
    monkeypatch.setattr("flexrouter._router.load_config", lambda _p: cfg)
    from flexrouter import app as app_mod
    app_mod.state.router = None
    return TestClient(app_mod.create_app(str(tmp_path / "config.yaml")))


def _answered_by(client):
    r = client.post("/v1/chat/completions", json={
        "model": "main", "messages": [{"role": "user", "content": "hi"}]})
    assert r.status_code == 200, r.text
    return r.json()["model"]


@respx.mock
def test_a_used_up_preferred_model_is_tried_again_after_30_minutes(
        tmp_path, monkeypatch, virtual_clock):
    alpha = respx.post("https://alpha.test/v1/chat/completions").mock(side_effect=[
        httpx.Response(402, json={"error": {"message": "Insufficient credits"}}),
        httpx.Response(200, json={**OK, "model": "best"}),
    ])
    respx.post("https://beta.test/v1/chat/completions").mock(
        return_value=httpx.Response(200, json={**OK, "model": "backup"}))
    client = _client(tmp_path, monkeypatch)

    assert _answered_by(client) == "backup"      # alpha out of credit: fails over
    virtual_clock.advance(29 * 60)
    assert _answered_by(client) == "backup"      # not rechecked yet
    assert alpha.call_count == 1

    virtual_clock.advance(2 * 60)
    assert _answered_by(client) == "best"        # rechecked and back
    assert alpha.call_count == 2


@respx.mock
def test_still_used_up_after_the_recheck_waits_another_interval(
        tmp_path, monkeypatch, virtual_clock):
    alpha = respx.post("https://alpha.test/v1/chat/completions").mock(
        return_value=httpx.Response(402, json={"error": {"message": "Insufficient credits"}}))
    respx.post("https://beta.test/v1/chat/completions").mock(
        return_value=httpx.Response(200, json={**OK, "model": "backup"}))
    client = _client(tmp_path, monkeypatch)

    assert _answered_by(client) == "backup"
    virtual_clock.advance(31 * 60)
    assert _answered_by(client) == "backup"      # one recheck, still out
    assert _answered_by(client) == "backup"      # and not hammered again
    assert alpha.call_count == 2


@respx.mock
def test_zero_turns_the_recheck_off(tmp_path, monkeypatch, virtual_clock):
    alpha = respx.post("https://alpha.test/v1/chat/completions").mock(
        return_value=httpx.Response(402, json={"error": {"message": "Insufficient credits"}}))
    respx.post("https://beta.test/v1/chat/completions").mock(
        return_value=httpx.Response(200, json={**OK, "model": "backup"}))
    client = _client(tmp_path, monkeypatch, needs_you_recheck_minutes=0)

    _answered_by(client)
    virtual_clock.advance(24 * 60 * 60)
    _answered_by(client)
    assert alpha.call_count == 1


def test_a_rejected_key_or_missing_model_is_never_rechecked(tmp_path):
    store = StatusStore(str(tmp_path), recheck_seconds=lambda: 1800)
    store.set_needs_you("alpha", "best", "rejected", kind="bad_key", action=None)
    store.set_needs_you("alpha", "gone", "no such model", kind="gone", action=None)
    store.set_needs_you("alpha", "broke", "balance", kind="balance_empty", action=None)

    assert store.get("alpha", "best").until is None
    assert store.get("alpha", "gone").until is None
    assert store.get("alpha", "broke").until is not None
