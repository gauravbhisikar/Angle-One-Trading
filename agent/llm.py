"""LLM client via OpenRouter (OpenAI-compatible API).

get_llm() returns None if OPENROUTER_API_KEY isn't set — every node that
needs the LLM must handle that case with an explicit, honestly-labeled
fallback (state["llm_used"] = False), never silently pretend a template
is AI reasoning. This lets the whole graph be exercised end-to-end before
a real key exists, per docs/agent testing needs.
"""
import os
from functools import lru_cache

from dotenv import load_dotenv

# Root .env, not agent/.env — one config file for the whole project.
load_dotenv(os.path.join(os.path.dirname(__file__), "..", ".env"))

OPENROUTER_API_KEY = os.getenv("OPENROUTER_API_KEY", "")
OPENROUTER_MODEL = os.getenv("OPENROUTER_MODEL", "deepseek/deepseek-v4-pro")
OPENROUTER_BASE_URL = "https://openrouter.ai/api/v1"


@lru_cache(maxsize=1)
def get_llm():
    """Returns a configured ChatOpenAI pointed at OpenRouter, or None if
    no API key is set yet. Callers must check for None."""
    if not OPENROUTER_API_KEY:
        return None
    from langchain_openai import ChatOpenAI

    return ChatOpenAI(
        model=OPENROUTER_MODEL,
        api_key=OPENROUTER_API_KEY,
        base_url=OPENROUTER_BASE_URL,
        temperature=0.2,
    )


def invoke_structured(llm, schema, messages, max_retries=2):
    """with_structured_output retried a few times before giving up —
    OpenRouter models occasionally emit malformed JSON (garbled/duplicated
    text mid-argument), which raises OutputParserException. Returns the
    parsed schema instance, or None if every attempt failed (caller must
    fall back to a deterministic template, same as the no-LLM path)."""
    structured_llm = llm.with_structured_output(schema)
    last_err = None
    for _ in range(max_retries + 1):
        try:
            return structured_llm.invoke(messages)
        except Exception as e:
            last_err = e
    return None
