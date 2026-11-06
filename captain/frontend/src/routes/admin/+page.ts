// /admin ya no es página dedicada: la administración vive en los MODALES de
// la barra global (AppShell). Redirige al tablero (id de la barra: igual
// URL, "Administración" salió de la nav). Redirect idiomático de SvelteKit:
// durante prerender se sigue el redirect y se genera el destino.
import { redirect } from '@sveltejs/kit';

export function load(): never {
	redirect(307, '/');
}
