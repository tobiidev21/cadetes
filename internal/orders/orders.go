// Package orders resuelve los pedidos: el admin los crea asignados a un
// cadete y cada cadete ve solo los suyos.
package orders

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/tobiidev21/cadetes/internal/auth"
	"github.com/tobiidev21/cadetes/internal/db"
)

// Status es el estado de un pedido. Coincide con el CHECK de la tabla orders.
type Status string

const (
	StatusPendiente Status = "pendiente"
	StatusAsignado  Status = "asignado"
	StatusEntregado Status = "entregado"
)

// MaxAddressLen es el largo máximo de una dirección, en caracteres.
const MaxAddressLen = 200

// ErrInvalidCourier es que el cadete elegido no existe, no es cadete o está
// inactivo.
var ErrInvalidCourier = errors.New("el cadete elegido no existe o no está activo")

// ErrNotFound es que el pedido no existe o el usuario no lo puede ver.
var ErrNotFound = errors.New("pedido no encontrado")

// InputError es un dato inválido que mandó el usuario; el mensaje se puede
// mostrar tal cual.
type InputError string

func (e InputError) Error() string { return string(e) }

// Order es un pedido tal como lo ve la interfaz.
type Order struct {
	ID             int64     `json:"id"`
	PickupAddress  string    `json:"pickup_address"`
	DropoffAddress string    `json:"dropoff_address"`
	PriceCents     int64     `json:"price_cents"`
	Status         Status    `json:"status"`
	CourierID      *int64    `json:"courier_id"`
	CourierName    *string   `json:"courier_name"`
	CreatedAt      time.Time `json:"created_at"`
}

// Courier es un cadete que se puede elegir al crear un pedido.
type Courier struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

// NewOrder son los datos que carga el admin.
type NewOrder struct {
	PickupAddress  string `json:"pickup_address"`
	DropoffAddress string `json:"dropoff_address"`
	PriceCents     int64  `json:"price_cents"`
	CourierID      int64  `json:"courier_id"`
}

// Store es lo que orders necesita de la base. *db.Queries lo cumple.
type Store interface {
	CreateAssignedOrder(ctx context.Context, arg db.CreateAssignedOrderParams) (db.Order, error)
	ListOrders(ctx context.Context) ([]db.ListOrdersRow, error)
	ListOrdersByCourier(ctx context.Context, courierID int64) ([]db.ListOrdersByCourierRow, error)
	ListActiveCouriers(ctx context.Context) ([]db.ListActiveCouriersRow, error)
	GetOrder(ctx context.Context, id int64) (db.GetOrderRow, error)
	GetOrderForCourier(ctx context.Context, arg db.GetOrderForCourierParams) (db.GetOrderForCourierRow, error)
}

// Notifier se entera de los pedidos recién asignados (push al cadete). Tiene
// que volver enseguida: el envío corre aparte y no frena la respuesta.
type Notifier interface {
	OrderAssigned(o Order)
}

// Service implementa la lógica de pedidos.
type Service struct {
	store    Store
	notifier Notifier
}

// NewService arma el servicio. notifier puede ser nil (sin avisos).
func NewService(store Store, notifier Notifier) *Service {
	return &Service{store: store, notifier: notifier}
}

