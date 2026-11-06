<script lang="ts">
	import ArrowDown from '@lucide/svelte/icons/arrow-down';
	import ArrowUp from '@lucide/svelte/icons/arrow-up';
	import type { Agent } from '#lib/api/types.js';
	import {
		fmtBytesMb,
		fmtBytesPairMb,
		fmtBytesUnitMb,
		fmtDisk,
		fmtPct,
		fmtUptime
	} from '#lib/format.js';

	let {
		agent,
		compact = false,
		muted = false
	}: {
		agent: Agent;
		compact?: boolean;
		muted?: boolean;
	} = $props();

	const cpu = $derived(agent.metrics?.cpu_pct ?? null);
	const ramUsed = $derived(agent.metrics?.ram_used_mb ?? null);
	const ramLimit = $derived(agent.metrics?.ram_limit_mb ?? null);
	const ramPct = $derived.by(() =>
		ramUsed == null || ramLimit == null || ramLimit <= 0 ? null : (ramUsed / ramLimit) * 100
	);
	const diskFree = $derived(agent.disk_free_mb);
	const diskTotal = $derived(agent.disk_total_mb);
	const diskFreePct = $derived.by(() =>
		diskFree == null || diskTotal == null || diskTotal <= 0 ? null : (diskFree / diskTotal) * 100
	);
	const diskText = $derived.by(() => {
		if (diskFree == null && diskTotal != null) return `— / ${fmtBytesUnitMb(diskTotal)}`;
		if (diskFree == null) return fmtDisk(diskFree, diskTotal);
		return `${fmtBytesUnitMb(diskFree)} libres${diskTotal != null && diskTotal > 0 ? ` / ${fmtBytesUnitMb(diskTotal)}` : ''}`;
	});

	function meterWidth(value: number | null): number {
		if (value == null || !Number.isFinite(value)) return 0;
		return Math.max(0, Math.min(100, value));
	}
</script>

<dl class="agent-metrics" class:agent-metrics--compact={compact} class:agent-metrics--muted={muted}>
	<div class="metric-tile">
		<div class="metric-heading">
			<dt>CPU</dt>
			<dd class:metric-value--hot={cpu != null && agent.thresholds_cpu != null && cpu > agent.thresholds_cpu}>
				{fmtPct(cpu)}
			</dd>
		</div>
		<div
			class="metric-track"
			role="progressbar"
			aria-label="Uso de CPU"
			aria-valuemin="0"
			aria-valuemax="100"
			aria-valuenow={cpu == null ? undefined : meterWidth(cpu)}
		>
			<span
				class="metric-fill metric-fill--gold"
				class:metric-fill--hot={cpu != null && agent.thresholds_cpu != null && cpu > agent.thresholds_cpu}
				style={`--meter-value:${meterWidth(cpu)}%`}
			></span>
		</div>
		<p class="metric-hint">umbral {fmtPct(agent.thresholds_cpu)}</p>
	</div>

	<div class="metric-tile">
		<div class="metric-heading">
			<dt>RAM</dt>
			<dd class:metric-value--hot={ramPct != null && agent.thresholds_ram != null && ramPct > agent.thresholds_ram}>
				{fmtBytesPairMb(ramUsed, ramLimit)}
			</dd>
		</div>
		<div
			class="metric-track"
			role="progressbar"
			aria-label="Uso de RAM"
			aria-valuemin="0"
			aria-valuemax="100"
			aria-valuenow={ramPct == null ? undefined : meterWidth(ramPct)}
		>
			<span
				class="metric-fill metric-fill--cyan"
				class:metric-fill--hot={ramPct != null && agent.thresholds_ram != null && ramPct > agent.thresholds_ram}
				style={`--meter-value:${meterWidth(ramPct)}%`}
			></span>
		</div>
		<p class="metric-hint">
			{ramPct == null ? 'límite no informado' : `${ramPct.toFixed(1)} % utilizado`}
		</p>
	</div>

	<div class="metric-tile">
		<div class="metric-heading"><dt>RED</dt><dd class="metric-subheading">por intervalo</dd></div>
		<div class="metric-network">
			<div>
				<span><ArrowDown size={12} strokeWidth={1.8} aria-hidden="true" /> Rx</span>
				<strong>{fmtBytesMb(agent.metrics?.rx_mb)}</strong>
			</div>
			<div>
				<span><ArrowUp size={12} strokeWidth={1.8} aria-hidden="true" /> Tx</span>
				<strong>{fmtBytesMb(agent.metrics?.tx_mb)}</strong>
			</div>
		</div>
		<p class="metric-hint">recibido · enviado</p>
	</div>

	<div class="metric-tile">
		<div class="metric-heading"><dt>UPTIME</dt><dd class="metric-subheading">ciclo {agent.interval}s</dd></div>
		<p class="metric-primary-value">{fmtUptime(agent.metrics?.uptime_sec)}</p>
		<p class="metric-hint">tiempo activo del contenedor</p>
	</div>

	<div class="metric-tile metric-tile--wide">
		<div class="metric-heading">
			<dt>ALMACENAMIENTO · LIBRE</dt>
			<dd>{diskText}</dd>
		</div>
		<div
			class="metric-track"
			role="progressbar"
			aria-label="Espacio libre de almacenamiento"
			aria-valuemin="0"
			aria-valuemax="100"
			aria-valuenow={diskFreePct == null ? undefined : meterWidth(diskFreePct)}
		>
			<span class="metric-fill metric-fill--cyan" style={`--meter-value:${meterWidth(diskFreePct)}%`}></span>
		</div>
		<p class="metric-hint">
			{diskFreePct == null ? 'capacidad total no informada' : `${diskFreePct.toFixed(1)} % del volumen disponible`}
		</p>
	</div>
</dl>
