package angelone

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
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
type WSFeed struct {
	client      *Client
	instruments map[string]Instrument // symbol -> instrument
	tokenToSym  map[string]string     // token -> symbol, for decoding incoming ticks

	conn  *websocket.Conn
	ticks chan models.Tick

	mu   sync.Mutex
	subs map[string]bool
}

func NewWSFeed(client *Client, instruments map[string]Instrument) *WSFeed {
	tokenToSym := make(map[string]string, len(instruments))
	for sym, inst := range instruments {
		tokenToSym[inst.Token] = sym
	}
	return &WSFeed{
		client:      client,
		instruments: instruments,
		tokenToSym:  tokenToSym,
		ticks:       make(chan models.Tick, 4096),
		subs:        map[string]bool{},
	}
}

func (f *WSFeed) Ticks() <-chan models.Tick { return f.ticks }

func (f *WSFeed) Close() error {
	if f.conn != nil {
		return f.conn.Close()
	}
	return nil
}

// Connect dials the feed using the session's feed token. Call once after
// Client.Login succeeds.
func (f *WSFeed) Connect(ctx context.Context) error {
	header := map[string][]string{
		"Authorization": {"Bearer " + f.client.JWTToken()},
		"x-api-key":     {f.client.apiKey},
		"x-client-code": {f.client.clientCode},
		"x-feed-token":  {f.client.FeedToken()},
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, header)
	if err != nil {
		return fmt.Errorf("angelone: ws connect: %w", err)
	}
	f.conn = conn
	go f.readLoop()
	go f.heartbeat(ctx)
	return nil
}

type subscribeRequest struct {
	CorrelationID string `json:"correlationID"`
	Action        int    `json:"action"` // 1=subscribe, 0=unsubscribe
	Params        struct {
		Mode      int `json:"mode"` // 1=LTP
		TokenList []struct {
			ExchangeType int      `json:"exchangeType"`
			Tokens       []string `json:"tokens"`
		} `json:"tokenList"`
	} `json:"params"`
}

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

	if len(byExchange) == 0 || f.conn == nil {
		return nil
	}

	var req subscribeRequest
	req.CorrelationID = "engine"
	req.Action = 1
	req.Params.Mode = 1
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
	return f.conn.WriteMessage(websocket.TextMessage, payload)
}

func (f *WSFeed) heartbeat(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if f.conn != nil {
				_ = f.conn.WriteMessage(websocket.TextMessage, []byte("ping"))
			}
		}
	}
}

func (f *WSFeed) readLoop() {
	defer close(f.ticks)
	for {
		msgType, data, err := f.conn.ReadMessage()
		if err != nil {
			return
		}
		if msgType != websocket.BinaryMessage {
			continue
		}
		if tick, ok := decodeLTPPacket(data, f.tokenToSym); ok {
			f.ticks <- tick
		}
	}
}

// decodeLTPPacket parses SmartAPI's LTP-mode binary tick (51 bytes):
//
//	[0]     subscription mode
//	[1]     exchange type
//	[2:27]  token, 25 bytes, null-padded ASCII
//	[27:35] sequence number, int64 LE
//	[35:43] exchange timestamp (epoch ms), int64 LE
//	[43:51] last traded price (paise), int64 LE
//
// Verify these offsets against SmartAPI's current WebSocket 2.0 docs
// before relying on this in a live session (see package doc).
func decodeLTPPacket(data []byte, tokenToSym map[string]string) (models.Tick, bool) {
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

	return models.Tick{
		Symbol:    symbol,
		Price:     decimal.NewFromInt(ltpPaise).Div(decimal.NewFromInt(100)),
		Timestamp: time.UnixMilli(tsMillis),
	}, true
}
