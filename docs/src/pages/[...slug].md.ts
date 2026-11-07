import type { APIRoute, GetStaticPaths } from 'astro';
import { getCollection, type CollectionEntry } from 'astro:content';

// The Markdown source of every page, for agents. A page's Markdown is at its URL
// without the trailing slash plus .md: /integrations/custom-events/ is at
// /integrations/custom-events.md, and the root page is at /index.md.
export const getStaticPaths = (async () =>
	(await getCollection('docs')).map((entry) => ({
		params: { slug: entry.id },
		props: { entry },
	}))) satisfies GetStaticPaths;

export const GET: APIRoute<{ entry: CollectionEntry<'docs'> }> = ({ props: { entry } }) =>
	new Response(`# ${entry.data.title}\n\n${entry.body}`, {
		headers: { 'Content-Type': 'text/markdown; charset=utf-8' },
	});
