<script lang="ts">
	import type { Snippet } from 'svelte';
	import { page } from '$app/state';
	import Anchor from '@lucide/svelte/icons/anchor';
	import Bell from '@lucide/svelte/icons/bell';
	import LogOut from '@lucide/svelte/icons/log-out';
	import UsersRound from '@lucide/svelte/icons/users-round';
	import { auth } from '#lib/stores/auth.svelte.js';
	import { sse } from '#lib/stores/sse.svelte.js';
	import TokensManageModal from '#lib/components/TokensManageModal.svelte';
	import UsersModal from '#lib/components/UsersModal.svelte';
	import NotificationsModal from '#lib/components/NotificationsModal.svelte';

	/*
	 * Shell de la app (TASK-006, refactor top bar): UNA SOLA barra sticky -”
	 * se elimina el sidebar y /admin como pagina dedicada (redirect al
	 * tablero; administracion = modales aqui):
	 * - Izquierda: marca RLS LEGACY (-> /) + nav inline Tablero · Alertas.
	 * - Derecha: pills de estado (SSE/alarmas/usuario), accesos admin
	 *   con iconos Lucide (tokens, usuarios, notificaciones), atados a SUS
	 *   MODALES al alcance en TODA la app) + Cerrar sesion.
	 * - Responsive: flex-wrap -> colapsa a una 2ª fila en mobile (sin menu).
	 * - VIEW TRANSITIONS OFF: sin onNavigate/startViewTransition.
	 * - Starfield queda en el layout global (debajo, z-10 por encima).
	 */
	let { children }: { children?: Snippet } = $props();

	const NAV = [
		{ href: '/', label: 'Tablero' },
		{ href: '/vps', label: 'VPS' },
		{ href: '/alerts', label: 'Alertas' }
	];

	const isActive = (href: string): boolean =>
		href === '/' ? page.url.pathname === '/' : page.url.pathname.startsWith(href);

	const isAdmin = $derived(auth.user?.role === 'admin');

	// Modales de administracion (los mismos que Parte A del dashboard),
	// ahora montados aqui para estar al alcance en cualquier pagina.
	let tokensManageOpen = $state(false);
	let usersOpen = $state(false);
	let notifOpen = $state(false);

	const statusLabel = $derived(
		sse.status === 'live'
			? 'EN VIVO'
			: sse.status === 'connecting'
				? 'CONECTANDO...'
				: 'RECONECTANDO'
	);

	function statusColor(status: string): string {
		switch (status) {
			case 'live':
				return 'bg-secondary';
			case 'connecting':
				return 'bg-muted-foreground animate-pulse';
			default:
				return 'bg-destructive';
		}
	}

	async function onLogout(): Promise<void> {
		sse.disconnect();
		await auth.logout();
	}
</script>

