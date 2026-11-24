// Service worker: solo push. No cachea nada (la app necesita red igual).

self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (event) => event.waitUntil(self.clients.claim()));

// Cada push tiene que mostrar una notificación: Safari da de baja la
// suscripción si llega un push y no se muestra nada.
self.addEventListener('push', (event) => {
  let datos = {};
  try {
    datos = event.data ? event.data.json() : {};
  } catch {
    datos = { body: event.data ? event.data.text() : '' };
  }
  event.waitUntil(self.registration.showNotification(datos.title || 'Cadetes', {
    body: datos.body || '',
    tag: datos.tag,
    data: { url: datos.url || '/' },
    icon: '/icon-192.png',
    badge: '/badge-96.png',
    lang: 'es-AR',
  }));
});

// Al tocarla: si la app ya está abierta, la lleva al pedido y la trae al
// frente; si no, la abre en el pedido.
self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  const url = new URL((event.notification.data && event.notification.data.url) || '/', self.location.origin).href;
  event.waitUntil((async () => {
    const ventanas = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
    const igual = ventanas.find((v) => v.url === url);
    if (igual) return igual.focus();
    for (const v of ventanas) {
      try {
        const navegada = await v.navigate(url);
        if (navegada) return navegada.focus();
      } catch {
        // Ventana no controlada por este service worker: se prueba la siguiente.
      }
    }
    return self.clients.openWindow(url);
  })());
});

// Si el navegador renueva la suscripción, se la vuelve a mandar al servidor.
// (Además la página la reenvía cada vez que se abre.)
self.addEventListener('pushsubscriptionchange', (event) => {
  event.waitUntil((async () => {
    let sub = event.newSubscription;
    if (!sub && event.oldSubscription) {
      sub = await self.registration.pushManager.subscribe(event.oldSubscription.options);
    }
    if (!sub) return;
    await fetch('/api/push/suscripciones', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(sub.toJSON()),
    });
  })());
});
