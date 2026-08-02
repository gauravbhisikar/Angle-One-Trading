package storage

import (
	"database/sql"
	"time"

	"github.com/shopspring/decimal"

	"tradingengine/internal/models"
)

const rfc3339 = time.RFC3339

func parseDecimal(s string) decimal.Decimal {
	d, _ := decimal.NewFromString(s)
	return d
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(rfc3339, s)
	return t
}

func scanOrders(rows *sql.Rows) ([]models.Order, error) {
	var out []models.Order
	for rows.Next() {
		var o models.Order
		var limitPrice, avgFillPrice, createdAt, updatedAt string
		if err := rows.Scan(&o.ID, &o.StrategyID, &o.StrategyVersion, &o.Symbol, &o.Side, &o.Product, &o.OrderType,
			&o.Quantity, &limitPrice, &o.FilledQuantity, &avgFillPrice, &o.State, &o.RejectReason, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		o.LimitPrice = parseDecimal(limitPrice)
		o.AvgFillPrice = parseDecimal(avgFillPrice)
		o.CreatedAt = parseTime(createdAt)
		o.UpdatedAt = parseTime(updatedAt)
		out = append(out, o)
	}
	return out, rows.Err()
}
