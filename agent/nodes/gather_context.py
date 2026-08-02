from state import AgentState
import clients


def gather_context(state: AgentState) -> dict:
    dc = clients.build_context(
        task=state.get("task", "build_strategy"),
        symbol=state.get("symbol", "NIFTYBEES"),
        user_preferences={"style": state["style"]},
    )
    return {"decision_context": dc}
