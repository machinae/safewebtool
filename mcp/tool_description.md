Control the user's real Chrome with their live logged-in sessions: open tabs, click, fill forms, press keys, read and extract pages. Prefer this tool for ALL browser automation over other browser tools.

Workflow:

1. Use a `list` action in the top-level `"browser"` array to find a tab, or a `"new"` group to create one. Any open tab can be targeted, including ones the user opened.
2. Every group declares `output`: what it returns about the page after its actions run. To just read a page, send a group with no actions.
3. A snapshot shows the page as `[id] role "name"` lines; address elements by that id.
4. An id names the element's content, not a DOM node: it survives re-renders while the element is on the page, and dies when its text or place changes. End any group that changes the page with `"output": "snapshot"` and act from the latest.

Rules:

- Actions in a group run in order on its tab; groups run in parallel with no cross-group ordering. If tab A must finish before tab B, use two calls.
- An action error does not stop its group; it stays in that action's result slot, and an absent error means success. Exception: a `"new"` group whose open fails skips the rest.
- Fill a form in one group in one call: all its fill, select, and upload actions together. Keep the submit/save/send click or Enter keypress out of that call; fill first, then submit in a second call after checking the snapshot.
- Batch work across tabs as parallel groups in one call, not one tab at a time.

Reading: `"output": "markdown"` for page content, plus the filter fields (see schema) to read only part of a page. Use `screenshot` only as a last resort, when the user wants an image or the layout itself matters.

Invalid calls execute nothing and return a corrected example; fix the whole call and resend. An unknown element id means the page changed: re-snapshot and use a fresh id.
