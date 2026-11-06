// SPA estática (DSG-002): todo se renderiza en el cliente.
// prerender genera el shell de cada ruta conocida; fallback 'index.html' cubre el resto.
export const prerender = true;
export const ssr = false;
