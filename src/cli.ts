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
    recall: () => import("./commands/recall").then((m) => m.default),
    get: () => import("./commands/get").then((m) => m.default),
    "list-topics": () => import("./commands/list-topics").then((m) => m.default),
    "read-raw": () => import("./commands/read-raw").then((m) => m.default),
    "read-session": () => import("./commands/read-session").then((m) => m.default),
    "capture-session": () => import("./commands/capture-session").then((m) => m.default),
    summarize: () => import("./commands/summarize").then((m) => m.default),
    summaries: () => import("./commands/summaries").then((m) => m.default),
  },
});

runMain(main);
