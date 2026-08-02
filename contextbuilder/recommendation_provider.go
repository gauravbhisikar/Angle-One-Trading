package contextbuilder

import (
	"context"
	"fmt"
	"strings"
)

// RecommendationProvider derives recommended/avoid styles from a fixed
// rule over Regime, then cross-checks memory's Lessons — a lesson with
// low confidence (net more failures than successes) and enough
// observations to be meaningful overrides the regime-only guess. Must
// run after RegimeProvider and MemoryProvider.
type RecommendationProvider struct{}

func NewRecommendationProvider() *RecommendationProvider { return &RecommendationProvider{} }

func (p *RecommendationProvider) Name() string { return "recommendation" }

var regimePlaybook = map[string]struct {
	recommend []string
	avoid     []string
}{
	"bull":     {recommend: []string{"momentum", "trend_following"}, avoid: []string{"mean_reversion"}},
	"bear":     {recommend: []string{"trend_following"}, avoid: []string{"momentum", "breakout"}}, // long-only in this build (DSL_SPEC Sec 8) — momentum/breakout need upside continuation bear markets don't give
	"sideways": {recommend: []string{"mean_reversion"}, avoid: []string{"momentum", "trend_following", "breakout"}},
}

const lessonConfidenceThreshold = 0.35
const lessonMinObservations = 5

func (p *RecommendationProvider) Load(ctx context.Context, req BuildRequest, dc *DecisionContext) error {
	play, ok := regimePlaybook[dc.Regime.Regime]
	if !ok {
		return nil
	}
	recommend := append([]string{}, play.recommend...)
	avoid := append([]string{}, play.avoid...)
	reasons := []string{fmt.Sprintf("regime=%s (%s)", dc.Regime.Regime, dc.Regime.Basis)}

	for _, lesson := range dc.Lessons {
		if lesson.TimesSeen < lessonMinObservations || lesson.Confidence >= lessonConfidenceThreshold {
			continue
		}
		lowerDesc := strings.ToLower(lesson.Description)
		for _, style := range append([]string{}, recommend...) {
			if strings.Contains(lowerDesc, strings.ReplaceAll(style, "_", " ")) {
				recommend = removeString(recommend, style)
				avoid = appendUnique(avoid, style)
				reasons = append(reasons, fmt.Sprintf("moved %q to avoid: lesson %q (%d/%d confidence, seen %d times)",
					style, lesson.Description, int(lesson.Confidence*100), 100, lesson.TimesSeen))
			}
		}
	}

	dc.Recommendations = RecommendationContext{RecommendedStyles: recommend, Avoid: avoid, Reasons: reasons}
	return nil
}

func removeString(list []string, target string) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		if s != target {
			out = append(out, s)
		}
	}
	return out
}
func appendUnique(list []string, target string) []string {
	for _, s := range list {
		if s == target {
			return list
		}
	}
	return append(list, target)
}
