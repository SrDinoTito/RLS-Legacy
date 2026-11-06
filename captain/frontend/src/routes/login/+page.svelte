<script lang="ts">
	import { goto } from '$app/navigation';
	import { toast } from 'svelte-sonner';
	import { auth } from '#lib/stores/auth.svelte.js';
	import { refreshAgents, sse } from '#lib/stores/sse.svelte.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import {
		Card,
		CardContent,
		CardDescription,
		CardHeader,
		CardTitle
	} from '#lib/components/ui/card/index.js';

	let username = $state('');
	let password = $state('');
	let submitting = $state(false);

	async function onSubmit(e: SubmitEvent) {
		e.preventDefault();
		if (submitting) return;
		submitting = true;
		try {
			const result = await auth.login(username, password);
			if (result.ok) {
				// The root layout survives SPA navigation from /login. Start the
				// stream and fetch the fleet explicitly before navigating so the
				// dashboard never renders its empty-state until a manual refresh.
				sse.connect();
				await refreshAgents();
				await goto('/');
			} else if (result.status === 401) {
				toast.error('Credenciales inválidas');
			} else if (result.status === 403) {
				toast.error('Ya existen usuarios', {
					description: 'Inicia sesión con tu usuario o usa /bootstrap si es el primer arranque.'
				});
			} else {
				toast.error(`Error del backend (${result.status})`);
			}
		} catch {
			// fetch lanza TypeError en fallo de red / backend caído
			toast.error('Backend no disponible', {
				description: 'No se pudo contactar a la API. Reintenta más tarde.'
			});
		} finally {
			submitting = false;
		}
	}
</script>

<svelte:head>
	<title>Iniciar sesión — RLS Legacy</title>
</svelte:head>

<!-- Card centrada max-w 400px, desktop-first -->
<div class="flex min-h-svh items-center justify-center px-4 sm:px-6">
	<Card class="auth-card max-w-[400px] w-full">
		<CardHeader>
			<CardTitle>Panel de control</CardTitle>
			<CardDescription>Inicia sesión en la consola central de RLS Legacy.</CardDescription>
		</CardHeader>
		<CardContent>
			<form class="space-y-5" onsubmit={onSubmit}>
				<div class="grid gap-2">
					<Label for="username">Usuario</Label>
					<Input
						id="username"
						name="username"
						autocomplete="username"
						placeholder="usuario"
						required
						bind:value={username}
					/>
				</div>
				<div class="grid gap-2">
					<Label for="password">Contraseña</Label>
					<Input
						id="password"
						name="password"
						type="password"
						autocomplete="current-password"
						placeholder="••••••••"
						required
						bind:value={password}
					/>
				</div>
				<Button type="submit" class="w-full" disabled={submitting}>
					{submitting ? 'Autenticando…' : 'Iniciar sesión'}
				</Button>
			</form>
		</CardContent>
	</Card>
</div>
