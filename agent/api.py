import json
import os
import uuid

from fastapi import FastAPI, HTTPException
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import StreamingResponse
from pydantic import BaseModel
from dotenv import load_dotenv

load_dotenv(os.path.join(os.path.dirname(__file__), "..", ".env"))

from graph import run_agent, GRAPH
import clients

NODE_LABELS = {
    "gather_context": "Gathering market/portfolio/memory context",
    "plan_node": "Planning candidate strategies",
    "research_node": "Researching supporting news/RBI signals",
    "generate_dsl": "Assembling DSL from templates",
    "validate": "Validating DSL against engine rules",
    "backtest": "Backtesting candidates",
    "rank": "Ranking candidates by risk-adjusted score + quality gates",
    "guardrails": "Running safety/risk/memory guardrails",
    "assess": "Stress-testing: Monte Carlo, parameter stability, confidence score",
    "self_review": "Writing rationale grounded in real numbers",
    "memory_update": "Saving results to memory",
}

app = FastAPI(title="NIFTYBEES Strategy Agent")

app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_methods=["*"],
    allow_headers=["*"],
)


class GenerateRequest(BaseModel):
    style: str  # "swing" | "intraday"
    symbol: str = "NIFTYBEES"


class DeployRequest(BaseModel):
    dsl: dict


@app.get("/health")
def health():
    return {"status": "ok"}


@app.post("/generate")
def generate(req: GenerateRequest):
    if req.style not in ("swing", "intraday"):
        raise HTTPException(400, "style must be 'swing' or 'intraday'")

    state = run_agent(req.style, req.symbol)
    return _result_payload(req.style, state)


def _result_payload(style: str, state: dict) -> dict:
    return {
        "style": style,
        "llm_used": state.get("llm_used", False),
        "description": state.get("description", ""),
        "rationale": state.get("rationale", ""),
        "evidence": state.get("evidence", {}),
        "selected": state.get("selected"),
        "ranked": state.get("ranked", []),
        "research_findings": state.get("research_findings", []),
        "decision_context": state.get("decision_context", {}),
        "guardrail_reasons": state.get("guardrail_reasons", []),
        "retry_count": state.get("retry_count", 0),
        "errors": state.get("errors", []),
    }


@app.post("/generate/stream")
def generate_stream(req: GenerateRequest):
    """Same graph as /generate, but emits one Server-Sent Event per node as
    it completes (LangGraph's stream_mode='updates'), so the Strategy Lab
    can show real progress instead of a static spinner. Ends with a
    {"type":"done","result":...} event carrying the same payload /generate
    returns."""
    if req.style not in ("swing", "intraday"):
        raise HTTPException(400, "style must be 'swing' or 'intraday'")

    def event_stream():
        initial_state = {
            "style": req.style, "symbol": req.symbol, "task": "build_strategy",
            "retry_count": 0, "max_retries": 2, "errors": [],
        }
        final_state = {}
        try:
            # Two stream modes at once: "updates" (one event per node, for
            # the step-by-step progress list) and "custom" (backtest.py's
            # per-candidate writer() calls — real "testing X... done" events
            # as they happen, not a single opaque "backtest" step).
            for mode, payload in GRAPH.stream(initial_state, stream_mode=["updates", "custom"]):
                if mode == "custom":
                    # payload already carries its own "type" (e.g.
                    # "backtest_candidate" from backtest.py's writer calls)
                    yield f"data: {json.dumps(payload)}\n\n"
                    continue
                for node_name, partial in payload.items():
                    # LangGraph's "updates" stream mode represents a node
                    # that returned a truly empty dict (no keys changed —
                    # memory_update always does) as bare None, not {}.
                    # GRAPH.invoke() never surfaces this since dict.update({})
                    # is a no-op either way; .stream() does, so it must be
                    # guarded here. Confirmed live (2026-08-02): reproduced
                    # 100% on memory_update, the only node with zero keys.
                    final_state.update(partial or {})
                    label = NODE_LABELS.get(node_name, node_name)
                    yield f"data: {json.dumps({'type': 'progress', 'node': node_name, 'label': label})}\n\n"
        except Exception as e:
            # A node crashed mid-run — every earlier node's output (context,
            # candidates, backtest results, DSL) is still real and already
            # in final_state. Throwing that away over one failed later step
            # would be strictly worse than showing it with an honest error
            # note, so degrade instead of discarding.
            import traceback
            traceback.print_exc()
            final_state.setdefault("errors", []).append(f"pipeline stopped early: {e}")

        result = _result_payload(req.style, final_state)
        yield f"data: {json.dumps({'type': 'done', 'result': result})}\n\n"

    return StreamingResponse(event_stream(), media_type="text/event-stream")


@app.post("/deploy")
def deploy(req: DeployRequest):
    """Deploys a generated candidate straight into paper trading — creates
    the strategy in the engine then starts it running, so the Strategy
    Lab's Deploy button shows live paper-trade performance, not just a
    saved draft."""
    dsl = req.dsl
    try:
        created = clients.create_strategy(dsl)
        strategy_id = created.get("strategy_id", dsl.get("strategy_id"))
        run_result = clients.run_strategy(strategy_id)
    except Exception as e:
        raise HTTPException(502, f"deploy failed: {e}")

    try:
        clients.save_deployment_memory({
            "DeploymentID": str(uuid.uuid4()),
            "StrategyID": strategy_id,
            "Version": created.get("strategy_version", dsl.get("strategy_version", 1)),
            "Mode": "paper",
            "Status": "running",
        })
    except Exception:
        pass

    return {"strategy_id": strategy_id, "status": "running", "run_result": run_result}


if __name__ == "__main__":
    import uvicorn
    port = int(os.getenv("AGENT_PORT", "9091"))
    uvicorn.run(app, host="0.0.0.0", port=port)
