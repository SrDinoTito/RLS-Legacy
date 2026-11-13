<script lang="ts">
	import ChevronLeft from '@lucide/svelte/icons/chevron-left';
	import ChevronRight from '@lucide/svelte/icons/chevron-right';
	import { Button } from '#lib/components/ui/button/index.js';

	let {
		page = $bindable(0),
		pageSize = 10,
		total = 0,
		itemLabel = 'elementos'
	}: {
		page?: number;
		pageSize?: number;
		total: number;
		itemLabel?: string;
	} = $props();

	const pageCount = $derived(Math.max(1, Math.ceil(total / pageSize)));
	const pageStart = $derived(total === 0 ? 0 : page * pageSize + 1);
	const pageEnd = $derived(Math.min((page + 1) * pageSize, total));

	$effect(() => {
		if (page >= pageCount) page = Math.max(0, pageCount - 1);
	});
</script>

<nav class="fleet-pagination" aria-label={`Paginación de ${itemLabel}`}>
	<p class="fleet-pagination-summary">
		Mostrando <strong>{pageStart}–{pageEnd}</strong> de <strong>{total}</strong> {itemLabel}
	</p>
	<div class="fleet-pagination-controls">
		<Button variant="action" size="sm" disabled={page === 0} aria-label="Página anterior" onclick={() => (page = Math.max(0, page - 1))}>
			<ChevronLeft size={15} aria-hidden="true" />
			Anterior
		</Button>
		<span class="fleet-pagination-position">Página <strong>{page + 1}</strong> de {pageCount}</span>
		<Button variant="action" size="sm" disabled={page >= pageCount - 1} aria-label="Página siguiente" onclick={() => (page = Math.min(pageCount - 1, page + 1))}>
			Siguiente
			<ChevronRight size={15} aria-hidden="true" />
		</Button>
	</div>
</nav>
