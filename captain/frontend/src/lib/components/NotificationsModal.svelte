<script lang="ts">
	import { toast } from 'svelte-sonner';
	import MessageCircle from '@lucide/svelte/icons/message-circle';
	import Send from '@lucide/svelte/icons/send';
	import Check from '@lucide/svelte/icons/check';
	import {
		getNotifySettings,
		putNotifySettings,
		testNotify
	} from '#lib/api/client.js';
	import type { NotifySettings, NotifySettingsPatch } from '#lib/api/types.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { Badge } from '#lib/components/ui/badge/index.js';
	import { Dialog } from '#lib/components/ui/dialog/index.js';
	import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '#lib/components/ui/card/index.js';

	/*
	 * Modal de notificaciones (REQ-050): mismos dos bloques que la tab de
	 * /admin — form write-only (inputs vacíos + hints enmascarados, PUT parcial
	 * con confirm al vaciar un canal activo) y "Probar envío" por canal
	 * (POST /notify/test usa la config PERSISTIDA, no este form).
	 */

	let { open = $bindable(false) }: { open?: boolean } = $props();

	// Inputs write-only: nunca rellenamos el valor (el GET solo trae masks).
	let ndLoaded = $state<NotifySettings | null>(null);
	let ndWebhook = $state('');
	let ndToken = $state('');
	let ndChatID = $state('');
	let ndTouched = $state({ discord: false, token: false, chat: false });
	let savingNotif = $state(false);
	let ndConfirmOpen = $state(false);
	let ndConfirmChannel = $state('');
	let ndPendingPatch = $state<NotifySettingsPatch | null>(null);

	async function loadNotify(): Promise<void> {
		try {
			const res = await getNotifySettings();
			if (res.ok) ndLoaded = res.data;
			else toast.error(`No se pudo leer la configuración (${res.status})`);
		} catch {
			toast.error('Backend no disponible');
		}
	}

	/**
	 * Guardar SOLO los campos tocados (PUT parcial, contratos REQ-050):
	 * vaciar un campo que hoy está activo deshabilita el canal → confirm previo.
	 */
	async function submitNotif(e: SubmitEvent): Promise<void> {
		e.preventDefault();
		if (savingNotif) return;
		const patch: NotifySettingsPatch = {};
		if (ndTouched.discord) patch.discord_webhook_url = ndWebhook.trim();
		if (ndTouched.token) patch.telegram_bot_token = ndToken.trim();
		if (ndTouched.chat) patch.telegram_chat_id = ndChatID.trim();
		const clearing: string[] = [];
		if (ndTouched.discord && patch.discord_webhook_url === '' && ndLoaded?.discord_enabled) {
			clearing.push('Discord');
		}
		if (
			(ndTouched.token && patch.telegram_bot_token === '') ||
			(ndTouched.chat && patch.telegram_chat_id === '')
		) {
			if (ndLoaded?.telegram_enabled) clearing.push('Telegram');
		}
		if (!('discord_webhook_url' in patch || 'telegram_bot_token' in patch || 'telegram_chat_id' in patch)) {
			toast.info('Nada que guardar', {
				description: 'Escribí al menos un campo para actualizar la configuración.'
			});
			return;
		}
		if (clearing.length > 0) {
			ndPendingPatch = patch;
			ndConfirmChannel = clearing.join(' y ');
			ndConfirmOpen = true;
			return;
		}
		await saveNotify(patch);
	}

	async function saveNotify(patch: NotifySettingsPatch): Promise<void> {
		savingNotif = true;
		try {
			const res = await putNotifySettings(patch);
			if (res.ok) {
				ndLoaded = res.data;
				ndWebhook = '';
				ndToken = '';
				ndChatID = '';
				ndTouched = { discord: false, token: false, chat: false };
				ndConfirmOpen = false;
				ndPendingPatch = null;
				toast.success('Notificaciones actualizadas', {
					description: 'Los cambios aplican a la próxima alerta (sin reiniciar el backend).'
				});
			} else {
				toast.error(`No se pudo guardar (${res.status})`, {
					description: res.error ?? undefined
				});
			}
		} catch {
			toast.error('Backend no disponible');
		} finally {
			savingNotif = false;
		}
	}

	/** Probar el canal GUARDADO (el test usa la config persistente, no este form). */
	async function testNotif(channel: 'discord' | 'telegram'): Promise<void> {
		const res = await testNotify(channel);
		if (res.ok) {
			toast.success('Mensaje de prueba enviado');
		} else if (res.status === 503) {
			toast.error('Canal no configurado', {
				description: 'Guardá una configuración para este canal primero.'
			});
		} else {
			toast.error(`Fallo de entrega (${res.status})`, {
				description: res.error ?? 'El canal rechazó el mensaje.'
			});
		}
	}

	// carga al abrir (el consumidor monta/desmonta con bind de open)
	$effect(() => {
		if (!open) return;
		void loadNotify();
	});
</script>

