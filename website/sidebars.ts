import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';

const sidebars: SidebarsConfig = {
  tutorials: [
    'tutorials/index',
    'tutorials/install-cli',
    'tutorials/install-ui-walkthrough',
    'integrations/pack-demo-playbook',
  ],

  howto: [
    'howto/index',
    {
      type: 'category',
      label: 'Operations',
      collapsed: false,
      items: [
        'howto/troubleshoot-install',
        'howto/upgrade-helmdeck',
        'howto/register-with-mcp-clients',
        'howto/watch-agent-via-vnc',
        'howto/manage-vault-credentials',
        'howto/configure-llm-providers',
        'howto/inspect-audit-logs',
      ],
    },
    {
      type: 'category',
      label: 'Client integrations',
      collapsed: false,
      items: [
        'integrations/claude-code',
        'integrations/claude-desktop',
        'integrations/gemini-cli',
        'integrations/hermes-agent',
        'integrations/openclaw',
        'integrations/openclaw-sidecar-research',
        'integrations/openclaw-upgrade-runbook',
        'integrations/openclaw-upstream-issue',
        'integrations/nemoclaw',
        'integrations/webhooks',
      ],
    },
    {
      type: 'category',
      label: 'Sidecars',
      items: [
        'SIDECAR-EXTENDING',
        'SIDECAR-LANGUAGES',
      ],
    },
  ],

  reference: [
    'reference/index',
    'reference/architecture',
    'reference/agent-memory',
    {
      type: 'category',
      label: 'Prompt templates',
      link: {type: 'doc', id: 'reference/prompt-templates/index'},
      items: [
        'reference/prompt-templates/packs',
        'reference/prompt-templates/pipelines',
      ],
    },
    'PACKS',
    'integrations/SKILLS',
    'integrations/README',
    {
      type: 'category',
      label: 'Pack reference (per-pack)',
      link: {type: 'doc', id: 'reference/packs/index'},
      items: [
        {
          type: 'category',
          label: 'browser',
          items: [
            'reference/packs/browser/screenshot-url',
            'reference/packs/browser/interact',
          ],
        },
        {
          type: 'category',
          label: 'web',
          items: [
            'reference/packs/web/scrape',
            'reference/packs/web/scrape-spa',
            'reference/packs/web/test',
          ],
        },
        {
          type: 'category',
          label: 'vision',
          items: [
            'reference/packs/vision/click-anywhere',
            'reference/packs/vision/extract-visible-text',
            'reference/packs/vision/fill-form-by-label',
          ],
        },
        {
          type: 'category',
          label: 'github',
          items: [
            'reference/packs/github/create-issue',
            'reference/packs/github/list-issues',
            'reference/packs/github/list-prs',
            'reference/packs/github/post-comment',
            'reference/packs/github/create-release',
            'reference/packs/github/search',
          ],
        },
        {
          type: 'category',
          label: 'http',
          items: ['reference/packs/http/fetch'],
        },
        {
          type: 'category',
          label: 'doc',
          items: [
            'reference/packs/doc/ocr',
            'reference/packs/doc/parse',
          ],
        },
        {
          type: 'category',
          label: 'fs',
          items: [
            'reference/packs/fs/read',
            'reference/packs/fs/write',
            'reference/packs/fs/list',
            'reference/packs/fs/patch',
            'reference/packs/fs/delete',
          ],
        },
        {
          type: 'category',
          label: 'cmd',
          items: ['reference/packs/cmd/run'],
        },
        {
          type: 'category',
          label: 'git',
          items: [
            'reference/packs/git/commit',
            'reference/packs/git/diff',
            'reference/packs/git/log',
          ],
        },
        {
          type: 'category',
          label: 'language',
          items: [
            'reference/packs/language/python-run',
            'reference/packs/language/node-run',
          ],
        },
        {
          type: 'category',
          label: 'repo',
          items: [
            'reference/packs/repo/fetch',
            'reference/packs/repo/map',
            'reference/packs/repo/push',
          ],
        },
        {
          type: 'category',
          label: 'slides',
          items: [
            'reference/packs/slides/render',
            'reference/packs/slides/narrate',
          ],
        },
        {
          type: 'category',
          label: 'desktop',
          items: [
            'reference/packs/desktop/run-app-and-screenshot',
            'reference/packs/desktop-rest-primitives',
          ],
        },
        {
          type: 'category',
          label: 'research',
          items: ['reference/packs/research/deep'],
        },
        {
          type: 'category',
          label: 'content',
          items: ['reference/packs/content/ground'],
        },
        {
          type: 'category',
          label: 'blog',
          items: ['reference/packs/blog/publish'],
        },
        {
          type: 'category',
          label: 'podcast',
          items: ['reference/packs/podcast/generate'],
        },
        {
          type: 'category',
          label: 'image',
          items: ['reference/packs/image/generate'],
        },
      ],
    },
    {
      type: 'category',
      label: 'Architecture Decisions',
      link: {type: 'generated-index', slug: '/adrs'},
      items: [{type: 'autogenerated', dirName: 'adrs'}],
    },
    {
      type: 'category',
      label: 'Project tracking',
      items: ['TASKS', 'MILESTONES', 'RELEASES'],
    },
  ],

  explanation: [
    'explanation/index',
    'explanation/why-helmdeck',
    'SECURITY-HARDENING',
  ],
};

export default sidebars;
