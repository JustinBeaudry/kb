#!/usr/bin/env bun
import { defineCommand, runMain } from "citty";

const main = defineCommand({
  meta: {
    name: "cairn",
    version: "0.1.0",
    description: "A persistent memory plugin for Claude Code",
  },
  subCommands: {
    init: () => import("./commands/init").then((m) => m.default),
    uninstall: () => import("./commands/uninstall").then((m) => m.default),
  },
});

runMain(main);
