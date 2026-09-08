// Upserts `item` into `items` keyed by `id`: appends when the id is not
// present (create), replaces the matching entry in place otherwise (edit).
// Pure — always returns a new array, never mutates the input.
export function upsertById<T extends { id: number }>(items: T[], item: T): T[] {
	const index = items.findIndex((existing) => existing.id === item.id);
	if (index === -1) {
		return [...items, item];
	}
	return items.map((existing, i) => (i === index ? item : existing));
}