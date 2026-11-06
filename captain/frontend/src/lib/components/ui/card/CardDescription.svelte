<script lang="ts">
	import type { HTMLAttributes } from 'svelte/elements';

	let { class: className = '', children, ...rest }: HTMLAttributes<HTMLParagraphElement> = $props();
</script>

<p class="text-sm text-muted-foreground {className}" {...rest}>
	{@render children?.()}
</p>
