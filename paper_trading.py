"""Paper trading — deterministic, no AI, no real orders. A local SQLite
file (gitignored, survives `git pull --ff-only` redeploys the same way
.env already does) tracks manually-confirmed BUY CE/BUY PE positions so
30-day win-rate/by-direction/by-trade-state analysis survives across the
frequent redeploys this project does.

Deliberately excluded from V1 (per explicit scope): futures, spreads,
option selling, multi-leg strategies, bracket/trailing orders, automated
entries, AI-decided strikes, backtesting. Max 1 open position at a time.
Capital resets to a fixed ₹50,000 every trading day — no compounding.

This module owns no wall-clock reads itself (same purity discipline as
trend_engine.py) — app.py passes in "today"/"now" as plain values, so
every function here stays a pure, testable read/write over the DB.
"""
import json
import sqlite3

DAILY_CAPITAL = 50000.0
MAX_OPEN_POSITIONS = 1
ANALYSIS_WINDOW_DAYS = 30


def get_conn(path):
    conn = sqlite3.connect(path, check_same_thread=False)
    conn.row_factory = sqlite3.Row
    conn.execute("""
        CREATE TABLE IF NOT EXISTS positions (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            status TEXT NOT NULL,           -- 'open' | 'closed'
            side TEXT NOT NULL,             -- 'CE' | 'PE'
            strike REAL NOT NULL,
            expiry TEXT NOT NULL,
            lot_size INTEGER NOT NULL,
            lots INTEGER NOT NULL,
            qty INTEGER NOT NULL,
            entry_price REAL NOT NULL,
            entry_time TEXT NOT NULL,
            exit_price REAL,
            exit_time TEXT,
            close_type TEXT,                -- 'manual' | 'auto_eod'
            pnl REAL,
            trading_day TEXT NOT NULL,      -- IST date the position was opened on
            entry_snapshot TEXT,            -- JSON: dashboard state at entry
            checklist TEXT,                 -- JSON: {"items": {...bool}, "notes": "..."}
            notes TEXT
        )
    """)
    conn.commit()
    return conn


def _row(r):
    if r is None:
        return None
    d = dict(r)
    for k in ("entry_snapshot", "checklist"):
        if d.get(k):
            try:
                d[k] = json.loads(d[k])
            except (TypeError, ValueError):
                pass
    return d


def get_open_position(conn):
    r = conn.execute("SELECT * FROM positions WHERE status = 'open' ORDER BY id DESC LIMIT 1").fetchone()
    return _row(r)


def open_position(conn, side, strike, expiry, lot_size, lots, entry_price, entry_time,
                   trading_day, entry_snapshot=None, checklist=None, notes=""):
    if get_open_position(conn) is not None:
        raise RuntimeError(f"a position is already open — max {MAX_OPEN_POSITIONS} at a time in V1")
    if side not in ("CE", "PE"):
        raise ValueError("side must be CE or PE")
    if lots < 1:
        raise ValueError("lots must be >= 1")
    qty = lot_size * lots
    cost = entry_price * qty
    account = compute_today_account(conn, trading_day)
    if cost > account["available_cash"] + 1e-6:
        raise RuntimeError(f"insufficient paper capital — need {cost:.2f}, have {account['available_cash']:.2f}")
    cur = conn.execute(
        "INSERT INTO positions (status, side, strike, expiry, lot_size, lots, qty, entry_price, "
        "entry_time, trading_day, entry_snapshot, checklist, notes) "
        "VALUES ('open', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
        (side, strike, expiry, lot_size, lots, qty, entry_price, entry_time, trading_day,
         json.dumps(entry_snapshot) if entry_snapshot is not None else None,
         json.dumps(checklist) if checklist is not None else None, notes or ""))
    conn.commit()
    return _row(conn.execute("SELECT * FROM positions WHERE id = ?", (cur.lastrowid,)).fetchone())


def close_position(conn, position_id, exit_price, exit_time, close_type="manual"):
    row = conn.execute("SELECT * FROM positions WHERE id = ? AND status = 'open'", (position_id,)).fetchone()
    if row is None:
        raise RuntimeError(f"no open position with id {position_id}")
    qty = row["qty"]
    pnl = (exit_price - row["entry_price"]) * qty  # long-only (BUY CE / BUY PE), no selling in V1
    conn.execute(
        "UPDATE positions SET status='closed', exit_price=?, exit_time=?, close_type=?, pnl=? WHERE id=?",
        (exit_price, exit_time, close_type, pnl, position_id))
    conn.commit()
    return _row(conn.execute("SELECT * FROM positions WHERE id = ?", (position_id,)).fetchone())


