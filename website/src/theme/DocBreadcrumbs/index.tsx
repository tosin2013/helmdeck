/**
 * Wrapper swizzle for Docusaurus DocBreadcrumbs. Injects a Schema.org
 * BreadcrumbList JSON-LD derived from the current pathname. Wraps
 * @theme-original/DocBreadcrumbs with a Head block; the original component
 * renders unchanged.
 *
 * Companion to the GSC "Discovered – currently not indexed" remediation
 * pass. Breadcrumb markup improves SERP appearance and crawl-navigation
 * signals — both bytes Google uses to budget crawls.
 *
 * Names are derived from URL slug segments (kebab-case → Title Case).
 * Not as polished as sidebar labels, but valid Schema.org and avoids
 * importing from @docusaurus/theme-common/internal (which Docusaurus
 * marks as not-public-API).
 */
import React from 'react';
import OriginalDocBreadcrumbs from '@theme-original/DocBreadcrumbs';
import Head from '@docusaurus/Head';
import {useLocation} from '@docusaurus/router';

const SITE_URL = 'https://helmdeck.dev';

function slugToTitle(slug: string): string {
  return slug
    .replace(/^\d+-/, '')
    .replace(/[-_]+/g, ' ')
    .replace(/\b\w/g, (c) => c.toUpperCase());
}

export default function DocBreadcrumbsWrapper(props: Record<string, unknown>): JSX.Element {
  const {pathname} = useLocation();

  const segments = pathname.split('/').filter(Boolean);

  if (segments.length === 0) {
    return <OriginalDocBreadcrumbs {...props} />;
  }

  const itemListElement: Array<Record<string, unknown>> = [
    {
      '@type': 'ListItem',
      position: 1,
      name: 'Helmdeck',
      item: SITE_URL,
    },
  ];

  segments.forEach((seg, i) => {
    const path = '/' + segments.slice(0, i + 1).join('/');
    const isLast = i === segments.length - 1;
    const entry: Record<string, unknown> = {
      '@type': 'ListItem',
      position: i + 2,
      name: slugToTitle(seg),
    };
    if (!isLast) entry.item = `${SITE_URL}${path}`;
    itemListElement.push(entry);
  });

  const breadcrumbList = {
    '@context': 'https://schema.org',
    '@type': 'BreadcrumbList',
    itemListElement,
  };

  return (
    <>
      <Head>
        <script type="application/ld+json">{JSON.stringify(breadcrumbList)}</script>
      </Head>
      <OriginalDocBreadcrumbs {...props} />
    </>
  );
}
