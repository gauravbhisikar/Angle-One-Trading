# Single place to build a memory lesson key — was independently duplicated
# as f"{archetype}_{style}" at 3 call sites (memory_update.py, plan.py's
# _avoid_archetypes, robustness.py's assess). memory.RecordLesson/GetLessons
# (the Go side, memory/lesson.go) treat this key as a fully opaque string
# end-to-end (Python dict -> JSON -> Go handler -> SQL TEXT PRIMARY KEY) —
# no schema change needed to extend the convention, which is why this stays
# a plain Python helper rather than moving key-construction into Go.
def lesson_key(archetype: str, style: str, regime: str | None = None) -> str:
    if regime:
        return f"{archetype}_{style}_{regime}"
    return f"{archetype}_{style}"
