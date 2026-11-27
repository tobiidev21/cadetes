// Chequeo de sesión y rol para las pantallas de prueba. Las rutas de la API
// ya controlan el rol; esto solo evita mostrar una pantalla que no va a andar.

const nombresRol = { admin: 'administradores', cadete: 'cadetes' };

// requerirSesion devuelve el usuario si tiene el rol pedido. Si no, muestra el
// motivo en el elemento aviso y devuelve null.
export async function requerirSesion(rol, aviso) {
  const mostrar = (texto) => {
    const enlace = document.createElement('a');
    enlace.href = '/';
    enlace.textContent = 'Ir al inicio';
    aviso.replaceChildren(`${texto} `, enlace);
    aviso.hidden = false;
  };
  let res;
  try {
    res = await fetch('/api/sesion');
  } catch {
    mostrar('No se pudo conectar con el servidor.');
    return null;
  }
  if (res.status === 401) {
    mostrar('Tenés que iniciar sesión.');
    return null;
  }
  if (!res.ok) {
    mostrar(`No se pudo leer la sesión (el servidor respondió ${res.status}).`);
    return null;
  }
  const usuario = await res.json();
  if (usuario.role !== rol) {
    mostrar(`Esta pantalla es solo para ${nombresRol[rol] || rol}.`);
    return null;
  }
  return usuario;
}

// Hora argentina, con segundos.
export const hora = new Intl.DateTimeFormat('es-AR', {
  timeStyle: 'medium', hourCycle: 'h23', timeZone: 'America/Argentina/Buenos_Aires',
});

// hace devuelve "hace 12 s", "hace 3 min" o "hace 2 h" desde la fecha.
export function hace(fecha, ahora = Date.now()) {
  const s = Math.max(0, Math.round((ahora - fecha.getTime()) / 1000));
  if (s < 60) return `hace ${s} s`;
  if (s < 3600) return `hace ${Math.floor(s / 60)} min`;
  return `hace ${Math.floor(s / 3600)} h`;
}
