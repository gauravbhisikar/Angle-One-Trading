// Package sentiment scores text (news headlines, and anything else
// text-shaped) for bullish/bearish tone using a small finance-tuned
// lexicon — see lexicon.go's doc for exactly what this is and isn't
// (a lighter, honestly-labeled cousin of VADER, not the real VADER word
// list). Deliberately NOT wired as a buy/sell signal on its own — the
// package only produces a score; whether/how it factors into a decision
// is the agent's job, per this project's standing rule that connectors
// fetch data and the engine/agent decide what it means.
package sentiment

import (
	"regexp"
	"strings"
)

type Label string

const (
	Bullish Label = "bullish"
	Neutral Label = "neutral"
	Bearish Label = "bearish"
)

type Result struct {
	Score float64 // roughly -1 (very bearish) to +1 (very bullish), unbounded in practice
	Label Label
	Words int // how many polarity words were found — 0 means "no signal, not neutral-by-conviction"
}

var tokenRe = regexp.MustCompile(`[a-zA-Z']+`)

// Score tokenizes text and sums word polarities, applying a negator to
// flip the sign of the next word and a booster to scale it — the same
// basic mechanism VADER uses, simplified.
func Score(text string) Result {
	tokens := tokenRe.FindAllString(strings.ToLower(text), -1)

	var sum float64
	var hits int
	negateNext := 0
	boostNext := 1.0

	for _, tok := range tokens {
		if negators[tok] {
			negateNext = 3 // negation window: flips up to the next 3 polarity words
			continue
		}
		if b, ok := boosters[tok]; ok {
			boostNext = b
			continue
		}

		polarity, found := positiveWords[tok]
		if !found {
			polarity, found = negativeWords[tok]
		}
		if found {
			hits++
			if negateNext > 0 {
				polarity = -polarity
			}
			sum += polarity * boostNext
			boostNext = 1.0
		}
		if negateNext > 0 {
			negateNext--
		}
	}

	res := Result{Score: sum, Words: hits}
	switch {
	case hits == 0:
		res.Label = Neutral
	case sum > 0.5:
		res.Label = Bullish
	case sum < -0.5:
		res.Label = Bearish
	default:
		res.Label = Neutral
	}
	return res
}
