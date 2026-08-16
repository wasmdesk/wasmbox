// SPDX-License-Identifier: BSD-3-Clause
//
// wasmbox accessibility (a11y) bridge — main-thread half.
//
// The compositor paints the entire desktop onto an OffscreenCanvas in a Web
// Worker. A canvas exposes no accessible text, so a screen reader (VoiceOver /
// Orca / NVDA) would perceive an empty page. This bridge gives it something to
// read: it maintains a LIVE ARIA element tree in the real DOM that mirrors the
// compositor's window stack — role="application" at the desktop root, a
// role="window" group per window, and a real <button> per activatable control
// (Activate / Minimize / Restore / Close). The accessibility API the browser
// derives from this DOM is the exact same API VoiceOver / Orca / NVDA (and
// Playwright's accessibility snapshot) consume, so the desktop becomes readable
// AND operable without a single pixel changing.
//
// This is the browser twin of go-widgets/window's AT-SPI bridge: there, the
// widget tree is walked and mirrored onto the Linux accessibility bus; here the
// window tree is walked (in Ruby, 18_a11y.rb) and mirrored into the browser
// accessibility DOM. The transport is postMessage:
//
//   worker -> main : { type: "a11y_update", tree: <json> }   (damage-gated)
//   main -> worker : { type: "a11y_action", detail: {action, id} }
//
// The tree is rebuilt only when the compositor's window state actually changes
// (Ruby gates on A11yTree.signature), so this reconciler runs rarely and does a
// KEYED reconcile (update-in-place by id / action name) rather than blowing the
// subtree away — that preserves keyboard focus while a user tabs the controls.

"use strict";

