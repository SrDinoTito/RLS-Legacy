<script lang="ts">
	import Cpu from '@lucide/svelte/icons/cpu';
	import MemoryStick from '@lucide/svelte/icons/memory-stick';
	import WifiOff from '@lucide/svelte/icons/wifi-off';
	import { toast } from 'svelte-sonner';
	import { listAlerts, resolveAlert } from '#lib/api/client.js';
	import { normalizeAlerts, type NormalizedAlert } from '#lib/alerts.js';
	import { sse, refreshAlerts } from '#lib/stores/sse.svelte.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Tabs } from '#lib/components/ui/tabs/index.js';
	import { Badge } from '#lib/components/ui/badge/index.js';
	import {
		Table,
		TableCell,
		TableHead,
		TableRow
	} from '#lib/components/ui/table/index.js';
	import { fmtTs, alertTypeLabel, fmtAlertMessage, fmtAlertReading } from '#lib/format.js';

	/*
	 * Alertas globales (REQ-022): tabs activas/todas con scope API
	 * (active|all), resolver (409 → toast), timestamps humano.
	 * Refetch en vivo por alert_new/alert_resolved vía eventTick del store SSE.
	 */

	type Scope = 'active' | 'all';
	let scope = $state<Scope>('active');
	let alerts = $state<NormalizedAlert[]>([]);
	let loading = $state(false);
	const openAlerts = $derived(alerts.filter((alert) => !alert.resolved_at));
	const openByType = $derived.by(() => ({
		offline: openAlerts.filter((alert) => alert.type === 'offline').length,
		high_cpu: openAlerts.filter((alert) => alert.type === 'high_cpu').length,
		high_ram: openAlerts.filter((alert) => alert.type === 'high_ram').length
	}));

	// deps: scope (cambio de tab) + eventTick (SSE alert_new/alert_resolved)
	$effect(() => {
		void scope;
		void sse.eventTick;
		void refreshAlerts(); // sanea el badge global sin duplicar el conteo del store
		void load();
	});

	async function load(): Promise<void> {
		loading = true;
		try {
			const res = await listAlerts(scope);
			if (res.ok) alerts = normalizeAlerts(res.data ?? []);
			else toast.error(`No se pudieron cargar las alertas (${res.status})`);
		} catch {
			toast.error('Backend no disponible');
		} finally {
			loading = false;
		}
	}

	async function resolveRow(id: number): Promise<void> {
		try {
			const res = await resolveAlert(id);
			if (res.ok) {
				toast.success('Alerta cerrada', {
					description: 'Se cierra este registro; las reglas automáticas siguen activas.'
				});
				void load();
			} else if (res.status === 409) {
				// toast on 409 (ya resuelta por otro panel)
				toast.warning('La alerta ya estaba cerrada');
				void load();
			} else {
				toast.error(`No se pudo cerrar (${res.status})`);
			}
		} catch {
			toast.error('Backend no disponible');
		}
	}
</script>

<svelte:head>
	<title>Alertas — RLS Legacy</title>
</svelte:head>


