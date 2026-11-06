<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { Button } from '#lib/components/ui/button/index.js';
	import {
		Card,
		CardContent,
		CardDescription,
		CardHeader,
		CardTitle
	} from '#lib/components/ui/card/index.js';

	/*
	 * ErrorBoundary global (hardening /alerts 500): toda excepción client-side
	 * que SvelteKit capture entra por aquí en vez del fallback crudo
	 * "500: Internal Error". Mismo tema (card centrada sobre starfield del
	 * layout), sin dependencias nuevas.
	 */

	const status = $derived(page.status ?? 500);
	const message = $derived(page.error?.message ?? 'Error interno');

	/** Reintenta montando la app desde cero (limpia estado corrupto en memoria). */
	function retry(): void {
		if (typeof window !== 'undefined') window.location.reload();
		else void goto('/');
	}
</script>

<svelte:head>
	<title>{status} — RLS Legacy</title>
</svelte:head>

<div class="relative z-10 flex min-h-svh items-center justify-center p-4">
	<Card class="w-full max-w-md border-border/60 bg-card/90 shadow-xl">
		<CardHeader class="items-center text-center">
			<!-- Código de status en display font (tema dorado del dashboard) -->
			<span class="font-display text-5xl font-bold tracking-wider text-primary glow-gold">
				{status}
			</span>
			<CardTitle class="mt-2 text-lg">
				{status === 404 ? 'Página no encontrada' : 'Algo se rompió'}
			</CardTitle>
			<CardDescription class="max-w-xs wrap-anywhere">{message}</CardDescription>
		</CardHeader>
		<CardContent class="text-center text-sm text-muted-foreground">
			{#if status >= 500}
				El panel siguió funcionando por debajo; reintenta o vuelve al tablero.
			{:else}
				Revisa la URL o vuelve al tablero para seguir de punta a punta.
			{/if}
		</CardContent>
		<CardContent class="justify-center gap-3 flex">
			<Button variant="action" onclick={() => void goto('/')}>Volver al tablero</Button>
			<Button onclick={retry}>Reintentar</Button>
		</CardContent>
	</Card>
</div>
