package api

import (
	"net/http"
	"time"

	"tradingengine/internal/marketsession"
)

type MarketStatus struct {
	Open     bool   `json:"open"`
	Reason   string `json:"reason"`
	ISTTime  string `json:"ist_time"`
	NextOpen string `json:"next_open,omitempty"`
}

func (s *Server) handleMarketStatus(w http.ResponseWriter, r *http.Request) {
	st := marketsession.Current(time.Now())
	writeJSON(w, http.StatusOK, MarketStatus{Open: st.Open, Reason: st.Reason, ISTTime: st.ISTTime, NextOpen: st.NextOpen})
}

type MarketPrice struct {
	Symbol string `json:"symbol"`
	Price  string `json:"price,omitempty"`
	Known  bool   `json:"known"` // false if no candle has arrived yet for this symbol
}

// handleMarketPrice reads the same last-candle-close the paper broker
// fills orders against (see cmd/engine/main.go's priceLookup) — never a
// second, independently-fetched price that could drift from what the
// engine actually executed against.
func (s *Server) handleMarketPrice(w http.ResponseWriter, r *http.Request) {
	symbol := r.PathValue("symbol")
	if s.PriceLookup == nil {
		writeJSON(w, http.StatusOK, MarketPrice{Symbol: symbol, Known: false})
		return
	}
	price, ok := s.PriceLookup(symbol)
	if !ok {
		writeJSON(w, http.StatusOK, MarketPrice{Symbol: symbol, Known: false})
		return
	}
	writeJSON(w, http.StatusOK, MarketPrice{Symbol: symbol, Price: price.String(), Known: true})
}
