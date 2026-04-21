#!/usr/bin/env node

import { homedir } from 'node:os';
import path from 'node:path';
import { Command } from 'commander';

import { initializeContext } from './initialize-context.js';
import { initializePlugins } from './initialize-plugins.js';
import { createJsonStoragePlugin } from './plugins/json-storage.js';
import { createConfigPlugin } from './plugins/config.js';
import { createSubscribePlugin } from './plugins/subscribe.js';
import { createReviewsPlugin } from './plugins/reviews.js';

const USER_DIR = homedir();
const APP_DIR = path.resolve(USER_DIR, '.github-manager');

const plugins = [
  createJsonStoragePlugin(),
  createConfigPlugin({
    editor: 'vim',
  }),
  createSubscribePlugin(),
  createReviewsPlugin(),
];

async function main() {
  const program = new Command();

  program
    .name('github-manager')
    .description('manage github pull request notifications')
    .version('1.0.0');

  const context = await initializeContext(APP_DIR);

  await initializePlugins({ plugins, program, context });

  program.parse();
}

main();
