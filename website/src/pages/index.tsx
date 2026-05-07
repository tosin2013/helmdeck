import type {ReactNode} from 'react';
import clsx from 'clsx';
import Link from '@docusaurus/Link';
import useDocusaurusContext from '@docusaurus/useDocusaurusContext';
import Layout from '@theme/Layout';
import Heading from '@theme/Heading';

import styles from './index.module.css';

const quadrants = [
  {
    title: 'Tutorials',
    blurb: 'Learning-oriented walkthroughs. Start here if helmdeck is new — go from zero to a working pack-driven agent with explicit steps.',
    to: '/tutorials/',
  },
  {
    title: 'How-to guides',
    blurb: 'Problem-solving recipes. Wire helmdeck into a specific MCP client, extend a sidecar, ship a webhook integration.',
    to: '/howto/',
  },
  {
    title: 'Reference',
    blurb: 'Information lookup. Pack contracts, SKILLS for LLMs, every Architecture Decision Record, project tracking.',
    to: '/reference/',
  },
  {
    title: 'Explanation',
    blurb: 'Understanding-oriented background. The why behind the security model and architecture choices.',
    to: '/explanation/',
  },
];

function HomepageHeader() {
  const {siteConfig} = useDocusaurusContext();
  return (
    <header className={clsx('hero hero--primary', styles.heroBanner)}>
      <div className="container">
        <Heading as="h1" className="hero__title">{siteConfig.title}</Heading>
        <p className="hero__subtitle">{siteConfig.tagline}</p>
        <div className={styles.buttons}>
          <Link className="button button--secondary button--lg" to="/tutorials/">
            Get started
          </Link>
          <Link
            className={clsx('button button--outline button--secondary button--lg', styles.heroSecondary)}
            to="/PACKS">
            Browse the pack catalog
          </Link>
        </div>
      </div>
    </header>
  );
}

function Quadrants() {
  return (
    <section className={styles.quadrants}>
      <div className="container">
        <div className={styles.quadrantGrid}>
          {quadrants.map((q) => (
            <Link key={q.to} to={q.to} className={styles.quadrantCard}>
              <Heading as="h3">{q.title}</Heading>
              <p>{q.blurb}</p>
              <span className={styles.quadrantArrow}>Read →</span>
            </Link>
          ))}
        </div>
      </div>
    </section>
  );
}

export default function Home(): ReactNode {
  const {siteConfig} = useDocusaurusContext();
  return (
    <Layout
      title={siteConfig.title}
      description={siteConfig.tagline}>
      <HomepageHeader />
      <main>
        <Quadrants />
      </main>
    </Layout>
  );
}
