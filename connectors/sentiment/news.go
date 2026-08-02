package sentiment

import "connectors/news"

type ScoredHeadline struct {
	news.Headline
	Sentiment Result
}

type Aggregate struct {
	Bullish, Neutral, Bearish int
	AverageScore              float64
	Label                     Label
	Scored                    []ScoredHeadline
}

// ScoreHeadlines runs Score over every headline's title (headlines carry
// no body text from the RSS feeds this connects to — title-only scoring
// is a real limitation, not hidden: a one-line headline gives the lexicon
// far less to work with than a full article).
func ScoreHeadlines(headlines []news.Headline) Aggregate {
	agg := Aggregate{Scored: make([]ScoredHeadline, 0, len(headlines))}
	var sum float64
	var scoredCount int

	for _, h := range headlines {
		r := Score(h.Title)
		agg.Scored = append(agg.Scored, ScoredHeadline{Headline: h, Sentiment: r})
		switch r.Label {
		case Bullish:
			agg.Bullish++
		case Bearish:
			agg.Bearish++
		default:
			agg.Neutral++
		}
		if r.Words > 0 {
			sum += r.Score
			scoredCount++
		}
	}

	if scoredCount > 0 {
		agg.AverageScore = sum / float64(scoredCount)
	}
	switch {
	case agg.AverageScore > 0.3:
		agg.Label = Bullish
	case agg.AverageScore < -0.3:
		agg.Label = Bearish
	default:
		agg.Label = Neutral
	}
	return agg
}
