// Pantalla "Rastreo (prueba)" del cadete: obtiene la ubicación con
// navigator.geolocation y la pasa a la cola (cola-posiciones.js), que es la
// que envía. Mide además cuándo la página deja de estar a la vista.

import { crearCola } from './cola-posiciones.js';
import { requerirSesion, hora, hace } from './sesion.js';

const $ = (id) => document.getElementById(id);

function leerEstado(clave) {
  try {
    return JSON.parse(localStorage.getItem(clave)) || {};
  } catch {
    return {};
  }
}

function guardarEstado(clave, estado) {
  try {
    localStorage.setItem(clave, JSON.stringify(estado));
  } catch {
    // sin almacenamiento: el estado vive solo mientras la página esté abierta
  }
}

// numeroOpcional pasa NaN, null o negativos a null: el GPS los da así cuando
// no sabe la velocidad (o el rumbo, si el celular está quieto).
function numeroOpcional(v) {
  return typeof v === 'number' && Number.isFinite(v) && v >= 0 ? v : null;
}

function rumbo(v) {
  const r = numeroOpcional(v);
  return r === null ? null : r % 360;
}

async function main() {
  const usuario = await requerirSesion('cadete', $('acceso'));
  if (!usuario) return;
  $('pantalla').hidden = false;

  const claveEstado = `cadetes.rastreo.estado.v1.${usuario.id}`;
  // rastreando sobrevive a una recarga: si la página se recarga mientras
  // rastreaba (p. ej. iOS la descartó en segundo plano), se retoma sola.
  const estado = {
    rastreando: false, segundoPlano: 0, reanudaciones: 0, erroresGps: 0, ultima: null,
    ...leerEstado(claveEstado),
  };
  const guardar = () => guardarEstado(claveEstado, estado);

  let watchId = null;
  let ultimoTimestamp = null;
  let wakeLock = null;

  const cola = crearCola({
    clave: `cadetes.rastreo.cola.v1.${usuario.id}`,
    alCambiar: mostrarCola,
  });

  function mostrarCola(e) {
    $('enviadas').textContent = e.enviadas;
    $('pendientes').textContent = e.pendientes + (e.enviando ? ' (enviando…)' : '');
    $('descartadas').textContent = e.descartadas;
    $('error-envio').textContent = [e.ultimoError, e.ultimoRechazo].filter(Boolean).join(' · ');
    $('sin-almacenamiento').hidden = e.persistida;
  }

  function mostrarEstado() {
    $('estado').textContent = estado.rastreando ? 'Rastreando' : 'Detenido';
    $('estado').dataset.rastreando = String(estado.rastreando);
    $('empezar').disabled = estado.rastreando;
    $('detener').disabled = !estado.rastreando;
    $('segundo-plano').textContent = `${estado.segundoPlano} ${estado.segundoPlano === 1 ? 'vez' : 'veces'}`;
    $('reanudaciones').textContent = estado.reanudaciones;
    $('errores-gps').textContent = estado.erroresGps;
    if (estado.ultima) {
      const t = new Date(estado.ultima.capturada_en);
      $('precision').textContent = `${Math.round(estado.ultima.precision_m)} m`;
      $('ultima').textContent = `${hora.format(t)} (${hace(t)})`;
    } else {
      $('precision').textContent = '—';
      $('ultima').textContent = '—';
    }
  }

  function alRecibir(pos) {
    // Algunos navegadores repiten la misma lectura: el servidor la descartaría
    // como duplicada (mismo cadete y misma hora) y los contadores no cerrarían.
    if (pos.timestamp === ultimoTimestamp) return;
    ultimoTimestamp = pos.timestamp;
    const c = pos.coords;
    const p = {
      lat: c.latitude,
      lng: c.longitude,
      precision_m: c.accuracy,
      velocidad_ms: numeroOpcional(c.speed),
      rumbo_grados: rumbo(c.heading),
      // toISOString siempre incluye milisegundos: sin ellos, dos posiciones
      // del mismo segundo se tomarían como repetidas.
      capturada_en: new Date(pos.timestamp).toISOString(),
      origen: 'pwa',
      visible: document.visibilityState === 'visible',
    };
    cola.agregar(p);
    estado.ultima = { precision_m: p.precision_m, capturada_en: p.capturada_en };
    $('error-gps').textContent = '';
    guardar();
    mostrarEstado();
  }

  function alFallar(err) {
    if (err.code === err.PERMISSION_DENIED) {
      $('ayuda-permiso').hidden = false;
      $('error-gps').textContent = 'Se negó el permiso de ubicación.';
      detener();
      return;
    }
    // POSITION_UNAVAILABLE o TIMEOUT: watchPosition sigue intentando solo.
    estado.erroresGps++;
    $('error-gps').textContent = `El GPS no dio la ubicación (${err.code === err.TIMEOUT ? 'tardó demasiado' : 'no disponible'}): ${err.message}`;
    guardar();
    mostrarEstado();
  }

  function empezar() {
    if (!('geolocation' in navigator)) {
      $('error-gps').textContent = 'Este navegador no da la ubicación.';
      return;
    }
    $('ayuda-permiso').hidden = true;
    $('error-gps').textContent = '';
    if (watchId === null) {
      watchId = navigator.geolocation.watchPosition(alRecibir, alFallar, {
        enableHighAccuracy: true,
        maximumAge: 0,
      });
    }
    estado.rastreando = true;
    guardar();
    mostrarEstado();
    pedirWakeLock();
  }

  function detener() {
    if (watchId !== null) navigator.geolocation.clearWatch(watchId);
    watchId = null;
    estado.rastreando = false;
    guardar();
    mostrarEstado();
    soltarWakeLock();
    cola.enviarAhora();
  }

  // --- Mantener pantalla encendida (Screen Wake Lock) ---

  const soportaWakeLock = 'wakeLock' in navigator;
  $('wake-lock').hidden = !soportaWakeLock;

  async function pedirWakeLock() {
    if (!soportaWakeLock || !$('pantalla-encendida').checked || !estado.rastreando) return;
    if (wakeLock || document.visibilityState !== 'visible') return;
    try {
      wakeLock = await navigator.wakeLock.request('screen');
      $('estado-wake-lock').textContent = '(activo)';
      // El sistema lo suelta solo al ocultarse la página; al volver se pide
      // de nuevo (ver visibilitychange).
      wakeLock.addEventListener('release', () => {
        wakeLock = null;
        $('estado-wake-lock').textContent = $('pantalla-encendida').checked ? '(en pausa)' : '';
      });
    } catch (err) {
      $('estado-wake-lock').textContent = `(no se pudo: ${err.message})`;
    }
  }

  function soltarWakeLock() {
    if (wakeLock) wakeLock.release().catch(() => {});
    wakeLock = null;
  }

  $('pantalla-encendida').addEventListener('change', () => {
    if ($('pantalla-encendida').checked) {
      pedirWakeLock();
    } else {
      soltarWakeLock();
      $('estado-wake-lock').textContent = '';
    }
  });

  // --- Eventos ---

  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'hidden' && estado.rastreando) {
      estado.segundoPlano++;
      guardar();
    }
    if (document.visibilityState === 'visible') pedirWakeLock();
    mostrarEstado();
  });

  $('empezar').addEventListener('click', () => {
    // Cada prueba empieza con los contadores en cero. La cola no se toca: si
    // quedaron pendientes de antes, se siguen enviando.
    Object.assign(estado, { segundoPlano: 0, reanudaciones: 0, erroresGps: 0, ultima: null });
    cola.reiniciarContadores();
    empezar();
  });
  $('detener').addEventListener('click', detener);

  $('inseguro').hidden = window.isSecureContext;

  // Si el permiso ya estaba negado, se muestra la ayuda antes de tocar nada.
  try {
    const permiso = await navigator.permissions.query({ name: 'geolocation' });
    if (permiso.state === 'denied') $('ayuda-permiso').hidden = false;
  } catch {
    // navigator.permissions no existe o no conoce "geolocation"
  }

  cola.iniciar();
  cola.enviarAhora(); // lo que quedó pendiente de antes de recargar
  if (estado.rastreando) {
    estado.reanudaciones++;
    empezar();
  }
  mostrarEstado();
  setInterval(mostrarEstado, 1000); // el "hace X s" de la última posición
}

main();
