<script lang="ts">
	import { onMount } from 'svelte';

	/*
	 * Starfield (REQ-021 / design DSG tema):
	 * - canvas fijo de fondo (z-0, pointer-events-none)
	 * - ~200 estrellas con twinkle (fase aleatoria) y deriva lenta 2–4 px/s
	 * - blanco cálido / dorado / cian tenues, opacity <= 0.45
	 * - prefers-reduced-motion -> frame estático sin animación
	 * - cleanup completo (rAF + listeners)
	 */

	interface Star {
		x: number;
		y: number;
		r: number;
		color: string;
		maxAlpha: number;
		phase: number;
		twinkleSpeed: number;
		vx: number;
		vy: number;
	}

	type RGBA = [number, number, number, number];

	// Colores: blanco cálido, oro, cian — siempre tenues (Last examplar: alpha <= .45)
	const PALETTE: RGBA[] = [
		[255, 247, 232, 0.45], // blanco cálido
		[242, 198, 106, 0.4], // oro tenue
		[127, 216, 232, 0.35] // cian tenue
	];

	const STAR_COUNT = 200;
	const DRIFT_MIN = 2;
	const DRIFT_MAX = 4; // px/s

	onMount(() => {
		const canvas = document.getElementById('starfield') as HTMLCanvasElement | null;
		if (!canvas) return;
		const ctx = canvas.getContext('2d');
		if (!ctx) return;

		let width = window.innerWidth;
		let height = window.innerHeight;
		let raf = 0;
		let running = true;
		const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;

		const rand = (min: number, max: number) => min + Math.random() * (max - min);

		function createStars(): Star[] {
			return Array.from({ length: STAR_COUNT }, () => {
				const [r, g, b, a] = PALETTE[Math.floor(Math.random() * PALETTE.length)];
				const angle = Math.random() * Math.PI * 2;
				const speed = rand(DRIFT_MIN, DRIFT_MAX);
				return {
					x: Math.random() * width,
					y: Math.random() * height,
					r: rand(0.4, 1.4),
					color: `rgba(${r},${g},${b},${a})`,
					maxAlpha: a,
					phase: Math.random() * Math.PI * 2,
					twinkleSpeed: rand(0.5, 1.8),
					vx: Math.sin(angle) * speed,
					vy: Math.cos(angle) * speed * 0.35 // deriva mayormente lateral
				};
			});
		}

		let stars = createStars();

		function resize() {
			width = window.innerWidth;
			height = window.innerHeight;
			canvas!.width = width * devicePixelRatio;
			canvas!.height = height * devicePixelRatio;
			canvas!.style.width = `${width}px`;
			canvas!.style.height = `${height}px`;
			ctx!.setTransform(devicePixelRatio, 0, 0, devicePixelRatio, 0, 0);
			// Reposicionar estrellas fuera de los nuevos límites
			for (const s of stars) {
				if (s.x > width) s.x = Math.random() * width;
				if (s.y > height) s.y = Math.random() * height;
			}
		}

		function paint(time: number) {
			ctx!.clearRect(0, 0, width, height);
			for (const s of stars) {
				const twinkle = 0.35 + 0.65 * (0.5 + 0.5 * Math.sin(s.phase + time * s.twinkleSpeed));
				const finalAlpha = Math.min(s.maxAlpha * twinkle, 0.45);
				const { r, color } = s;
				ctx!.globalAlpha = finalAlpha;
				ctx!.fillStyle = color;
				ctx!.beginPath();
				ctx!.arc(s.x, s.y, r, 0, Math.PI * 2);
				ctx!.fill();
			}
			ctx!.globalAlpha = 1;
		}

		function advanceAndPaint(dt: number, time: number) {
			for (const s of stars) {
				s.x += s.vx * dt;
				s.y += s.vy * dt;
				if (s.x < -4) s.x = width + 4;
				else if (s.x > width + 4) s.x = -4;
				if (s.y < -4) s.y = height + 4;
				else if (s.y > height + 4) s.y = -4;
			}
			paint(time);
		}

		resize();
		window.addEventListener('resize', resize);

		if (reducedMotion) {
			// Frame estático: sin animación, sin deriva
			paint(0);
		} else {
			let last = performance.now();
			const loop = (now: number) => {
				if (!running) return;
				const dt = Math.min((now - last) / 1000, 0.1);
				last = now;
				advanceAndPaint(dt, now / 1000);
				raf = requestAnimationFrame(loop);
			};
			raf = requestAnimationFrame(loop);
		}

		return () => {
			running = false;
			cancelAnimationFrame(raf);
			window.removeEventListener('resize', resize);
		};
	});
</script>

<canvas
	id="starfield"
	class="pointer-events-none fixed inset-0 z-0"
	aria-hidden="true"
></canvas>
