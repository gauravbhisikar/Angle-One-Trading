package indicators

// ring is a fixed-capacity float64 ring buffer. Bounded memory per series
// regardless of how long the strategy runs (ENGINE_SPEC Sec 0.6) — never
// grows past its configured period/lookback.
type ring struct {
	buf  []float64
	cap  int
	size int
	pos  int
	sum  float64
}

func newRing(capacity int) *ring {
	if capacity < 1 {
		capacity = 1
	}
	return &ring{buf: make([]float64, capacity), cap: capacity}
}

// push adds a value, evicting the oldest if full, and returns the evicted
// value (0 if the buffer wasn't full yet).
func (r *ring) push(v float64) (evicted float64, wasFull bool) {
	if r.size < r.cap {
		r.buf[r.pos] = v
		r.sum += v
		r.size++
		r.pos = (r.pos + 1) % r.cap
		return 0, false
	}
	evicted = r.buf[r.pos]
	r.sum += v - evicted
	r.buf[r.pos] = v
	r.pos = (r.pos + 1) % r.cap
	return evicted, true
}

func (r *ring) full() bool { return r.size >= r.cap }

func (r *ring) total() float64 { return r.sum }

func (r *ring) mean() float64 {
	if r.size == 0 {
		return 0
	}
	return r.sum / float64(r.size)
}

func (r *ring) values() []float64 {
	out := make([]float64, r.size)
	start := r.pos
	if r.size < r.cap {
		start = 0
	}
	for i := 0; i < r.size; i++ {
		out[i] = r.buf[(start+i)%r.cap]
	}
	return out
}

func (r *ring) max() float64 {
	vals := r.values()
	if len(vals) == 0 {
		return 0
	}
	m := vals[0]
	for _, v := range vals[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

func (r *ring) min() float64 {
	vals := r.values()
	if len(vals) == 0 {
		return 0
	}
	m := vals[0]
	for _, v := range vals[1:] {
		if v < m {
			m = v
		}
	}
	return m
}

// oldest returns the value that will be evicted on the next push (0 if not full).
func (r *ring) oldest() float64 {
	if r.size < r.cap {
		return 0
	}
	return r.buf[r.pos]
}
