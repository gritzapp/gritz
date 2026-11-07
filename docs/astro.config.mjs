// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import starlightLinksValidator from 'starlight-links-validator';
import { existsSync } from 'node:fs';

// PR previews build with DOCS_BASE=/pr/<n>/. Astro writes absolute URLs, so a
// preview needs its own base, and must not be indexed.
const base = process.env.DOCS_BASE;

// A sidebar group listing every page under one directory of src/content/docs/.
// A page shows up in its group as soon as its file lands. Starlight renders a
// group with no pages as an empty heading, so a group whose directory doesn't
// exist yet is left out.
const section = (label, directory) =>
	existsSync(new URL(`./src/content/docs/${directory}`, import.meta.url))
		? [{ label, items: [{ autogenerate: { directory } }] }]
		: [];

export default defineConfig({
	site: 'https://docs.gritz.dev',
	base: base ?? '/',
	integrations: [
		starlight({
			title: 'gritz',
			social: [{ icon: 'github', label: 'GitHub', href: 'https://github.com/gritzapp/gritz' }],
			editLink: {
				baseUrl: 'https://github.com/gritzapp/gritz/edit/master/docs/',
			},
			head: base
				? [{ tag: 'meta', attrs: { name: 'robots', content: 'noindex' } }]
				: [],
			// One group per section.
			sidebar: [
				{ label: 'Start here', items: ['index'] },
				...section('Concepts', 'concepts'),
				...section('Using gritz', 'using'),
				...section('Integrations', 'integrations'),
				...section('Reference', 'reference'),
				...section('Operators', 'operators'),
			],
			plugins: [starlightLinksValidator()],
		}),
	],
});
