import type { APIRoute } from 'astro';
import { getCollection } from 'astro:content';
import { sections, sets } from '../sections.mjs';

// Replaces the llms.txt of starlight-llms-txt, which lists only the documentation
// sets, with one in the llms.txt format (https://llmstxt.org/) that also links
// every page's Markdown. The plugin's llms-full.txt, llms-small.txt and sets stay.
export const GET: APIRoute = async ({ site }) => {
	const url = (path: string) =>
		new URL(import.meta.env.BASE_URL.replace(/\/?$/, '/') + path, site);
	const docs = (await getCollection('docs')).sort((a, b) => a.id.localeCompare(b.id));
	const list = (pages: typeof docs) =>
		pages
			.map(
				({ id, data }) =>
					`- [${data.title}](${url(`${id}.md`)})` +
					(data.description ? `: ${data.description}` : ''),
			)
			.join('\n');

	const segments = [
		'# gritz',
		'> gritz runs coding agents such as Claude Code and Codex in parallel, each in its own sandbox, driven by tasks and events.',
		'Every page is also served as Markdown: take its URL, drop the trailing slash and append `.md`. The root page is `index.md`.',
		'## Documentation sets',
		[
			`- [Complete documentation](${url('llms-full.txt')}): every page, concatenated`,
			`- [Abridged documentation](${url('llms-small.txt')}): every page, with non-essential content removed`,
			...sets.map(
				({ label, slug, description }) =>
					`- [${label}](${url(`_llms-txt/${slug}.txt`)}): ${description}`,
			),
		].join('\n'),
		'## Start here',
		// The proposals are design history and aren't listed; their index's id is `proposals`.
		list(docs.filter(({ id }) => !id.includes('/') && id !== 'proposals')),
	];
	for (const { label, directory } of sections) {
		const pages = docs.filter(({ id }) => id.startsWith(`${directory}/`));
		if (pages.length === 0) continue;
		segments.push(`## ${label}`, list(pages));
	}
	return new Response(segments.join('\n\n') + '\n', {
		headers: { 'Content-Type': 'text/plain; charset=utf-8' },
	});
};
