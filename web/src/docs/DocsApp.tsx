// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect } from "react";
import { Link, useLocation } from "react-router-dom";
import { DocsShell } from "./DocsShell";
import { Markdown } from "./Markdown";
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
      {page ? (
        <article className="docs-content">
          <Markdown source={page.body} base={page.path} brand={page.icon} />
        </article>
      ) : (
        <div className="docs-content">
          <h1>Page not found</h1>
          <p>
            That page doesn’t exist. <Link to="/guide/concepts">Back to the guide</Link>.
          </p>
        </div>
      )}
    </DocsShell>
  );
}
