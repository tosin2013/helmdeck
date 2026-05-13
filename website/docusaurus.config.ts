import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

const config: Config = {
  title: 'Helmdeck',
  tagline: 'Run agentic workflows on cheap or local LLMs at 10× lower cost than frontier-model APIs.',
  favicon: 'img/favicon.svg',

  future: {
    v4: true,
  },

  url: 'https://helmdeck.dev',
  baseUrl: '/',
  trailingSlash: false,

  organizationName: 'tosin2013',
  projectName: 'helmdeck',

  onBrokenLinks: 'warn',

  // Treat .md files as plain CommonMark — avoids MDX v3 choking on `<...>` in
  // existing docs. .mdx (none today) would still parse as MDX.
  markdown: {
    format: 'detect',
    mermaid: true,
    hooks: {
      onBrokenMarkdownLinks: 'warn',
    },
  },

  themes: ['@docusaurus/theme-mermaid'],

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  presets: [
    [
      'classic',
      {
        docs: {
          path: '../docs',
          routeBasePath: '/',
          sidebarPath: './sidebars.ts',
          editUrl: 'https://github.com/tosin2013/helmdeck/edit/main/',
        },
        blog: {
          showReadingTime: true,
          blogTitle: 'Helmdeck blog',
          blogDescription:
            'Engineering notes, design rationale, and field reports from the helmdeck project.',
          postsPerPage: 'ALL',
          blogSidebarTitle: 'All posts',
          blogSidebarCount: 'ALL',
          feedOptions: {
            type: ['rss', 'atom'],
            copyright: `Copyright © ${new Date().getFullYear()} Tosin Akinosho.`,
            title: 'Helmdeck blog',
            description:
              'Engineering notes, design rationale, and field reports from the helmdeck project.',
          },
        },
        pages: {},
        theme: {
          customCss: './src/css/custom.css',
        },
        sitemap: {
          changefreq: 'weekly',
          priority: 0.5,
          filename: 'sitemap.xml',
          ignorePatterns: [
            '/search',
            // Thin/duplicate index pages — listed in Google Search Console's
            // "Discovered – currently not indexed" bucket. Excluding from
            // sitemap concentrates crawl budget on content pages.
            '/blog/tags',
            '/blog/tags/**',
            '/blog/archive',
            '/blog/authors',
          ],
          // Per-route priority/changefreq bumps for the highest-value
          // pages. Docusaurus's default sitemap plugin does NOT read
          // frontmatter priority/changefreq, so we override here.
          createSitemapItems: async (params) => {
            const {defaultCreateSitemapItems, ...rest} = params;
            const items = await defaultCreateSitemapItems(rest);
            const bump = (urlSuffix: string, priority: number, changefreq?: 'weekly' | 'monthly' | 'yearly') =>
              items
                .filter((i) => i.url.endsWith(urlSuffix))
                .forEach((i) => {
                  i.priority = priority;
                  if (changefreq) i.changefreq = changefreq;
                });
            bump('/', 1.0, 'weekly');                                  // landing
            bump('/PACKS', 0.9, 'weekly');
            bump('/integrations/SKILLS', 0.9, 'weekly');
            bump('/explanation/why-helmdeck', 0.85, 'monthly');        // long-form positioning page
            bump('/integrations', 0.8, 'weekly');                      // index
            bump('/blog', 0.8, 'weekly');                              // blog index
            bump('/tutorials/install-cli', 0.8, 'monthly');
            bump('/tutorials/install-ui-walkthrough', 0.8, 'monthly');
            bump('/integrations/pack-demo-playbook', 0.8, 'monthly');
            bump('/howto/troubleshoot-install', 0.7, 'monthly');
            // Bump every individual blog post — Docusaurus emits them
            // under /blog/<slug>; default priority is 0.5 which buries
            // them. Field reports / cost analyses warrant 0.7 monthly.
            items
              .filter((i) => i.url.includes('/blog/') && !i.url.endsWith('/blog'))
              .forEach((i) => {
                i.priority = 0.7;
                i.changefreq = 'monthly';
              });
            return items;
          },
        },
      } satisfies Preset.Options,
    ],
  ],

  plugins: [
    [
      '@easyops-cn/docusaurus-search-local',
      {
        hashed: true,
        indexDocs: true,
        indexBlog: true,
        docsRouteBasePath: '/',
        blogRouteBasePath: '/blog',
      },
    ],
  ],

  // SEO: site-wide structured data + GSC verification slot. The GSC
  // entry stays commented until the property is created (DNS-domain
  // verification preferred — uncomment only if URL-prefix verification
  // is chosen instead).
  headTags: [
    {
      tagName: 'script',
      attributes: {type: 'application/ld+json'},
      innerHTML: JSON.stringify({
        '@context': 'https://schema.org',
        '@type': 'WebSite',
        name: 'Helmdeck',
        url: 'https://helmdeck.dev',
        description:
          'Self-hosted AI agent platform. 36 typed capability packs make agentic workflows reliable on cheap or local LLMs (gpt-oss-120b, Gemma, Mistral) at 10× lower per-task cost than Anthropic Computer Use, OpenAI Operator, or naive Sonnet function-calling.',
        publisher: {
          '@type': 'Organization',
          name: 'Helmdeck contributors',
          url: 'https://github.com/tosin2013/helmdeck',
        },
        potentialAction: {
          '@type': 'SearchAction',
          target: {
            '@type': 'EntryPoint',
            urlTemplate: 'https://helmdeck.dev/search?q={search_term_string}',
          },
          'query-input': 'required name=search_term_string',
        },
      }),
    },
  ],

  themeConfig: {
    image: 'img/social-card.png',
    metadata: [
      {
        name: 'description',
        content:
          'Self-hosted AI agent platform. 36 typed capability packs (browser, code, slides, vision, desktop) make agentic workflows reliable on cheap or local LLMs (gpt-oss-120b, Gemma, Mistral) — 10× lower per-task cost than Anthropic Computer Use, OpenAI Operator, or naive Sonnet function-calling. Apache 2.0.',
      },
      {
        name: 'keywords',
        content:
          'helmdeck, AI agents, MCP, capability packs, self-hosted, open-source, weak models, local LLMs, gpt-oss, Gemma, OpenClaw, Claude Code, browser automation, agent platform, cost optimization',
      },
      {name: 'twitter:card', content: 'summary_large_image'},
      {name: 'twitter:site', content: '@tosin2013'},
      // Uncomment + paste the token if you choose URL-prefix verification
      // in Google Search Console. DNS-domain verification (TXT record)
      // doesn't need this.
      // {name: 'google-site-verification', content: 'PASTE-TOKEN-HERE'},
    ],
    colorMode: {
      respectPrefersColorScheme: true,
    },
    navbar: {
      title: 'Helmdeck',
      logo: {
        alt: 'Helmdeck logo',
        src: 'img/logo.svg',
        srcDark: 'img/logo-dark.svg',
        width: 32,
        height: 32,
      },
      items: [
        {type: 'docSidebar', sidebarId: 'tutorials',   label: 'Tutorials',   position: 'left'},
        {type: 'docSidebar', sidebarId: 'howto',       label: 'How-to',      position: 'left'},
        {type: 'docSidebar', sidebarId: 'reference',   label: 'Reference',   position: 'left'},
        {type: 'docSidebar', sidebarId: 'explanation', label: 'Explanation', position: 'left'},
        {to: '/blog', label: 'Blog', position: 'left'},
        {to: '/changelog', label: 'Changelog', position: 'left'},
        {
          href: 'https://github.com/tosin2013/helmdeck',
          label: 'GitHub',
          position: 'right',
        },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Docs',
          items: [
            {label: 'Tutorials',   to: '/tutorials/'},
            {label: 'How-to',      to: '/howto/'},
            {label: 'Reference',   to: '/reference/'},
            {label: 'Explanation', to: '/explanation/'},
          ],
        },
        {
          title: 'Project',
          items: [
            {label: 'Blog', to: '/blog'},
            {label: 'Changelog', to: '/changelog'},
            {label: 'Architecture Decisions', to: '/adrs'},
            {label: 'Tasks', to: '/TASKS'},
          ],
        },
        {
          title: 'More',
          items: [
            {label: 'GitHub', href: 'https://github.com/tosin2013/helmdeck'},
            {label: 'Releases', href: 'https://github.com/tosin2013/helmdeck/releases'},
          ],
        },
      ],
      copyright: `Copyright © ${new Date().getFullYear()} Helmdeck contributors. Apache 2.0.`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['bash', 'json', 'yaml', 'go', 'docker', 'toml'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
