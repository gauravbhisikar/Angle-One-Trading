package storage

import (
	"database/sql"
	"fmt"

	"tradingengine/internal/models"
)

type OrderRepo struct{ db *sql.DB }

func NewOrderRepo(db *sql.DB) *OrderRepo { return &OrderRepo{db: db} }

func (r *OrderRepo) Insert(o models.Order) error {
	_, err := r.db.Exec(
		`INSERT INTO orders (id, strategy_id, strategy_version, symbol, side, product, order_type,
		 quantity, limit_price, filled_quantity, avg_fill_price, state, reject_reason, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.ID, o.StrategyID, o.StrategyVersion, o.Symbol, o.Side, o.Product, o.OrderType,
		o.Quantity, o.LimitPrice.String(), o.FilledQuantity, o.AvgFillPrice.String(),
		o.State, o.RejectReason, o.CreatedAt.UTC().Format(rfc3339), o.UpdatedAt.UTC().Format(rfc3339),
	)
	if err != nil {
		return fmt.Errorf("storage: insert order: %w", err)
	}
	return nil
}

func (r *OrderRepo) DeleteByStrategy(strategyID string) error {
	_, err := r.db.Exec(`DELETE FROM orders WHERE strategy_id = ?`, strategyID)
	return err
}

func (r *OrderRepo) ListByStrategy(strategyID string, version int) ([]models.Order, error) {
	rows, err := r.db.Query(
		`SELECT id, strategy_id, strategy_version, symbol, side, product, order_type,
		 quantity, limit_price, filled_quantity, avg_fill_price, state, reject_reason, created_at, updated_at
		 FROM orders WHERE strategy_id = ? AND strategy_version = ? ORDER BY created_at`,
		strategyID, version,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanOrders(rows)
}
