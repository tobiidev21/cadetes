package positions

import (
	"sync"
	"time"
)

// Límite de envíos por cadete: hasta limiterBurst seguidos y después uno cada
// limiterEvery (unos 30 por minuto). Cuenta pedidos, no posiciones: con lotes
// de hasta 100 alcanza de sobra, y un cliente con un error que reintenta en
// bucle no llena la base.
const (
	limiterBurst = 10
	limiterEvery = 2 * time.Second
)

// Limiter es un token bucket en memoria por cadete. El mapa crece a lo sumo
// hasta la cantidad de cadetes.
type Limiter struct {
	mu      sync.Mutex
	burst   float64
	every   time.Duration
	now     func() time.Time
	buckets map[int64]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewLimiter arma un limitador con los valores de producción.
func NewLimiter() *Limiter {
	return &Limiter{burst: limiterBurst, every: limiterEvery, now: time.Now, buckets: map[int64]*bucket{}}
}

// Allow consume un permiso del cadete. Si no hay, devuelve false y cuánto
// falta para el próximo.
func (l *Limiter) Allow(courierID int64) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[courierID]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[courierID] = b
	}
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens = min(l.burst, b.tokens+float64(elapsed)/float64(l.every))
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) * float64(l.every))
}
