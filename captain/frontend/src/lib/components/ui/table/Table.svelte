<script lang="ts">
	import type { HTMLAttributes } from 'svelte/elements';

	let { class: className = '', children, ...rest }: HTMLAttributes<HTMLTableElement> = $props();
</script>

<div class="w-full overflow-x-auto rounded-lg border border-border">
	<table class="w-full caption-bottom text-sm {className}" {...rest}>
		{@render children?.()}
	</table>
</div>
