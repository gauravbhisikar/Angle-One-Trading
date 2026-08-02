package featurestore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// SaveRows upserts computed technical features — safe to call repeatedly
// with overlapping date ranges (e.g. after Refresh-ing historical data),
// same idempotent-upsert pattern as connectors/store.
func (s *Store) SaveRows(ctx context.Context, rows []FeatureRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339)
	for _, r := range rows {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO features (symbol, date, close, rsi14, ema20, ema50, ema_cross_bullish, ema_cross_bearish,
			 macd_bullish, macd_bearish, adx14, atr14, bollinger_percent_b, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(symbol, date) DO UPDATE SET
			   close=excluded.close, rsi14=excluded.rsi14, ema20=excluded.ema20, ema50=excluded.ema50,
			   ema_cross_bullish=excluded.ema_cross_bullish, ema_cross_bearish=excluded.ema_cross_bearish,
			   macd_bullish=excluded.macd_bullish, macd_bearish=excluded.macd_bearish,
			   adx14=excluded.adx14, atr14=excluded.atr14, bollinger_percent_b=excluded.bollinger_percent_b,
			   updated_at=excluded.updated_at`,
			r.Symbol, r.Date, r.Close.String(), r.RSI14.String(), r.EMA20.String(), r.EMA50.String(),
			boolPtrToNullInt(r.EMACrossBullish), boolPtrToNullInt(r.EMACrossBearish),
			boolPtrToNullInt(r.MACDBullish), boolPtrToNullInt(r.MACDBearish),
			r.ADX14.String(), r.ATR14.String(), r.BollingerPercentB.String(), now,
		)
		if err != nil {
			return fmt.Errorf("featurestore: save row %s %s: %w", r.Symbol, r.Date, err)
		}
	}
	return tx.Commit()
}

func boolPtrToNullInt(b *bool) interface{} {
	if b == nil {
		return nil
	}
	if *b {
		return 1
	}
	return 0
}

// MacroSnapshot is one day's macro/sentiment context for a symbol —
// sparse by design (see package doc: these connectors don't have free
// historical series, only current-day snapshots).
type MacroSnapshot struct {
	Symbol         string
	Date           string
	VIX            string
	FIINet         string
	DIINet         string
	BreadthADRatio string
	NewsSentiment  string
	NewsScore      string
}

func (s *Store) UpsertMacro(ctx context.Context, m MacroSnapshot) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO features (symbol, date, vix, fii_net, dii_net, breadth_ad_ratio, news_sentiment, news_score, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(symbol, date) DO UPDATE SET
		   vix=excluded.vix, fii_net=excluded.fii_net, dii_net=excluded.dii_net,
		   breadth_ad_ratio=excluded.breadth_ad_ratio, news_sentiment=excluded.news_sentiment,
		   news_score=excluded.news_score, updated_at=excluded.updated_at`,
		m.Symbol, m.Date, m.VIX, m.FIINet, m.DIINet, m.BreadthADRatio, m.NewsSentiment, m.NewsScore,
		time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("featurestore: upsert macro %s %s: %w", m.Symbol, m.Date, err)
	}
	return nil
}

// FullRow is what queries return: technical + whatever macro happens to
// be populated for that day (nullable strings, empty if never set).
type FullRow struct {
	FeatureRow
	VIX            string
	FIINet         string
	DIINet         string
	BreadthADRatio string
	NewsSentiment  string
	NewsScore      string
}

func (s *Store) Get(ctx context.Context, symbol, date string) (FullRow, error) {
	var r FullRow
	var closeStr, rsi, ema20, ema50, adx, atr, bb string
	var crossBull, crossBear, macdBull, macdBear *int
	var vix, fiiNet, diiNet, breadth, sentiment, score sql.NullString

	err := s.db.QueryRowContext(ctx,
		`SELECT symbol, date, close, rsi14, ema20, ema50, ema_cross_bullish, ema_cross_bearish,
		 macd_bullish, macd_bearish, adx14, atr14, bollinger_percent_b,
		 coalesce(vix,''), coalesce(fii_net,''), coalesce(dii_net,''), coalesce(breadth_ad_ratio,''),
		 coalesce(news_sentiment,''), coalesce(news_score,'')
		 FROM features WHERE symbol = ? AND date = ?`,
		symbol, date,
	).Scan(&r.Symbol, &r.Date, &closeStr, &rsi, &ema20, &ema50, &crossBull, &crossBear, &macdBull, &macdBear,
		&adx, &atr, &bb, &vix, &fiiNet, &diiNet, &breadth, &sentiment, &score)
	if err != nil {
		return FullRow{}, err
	}

	r.Close = parseDecimal(closeStr)
	r.RSI14 = parseDecimal(rsi)
	r.EMA20 = parseDecimal(ema20)
	r.EMA50 = parseDecimal(ema50)
	r.ADX14 = parseDecimal(adx)
	r.ATR14 = parseDecimal(atr)
	r.BollingerPercentB = parseDecimal(bb)
	r.EMACrossBullish = intPtrToBoolPtr(crossBull)
	r.EMACrossBearish = intPtrToBoolPtr(crossBear)
	r.MACDBullish = intPtrToBoolPtr(macdBull)
	r.MACDBearish = intPtrToBoolPtr(macdBear)
	r.VIX, r.FIINet, r.DIINet, r.BreadthADRatio, r.NewsSentiment, r.NewsScore = vix.String, fiiNet.String, diiNet.String, breadth.String, sentiment.String, score.String
	return r, nil
}

// Query returns every stored day for symbol between from and to
// (inclusive), oldest first.
func (s *Store) Query(ctx context.Context, symbol, from, to string) ([]FullRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT date FROM features WHERE symbol = ? AND date BETWEEN ? AND ? ORDER BY date`,
		symbol, from, to,
	)
	if err != nil {
		return nil, err
	}
	var dates []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			rows.Close()
			return nil, err
		}
		dates = append(dates, d)
	}
	rows.Close()

	out := make([]FullRow, 0, len(dates))
	for _, d := range dates {
		r, err := s.Get(ctx, symbol, d)
		if err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func parseDecimal(s string) decimal.Decimal {
	d, _ := decimal.NewFromString(s)
	return d
}
func intPtrToBoolPtr(i *int) *bool {
	if i == nil {
		return nil
	}
	b := *i == 1
	return &b
}
