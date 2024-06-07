import { spawn } from 'node:child_process';
import { confirm } from '@inquirer/prompts';
import chalk from 'chalk';

export function createConfigPlugin(defaultConfig) {
  return {
    name: 'config',
    dependencies: ['jsonStorage'],
    async initialize(program, context) {
      const { jsonStorage } = context.plugins;

      const configFile = await jsonStorage.create('config', defaultConfig);

      const config = {
        ...defaultConfig,
        ...(await configFile.read()),
      };

      const subprogram = program.command('config').description('manage config');

      subprogram
        .command('edit')
        .description('edit config')
        .action(() => {
          spawn(config.editor, [configFile.path], {
            stdio: 'inherit',
          });
        });

      subprogram
        .command('reset')
        .description('reset config')
        .action(async () => {
          const yes = await confirm({
            message: 'Are you sure you want to reset the config?',
          });
          if (yes) {
            await configFile.delete();
            console.log(chalk.bold.green('Config reset'));
          }
        });

      return config;
    },
  };
}
