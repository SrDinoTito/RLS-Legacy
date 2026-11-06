<script lang="ts">
	import type { HTMLButtonAttributes } from 'svelte/elements';

	type Variant = 'default' | 'secondary' | 'destructive' | 'ghost'
	             | 'action' | 'destructive-ghost' | 'toggle-active';
	type Size = 'sm' | 'md';

	let {
		type = 'button',
		variant = 'default',
		size = 'md',
		class: className = '',
		children,
		...rest
	}: HTMLButtonAttributes & {
		type?: 'button' | 'submit' | 'reset' | null;
		variant?: Variant;
		size?: Size;
	} = $props();

	// size sm (pulido): h-8 px-3 para CTAs compactos (empty state, botones de fila)
	const sizeClass = $derived(size === 'sm' ? 'h-8 px-3 text-xs' : 'h-10 px-4 text-sm');
</script>

<!-- Lenguaje de acción universal: los botones descansan como texto limpio y
     revelan borde, superficie y brillo solo al interactuar. Los estados de
     selección permanecen destacados para no ocultar qué opción está activa. -->
<button
	{type}
	class="glow-gold inline-flex {sizeClass} items-center justify-center gap-2 rounded-lg border border-transparent bg-transparent font-medium transition-[background-color,border-color,color,box-shadow]
		focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background
		focus-visible:outline-none disabled:pointer-events-none disabled:opacity-50
		{variant === 'default' && 'text-foreground/90 hover:border-primary/50 hover:bg-primary/12 hover:text-foreground'}
		{variant === 'secondary' && 'text-foreground/90 hover:border-secondary/50 hover:bg-secondary/10 hover:text-secondary'}
		{variant === 'destructive' && 'text-muted-foreground hover:border-destructive/50 hover:bg-destructive/12 hover:text-destructive'}
		{variant === 'ghost' && 'text-foreground/80 hover:border-border hover:bg-accent hover:text-foreground'}
		{variant === 'action' && 'text-foreground/90 hover:border-primary/50 hover:bg-primary/12 hover:text-foreground'}
		{variant === 'destructive-ghost' && 'text-muted-foreground hover:border-destructive/50 hover:bg-destructive/12 hover:text-destructive'}
		{variant === 'toggle-active' && 'border-primary/45 bg-primary/15 text-primary font-semibold hover:bg-primary/20'}
		{className}"
	{...rest}
>
	{@render children?.()}
</button>
