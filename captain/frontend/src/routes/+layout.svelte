<script lang="ts">
	// Fuentes: SELF-HOSTED en static/fonts con @font-face (layout.css) +
	// <link rel="preload"> en app.html — NO @fontsource por bundler (CSS
	// hash-izado por Vite no permite preload determinista y el swap de
	// Orbitron causaba layout shift en la barra). Inter 400/500/600 +
	// Orbitron 600/700 (subset latin — los que usa la UI).

	import './layout.css';
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { Toaster } from 'svelte-sonner';
	import Starfield from '#lib/components/Starfield.svelte';
	import AppShell from '#lib/components/AppShell.svelte';
	import favicon from '#lib/assets/favicon.svg';
	import { auth } from '#lib/stores/auth.svelte.js';
	import { sse } from '#lib/stores/sse.svelte.js';
	import { page } from '$app/state';
	import type { LayoutProps } from './$types';

	/*
	 * Shell global (TASK-006):
	 * - Guard de rutas (REQ-024): sin /auth/me válida → /login (excepto login/bootstrap).
	 * - El stream SSE vive una sola vez en el layout y se cierra limpio en unload.
	 * - VIEW TRANSITIONS OFF: no se activan (regla "sin animaciones pesadas").
	 */

	let { children }: LayoutProps = $props();

	/**
	 * (Nota: $app/environment no expone d.ts en este kit v3; con ssr=false +
	 * prerender el equivalente exacto es verificar window en runtime de build.)
	 */
	const isBrowser = typeof window !== 'undefined';

	const isAuthRoute = $derived(
		page.url.pathname === '/login' || page.url.pathname === '/bootstrap'
	);

	// The layout survives SPA navigation from /login. Keep the fleet stream
	// coupled to the reactive session so a successful login starts SSE without
	// requiring a full browser reload; clearing the session closes it again.
	$effect(() => {
		if (!isBrowser) return;
		if (auth.user) sse.connect();
		else sse.disconnect();
	});

	onMount(() => {
		void (async () => {
			const user = await auth.me();
			if (!user && page.url.pathname !== '/login' && page.url.pathname !== '/bootstrap') {
				goto('/login');
			}
		})();
	});

	// Salida limpia del stream en navigation/unload de página (close nativo).
	function exitCleanly(): void {
		sse.disconnect();
	}
</script>

<svelte:head>
	<link rel="icon" href={favicon} />
	<meta name="theme-color" content="hsl(222 38% 5%)" />
</svelte:head>

<!-- Campo de estrellas fijo al fondo; visualización siempre "viewport" -->
<Starfield />
<Toaster
	theme="dark"
	position="top-center"
	closeButton
	pauseWhenPageIsHidden
	toastOptions={{ class: 'astral-toast', descriptionClass: 'astral-toast-description' }}
/>

<svelte:window
	onpagehide={exitCleanly}
	onbeforeunload={exitCleanly}
/>

{#if isBrowser && !isAuthRoute}
	<!-- Shell solo en rutas de la app; sin SSR (ssr=false en +layout.ts). -->
	<AppShell>
		{@render children()}
	</AppShell>
{:else}
	<!-- Sitio en 100% de altura y sin márgenes; contenido por ENCIMA del starfield -->
	<div class="relative z-10 flex min-h-svh flex-col">
		{@render children()}
	</div>
{/if}
