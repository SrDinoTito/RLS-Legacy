import { resolve, sep } from 'node:path';

const port = Number(Bun.env.PORT ?? 3008);
const host = Bun.env.HOST ?? '0.0.0.0';
const backendURL = new URL(Bun.env.BACKEND_URL ?? 'http://backend:8080');
const staticRoot = resolve(import.meta.dir, 'build');
const indexPath = resolve(staticRoot, 'index.html');

const hopByHopHeaders = new Set([
	'connection',
	'keep-alive',
	'proxy-authenticate',
	'proxy-authorization',
	'te',
	'trailer',
	'transfer-encoding',
	'upgrade'
]);

function copyEndToEndHeaders(source: Headers, { removeContentLength = false } = {}): Headers {
	const headers = new Headers();
	for (const [name, value] of source) {
		const lowerName = name.toLowerCase();
		if (hopByHopHeaders.has(lowerName) || lowerName === 'host') continue;
		if (removeContentLength && lowerName === 'content-length') continue;
		headers.append(name, value);
	}
	return headers;
}

function staticPath(pathname: string): string | null {
	let decodedPath: string;
	try {
		decodedPath = decodeURIComponent(pathname);
	} catch {
		return null;
	}

	const candidate = resolve(staticRoot, `.${decodedPath}`);
	if (candidate !== staticRoot && !candidate.startsWith(`${staticRoot}${sep}`)) return null;
	return candidate;
}

async function serveStatic(request: Request): Promise<Response> {
	const filePath = staticPath(new URL(request.url).pathname);
	if (!filePath) return new Response('Bad Request', { status: 400 });

	const requestedFile = Bun.file(filePath);
	const file = (await requestedFile.exists()) ? requestedFile : Bun.file(indexPath);
	const headers = new Headers({
		'Cache-Control': filePath.includes(`${sep}_app${sep}immutable${sep}`)
			? 'public, max-age=31536000, immutable'
			: 'no-cache',
		'X-Content-Type-Options': 'nosniff'
	});

	return new Response(request.method === 'HEAD' ? null : file, { headers });
}

async function proxyToBackend(request: Request, server: Bun.Server): Promise<Response> {
	const incomingURL = new URL(request.url);
	if (incomingURL.pathname === '/api/v1/stream') server.timeout(request, 0);

	const targetURL = new URL(`${incomingURL.pathname}${incomingURL.search}`, backendURL);
	const headers = copyEndToEndHeaders(request.headers, { removeContentLength: true });
	const forwardedHost = request.headers.get('x-forwarded-host') ?? request.headers.get('host');
	const forwardedProto = request.headers.get('x-forwarded-proto') ?? incomingURL.protocol.slice(0, -1);
	if (forwardedHost) headers.set('x-forwarded-host', forwardedHost);
	headers.set('x-forwarded-proto', forwardedProto);

	const method = request.method.toUpperCase();
	const body = method === 'GET' || method === 'HEAD' ? undefined : request.body ?? undefined;

	try {
		const upstream = await fetch(targetURL, {
			method,
			headers,
			body,
			redirect: 'manual',
			signal: request.signal
		});
		const responseHeaders = copyEndToEndHeaders(upstream.headers);
		return new Response(upstream.body, {
			status: upstream.status,
			statusText: upstream.statusText,
			headers: responseHeaders
		});
	} catch (error) {
		console.error('Backend proxy request failed:', error);
		return Response.json({ error: 'backend unavailable' }, { status: 502 });
	}
}

const server = Bun.serve({
	hostname: host,
	port,
	idleTimeout: 30,
	async fetch(request, server) {
		const { pathname } = new URL(request.url);
		if (pathname === '/healthz') return new Response('ok');
		if (pathname === '/api' || pathname.startsWith('/api/')) return proxyToBackend(request, server);
		if (pathname === '/script.sh' || pathname === '/downloads' || pathname.startsWith('/downloads/')) {
			return proxyToBackend(request, server);
		}
		return serveStatic(request);
	}
});

console.log(`RLS Legacy frontend listening on ${server.hostname}:${server.port}`);