def get_trade_log(conn, since_date=None, limit=200):
    if since_date:
        rows = conn.execute(
            "SELECT * FROM positions WHERE status='closed' AND trading_day >= ? ORDER BY id DESC LIMIT ?",
            (since_date, limit)).fetchall()
    else:
        rows = conn.execute(
            "SELECT * FROM positions WHERE status='closed' ORDER BY id DESC LIMIT ?", (limit,)).fetchall()
    return [_row(r) for r in rows]


def compute_today_account(conn, today, current_ltp=None):
    """Daily scoreboard: capital resets to DAILY_CAPITAL every trading day,
    never compounds. available_cash = DAILY_CAPITAL - cost of today's open
    position (if any) + today's realized P&L so far."""
    closed_today = conn.execute(
        "SELECT * FROM positions WHERE status='closed' AND trading_day=?", (today,)).fetchall()
    closed_today = [_row(r) for r in closed_today]
    realized_pnl = sum(r["pnl"] for r in closed_today)
    wins = sum(1 for r in closed_today if r["pnl"] > 0)
    losses = sum(1 for r in closed_today if r["pnl"] <= 0)

    open_pos = get_open_position(conn)
    open_cost = 0.0
    unrealized_pnl = 0.0
    if open_pos and open_pos["trading_day"] == today:
        open_cost = open_pos["entry_price"] * open_pos["qty"]
        if current_ltp is not None:
            unrealized_pnl = (current_ltp - open_pos["entry_price"]) * open_pos["qty"]

    available_cash = DAILY_CAPITAL - open_cost + realized_pnl
    open_value = open_cost + unrealized_pnl
    total_pnl = realized_pnl + unrealized_pnl
    equity = DAILY_CAPITAL + total_pnl
    return {
        "starting_capital": DAILY_CAPITAL,
        "available_cash": round(available_cash, 2),
        "open_position_value": round(open_value, 2),
        "unrealized_pnl": round(unrealized_pnl, 2),
        "realized_pnl": round(realized_pnl, 2),
        "total_pnl": round(total_pnl, 2),
        "equity": round(equity, 2),
        "return_pct": round(total_pnl / DAILY_CAPITAL * 100, 2),
        "trades_today": len(closed_today) + (1 if (open_pos and open_pos["trading_day"] == today) else 0),
        "wins_today": wins, "losses_today": losses,
    }


def compute_30day_analysis(conn, since_date):
    """Deterministic win-rate/by-direction/by-trade-state/risk stats over
    every CLOSED trade on/after since_date. No AI — plain aggregation."""
    rows = get_trade_log(conn, since_date=since_date, limit=100000)
    n = len(rows)
    if n == 0:
        return {"trades": 0}
    wins = [r for r in rows if r["pnl"] > 0]
    losses = [r for r in rows if r["pnl"] <= 0]
    gross_profit = sum(r["pnl"] for r in wins)
    gross_loss = sum(r["pnl"] for r in losses)
    by_day = {}
    for r in rows:
        by_day.setdefault(r["trading_day"], 0.0)
        by_day[r["trading_day"]] += r["pnl"]

    def side_bucket(side):
        sub = [r for r in rows if r["side"] == side]
        if not sub:
            return None
        sw = sum(1 for r in sub if r["pnl"] > 0)
        return {"trades": len(sub), "win_rate": round(sw / len(sub) * 100, 1), "pnl": round(sum(r["pnl"] for r in sub), 2)}

    def snapshot_field(r, path):
        snap = r.get("entry_snapshot") or {}
        cur = snap
        for p in path:
            if not isinstance(cur, dict):
                return None
            cur = cur.get(p)
        return cur

    def bucket_by(keyfn):
        groups = {}
        for r in rows:
            k = keyfn(r)
            if k is None:
                continue
            groups.setdefault(k, []).append(r)
        out = {}
        for k, sub in groups.items():
            sw = sum(1 for r in sub if r["pnl"] > 0)
            out[k] = {"trades": len(sub), "win_rate": round(sw / len(sub) * 100, 1), "pnl": round(sum(r["pnl"] for r in sub), 2)}
        return out

    return {
        "trades": n,
        "trading_days": len(by_day),
        "wins": len(wins), "losses": len(losses),
        "win_rate": round(len(wins) / n * 100, 1),
        "gross_profit": round(gross_profit, 2), "gross_loss": round(gross_loss, 2),
        "net_pnl": round(gross_profit + gross_loss, 2),
        "avg_win": round(gross_profit / len(wins), 2) if wins else 0,
        "avg_loss": round(gross_loss / len(losses), 2) if losses else 0,
        "best_day": round(max(by_day.values()), 2) if by_day else 0,
        "worst_day": round(min(by_day.values()), 2) if by_day else 0,
        "by_side": {"CE": side_bucket("CE"), "PE": side_bucket("PE")},
        "by_trade_state": bucket_by(lambda r: snapshot_field(r, ["setup_state_status"])),
        "by_market_state": bucket_by(lambda r: snapshot_field(r, ["market_state"])),
    }
