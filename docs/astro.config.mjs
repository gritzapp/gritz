// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import starlightLinksValidator from 'starlight-links-validator';
import starlightLlmsTxt from 'starlight-llms-txt';
import { existsSync } from 'node:fs';
import { sections, sets } from './src/sections.mjs';

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

// The validator only knows the docs pages, and the diagrams are static files that
// `pnpm diagrams` builds into public/diagrams/. A link under /diagrams/ (base
// included, as base-links has run) is checked against those files instead: it
// is excluded from validation when the page exists, and fails when it doesn't.
const diagramPrefix = `${(base ?? '/').replace(/\/?$/, '/')}diagrams/`;
const isDiagramPage = ({ link }) => {
	const path = link.replace(/[?#].*$/, '');
	if (!path.startsWith(diagramPrefix)) return false;
	const file = path.slice(diagramPrefix.length).replace(/(^|\/)$/, '$1index.html');
	return existsSync(new URL(`./public/diagrams/${file}`, import.meta.url));
};

// starlight-llms-txt, minus its llms.txt: that one lists only the documentation
// sets, so src/pages/llms.txt.ts replaces it with one that also links every
// page's Markdown. Astro rejects two routes for one URL, so the plugin's route is
// dropped as the plugin injects it.
const llmsTxt = (options) => {
	const plugin = starlightLlmsTxt(options);
	return {
		...plugin,
		hooks: {
			setup: (params) =>
				plugin.hooks.setup({
					...params,
					addIntegration: (integration) =>
						params.addIntegration({
							...integration,
							hooks: {
								'astro:config:setup': (setup) =>
									integration.hooks['astro:config:setup']({
										...setup,
										injectRoute: (route) =>
											route.pattern === '/llms.txt' || setup.injectRoute(route),
									}),
							},
						}),
				}),
		},
	};
};

// A sidebar group listing every page under one directory of src/content/docs/.
// A page shows up in its group as soon as its file lands. Starlight renders a
// group with no pages as an empty heading, so a group whose directory doesn't
// exist yet is left out.
const section = ({ label, directory }) =>
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
				{ label: 'Start here', items: ['index', 'getting-started'] },
				...sections.flatMap(section),
			],
			components: {
				// Links each page to its Markdown source.
				Head: './src/components/Head.astro',
			},
			plugins: [
				starlightLinksValidator({ exclude: isDiagramPage }),
				// llms-full.txt, llms-small.txt and the Users and Operators sets.
				llmsTxt({
					projectName: 'gritz',
					// The plugin names a set's file after its label.
					customSets: sets.map(({ label, description, paths }) => ({
						label,
						description,
						paths,
					})),
					promote: ['index', 'concepts/**'],
				}),
			],
		}),
	],
});
