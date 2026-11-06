export const UNKNOWN_VPS_KEY = 'sin-hostname';

export type VPSAgent = {
	hostname: string | null | undefined;
	labels?: Record<string, string> | null;
	status: string;
	alert_count?: number | null;
	monitoring_ignored?: boolean;
};

export type VPSGroup<T extends VPSAgent> = {
	key: string;
	name: string;
	agents: T[];
	onlineCount: number;
	offlineCount: number;
	alertCount: number;
	ignoredCount: number;
};

export function vpsKey(hostname: string | null | undefined, labels?: Record<string, string> | null): string {
	return labels?.rls_vps_id?.trim() || hostname?.trim() || UNKNOWN_VPS_KEY;
}

export function groupAgentsByVPS<T extends VPSAgent>(agents: T[]): VPSGroup<T>[] {
	const groups = new Map<string, T[]>();
	for (const agent of agents) {
		const key = vpsKey(agent.hostname, agent.labels);
		const group = groups.get(key) ?? [];
		group.push(agent);
		groups.set(key, group);
	}
	const entries = [...groups.entries()].map(([key, members]) => {
		const first = members[0];
		const name = first.labels?.rls_vps_name?.trim() || first.hostname?.trim() || 'Sin hostname';
		return { key, members, name };
	});
	const nameCounts = new Map<string, number>();
	for (const entry of entries) nameCounts.set(entry.name, (nameCounts.get(entry.name) ?? 0) + 1);
	return entries
		.map(({ key, members, name }) => {
			const monitored = members.filter((agent) => !agent.monitoring_ignored);
			return {
				key,
				// If two Docker Engines report the same hostname, append a stable
				// short engine suffix so the VPS cards remain distinguishable.
				name: nameCounts.get(name)! > 1 ? `${name} · ${key.slice(-6)}` : name,
				agents: members,
				onlineCount: monitored.filter((agent) => agent.status === 'online').length,
				offlineCount: monitored.filter((agent) => agent.status !== 'online').length,
				alertCount: monitored.reduce((sum, agent) => sum + (agent.alert_count ?? 0), 0),
				ignoredCount: members.length - monitored.length
			};
		})
		.sort((a, b) => a.name.localeCompare(b.name));
}

export function agentBelongsToVPS(agent: VPSAgent, key: string): boolean {
	return vpsKey(agent.hostname, agent.labels) === key;
}
