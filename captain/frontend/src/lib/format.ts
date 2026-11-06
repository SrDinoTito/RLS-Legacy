/*
 * Formateadores compartidos por las páginas (Dashboard/Detalle/Alertas/Admin).
 * Inter para datos, strings en español corto (desktop-first).
 */

/** "…" mientras null; fecha local compacta para unix SEGUNDOS. */
export function fmtTs(secs: number | null | undefined): string {
	if (secs == null) return '—';
	return new Date(secs * 1000).toLocaleString('es', {
		day: '2-digit',
		month: '2-digit',
		hour: '2-digit',
		minute: '2-digit',
		second: '2-digit'
	});
}

/** Hace N segundos legible ("hace 12 s") para last_seen. */
export function fmtAgo(secs: number | null | undefined, nowSecs: number): string {
	if (secs == null) return 'nunca';
	const delta = Math.max(0, Math.floor(nowSecs - secs));
	if (delta < 60) return `hace ${delta} s`;
	if (delta < 3600) return `hace ${Math.floor(delta / 60)} min`;
	return `hace ${Math.floor(delta / 3600)} h`;
}

/**
 * uptime_sec humanizado (compacto, una unidad grande + resto):
 * <90s → "45s" · <1h → "12m 30s" · <48h → "5h 20m" · después → "3d 4h".
 */
export function fmtUptime(seconds: number | null | undefined): string {
	if (seconds == null) return '—';
	const s = Math.floor(seconds);
	if (s < 90) return `${s}s`;
	if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
	const h = Math.floor(s / 3600);
	const remM = Math.floor((s % 3600) / 60);
	if (s < 48 * 3600) return `${h}h ${remM}m`;
	const d = Math.floor(h / 24);
	return `${d}d ${h % 24}h`;
}

/** MB con 1 decimal y "—" en null (detalle/tooltip; el card usa fmtBytesMb). */
export function fmtMb(mb: number | null | undefined): string {
	if (mb == null) return '—';
	return `${mb.toFixed(1)} MB`;
}

/**
 * Bytes (MB de entrada) compacto para el card 2×2 (nowrap): unidad al FINAL,
 * 1 solo decimal. null → "—". <1 MB → "38.2 KB"; <100 MB → "12.3 MB";
 * mayor → MB sin decimales ("132 MB").
 */
export function fmtBytesMb(mb: number | null | undefined): string {
	if (mb == null) return '—';
	if (mb < 1) return `${(mb * 1024).toFixed(1)} KB`;
	if (mb < 100) return `${mb.toFixed(1)} MB`;
	return `${Math.round(mb)} MB`;
}

/**
 * Par de valores en MB con unidad común al final: "2.2 / 32 MB", "12.3 / 38 KB".
 * La unidad se decide por el MAYOR de los dos (RAM usada/limit, RED rx/tx).
 * Un solo null → solo el presente; ambos → "—".
 */
export function fmtBytesPairMb(
	a: number | null | undefined,
	b: number | null | undefined
): string {
	if (a == null && b == null) return '—';
	if (a == null) return fmtBytesMb(b);
	if (b == null) return fmtBytesMb(a);
	const big = Math.max(a, b);
	if (big < 1) return `${(a * 1024).toFixed(1)} / ${(b * 1024).toFixed(1)} KB`;
	if (big < 100) return `${a.toFixed(1)} / ${b.toFixed(1)} MB`;
	return `${Math.round(a)} / ${Math.round(b)} MB`;
}

/** Pct con 1 decimal y "—" en null (CPU/thresholds). */
export function fmtPct(v: number | null | undefined): string {
	if (v == null) return '—';
	return `${v.toFixed(1)}%`;
}

/**
 * Valor de disco con unidad al final, 1 decimal: "38.2 MB" / "18.4 GB"
 * (<1 MB → KB; <1024 MB → MB; después GB). Base 1024 (binaria, coherente
 * con los MiB que reporta el explorer).
 */
export function fmtBytesUnitMb(mb: number | null | undefined): string {
	if (mb == null) return '—';
	if (mb < 1) return `${(mb * 1024).toFixed(1)} KB`;
	if (mb < 1024) return `${mb.toFixed(1)} MB`;
	return `${(mb / 1024).toFixed(1)} GB`;
}

/**
 * Almacenamiento del agent (explorer v4 disk, snapshot df en MiB):
 * "52.3 / 74.5 GB" — el FREE más prominente y el total va tenue en el
 * componente (aquí son textos plano, unidad común decidida por el total).
 * Sin total (0/null) → solo el libre ("52.3 GB"). Ambos null → "—".
 */
export function fmtDisk(free: number | null | undefined, total: number | null | undefined): string {
	if (free == null && total == null) return '—';
	if (total == null || total === 0) return fmtBytesUnitMb(free);
	const f = free ?? 0;
	return `${fmtBytesUnitMb(f)} / ${fmtBytesUnitMb(total)}`;
}

/** Título corto de alerta por tipo (badge + timeline). */
export function alertTypeLabel(type: string): string {
	switch (type) {
		case 'offline':
			return 'OFFLINE';
		case 'high_cpu':
			return 'CPU ALTA';
		case 'high_ram':
			return 'RAM ALTA';
		default:
			return type.toUpperCase();
	}
}

/** Corrige mensajes históricos que llamaban "sostenido" a una sola muestra. */
export function fmtAlertMessage(message: string | null | undefined): string {
	if (!message) return '';
	return message.replace(/\b(CPU|RAM) sostenido\b/gi, (_match, metric: string) => `${metric.toUpperCase()} alta`);
}

/** Lectura que disparó la alerta: porcentajes para umbrales y duración sin señal. */
export function fmtAlertReading(type: string, value: number | null | undefined): string {
	if (value == null || !Number.isFinite(value)) return '—';
	if (type === 'offline') return `${fmtUptime(value)} sin señal`;
	if (type === 'high_cpu' || type === 'high_ram') return `${value.toFixed(1)}%`;
	return String(value);
}
