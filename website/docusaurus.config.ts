import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

const config: Config = {
  title: 'Laredo',
  tagline: 'Real-time data sync from PostgreSQL to in-memory targets',
  favicon: 'img/favicon.svg',

  future: {
    v4: true,
  },

  url: 'https://zrz.io',
  baseUrl: '/laredo/',

  organizationName: 'zourzouvillys',
  projectName: 'laredo',

  onBrokenLinks: 'throw',

  markdown: {
    hooks: {
      onBrokenMarkdownLinks: 'throw',
    },
  },

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  themes: [
    [
      '@easyops-cn/docusaurus-search-local',
      {
        hashed: true,
        docsRouteBasePath: '/',
      },
    ],
  ],

  presets: [
    [
      'classic',
      {
        docs: {
          sidebarPath: './sidebars.ts',
          routeBasePath: '/',
          editUrl: 'https://github.com/zourzouvillys/laredo/edit/main/website/',
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    colorMode: {
      respectPrefersColorScheme: true,
    },
    navbar: {
      title: 'Laredo',
      items: [
        {
          type: 'docSidebar',
          sidebarId: 'docs',
          position: 'left',
          label: 'Docs',
        },
        {
          href: 'https://github.com/zourzouvillys/laredo',
          label: 'GitHub',
          position: 'right',
        },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Documentation',
          items: [
            {label: 'Introduction', to: '/'},
            {label: 'Getting Started', to: '/getting-started/quick-start'},
            {label: 'Concepts', to: '/concepts/architecture'},
          ],
        },
        {
          title: 'Guides',
          items: [
            {label: 'PostgreSQL Source', to: '/guides/postgresql'},
            {label: 'In-Memory Targets', to: '/guides/in-memory-targets'},
            {label: 'Replication Fan-Out', to: '/guides/fan-out'},
          ],
        },
        {
          title: 'More',
          items: [
            {label: 'GitHub', href: 'https://github.com/zourzouvillys/laredo'},
            {label: 'Go Reference', href: 'https://pkg.go.dev/github.com/zourzouvillys/laredo'},
          ],
        },
      ],
      copyright: `Copyright ${new Date().getFullYear()} Laredo Contributors.`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['bash', 'json', 'protobuf', 'toml', 'go'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
