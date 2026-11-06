<script lang="ts">
	import type { HTMLAttributes } from 'svelte/elements';

	let { class: className = '', children, ...rest }: HTMLAttributes<HTMLDivElement> = $props();
</script>

<div class="space-y-4 p-6 {className}" {...rest}>
	{@render children?.()}
</div>
