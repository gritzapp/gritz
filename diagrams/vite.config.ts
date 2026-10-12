import {defineConfig} from 'vite';
import motionCanvas from '@motion-canvas/vite-plugin';

import {site} from './site';

export default defineConfig({
  plugins: [
    // One project per diagram, so each gets its own timeline in the editor.
    motionCanvas({project: './src/projects/**/*.ts'}),
    // `vite build` emits the static site published to GitHub Pages.
    site(),
  ],
});
