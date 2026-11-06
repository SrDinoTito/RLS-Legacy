<script lang="ts">
	import type { HTMLAttributes } from 'svelte/elements';

	let { class: className = '', children, ...rest }: HTMLAttributes<HTMLDivElement> = $props();
</script>

<div
	class="rounded-lg border bg-card text-card-foreground shadow-sm {className}"
	{...rest}
>
	{@render children?.()}
</div>
