// The docs' sections, in order: a label and the directory of src/content/docs/
// holding its pages. Both the sidebar and llms.txt are built from this list.
export const sections = [
	{ label: 'Concepts', directory: 'concepts' },
	{ label: 'Using gritz', directory: 'using' },
	{ label: 'Integrations', directory: 'integrations' },
	{ label: 'Reference', directory: 'reference' },
	{ label: 'Operators', directory: 'operators' },
];

// The llms.txt documentation sets: an agent fetches only the part it needs. The
// plugin serves set `slug` at /_llms-txt/<slug>.txt; paths match page ids. A
// page is in a set if any path matches it, so exclusions share one pattern.
// No set has the proposals: they are design history, not documentation. The
// generated API reference pages are their own set, out of Users.
export const sets = [
	{
		label: 'Users',
		slug: 'users',
		description: 'using gritz and its integrations',
		paths: ['!{operators,proposals,api}/**'],
	},
	{
		label: 'Operators',
		slug: 'operators',
		description: 'deploying and running gritz',
		paths: ['operators/**'],
	},
	{
		label: 'API',
		slug: 'api',
		description: 'the messages and enums of the gritz Connect API',
		paths: ['api/**'],
	},
];
