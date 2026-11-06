<script lang="ts">
	import { goto } from '$app/navigation';
	import { toast } from 'svelte-sonner';
	import { auth } from '#lib/stores/auth.svelte.js';
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
	let confirm = $state('');
	let submitting = $state(false);

	async function onSubmit(e: SubmitEvent) {
		e.preventDefault();
		if (submitting) return;
		if (password !== confirm) {
			toast.error('Las contraseñas no coinciden');
			return;
		}
		submitting = true;
		try {
			const result = await auth.bootstrapUser(username, password);
			if (result.ok) {
				toast.success('Primer admin creado', {
					description: 'Inicia sesión con las credenciales recién creadas.'
				});
				goto('/login');
			} else if (result.status === 403) {
				toast.error('Ya existen usuarios', {
					description: 'El bootstrap solo está disponible antes de crear el primer admin.'
				});
			} else {
				toast.error(`Error del backend (${result.status})`);
			}
		} catch {
			toast.error('Backend no disponible', {
				description: 'No se pudo contactar a la API. Reintenta más tarde.'
			});
		} finally {
			submitting = false;
		}
	}
</script>

<svelte:head>
	<title>Primer arranque — RLS Legacy</title>
</svelte:head>

<div class="flex min-h-svh items-center justify-center px-4 sm:px-6">
	<Card class="auth-card max-w-[400px] w-full">
		<CardHeader>
			<CardTitle>Primer arranque</CardTitle>
			<CardDescription>
				Crea el primer usuario admin. Este formulario solo funciona si aún no existen usuarios.
			</CardDescription>
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
						autocomplete="new-password"
						placeholder="••••••••"
						required
						bind:value={password}
					/>
				</div>
				<div class="grid gap-2">
					<Label for="confirm">Confirmar contraseña</Label>
					<Input
						id="confirm"
						name="confirm"
						type="password"
						autocomplete="new-password"
						placeholder="repite la contraseña"
						required
						bind:value={confirm}
					/>
				</div>
				<Button type="submit" class="w-full" disabled={submitting}>
					{submitting ? 'Creando…' : 'Crear usuario admin'}
				</Button>
			</form>
		</CardContent>
	</Card>
</div>
