import { describe, expect, it } from 'bun:test';
import { agentBelongsToVPS, groupAgentsByVPS } from './vps';

describe('VPS grouping', () => {
	it('groups agents by normalized hostname and aggregates fleet status', () => {
		const groups = groupAgentsByVPS([
			{ id: 1, hostname: 'node-a', status: 'online', alert_count: 2 },
			{ id: 2, hostname: 'node-a', status: 'offline', alert_count: 1 },
			{ id: 3, hostname: 'node-b', status: 'online', alert_count: 0 }
		]);
		expect(groups.map((group) => group.name)).toEqual(['node-a', 'node-b']);
		expect(groups[0]).toMatchObject({ onlineCount: 1, offlineCount: 1, alertCount: 3 });
		expect(agentBelongsToVPS({ hostname: 'node-a', status: 'online' }, 'node-a')).toBe(true);
	});

	it('keeps missing hostnames in a visible fallback group', () => {
		const groups = groupAgentsByVPS([{ hostname: '', status: 'offline' }]);
		expect(groups[0].name).toBe('Sin hostname');
		expect(agentBelongsToVPS({ hostname: null, status: 'offline' }, groups[0].key)).toBe(true);
	});

	it('separates VPSes that share a hostname using the Docker Engine identity', () => {
		const agents = [
			{ hostname: 'ubuntu', labels: { rls_vps_id: 'daemon-AAAAAA', rls_vps_name: 'ubuntu' }, status: 'online', alert_count: 0 },
			{ hostname: 'ubuntu', labels: { rls_vps_id: 'daemon-AAAAAA', rls_vps_name: 'ubuntu' }, status: 'offline', alert_count: 1 },
			{ hostname: 'ubuntu', labels: { rls_vps_id: 'daemon-BBBBBB', rls_vps_name: 'ubuntu' }, status: 'online', alert_count: 0 }
		];
		const groups = groupAgentsByVPS(agents);
		expect(groups).toHaveLength(2);
		expect(groups.map((group) => group.name)).toEqual(['ubuntu · AAAAAA', 'ubuntu · BBBBBB']);
		expect(groups[0]).toMatchObject({ agents: [agents[0], agents[1]], onlineCount: 1, offlineCount: 1, alertCount: 1 });
		expect(agentBelongsToVPS(agents[2], 'daemon-BBBBBB')).toBe(true);
	});

	it('keeps ignored containers in the VPS inventory but excludes them from monitoring counters', () => {
		const groups = groupAgentsByVPS([
			{ hostname: 'node-a', labels: { rls_vps_id: 'engine-a' }, status: 'online', alert_count: 2 },
			{ hostname: 'node-a', labels: { rls_vps_id: 'engine-a' }, status: 'offline', alert_count: 1, monitoring_ignored: true }
		]);
		expect(groups[0]).toMatchObject({ agents: expect.any(Array), onlineCount: 1, offlineCount: 0, alertCount: 2, ignoredCount: 1 });
	});
});
