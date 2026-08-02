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
