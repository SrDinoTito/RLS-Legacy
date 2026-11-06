import { describe, expect, it } from 'bun:test';
import { filterAgentsByTag } from './tags';

describe('filterAgentsByTag', () => {
	const agents = [
		{ name: 'api', tags: ['web', 'prod'], container_tags: ['blue'] },
		{ name: 'web-preview', tags: ['web-preview'], container_tags: null },
		{ name: 'untagged', tags: null, container_tags: ['blue'] }
	];

	it('returns all containers with an exact tag match', () => {
		expect(filterAgentsByTag(agents, 'web')).toEqual([agents[0]]);
		expect(filterAgentsByTag(agents, 'blue')).toEqual([agents[0], agents[2]]);
	});

	it('does not match substrings and handles missing profiles', () => {
		expect(filterAgentsByTag(agents, 'we')).toEqual([]);
		expect(filterAgentsByTag(agents, '')).toEqual([]);
	});
});
