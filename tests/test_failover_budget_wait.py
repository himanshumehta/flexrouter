"""A bucket that is full for longer than the failover budget has left gives
up at once. It used to sleep the whole wait - a full rate window, whatever
the budget - and then give up without trying anything."""
import httpx
import pytest
import respx
from fastapi.testclient import TestClient

from flexrouter.config import FlexConfig, ModelConfig, ProviderConfig

OK = {"choices": [{"message": {"role": "assistant", "content": "hi"}}],
      "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}}


def _client(tmp_path, monkeypatch, budget):
    cfg = FlexConfig(
        tiers={"smart": [ModelConfig(provider="alpha", model="big", score=99,
                                     rpm=1, tpm=60000)]},
        providers={"alpha": ProviderConfig(base_url="https://alpha.test/v1",
                                           api_keys=["k"])},
        state_dir=str(tmp_path / "state"),
        failover_budget_seconds=budget,
    )
    monkeypatch.setattr("flexrouter._router.load_config", lambda _p: cfg)
    from flexrouter import app as app_mod
    app_mod.state.router = None
    return TestClient(app_mod.create_app(str(tmp_path / "config.yaml")))


def _ask(client, stream=False):
    return client.post("/v1/chat/completions", json={
        "model": "smart", "stream": stream,
        "messages": [{"role": "user", "content": "hi"}]})


@pytest.mark.parametrize("stream", [False, True])
@respx.mock
def test_a_wait_longer_than_the_budget_gives_up_without_sleeping_it(
        tmp_path, monkeypatch, virtual_clock, stream):
    respx.post("https://alpha.test/v1/chat/completions").mock(
        return_value=httpx.Response(200, json=OK))
    client = _client(tmp_path, monkeypatch, budget=5)
    assert _ask(client).status_code == 200  # uses the one request this minute

    before = virtual_clock.offset
    r = _ask(client, stream=stream)
    slept = virtual_clock.offset - before

    assert slept <= 5
    if stream:
        assert "failover budget" in r.text
    else:
        assert r.status_code == 503


@respx.mock
def test_a_wait_inside_the_budget_is_still_waited_out(tmp_path, monkeypatch, virtual_clock):
    respx.post("https://alpha.test/v1/chat/completions").mock(
        return_value=httpx.Response(200, json=OK))
    client = _client(tmp_path, monkeypatch, budget=120)
    assert _ask(client).status_code == 200

    before = virtual_clock.offset
    r = _ask(client)

    assert r.status_code == 200
    assert 0 < virtual_clock.offset - before <= 120
