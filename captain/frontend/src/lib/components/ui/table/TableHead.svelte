<script lang="ts">
	import type { HTMLThAttributes } from 'svelte/elements';

	let { class: className = '', children, ...rest }: HTMLThAttributes = $props();
</script>

<th
	class="h-10 px-3 text-left align-middle text-xs font-medium uppercase tracking-wide text-muted-foreground {className}"
	{...rest}
>
	{@render children?.()}
</th>
