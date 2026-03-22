import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';

const sidebars: SidebarsConfig = {
  docs: [
    'intro',
    {
      type: 'category',
      label: 'Getting Started',
      items: [
        'getting-started/quick-start',
        'getting-started/library-usage',
        'getting-started/docker',
      ],
    },
    {
      type: 'category',
      label: 'Concepts',
      items: [
        'concepts/architecture',
        'concepts/pipelines',
        'concepts/sources',
        'concepts/targets',
        'concepts/snapshots',
        'concepts/ordering-and-delivery',
      ],
    },
    {
      type: 'category',
      label: 'Guides',
      items: [
        'guides/postgresql',
        'guides/in-memory-targets',
        'guides/http-sync',
        'guides/fan-out',
        'guides/fan-out-client',
        'guides/filters-and-transforms',
        'guides/snapshots',
        'guides/error-handling',
        'guides/monitoring',
        'guides/kubernetes',
      ],
    },
    {
      type: 'category',
      label: 'Reference',
      items: [
        'reference/configuration',
        'reference/cli',
        'reference/grpc-api',
        'reference/metrics',
        'reference/health-endpoints',
      ],
    },
    {
      type: 'category',
      label: 'Operations',
      items: [
        'operations/slot-lag',
        'operations/re-baseline',
        'operations/dead-letters',
        'operations/troubleshooting',
      ],
    },
  ],
};

export default sidebars;
