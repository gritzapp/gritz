// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import starlightLinksValidator from 'starlight-links-validator';
import { existsSync } from 'node:fs';

// PR previews build with DOCS_BASE=/pr/<n>/. Astro writes absolute URLs, so a
// preview needs its own base, and must not be indexed.
const base = process.env.DOCS_BASE;

// Astro prefixes its own URLs with the base, but not root-relative links written
// in Markdown, such as [envelope](/reference/custom-event-envelope/). In a
// preview this prefixes them, ahead of the links validator, so they stay inside
// the preview and are checked against its pages.
const baseLinks = {
	name: 'base-links',
	hooks: {
		'astro:config:setup': ({ config }) => {
			if (!base) return;
			config.markdown.processor.options.hastPlugins.unshift({
				name: 'base-links',
				element: {
					filter: ['a'],
					visit(node, ctx) {
						const href = node.properties.href;
						if (typeof href === 'string' && /^\/(?!\/)/.test(href)) {
							ctx.setProperty(node, 'href', base.replace(/\/$/, '') + href);
						}
					},
				},
			});
		},
	},
};

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
		baseLinks,
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
