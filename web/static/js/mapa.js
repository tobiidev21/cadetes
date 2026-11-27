// Pantalla "Mapa (prueba)" del admin: última posición y recorrido de las
// últimas 2 h de cada cadete, en vivo por el stream SSE, y el resumen de
// /api/posiciones/resumen para medir cada prueba.
//
// Leaflet se carga antes como script clásico (/vendor/leaflet/leaflet.js) y
// deja el global L.

import { requerirSesion, hora, hace } from './sesion.js';

const L = window.L;
const $ = (id) => document.getElementById(id);

const VENTANA_RECORRIDO_MS = 2 * 60 * 60 * 1000;
const CENTRO_INICIAL = [-35.5772, -58.0094]; // Chascomús, solo si no hay posiciones
const COLORES = ['#1d4ed8', '#dc2626', '#16a34a', '#9333ea', '#ea580c', '#0891b2', '#db2777', '#4d7c0f'];
const RECONEXION_MAX_MS = 30_000;

const fechaHora = new Intl.DateTimeFormat('es-AR', {
  dateStyle: 'short', timeStyle: 'medium', hourCycle: 'h23', timeZone: 'America/Argentina/Buenos_Aires',
});
const numero = new Intl.NumberFormat('es-AR', { maximumFractionDigits: 3 });

async function getJSON(url) {
  const res = await fetch(url);
  const cuerpo = await res.json().catch(() => null);
  if (!res.ok) {
    const err = new Error((cuerpo && cuerpo.error) || `El servidor respondió ${res.status}`);
    err.status = res.status;
    throw err;
  }
  return cuerpo;
}

const t = (p) => Date.parse(p.capturada_en);

// "2026-11-27T14:05:09" en la hora local del navegador, para datetime-local.
function aLocal(fecha) {
  const d = new Date(fecha.getTime() - fecha.getTimezoneOffset() * 60_000);
  return d.toISOString().slice(0, 19);
}

