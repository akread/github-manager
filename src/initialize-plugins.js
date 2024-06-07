import chalk from 'chalk';

export async function initializePlugins({ plugins, program, context }) {
  for (const plugin of plugins) {
    if (plugin.dependencies) {
      for (const dep of plugin.dependencies) {
        if (!context.plugins.hasOwnProperty(dep)) {
          console.error(
            chalk.bold.red(
              `Plugin Error: "${plugin.name}" has an unmet dependency of "${dep}".`,
            ),
          );
          process.exit(1);
        }
      }
    }

    context.plugins[plugin.name] = await plugin.initialize(program, context);
  }
}
