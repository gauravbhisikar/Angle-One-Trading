package contextbuilder

import (
	"context"
	"fmt"
	"time"
)

// ContextProvider fills in its one section of DecisionContext. Modular on
// purpose: adding a new data source (e.g. a future options-flow signal)
// means writing one new provider, not touching the others.
type ContextProvider interface {
	Name() string
	Load(ctx context.Context, req BuildRequest, dc *DecisionContext) error
}

// taskSections declares which providers run for each Task — this is what
// makes contextbuilder task-aware instead of dumping everything into
// every request. Provider order matters: Market runs before Regime (which
// reads dc.Market), and Regime runs before Recommendation (which reads
// dc.Regime and dc.Lessons).
var taskSections = map[Task][]string{
	TaskBuildStrategy:    {"market", "global", "portfolio", "memory", "regime", "recommendation"},
	TaskReviewStrategy:   {"market", "global", "portfolio", "memory", "regime"},
	TaskOptimizeStrategy: {"market", "global", "portfolio", "memory", "regime", "recommendation"},
}

type Builder struct {
	providers map[string]ContextProvider
}

func NewBuilder(providers ...ContextProvider) *Builder {
	m := make(map[string]ContextProvider, len(providers))
	for _, p := range providers {
		m[p.Name()] = p
	}
	return &Builder{providers: m}
}

// Build runs exactly the providers taskSections declares for req.Task, in
// order. A provider failing doesn't abort the whole context — it's
// recorded in Warnings so the caller (and the AI) knows a section is
// missing rather than silently getting a zero value that looks like a
// real "no signal" answer.
func (b *Builder) Build(ctx context.Context, req BuildRequest) (DecisionContext, error) {
	sections, ok := taskSections[req.Task]
	if !ok {
		return DecisionContext{}, fmt.Errorf("contextbuilder: unknown task %q", req.Task)
	}

	dc := DecisionContext{BuiltAt: time.Now().UTC(), Task: req.Task, User: req.UserPreferences}

	for _, name := range sections {
		p, ok := b.providers[name]
		if !ok {
			dc.Warnings = append(dc.Warnings, fmt.Sprintf("no provider registered for section %q", name))
			continue
		}
		if err := p.Load(ctx, req, &dc); err != nil {
			dc.Warnings = append(dc.Warnings, fmt.Sprintf("%s: %v", name, err))
		}
	}
	return dc, nil
}
