<script lang="ts">
	import type { HTMLAttributes } from 'svelte/elements';

	type Variant = 'default' | 'secondary' | 'destructive' | 'outline';

	let {
		variant = 'default',
		class: className = '',
		children,
		...rest
	}: HTMLAttributes<HTMLSpanElement> & { variant?: Variant } = $props();
</script>

<span
	class="inline-flex items-center gap-1 rounded-full border px-2.5 py-0.5 text-xs font-medium tracking-wide
		{variant === 'default' && 'border-primary/40 bg-primary/15 text-primary glow-gold'}
		{variant === 'secondary' && 'border-secondary/40 bg-secondary/15 text-secondary'}
		{variant === 'destructive' && 'border-destructive/50 bg-destructive/15 text-destructive'}
		{variant === 'outline' && 'border-border bg-transparent text-muted-foreground'}
		{className}"
	{...rest}
>
	{@render children?.()}
</span>
