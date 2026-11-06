<script lang="ts">
	import { toast } from 'svelte-sonner';
	import { createToken, listAgents, listTokens } from '#lib/api/client.js';
	import type { TokenCreated } from '#lib/api/types.js';
	import TagInput from '#lib/components/TagInput.svelte';
	import VPSConnectInstructions from '#lib/components/VPSConnectInstructions.svelte';
	import { Dialog } from '#lib/components/ui/dialog/index.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';

	/*
	 * Creador de tokens como modal, "más al alcance" (refactor dashboard):
	 * paso 1 = nombre de VPS + tags → POST /tokens (único modo VPS-scoped);
	 * paso 2 = credencial + instalador Stowaway.
	 * onCreated avisa al consumidor (Admin refresca la lista).
	 */
	let {
		open = $bindable(false),
		onCreated
	}: {
		open?: boolean;
		onCreated?: (t: TokenCreated) => void;
	} = $props();

	let name = $state('');
	let tkTags = $state<string[]>([]);
	let tkTagInput = $state('');
	/** Autocomplete: suggestions al abrir (unión de tags de tokens + agents). */
	let suggestions = $state<string[]>([]);
	/** Estado de envío del único flujo de alta por VPS. */
	let creating = $state(false);
	let created = $state<TokenCreated | null>(null);
	let installOpen = $state(false);

	// cada apertura arranca un flujo nuevo (form limpio, paso 1) + suggestions
	$effect(() => {
		if (!open) return;
		created = null;
		name = '';
		tkTags = [];
		tkTagInput = '';
		installOpen = false;
		void loadSuggestions();
	});

	/** Union de tags existentes (tokens + agents para el autocomplete). */
	async function loadSuggestions(): Promise<void> {
		try {
			const [toks, ags] = await Promise.all([listTokens(), listAgents()]);
			const set = new Set<string>();
			for (const t of toks.data ?? []) {
				for (const tg of t.tags ?? []) set.add(tg);
			}
			for (const a of ags.data ?? []) {
				for (const tg of a.tags ?? []) set.add(tg);
			}
			suggestions = [...set].sort();
		} catch {
			suggestions = []; // silencioso: el modal sirve igual sin autocomplete
		}
	}

	async function submit(e: SubmitEvent): Promise<void> {
		e.preventDefault();
		if (creating) return;
		creating = true;
		try {
			const res = await createToken(name.trim(), tkTags);
			if (res.ok && res.data) {
				created = res.data; // paso 2: token plaintext dentro del MISMO modal
				onCreated?.(res.data);
			} else {
				toast.error(`No se pudo crear el token (${res.status})`, {
					description: res.error ?? undefined
				});
			}
		} catch {
			toast.error('Backend no disponible');
		} finally {
			creating = false;
		}
	}

	async function copyToken(): Promise<void> {
		if (!created) return;
		try {
			await navigator.clipboard.writeText(created.token);
			toast.success('Token copiado al portapapeles');
		} catch {
			const ta = document.createElement('textarea');
			ta.value = created.token;
			ta.style.position = 'fixed';
			ta.style.opacity = '0';
			document.body.appendChild(ta);
			ta.select();
			try {
				document.execCommand('copy');
				toast.success('Token copiado al portapapeles');
			} catch {
				toast.error('No se pudo copiar — selecciónalo manualmente');
			} finally {
				ta.remove();
			}
		}
	}
</script>

<Dialog bind:open title={created ? `VPS conectada — ${created.name}` : 'Conectar una VPS'}>
	{#if open && !created}
		<!-- PASO 1: form de creación -->
		<form class="grid gap-4" onsubmit={submit}>
			<div class="grid gap-2">
				<Label for="tcm-name">Nombre de la VPS</Label>
				<Input id="tcm-name" bind:value={name} placeholder="ej. produccion-europa" required autocomplete="off" />
			</div>
			<div class="grid gap-2">
				<Label for="tcm-tags">Tags (opcional, máx 5)</Label>
				<TagInput id="tcm-tags" bind:tags={tkTags} bind:input={tkTagInput} placeholder="ej. lab + Enter" {suggestions} />
			</div>
			<p class="rounded-lg border border-secondary/25 bg-secondary/5 p-3 text-xs text-muted-foreground">
				Se crea un token exclusivo para esta VPS externa. Instala <code>rls-stowaway</code> como servicio del host: consultará su Docker Engine y reportará sus contenedores en ejecución, sin cambios en los Compose ni agentes dentro de cada contenedor.
			</p>
			<div class="flex justify-end gap-2">
				<Button variant="action" type="button" onclick={() => (open = false)}>Cancelar</Button>
				<Button type="submit" disabled={creating}>
					{creating ? 'Creando…' : 'Crear token'}
				</Button>
			</div>
		</form>
	{:else if open && created}
		<!-- PASO 2: token copiable e instrucciones de conexión de VPS -->
		<div class="grid gap-4">
			<p class="text-sm text-muted-foreground">
				Credencial creada. Ejecuta el instalador en el host de esa VPS externa; encontrarás el token y el comando también en la sección VPS.
			</p>
			<code
				class="block break-all rounded-lg border border-primary/40 bg-[#0d0d12] p-4 font-mono text-sm leading-relaxed text-secondary"
			>
				{created.token}
			</code>
			<div class="flex flex-wrap items-center justify-end gap-2">
				<Button variant="action" onclick={() => void copyToken()}>Copiar</Button>
				<Button variant="action" onclick={() => (installOpen = true)}>
					Instalar Stowaway
				</Button>
				<Button onclick={() => (open = false)}>Listo</Button>
			</div>
		</div>
	{/if}
</Dialog>

<!-- Instalador del token recién creado (dialog anidado) -->
{#if created}
	<Dialog
		bind:open={installOpen}
		title={`Instalar Stowaway — ${created.name}`}
		contentClass="w-[min(94vw,780px)]"
	>
		{#if installOpen}
			<VPSConnectInstructions token={created} />
		{/if}
	</Dialog>
{/if}