(function () {
  const ROOT_ID = "wasmbox-a11y-root";
  const WIN_PREFIX = "wasmbox-a11y-win-";

  let workerRef = null;
  let lastTree = null; // last parsed tree, exposed for tests

  // The sr-only container: present in the accessibility tree (NOT aria-hidden,
  // NOT display:none — either would erase it from every screen reader) but
  // clipped to a 1px box off the visible desktop so its buttons never paint over
  // the canvas. This is the canonical visually-hidden / screen-reader-only
  // pattern; focusable descendants stay focusable and operable.
  function ensureRoot() {
    let root = document.getElementById(ROOT_ID);
    if (root) return root;
    root = document.createElement("div");
    root.id = ROOT_ID;
    root.setAttribute("role", "application");
    root.setAttribute("aria-label", "wasmbox desktop");
    root.style.cssText =
      "position:fixed;width:1px;height:1px;overflow:hidden;" +
      "clip:rect(0 0 0 0);clip-path:inset(50%);white-space:nowrap;" +
      "margin:-1px;padding:0;border:0;left:0;top:0;";
    document.body.appendChild(root);
    return root;
  }

  // Reconcile the DOM to match `tree` (already-parsed a11y snapshot). Keyed by
  // window id and, within a window, by action name — existing elements are
  // updated in place, missing ones created, stale ones removed. Focus survives a
  // reconcile that does not remove the focused control.
  function reconcile(tree) {
    const root = ensureRoot();
    if (tree.label != null) root.setAttribute("aria-label", String(tree.label));

    const nodes = Array.isArray(tree.nodes) ? tree.nodes : [];
    const seenWin = new Set();

    for (const node of nodes) {
      const winId = WIN_PREFIX + node.id;
      seenWin.add(winId);
      let winEl = document.getElementById(winId);
      if (!winEl) {
        winEl = document.createElement("div");
        winEl.id = winId;
        winEl.setAttribute("role", "group");
        // ARIA has no "window" role; aria-roledescription makes a screen reader
        // announce "<title>, window" rather than a bare group.
        winEl.setAttribute("aria-roledescription", "window");
        root.appendChild(winEl);
      }
      winEl.setAttribute("aria-label", String(node.label));
      // aria-current marks the focused window; a screen reader announces it and
      // the root's aria-activedescendant (set below) points AT it.
      winEl.setAttribute("aria-current", node.focused ? "true" : "false");
      winEl.dataset.minimized = node.minimized ? "true" : "false";

      reconcileActions(winEl, node);
    }

    // Drop windows that closed / left the workspace since the last snapshot.
    for (const child of Array.from(root.children)) {
      if (child.id && child.id.startsWith(WIN_PREFIX) && !seenWin.has(child.id)) {
        child.remove();
      }
    }

    // Point the desktop root at the focused window so an AT that follows
    // aria-activedescendant lands on it.
    if (tree.active_id == null) {
      root.removeAttribute("aria-activedescendant");
    } else {
      root.setAttribute("aria-activedescendant", WIN_PREFIX + tree.active_id);
    }
  }

  // Reconcile the <button> children of one window group against node.actions,
  // keyed by action name (data-a11y-action). The action SET changes when a
  // window folds/unfolds (minimize <-> restore), so buttons are added/removed as
  // well as relabelled.
  function reconcileActions(winEl, node) {
    const actions = Array.isArray(node.actions) ? node.actions : [];
    const seenAct = new Set();

    for (const act of actions) {
      seenAct.add(act.name);
      let btn = winEl.querySelector('button[data-a11y-action="' + act.name + '"]');
      if (!btn) {
        btn = document.createElement("button");
        btn.type = "button";
        btn.dataset.a11yAction = act.name;
        btn.dataset.a11yId = String(node.id);
        btn.addEventListener("click", onActivate);
        winEl.appendChild(btn);
      }
      btn.dataset.a11yId = String(node.id);
      // The button's text IS its accessible name; set aria-label too so the name
      // is stable even if a future style hides the text node.
      btn.textContent = String(act.label);
      btn.setAttribute("aria-label", String(act.label));
    }

    for (const btn of Array.from(winEl.querySelectorAll("button[data-a11y-action]"))) {
      if (!seenAct.has(btn.dataset.a11yAction)) btn.remove();
    }
  }

  // A control was activated (mouse, Enter/Space, or a screen reader's synthetic
  // press all fire a click). Send the action to the compositor worker, which
  // routes it to the same WindowManager path a titlebar click uses.
  function onActivate(ev) {
    const btn = ev.currentTarget;
    const action = btn.dataset.a11yAction;
    const id = parseInt(btn.dataset.a11yId, 10);
    if (!workerRef || !action || Number.isNaN(id)) return;
    workerRef.postMessage({ type: "a11y_action", detail: { action: action, id: id } });
  }

  // Called from index.html once the compositor worker exists. Wires the update
  // listener and remembers the worker for outbound actions.
  function attachWorker(worker) {
    workerRef = worker;
    worker.addEventListener("message", (ev) => {
      const m = ev.data;
      if (!m || m.type !== "a11y_update") return;
      update(m.tree);
    });
  }

  // Parse + apply an ARIA tree snapshot (JSON string). Exposed for tests so a
  // snapshot can be driven without a worker.
  function update(json) {
    let tree;
    try {
      tree = typeof json === "string" ? JSON.parse(json) : json;
    } catch (_) {
      return null;
    }
    lastTree = tree;
    reconcile(tree);
    return tree;
  }

  globalThis.WASMBOX_A11Y = {
    attachWorker: attachWorker,
    update: update,
    // Test/introspection hooks: the last parsed tree, and a programmatic
    // activate that clicks the real button (proving the DOM control exists and
    // is wired — the exact path a screen reader's press takes).
    lastTree: () => lastTree,
    invoke: (id, action) => {
      const root = document.getElementById(ROOT_ID);
      if (!root) return false;
      const btn = root.querySelector(
        'button[data-a11y-action="' + action + '"][data-a11y-id="' + id + '"]'
      );
      if (!btn) return false;
      btn.click();
      return true;
    },
  };
})();