async function main() {
  const usuario = await requerirSesion('admin', $('acceso'));
  if (!usuario) return;
  $('pantalla').hidden = false; // antes de crear el mapa: Leaflet necesita el tamaño

  const mapa = L.map('mapa').setView(CENTRO_INICIAL, 14);
  // Política de OpenStreetMap: crédito visible y solo los mosaicos de la vista
  // (Leaflet no precarga). Sirve para la prueba, no para producción.
  L.tileLayer('https://tile.openstreetmap.org/{z}/{x}/{y}.png', {
    maxZoom: 19,
    attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a>',
  }).addTo(mapa);

  // id del cadete → { id, nombre, color, ultima, recorrido, ids, marcador, circulo, linea, boton }
  const cadetes = new Map();
  let seleccionado = null;
  let encuadrado = false;

  function cadete(id, nombre) {
    let c = cadetes.get(id);
    if (!c) {
      const color = COLORES[cadetes.size % COLORES.length];
      c = {
        id, nombre: nombre || `Cadete ${id}`, color, ultima: null, recorrido: [], ids: new Set(),
        marcador: null, circulo: null,
        linea: L.polyline([], { color, weight: 4, opacity: 0.7 }).addTo(mapa),
        boton: null,
      };
      cadetes.set(id, c);
      agregarALista(c);
    } else if (nombre && c.nombre !== nombre) {
      c.nombre = nombre;
    }
    return c;
  }

  // agregar suma una posición al cadete: la usa como última si es la más
  // nueva y la mete en el recorrido en su lugar (al vaciarse la cola del
  // celular llegan posiciones viejas). Se ignora si ya estaba (por id).
  function agregar(c, p) {
    if (!c.ultima || t(p) >= t(c.ultima)) c.ultima = p;
    if (c.ids.has(p.id) || t(p) < Date.now() - VENTANA_RECORRIDO_MS) return;
    c.ids.add(p.id);
    let i = c.recorrido.length;
    while (i > 0 && t(c.recorrido[i - 1]) > t(p)) i--;
    c.recorrido.splice(i, 0, p);
  }

  function recortar(c) {
    const limite = Date.now() - VENTANA_RECORRIDO_MS;
    let i = 0;
    while (i < c.recorrido.length && t(c.recorrido[i]) < limite) c.ids.delete(c.recorrido[i++].id);
    if (i > 0) c.recorrido.splice(0, i);
  }

  function etiqueta(c) {
    const el = document.createElement('span');
    el.className = 'etiqueta-cadete';
    el.textContent = `${c.nombre} · ${hora.format(new Date(c.ultima.capturada_en))}`;
    return el;
  }

  function dibujar(c) {
    recortar(c);
    c.linea.setLatLngs(c.recorrido.map((p) => [p.lat, p.lng]));
    if (!c.ultima) return;
    const punto = [c.ultima.lat, c.ultima.lng];
    if (!c.marcador) {
      c.circulo = L.circle(punto, { radius: c.ultima.precision_m, color: c.color, weight: 1, fillOpacity: 0.1, interactive: false }).addTo(mapa);
      c.marcador = L.circleMarker(punto, { radius: 8, color: '#fff', weight: 2, fillColor: c.color, fillOpacity: 1 })
        .bindTooltip(etiqueta(c), { permanent: true, direction: 'right', offset: [10, 0] })
        .on('click', () => seleccionar(c.id))
        .addTo(mapa);
    } else {
      c.marcador.setLatLng(punto).setTooltipContent(etiqueta(c));
      c.circulo.setLatLng(punto).setRadius(c.ultima.precision_m);
    }
    actualizarLista(c);
    if (seleccionado === c.id) mostrarDetalle();
  }

  // --- Panel ---

  function agregarALista(c) {
    const li = document.createElement('li');
    c.boton = document.createElement('button');
    c.boton.type = 'button';
    c.boton.setAttribute('aria-pressed', 'false');
    c.boton.addEventListener('click', () => seleccionar(c.id));
    li.append(c.boton);
    $('lista-cadetes').append(li);
    $('sin-cadetes').hidden = true;
    actualizarLista(c);
  }

  function actualizarLista(c) {
    const color = document.createElement('span');
    color.className = 'color';
    color.style.background = c.color;
    const cuando = c.ultima ? ` — ${hace(new Date(c.ultima.capturada_en))}` : '';
    c.boton.replaceChildren(color, `${c.nombre}${cuando}`);
  }

  function seleccionar(id) {
    const cambio = seleccionado !== id;
    seleccionado = id;
    for (const c of cadetes.values()) c.boton.setAttribute('aria-pressed', String(c.id === id));
    if (cambio) {
      $('resumen').hidden = true;
      $('r-json').hidden = true;
      $('error-resumen').textContent = '';
      const ahora = new Date();
      const form = $('form-resumen');
      form.desde.value = aLocal(new Date(ahora.getTime() - 60 * 60 * 1000));
      form.hasta.value = aLocal(ahora);
    }
    $('detalle-cadete').hidden = false;
    mostrarDetalle();
  }

  function mostrarDetalle() {
    const c = cadetes.get(seleccionado);
    if (!c) return;
    $('nombre-cadete').textContent = c.nombre;
    const u = c.ultima;
    if (!u) return;
    const capturada = new Date(u.capturada_en);
    const demora = (Date.parse(u.recibida_en) - capturada.getTime()) / 1000;
    $('d-ultima').textContent = `${fechaHora.format(capturada)} (${hace(capturada)})`;
    $('d-demora').textContent = `${numero.format(demora)} s`;
    $('d-precision').textContent = `${numero.format(u.precision_m)} m`;
    $('d-coordenadas').textContent = `${u.lat.toFixed(5)}, ${u.lng.toFixed(5)}`;
    $('d-origen').textContent = u.origen;
    $('d-visible').textContent = u.visible === null ? '—' : u.visible ? 'sí' : 'no';
    $('d-recorrido').textContent = `${c.recorrido.length} posiciones`;
  }

  const seg = (v) => (v === null ? '—' : `${numero.format(v)} s`);

  $('form-resumen').addEventListener('submit', async (e) => {
    e.preventDefault();
    const form = e.target;
    $('error-resumen').textContent = '';
    const desde = new Date(form.desde.value);
    const hasta = new Date(form.hasta.value);
    if (Number.isNaN(desde.getTime()) || Number.isNaN(hasta.getTime())) {
      $('error-resumen').textContent = 'Completá desde y hasta.';
      return;
    }
    const params = new URLSearchParams({
      cadete: String(seleccionado), desde: desde.toISOString(), hasta: hasta.toISOString(),
    });
    const url = `/api/posiciones/resumen?${params}`;
    try {
      const r = await getJSON(url);
      $('r-posiciones').textContent = r.posiciones;
      $('r-primera').textContent = r.primera ? fechaHora.format(new Date(r.primera)) : '—';
      $('r-ultima').textContent = r.ultima ? fechaHora.format(new Date(r.ultima)) : '—';
      $('r-intervalo').textContent = seg(r.intervalo_medio_s);
      $('r-huecos').textContent = `${r.huecos_60s.cantidad} / mayor: ${seg(r.huecos_60s.mayor_s)}`;
      $('r-demora-media').textContent = seg(r.demora_media_s);
      $('r-demora-max').textContent = seg(r.demora_max_s);
      $('r-precision').textContent = r.precision_media_m === null ? '—' : `${numero.format(r.precision_media_m)} m`;
      $('r-no-visibles').textContent = r.no_visibles;
      $('resumen').hidden = false;
      $('r-json-link').href = url;
      $('r-json').hidden = false;
    } catch (err) {
      $('error-resumen').textContent = err.message;
    }
  });

  // --- Datos: carga completa y stream ---

  // sincronizar vuelve a pedir todo: al abrir la pantalla y cada vez que el
  // stream se (re)conecta, para no perder lo que llegó mientras estaba
  // cortado. Los eventos que llegan a la vez no se duplican (se juntan por id).
  async function sincronizar() {
    try {
      const ultimas = await getJSON('/api/posiciones/ultimas');
      for (const p of ultimas) agregar(cadete(p.cadete_id, p.cadete_nombre), p);
      const desde = new Date(Date.now() - VENTANA_RECORRIDO_MS).toISOString();
      await Promise.all([...cadetes.values()].map(async (c) => {
        const params = new URLSearchParams({ cadete: String(c.id), desde });
        const recorrido = await getJSON(`/api/posiciones/recorrido?${params}`);
        for (const p of recorrido) agregar(c, p);
        dibujar(c);
      }));
      $('error-mapa').textContent = '';
      if (!encuadrado && cadetes.size > 0) {
        encuadrado = true;
        const puntos = [...cadetes.values()].filter((c) => c.ultima).map((c) => [c.ultima.lat, c.ultima.lng]);
        mapa.fitBounds(puntos, { maxZoom: 16, padding: [40, 40] });
      }
    } catch (err) {
      $('error-mapa').textContent = `No se pudieron cargar las posiciones: ${err.message}`;
    }
  }

  function alLlegar(lista) {
    const tocados = new Set();
    for (const p of lista) {
      const c = cadete(p.cadete_id, p.cadete_nombre);
      agregar(c, p);
      tocados.add(c);
    }
    for (const c of tocados) dibujar(c);
  }

  // El navegador reconecta solo el EventSource cuando el stream se corta
  // (Railway lo corta a los 15 min). Si el servidor responde algo que no es un
  // stream (502 durante un deploy, 401), el EventSource queda cerrado del todo
  // y hay que crearlo de nuevo.
  let fuente = null;
  let intentos = 0;
  let reconexion = null;

  function conectar() {
    reconexion = null;
    fuente = new EventSource('/api/posiciones/stream');
    fuente.addEventListener('open', () => {
      intentos = 0;
      $('conexion').hidden = true;
      sincronizar();
    });
    fuente.addEventListener('posiciones', (e) => {
      try {
        alLlegar(JSON.parse(e.data));
      } catch (err) {
        console.error('Evento de posiciones inválido', err, e.data);
      }
    });
    fuente.addEventListener('error', async () => {
      $('conexion').textContent = 'Reconectando…';
      $('conexion').hidden = false;
      if (fuente.readyState !== EventSource.CLOSED || reconexion) return;
      fuente.close();
      try {
        const res = await fetch('/api/sesion');
        if (res.status === 401) {
          $('conexion').textContent = 'La sesión venció: recargá la página e iniciá sesión.';
          return;
        }
      } catch {
        // sin red: se reintenta igual
      }
      const espera = Math.min(RECONEXION_MAX_MS, 1000 * 2 ** intentos++);
      reconexion = setTimeout(conectar, espera);
    });
  }

  await sincronizar();
  conectar();

  // Cada segundo: el "hace X s" del panel. Cada minuto: recortar los
  // recorridos a las últimas 2 h aunque no lleguen posiciones.
  setInterval(() => {
    for (const c of cadetes.values()) actualizarLista(c);
    mostrarDetalle();
  }, 1000);
  setInterval(() => {
    for (const c of cadetes.values()) dibujar(c);
  }, 60_000);
}

main();
