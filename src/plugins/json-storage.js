import {
  access,
  constants,
  writeFile,
  readFile,
  rm,
  mkdir,
} from 'node:fs/promises';
import path from 'node:path';

export function createJsonStoragePlugin() {
  return {
    name: 'jsonStorage',
    dependencies: [],
    async initialize(program, context) {
      return {
        async create(
          name,
          defaultContent,
          { version = 1, filepath = '' } = {},
        ) {
          const PATH = path.resolve(context.appDir, filepath);
          const FILE = path.resolve(PATH, `${name}.json`);

          try {
            await access(FILE, constants.F_OK);
          } catch {
            await mkdir(PATH, { recursive: true });
            await writeFile(
              FILE,
              JSON.stringify(
                {
                  version,
                  data: defaultContent,
                },
                undefined,
                2,
              ),
            );
          }

          return {
            path: FILE,
            async read() {
              return JSON.parse(await readFile(FILE)).data;
            },
            async write(content) {
              return writeFile(
                FILE,
                JSON.stringify({ version, data: content }, undefined, 2),
              );
            },
            async update(content) {
              const current = await this.read();

              return writeFile(
                FILE,
                JSON.stringify(
                  { version, data: { ...current, ...content } },
                  undefined,
                  2,
                ),
              );
            },
            async delete() {
              return rm(FILE);
            },
          };
        },
      };
    },
  };
}
