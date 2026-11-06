<script lang="ts">
	import X from '@lucide/svelte/icons/x';
	import { toast } from 'svelte-sonner';

	/*
	 * Editor de tags con chips (TASK refactor dashboard): Enter/coma comiten
	 * el chip, backspace borra el último, blur comite lo pendiente.
	 * Pre-validación client-side igual que el backend (contrato v2):
	 * lowercase, a-z0-9-_ ≤24 chars, dedupe, máx 5.
	 * Estado vivo en el consumidor vía bind (tags lista + input texto).
	 *
	 * Autocomplete (feature): si recibe `suggestions` (tags ya existentes en
	 * la flota), input enfocado abre un dropdown filtrado (includes,
	 * case-insensitive, excluye los YA en la lista de chips). ↑↓ navegan,
	 * Enter agrega la resaltada (o crea el tipeado si no hay match), click
	 * agrega, Esc cierra sin perder el foco (no cierra el modal: blur +
	 * stopPropagation). Sin `suggestions` → sin dropdown, comportamiento
	 * original intacto (backward compatible).
	 */
	const TAG_RE = /^[a-z0-9-_]{1,24}$/;
	const MAX_TAGS = 5;
	const MAX_SUGGESTIONS = 8;

	let {
		tags = $bindable([]),
		input = $bindable(''),
		placeholder = 'tag + Enter',
		id,
		/** Clase del contenedor con borde (caja del form). */
		class: className =
			'flex min-h-10 flex-wrap items-center gap-1 rounded-lg border border-input bg-background px-2 py-1.5 focus-within:ring-2 focus-within:ring-ring',
		/** Clase del input interno (compacto para edición inline). */
		inputClass = 'h-6 min-w-24 flex-1 bg-transparent text-sm text-foreground outline-none',
		hint = true,
		/** Tags ya existentes (unión tokens+agents). Vacío/undefined = sin dropdown. */
		suggestions = [],
		/** id base para aria (listbox/opciones). */
		listId = 'tag-input-options'
	}: {
		tags?: string[];
		input?: string;
		placeholder?: string;
		id?: string;
		class?: string;
		inputClass?: string;
		/** Muestra el hint "a-z0-9-_ · hasta 24 caracteres" bajo la caja. */
		hint?: boolean;
		suggestions?: string[];
		listId?: string;
	} = $props();

	/** Dropdown abierto (solo si hay suggestions y foco/texto). */
	let listOpen = $state(false);
	/** Índice resaltado del dropdown (0 por defecto al abrir). */
	let activeIdx = $state(0);
	/** Contiene el foco (para mantener el dropdown con texto vacío). */
	let focused = $state(false);

	const hasSuggestions = $derived(Array.isArray(suggestions) && suggestions.length > 0);

	/** Filtradas: dedupe + case-insensitive + excluye ya-agregadas. */
	const filtered = $derived.by(() => {
		if (!listOpen || !focused || !hasSuggestions) return [];
		const q = input.trim().toLowerCase();
		const used = new Set(tags.map((t) => t.toLowerCase()));
		const seen = new Set<string>();
		const out: string[] = [];
		for (const raw of suggestions) {
			const s = String(raw).toLowerCase();
			if (used.has(s) || seen.has(s)) continue;
			seen.add(s);
			if (!q || s.includes(q)) {
				out.push(String(raw));
				if (out.length >= MAX_SUGGESTIONS) break;
			}
		}
		return out;
	});

	/** agrega un tag validado (reusa las reglas de commit). */
	function addTag(t: string): boolean {
		if (!t) return false;
		if (tags.some((x) => x === t)) return false;
		if (tags.length >= MAX_TAGS) {
			toast.error(`Máximo ${MAX_TAGS} tags`);
			return false;
		}
		if (!TAG_RE.test(t)) {
			toast.error(`Tag inválido: "${t}"`, {
				description: 'a-z0-9-_ sin espacios, hasta 24 caracteres.'
			});
			return false;
		}
		tags.push(t);
		return true;
	}

	function commit(): void {
		if (!input.trim()) return;
		for (const part of input.split(',')) {
			addTag(part.trim().toLowerCase());
		}
		input = '';
	}

	/** Acepta la sugerencia resaltada (Enter/click). */
	function acceptSuggestion(): void {
		if (!listOpen || activeIdx < 0 || activeIdx >= filtered.length) return;
		addTag(filtered[activeIdx].toLowerCase());
		input = '';
		listOpen = false;
		focused = true; // sigue el foco en el input tras seleccionar
	}

	function onKeydown(e: KeyboardEvent): void {
		if (listOpen && filtered.length > 0) {
			if (e.key === 'ArrowDown') {
				e.preventDefault();
				activeIdx = (activeIdx + 1) % filtered.length;
				return;
			}
			if (e.key === 'ArrowUp') {
				e.preventDefault();
				activeIdx = (activeIdx - 1 + filtered.length) % filtered.length;
				return;
			}
			if (e.key === 'Escape') {
				// cierra SOLO el dropdown; no deja que el Esc cierre el modal
				e.preventDefault();
				e.stopPropagation();
				listOpen = false;
				return;
			}
			if (e.key === 'Enter' || e.key === ',') {
				e.preventDefault();
				// hay match resaltado → sugerencia; hay texto sin match → crea el tipeado
				if (activeIdx >= 0 && activeIdx < filtered.length) acceptSuggestion();
				else commit();
				return;
			}
		} else if (hasSuggestions && e.key === 'ArrowDown' && !listOpen) {
			// ↑↓ también abre el dropdown desde el foco (keyboard-only friendly)
			listOpen = true;
			return;
		}
		if (e.key === 'Enter' || e.key === ',') {
			e.preventDefault();
			commit();
		} else if (e.key === 'Backspace' && input === '' && tags.length > 0) {
			tags.pop(); // UX de chips estándar: borrar el último con backspace
		}
	}

	function onFocus(): void {
		focused = true;
		if (hasSuggestions) listOpen = true;
	}

	/** blur: comite lo pendiente y cierra el dropdown (click fuera). */
	function onBlur(e: FocusEvent): void {
		commit();
		focused = false;
		listOpen = false;
	}

	function onInput(): void {
		// al tipear se recalcula el filtro; el resaltado vuelve a la 1ª opción
		activeIdx = 0;
		if (hasSuggestions) listOpen = true;
	}
