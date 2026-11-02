import { describe, expect, test } from "bun:test";
import { filterEvent } from "../src/capture/policy";
import type { CaptureEvent, CapturePolicy } from "../src/capture/types";
import { event, policy } from "./fixtures/capture";
describe("capture policy", () => {
  test("redacts known secrets before any disk or network write", () => {
    const secret = "跨界\nsecret🔑";
    const result = filterEvent(
      event({
        nested: [{ x: secret }],
        outputs: ["跨界\n", "secret🔑"],
        token: "unknown-token",
        multiline: "first\n" + secret + "\nlast",
      }),
      policy(),
      [secret],
    );
    expect(result.kind).toBe("accepted");
    expect(JSON.stringify(result)).not.toContain(secret);
    expect(JSON.stringify(result)).not.toContain("unknown-token");
    expect(JSON.stringify(result)).not.toContain("secret🔑");
    // The multiline secret must also be absent in its JSON-escaped form.
    expect(JSON.stringify(result)).not.toContain(
      JSON.stringify(secret).slice(1, -1),
    );
    expect(JSON.stringify(result)).not.toContain("跨界\\nsecret");
  });
  test("server and local exclusions narrow scope using slash directory boundaries", () => {
    for (const excluded of ["src", "src/", "src/file.ts"])
      expect(
        filterEvent(event(), { ...policy(), excludedPaths: [excluded] }, [])
          .kind,
      ).toBe("excluded");
    expect(
      filterEvent(event(), { ...policy(), excludedPaths: ["sr"] }, []).kind,
    ).toBe("accepted");
    expect(
      filterEvent(
        { ...event(), sourcePath: undefined },
        { ...policy(), excludedPaths: ["other"] },
        [],
      ).kind,
    ).toBe("withheld");
    expect(
      filterEvent(event(), { ...policy(), localExcludedPaths: ["src"] }, [])
        .kind,
    ).toBe("excluded");
  });
  test("missing expired paused and malformed input withhold content", () => {
    for (const p of [
      undefined,
      { ...policy(), expiresAt: 0 },
      { ...policy(), paused: true },
    ])
      expect(filterEvent(event(), p, []).kind).toBe("withheld");
    for (const path of ["/tmp/file", "../file", "a/../b", "a\\b"])
      expect(
        filterEvent({ ...event(), sourcePath: path }, policy(), []).kind,
      ).toBe("withheld");
    expect(
      filterEvent({ ...event(), id: "known-secret" }, policy(), [
        "known-secret",
      ]).kind,
    ).toBe("withheld");
  });
  test("field exclusions remove nested names and paths", () => {
    const r = filterEvent(
      event({
        safe: "ok",
        nested: { private: "gone", sensitive: "gone too" },
        array: [{ private: "also gone" }],
      }),
      { ...policy(), excludedFields: ["private", "nested.sensitive"] },
      [],
    );
    expect(JSON.stringify(r)).not.toContain("gone");
    expect(JSON.stringify(r)).toContain("ok");
  });
  test("bounded parsing rejects excessive depth and unsafe numeric sequences", () => {
    let x: CaptureEvent["content"] = "x";
    for (let i = 0; i < 40; i++) x = [x];
    expect(filterEvent(event(x), policy(), []).kind).toBe("withheld");
    expect(
      filterEvent(
        { ...event(), sequence: Number.MAX_SAFE_INTEGER + 1 },
        policy(),
        [],
      ).kind,
    ).toBe("withheld");
  });
});
test("redaction range application stays efficient at the node bound", () => {
  const secret = "ab";
  const content = Array.from({ length: 49_000 }, () => "ab");
  const started = performance.now();
  const result = filterEvent(event(content), policy(), [secret]);
  const elapsed = performance.now() - started;
  expect(result.kind).toBe("accepted");
  expect(JSON.stringify(result)).not.toContain(secret);
  expect(elapsed).toBeLessThan(1500);
});
test("adjacent and overlapping secrets across many strings are all masked", () => {
  const result = filterEvent(
    event(["xa", "bcab", "c", "zz", "abc"]),
    policy(),
    ["abc", "bca"],
  );
  expect(result.kind === "accepted" && result.event.content).toEqual([
    "x*",
    "****",
    "*",
    "zz",
    "***",
  ]);
});
