"use client";

import Link from "next/link";
import { Fragment } from "react";
import { moreNav, primaryNav, tabNav } from "../shell/nav";

type Props = {
  section: string;
  inboxOpen: boolean;
  moreOpen: boolean;
  decisionCount: number;
  setInboxOpen: (open: boolean) => void;
  setMoreOpen: (open: boolean) => void;
  signOut: () => void;
};

export function MobileNav({ section, inboxOpen, moreOpen, decisionCount, setInboxOpen, setMoreOpen, signOut }: Props) {
  const tabs = tabNav.map((item) => item.id);
  const leave = () => { setInboxOpen(false); setMoreOpen(false); };

  return <>
    <nav className="tabbar" aria-label="Primary">
      <button type="button" aria-pressed={inboxOpen} onClick={() => { setMoreOpen(false); setInboxOpen(!inboxOpen); window.scrollTo({ top: 0 }); }}>
        Inbox<span className="nav-count">{decisionCount}</span>
      </button>
      {tabNav.map((item) => (
        <Link key={item.id} href={item.href} aria-current={section === item.id && !inboxOpen ? "page" : undefined} onClick={leave}>
          {item.name}
        </Link>
      ))}
      <button type="button" aria-expanded={moreOpen} aria-current={!tabs.includes(section) && !inboxOpen ? "page" : undefined} onClick={() => setMoreOpen(!moreOpen)}>
        More
      </button>
    </nav>
    {moreOpen && <div className="more-sheet" role="dialog" aria-label="More">
      {primaryNav.slice(tabNav.length).map((item) => (
        <Link key={item.id} href={item.href} aria-current={section === item.id ? "page" : undefined} onClick={leave}>{item.name}</Link>
      ))}
      {moreNav.map((group) => <Fragment key={group.name}>
        <h2 className="more-group">{group.name}</h2>
        {group.items.map((item) => (
          <Link key={item.id} href={item.href} aria-current={section === item.id ? "page" : undefined} onClick={leave}>{item.name}</Link>
        ))}
      </Fragment>)}
      <button type="button" onClick={signOut}>Sign out</button>
    </div>}
  </>;
}