</script>

<div class="grid gap-2">
	<div class="relative">
		<div class={className}>
			{#each tags as tg, i (tg)}
				<button
					type="button"
					class="inline-flex items-center gap-1 rounded-full border border-transparent bg-transparent px-2 py-0.5 text-[11px] text-secondary/80 transition-colors hover:border-secondary/45 hover:bg-secondary/10 hover:text-secondary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-secondary"
					title="Quitar tag"
					aria-label={`Quitar tag ${tg}`}
					onclick={() => tags.splice(i, 1)}
				>
					{tg}<X size={12} strokeWidth={2} aria-hidden="true" />
				</button>
			{/each}
			<input
				{id}
				bind:value={input}
				onkeydown={onKeydown}
				onblur={onBlur}
				onfocus={onFocus}
				oninput={onInput}
				{placeholder}
				autocomplete="off"
				spellcheck="false"
				role="combobox"
				aria-expanded={listOpen && filtered.length > 0}
				aria-controls={hasSuggestions ? listId : undefined}
				aria-autocomplete="list"
				class={inputClass}
			/>
		</div>
		{#if listOpen && filtered.length > 0}
			<!-- dropdown: anclado bajo la caja; z sobre el modal (native dialog) -->
			<ul
				id={listId}
				role="listbox"
				class="absolute left-0 right-0 top-full z-30 mt-1 max-h-44 overflow-y-auto rounded-lg border border-border
					bg-card p-1 shadow-lg"
			>
				{#each filtered as sg, i (sg)}
					<li role="none">
						<button
							type="button"
							role="option"
							aria-selected={i === activeIdx}
							class="w-full rounded-md px-2.5 py-1.5 text-left text-sm transition-colors
								{i === activeIdx
								? 'bg-primary/15 text-primary'
								: 'text-foreground/90 hover:bg-primary/10 hover:text-foreground'}"
							onmousedown={(e) => e.preventDefault()}
							onclick={() => {
								activeIdx = i;
								acceptSuggestion();
							}}
							onfocus={() => (activeIdx = i)}
						>
							{sg}
						</button>
					</li>
				{/each}
			</ul>
		{/if}
	</div>
	{#if hint}
		<p class="text-xs text-muted-foreground">a-z0-9-_ · hasta 24 caracteres</p>
	{/if}
</div>
