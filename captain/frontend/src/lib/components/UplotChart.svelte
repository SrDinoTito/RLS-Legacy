<script lang="ts">
	import uPlot from 'uplot';
	import 'uplot/dist/uPlot.min.css';
	import { onMount } from 'svelte';


	/*
	 * UplotChart (TASK-006, de decisión de implementación: uPlot ~45KB
	 * frente a chart.js ~200KB): wrapper fino con tokens de tema cacheados
	 * y huecos (null → gap nativo de uPlot, buckets vacíos del history).
	 */
	interface SeriesSpec {
		/** Nombre en la leyenda. */
		label: string;
		/** Valores alineados con el eje x (null = hueco). */
		values: (number | null)[];
		/** Color CSS del tema (tokens cacheados). */
		color: string;
		dash?: number[];
		unit?: string;
		digits?: number;
		/** Relleno bajo la línea (0–1 alpha); omitido para líneas dashed. */
		fillAlpha?: number;
	}

	interface Props {
		times: number[];
		series: SeriesSpec[];
		height?: number;
		yMin?: number | null;
		yMax?: number | null;
		class?: string;
	}

	let { times, series, height = 200, yMin = null, yMax = null, class: className = '' }: Props =
		$props();

	/* ------------------------------ tema cacheado ------------------------------ */

	let tokens: Record<string, string> | null = null;
	function themeToken(name: string, fallback: string): string {
		const cache = (tokens ??= readTokens());
		return cache[name] ?? fallback;
	}
	function readTokens(): Record<string, string> {
		if (typeof document === 'undefined') return {};
		const cs = getComputedStyle(document.documentElement);
		return {
			muted: cs.getPropertyValue('--color-muted-foreground').trim() || 'hsl(240 8% 56%)',
			border: cs.getPropertyValue('--color-border').trim() || 'hsl(240 8% 18%)'
		};
	}
	function withAlpha(color: string, alpha: number): string {
		return color.endsWith(')') && color.startsWith('hsl')
			? `${color.slice(0, -1)} / ${alpha})`
			: color;
	}

	/* --------------------------------- estado --------------------------------- */

	let container: HTMLDivElement | null = null;
	let chart: uPlot | null = null;
	let lastStructKey = '';
	let ro: ResizeObserver | null = null;
	let width = 600;

	onMount(() => {
		if (!container) return;
		if (container.clientWidth > 0) width = container.clientWidth;
		ro = new ResizeObserver(() => {
			if (!container) return;
			const w = container.clientWidth;
			if (w > 0 && w !== width) {
				width = w;
				chart?.setSize({ width: w, height });
			}
		});
		ro.observe(container);
		return () => {
			ro?.disconnect();
			ro = null;
			chart?.destroy();
			chart = null;
			lastStructKey = '';
		};
	});

	const structKey = $derived(`${series.map((s) => s.label).join('|')}#${height}`);

	function buildOptions(specs: SeriesSpec[], dataWidth: number): uPlot.Options {
		const axisStroke = themeToken('muted', 'hsl(240 8% 56%)');
		const gridStroke = withAlpha(themeToken('border', 'hsl(240 8% 18%)'), 0.6);
		return {
			width: Math.max(240, dataWidth),
			height,
			// DOMTokenList.add('') lanza DOMException: addClass de uPlot usa guard
			// `!= null` y deja pasar ''; coalesce a undefined para que lo omita.
			class: className || undefined,
			axes: [
				{
					stroke: axisStroke,
					grid: { stroke: gridStroke, width: 1 },
					ticks: { stroke: gridStroke, width: 1 },
					values: (_u, vals) =>
						vals.map((v) => {
							const d = new Date(v * 1000);
							return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
						})
				},
				{
					stroke: axisStroke,
					grid: { stroke: gridStroke, width: 1 },
					ticks: { stroke: gridStroke, width: 1 },
					size: 48
				}
			],
			scales: { x: { time: true }, y: { range: [yMin ?? null, yMax ?? null] } },
			series: [
				{},
				...specs.map((s) => ({
					label: s.label,
					stroke: s.color,
					width: 1.5,
					dash: s.dash,
					spanGaps: false,
					fill: s.fillAlpha != null && !s.dash ? withAlpha(s.color, s.fillAlpha) : undefined,
					value: (_u: uPlot, v: number | null) =>
						v == null ? '—' : `${v.toFixed(s.digits ?? 1)}${s.unit ? ` ${s.unit}` : ''}`
				}))
			],
			legend: { show: true }
		};
	}

	$effect(() => {
		// deps explícitos: valores nuevos → setData (sin recrear); labels/eje Y → recrear
		void times.length;
		void series.map((s) => s.values);
		void yMin;
		void yMax;
		if (!container || !ro) return;
		const data: uPlot.AlignedData = [times, ...series.map((s) => s.values)];

		if (times.length === 0) {
			chart?.destroy();
			chart = null;
			lastStructKey = '';
			return;
		}

		if (!chart || structKey !== lastStructKey) {
			chart?.destroy();
			chart = new uPlot(buildOptions(series, width), data, container);
			lastStructKey = structKey;
		} else {
			chart.setData(data);
		}
	});
</script>

<div class="min-h-[120px] w-full {className}" bind:this={container}>
	{#if times.length === 0}
		<div
			class="flex h-[120px] items-center justify-center rounded-md border border-dashed border-border text-sm text-muted-foreground"
		>
			Sin datos en esta ventana de tiempo
		</div>
	{/if}
</div>
