// Cola y envío de posiciones a POST /api/posiciones.
//
// No sabe de dónde salen las posiciones: la PWA las agrega desde
// navigator.geolocation (rastreo.js) y en la parte 2b se suma el plugin
// nativo, que usa esta misma cola con origen "nativa".
//
// Toda posición entra a la cola y se guarda en localStorage, así sobrevive a
// una recarga. Cada INTERVALO_ENVIO_MS se manda en lotes de hasta LOTE_MAX
// hasta vaciarla o hasta que falle un envío. Antes de mandar se descartan las
// que el servidor rechazaría (mismas reglas que internal/positions), porque un
// 400 rechaza el lote entero.

export const TOPE_COLA = 2000;
export const LOTE_MAX = 100; // positions.MaxBatch
export const INTERVALO_ENVIO_MS = 10_000;

const MAX_FUTURO_MS = 60_000; // positions.MaxFuture
const MAX_PASADO_MS = 24 * 60 * 60 * 1000; // positions.MaxPast
const TIMEOUT_ENVIO_MS = 20_000;
const ESPERA_429_POR_DEFECTO_MS = 5_000;

function esNumero(v) {
  return typeof v === 'number' && Number.isFinite(v);
}

// validarPosicion devuelve '' si la posición es válida o el motivo si no.
// Son las reglas de validateOne en internal/positions/positions.go.
export function validarPosicion(p, ahora = Date.now()) {
  if (!esNumero(p.lat)) return 'falta la latitud';
  if (p.lat < -90 || p.lat > 90) return 'latitud fuera de rango';
  if (!esNumero(p.lng)) return 'falta la longitud';
  if (p.lng < -180 || p.lng > 180) return 'longitud fuera de rango';
  if (!esNumero(p.precision_m)) return 'falta la precisión';
  if (p.precision_m < 0) return 'precisión negativa';
  if (p.velocidad_ms != null && (!esNumero(p.velocidad_ms) || p.velocidad_ms < 0)) return 'velocidad inválida';
  if (p.rumbo_grados != null && (!esNumero(p.rumbo_grados) || p.rumbo_grados < 0 || p.rumbo_grados >= 360)) return 'rumbo inválido';
  const t = typeof p.capturada_en === 'string' ? Date.parse(p.capturada_en) : NaN;
  if (!Number.isFinite(t)) return 'falta la hora de captura';
  if (t > ahora + MAX_FUTURO_MS) return 'capturada más de 1 minuto en el futuro';
  if (t < ahora - MAX_PASADO_MS) return 'capturada hace más de 24 h';
  if (p.origen !== 'pwa' && p.origen !== 'nativa') return 'origen inválido';
  if (p.visible != null && typeof p.visible !== 'boolean') return 'visible inválido';
  return '';
}

// Solo los campos que acepta el servidor: uno de más da 400.
function normalizar(p) {
  return {
    lat: p.lat,
    lng: p.lng,
    precision_m: p.precision_m,
    velocidad_ms: p.velocidad_ms ?? null,
    rumbo_grados: p.rumbo_grados ?? null,
    capturada_en: p.capturada_en,
    origen: p.origen,
    visible: p.visible ?? null,
  };
}

function leer(clave) {
  try {
    const crudo = localStorage.getItem(clave);
    if (!crudo) return null;
    const datos = JSON.parse(crudo);
    return datos && Array.isArray(datos.posiciones) ? datos : null;
  } catch {
    return null;
  }
}

function escribir(clave, datos) {
  try {
    localStorage.setItem(clave, JSON.stringify(datos));
    return true;
  } catch {
    return false; // modo privado, almacenamiento lleno o bloqueado: la cola sigue en memoria
  }
}

