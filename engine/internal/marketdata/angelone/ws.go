package angelone

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"

	"tradingengine/internal/models"
)

const wsURL = "wss://smartapisocket.angelone.in/smart-stream"

// Instrument identifies a tradeable symbol for the WebSocket feed —
// sourced from the Instrument Master (ENGINE_SPEC Sec 8), not hardcoded
// per strategy.
type Instrument struct {
	ExchangeType int // 1=NSE_CM, 2=NSE_FO, 3=BSE_CM, 4=BSE_FO, ...
	Token        string
}

// WSFeed implements marketdata.Feed against Angel One's live tick stream.
// It survives disconnects: a single supervisor goroutine relogins, redials,
// and resubscribes every previously-subscribed symbol whenever the socket
// drops, instead of dying permanently on the first transient error (a dead
// feed silently stops candle processing for every running strategy —
// unacceptable for an unattended, multi-day live process).
type WSFeed struct {
	client      *Client
	instruments map[string]Instrument // symbol -> instrument
	tokenToSym  map[string]string     // token -> symbol, for decoding incoming ticks

	conn      atomic.Pointer[websocket.Conn]
	ticks     chan models.Tick
	closeOnce sync.Once
	closed    atomic.Bool

	// writeMu serializes every WriteMessage call across whichever conn is
	// currently active. gorilla/websocket allows one concurrent reader and
	// one concurrent writer per Conn, but NOT two concurrent writers —
	// heartbeat's ping and Subscribe/resubscribeAll's subscribe frame can
	// otherwise land on the same conn at the same instant and panic
	// ("concurrent write to websocket connection"). The atomic conn pointer
	// only makes swapping *which* conn is current race-free; it says
	// nothing about serializing writes to it, which is a separate,
	// necessary lock.
	writeMu sync.Mutex

	mu   sync.Mutex
	subs map[string]bool

	// heartbeatInterval defaults to 30s; only overridden in tests so the
	// concurrency test below can exercise real ping traffic without a
	// 30-second wait.
	heartbeatInterval time.Duration
}

func NewWSFeed(client *Client, instruments map[string]Instrument) *WSFeed {
	tokenToSym := make(map[string]string, len(instruments))
	for sym, inst := range instruments {
		tokenToSym[inst.Token] = sym
	}
	return &WSFeed{
		client:            client,
		instruments:       instruments,
		tokenToSym:        tokenToSym,
		ticks:             make(chan models.Tick, 4096),
		subs:              map[string]bool{},
		heartbeatInterval: 30 * time.Second,
	}
}

func (f *WSFeed) Ticks() <-chan models.Tick { return f.ticks }

// Close stops the feed for good — no further reconnect attempts. Setting
// closed before closing the connection is what tells the supervisor loop
// (which sees the resulting read error) to stop reconnecting and close the
// ticks channel exactly once, rather than treating this as a transient drop.
func (f *WSFeed) Close() error {
	f.closed.Store(true)
	if conn := f.conn.Load(); conn != nil {
		return conn.Close()
	}
	return nil
}

// Connect dials the feed using the session's feed token, then hands off to
// a long-lived supervisor (reconnect) and heartbeat goroutine for the rest
// of the feed's life. dialCtx bounds only this first synchronous dial — the
// caller can treat a non-nil error here as "give up and fall back" (e.g. a
// short boot-time timeout). lifeCtx has no deadline of its own; it's what
// the supervisor/heartbeat goroutines and every later reconnect attempt run
// under, and only its cancellation (engine shutdown) ever stops them —
// using dialCtx for those too would kill the feed the moment a boot timeout
// expires, even on a fully healthy connection.
func (f *WSFeed) Connect(dialCtx, lifeCtx context.Context) error {
	conn, err := f.dial(dialCtx)
	if err != nil {
		return err
	}
	f.conn.Store(conn)
	go f.supervise(lifeCtx)
	go f.heartbeat(lifeCtx)
	return nil
}

func (f *WSFeed) dial(ctx context.Context) (*websocket.Conn, error) {
	header := map[string][]string{
		"Authorization": {"Bearer " + f.client.JWTToken()},
		"x-api-key":     {f.client.apiKey},
		"x-client-code": {f.client.clientCode},
		"x-feed-token":  {f.client.FeedToken()},
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, header)
	if err != nil {
		return nil, fmt.Errorf("angelone: ws connect: %w", err)
	}
	return conn, nil
}

