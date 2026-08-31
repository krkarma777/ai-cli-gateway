#!/usr/bin/env node
import { main } from "../lib/launcher.js";

await main(process.argv.slice(2));
