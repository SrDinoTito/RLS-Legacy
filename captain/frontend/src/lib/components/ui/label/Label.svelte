<script lang="ts">
	import type { HTMLLabelAttributes } from 'svelte/elements';

	let { class: className = '', children, ...rest }: HTMLLabelAttributes = $props();
</script>

<label class="text-sm font-medium leading-none text-foreground {className}" {...rest}>
	{@render children?.()}
</label>
