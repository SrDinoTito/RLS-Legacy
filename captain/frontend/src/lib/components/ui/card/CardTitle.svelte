<script lang="ts">
	import type { HTMLAttributes } from 'svelte/elements';

	let { class: className = '', children, ...rest }: HTMLAttributes<HTMLHeadingElement> = $props();
</script>

<h3 class="font-display text-lg font-bold tracking-widest uppercase {className}" {...rest}>
	{@render children?.()}
</h3>
