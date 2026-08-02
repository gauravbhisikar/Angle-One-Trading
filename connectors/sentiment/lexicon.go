package sentiment

// Finance-tuned polarity lexicon, curated for market/news headlines —
// NOT the published VADER word list (reproducing that accurately from
// memory risked silently mislabeling it as authoritative when it might be
// subtly wrong). This is a lighter lexicon built the same way VADER
// works — sum per-word polarity, adjust for negation and intensity — but
// honestly labeled as its own thing, tuned for financial vocabulary
// VADER's general-purpose list doesn't cover well (e.g. "downgrade",
// "beat estimates", "circuit", "FII outflow").
var positiveWords = map[string]float64{
	"rally": 2, "rallies": 2, "rallying": 2, "surge": 2.5, "surges": 2.5, "surging": 2.5,
	"soar": 2.5, "soars": 2.5, "soaring": 2.5, "jump": 1.5, "jumps": 1.5, "jumped": 1.5,
	"gain": 1.5, "gains": 1.5, "gained": 1.5, "gaining": 1.5, "rise": 1.2, "rises": 1.2, "risen": 1.2, "rising": 1.2,
	"bullish": 2.5, "bull": 1.5, "upbeat": 1.5, "optimistic": 1.5, "optimism": 1.5,
	"upgrade": 2, "upgraded": 2, "upgrades": 2, "outperform": 2, "outperforms": 2,
	"beat": 1.5, "beats": 1.5, "beating": 1.5, "exceeds": 1.5, "exceeded": 1.5,
	"strong": 1.2, "stronger": 1.5, "strength": 1.2, "robust": 1.5, "solid": 1.2,
	"record": 1.5, "high": 0.8, "highs": 0.8, "recovery": 1.2, "recovers": 1.2, "rebound": 1.5, "rebounds": 1.5,
	"boost": 1.5, "boosts": 1.5, "boosted": 1.5, "growth": 1.2, "growing": 1,
	"profit": 1.2, "profits": 1.2, "profitable": 1.2, "positive": 1.2, "buy": 1, "buying": 1,
	"inflow": 1.5, "inflows": 1.5, "advance": 1, "advances": 1, "advancing": 1,
	"breakout": 1.5, "breaks out": 1.5, "momentum": 1, "buoyant": 1.5, "cheer": 1.2, "cheers": 1.2,
	"win": 1, "wins": 1, "winning": 1, "success": 1.2, "successful": 1.2, "expansion": 1,
}

var negativeWords = map[string]float64{
	"crash": -2.5, "crashes": -2.5, "crashing": -2.5, "plunge": -2.5, "plunges": -2.5, "plunging": -2.5,
	"slump": -2, "slumps": -2, "slumping": -2, "tumble": -2, "tumbles": -2, "tumbling": -2,
	"fall": -1.2, "falls": -1.2, "fallen": -1.2, "falling": -1.2, "drop": -1.2, "drops": -1.2, "dropped": -1.2,
	"decline": -1.2, "declines": -1.2, "declining": -1.2, "sink": -1.5, "sinks": -1.5, "sinking": -1.5,
	"bearish": -2.5, "bear": -1.5, "pessimistic": -1.5, "pessimism": -1.5,
	"downgrade": -2, "downgraded": -2, "downgrades": -2, "underperform": -2, "underperforms": -2,
	"miss": -1.5, "misses": -1.5, "missed": -1.5, "weak": -1.2, "weaker": -1.5, "weakness": -1.2,
	"low": -0.8, "lows": -0.8, "loss": -1.5, "losses": -1.5, "losing": -1.2,
	"selloff": -2, "sell-off": -2, "outflow": -1.5, "outflows": -1.5, "sell": -1, "selling": -1,
	"correction": -1.5, "crisis": -2, "recession": -2.5, "slowdown": -1.5, "concerns": -1.2, "concern": -1.2,
	"worry": -1.2, "worries": -1.2, "worried": -1.2, "fear": -1.5, "fears": -1.5, "risk": -0.8, "risks": -0.8,
	"volatile": -1, "volatility": -1, "turmoil": -2, "plummet": -2.5, "plummets": -2.5,
	"cut": -1, "cuts": -1, "cutting": -1, "warns": -1.5, "warning": -1.5, "warned": -1.5,
	"decline in": -1.2, "circuit": -1, "halt": -1.2, "halted": -1.2, "default": -2, "bankruptcy": -2.5,
}

// negators flip the polarity of the next 1-3 words.
var negators = map[string]bool{
	"not": true, "no": true, "never": true, "without": true, "n't": true, "isn't": true,
	"doesn't": true, "won't": true, "didn't": true, "hasn't": true, "unable": true,
}

// boosters scale up (or down, if "slightly"/"marginally") the following word's polarity.
var boosters = map[string]float64{
	"very": 1.4, "extremely": 1.6, "highly": 1.3, "significantly": 1.4, "sharply": 1.5,
	"massively": 1.6, "strongly": 1.4, "slightly": 0.6, "marginally": 0.5, "somewhat": 0.7,
}
