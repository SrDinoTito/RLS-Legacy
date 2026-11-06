<script lang="ts">
	import type { HTMLAttributes } from 'svelte/elements';

	let { class: className = '', children, ...rest }: HTMLAttributes<HTMLDivElement> = $props();
</script>

<div class="px-6 pt-6 {className}" {...rest}>
	{@render children?.()}
</div>
