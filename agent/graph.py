from langgraph.graph import StateGraph, START, END

from state import AgentState
from nodes.gather_context import gather_context
from nodes.plan import plan
from nodes.research import research
from nodes.combinatorics import expand_node
from nodes.quick_filter import quick_filter_node
from nodes.generate_dsl import generate_dsl
from nodes.validate import validate
from nodes.backtest import backtest
from nodes.rank import rank
from nodes.guardrails import guardrails, should_retry
from nodes.robustness import assess
from nodes.self_review import self_review
from nodes.memory_update import memory_update


def _route_after_plan(state: AgentState) -> str:
    if state["plan"].get("research_needed"):
        return "research"
    return "expand" if state["style"] == "intraday" else "direct"


def _route_after_research(state: AgentState) -> str:
    return "expand" if state["style"] == "intraday" else "direct"


def build_graph():
    g = StateGraph(AgentState)

    g.add_node("gather_context", gather_context)
    g.add_node("plan_node", plan)
    g.add_node("research_node", research)
    g.add_node("expand_node", expand_node)
    g.add_node("quick_filter_node", quick_filter_node)
    g.add_node("generate_dsl", generate_dsl)
    g.add_node("validate", validate)
    g.add_node("backtest", backtest)
    g.add_node("rank", rank)
    g.add_node("guardrails", guardrails)
    g.add_node("assess", assess)
    g.add_node("self_review", self_review)
    g.add_node("memory_update", memory_update)

    g.add_edge(START, "gather_context")
    g.add_edge("gather_context", "plan_node")
    # Swing keeps its original fixed-archetype path straight to
    # generate_dsl; intraday routes through expand_node/quick_filter_node
    # first (nodes/combinatorics.py, nodes/quick_filter.py) to turn
    # plan_node's small DimensionSelectionPlan into dozens of real
    # candidates before generate_dsl builds DSL for each one.
    g.add_conditional_edges("plan_node", _route_after_plan, {"research": "research_node", "expand": "expand_node", "direct": "generate_dsl"})
    g.add_conditional_edges("research_node", _route_after_research, {"expand": "expand_node", "direct": "generate_dsl"})
    g.add_edge("expand_node", "quick_filter_node")
    g.add_edge("quick_filter_node", "generate_dsl")
    g.add_edge("generate_dsl", "validate")
    g.add_edge("validate", "backtest")
    g.add_edge("backtest", "rank")
    g.add_edge("rank", "guardrails")
    # If nothing survived rank's quality gates or guardrails' safety/risk/
    # memory checks, retry with a fresh plan (excluding archetypes already
    # tried this run) up to max_retries times before giving up and telling
    # the user plainly that nothing was deployable — never present a
    # rejected candidate just because it's the only one left.
    g.add_conditional_edges("guardrails", should_retry, {"retry": "plan_node", "continue": "assess"})
    g.add_edge("assess", "self_review")
    g.add_edge("self_review", "memory_update")
    g.add_edge("memory_update", END)

    # No checkpointer in V1 — "Human Approval" is a separate UI action
    # (the Strategy Lab's Deploy button, after the user reviews this
    # result), not a graph-level interrupt().
    return g.compile()


GRAPH = build_graph()


def run_agent(style: str, symbol: str = "NIFTYBEES") -> dict:
    initial_state = {
        "style": style,
        "symbol": symbol,
        "task": "build_strategy",
        "retry_count": 0,
        "max_retries": 2,
        "errors": [],
    }
    return GRAPH.invoke(initial_state)
