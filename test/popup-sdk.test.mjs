// SPDX-License-Identifier: BSD-3-Clause
//
// Unit test for the multi-surface SDK routing that powers popups
// (clients/sdk/sdk.js). One worker may own several surfaces (a window + its
// popups), all multiplexed over a single MessagePort. This test proves:
//
//   1. A window's `hello` carries role "window" and no parent.
//   2. `openPopup` before the parent's welcome throws.
//   3. After the parent welcome, `openPopup` sends a popup `hello` with
//      role "popup" + parent window_id + rel_x/rel_y.
//   4. FIFO welcome matching: welcomes are assigned to surfaces in hello order.
//   5. By-window_id routing: `input` reaches only the addressed surface.
//   6. On `closed` a surface deregisters — later traffic for it is dropped,
//      while the worker's other surfaces keep working over the same port.
//
// The SDK is plain ES5 using `self` as its global; we load it via
// vm.runInContext with a sandbox, and a FakePort that records sends + can
// `deliver()` simulated inbound messages.
//
// Run with: node test/popup-sdk.test.mjs

import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import vm from "node:vm";

const ROOT = fileURLToPath(new URL("..", import.meta.url));

let failures = 0;
const ok = (l) => console.log("ok  " + l);
const fail = (l, d) => { failures++; console.error("FAIL " + l + (d ? ": " + d : "")); };
const assert = (c, l, d) => (c ? ok(l) : fail(l, d));
const eq = (a, b, l) =>
  (JSON.stringify(a) === JSON.stringify(b)
    ? ok(l)
    : fail(l, `expected ${JSON.stringify(b)} got ${JSON.stringify(a)}`));

const src = await readFile(`${ROOT}/clients/sdk/sdk.js`, "utf8");

// A port that records outbound sends and can deliver() inbound messages to the
// single dispatcher the SDK installs via addEventListener("message", ...).
class FakePort {
  constructor() { this.sent = []; this._l = []; }
  postMessage(msg) { this.sent.push(msg); }
  addEventListener(t, fn) { if (t === "message") this._l.push(fn); }
  removeEventListener(t, fn) { this._l = this._l.filter((x) => x !== fn); }
  start() {}
  deliver(data) { for (const fn of this._l.slice()) fn({ data }); }
  hellos() { return this.sent.filter((m) => m && m.type === "hello"); }
}

function freshContext() {
  const self = new EventTarget();
  self.self = self;
  self.SharedArrayBuffer = SharedArrayBuffer;
  self.Uint8ClampedArray = Uint8ClampedArray;
  self.Int32Array = Int32Array;
  self.Atomics = Atomics;
  self.Promise = Promise;
  self.Math = Math;
  self.Error = Error;
  self.Number = Number;
  vm.createContext(self);
  vm.runInContext(src, self, { filename: "clients/sdk/sdk.js" });
  return self;
}

// --- scenario: a window + a popup over one port -----------------------------
const ctx = freshContext();
const port = new FakePort();
ctx.WasmboxClient.useMessagePort(port);

const win = new ctx.WasmboxClient({ title: "app", w: 200, h: 150 });
const winInputs = [], winClosed = [];
win.onInput((e) => winInputs.push(e));
win.onClosed((r) => winClosed.push(r));
win.start();

// 1. window hello: role "window", no parent.
const h1 = port.hellos();
eq(h1.length, 1, "window sent exactly one hello");
eq(h1[0].role, "window", "window hello role = window");
assert(h1[0].parent === undefined, "window hello carries no parent field");

// 2. openPopup before the parent's welcome throws.
let threw = false;
try { win.openPopup({ w: 10, h: 10 }); } catch (_) { threw = true; }
assert(threw, "openPopup before parent welcome throws");

// deliver the window's welcome (FIFO: it is the only pending surface).
port.deliver({ type: "welcome", window_id: 7, granted_w: 200, granted_h: 150 });
eq(win.windowId, 7, "window adopts window_id 7 from its welcome");

// 3. openPopup now sends a popup hello with parent + rel placement.
const pop = win.openPopup({ title: "menu", w: 120, h: 90, rel_x: 20, rel_y: 30 });
const h2 = port.hellos();
eq(h2.length, 2, "openPopup sent a second hello");
eq(h2[1].role, "popup", "popup hello role = popup");
eq(h2[1].parent, 7, "popup hello parent = parent window_id (7)");
eq(h2[1].rel_x, 20, "popup hello rel_x carried");
eq(h2[1].rel_y, 30, "popup hello rel_y carried");

const popInputs = [], popClosed = [];
pop.onInput((e) => popInputs.push(e));
pop.onClosed((r) => popClosed.push(r));

// 4. FIFO welcome matching: this welcome belongs to the popup (the only one
//    still awaiting), even though both surfaces share the port.
port.deliver({ type: "welcome", window_id: 8, granted_w: 120, granted_h: 90 });
eq(pop.windowId, 8, "popup adopts window_id 8 (FIFO welcome matching)");

// 5. by-window_id routing: each input reaches only its addressed surface.
port.deliver({ type: "input", window_id: 7, event: { kind: "mousedown", x: 1, y: 2 } });
eq(winInputs.length, 1, "input(7) routed to the window");
eq(popInputs.length, 0, "input(7) not routed to the popup");
port.deliver({ type: "input", window_id: 8, event: { kind: "mousedown", x: 3, y: 4 } });
eq(popInputs.length, 1, "input(8) routed to the popup");
eq(winInputs.length, 1, "input(8) not routed to the window");

// 6. closed deregisters the popup; later traffic for it is dropped, the window
//    keeps working over the same port.
port.deliver({ type: "closed", window_id: 8, reason: "user" });
eq(popClosed, ["user"], "popup received closed(user)");
port.deliver({ type: "input", window_id: 8, event: { kind: "mousedown" } });
eq(popInputs.length, 1, "input(8) after close is dropped (popup deregistered)");
port.deliver({ type: "input", window_id: 7, event: { kind: "mouseup" } });
eq(winInputs.length, 2, "window still receives input after the popup closed");

console.log(failures ? `\nRESULT: FAIL (${failures})` : "\nRESULT: PASS");
process.exit(failures ? 1 : 0);
