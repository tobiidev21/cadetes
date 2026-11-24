package orders

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tobiidev21/cadetes/internal/auth"
	"github.com/tobiidev21/cadetes/internal/db"
)

// fakeStore es una base en memoria que imita las consultas de sqlc.
type fakeStore struct {
	mu     sync.Mutex
	nextID int64
	users  map[int64]db.User
	orders []db.Order

	failList error // si no es nil, ListOrders y ListOrdersByCourier lo devuelven
}

func newFakeStore(users ...db.User) *fakeStore {
	f := &fakeStore{users: map[int64]db.User{}}
	for _, u := range users {
		f.users[u.ID] = u
	}
	return f
}

func (f *fakeStore) CreateAssignedOrder(_ context.Context, arg db.CreateAssignedOrderParams) (db.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.users[arg.CourierID]
	if !ok || c.Role != string(auth.RoleCadete) || !c.Active {
		return db.Order{}, pgx.ErrNoRows
	}
	f.nextID++
	courier := c.ID
	o := db.Order{
		ID: f.nextID, PickupAddress: arg.PickupAddress, DropoffAddress: arg.DropoffAddress,
		PriceCents: arg.PriceCents, Status: string(StatusAsignado), CourierID: &courier,
		CreatedBy: arg.CreatedBy, CreatedAt: time.Now(),
	}
	f.orders = append(f.orders, o)
	return o, nil
}

func (f *fakeStore) row(o db.Order) db.ListOrdersRow {
	r := db.ListOrdersRow{
		ID: o.ID, PickupAddress: o.PickupAddress, DropoffAddress: o.DropoffAddress,
		PriceCents: o.PriceCents, Status: o.Status, CourierID: o.CourierID, CreatedAt: o.CreatedAt,
	}
	if o.CourierID != nil {
		name := f.users[*o.CourierID].Name
		r.CourierName = &name
	}
	return r
}

func (f *fakeStore) ListOrders(context.Context) ([]db.ListOrdersRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failList != nil {
		return nil, f.failList
	}
	out := []db.ListOrdersRow{}
	for _, o := range slices.Backward(f.orders) {
		out = append(out, f.row(o))
	}
	return out, nil
}

func (f *fakeStore) ListOrdersByCourier(_ context.Context, courierID int64) ([]db.ListOrdersByCourierRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failList != nil {
		return nil, f.failList
	}
	out := []db.ListOrdersByCourierRow{}
	for _, o := range slices.Backward(f.orders) {
		if o.CourierID != nil && *o.CourierID == courierID {
			out = append(out, db.ListOrdersByCourierRow(f.row(o)))
		}
	}
	return out, nil
}

