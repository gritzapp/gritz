import type { APIRoute, GetStaticPaths } from 'astro';
import { getCollection, type CollectionEntry } from 'astro:content';

// The Markdown source of every page, for agents. A page's Markdown is at its URL
// without the trailing slash plus .md: /integrations/custom-events/ is at
// /integrations/custom-events.md, and the root page is at /index.md.
//
// An .mdx page's body starts with its component imports, which are noise to an
// agent and are dropped. Its components stay as written, e.g.
// <Diagram slug="create-task" />, which reads as a reference to
// /diagrams/create-task/.
export const getStaticPaths = (async () =>
	(await getCollection('docs')).map((entry) => ({
		params: { slug: entry.id },
		props: { entry },
	}))) satisfies GetStaticPaths;

const body = ({ filePath, body = '' }: CollectionEntry<'docs'>) =>
	filePath?.endsWith('.mdx') ? body.replace(/^\s*(?:import [^\n]+\n\s*)+/, '') : body;

export const GET: APIRoute<{ entry: CollectionEntry<'docs'> }> = ({ props: { entry } }) =>
	new Response(`# ${entry.data.title}\n\n${body(entry)}`, {
		headers: { 'Content-Type': 'text/markdown; charset=utf-8' },
	});
