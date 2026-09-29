from __future__ import annotations
from dataclasses import dataclass, field
import tiktoken

_TOKEN_ENCODING = "cl100k_base"  # GPT-4 tokenizer; used as a universal approximation

# tiktoken downloads the encoding on first use. Loading it at import time made
# `import flexrouter` (and so every CLI command, the server and the tests)
# crash on a machine with no internet or behind a proxy that blocks the
# download. It is loaded on first count instead, and if it can't be loaded the
# count falls back to the usual ~4 characters per token.
_ENC = None
_ENC_UNAVAILABLE = False


def count_tokens(text: str) -> int:
    global _ENC, _ENC_UNAVAILABLE
    if not text:
        return 0
    if _ENC is None and not _ENC_UNAVAILABLE:
        try:
            _ENC = tiktoken.get_encoding(_TOKEN_ENCODING)
        except Exception:
            _ENC_UNAVAILABLE = True
    if _ENC is None:
        return (len(text) + 3) // 4
    return len(_ENC.encode(text))


@dataclass
class HookContext:
    messages: list[dict]
    hooks: list[str]
    token_counts: dict = field(default_factory=dict)
    vision: bool = False
    estimated_tokens: int = 0


class HookRunner:
    _HOOKS = {"detect_vision", "estimate_tokens"}

    def run(self, ctx: HookContext) -> HookContext:
        for hook in ctx.hooks:
            if hook not in self._HOOKS:
                raise ValueError(f"Unknown hook: {hook!r}. Available: {sorted(self._HOOKS)}")
            getattr(self, f"_hook_{hook}")(ctx)
        return ctx

    def _hook_detect_vision(self, ctx: HookContext) -> None:
        for msg in ctx.messages:
            content = msg.get("content", "")
            if isinstance(content, list):
                for part in content:
                    if isinstance(part, dict) and part.get("type") == "image_url":
                        ctx.vision = True
                        return

    def _hook_estimate_tokens(self, ctx: HookContext) -> None:
        total = 0
        for msg in ctx.messages:
            content = msg.get("content", "")
            if isinstance(content, str):
                total += count_tokens(content)
            elif isinstance(content, list):
                for part in content:
                    if isinstance(part, dict) and part.get("type") == "text":
                        total += count_tokens(part.get("text", ""))
        ctx.estimated_tokens = total