// supervise owns the ticks channel's entire lifecycle and all reconnect
// decisions — readLoop/heartbeat/Subscribe never close it or give up on
// their own. On a disconnect that isn't a deliberate Close(), it relogins
// (handles daily token expiry across a multi-day run for free), redials,
// and resubscribes every symbol already recorded in f.subs. Backoff is
// capped and tight while the market's open, but backs off to a long fixed
// interval outside session hours so an overnight/weekend outage doesn't
// hammer Angel One's login endpoint every few seconds for nothing.
func (f *WSFeed) supervise(ctx context.Context) {
	backoff := time.Second
	for {
		conn := f.conn.Load()
		err := f.readLoop(conn)

		if f.closed.Load() || ctx.Err() != nil {
			f.closeOnce.Do(func() { close(f.ticks) })
			return
		}
		log.Printf("angelone: ws disconnected (%v) — reconnecting", err)

		wait := backoff
		if !istMarketOpen(time.Now()) {
			wait = 5 * time.Minute
		}
		select {
		case <-ctx.Done():
			f.closeOnce.Do(func() { close(f.ticks) })
			return
		case <-time.After(wait):
		}

		if loginErr := f.client.Login(ctx); loginErr != nil {
			log.Printf("angelone: relogin failed: %v", loginErr)
			backoff = nextBackoff(backoff)
			continue
		}
		dialCtx, dialCancel := context.WithTimeout(ctx, 15*time.Second)
		newConn, dialErr := f.dial(dialCtx)
		dialCancel()
		if dialErr != nil {
			log.Printf("angelone: reconnect dial failed: %v", dialErr)
			backoff = nextBackoff(backoff)
			continue
		}
		f.conn.Store(newConn)
		f.resubscribeAll()
		backoff = time.Second
		log.Println("angelone: ws reconnected")
	}
}

// istMarketOpen is a minimal, standalone NSE-hours check (Mon-Fri
// 09:15-15:30 IST) — deliberately duplicated from
// internal/marketsession.Current's logic rather than imported, since that
// package pulls in internal/scheduler -> internal/execution ->
// internal/marketdata/angelone, which would be an import cycle. Only used
// here to pick a reconnect backoff cadence, not as a trading-hours source
// of truth (marketsession remains that for the rest of the engine).
func istMarketOpen(now time.Time) bool {
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		loc = time.FixedZone("IST", 5*3600+1800)
	}
	ist := now.In(loc)
	if ist.Weekday() == time.Saturday || ist.Weekday() == time.Sunday {
		return false
	}
	tod := ist.Format("15:04")
	return tod >= "09:15" && tod <= "15:30"
}

func nextBackoff(b time.Duration) time.Duration {
	b *= 2
	if b > 30*time.Second {
		return 30 * time.Second
	}
	return b
}

type subscribeRequest struct {
	CorrelationID string `json:"correlationID"`
	Action        int    `json:"action"` // 1=subscribe, 0=unsubscribe
	Params        struct {
		Mode      int `json:"mode"` // 2=Quote (LTP mode 1 carries no volume field at all — see decodeTickPacket)
		TokenList []struct {
			ExchangeType int      `json:"exchangeType"`
			Tokens       []string `json:"tokens"`
		} `json:"tokenList"`
	} `json:"params"`
}

// Subscribe records interest in symbols and, if connected, sends the
// subscribe frame immediately. A wire-send failure here (e.g. landing mid
// reconnect-gap) is logged and swallowed, not returned as a hard error —
// the symbol is already in f.subs and resubscribeAll replays it on the next
// successful reconnect, so failing an otherwise-valid strategy deploy over
// a transient disconnect would be wrong. Only "no instrument mapping" (a
// real caller error) is returned.
func (f *WSFeed) Subscribe(symbols []string) error {
	f.mu.Lock()
	byExchange := map[int][]string{}
	for _, s := range symbols {
		if f.subs[s] {
			continue
		}
		inst, ok := f.instruments[s]
		if !ok {
			f.mu.Unlock()
			return fmt.Errorf("angelone: no instrument mapping for symbol %q", s)
		}
		f.subs[s] = true
		byExchange[inst.ExchangeType] = append(byExchange[inst.ExchangeType], inst.Token)
	}
	f.mu.Unlock()

	if len(byExchange) == 0 {
		return nil
	}
	if err := f.sendSubscribeFrame(byExchange); err != nil {
		log.Printf("angelone: subscribe send failed (will retry on reconnect): %v", err)
	}
	return nil
}

