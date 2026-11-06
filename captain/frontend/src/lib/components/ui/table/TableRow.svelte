<script lang="ts">
	import type { HTMLAttributes } from 'svelte/elements';

	let { class: className = '', children, ...rest }: HTMLAttributes<HTMLTableRowElement> = $props();
</script>

<tr class="border-b border-border transition-colors hover:bg-accent/30 {className}" {...rest}>
	{@render children?.()}
</tr>
