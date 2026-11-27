import { describe, expect, it } from "bun:test";
import { normalizeShortInlineValues } from "../src/lib/argv";

describe("normalizeShortInlineValues", () => {
  it("splits a short option's inline value into its own token", () => {
    expect(normalizeShortInlineValues(["summarize", "abc", "-p=/vault"])).toEqual(["summarize", "abc", "-p", "/vault"]);
  });

  it("keeps an empty inline value and any further equals signs", () => {
    expect(normalizeShortInlineValues(["-p="])).toEqual(["-p", ""]);
    expect(normalizeShortInlineValues(["-p=a=b"])).toEqual(["-p", "a=b"]);
  });

  it("leaves long options, clustered flags and positionals unchanged", () => {
    const args = ["--vault-path=/v", "-ab", "-p", "/v", "x=y", "-"];
    expect(normalizeShortInlineValues(args)).toEqual(args);
  });

  it("only rewrites the vault-path alias, so other values that look like flags survive", () => {
    const args = ["remote", "search", "--query", "-x=foo", "-q=bar"];
    expect(normalizeShortInlineValues(args)).toEqual(args);
  });

  it("leaves the value of a separate vault-path flag alone", () => {
    expect(normalizeShortInlineValues(["-p", "-p=odd"])).toEqual(["-p", "-p=odd"]);
    expect(normalizeShortInlineValues(["--vault-path", "-p=odd"])).toEqual(["--vault-path", "-p=odd"]);
  });

  it("stops at the end-of-options terminator", () => {
    expect(normalizeShortInlineValues(["-p=/v", "--", "-q=raw"])).toEqual(["-p", "/v", "--", "-q=raw"]);
  });
});
