// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect } from "react";
import { Link, useLocation } from "react-router-dom";
import { DocsShell } from "./DocsShell";
import { Markdown } from "./Markdown";
import { PageNav } from "./PageNav";
import { Toc } from "./Toc";
import { DocsFooter } from "./DocsFooter";
import { getPage } from "./content";

// The shell persists across navigations; the content pane swaps by pathname.
// `/` lands on the first guide page (the sidebar is the real home here).
export function DocsApp() {
  const { pathname, hash } = useLocation();
  const path = pathname === "/" ? "/guide/concepts" : pathname;
  const page = getPage(path);

  // Scroll to top on page change, or to the anchor if the URL carries one.
  useEffect(() => {
    if (hash) {
      const el = document.getElementById(hash.slice(1));
      if (el) {
        el.scrollIntoView();
        return;
      }
    }
    document.querySelector(".docs-main")?.scrollTo(0, 0);
  }, [path, hash]);

  return (
    <DocsShell>
      {/* Reading column and the "on this page" rail sit side by side; the rail
          drops out below 1200px and the column re-centres (docs.css). */}
      <div className="docs-layout">
        <div className="docs-col">
          {page ? (
            <>
              <article className="docs-content">
                <Markdown source={page.body} base={page.path} brand={page.icon} />
              </article>
              <PageNav path={path} />
            </>
          ) : (
            <div className="docs-content">
              <h1>Page not found</h1>
              <p>
                That page doesn’t exist. <Link to="/guide/concepts">Back to the guide</Link>.
              </p>
            </div>
          )}
          {/* Below the prev/next pair, and inside the reading column so it
              stops at the rail rather than running under it. Rendered on the
              not-found page too: that is exactly where a reader needs a way
              out. */}
          <DocsFooter />
        </div>
        {/* Keyed by path so the rail rebuilds from the new page's headings. */}
        {page && <Toc pathKey={path} />}
      </div>
    </DocsShell>
  );
}
