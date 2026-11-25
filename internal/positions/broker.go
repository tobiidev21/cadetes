package positions

import (
	"encoding/json"
	"log/slog"
	"sync"
)

// subscriberBuffer es cuántos eventos se guardan para un admin que todavía no
// los leyó. Si se llena, los siguientes se descartan para ese admin.
const subscriberBuffer = 64

// Broker difunde en memoria cada lote guardado a los admins conectados al
// stream. Publicar nunca bloquea: si un admin está lento, pierde mensajes.
// Anda con una sola instancia del servidor.
type Broker struct {
	mu     sync.Mutex
	subs   map[*Subscription]struct{}
	closed bool
}

// Subscription es un admin conectado. C se cierra al cancelar o al cerrar el
// broker.
type Subscription struct {
	C       <-chan []byte
	ch      chan []byte
	broker  *Broker
	dropped int64 // protegido por broker.mu
}

// NewBroker arma un broker vacío.
func NewBroker() *Broker {
	return &Broker{subs: map[*Subscription]struct{}{}}
}

// Subscribe suma un suscriptor. Con el broker cerrado, C ya viene cerrado.
func (b *Broker) Subscribe() *Subscription {
	ch := make(chan []byte, subscriberBuffer)
	s := &Subscription{C: ch, ch: ch, broker: b}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		close(ch)
		return s
	}
	b.subs[s] = struct{}{}
	return s
}

// Cancel saca al suscriptor y devuelve cuántos mensajes se le descartaron.
// Se puede llamar más de una vez.
func (s *Subscription) Cancel() int64 {
	b := s.broker
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.subs[s]; ok {
		delete(b.subs, s)
		close(s.ch)
	}
	return s.dropped
}

// Publish manda el mensaje a todos los suscriptores sin esperar a ninguno.
func (b *Broker) Publish(msg []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for s := range b.subs {
		select {
		case s.ch <- msg:
		default:
			s.dropped++
		}
	}
}

// PublishPositions arma el evento SSE "posiciones" con el lote y lo publica.
func (b *Broker) PublishPositions(ps []Position) {
	data, err := json.Marshal(ps)
	if err != nil {
		slog.Error("armando evento de posiciones", "err", err)
		return
	}
	// json.Marshal no deja saltos de línea, así que entra en un solo "data:".
	msg := make([]byte, 0, len(data)+32)
	msg = append(msg, "event: posiciones\ndata: "...)
	msg = append(msg, data...)
	msg = append(msg, "\n\n"...)
	b.Publish(msg)
}

// Close cierra todos los suscriptores (al apagar el servidor: sin esto los
// streams abiertos no dejan terminar http.Server.Shutdown).
func (b *Broker) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for s := range b.subs {
		delete(b.subs, s)
		close(s.ch)
	}
}

// Subscribers devuelve cuántos admins hay conectados.
func (b *Broker) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
