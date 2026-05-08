import type {ReactNode} from 'react';
import clsx from 'clsx';
import Link from '@docusaurus/Link';
import useDocusaurusContext from '@docusaurus/useDocusaurusContext';
import Layout from '@theme/Layout';
import Heading from '@theme/Heading';

import styles from './index.module.css';

const whyCards = [
  {
    title: 'Cheap models, real work',
    blurb: 'Run agentic browser, code, slides, vision, and desktop workflows on gpt-oss-120b, Gemma 4, or Mistral — the same Phase 5.5 code-edit loop that needs Sonnet on Cursor.',
  },
  {
    title: 'Deterministic primitives',
    blurb: '36 typed capability packs do the work. The LLM only picks which pack to call. Move recurring deterministic work out of the expensive token-priced layer.',
  },
  {
    title: 'Self-hosted, audited',
    blurb: 'Your data, your keys, your hardware. Per-pack audit log, vault-backed credentials, egress-guarded network. Apache 2.0.',
  },
];

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

function WhyHelmdeck() {
  return (
    <section className={styles.why}>
      <div className="container">
        <Heading as="h2" className={styles.whyHeading}>Why helmdeck</Heading>
        <p className={styles.whyLead}>
          Frontier-model APIs price a single agentic workflow at $0.20–$0.50.
          Helmdeck runs the same workflow on a cheap or local model for $0.05–$0.10,
          with deterministic packs absorbing the ambiguity that the model would
          otherwise burn tokens rediscovering.
        </p>
        <div className={styles.whyGrid}>
          {whyCards.map((c) => (
            <div key={c.title} className={styles.whyCard}>
              <Heading as="h3">{c.title}</Heading>
              <p>{c.blurb}</p>
            </div>
          ))}
        </div>
        <div className={styles.whyCta}>
          <Link className="button button--primary button--lg" to="/explanation/why-helmdeck">
            Read the full comparison →
          </Link>
        </div>
      </div>
    </section>
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
      title={`${siteConfig.title} — ${siteConfig.tagline}`}
      description="Self-hosted AI agent platform. 36 typed capability packs (browser, code, slides, vision, desktop) make agentic workflows reliable on cheap or local LLMs (gpt-oss-120b, Gemma, Mistral) — 10× lower per-task cost than Anthropic Computer Use, OpenAI Operator, or naive Sonnet function-calling. Apache 2.0.">
      <HomepageHeader />
      <main>
        <WhyHelmdeck />
        <Quadrants />
      </main>
    </Layout>
  );
}
