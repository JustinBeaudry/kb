import { expect, test } from "bun:test";
import { F4Uploader } from "../src/capture/uploader";
import { MAX_SPOOL_BYTES } from "../src/capture/types";
import { CaptureService } from "../src/services/capture.service";
import { event, spoolFixture } from "./fixtures/capture";

for (const scenario of [
  {
    name: "32 GiB remote quota queues within the default local cap",
    remote: 32 * 1024 ** 3,
    local: undefined,
    effective: MAX_SPOOL_BYTES,
    state: "queued",
  },
  {
    name: "32 GiB remote quota still respects a restrictive local cap",
    remote: 32 * 1024 ** 3,
    local: 1,
    effective: MAX_SPOOL_BYTES,
    state: "dropped",
  },
  {
    name: "smaller remote quota remains unchanged and permits fitting events",
    remote: 1024,
    local: undefined,
    effective: 1024,
    state: "queued",
  },
  {
    name: "smaller remote quota refuses events above its limit",
    remote: 1,
    local: undefined,
    effective: 1,
    state: "dropped",
  },
  {
    name: "zero remote quota remains closed",
    remote: 0,
    local: undefined,
    effective: 0,
    state: "dropped",
  },
] satisfies Array<{
  name: string;
  remote: number;
  local: number | undefined;
  effective: number;
  state: "queued" | "dropped";
}>) {
  test(scenario.name, async () => {
    const fixture = await spoolFixture(scenario.local);
    const server = Bun.serve({
      port: 0,
      hostname: "127.0.0.1",
      fetch(request) {
        expect(new URL(request.url).pathname).toBe("/v1/policies/demo");
        return Response.json({
          schema_version: "3",
          policy: {
            revision: "p1",
            paused: false,
            excluded_paths: [],
            excluded_fields: [],
            allowed_model_routes: [],
            spool_limit_bytes: scenario.remote,
          },
        });
      },
    });
    try {
      const uploader = new F4Uploader(server.url.origin, "a".repeat(64), 1);
      const service = new CaptureService(fixture.spool, uploader);

      const receipt = await service.enqueue(event());

      expect(receipt).toEqual({
        ok: true,
        value: { id: "event-1", state: scenario.state },
      });
      const queued = scenario.state === "queued";
      const batch = await fixture.spool.readBatch();
      expect(batch.ok && batch.value.events.map((item) => item.id)).toEqual(
        queued ? ["event-1"] : [],
      );
      expect(batch.ok && batch.value.gaps).toEqual(
        queued ? [] : [{ sequence: 1, reason: "quota" }],
      );
      const cached = await fixture.spool.cachedPolicy("demo");
      expect(cached.ok && cached.value?.spoolLimitBytes).toBe(
        scenario.effective,
      );
    } finally {
      await server.stop(true);
      await fixture.cleanup();
    }
  });
}

for (const quota of [-1, 0.5, Number.MAX_SAFE_INTEGER + 1, "1024", null, undefined]) {
  test(`invalid remote spool quota ${String(quota)} remains rejected`, async () => {
    const server = Bun.serve({
      port: 0,
      hostname: "127.0.0.1",
      fetch() {
        return Response.json({
          schema_version: "3",
          policy: {
            revision: "p1",
            paused: false,
            excluded_paths: [],
            excluded_fields: [],
            allowed_model_routes: [],
            spool_limit_bytes: quota,
          },
        });
      },
    });
    try {
      const result = await new F4Uploader(
        server.url.origin,
        "a".repeat(64),
        1,
      ).policy("demo");
      expect(result).toEqual({
        ok: false,
        error: { code: "policy_unavailable" },
      });
    } finally {
      await server.stop(true);
    }
  });
}
