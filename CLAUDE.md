# Cadetes — prueba técnica

Sistema de gestión de envíos para una cadetería de Chascomús (admin, cadetes y, más adelante, comercios y clientes invitados). Toda la interfaz va en español (es-AR).

**Etapa actual: prueba técnica (spike).** Antes de cualquier tarea, leé `docs/plan-prueba.md`: tiene el objetivo, el stack cerrado, el modelo de datos y el plan paso a paso.

## Reglas

- Stack fijo: Go con `net/http` estándar, Postgres con `pgx/v5`, `sqlc`, `goose`, `webpush-go`, frontend Vite como PWA embebido con `go:embed`, deploy con Dockerfile en Railway. No agregues frameworks ni servicios sin consultarlo.
- Nada que dependa de un proveedor en particular: la app tiene que correr en cualquier lado con el mismo Dockerfile.
- Una rama por paso (`spike/<paso>`), PR para revisión. No mergees vos.
- Hacé solo lo que pide el paso actual. Lo que figura como "fuera de la prueba" no se toca.
- Nunca edites migraciones ya aplicadas: creá una nueva.
- El servidor escucha en el puerto de la variable `PORT` (Railway la define); si no existe, usa 8080.
- Plata siempre en centavos (enteros).
- Sin usuarios ni datos demo en el código. El primer admin sale de `ADMIN_INITIAL_PASSWORD`.
- Al terminar cada paso, actualizá `docs/estado.md` con qué se hizo y cómo se probó.
