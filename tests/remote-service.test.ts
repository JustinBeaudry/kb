import { expect, test } from "bun:test";
import { RemoteService } from "../src/services/remote.service";
import { renderRemoteFrame } from "../src/lib/remote-envelope";

test("complete frame reserves later-page overhead and pins the effective server budget", async () => {
  const budgets: number[] = [];
  const gateway = {
    orient: async (input: { budget_bytes: number; continuation?: string }) => {
      budgets.push(input.budget_bytes);
      const value = { schema_version: "3" as const, chunks: [], truncated: true, visited: 1, no_results: false, continuation: "signed.cursor", action: "", truncation_reason: "wire_budget" as const };
      // The first page is sparse; the next consumes every byte the server may
      // emit. This detects exposing a cursor before reserving frame overhead.
      if (input.continuation) {
        const room = Math.max(0, input.budget_bytes - 1 - Buffer.byteLength(JSON.stringify(value)));
        value.action = "猫".repeat(Math.floor(room / 3));
      }
      return { ok: true as const, value };
    },
    search: async () => ({ ok: false as const, error: { code: "unavailable" as const } }),
    fetch: async () => ({ ok: false as const, error: { code: "unavailable" as const } }),
    status: async () => ({ ok: false as const, error: { code: "unavailable" as const } }),
  };
  const service = new RemoteService(gateway);
  const input = { scope: { project_id: "p" }, budget_bytes: 250 };
  const first = await service.orient(input);
  expect(first.ok).toBe(true);
  if (!first.ok) return;
  expect(budgets.length).toBeGreaterThan(1);
  const effective = budgets[budgets.length - 1]!;
  budgets.length = 0;
  const second = await service.orient({ ...input, continuation: first.value.continuation });
  expect(second.ok).toBe(true);
  if (second.ok) expect(Buffer.byteLength(renderRemoteFrame(second.value))).toBeLessThanOrEqual(input.budget_bytes);
  expect(budgets).toEqual([effective]);
  expect(await service.orient({ ...input, budget_bytes: 251, continuation: first.value.continuation })).toMatchObject({ error: { code: "stale" } });
  expect(budgets).toEqual([effective]);
});
