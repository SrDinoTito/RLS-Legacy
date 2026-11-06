<script lang="ts">
	/*
	 * Sparkline inline SVG (TASK-006): polyline CSS puro, SIN librerías.
	 * - values puede traer null (huecos del history) → segmentos separados;
	 * - escala fija 0..100 (CPU %); alto de tight viewBox 100x28;
	 * - color por clase Tailwind (stroke-secondary/CPU) para heredar el tema.
	 */
	let {
		values,
		class: className = '',
		scaleMax = 100
	}: { values: (number | null)[]; class?: string; scaleMax?: number } = $props();

	const W = 100;
	const H = 28;
	const PAD = 2;

	const segments = $derived.by(() => {
		const n = values.length;
		const segs: string[] = [];
		let current: string[] = [];
		for (let i = 0; i < n; i += 1) {
			const v = values[i];
			if (v == null || !Number.isFinite(v)) {
				if (current.length > 1) segs.push(current.join(' '));
				current = [];
				continue;
			}
			const x =
				n === 1 ? W / 2 : PAD + (i / (n - 1)) * (W - 2 * PAD);
			const y = H - PAD - (Math.max(0, Math.min(scaleMax, v)) / scaleMax) * (H - 2 * PAD);
			current.push(`${x.toFixed(2)},${y.toFixed(2)}`);
		}
		if (current.length > 1) segs.push(current.join(' '));
		return segs;
	});
</script>

<!-- sparkline puramente decorativo: la info accesible vive en la fila
     "CPU · últimas 6 h" del card (por eso aria-hidden, sin role img) -->
<svg
	viewBox="0 0 {W} {H}"
	preserveAspectRatio="none"
	class="h-8 w-full overflow-visible {className}"
	aria-hidden="true"
>
	{#each segments as points}
		<polyline points={points} fill="none" stroke="currentColor" stroke-width="1.5" />
	{/each}
</svg>
