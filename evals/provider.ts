#!/usr/bin/env bun
// Local deterministic transport fixture. This is not a language-model evaluator.
import { appendFileSync } from "node:fs";

const input = await Bun.stdin.text();
const log = process.env.KB_EVAL_PROVIDER_LOG;
if (!log) throw new Error("KB_EVAL_PROVIDER_LOG is required");
appendFileSync(log, JSON.stringify({ args: process.argv.slice(2), input }) + "\n");

switch (process.env.KB_EVAL_PROVIDER_MODE) {
  case "empty": break;
  case "invalid": console.log("No structured summary was produced."); break;
  case "fail": console.error("fixture provider intentional failure"); process.exitCode = 3; break;
  default:
    console.log("## Summary\n\nDeterministic fixture provider received the input.\n\n## Extraction Candidates\n\nNone.");
}
