import chalk from 'chalk';
import path from 'node:path';
import { readdir } from 'node:fs/promises';
import { execFile } from 'node:child_process';
import util from 'node:util';

const execp = util.promisify(execFile);

async function fetchGithubApi(path, { domain }) {
  const response = await execp('gh', ['api', '--hostname', domain, path]);
  return JSON.parse(response.stdout);
}

async function fetchUsername(domain) {
  const response = await execp('gh', [
    'api',
    'user',
    '-q',
    '.login',
    '--hostname',
    domain,
  ]);
  return response.stdout.trim();
}

async function fetchRequiredChecks(data) {
  try {
    const response = await execp('gh', [
      'pr',
      'checks',
      data.id,
      '--repo',
      `${data.domain}/${data.repo}`,
      '--required',
      '--json',
      'state',
    ]);
    return JSON.parse(response.stdout);
  } catch (e) {
    if (
      e.stderr.includes('no checks reported') ||
      e.stderr.includes('no required checks reported')
    ) {
      return [];
    }

    throw e;
  }
}

export function createSubscribePlugin() {
  return {
    name: 'subscribe',
    dependencies: ['config'],
    async initialize(program, context) {
      const { jsonStorage, config } = context.plugins;

      async function getPullMetaFromUrl(url) {
        const match = url.match(
          /https:\/\/(?<domain>.*?)\/(?<repo>.*)\/pull\/(?<id>[0-9]*)/,
        );

        if (!match) {
          return;
        }

        const safeUrl = `https://${match.groups.domain}/${match.groups.repo}/pull/${match.groups.id}`;

        const pullFile = await jsonStorage.create(
          `pull-${encodeURIComponent(safeUrl)}`,
          {},
          { filepath: 'pulls' },
        );

        return [safeUrl, match.groups, pullFile];
      }

      async function subscribe(url) {
        const pullMeta = await getPullMetaFromUrl(url);

        if (!pullMeta) {
          console.error('Bad URL');
          process.exit(1);
        }

        const [safeUrl, meta, pullFile] = pullMeta;

        const data = {
          url: safeUrl,
          id: meta.id,
          domain: meta.domain,
          repo: meta.repo,
          since: new Date().toISOString(),
        };

        await pullFile.write(data);

        console.log();
        console.log(chalk.italic.green('Subscribed'));
        console.log();
        console.log(`url: ${data.url}`);
        console.log(`domain: ${data.domain}`);
        console.log(`repo: ${data.repo}`);
        console.log(`id: ${data.id}`);
      }

      program
        .command('subscribe')
        .argument('url', 'pull request url to subscribe to')
        .action(subscribe);

      program
        .command('unsubscribe')
        .argument('url', 'pull request url to unsubscribe to')
        .action(async (url) => {
          const pullMeta = await getPullMetaFromUrl(url);

          if (!pullMeta) {
            console.error('Bad URL');
            process.exit(1);
          }

          const [safeUrl, meta, pullFile] = pullMeta;

          await pullFile.delete();

          console.log();
          console.log(chalk.italic.green('Unsubscribed'));
          console.log();
          console.log(`url: ${safeUrl}`);
          console.log(`domain: ${meta.domain}`);
          console.log(`repo: ${meta.repo}`);
          console.log(`id: ${meta.id}`);
        });

      async function pullFiles() {
        const files = await readdir(path.resolve(context.appDir, 'pulls'));

        return Promise.all(
          files.map(async (filename) => {
            const pullFile = await jsonStorage.create(
              filename.replace('.json', ''),
              {},
              { filepath: 'pulls' },
            );

            const pull = await pullFile.read();

            return { pullFile, pull };
          }),
        );
      }

      program.command('list').action(async () => {
        const files = await pullFiles();

        for (const { pull } of files) {
          console.log();
          console.log(pull.repo + ' ' + pull.id);
          console.log(chalk.dim(pull.url));
        }
      });

      async function loadPulls() {
        const files = await pullFiles();

        return Promise.all(
          files.map(async ({ pullFile, pull }) => {
            const domainConfig = config[pull.domain];

            if (!domainConfig) {
              console.error('No config for domain', pull.domain);
              process.exit(1);
            }

            const apiConfig = {
              domain: pull.domain,
            };

            const [
              pullRequest,
              comments,
              reviewComments,
              requestReviewers,
              reviews,
              checks,
              username,
            ] = await Promise.all([
              ...[
                `/repos/${pull.repo}/pulls/${pull.id}`,
                `/repos/${pull.repo}/issues/${pull.id}/comments?since=${pull.since}`,
                `/repos/${pull.repo}/pulls/${pull.id}/comments?since=${pull.since}`,
                `/repos/${pull.repo}/pulls/${pull.id}/requested_reviewers`,
                `/repos/${pull.repo}/pulls/${pull.id}/reviews`,
              ].map((route) => fetchGithubApi(route, apiConfig)),
              fetchRequiredChecks(pull),
              fetchUsername(pull.domain),
            ]);

            const since = new Date();

            const excludedUsernames = domainConfig.excludedUsernames ?? [];

            // - [].user.login
            // - [].user.type === 'User'
            const commentFilterPredicate = (comment) =>
              comment.user.login !== username &&
              comment.user.type === 'User' &&
              !excludedUsernames.includes(comment.user.login);

            const approvals = reviews.filter(
              (review) => review.state === 'APPROVED',
            );
            const changesRequested = reviews.filter(
              (review) =>
                review.state === 'CHANGES_REQUESTED' &&
                !approvals.find(
                  (approval) =>
                    approval.user.login === review.user.login &&
                    new Date(review.submitted_at) <
                      new Date(approval.submitted_at),
                ),
            );

            return {
              file: pullFile,
              ...pull,
              title: pullRequest.title,
              author: pullRequest.user.login,
              ours: username === pullRequest.user.login,
              state: pullRequest.state,
              merged: pullRequest.merged,
              mergedCommit: pullRequest.merge_commit_sha,
              draft: pullRequest.draft,
              since,
              comments: comments.filter(commentFilterPredicate),
              reviewComments: reviewComments.filter(commentFilterPredicate),
              // - users[].login
              requestReviewers: requestReviewers.users.filter(
                (user) => user.login === username,
              ),
              approvals,
              newApprovals: approvals.filter(
                (approval) =>
                  new Date(approval.submitted_at) > new Date(pull.since),
              ),
              changesRequested,
              newChangesRequested: changesRequested.filter(
                (cr) => new Date(cr.submitted_at) > new Date(pull.since),
              ),
              checkState: checks?.reduce((accum, next) => {
                if (accum === 'FAILURE') {
                  return accum;
                }

                if (accum === 'PENDING') {
                  return accum;
                }

                return next.state;
              }, ''),
            };
          }),
        );
      }

      async function updatePulls(pulls) {
        return Promise.all(
          pulls.map(async (pull) => {
            let unsubscribed = false;

            if (pull.state === 'closed') {
              await pull.file.delete();
              unsubscribed = true;
            } else {
              await pull.file.update({ since: pull.since.toISOString() });
            }

            return { ...pull, unsubscribed };
          }),
        );
      }

      async function displayPulls(pulls, options) {
        const updates = pulls.filter(
          (result) =>
            result.unsubscribed ||
            result.state !== 'open' ||
            result.comments.length ||
            result.reviewComments.length ||
            result.requestReviewers.length ||
            result.newApprovals.length ||
            result.newChangesRequested.length,
        );

        const hasUpdates = !!updates.length;
        const highlight = hasUpdates ? chalk.bold : chalk.red;

        console.log();
        console.log(
          highlight(
            `${updates.length} of ${pulls.length} watched pull requests contain updates`,
          ),
        );

        const display = options.expanded ? pulls : updates;

        function consoleComment(comment) {
          const formattedBody = comment.body
            .replace(/\n/g, ' ')
            .replace(/\r/g, '')
            .trim();

          console.log(
            `@${comment.user.login}: ${formattedBody.slice(0, 100).trim()}${
              formattedBody.length > 100 ? '...' : ''
            }`,
          );
          console.log(chalk.dim(comment.html_url));
        }

        for (const u of display) {
          function getStateLabel() {
            if (u.merged) {
              return chalk.italic.magenta('[MERGED]');
            }

            if (u.state === 'closed') {
              return chalk.italic.red('[CLOSED]');
            }

            if (u.draft) {
              return chalk.italic.dim('[DRAFT]');
            }

            return chalk.italic.green('[OPEN]');
          }

          const authorTag = `@${u.author}`;

          console.log();
          console.log(
            `${getStateLabel()} ${u.title} ${
              u.ours ? chalk.underline.dim(authorTag) : chalk.dim(authorTag)
            }`,
          );

          if (u.merged) {
            console.log(chalk.magenta(u.mergedCommit));
          }

          console.log(`${chalk.dim(u.url)}`);

          if (u.comments.length) {
            const message = `${u.comments.length} new comment(s)`;
            console.log(chalk.italic.yellow(message));

            if (options.comments) {
              for (const comment of u.comments) {
                consoleComment(comment);
              }
            }
          }

          if (u.reviewComments.length) {
            const message = `${u.reviewComments.length} new review comment(s)`;
            console.log(chalk.italic.yellow(message));

            if (options.comments) {
              for (const comment of u.reviewComments) {
                consoleComment(comment);
              }
            }
          }

          if (u.requestReviewers.length) {
            const message = 'New review requested';
            console.log(chalk.italic.yellow(message));
          }

          if (u.approvals.length) {
            const message = `${u.approvals.length} approval(s)`;
            if (u.newApprovals.length) {
              console.log(chalk.italic.yellow(message));
            } else {
              console.log(chalk.italic.dim(message));
            }
          }

          if (u.changesRequested.length) {
            const message = `${u.changesRequested.length} changes requested`;
            if (u.newChangesRequested.length) {
              console.log(chalk.italic.red(message));
            } else {
              console.log(chalk.italic.dim(message));
            }
          }

          if (u.checkState && !u.merged) {
            if (u.checkState === 'FAILURE') {
              console.log(
                chalk.italic.rgb(242, 106, 27)('Required checks failed'),
              );
            } else if (u.checkState === 'PENDING') {
              console.log(chalk.italic.green('Requred checks running'));
            } else {
              console.log(chalk.italic.dim('Requred checks passed'));
            }
          }

          if (u.unsubscribed) {
            const message = 'Unsubscribed';
            console.log(chalk.italic.red(message));
          }
        }
      }

      program
        .command('check')
        .option('-u, --update')
        .option('--expanded')
        .option('--comments')
        .action(async (options) => {
          const pulls = await loadPulls();
          const results = options.update ? await updatePulls(pulls) : pulls;

          await displayPulls(results, options);
        });

      program
        .command('watch')
        .option('--expanded')
        .option('--comments')
        .action(async (options) => {
          let pulls = [];
          let date;

          async function refresh(getPulls = loadPulls, datetime = new Date()) {
            date = datetime;

            process.stdout.write('\x1Bc'); // clear screen

            console.log(chalk.dim(date.toString()));
            console.log();
            console.log('Refreshing...');

            try {
              pulls = await getPulls();

              process.stdout.write('\x1Bc'); // clear screen
              console.log(chalk.dim(date.toString()));
              console.log();

              await displayPulls(pulls, options);
            } catch (e) {
              console.log(e);
              console.log();
              console.log(chalk.bold.red('Error retrieving pull requests...'));
            }

            console.log('\n');
            console.log(
              chalk.dim(
                `[${chalk.bold('s')}] subscribe [${chalk.bold(
                  'c',
                )}] commit [${chalk.bold('r')}] refresh [${chalk.bold(
                  'x',
                )}] exit`,
              ),
            );
          }

          await refresh();

          // Watch interval
          const interval = setInterval(refresh, 5 * 60 * 1000);

          let mode = 'shortcuts';

          process.stdin.write('\u001B[?25l');

          process.stdin
            .setRawMode(true)
            .setEncoding('utf8')
            .resume()
            .on('data', async (key) => {
              if (key === '\u0003') {
                clearInterval(interval);
                process.stdin.write('\u001B[?25h');
                process.exit();
              }
              if (mode === 'shortcuts') {
                if (key === 'x') {
                  clearInterval(interval);
                  process.stdin.write('\u001B[?25h');
                  process.exit();
                } else if (key === 'r') {
                  await refresh();
                  process.stdin.write('\u001B[?25l');
                } else if (key === 'c') {
                  await refresh(() => updatePulls(pulls), date);
                  process.stdin.write('\u001B[?25l');
                } else if (key === 's') {
                  mode = 'subscribe';
                  process.stdin.setRawMode(false);
                  process.stdin.write('\u001B[?25h');
                  console.log('');
                  console.log('Input a URL to subscribe to:');
                }
              } else {
                const url = key.trim();
                if (url) {
                  await subscribe(url);
                  await new Promise((res) => setTimeout(res, 3000));
                }
                await refresh();
                process.stdin.setRawMode(true);
                process.stdin.write('\u001B[?25l');
                mode = 'shortcuts';
              }
            });
        });

      return { subscribe };
    },
  };
}
