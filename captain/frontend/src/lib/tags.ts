export type TaggedAgent = { tags: string[] | null; container_tags?: string[] | null };

/** Tags de token + etiquetas manuales del contenedor, sin duplicados. */
export function allAgentTags(agent: TaggedAgent): string[] {
	return [...new Set([...(agent.tags ?? []), ...(agent.container_tags ?? [])])];
}

/** Filtra las instancias que pertenecen exactamente a una tag, no por substring. */
export function filterAgentsByTag<T extends TaggedAgent>(agents: T[], tag: string): T[] {
	if (!tag) return [];
	return agents.filter((agent) => allAgentTags(agent).includes(tag));
}
