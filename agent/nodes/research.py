from state import AgentState
import clients


def research(state: AgentState) -> dict:
    plan = state["plan"]
    if not plan.get("research_needed"):
        return {"research_findings": []}

    findings = []
    for q in plan.get("research_queries", [])[:3]:
        findings.extend(clients.research(q, max_results=3))
    return {"research_findings": findings}