func (f *fakeStore) ListActiveCouriers(context.Context) ([]db.ListActiveCouriersRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []db.ListActiveCouriersRow{}
	for _, u := range f.users {
		if u.Role == string(auth.RoleCadete) && u.Active {
			out = append(out, db.ListActiveCouriersRow{ID: u.ID, Username: u.Username, Name: u.Name})
		}
	}
	slices.SortFunc(out, func(a, b db.ListActiveCouriersRow) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

var (
	admin    = auth.User{ID: 1, Username: "admin", Name: "Administrador", Role: auth.RoleAdmin}
	juan     = auth.User{ID: 2, Username: "juan", Name: "Juan", Role: auth.RoleCadete}
	ana      = auth.User{ID: 3, Username: "ana", Name: "Ana", Role: auth.RoleCadete}
	inactivo = auth.User{ID: 4, Username: "pedro", Name: "Pedro", Role: auth.RoleCadete}
)

func dbUser(u auth.User, active bool) db.User {
	return db.User{ID: u.ID, Username: u.Username, Name: u.Name, Role: string(u.Role), Active: active}
}

func newTestService() (*Service, *fakeStore) {
	store := newFakeStore(
		dbUser(admin, true), dbUser(juan, true), dbUser(ana, true), dbUser(inactivo, false),
	)
	return NewService(store), store
}

func validOrder(courierID int64) NewOrder {
	return NewOrder{
		PickupAddress:  "Lavalle 123",
		DropoffAddress: "Belgrano 456",
		PriceCents:     150050,
		CourierID:      courierID,
	}
}

func TestCreateAssignsToCourier(t *testing.T) {
	svc, store := newTestService()
	in := validOrder(juan.ID)
	in.PickupAddress = "  Lavalle 123 "
	in.DropoffAddress = "\tBelgrano 456\n"

	o, err := svc.Create(context.Background(), admin, in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if o.Status != StatusAsignado || o.CourierID == nil || *o.CourierID != juan.ID {
		t.Fatalf("pedido = %+v, quería asignado a %d", o, juan.ID)
	}
	if o.PickupAddress != "Lavalle 123" || o.DropoffAddress != "Belgrano 456" {
		t.Errorf("direcciones sin recortar: %q, %q", o.PickupAddress, o.DropoffAddress)
	}
	if o.PriceCents != 150050 {
		t.Errorf("precio = %d, quería 150050", o.PriceCents)
	}
	if got := store.orders[0].CreatedBy; got != admin.ID {
		t.Errorf("created_by = %d, quería %d", got, admin.ID)
	}
}

func TestCreateValidation(t *testing.T) {
	cases := map[string]func(*NewOrder){
		"sin retiro":         func(o *NewOrder) { o.PickupAddress = "" },
		"retiro con blancos": func(o *NewOrder) { o.PickupAddress = "   " },
		"sin entrega":        func(o *NewOrder) { o.DropoffAddress = "" },
		"retiro muy largo":   func(o *NewOrder) { o.PickupAddress = strings.Repeat("ñ", MaxAddressLen+1) },
		"precio cero":        func(o *NewOrder) { o.PriceCents = 0 },
		"precio negativo":    func(o *NewOrder) { o.PriceCents = -100 },
		"sin cadete":         func(o *NewOrder) { o.CourierID = 0 },
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			svc, store := newTestService()
			in := validOrder(juan.ID)
			mod(&in)
			_, err := svc.Create(context.Background(), admin, in)
			var inputErr InputError
			if !errors.As(err, &inputErr) {
				t.Fatalf("err = %v, quería InputError", err)
			}
			if len(store.orders) != 0 {
				t.Errorf("se creó un pedido inválido")
			}
		})
	}
}

func TestCreateAddressAtLimit(t *testing.T) {
	svc, _ := newTestService()
	in := validOrder(juan.ID)
	in.PickupAddress = strings.Repeat("ñ", MaxAddressLen) // 200 caracteres, 400 bytes
	if _, err := svc.Create(context.Background(), admin, in); err != nil {
		t.Fatalf("Create con %d caracteres: %v", MaxAddressLen, err)
	}
}

func TestCreateRejectsInvalidCourier(t *testing.T) {
	cases := map[string]int64{
		"inexistente": 99,
		"inactivo":    inactivo.ID,
		"es admin":    admin.ID,
	}
	for name, id := range cases {
		t.Run(name, func(t *testing.T) {
			svc, store := newTestService()
			_, err := svc.Create(context.Background(), admin, validOrder(id))
			if !errors.Is(err, ErrInvalidCourier) {
				t.Fatalf("err = %v, quería ErrInvalidCourier", err)
			}
			if len(store.orders) != 0 {
				t.Errorf("se creó el pedido igual")
			}
		})
	}
}

func TestListForCourierSeesOnlyOwn(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()
	for _, id := range []int64{juan.ID, ana.ID, juan.ID} {
		if _, err := svc.Create(ctx, admin, validOrder(id)); err != nil {
			t.Fatal(err)
		}
	}

	mine, err := svc.ListFor(ctx, juan)
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 2 {
		t.Fatalf("juan ve %d pedidos, quería 2", len(mine))
	}
	for _, o := range mine {
		if *o.CourierID != juan.ID {
			t.Errorf("juan ve el pedido %d de otro cadete", o.ID)
		}
		if o.CourierName == nil || *o.CourierName != "Juan" {
			t.Errorf("courier_name = %v, quería Juan", o.CourierName)
		}
	}
	if mine[0].ID != 3 || mine[1].ID != 1 {
		t.Errorf("orden = %d, %d; quería el más nuevo primero (3, 1)", mine[0].ID, mine[1].ID)
	}

	all, err := svc.ListFor(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("el admin ve %d pedidos, quería 3", len(all))
	}

	nadie, err := svc.ListFor(ctx, auth.User{ID: 50, Role: "otro"})
	if err != nil || len(nadie) != 0 {
		t.Errorf("rol desconocido: %d pedidos, err %v; quería ninguno", len(nadie), err)
	}

	sinPedidos, err := svc.ListFor(ctx, auth.User{ID: 77, Role: auth.RoleCadete})
	if err != nil || sinPedidos == nil || len(sinPedidos) != 0 {
		t.Errorf("cadete sin pedidos: %v, err %v; quería lista vacía", sinPedidos, err)
	}
}

func TestActiveCouriers(t *testing.T) {
	svc, _ := newTestService()
	got, err := svc.ActiveCouriers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range got {
		names = append(names, c.Name)
	}
	if want := []string{"Ana", "Juan"}; !slices.Equal(names, want) {
		t.Errorf("cadetes = %v, quería %v (sin admin ni inactivos)", names, want)
	}
}

// --- HTTP ---

func do(h http.HandlerFunc, u *auth.User, method, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/pedidos", strings.NewReader(body))
	if u != nil {
		req = req.WithContext(auth.ContextWithUser(req.Context(), *u))
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestHTTPCreate(t *testing.T) {
	svc, _ := newTestService()
	h := NewHandler(svc)

	rec := do(h.Create, &admin, http.MethodPost,
		`{"pickup_address":"Lavalle 123","dropoff_address":"Belgrano 456","price_cents":150050,"courier_id":2}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, cuerpo %s", rec.Code, rec.Body)
	}
	var o Order
	if err := json.Unmarshal(rec.Body.Bytes(), &o); err != nil {
		t.Fatal(err)
	}
	if o.Status != StatusAsignado || o.PriceCents != 150050 || *o.CourierID != 2 {
		t.Errorf("respuesta = %+v", o)
	}

	bad := map[string]string{
		"precio con decimales": `{"pickup_address":"a","dropoff_address":"b","price_cents":1500.5,"courier_id":2}`,
		"precio como texto":    `{"pickup_address":"a","dropoff_address":"b","price_cents":"1500","courier_id":2}`,
		"campo desconocido":    `{"pickup_address":"a","dropoff_address":"b","price_cents":1500,"courier_id":2,"status":"entregado"}`,
		"cadete inactivo":      `{"pickup_address":"a","dropoff_address":"b","price_cents":1500,"courier_id":4}`,
		"sin precio":           `{"pickup_address":"a","dropoff_address":"b","courier_id":2}`,
	}
	for name, body := range bad {
		if rec := do(h.Create, &admin, http.MethodPost, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, quería 400 (%s)", name, rec.Code, rec.Body)
		}
	}

	if rec := do(h.Create, nil, http.MethodPost, `{}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("sin usuario: status = %d, quería 401", rec.Code)
	}
}

func TestHTTPList(t *testing.T) {
	svc, store := newTestService()
	h := NewHandler(svc)
	if _, err := svc.Create(context.Background(), admin, validOrder(ana.ID)); err != nil {
		t.Fatal(err)
	}

	rec := do(h.List, &juan, http.MethodGet, "")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("juan: status %d, cuerpo %s; quería 200 y []", rec.Code, rec.Body)
	}
	rec = do(h.List, &ana, http.MethodGet, "")
	var got []Order
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got) != 1 {
		t.Errorf("ana: %s; quería un pedido", rec.Body)
	}

	if rec := do(h.List, nil, http.MethodGet, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("sin usuario: status = %d, quería 401", rec.Code)
	}

	store.failList = errors.New("se cayó la base")
	rec = do(h.List, &admin, http.MethodGet, "")
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "base") {
		t.Errorf("error de base: status %d, cuerpo %s; quería 500 sin detalles", rec.Code, rec.Body)
	}
}