// Create valida los datos y crea el pedido asignado al cadete elegido. El
// alta y el control de que sea un cadete activo van en una sola sentencia.
func (s *Service) Create(ctx context.Context, createdBy auth.User, in NewOrder) (Order, error) {
	pickup, err := cleanAddress(in.PickupAddress, "retiro")
	if err != nil {
		return Order{}, err
	}
	dropoff, err := cleanAddress(in.DropoffAddress, "entrega")
	if err != nil {
		return Order{}, err
	}
	if in.PriceCents <= 0 {
		return Order{}, InputError("el precio tiene que ser mayor que cero")
	}
	if in.CourierID <= 0 {
		return Order{}, InputError("falta elegir el cadete")
	}

	o, err := s.store.CreateAssignedOrder(ctx, db.CreateAssignedOrderParams{
		PickupAddress:  pickup,
		DropoffAddress: dropoff,
		PriceCents:     in.PriceCents,
		CreatedBy:      createdBy.ID,
		CourierID:      in.CourierID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, ErrInvalidCourier
	}
	if err != nil {
		return Order{}, fmt.Errorf("creando pedido: %w", err)
	}
	out := Order{
		ID:             o.ID,
		PickupAddress:  o.PickupAddress,
		DropoffAddress: o.DropoffAddress,
		PriceCents:     o.PriceCents,
		Status:         Status(o.Status),
		CourierID:      o.CourierID,
		CreatedAt:      o.CreatedAt,
	}
	if s.notifier != nil {
		s.notifier.OrderAssigned(out)
	}
	return out, nil
}

// Get devuelve un pedido si el usuario lo puede ver: el admin, cualquiera; el
// cadete, solo los asignados a él. Si no, ErrNotFound (no se distingue entre
// "no existe" y "es de otro").
func (s *Service) Get(ctx context.Context, u auth.User, id int64) (Order, error) {
	var (
		row db.GetOrderRow
		err error
	)
	switch u.Role {
	case auth.RoleAdmin:
		row, err = s.store.GetOrder(ctx, id)
	case auth.RoleCadete:
		var r db.GetOrderForCourierRow
		r, err = s.store.GetOrderForCourier(ctx, db.GetOrderForCourierParams{ID: id, CourierID: u.ID})
		row = db.GetOrderRow(r)
	default:
		return Order{}, ErrNotFound
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, fmt.Errorf("buscando pedido: %w", err)
	}
	return toOrder(db.ListOrdersRow(row)), nil
}

// ListFor devuelve los pedidos que puede ver el usuario: el admin, todos; el
// cadete, solo los asignados a él. Cualquier otro rol no ve nada.
func (s *Service) ListFor(ctx context.Context, u auth.User) ([]Order, error) {
	switch u.Role {
	case auth.RoleAdmin:
		rows, err := s.store.ListOrders(ctx)
		if err != nil {
			return nil, fmt.Errorf("listando pedidos: %w", err)
		}
		out := make([]Order, 0, len(rows))
		for _, r := range rows {
			out = append(out, toOrder(r))
		}
		return out, nil
	case auth.RoleCadete:
		rows, err := s.store.ListOrdersByCourier(ctx, u.ID)
		if err != nil {
			return nil, fmt.Errorf("listando pedidos del cadete: %w", err)
		}
		out := make([]Order, 0, len(rows))
		for _, r := range rows {
			out = append(out, toOrder(db.ListOrdersRow(r)))
		}
		return out, nil
	default:
		return []Order{}, nil
	}
}

// ActiveCouriers devuelve los cadetes activos, para elegir al crear un pedido.
func (s *Service) ActiveCouriers(ctx context.Context) ([]Courier, error) {
	rows, err := s.store.ListActiveCouriers(ctx)
	if err != nil {
		return nil, fmt.Errorf("listando cadetes activos: %w", err)
	}
	out := make([]Courier, 0, len(rows))
	for _, r := range rows {
		out = append(out, Courier{ID: r.ID, Username: r.Username, Name: r.Name})
	}
	return out, nil
}

func cleanAddress(s, which string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", InputError("falta la dirección de " + which)
	}
	if utf8.RuneCountInString(s) > MaxAddressLen {
		return "", InputError(fmt.Sprintf("la dirección de %s no puede tener más de %d caracteres", which, MaxAddressLen))
	}
	return s, nil
}

func toOrder(r db.ListOrdersRow) Order {
	return Order{
		ID:             r.ID,
		PickupAddress:  r.PickupAddress,
		DropoffAddress: r.DropoffAddress,
		PriceCents:     r.PriceCents,
		Status:         Status(r.Status),
		CourierID:      r.CourierID,
		CourierName:    r.CourierName,
		CreatedAt:      r.CreatedAt,
	}
}
