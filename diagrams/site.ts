// Turns `vite build` into a static site, built into the docs site's
// public/diagrams/ (see docs/package.json) and served under its /diagrams/: an
// index listing every project and a page per project that plays it with the
// @motion-canvas/player web component and the playback controls in
// controls.ts. Every URL in the pages is relative, so the same build works
// under the docs root and under a PR preview's /pr/<n>/.
//
// A project's bundle is always <slug>/project.js and the controls are always
// controls.js, so the docs' Diagram component can play a project inline from
// its slug alone.

import path from 'path';
import type {Plugin} from 'vite';

const PROJECTS_DIR = 'src/projects';
const CONTROLS = path.resolve('controls.ts');

const style = `
  :root { color-scheme: dark; }
  body {
    margin: 0 auto; padding: 32px; max-width: 1280px;
    background: #101216; color: #e6e8eb;
    font: 16px/1.5 system-ui, sans-serif;
  }
  a { color: #5aa9e6; }
  h1 { font-size: 24px; }
  h2 { font-size: 18px; color: #8b93a1; }
  motion-canvas-player { display: block; width: 100%; aspect-ratio: 16 / 9; }
`;

const escape = (s: string) =>
  s.replace(
    /[&<>"']/g,
    c => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'})[c]!,
  );

const page = (title: string, body: string) => `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>${escape(title)}</title>
<style>${style}</style>
</head>
<body>
${body}
</body>
</html>
`;

// Relative URL from the page at `from` (a file in the output) to `to`.
const relative = (from: string, to: string) =>
  path.posix.relative(path.posix.dirname(from), to) || '.';

// The project file's path under src/projects without the extension, e.g.
// `create-task` or `proposals/restart`, for a project entry's module ID.
const slugOf = (id: string | null) =>
  id?.endsWith('?project')
    ? path.posix
        .relative(path.posix.resolve(PROJECTS_DIR), id.split('?')[0])
        .replace(/\.ts$/, '')
    : undefined;

export function site(): Plugin {
  return {
    name: 'gritz:site',
    apply: 'build',
    config: () => ({
      base: './',
      build: {
        rollupOptions: {
          output: {
            entryFileNames: chunk => {
              const slug = slugOf(chunk.facadeModuleId);
              return slug ? `${slug}/project.js` : '[name]-[hash].js';
            },
          },
        },
      },
    }),
    buildStart() {
      this.emitFile({type: 'chunk', id: CONTROLS, fileName: 'controls.js'});
    },
    generateBundle(_, bundle) {
      const projects = Object.values(bundle)
        .flatMap(chunk => {
          const slug = chunk.type === 'chunk' && chunk.isEntry && slugOf(chunk.facadeModuleId);
          return slug ? [{slug, src: chunk.fileName}] : [];
        })
        .sort((a, b) => a.slug.localeCompare(b.slug));

      for (const {slug, src} of projects) {
        const fileName = `${slug}/index.html`;
        // controls.ts turns data-src into the player's src; data-page makes the
        // player autoplay and take the page's keys.
        const body = `<p><a href="${relative(fileName, 'index.html')}">All diagrams</a></p>
<h1>${escape(slug)}</h1>
<motion-canvas-player data-src="${relative(fileName, src)}" data-page></motion-canvas-player>
<script type="module" src="${relative(fileName, 'controls.js')}"></script>`;
        this.emitFile({type: 'asset', fileName, source: page(slug, body)});
      }

      const groups = new Map<string, string[]>();
      for (const {slug} of projects) {
        const dir = path.posix.dirname(slug);
        groups.set(dir, [...(groups.get(dir) ?? []), slug]);
      }
      // Top-level projects first, then one section per subdirectory.
      const sections = [...groups]
        .sort(([a], [b]) => (a === '.' ? -1 : b === '.' ? 1 : a.localeCompare(b)))
        .map(
          ([dir, slugs]) => `${dir === '.' ? '' : `<h2>${escape(dir)}</h2>\n`}<ul>
${slugs.map(slug => `<li><a href="${escape(slug)}/">${escape(path.posix.basename(slug))}</a></li>`).join('\n')}
</ul>`,
        )
        .join('\n');
      this.emitFile({
        type: 'asset',
        fileName: 'index.html',
        source: page('gritz diagrams', `<h1>gritz diagrams</h1>\n${sections}`),
      });
    },
  };
}