<div class="relative z-10 flex min-h-svh flex-col">
	<!-- Topbar sticky unica: marca + nav inline | pills + accesos admin + salir.
	     flex-wrap: en <lg los accesos pasan a una segunda fila (sin menu). -->
	<header
		class="app-header sticky top-0 z-20 flex min-h-14 flex-wrap items-center justify-between gap-y-1
			border-b border-border/70 bg-background/80 px-4 py-1 backdrop-blur-sm sm:px-6"
	>
		<!-- Izquierda: marca + nav -->
		<div class="flex flex-wrap items-center gap-x-4 gap-y-1">
			<a
				href="/"
				class="brand-lockup transition-opacity hover:opacity-80"
			>
				<span class="brand-emblem" aria-hidden="true">
					<svg viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg">
						<circle cx="12" cy="12" r="8.75" stroke="currentColor" stroke-opacity=".65" />
						<path d="M12 3.25 14.4 10l6.35 2-6.35 2L12 20.75 9.6 14l-6.35-2 6.35-2L12 3.25Z" stroke="currentColor" stroke-linejoin="round" />
						<circle cx="12" cy="12" r="1.4" fill="currentColor" />
					</svg>
				</span>
				<span class="brand-wordmark">
					<span class="brand-title font-display font-semibold">RLS LEGACY</span>
				</span>
			</a>

			<nav class="flex items-center gap-1" aria-label="Principal">
				{#each NAV as item (item.href)}
					<a href={item.href} aria-current={isActive(item.href) ? 'page' : undefined} class="nav-item">
						{item.label}
					</a>
				{/each}
			</nav>
		</div>

		<!-- Derecha: estado + accesos de gestion + salir.
		     Anti-CLS (fix UX refresh): pill de usuario y 3 accesos admin SIEMPRE
		     existen con su tamano final desde el primer render; mientras `me()`
		     no resuelve van invisibles (caja reservada, sin saltos). Cuando el
		     resolve dice VIEWER, los botones colapsan (hidden); no hay desliz -->
		<div class="flex flex-wrap items-center justify-end gap-2">
			<!-- pills con ancho ESTABLE (anti-CLS): CONECTANDO.../EN VIVO y el
			     placeholder del usuario miden distinto; min-w fija el ancho -->
			<span class="pill hidden min-w-[126px] justify-center sm:inline-flex">
				<span class="h-1.5 w-1.5 rounded-full {statusColor(sse.status)}" aria-hidden="true"></span>
				{statusLabel}
			</span>
			<span class="pill hidden md:inline-flex">
				{sse.activeAlerts > 0 ? `${sse.activeAlerts} alerta(s)` : 'Sin alertas'}
			</span>
			<span
				class="pill hidden max-w-full truncate lg:inline-flex lg:min-w-[132px] lg:justify-center"
				class:invisible={!auth.user}
			>
				<span class="truncate">{auth.user?.username ?? '········'} · {auth.user?.role ?? '······'}</span>
			</span>
			<!-- Accesos admin: ghost dorado (tema); slot SIEMPRE presente
			     (anti-CLS): mientras me() no resuelve o el rol no es admin, el
			     boton existe invisible con su ancho final; viewer: hidden -->
			<button
				type="button"
				title="Tokens de VPS (crear / copiar / revocar)"
				tabindex={isAdmin ? 0 : -1}
				class="glow-gold inline-flex items-center gap-1.5 rounded-lg border border-transparent bg-transparent px-3 py-1.5 text-xs font-medium
					text-foreground/80 transition-[background-color,border-color,color,box-shadow] hover:border-primary/50 hover:bg-primary/12
					focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
				class:invisible={!auth.user} class:hidden={auth.user && auth.user.role !== 'admin'}
				onclick={() => isAdmin && (tokensManageOpen = true)}
			>
				<Anchor size={14} strokeWidth={1.8} class="text-secondary" aria-hidden="true" />
				Tokens
			</button>
			<button
				type="button"
				title="Administrar usuarios"
				tabindex={isAdmin ? 0 : -1}
				class="glow-gold inline-flex items-center gap-1.5 rounded-lg border border-transparent bg-transparent px-3 py-1.5 text-xs font-medium
					text-foreground/80 transition-[background-color,border-color,color,box-shadow] hover:border-primary/50 hover:bg-primary/12
					focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
				class:invisible={!auth.user} class:hidden={auth.user && auth.user.role !== 'admin'}
				onclick={() => isAdmin && (usersOpen = true)}
			>
				<UsersRound size={14} strokeWidth={1.8} class="text-secondary" aria-hidden="true" />
				Usuarios
			</button>
			<button
				type="button"
				title="Canales de notificacion (Discord / Telegram)"
				tabindex={isAdmin ? 0 : -1}
				class="glow-gold inline-flex items-center gap-1.5 rounded-lg border border-transparent bg-transparent px-3 py-1.5 text-xs font-medium
					text-foreground/80 transition-[background-color,border-color,color,box-shadow] hover:border-primary/50 hover:bg-primary/12
					focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
				class:invisible={!auth.user} class:hidden={auth.user && auth.user.role !== 'admin'}
				onclick={() => isAdmin && (notifOpen = true)}
			>
				<Bell size={14} strokeWidth={1.8} class="text-primary" aria-hidden="true" />
				Notificaciones
			</button>
			<button
				type="button"
				class="inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
				onclick={() => void onLogout()}
			>
				<LogOut size={14} strokeWidth={1.8} aria-hidden="true" />
				Cerrar sesion
			</button>
		</div>
	</header>

	<main class="min-w-0 flex-1">
		<!-- page-shell: ancho comun (1400px) para TODAS las paginas -->
		<div class="page-shell">
			{@render children?.()}
		</div>
	</main>
</div>

<!-- Gestiones de administracion: desmontadas al cerrar -> recargan su data -->
{#if isAdmin && tokensManageOpen}
	<TokensManageModal bind:open={tokensManageOpen} />
{/if}
{#if isAdmin && usersOpen}
	<UsersModal bind:open={usersOpen} />
{/if}
{#if isAdmin && notifOpen}
	<NotificationsModal bind:open={notifOpen} />
{/if}
