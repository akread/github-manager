import chalk from 'chalk';
import path from 'node:path';
import { readdir } from 'node:fs/promises';
import { execFile } from 'node:child_process';
import util from 'node:util';

const execp = util.promisify(execFile);

async function fetchGithubApi(path, { domain, token }) {
  if (token === 'cli') {
    const response = await execp('gh', ['api', path]);
    return JSON.parse(response.stdout);
  }

  const response = await fetch(
    `https://api.${domain}/${
      path.startsWith('/') ? path.replace('/', '') : path
    }`,
    {
      headers: {
        accept: 'application/vnd.github+json',
        authorization: `Bearer ${token}`,
        'X-GitHub-Api-Version': '2022-11-28',
      },
    },
  );

  return response.json();
}

export function createReviewsPlugin() {
  return {
    name: 'reviews',
    dependencies: ['config'],
    async initialize(program, context) {
      const { jsonStorage, config } = context.plugins;

      function parseRepo(input) {
        const urlMatch = input.match(
          /https:\/\/(?<domain>[^/]+)\/(?<owner>[^/]+)\/(?<name>[^/]+?)\/?$/,
        );
        if (urlMatch) {
          return {
            domain: urlMatch.groups.domain,
            repo: `${urlMatch.groups.owner}/${urlMatch.groups.name}`,
          };
        }
        const slashMatch = input.match(/^(?<owner>[^/]+)\/(?<name>[^/]+)$/);
        if (slashMatch) {
          return {
            domain: 'github.com',
            repo: `${slashMatch.groups.owner}/${slashMatch.groups.name}`,
          };
        }
        return null;
      }

      async function getRepoFile({ domain, repo }) {
        return jsonStorage.create(
          `repo-${encodeURIComponent(`${domain}/${repo}`)}`,
          {},
          { filepath: 'repos' },
        );
      }

      const reviews = program
        .command('reviews')
        .description('watch repositories for review requests');

      reviews
        .command('subscribe')
        .argument('<repo>', 'repository (owner/name or URL)')
        .action(async (input) => {
          const meta = parseRepo(input);
          if (!meta) {
            console.error('Bad repo');
            process.exit(1);
          }

          const file = await getRepoFile(meta);
          const data = {
            repo: meta.repo,
            domain: meta.domain,
            seen: [],
          };
          await file.write(data);

          console.log();
          console.log(chalk.italic.green('Subscribed'));
          console.log();
          console.log(`domain: ${data.domain}`);
          console.log(`repo: ${data.repo}`);
        });

      reviews
        .command('unsubscribe')
        .argument('<repo>', 'repository (owner/name or URL)')
        .action(async (input) => {
          const meta = parseRepo(input);
          if (!meta) {
            console.error('Bad repo');
            process.exit(1);
          }

          const file = await getRepoFile(meta);
          await file.delete();

          console.log();
          console.log(chalk.italic.green('Unsubscribed'));
          console.log();
          console.log(`domain: ${meta.domain}`);
          console.log(`repo: ${meta.repo}`);
        });

      async function repoFiles() {
        const dir = path.resolve(context.appDir, 'repos');
        try {
          const files = await readdir(dir);
          return Promise.all(
            files.map(async (filename) => {
              const file = await jsonStorage.create(
                filename.replace('.json', ''),
                {},
                { filepath: 'repos' },
              );
              const repo = await file.read();
              return { file, repo };
            }),
          );
        } catch {
          return [];
        }
      }

      reviews.command('list').action(async () => {
        const files = await repoFiles();

        for (const { repo } of files) {
          console.log();
          console.log(repo.repo);
          console.log(chalk.dim(`https://${repo.domain}/${repo.repo}`));
        }
      });

      async function loadReviews() {
        const files = await repoFiles();

        return Promise.all(
          files.map(async ({ file, repo }) => {
            const domainConfig = config[repo.domain];

            if (!domainConfig) {
              console.error('No config for domain', repo.domain);
              process.exit(1);
            }

            const apiConfig = {
              domain: repo.domain,
              token: domainConfig.token,
            };

            const query = encodeURIComponent(
              `is:pr is:open review-requested:${domainConfig.username} repo:${repo.repo}`,
            );

            const result = await fetchGithubApi(
              `/search/issues?q=${query}`,
              apiConfig,
            );

            const seen = repo.seen ?? [];

            const pulls = (result.items ?? []).map((item) => ({
              number: item.number,
              title: item.title,
              url: item.html_url,
              author: item.user.login,
              draft: item.draft,
              isNew: !seen.includes(item.number),
            }));

            return {
              file,
              ...repo,
              pulls,
            };
          }),
        );
      }

      async function updateReviews(results) {
        return Promise.all(
          results.map(async (result) => {
            await result.file.update({
              seen: result.pulls.map((p) => p.number),
            });
            return result;
          }),
        );
      }

      function displayReviews(results, options) {
        const totalPulls = results.reduce((n, r) => n + r.pulls.length, 0);
        const newPulls = results.reduce(
          (n, r) => n + r.pulls.filter((p) => p.isNew).length,
          0,
        );

        const highlight = newPulls ? chalk.bold : chalk.dim;

        console.log();
        console.log(
          highlight(
            `${newPulls} new review request(s) across ${results.length} repo(s) (${totalPulls} total pending)`,
          ),
        );

        for (const result of results) {
          const displayPulls = options.expanded
            ? result.pulls
            : result.pulls.filter((p) => p.isNew);

          if (!displayPulls.length && !options.expanded) {
            continue;
          }

          console.log();
          console.log(chalk.bold(result.repo));

          if (!displayPulls.length) {
            console.log(chalk.dim('No review requests'));
            continue;
          }

          for (const pull of displayPulls) {
            const draftTag = pull.draft ? chalk.italic.dim('[DRAFT] ') : '';
            const newTag = pull.isNew ? chalk.bold.yellow('• ') : '';
            console.log(
              `  ${newTag}${draftTag}${pull.title} ${chalk.dim(
                `@${pull.author}`,
              )}`,
            );
            console.log(`  ${chalk.dim(pull.url)}`);
          }
        }
      }

      reviews
        .command('check')
        .option('-u, --update')
        .option('--expanded')
        .action(async (options) => {
          const results = await loadReviews();

          if (options.update) {
            await updateReviews(results);
          }

          displayReviews(results, options);
        });

      reviews
        .command('watch')
        .option('--expanded')
        .action(async (options) => {
          let results = [];
          let date;

          async function refresh(
            getResults = loadReviews,
            datetime = new Date(),
          ) {
            date = datetime;

            process.stdout.write('\x1Bc'); // clear screen

            console.log(chalk.dim(date.toString()));
            console.log();
            console.log('Refreshing...');

            try {
              results = await getResults();

              process.stdout.write('\x1Bc'); // clear screen
              console.log(chalk.dim(date.toString()));
              console.log();

              displayReviews(results, options);
            } catch (e) {
              console.log(e);
              console.log();
              console.log(
                chalk.bold.red('Error retrieving review requests...'),
              );
            }

            console.log('\n');
            console.log(
              chalk.dim(
                `[${chalk.bold('c')}] commit [${chalk.bold(
                  'r',
                )}] refresh [${chalk.bold('x')}] exit`,
              ),
            );
          }

          await refresh();

          const interval = setInterval(refresh, 5 * 60 * 1000);

          process.stdin.write('\u001B[?25l');

          process.stdin
            .setRawMode(true)
            .setEncoding('utf8')
            .resume()
            .on('data', async (key) => {
              if (key === '\u0003' || key === 'x') {
                clearInterval(interval);
                process.stdin.write('\u001B[?25h');
                process.exit();
              } else if (key === 'r') {
                await refresh();
                process.stdin.write('\u001B[?25l');
              } else if (key === 'c') {
                await refresh(() => updateReviews(results), date);
                process.stdin.write('\u001B[?25l');
              }
            });
        });
    },
  };
}
