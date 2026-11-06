<script lang="ts">
	import type { HTMLInputAttributes } from 'svelte/elements';

	let {
		value = $bindable(),
		type = 'text',
		class: className = '',
		...rest
	}: HTMLInputAttributes & {
		value?: string | number | null;
		type?: string;
	} = $props();
</script>

<input
	bind:value
	{type}
	class="flex h-10 w-full rounded-lg border border-input bg-background px-3 py-2 text-sm text-foreground
		placeholder:text-muted-foreground transition-colors
		focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none
		disabled:cursor-not-allowed disabled:opacity-50
		{className}"
	{...rest}
/>
