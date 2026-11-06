<script lang="ts">
	import Copy from '@lucide/svelte/icons/copy';
	import { toast } from 'svelte-sonner';
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

	function dashboardOriginForHostInstaller(): string {
		if (typeof window === 'undefined') return 'https://tu-dashboard.example';
		return new URL(window.location.origin).origin;
	}

	const bridgeInstallCommand = $derived(
		`curl -fsSL '${dashboardOriginForHostInstaller()}/script.sh' | sudo sh`
	);

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
					<Button variant="action" size="sm" class="shrink-0" onclick={() => void copyText(bridgeInstallCommand, 'Comando de instalación copiado')}>
						<Copy size={14} strokeWidth={1.8} aria-hidden="true" />
						Copiar comando
					</Button>
				</div>
				<pre class="compose-code" aria-label="Comando para instalar Stowaway">{bridgeInstallCommand}</pre>
				<p class="compose-note">
					El instalador pedirá este token sin mostrarlo y confirmará el acceso al Docker Engine local. El servicio se habilita al inicio; para pausarlo usa
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
