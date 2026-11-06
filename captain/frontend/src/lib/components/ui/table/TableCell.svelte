<script lang="ts">
	import type { HTMLTdAttributes } from 'svelte/elements';

	let { class: className = '', children, ...rest }: HTMLTdAttributes = $props();
</script>

<td class="px-3 py-2 align-middle {className}" {...rest}>
	{@render children?.()}
</td>