<div class="grid gap-6">
	<header class="route-heading">
		<div>
			<p class="route-kicker">Bitácora de a bordo · incidencias</p>
			<h1 class="route-title">Bitácora de alertas</h1>
			<p class="route-copy">
				CPU/RAM se revisan automáticamente. Cerrar una alerta solo cierra ese registro; no apaga la regla.
			</p>
		</div>
		<div class="flex flex-wrap items-center gap-3">
			{#if sse.activeAlerts > 0}
				<Badge variant="destructive">{sse.activeAlerts} activa(s)</Badge>
			{:else}
				<Badge variant="secondary">sin alertas activas</Badge>
			{/if}
			<Tabs
				options={[
					{ value: 'active' as Scope, label: 'Activas' },
					{ value: 'all' as Scope, label: 'Todas' }
				]}
				bind:value={scope}
			/>
		</div>
	</header>

	<section class="alarm-guide-grid" aria-label="Tipos de alarma y condiciones de disparo">
		<article class="alarm-guide alarm-guide--offline">
			<div class="alarm-guide__icon"><WifiOff size={18} strokeWidth={1.8} aria-hidden="true" /></div>
			<div class="min-w-0">
				<div class="alarm-guide__heading">
					<h2>Sin señal</h2>
					<span>{openByType.offline} abiertas</span>
				</div>
				<p>Se activa tras más de 3 intervalos sin recibir un reporte y se resuelve al reconectar.</p>
			</div>
		</article>
		<article class="alarm-guide alarm-guide--cpu">
			<div class="alarm-guide__icon"><Cpu size={18} strokeWidth={1.8} aria-hidden="true" /></div>
			<div class="min-w-0">
				<div class="alarm-guide__heading">
					<h2>CPU alta</h2>
					<span>{openByType.high_cpu} abiertas</span>
				</div>
				<p>La lectura de CPU supera el umbral de esa instancia; el valor inicial es 90 %.</p>
			</div>
		</article>
		<article class="alarm-guide alarm-guide--ram">
			<div class="alarm-guide__icon"><MemoryStick size={18} strokeWidth={1.8} aria-hidden="true" /></div>
			<div class="min-w-0">
				<div class="alarm-guide__heading">
					<h2>RAM alta</h2>
					<span>{openByType.high_ram} abiertas</span>
				</div>
				<p>Memoria usada sobre el límite supera el umbral configurado; el valor inicial es 95 %.</p>
			</div>
		</article>
	</section>

	<Table class="alerts-table">
		<thead>
			<TableRow class="bg-muted/40">
				<TableHead class="w-[130px]">Tipo</TableHead>
				<TableHead>Agent</TableHead>
				<TableHead>Mensaje</TableHead>
				<TableHead>Lectura</TableHead>
				<TableHead>Disparada</TableHead>
				<TableHead>Resuelta</TableHead>
				<TableHead class="w-[110px] text-right">Acción</TableHead>
			</TableRow>
		</thead>
		<tbody>
			{#each alerts as alert (alert.id)}
				<TableRow>
					<TableCell>
						<Badge variant={alert.resolved_at ? 'outline' : 'destructive'}>
							{alertTypeLabel(alert.type)}
						</Badge>
					</TableCell>
					<TableCell class="whitespace-nowrap">{alert.agent_name ?? `#${alert.agent_id}`}</TableCell>
					<TableCell class="max-w-[360px] truncate text-muted-foreground">
						{fmtAlertMessage(alert.message)}
					</TableCell>
					<TableCell class="whitespace-nowrap font-medium tabular-nums text-foreground">
						{fmtAlertReading(alert.type, alert.value)}
					</TableCell>
					<TableCell class="whitespace-nowrap">{fmtTs(alert.fired_at)}</TableCell>
					<TableCell class="whitespace-nowrap">
						{alert.resolved_at ? fmtTs(alert.resolved_at) : '—'}
					</TableCell>
					<TableCell class="text-right">
						{#if !alert.resolved_at}
							<Button
								variant="action"
								title="Cierra este registro; no desactiva las reglas automáticas"
								onclick={() => void resolveRow(alert.id)}
							>
								Cerrar alerta
							</Button>
						{:else}
							<span class="text-xs text-muted-foreground">cerrada</span>
						{/if}
					</TableCell>
				</TableRow>
			{:else}
				<TableRow>
					<TableCell colspan={7}>
						<p class="py-6 text-center text-sm text-muted-foreground">
							{loading
								? 'Cargando…'
								: scope === 'active'
									? 'Ninguna alerta abierta — mar en calma.'
									: 'Ninguna alerta histórica todavía.'}
						</p>
					</TableCell>
				</TableRow>
			{/each}
		</tbody>
	</Table>
</div>
