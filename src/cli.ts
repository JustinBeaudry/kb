#!/usr/bin/env bun
import { defineCommand, runMain } from "citty";
import { VERSION } from "./lib/constants";

const main = defineCommand({
  meta: {
    name: "cairn",
    version: VERSION,
    description: "A persistent memory plugin for Claude Code",
  },
  subCommands: {
    init: () => import("./commands/init").then((m) => m.default),
    doctor: () => import("./commands/doctor").then((m) => m.default),
    uninstall: () => import("./commands/uninstall").then((m) => m.default),
    "capture-session": () => import("./commands/capture-session").then((m) => m.default),
    summarize: () => import("./commands/summarize").then((m) => m.default),
    summaries: () => import("./commands/summaries").then((m) => m.default),
    "migrate-sessions": () => import("./commands/migrate-sessions").then((m) => m.default),
  },
});

runMain(main);