// resubscribeAll replays every symbol already recorded in f.subs against a
// freshly-redialed connection — the reconnect path's counterpart to
// Subscribe, called only from supervise.
func (f *WSFeed) resubscribeAll() {
	f.mu.Lock()
	byExchange := map[int][]string{}
	for s := range f.subs {
		inst, ok := f.instruments[s]
		if !ok {
			continue
		}
		byExchange[inst.ExchangeType] = append(byExchange[inst.ExchangeType], inst.Token)
	}
	f.mu.Unlock()

	if len(byExchange) == 0 {
		return
	}
	if err := f.sendSubscribeFrame(byExchange); err != nil {
		log.Printf("angelone: resubscribe failed: %v", err)
	}
}

func (f *WSFeed) sendSubscribeFrame(byExchange map[int][]string) error {
	conn := f.conn.Load()
	if conn == nil {
		return nil
	}

	var req subscribeRequest
	req.CorrelationID = "engine"
	req.Action = 1
	req.Params.Mode = 2
	for ex, tokens := range byExchange {
		req.Params.TokenList = append(req.Params.TokenList, struct {
			ExchangeType int      `json:"exchangeType"`
			Tokens       []string `json:"tokens"`
		}{ExchangeType: ex, Tokens: tokens})
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	return conn.WriteMessage(websocket.TextMessage, payload)
}

func (f *WSFeed) heartbeat(ctx context.Context) {
	ticker := time.NewTicker(f.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if conn := f.conn.Load(); conn != nil {
				f.writeMu.Lock()
				_ = conn.WriteMessage(websocket.TextMessage, []byte("ping"))
				f.writeMu.Unlock()
			}
		}
	}
}

// readLoop reads from one connection until it errors (the connection was
// closed, either deliberately via Close or by a network drop) — it never
// closes f.ticks itself; that's supervise's job, exactly once, for the
// feed's entire lifetime.
func (f *WSFeed) readLoop(conn *websocket.Conn) error {
	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		if msgType != websocket.BinaryMessage {
			continue
		}
		if tick, ok := decodeTickPacket(data, f.tokenToSym); ok {
			f.ticks <- tick
		}
	}
}

// decodeTickPacket parses SmartAPI's Quote-mode binary tick (Subscribe
// requests mode=2 — see sendSubscribeFrame). Byte offsets verified 2026-08
// against Angel One's own official smartapi-python client
// (SmartApi/smartWebSocketV2.py's _parse_binary_data):
//
//	[0]     subscription mode
//	[1]     exchange type
//	[2:27]  token, 25 bytes, null-padded ASCII
//	[27:35] sequence number, int64 LE
//	[35:43] exchange timestamp (epoch ms), int64 LE
//	[43:51] last traded price (paise), int64 LE
//	[51:59] last traded quantity, int64 LE — the per-trade fill size,
//	        i.e. exactly what a candle's Volume should sum across ticks
//	        (NOT the "volume traded for the day" field at [67:75], which
//	        is a cumulative day total and would wildly overcount if
//	        accumulated per-tick the way OneMinuteBuilder.OnTick does)
//
// Only present when the subscribed mode is Quote (2) or SnapQuote/Full (3)
// — LTP mode (1) is 51 bytes and carries no quantity/volume field at all,
// which is why this feed subscribes at mode 2, not 1.
func decodeTickPacket(data []byte, tokenToSym map[string]string) (models.Tick, bool) {
	if len(data) < 51 {
		return models.Tick{}, false
	}
	token := strings.TrimRight(string(data[2:27]), "\x00")
	symbol, ok := tokenToSym[token]
	if !ok {
		return models.Tick{}, false
	}
	tsMillis := int64(binary.LittleEndian.Uint64(data[35:43]))
	ltpPaise := int64(binary.LittleEndian.Uint64(data[43:51]))

	var qty int64
	if len(data) >= 59 {
		qty = int64(binary.LittleEndian.Uint64(data[51:59]))
	}

	return models.Tick{
		Symbol:    symbol,
		Price:     decimal.NewFromInt(ltpPaise).Div(decimal.NewFromInt(100)),
		Volume:    qty,
		Timestamp: time.UnixMilli(tsMillis),
	}, true
}
