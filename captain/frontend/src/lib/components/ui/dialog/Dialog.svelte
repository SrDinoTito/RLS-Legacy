<script lang="ts">
	import type { Snippet } from 'svelte';

	/*
	 * Superficie modal compartida sobre <dialog> nativo (TASK-006): showModal()
	 * conserva foco modal/Escape y el consumidor mantiene el estado del flujo.
	 */
	let {
		open = $bindable(false),
		title,
		class: className = '',
		/** Override del ancho/alto por defecto (modales grandes, p.ej. detalle de agent). */
		contentClass = '',
		footer,
		children
	}: {
		open?: boolean;
		title?: string;
		class?: string;
		contentClass?: string;
		footer?: Snippet;
		children?: Snippet;
	} = $props();

	let el: HTMLDialogElement | null = null;

	$effect(() => {
		if (!el) return;
		if (open && !el.open) el.showModal();
		else if (!open && el.open) el.close();
	});
</script>

<!-- Geometría y superficie compartidas en layout.css; cada flujo conserva su
     ancho y su propio contenido mediante contentClass. -->
<dialog
	bind:this={el}
	aria-label={title}
	class="astral-dialog border border-border p-0 text-card-foreground
		{className}"
	onclose={() => (open = false)}
>
	<div class="dialog-frame {contentClass || 'w-[min(92vw,480px)]'}">
		{#if title}
			<header class="dialog-header">
				<div class="dialog-heading">
					<div class="dialog-kicker" aria-hidden="true">
						<svg viewBox="0 0 20 20" fill="none">
							<circle cx="10" cy="10" r="7.2" stroke="currentColor" stroke-width="1.2" />
							<path d="m10 3 1.8 5.2L17 10l-5.2 1.8L10 17l-1.8-5.2L3 10l5.2-1.8L10 3Z" stroke="currentColor" stroke-width="1.1" stroke-linejoin="round" />
						</svg>
						<span>RLS LEGACY</span>
					</div>
					<h2 class="dialog-title">{title}</h2>
				</div>
				<button
					type="button"
					class="dialog-close"
					aria-label="Cerrar"
					title="Cerrar"
					onclick={() => (open = false)}
				>
					<svg viewBox="0 0 20 20" fill="none" aria-hidden="true">
						<path d="m5 5 10 10M15 5 5 15" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" />
					</svg>
				</button>
			</header>
		{/if}
		<div class="dialog-body">{@render children?.()}</div>
		{#if footer}
			<footer class="dialog-footer">{@render footer()}</footer>
		{/if}
	</div>
</dialog>
