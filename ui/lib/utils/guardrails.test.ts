import { describe, expect, it } from "vitest";
import { upsertById } from "./guardrails";

describe("upsertById", () => {
	it("appends when id is new (create mode)", () => {
		const existing = [
			{ id: 1, name: "a" },
			{ id: 2, name: "b" },
		];
		const next = upsertById(existing, { id: 3, name: "c" });
		expect(next).toHaveLength(3);
		expect(next[2]).toEqual({ id: 3, name: "c" });
	});

	it("replaces in place when id exists (edit mode)", () => {
		const existing = [
			{ id: 1, name: "a" },
			{ id: 2, name: "b" },
		];
		const next = upsertById(existing, { id: 2, name: "b2" });
		expect(next).toHaveLength(2);
		expect(next[1]).toEqual({ id: 2, name: "b2" });
	});

	it("does not mutate the input array", () => {
		const existing = [{ id: 1, name: "a" }];
		upsertById(existing, { id: 1, name: "a2" });
		expect(existing).toEqual([{ id: 1, name: "a" }]);
	});

	it("appends to an empty list", () => {
		expect(upsertById([], { id: 1, name: "a" })).toEqual([{ id: 1, name: "a" }]);
	});
});