/**
 * Wrapper swizzle for Docusaurus BlogPostPage. Injects:
 *   - Schema.org Article JSON-LD for rich-snippet eligibility
 *   - Per-page og:title / og:description / og:url / og:type=article
 *   - article:published_time + article:modified_time
 *
 * Wraps @theme-original/BlogPostPage with Head additions; the original
 * component renders unchanged inside.
 *
 * Companion to the GSC "Discovered – currently not indexed" remediation
 * pass (see CHANGELOG [Unreleased] + plan). Per-page meta beats the
 * site-wide static fallbacks emitted from docusaurus.config.ts metadata[].
 */
import React from 'react';
import OriginalBlogPostPage from '@theme-original/BlogPostPage';
import Head from '@docusaurus/Head';
import type BlogPostPageType from '@theme/BlogPostPage';
import type {WrapperProps} from '@docusaurus/types';

type Props = WrapperProps<typeof BlogPostPageType>;

const SITE_URL = 'https://helmdeck.dev';
const DEFAULT_OG_IMAGE = `${SITE_URL}/img/social-card.png`;

function absoluteUrl(path: string | undefined, fallback: string): string {
  if (!path) return fallback;
  if (/^https?:\/\//i.test(path)) return path;
  return `${SITE_URL}${path.startsWith('/') ? '' : '/'}${path}`;
}

export default function BlogPostPageWrapper(props: Props): JSX.Element {
  // BlogPostPage receives the rendered MDX content as a prop; metadata
  // lives on the component itself.
  const content = (props as unknown as {content?: {metadata?: Record<string, unknown>; frontMatter?: Record<string, unknown>}}).content;
  const metadata = content?.metadata ?? {};
  const frontMatter = (content?.frontMatter ?? {}) as Record<string, unknown>;

  const title = (metadata.title as string | undefined) ?? (frontMatter.title as string | undefined);
  const description = (metadata.description as string | undefined) ?? (frontMatter.description as string | undefined);
  const date = (metadata.date as string | undefined);
  const lastUpdatedAt = (metadata.lastUpdatedAt as number | undefined);
  const permalink = (metadata.permalink as string | undefined);
  const authors = ((metadata.authors as unknown[] | undefined) ?? []) as Array<Record<string, string>>;

  if (!title || !permalink) {
    return <OriginalBlogPostPage {...props} />;
  }

  const pageUrl = `${SITE_URL}${permalink}`;
  const ogImage = absoluteUrl(frontMatter.image as string | undefined, DEFAULT_OG_IMAGE);

  const dateModified = lastUpdatedAt
    ? new Date(lastUpdatedAt * 1000).toISOString()
    : date;

  const jsonLd = {
    '@context': 'https://schema.org',
    '@type': 'Article',
    headline: title,
    ...(description ? {description} : {}),
    ...(date ? {datePublished: date} : {}),
    ...(dateModified ? {dateModified} : {}),
    author: authors.length > 0
      ? authors.map((a) => ({
          '@type': 'Person',
          name: a.name || a.title || 'Helmdeck contributors',
          ...(a.url ? {url: a.url} : {}),
        }))
      : [{
          '@type': 'Organization',
          name: 'Helmdeck contributors',
          url: 'https://github.com/tosin2013/helmdeck',
        }],
    publisher: {
      '@type': 'Organization',
      name: 'Helmdeck contributors',
      url: 'https://github.com/tosin2013/helmdeck',
      logo: {
        '@type': 'ImageObject',
        url: `${SITE_URL}/img/logo.svg`,
      },
    },
    mainEntityOfPage: {
      '@type': 'WebPage',
      '@id': pageUrl,
    },
    url: pageUrl,
    image: ogImage,
  };

  return (
    <>
      <Head>
        <script type="application/ld+json">{JSON.stringify(jsonLd)}</script>
        <meta property="og:title" content={title} />
        {description && <meta property="og:description" content={description} />}
        <meta property="og:url" content={pageUrl} />
        <meta property="og:type" content="article" />
        <meta property="og:image" content={ogImage} />
        {date && <meta property="article:published_time" content={date} />}
        {dateModified && dateModified !== date && (
          <meta property="article:modified_time" content={dateModified} />
        )}
        <link rel="canonical" href={pageUrl} />
      </Head>
      <OriginalBlogPostPage {...props} />
    </>
  );
}
