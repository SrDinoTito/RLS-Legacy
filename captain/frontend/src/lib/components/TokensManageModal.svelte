<script lang="ts">
	import { Dialog } from '#lib/components/ui/dialog/index.js';
	import TokensPanel from '#lib/components/TokensPanel.svelte';

	/*
	 * Modal de gestión de tokens (acceso desde el dashboard): envuelve el MISMO
	 * TokensPanel que antes era la tab Tokens de /admin (hoy redirect a /) —
	 * lista con badges, crear vía TokenCreateModal anidado, tags inline, compose y revoke con
	 * confirm. Native <dialog> (patrón AgentDetailModal); al cerrarse el
	 * consumidor lo desmonta y cada apertura recarga la lista.
	 */
	let { open = $bindable(false) }: { open?: boolean } = $props();
</script>

<Dialog bind:open title="Tokens de VPS" class="tokens-manage-dialog" contentClass="w-[min(96vw,1160px)]">
	{#if open}
		<TokensPanel />
	{/if}
</Dialog>
