"""Counting tokens must not need the internet. tiktoken downloads its
encoding on first use; loading it at import time made `import flexrouter`
crash on any machine that could not reach the download."""
from flexrouter import hooks
from flexrouter.hooks import HookContext, HookRunner


def test_counting_falls_back_when_the_encoding_cannot_load(monkeypatch):
    def unreachable(_name):
        raise OSError("no network")

    monkeypatch.setattr(hooks, "_ENC", None)
    monkeypatch.setattr(hooks, "_ENC_UNAVAILABLE", False)
    monkeypatch.setattr(hooks.tiktoken, "get_encoding", unreachable)

    ctx = HookRunner().run(HookContext(
        messages=[{"role": "user", "content": "x" * 40}], hooks=["estimate_tokens"]))

    assert ctx.estimated_tokens == 10


def test_empty_text_counts_as_nothing():
    assert hooks.count_tokens("") == 0
