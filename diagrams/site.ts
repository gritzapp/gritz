// Turns `vite build` into a static site for GitHub Pages: an index listing
// every project and a page per project that plays it with the
// @motion-canvas/player web component. Every URL is relative, so the same
// build works at the site root and under a PR preview's /pr/<n>/.

import path from 'path';
import type {Plugin} from 'vite';

const PROJECTS_DIR = 'src/projects';
const PLAYER_ID = '@motion-canvas/player';

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

export function site(): Plugin {
  let playerRef: string;

  return {
    name: 'gritz:site',
    apply: 'build',
    config: () => ({base: './'}),
    buildStart() {
      playerRef = this.emitFile({type: 'chunk', id: PLAYER_ID, name: 'player'});
    },
    generateBundle(_, bundle) {
      // Serve files as-is; Jekyll would drop chunks whose names start with `_`.
      this.emitFile({type: 'asset', fileName: '.nojekyll', source: ''});

      const player = this.getFileName(playerRef);

      // `slug` is the project file's path under src/projects without the
      // extension, e.g. `create-task` or `proposals/restart`.
      const projects = Object.values(bundle)
        .flatMap(chunk =>
          chunk.type === 'chunk' &&
          chunk.isEntry &&
          chunk.facadeModuleId?.endsWith('?project')
            ? [{chunk, file: chunk.facadeModuleId.split('?')[0]}]
            : [],
        )
        .map(({chunk, file}) => ({
          slug: path.posix
            .relative(path.posix.resolve(PROJECTS_DIR), file)
            .replace(/\.ts$/, ''),
          src: chunk.fileName,
        }))
        .sort((a, b) => a.slug.localeCompare(b.slug));

      for (const {slug, src} of projects) {
        const fileName = `${slug}/index.html`;
        // The player resolves `src` against its own module URL, so hand it
        // an absolute URL built from the page's location.
        const body = `<p><a href="${relative(fileName, 'index.html')}">All diagrams</a></p>
<h1>${escape(slug)}</h1>
<motion-canvas-player auto="true"></motion-canvas-player>
<script type="module">
import '${relative(fileName, player)}';
document
  .querySelector('motion-canvas-player')
  .setAttribute('src', new URL('${relative(fileName, src)}', location.href).href);
</script>`;
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