<Dialog bind:open title="Canales de notificación" contentClass="w-[min(96vw,840px)] max-h-[86dvh]">
	{#if open}
		<div class="grid gap-4">
			<form id="notify-settings-form" class="grid gap-3" onsubmit={submitNotif}>
				<div class="notification-config-grid">
					<Card>
						<CardHeader class="pb-3">
							<div class="notification-channel-heading">
								<CardTitle class="flex items-center gap-2 text-base">
									<MessageCircle size={17} class="text-secondary" aria-hidden="true" /> Discord
								</CardTitle>
								<Badge variant={ndLoaded?.discord_enabled ? 'default' : 'outline'}>
									{ndLoaded ? (ndLoaded.discord_enabled ? 'Activo' : 'Sin configurar') : 'Leyendo…'}
								</Badge>
							</div>
							<CardDescription>Recibe una notificación cuando se abre una alerta.</CardDescription>
						</CardHeader>
						<CardContent class="grid gap-2">
							<Label for="nm-webhook">Webhook URL</Label>
							<Input
								id="nm-webhook"
								bind:value={ndWebhook}
								oninput={() => (ndTouched.discord = true)}
								autocomplete="off"
								placeholder={ndLoaded?.discord_webhook_url
									? `••••••${ndLoaded.discord_webhook_url}`
									: 'https://discord.com/api/webhooks/…'}
							/>
							<p class="text-xs text-muted-foreground">
								{ndLoaded?.discord_enabled
									? 'La dirección guardada está protegida. Escribe otra para reemplazarla.'
									: 'Pega la URL del webhook de tu canal.'}
							</p>
						</CardContent>
					</Card>

					<Card>
						<CardHeader class="pb-3">
							<div class="notification-channel-heading">
								<CardTitle class="flex items-center gap-2 text-base">
									<Send size={16} class="text-secondary" aria-hidden="true" /> Telegram
								</CardTitle>
								<Badge variant={ndLoaded?.telegram_enabled ? 'default' : 'outline'}>
									{ndLoaded ? (ndLoaded.telegram_enabled ? 'Activo' : 'Sin configurar') : 'Leyendo…'}
								</Badge>
							</div>
							<CardDescription>Usa un bot y el chat al que se enviarán los avisos.</CardDescription>
						</CardHeader>
						<CardContent class="grid gap-2">
							<Label for="nm-token">Bot token</Label>
							<Input
								id="nm-token"
								bind:value={ndToken}
								oninput={() => (ndTouched.token = true)}
								autocomplete="off"
								placeholder={ndLoaded?.telegram_bot_token ? 'Token protegido' : '123456789:AA…'}
							/>
							<p class="text-xs text-muted-foreground">
								{ndLoaded?.telegram_bot_token
									? 'El token guardado no se vuelve a mostrar. Escribe uno nuevo solo para rotarlo.'
									: 'Obtén la credencial creando un bot con @BotFather.'}
							</p>
							<Label for="nm-chat" class="mt-2">Chat ID</Label>
							<Input
								id="nm-chat"
								bind:value={ndChatID}
								oninput={() => (ndTouched.chat = true)}
								autocomplete="off"
								placeholder={ndLoaded?.telegram_chat_id ?? '-1001234 (también admite grupos)'}
							/>
						</CardContent>
					</Card>
				</div>
			</form>

			<Card>
				<CardHeader class="pb-3">
					<CardTitle class="text-base">Probar conexión</CardTitle>
					<CardDescription>Envía un mensaje usando la configuración guardada, no los campos sin guardar.</CardDescription>
				</CardHeader>
				<CardContent class="grid gap-2 sm:grid-cols-2">
					<Button variant="action" size="sm" disabled={!ndLoaded?.discord_enabled} onclick={() => void testNotif('discord')}>
						<MessageCircle size={14} aria-hidden="true" /> Probar Discord
					</Button>
					<Button variant="action" size="sm" disabled={!ndLoaded?.telegram_enabled} onclick={() => void testNotif('telegram')}>
						<Send size={14} aria-hidden="true" /> Probar Telegram
					</Button>
				</CardContent>
			</Card>
		</div>
	{/if}
	{#snippet footer()}
		{#if open}
			<div class="flex flex-wrap items-center justify-between gap-3">
				<p class="text-xs text-muted-foreground">Las credenciales guardadas no se muestran de nuevo.</p>
				<Button form="notify-settings-form" type="submit" disabled={savingNotif}>
					<Check size={15} aria-hidden="true" />
					{savingNotif ? 'Guardando…' : 'Guardar cambios'}
				</Button>
			</div>
		{/if}
	{/snippet}
</Dialog>

<!-- Confirmación: vaciar un canal activo lo deshabilita (REQ-050) -->
<Dialog bind:open={ndConfirmOpen} title="Deshabilitar canal">
	<div class="grid gap-4">
		<p class="text-sm text-muted-foreground">
			Vas a vaciar la configuración de
			<span class="font-medium text-foreground">{ndConfirmChannel}</span>.
			Ese canal dejará de recibir notificaciones de alertas.
		</p>
		<div class="flex justify-end gap-2">
			<Button
				variant="action"
				onclick={() => {
					ndConfirmOpen = false;
					ndPendingPatch = null;
				}}
			>
				Cancelar
			</Button>
			<Button
				variant="destructive"
				onclick={() => {
					if (ndPendingPatch) void saveNotify(ndPendingPatch);
				}}
			>
				Deshabilitar y guardar
			</Button>
		</div>
	</div>
</Dialog>
