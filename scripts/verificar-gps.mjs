#!/usr/bin/env node
// Verifica de punta a punta las pantallas "Rastreo (prueba)" y "Mapa (prueba)"
// con Chrome headless, sin instalar nada: Node 22 ya trae WebSocket y fetch, y
// Chrome se maneja con el protocolo de DevTools (CDP), el mismo que usa la
// pestaña de DevTools por dentro.
//
// Abre dos ventanas aisladas (cada una con sus cookies): un cadete en
// /rastreo.html, con la ubicación simulada, y un admin en /mapa.html. Comprueba:
//   1. el marcador del admin sigue al cadete sin recargar la página;
//   2. sin conexión las posiciones quedan en la cola (y en localStorage) y al
//      volver se envían con su hora original;
//   3. al recargar mientras rastrea, retoma solo y cuenta la reanudación;
//   4. el resumen del panel coincide con /api/posiciones/resumen;
//   5. nada se pide a un CDN (solo al servidor y a los mosaicos de OSM).
// Con --servidor-apagado, en vez del paso 2 simulado pide apagar y prender el
// servidor de verdad (espera un Enter en cada paso).
//
// Uso (el cadete tiene que existir; se crea desde la pantalla del admin):
//   VERIF_URL=http://localhost:8080 ADMIN_PASS=… CADETE_USER=… CADETE_PASS=… \
//     node scripts/verificar-gps.mjs [--servidor-apagado] [--ver]
// --ver abre Chrome con ventana en vez de headless.

import { spawn } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createInterface } from 'node:readline';

const BASE = (process.env.VERIF_URL || 'http://localhost:8080').replace(/\/$/, '');
const ADMIN_USER = process.env.ADMIN_USER || 'admin';
const { ADMIN_PASS, CADETE_USER, CADETE_PASS } = process.env;
const CHROME = process.env.CHROME || 'google-chrome';
const SERVIDOR_APAGADO = process.argv.includes('--servidor-apagado');
const VER = process.argv.includes('--ver');

if (!ADMIN_PASS || !CADETE_USER || !CADETE_PASS) {
  console.error('Faltan ADMIN_PASS, CADETE_USER o CADETE_PASS (ver el comentario al principio del script).');
  process.exit(2);
}

const pausa = (ms) => new Promise((r) => setTimeout(r, ms));
let fallas = 0;
function ok(cond, texto) {
  console.log(`${cond ? '  ✔' : '  ✘'} ${texto}`);
  if (!cond) fallas++;
}

// --- Chrome y CDP ---

const perfil = mkdtempSync(join(tmpdir(), 'verificar-gps-'));
const chrome = spawn(CHROME, [
  ...(VER ? [] : ['--headless=new']),
  '--remote-debugging-port=0', `--user-data-dir=${perfil}`,
  '--no-first-run', '--no-default-browser-check', 'about:blank',
], { stdio: ['ignore', 'ignore', 'pipe'] });

function cerrar() {
  chrome.kill();
  try { rmSync(perfil, { recursive: true, force: true }); } catch { /* el perfil puede seguir en uso un instante */ }
}

// Chrome imprime en stderr "DevTools listening on ws://127.0.0.1:PUERTO/devtools/browser/…".
const wsURL = await new Promise((resolve, reject) => {
  let salida = '';
  chrome.stderr.on('data', (d) => {
    salida += d;
    const m = /DevTools listening on (ws:\/\/\S+)/.exec(salida);
    if (m) resolve(m[1]);
  });
  chrome.on('exit', (code) => reject(new Error(`Chrome terminó (código ${code}) antes de abrir DevTools:\n${salida}`)));
});

const ws = new WebSocket(wsURL);
await new Promise((r, e) => { ws.onopen = r; ws.onerror = e; });
let proximoId = 1;
const pendientes = new Map();
const oyentes = new Set();
ws.onmessage = (m) => {
  const msg = JSON.parse(m.data);
  if (msg.id && pendientes.has(msg.id)) {
    const { resolve, reject } = pendientes.get(msg.id);
    pendientes.delete(msg.id);
    if (msg.error) reject(new Error(`${msg.error.message} (${msg.error.code})`));
    else resolve(msg.result);
  } else if (msg.method) {
    for (const f of oyentes) f(msg);
  }
};

