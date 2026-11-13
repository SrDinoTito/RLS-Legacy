<script lang="ts">
	import { onMount } from 'svelte';
	import Copy from '@lucide/svelte/icons/copy';
	import { toast } from 'svelte-sonner';
	import { getPublicConfig } from '#lib/api/client.js';
	import { Button } from '#lib/components/ui/button/index.js';

	/*
	 * Instrucciones de conexión: nuevas credenciales instalan Stowaway una vez
	 * en la VPS; credenciales antiguas reciben únicamente guía de migración.
	 */
	let {
		token
	}: {
		token: { name: string; token: string; tags?: string[] | null; enrollment?: boolean; vps_scoped?: boolean } | null;
	} = $props();

	let dashboardURL = $state('');
	let loadingConfig = $state(false);
	let configError = $state(false);

	const bridgeInstallCommand = $derived(dashboardURL
		? `(
set -euo pipefail
read -r -s -p 'Token de esta VPS: ' RLS_TOKEN
printf '\\n'
[ -n "$RLS_TOKEN" ] || { printf 'El token no puede estar vacío.\\n' >&2; exit 1; }
AUTH_CONFIG=$(mktemp)
chmod 600 "$AUTH_CONFIG"
cleanup() { rm -f "$AUTH_CONFIG"; }
trap cleanup EXIT HUP INT TERM
printf 'header = "Authorization: Bearer %s"\\n' "$RLS_TOKEN" > "$AUTH_CONFIG"
curl -fsSL --config "$AUTH_CONFIG" '${dashboardURL}/script.sh' | sudo sh
unset RLS_TOKEN
)`
		: '');

	onMount(() => {
		if (token?.vps_scoped) void loadPublicConfig();
	});

	async function loadPublicConfig(): Promise<void> {
		loadingConfig = true;
		try {
			const result = await getPublicConfig();
			if (!result.ok || !result.data?.dashboard_url) {
				configError = true;
				toast.error('No se pudo cargar la URL pública del dashboard', {
					description: 'Configura RLS_PUBLIC_URL y reinicia Captain.'
				});
				return;
			}
			dashboardURL = result.data.dashboard_url.replace(/\/+$/, '');
		} catch {
			configError = true;
			toast.error('No se pudo conectar con el backend para leer RLS_PUBLIC_URL');
		} finally {
			loadingConfig = false;
		}
	}

	async function copyText(value: string, successMessage: string): Promise<void> {
		try {
			await navigator.clipboard.writeText(value);
			toast.success(successMessage);
			return;
		} catch {
			// Fallback para navegadores sin Clipboard API o contextos no seguros.
		}

		const textarea = document.createElement('textarea');
		textarea.value = value;
		textarea.style.position = 'fixed';
		textarea.style.opacity = '0';
		document.body.appendChild(textarea);
		textarea.select();
		try {
			if (!document.execCommand('copy')) throw new Error('copy command rejected');
			toast.success(successMessage);
		} catch {
			toast.error('No se pudo copiar — selecciona el contenido manualmente');
		} finally {
			textarea.remove();
		}
	}
</script>

{#if token}
	<div class="grid gap-4">
		<section class="compose-credential" aria-label="Token de VPS">
			<div class="min-w-0">
				<p class="compose-eyebrow">Credencial {token.vps_scoped ? 'de VPS' : 'legacy'}</p>
				<code class="compose-token">{token.token}</code>
			</div>
			<Button variant="action" size="sm" class="shrink-0" onclick={() => void copyText(token.token, 'Token copiado al portapapeles')}>
				<Copy size={14} strokeWidth={1.8} aria-hidden="true" />
				Copiar token
			</Button>
		</section>

		{#if token.vps_scoped}
			<p class="compose-note compose-note--fleet">
				Un token corresponde a esta VPS externa y se vincula al ID de su Docker Engine en el primer reporte. Stowaway informa sus contenedores en ejecución sin modificar los Compose.
			</p>

			<section class="compose-preview" aria-label="Instalación de Stowaway en una VPS externa">
				<div class="compose-preview__bar">
					<div class="min-w-0">
						<p class="compose-eyebrow">VPS externa · Linux / systemd</p>
						<code>RLS Stowaway · un servicio por VPS</code>
					</div>
					<Button variant="action" size="sm" class="shrink-0" disabled={!bridgeInstallCommand} onclick={() => void copyText(bridgeInstallCommand, 'Comando de instalación copiado')}>
						<Copy size={14} strokeWidth={1.8} aria-hidden="true" />
						Copiar comando
					</Button>
				</div>
				{#if bridgeInstallCommand}
					<pre class="compose-code" aria-label="Comando para instalar Stowaway">{bridgeInstallCommand}</pre>
				{:else if loadingConfig}
					<p class="compose-code">Leyendo RLS_PUBLIC_URL…</p>
				{:else if configError}
					<p class="compose-code">No se pudo leer la URL pública. Revisa RLS_PUBLIC_URL en Captain.</p>
				{/if}
				<p class="compose-note">
					El comando solicita el token de forma oculta y lo envía en el encabezado Authorization al endpoint
					{#if dashboardURL}<code>{dashboardURL}/script.sh</code>{:else}<code>/script.sh</code>{/if}.
					Ese mismo token autoriza la descarga del binario y queda configurado en el servicio. Confirmará el acceso al Docker Engine local. El servicio se habilita al inicio; para pausarlo usa
					<code>sudo systemctl disable --now rls-stowaway</code> y para reactivarlo
					<code>sudo systemctl enable --now rls-stowaway</code>.
				</p>
			</section>
		{:else}
			<p class="compose-note compose-note--fleet">
				Esta credencial fue creada antes del esquema <strong>un token por VPS</strong>. Se mantiene activa para no cortar instalaciones existentes, pero no la uses para conectar otra VPS. Crea una credencial nueva de VPS e instala Stowaway para migrar.
			</p>
		{/if}
	</div>
{/if}
