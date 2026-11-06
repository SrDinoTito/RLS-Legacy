<script lang="ts">
	import Pencil from '@lucide/svelte/icons/pencil';
	import Save from '@lucide/svelte/icons/save';
	import X from '@lucide/svelte/icons/x';
	import { toast } from 'svelte-sonner';
	import { putContainerTags } from '#lib/api/client.js';
	import type { Agent } from '#lib/api/types.js';
import { applyContainerTags } from '#lib/stores/sse.svelte.js';
	import TagInput from '#lib/components/TagInput.svelte';
	import { Button } from '#lib/components/ui/button/index.js';

	let { agent, suggestions = [] }: { agent: Agent; suggestions?: string[] } = $props();
	let editing = $state(false);
	let saving = $state(false);
	let tags = $state<string[]>([]);
	let input = $state('');

	$effect(() => {
		if (!editing) tags = [...(agent.container_tags ?? [])];
	});

	function cancel(): void {
		tags = [...(agent.container_tags ?? [])];
		input = '';
		editing = false;
	}

	async function save(): Promise<void> {
		if (saving) return;
		saving = true;
		try {
			const result = await putContainerTags(agent.id, tags);
			if (!result.ok || !result.data) {
				toast.error('No se pudieron guardar las etiquetas', {
					description: result.error ?? `Error HTTP ${result.status}`
				});
				return;
			}
			tags = [...result.data.container_tags];
			applyContainerTags(agent.id, result.data.container_tags);
			editing = false;
			input = '';
			toast.success('Etiquetas del contenedor actualizadas');
		} catch {
			toast.error('No se pudieron guardar las etiquetas', { description: 'Revisa la conexión con el dashboard e inténtalo de nuevo.' });
		} finally {
			saving = false;
		}
	}
</script>

<div class="container-tags-editor" aria-label={`Etiquetas manuales de ${agent.name}`}>
	{#if editing}
		<div class="grid gap-2">
			<div class="flex items-center justify-between gap-2">
				<p class="container-tags-kicker">Clasificación de este contenedor</p>
				<div class="flex items-center gap-1">
					<Button variant="action" size="sm" onclick={() => void save()} disabled={saving}>
						<Save size={14} aria-hidden="true" />{saving ? 'Guardando…' : 'Guardar'}
					</Button>
					<Button variant="action" size="sm" onclick={cancel} disabled={saving} aria-label="Cancelar edición">
						<X size={14} aria-hidden="true" />Cancelar
					</Button>
				</div>
			</div>
			<TagInput bind:tags bind:input {suggestions} hint={false} placeholder="Agregar etiqueta y Enter" listId={`container-tags-${agent.id}`} />
		</div>
	{:else}
		<div class="flex flex-wrap items-center justify-between gap-2">
			<div class="flex min-w-0 flex-wrap items-center gap-1.5">
				<span class="container-tags-kicker">Etiquetas del contenedor</span>
				{#if tags.length > 0}
					{#each tags as tag (tag)}
						<a href={`/tags/${encodeURIComponent(tag)}`} class="container-tag-chip" title={`Ver todos los contenedores con ${tag}`}>{tag}</a>
					{/each}
				{:else}
					<span class="text-xs text-muted-foreground">Sin clasificar</span>
				{/if}
			</div>
			<Button variant="action" size="sm" onclick={() => (editing = true)} aria-label={`Editar etiquetas de ${agent.name}`}>
				<Pencil size={13} aria-hidden="true" />Editar
			</Button>
		</div>
	{/if}
</div>
