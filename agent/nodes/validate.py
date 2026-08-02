from state import AgentState
import clients


def validate(state: AgentState) -> dict:
    candidates = state["candidates"]
    validated = []
    for c in candidates:
        try:
            result = clients.validate_strategy(c["dsl"])
            c["valid"] = bool(result.get("valid", False))
            c["validation_errors"] = result.get("errors", [])
        except Exception as e:
            c["valid"] = False
            c["validation_errors"] = [str(e)]
        validated.append(c)
    return {"candidates": validated}