// crearCola arma la cola de un usuario. clave es la de localStorage: tiene que
// incluir el id del cadete, para que si entra otro en el mismo teléfono no se
// manden posiciones ajenas con su sesión. alCambiar recibe el estado (ver
// estado()) cada vez que algo cambia.
export function crearCola({ clave, alCambiar = () => {}, url = '/api/posiciones' }) {
  const guardado = leer(clave);
  let posiciones = guardado ? guardado.posiciones : [];
  let enviadas = guardado ? guardado.enviadas || 0 : 0;
  let descartadas = guardado ? guardado.descartadas || 0 : 0;
  let ultimoError = ''; // de paso: se borra con el próximo envío que anda
  let ultimoRechazo = ''; // un 400: queda hasta reiniciarContadores
  let sinSesion = false;
  let pausadaHasta = 0;
  let enviando = false;
  let persistida = true;
  let temporizador = null;

  function estado() {
    return {
      pendientes: posiciones.length,
      enviadas,
      descartadas,
      ultimoError,
      ultimoRechazo,
      sinSesion,
      pausadaHasta,
      enviando,
      persistida,
    };
  }

  function guardar() {
    persistida = escribir(clave, { posiciones, enviadas, descartadas });
  }

  function cambio() {
    guardar();
    alCambiar(estado());
  }

  function descartar(cuantas, motivo, detalle) {
    descartadas += cuantas;
    console.warn(`Cola de posiciones: se descartaron ${cuantas} (${motivo})`, detalle ?? '');
  }

  function agregar(p) {
    posiciones.push(normalizar(p));
    if (posiciones.length > TOPE_COLA) {
      const sobran = posiciones.length - TOPE_COLA;
      posiciones.splice(0, sobran);
      descartar(sobran, `la cola superó el tope de ${TOPE_COLA}; se sacan las más viejas`);
    }
    cambio();
  }

  function limpiarInvalidas() {
    const ahora = Date.now();
    const validas = [];
    for (const p of posiciones) {
      const motivo = validarPosicion(p, ahora);
      if (motivo) descartar(1, motivo, p);
      else validas.push(p);
    }
    posiciones = validas;
  }

  // quitar saca de la cola las posiciones del lote por identidad: mientras se
  // envía pueden entrar posiciones nuevas o descartarse viejas por el tope.
  function quitar(lote) {
    const enviadasSet = new Set(lote);
    posiciones = posiciones.filter((p) => !enviadasSet.has(p));
  }

  async function enviar() {
    if (enviando) return;
    enviando = true;
    alCambiar(estado());
    try {
      while (posiciones.length > 0 && Date.now() >= pausadaHasta) {
        limpiarInvalidas();
        if (posiciones.length === 0) break;
        const lote = posiciones.slice(0, LOTE_MAX);
        let res;
        try {
          res = await fetch(url, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ posiciones: lote }),
            signal: AbortSignal.timeout(TIMEOUT_ENVIO_MS),
          });
        } catch (err) {
          ultimoError = `Sin conexión con el servidor (${err.name === 'TimeoutError' ? 'tardó demasiado' : 'falló la red'})`;
          break;
        }
        if (res.ok) {
          quitar(lote);
          enviadas += lote.length;
          ultimoError = '';
          sinSesion = false;
          continue;
        }
        const cuerpo = await res.json().catch(() => ({}));
        if (res.status === 429) {
          const segundos = Number(res.headers.get('Retry-After'));
          const espera = Number.isFinite(segundos) && segundos > 0 ? segundos * 1000 : ESPERA_429_POR_DEFECTO_MS;
          pausadaHasta = Date.now() + espera;
          ultimoError = `El servidor pidió esperar ${Math.ceil(espera / 1000)} s (429)`;
          setTimeout(enviar, espera);
          break;
        }
        if (res.status === 401) {
          sinSesion = true;
          ultimoError = 'La sesión venció: volvé a iniciar sesión. Las posiciones siguen en la cola.';
          break;
        }
        if (res.status === 400) {
          // No debería pasar (se validan antes), pero reintentarlo daría el
          // mismo error y trabaría la cola para siempre.
          quitar(lote);
          descartar(lote.length, `el servidor rechazó el lote: ${cuerpo.error || 'sin mensaje'}`, lote);
          console.error('POST /api/posiciones respondió 400:', cuerpo.error);
          ultimoRechazo = `El servidor rechazó un lote de ${lote.length}: ${cuerpo.error || 'sin mensaje'}`;
          continue;
        }
        ultimoError = `El servidor respondió ${res.status}${cuerpo.error ? `: ${cuerpo.error}` : ''}`;
        break;
      }
    } finally {
      enviando = false;
      cambio();
    }
  }

  return {
    agregar,
    estado,
    // enviarAhora no espera al próximo ciclo (p. ej. al tocar "Detener").
    enviarAhora: enviar,
    iniciar() {
      if (temporizador === null) temporizador = setInterval(enviar, INTERVALO_ENVIO_MS);
      alCambiar(estado());
    },
    detener() {
      clearInterval(temporizador);
      temporizador = null;
    },
    reiniciarContadores() {
      enviadas = 0;
      descartadas = 0;
      ultimoRechazo = '';
      cambio();
    },
  };
}
