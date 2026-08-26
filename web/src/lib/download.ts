// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Saving a file the app has already fetched.
//
// Every download here is data the browser holds in memory, not a URL it can be
// pointed at: the endpoints need the Authorization header, which a plain
// `<a href="/api/v1/…">` cannot send, and the CSV is built client-side from the
// rows on screen and doesn't exist server-side at all. So the file is assembled
// into a blob, handed to a transient object URL, and clicked.
//
// This lived in three places — the org export, the collections CSV, and then a
// third copy wanted it for the account export. The dance has a
// leak-if-you-forget step (revokeObjectURL) and a needs-to-be-in-the-document
// step (Firefox ignores a click on a detached anchor), which is exactly the
// shape of thing that should exist once.

// downloadText saves `text` as a file. `mime` sets the type so the OS opens it
// with something sensible.
export function downloadText(text: string, mime: string, filename: string) {
  const url = URL.createObjectURL(new Blob([text], { type: mime }));
  try {
    const a = document.createElement("a");
    a.href = url;
    a.download = filename;
    // Firefox won't act on a click on an anchor outside the document.
    document.body.appendChild(a);
    a.click();
    a.remove();
  } finally {
    // In a finally: an exception between creating the URL and clicking would
    // otherwise leak the blob for the lifetime of the document.
    URL.revokeObjectURL(url);
  }
}

// downloadJson saves an object as a pretty-printed .json file. Indented
// deliberately: an export a person may open, read, or hand to somebody else is
// worth the extra bytes.
export function downloadJson(data: unknown, filename: string) {
  downloadText(JSON.stringify(data, null, 2), "application/json", filename);
}
