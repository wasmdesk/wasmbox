// Command browser is a WhiteSur/Safari-styled web browser client for wasmdesk.
// It is a thin front-end for the go-webengine browserproxy: it opens a
// WebSocket to the proxy, streams rendered page frames into its content area,
// and forwards the user's navigation (address bar, favourites, back/forward),
// content clicks, wheel scrolls and keys back as intents. Rendering happens
// server-side, so ANY site works (including pages that forbid framing) and the
// wasmbox page stays under COEP:require-corp — a WebSocket is exempt from
// COEP/CORS. If the proxy is unreachable the client shows a clear offline
// panel instead of crashing.
//
//go:build js && wasm

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/draw"
	"image/png"
	"syscall/js"

	"github.com/wasmdesk/wasmbox/clients/browser/internal/scene"
)

// defaultProxyURL is the dev-default browserproxy endpoint. It can be
// overridden by the compositor via wasmboxClient.browserProxyURL or a global
// BROWSERPROXY_URL, so a deployment can point the client at a hosted proxy.
const defaultProxyURL = "ws://localhost:8090/ws"

// serverMsg is a server→client protocol message (see browserproxy
// docs/protocol.md). Only the fields for the received kind are populated.
type serverMsg struct {
	Kind       string `json:"kind"`
	Frame      string `json:"frame"`
	W          int    `json:"w"`
	H          int    `json:"h"`
	OffsetY    int    `json:"offsetY"`
	URL        string `json:"url"`
	Title      string `json:"title"`
	Loading    bool   `json:"loading"`
	CanBack    bool   `json:"canBack"`
	CanForward bool   `json:"canForward"`
	Message    string `json:"message"`
}

func main() {
	client := js.Global().Get("wasmboxClient")
	if client.IsUndefined() {
		println("browser: wasmboxClient missing; SDK not loaded?")
		return
	}

	w := client.Get("w").Int()
	h := client.Get("h").Int()
	pixels := client.Get("pixels")
	if pixels.Get("length").Int() != 4*w*h {
		println("browser: pixel buffer size mismatch")
		return
	}

	local := make([]byte, 4*w*h)
	state := scene.New(w, h)

	render := func() {
		scene.Render(state, local)
		client.Call("beginFrame") // open the seqlock window before the bulk copy (tear-free)
		js.CopyBytesToJS(pixels, local)
		damage := js.Global().Call("Object")
		damage.Set("x", 0)
		damage.Set("y", 0)
		damage.Set("w", w)
		damage.Set("h", h)
		client.Call("commit", damage)
	}

	conn := newConn(proxyURL(client), state, render)
	state.OnNavigate = func(url string) { conn.send(map[string]any{"kind": "navigate", "url": url}) }
	state.OnBack = func() { conn.send(map[string]any{"kind": "back"}) }
	state.OnForward = func() { conn.send(map[string]any{"kind": "forward"}) }
	state.OnContentClick = func(x, y int) { conn.send(map[string]any{"kind": "click", "x": x, "y": y}) }
	state.OnScroll = func(dy int) { conn.send(map[string]any{"kind": "scroll", "dy": dy}) }
	state.OnContentKey = func(key string) { conn.send(map[string]any{"kind": "key", "key": key}) }

	render() // initial paint (offline panel until the socket opens)
	conn.open()

	cb := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		ev := args[0]
		switch ev.Get("kind").String() {
		case "mousedown":
			if state.HandleMouse(ev.Get("x").Int(), ev.Get("y").Int()) {
				render()
			}
		case "wheel":
			if state.HandleWheel(ev.Get("deltaY").Int()) {
				render()
			}
		case "keydown":
			if state.HandleKey(ev.Get("key").String()) {
				render()
			}
		}
		return nil
	})
	client.Call("onInput", cb)
	select {}
}

// proxyURL resolves the browserproxy WebSocket URL: a compositor-provided
// wasmboxClient.browserProxyURL, then a global BROWSERPROXY_URL, then the dev
// default.
func proxyURL(client js.Value) string {
	if v := client.Get("browserProxyURL"); v.Type() == js.TypeString && v.String() != "" {
		return v.String()
	}
	if v := js.Global().Get("BROWSERPROXY_URL"); v.Type() == js.TypeString && v.String() != "" {
		return v.String()
	}
	return defaultProxyURL
}

// conn owns the browser-side WebSocket to the proxy and marshals scene intents
// out / server messages in.
type conn struct {
	url    string
	state  *scene.State
	render func()
	ws     js.Value
	open_  bool
}

func newConn(url string, state *scene.State, render func()) *conn {
	return &conn{url: url, state: state, render: render}
}

// open dials the proxy and wires the socket event handlers. A construction
// failure (or a later error/close) leaves the client in the offline state.
func (c *conn) open() {
	ctor := js.Global().Get("WebSocket")
	if ctor.IsUndefined() {
		c.state.SetConnected(false)
		c.render()
		return
	}
	c.ws = ctor.New(c.url)

	c.ws.Call("addEventListener", "open", js.FuncOf(func(js.Value, []js.Value) any {
		c.open_ = true
		c.state.SetConnected(true)
		// Ask the proxy to render at our content-area size.
		cw, ch := c.state.ContentSize()
		c.send(map[string]any{"kind": "resize", "w": cw, "h": ch})
		c.render()
		return nil
	}))
	closed := js.FuncOf(func(js.Value, []js.Value) any {
		c.open_ = false
		c.state.SetConnected(false)
		c.render()
		return nil
	})
	c.ws.Call("addEventListener", "close", closed)
	c.ws.Call("addEventListener", "error", closed)
	c.ws.Call("addEventListener", "message", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) > 0 {
			c.onMessage(args[0].Get("data").String())
		}
		return nil
	}))
}

// send JSON-encodes an intent and writes it if the socket is open.
func (c *conn) send(m map[string]any) {
	if !c.open_ {
		return
	}
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	c.ws.Call("send", string(b))
}

// onMessage decodes one server message and updates the scene, then repaints.
func (c *conn) onMessage(data string) {
	var m serverMsg
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return
	}
	switch m.Kind {
	case "frame":
		if rgba, w, h, ok := decodeFrame(m.Frame); ok {
			c.state.SetFrame(rgba, w, h)
		}
	case "state":
		c.state.SetState(m.URL, m.Title, m.Loading, m.CanBack, m.CanForward)
	case "error":
		c.state.SetError(m.Message)
	default:
		return
	}
	c.render()
}

// decodeFrame turns a base64 PNG payload into a tightly-packed RGBA byte buffer.
func decodeFrame(b64 string) (rgba []byte, w, h int, ok bool) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, 0, 0, false
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, 0, 0, false
	}
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), img, b.Min, draw.Src)
	return out.Pix, b.Dx(), b.Dy(), true
}
