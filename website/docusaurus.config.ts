import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

const config: Config = {
  title: 'Helmdeck',
  tagline: 'Self-hosted AI agent platform for small open-weight models',
  favicon: 'img/favicon.svg',

  future: {
    v4: true,
  },

  url: 'https://helmdeck.vercel.app',
  baseUrl: '/',

  organizationName: 'tosin2013',
  projectName: 'helmdeck',

  onBrokenLinks: 'warn',

  // Treat .md files as plain CommonMark — avoids MDX v3 choking on `<...>` in
  // existing docs. .mdx (none today) would still parse as MDX.
  markdown: {
    format: 'detect',
    hooks: {
      onBrokenMarkdownLinks: 'warn',
    },
  },

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
        blog: false,
        pages: {},
        theme: {
          customCss: './src/css/custom.css',
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
        indexBlog: false,
        docsRouteBasePath: '/',
      },
    ],
  ],

  themeConfig: {
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