// cdp manda un comando. Con sessionId va a una pestaña; sin él, al navegador.
function cdp(method, params = {}, sessionId) {
  const id = proximoId++;
  ws.send(JSON.stringify({ id, method, params, sessionId }));
  return new Promise((resolve, reject) => pendientes.set(id, { resolve, reject }));
}

function proximoEvento(sessionId, method) {
  return new Promise((resolve) => {
    const f = (msg) => {
      if (msg.sessionId === sessionId && msg.method === method) {
        oyentes.delete(f);
        resolve(msg.params);
      }
    };
    oyentes.add(f);
  });
}

const pedidos = []; // todas las URL que pidió cualquier pestaña
oyentes.add((msg) => {
  if (msg.method === 'Network.requestWillBeSent') pedidos.push(msg.params.request.url);
});

// ventana abre una pestaña en un contexto nuevo (cookies y localStorage propios).
async function ventana() {
  const { browserContextId } = await cdp('Target.createBrowserContext');
  const { targetId } = await cdp('Target.createTarget', { url: 'about:blank', browserContextId });
  const { sessionId } = await cdp('Target.attachToTarget', { targetId, flatten: true });
  await Promise.all(['Page.enable', 'Runtime.enable', 'Network.enable'].map((m) => cdp(m, {}, sessionId)));
  oyentes.add((msg) => {
    if (msg.sessionId !== sessionId) return;
    if (msg.method === 'Runtime.exceptionThrown') console.log('    [error en la página]', msg.params.exceptionDetails.exception?.description || msg.params.exceptionDetails.text);
    if (msg.method === 'Runtime.consoleAPICalled' && ['error', 'warning'].includes(msg.params.type)) {
      console.log(`    [consola ${msg.params.type}]`, msg.params.args.map((a) => a.value ?? a.description).join(' '));
    }
  });
  return { sessionId, browserContextId };
}

