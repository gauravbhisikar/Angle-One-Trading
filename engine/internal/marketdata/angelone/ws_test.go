package angelone

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"
)

// wsPair spins up a real local WebSocket server and dials it, returning a
// live *websocket.Conn usable anywhere WSFeed expects one — lets the
// concurrency tests below exercise real Conn.WriteMessage/Close calls
// without touching Angel One's production wsURL (dial() hardcodes that
// endpoint, so these tests bypass dial/Connect entirely and drive the
// atomic conn pointer + subs map directly, which is where the actual
// concurrency risk lives).
func wsPair(t *testing.T) (*websocket.Conn, func()) {
	t.Helper()
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		go func() {
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()
	}))
	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		srv.Close()
		t.Fatalf("dial local test server: %v", err)
	}
	return conn, func() { conn.Close(); srv.Close() }
}

// TestWSFeedConcurrentAccessIsRaceFree drives Subscribe (subs map + wire
// send), a heartbeat-style conn read/write, and a reconnect-style conn swap
// all concurrently — the exact three access patterns supervise/heartbeat/
// Subscribe use in the running engine. Run with -race; this is what proves
// the atomic.Pointer[websocket.Conn] change actually removed the data race
// that existed when conn was a plain field written from one goroutine and
// read from others.
func TestWSFeedConcurrentAccessIsRaceFree(t *testing.T) {
	f := NewWSFeed(&Client{}, map[string]Instrument{
		"NIFTYBEES": {ExchangeType: 1, Token: "10576"},
	})

	connA, closeA := wsPair(t)
	defer closeA()
	connB, closeB := wsPair(t)
	defer closeB()
	f.conn.Store(connA)
	f.heartbeatInterval = time.Millisecond // exercise real ping traffic within the test window

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Subscribe/resubscribe: mutates f.subs and sends over whatever conn is
	// current, same as a real strategy deploy racing a reconnect.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = f.Subscribe([]string{"NIFTYBEES"})
				f.resubscribeAll()
			}
		}
	}()

	// The real heartbeat goroutine — exercises its actual writeMu-guarded
	// WriteMessage call, not a re-implementation of it.
	hbCtx, hbCancel := context.WithCancel(context.Background())
	defer hbCancel()
	wg.Add(1)
	go func() {
		defer wg.Done()
		f.heartbeat(hbCtx)
	}()

	// supervise-style: swaps the conn pointer repeatedly, as a reconnect
	// would after a redial.
	wg.Add(1)
	go func() {
		defer wg.Done()
		toggle := false
		for {
			select {
			case <-stop:
				return
			default:
				if toggle {
					f.conn.Store(connA)
				} else {
					f.conn.Store(connB)
				}
				toggle = !toggle
			}
		}
	}()

	time.Sleep(200 * time.Millisecond)
	close(stop)
	hbCancel()
	wg.Wait()
}

func TestSubscribeUnknownSymbolErrors(t *testing.T) {
	f := NewWSFeed(&Client{}, map[string]Instrument{"NIFTYBEES": {ExchangeType: 1, Token: "10576"}})
	if err := f.Subscribe([]string{"RELIANCE"}); err == nil {
		t.Fatal("expected error for symbol with no instrument mapping")
	}
}

func TestSubscribeWithoutConnDoesNotError(t *testing.T) {
	f := NewWSFeed(&Client{}, map[string]Instrument{"NIFTYBEES": {ExchangeType: 1, Token: "10576"}})
	if err := f.Subscribe([]string{"NIFTYBEES"}); err != nil {
		t.Fatalf("Subscribe before Connect should not hard-fail: %v", err)
	}
}

// TestDecodeTickPacketExtractsVolume guards against regressing back to the
// real bug found live on 2026-08-04: subscribing in LTP mode (or reading
// the wrong byte range) leaves every tick's Volume at 0, silently starving
// any strategy whose entry/exit logic depends on a real volume condition
// (e.g. volume_spike_pct) — it looks exactly like "still warming up"
// forever, never like an error.
func TestDecodeTickPacketExtractsVolume(t *testing.T) {
	data := make([]byte, 59)
	data[0] = 2 // subscription mode: Quote
	data[1] = 1 // exchange type: NSE_CM
	copy(data[2:27], "10576")
	binary.LittleEndian.PutUint64(data[27:35], 1)                    // sequence number
	binary.LittleEndian.PutUint64(data[35:43], uint64(1754300000000)) // exchange timestamp (ms)
	binary.LittleEndian.PutUint64(data[43:51], 27885)                 // LTP: 278.85 rupees, paise-scaled
	binary.LittleEndian.PutUint64(data[51:59], 42)                    // last traded quantity

	tick, ok := decodeTickPacket(data, map[string]string{"10576": "NIFTYBEES"})
	if !ok {
		t.Fatal("expected decode to succeed")
	}
	if tick.Symbol != "NIFTYBEES" {
		t.Errorf("Symbol = %q, want NIFTYBEES", tick.Symbol)
	}
	if !tick.Price.Equal(decimal.NewFromFloat(278.85)) {
		t.Errorf("Price = %v, want 278.85", tick.Price)
	}
	if tick.Volume != 42 {
		t.Errorf("Volume = %d, want 42 (the real bug: this used to always be 0)", tick.Volume)
	}
}

func TestDecodeTickPacketShorterThanQuoteModeHasZeroVolume(t *testing.T) {
	data := make([]byte, 51) // LTP-mode-length packet, no quantity field
	copy(data[2:27], "10576")
	binary.LittleEndian.PutUint64(data[43:51], 27885)

	tick, ok := decodeTickPacket(data, map[string]string{"10576": "NIFTYBEES"})
	if !ok {
		t.Fatal("expected decode to succeed on a short (LTP-length) packet")
	}
	if tick.Volume != 0 {
		t.Errorf("Volume = %d, want 0 for a packet too short to carry a quantity field", tick.Volume)
	}
}

func TestIstMarketOpen(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Skip("no tzdata available")
	}
	cases := []struct {
		name string
		t    time.Time
		open bool
	}{
		{"weekday mid-session", time.Date(2026, 8, 4, 11, 0, 0, 0, loc), true}, // 2026-08-04 is a Tuesday
		{"weekday before open", time.Date(2026, 8, 4, 9, 0, 0, 0, loc), false},
		{"weekday after close", time.Date(2026, 8, 4, 16, 0, 0, 0, loc), false},
		{"saturday", time.Date(2026, 8, 8, 11, 0, 0, 0, loc), false},
		{"sunday", time.Date(2026, 8, 9, 11, 0, 0, 0, loc), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := istMarketOpen(c.t); got != c.open {
				t.Errorf("istMarketOpen(%v) = %v, want %v", c.t, got, c.open)
			}
		})
	}
}
