import { ICredentialType, INodeProperties } from 'n8n-workflow';

export class GritzApi implements ICredentialType {
	name = 'GritzApi';
	displayName = 'Gritz API';
	documentationUrl = 'https://github.com/gritzapp/gritz';
	properties: INodeProperties[] = [
		{
			displayName: 'Server URL',
			name: 'serverUrl',
			type: 'string',
			default: '',
			placeholder: 'https://gritz.example.com',
			required: true,
		},
		{
			displayName: 'API Key',
			name: 'apiKey',
			type: 'string',
			typeOptions: { password: true },
			default: '',
			required: true,
		},
	];
}