async function evaluar(s, expression) {
  const r = await cdp('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true }, s);
  if (r.exceptionDetails) throw new Error(`${expression}: ${r.exceptionDetails.exception?.description || r.exceptionDetails.text}`);
  return r.result.value;
}

async function navegar(s, url) {
  const cargada = proximoEvento(s, 'Page.loadEventFired');
  await cdp('Page.navigate', { url }, s);
  await cargada;
}

async function esperar(s, expression, ms, descripcion) {
  const hasta = Date.now() + ms;
  while (Date.now() < hasta) {
    if (await evaluar(s, expression).catch(() => false)) return true;
    await pausa(250);
  }
  console.log(`    (no se cumplió en ${ms / 1000} s: ${descripcion})`);
  return false;
}

async function entrar(s, usuario, clave) {
  await navegar(s, `${BASE}/`);
  const estado = await evaluar(s, `fetch('/api/login', {method: 'POST', headers: {'Content-Type': 'application/json'},
    body: JSON.stringify(${JSON.stringify({ username: usuario, password: clave })})}).then((r) => r.status)`);
  if (estado !== 200) throw new Error(`no se pudo entrar como ${usuario}: el servidor respondió ${estado}`);
}

const texto = (id) => `document.getElementById(${JSON.stringify(id)}).textContent`;
const ubicar = (s, lat, lng, accuracy = 8) => cdp('Emulation.setGeolocationOverride', { latitude: lat, longitude: lng, accuracy }, s);

let entrada = null;
function enter(mensaje) {
  entrada ??= createInterface({ input: process.stdin });
  console.log(`\n>>> ${mensaje} y apretá Enter`);
  return new Promise((r) => entrada.once('line', r));
}

// --- Pruebas ---

try {
  const cadete = await ventana();
  const admin = await ventana();

  console.log(`Servidor: ${BASE}`);
  await entrar(cadete.sessionId, CADETE_USER, CADETE_PASS);
  await entrar(admin.sessionId, ADMIN_USER, ADMIN_PASS);
  const idCadete = await evaluar(cadete.sessionId, `fetch('/api/sesion').then((r) => r.json()).then((u) => u.id)`);

  await cdp('Browser.grantPermissions', { origin: BASE, permissions: ['geolocation'], browserContextId: cadete.browserContextId });
  // Punto de partida cerca de Chascomús, corrido un poco cada corrida para no
  // repetir coordenadas.
  let lat = -35.5772 + (Date.now() % 1000) / 1e6;
  const lng = -58.0094;
  await ubicar(cadete.sessionId, lat, lng);

  await navegar(cadete.sessionId, `${BASE}/rastreo.html`);
  await navegar(admin.sessionId, `${BASE}/mapa.html`);
  await evaluar(admin.sessionId, 'window.__sinRecargar = true');
  const inicio = new Date();

  console.log('\n1. El marcador sigue al cadete en vivo');
  await evaluar(cadete.sessionId, `document.getElementById('empezar').click()`);
  ok(await esperar(cadete.sessionId, `${texto('estado')} === 'Rastreando'`, 5000, 'estado rastreando'), 'el cadete queda "Rastreando"');
  ok(await esperar(admin.sessionId, `[...document.querySelectorAll('#lista-cadetes button')].some((b) => b.textContent.length > 0)`, 15000, 'cadete en la lista'),
    'el cadete aparece en el panel del admin (primer lote, ≤ 10 s)');
  // Se elige en el panel el cadete con el nombre de la sesión.
  const nombre = await evaluar(cadete.sessionId, `fetch('/api/sesion').then((r) => r.json()).then((u) => u.name)`);
  await evaluar(admin.sessionId, `[...document.querySelectorAll('#lista-cadetes button')].find((b) => b.textContent.includes(${JSON.stringify(nombre)})).click()`);
  for (let i = 1; i <= 3; i++) {
    lat += 0.001;
    await ubicar(cadete.sessionId, lat, lng);
    const esperado = `${lat.toFixed(5)}, ${lng.toFixed(5)}`;
    ok(await esperar(admin.sessionId, `${texto('d-coordenadas')} === ${JSON.stringify(esperado)}`, 15000, `coordenadas ${esperado}`),
      `cambio ${i}: el admin ve ${esperado}`);
  }
  const enLinea = await evaluar(admin.sessionId, `parseInt(${texto('d-recorrido')})`);
  ok(enLinea >= 4, `la línea del recorrido tiene ${enLinea} puntos (≥ 4)`);
  ok(await evaluar(admin.sessionId, 'window.__sinRecargar === true'), 'el mapa no se recargó');

  console.log(SERVIDOR_APAGADO ? '\n2. Servidor apagado: las posiciones esperan en la cola' : '\n2. Sin conexión: las posiciones esperan en la cola');
  if (SERVIDOR_APAGADO) {
    await enter('Apagá el servidor');
  } else {
    await cdp('Network.emulateNetworkConditions', { offline: true, latency: 0, downloadThroughput: -1, uploadThroughput: -1 }, cadete.sessionId);
  }
  const capturas = [];
  for (let i = 0; i < 3; i++) {
    lat += 0.001;
    await ubicar(cadete.sessionId, lat, lng);
    capturas.push(Date.now());
    await pausa(1500);
  }
  ok(await esperar(cadete.sessionId, `parseInt(${texto('pendientes')}) >= 3`, 3000, 'pendientes ≥ 3'), 'quedan al menos 3 pendientes en la cola');
  ok(await esperar(cadete.sessionId, `${texto('error-envio')}.length > 0`, 12000, 'mensaje de error de envío'),
    `se muestra el error de envío: "${await evaluar(cadete.sessionId, texto('error-envio'))}"`);
  const guardadas = await evaluar(cadete.sessionId, `JSON.parse(localStorage.getItem('cadetes.rastreo.cola.v1.${idCadete}')).posiciones.length`);
  ok(guardadas >= 3, `la cola está guardada en localStorage (${guardadas} posiciones)`);
  if (SERVIDOR_APAGADO) {
    ok(await esperar(admin.sessionId, `!document.getElementById('conexion').hidden`, 10000, 'indicador reconectando'), 'el mapa muestra "Reconectando…"');
    await enter('Prendé el servidor');
  } else {
    await pausa(3000); // que pase al menos un ciclo más sin red
    await cdp('Network.emulateNetworkConditions', { offline: false, latency: 0, downloadThroughput: -1, uploadThroughput: -1 }, cadete.sessionId);
  }
  const vuelta = Date.now();
  ok(await esperar(cadete.sessionId, `parseInt(${texto('pendientes')}) === 0`, 20000, 'pendientes 0'), 'al volver se envían solas (pendientes: 0)');
  const recorrido = await evaluar(admin.sessionId, `fetch('/api/posiciones/recorrido?cadete=${idCadete}&desde=' + encodeURIComponent(${JSON.stringify(new Date(capturas[0] - 500).toISOString())})).then((r) => r.json())`);
  const offline = recorrido.filter((p) => Date.parse(p.capturada_en) < vuelta);
  ok(offline.length >= 3, `el servidor tiene ${offline.length} posiciones capturadas durante el corte`);
  ok(offline.every((p) => Date.parse(p.recibida_en) - Date.parse(p.capturada_en) > 2000),
    `conservan la hora original (demoras: ${offline.map((p) => ((Date.parse(p.recibida_en) - Date.parse(p.capturada_en)) / 1000).toFixed(1)).join(', ')} s)`);
  const esperado = `${lat.toFixed(5)}, ${lng.toFixed(5)}`;
  ok(await esperar(admin.sessionId, `${texto('d-coordenadas')} === ${JSON.stringify(esperado)}`, 15000, 'el mapa se pone al día'),
    `el mapa se pone al día sin recargar (${esperado})`);
  ok(await evaluar(admin.sessionId, 'window.__sinRecargar === true'), 'el mapa no se recargó');

  console.log('\n3. Recargar mientras rastrea');
  await navegar(cadete.sessionId, `${BASE}/rastreo.html`);
  ok(await esperar(cadete.sessionId, `${texto('estado')} === 'Rastreando' && ${texto('reanudaciones')} === '1'`, 5000, 'reanudación'),
    'retoma solo y cuenta 1 reanudación');

  console.log('\n4. El resumen del panel coincide con la API');
  await pausa(11000); // que se envíe lo último
  // El formulario usa datetime-local, que va al segundo: se redondea igual.
  const alSegundo = (ms) => new Date(Math.floor(ms / 1000) * 1000);
  const desde = alSegundo(inicio.getTime() - 60_000);
  const hasta = alSegundo(Date.now() + 60_000);
  const local = (d) => new Date(d.getTime() - d.getTimezoneOffset() * 60_000).toISOString().slice(0, 19);
  await evaluar(admin.sessionId, `(() => { const f = document.getElementById('form-resumen');
    f.desde.value = ${JSON.stringify(local(desde))}; f.hasta.value = ${JSON.stringify(local(hasta))}; f.requestSubmit(); })()`);
  ok(await esperar(admin.sessionId, `!document.getElementById('resumen').hidden`, 5000, 'resumen visible'), 'el panel muestra el resumen');
  const panel = await evaluar(admin.sessionId, `({ posiciones: ${texto('r-posiciones')}, noVisibles: ${texto('r-no-visibles')}, huecos: ${texto('r-huecos')}, url: document.getElementById('r-json-link').href })`);
  const api = await evaluar(admin.sessionId, `fetch('/api/posiciones/resumen?cadete=${idCadete}&desde=${encodeURIComponent(desde.toISOString())}&hasta=${encodeURIComponent(hasta.toISOString())}').then((r) => r.json())`);
  ok(new URL(panel.url).search === `?cadete=${idCadete}&desde=${encodeURIComponent(desde.toISOString())}&hasta=${encodeURIComponent(hasta.toISOString())}`,
    'el panel consulta el mismo rango');
  ok(String(api.posiciones) === panel.posiciones, `posiciones: panel ${panel.posiciones}, API ${api.posiciones}`);
  ok(String(api.no_visibles) === panel.noVisibles, `no visibles: panel ${panel.noVisibles}, API ${api.no_visibles}`);
  ok(panel.huecos.startsWith(`${api.huecos_60s.cantidad} /`), `huecos: panel "${panel.huecos}", API ${api.huecos_60s.cantidad}`);

  await evaluar(cadete.sessionId, `document.getElementById('detener').click()`);

  console.log('\n5. Nada desde un CDN');
  const hosts = new Set(pedidos.filter((u) => /^https?:/.test(u)).map((u) => new URL(u).host));
  const ajenos = [...hosts].filter((h) => h !== new URL(BASE).host && h !== 'tile.openstreetmap.org');
  ok(ajenos.length === 0, `hosts pedidos: ${[...hosts].join(', ')}`);
  ok(pedidos.some((u) => u === `${BASE}/vendor/leaflet/leaflet.js`) && pedidos.some((u) => u === `${BASE}/vendor/leaflet/leaflet.css`),
    'leaflet.js y leaflet.css salen del servidor');
} catch (err) {
  console.error('\nLa verificación se cortó:', err.message);
  fallas++;
} finally {
  entrada?.close();
  ws.close();
  cerrar();
}

console.log(fallas === 0 ? '\nTodo bien.' : `\n${fallas} comprobaciones fallaron.`);
process.exit(fallas === 0 ? 0 : 1);
