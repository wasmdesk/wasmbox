// Command browser is a WhiteSur/Safari-styled web browser client for wasmdesk.
// It is a thin front-end for the go-webengine browserproxy: it opens a gRPC
// stream to the proxy, streams rendered page frames into its content area, and
// forwards the user's navigation (address bar, favourites, back/forward),
// content clicks, wheel scrolls and keys back as intents. Rendering happens
// server-side, so ANY site works (including pages that forbid framing) and the
// wasmbox page stays under COEP:require-corp — a WebSocket is exempt from
// COEP/CORS. If the proxy is unreachable the client shows a clear offline
// panel instead of crashing.
//
// The wire protocol is the browserproxy.v1.Browser gRPC service carried over
// grpc-transports/websocket: one bidirectional Session stream per tab. The
// transport ships a zero-dependency syscall/js client, so this very client
// compiles to GOOS=js/wasm and speaks full gRPC in the browser with no sidecar.
//
//go:build js && wasm

package main

import (
	"bytes"
	"context"
	"image"
	"image/draw"
	"image/png"
	"sync"
	"syscall/js"

	"github.com/go-webengine/browserproxy/browserpb"
	wstransport "github.com/grpc-transports/websocket"
	"github.com/wasmdesk/wasmbox/clients/browser/internal/scene"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// defaultProxyURL is the dev-default browserproxy endpoint. It can be
// overridden by the compositor via wasmboxClient.browserProxyURL or a global
// BROWSERPROXY_URL, so a deployment can point the client at a hosted proxy.
const defaultProxyURL = "ws://localhost:8090/ws"

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

	// scheduleAnim drives the loading-skeleton shimmer. It is nil until wired
	// below; render calls it after every paint so any repaint that leaves the
	// browser in the loading state (re)starts the animation loop.
	var scheduleAnim func()

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
		if scheduleAnim != nil {
			scheduleAnim()
		}
	}

	conn := newConn(proxyURL(client), state, render)

	// Loading-skeleton animation loop. While the scene reports Loading() (a
	// navigation or the initial connect is awaiting its first frame), a
	// self-rescheduling setTimeout advances the shimmer phase from a wall clock
	// and repaints. The moment a frame arrives (or an error/idle), Loading() goes
	// false and the loop stops — so an idle browser never burns CPU repainting,
	// matching the compositor's don't-paint-when-idle discipline. Scene access
	// runs under the conn lock, serialised against the gRPC receive goroutine.
	dateNow := js.Global().Get("Date")
	animPending := false
	var tick js.Func
	tick = js.FuncOf(func(js.Value, []js.Value) any {
		conn.withLock(func() {
			animPending = false
			if !state.Loading() {
				return
			}
			now := dateNow.Call("now").Float()
			state.SetPhase(now / 1000.0 / scene.SkelCycleSeconds)
			render() // render re-arms the loop via scheduleAnim while still loading
		})
		return nil
	})
	scheduleAnim = func() {
		// Always called from within render(), i.e. under the conn lock.
		if animPending || !state.Loading() {
			return
		}
		animPending = true
		js.Global().Call("setTimeout", tick, 33) // ~30fps shimmer
	}

	state.OnNavigate = func(url string) { conn.sendLocked(navigateMsg(url)) }
	state.OnBack = func() { conn.sendLocked(backMsg()) }
	state.OnForward = func() { conn.sendLocked(forwardMsg()) }
	state.OnContentClick = func(x, y int) { conn.sendLocked(clickMsg(x, y)) }
	state.OnScroll = func(dy int) { conn.sendLocked(scrollMsg(dy)) }
	state.OnContentKey = func(key string) { conn.sendLocked(keyMsg(key)) }

	render() // initial paint (offline panel until the stream opens)
	conn.open()

	cb := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		ev := args[0]
		conn.withLock(func() {
			var dirty bool
			switch ev.Get("kind").String() {
			case "mousedown":
				dirty = state.HandleMouse(ev.Get("x").Int(), ev.Get("y").Int())
			case "wheel":
				dirty = state.HandleWheel(ev.Get("deltaY").Int())
			case "keydown":
				dirty = state.HandleKey(ev.Get("key").String())
			}
			if dirty {
				render()
			}
		})
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

// conn owns the gRPC Session stream to the proxy and marshals scene intents out
// / server messages in. Its lock serialises every scene mutation + repaint: the
// background receive goroutine, the input callbacks and the animation tick all
// take it, so the single-threaded scene stays race-free.
type conn struct {
	url    string
	state  *scene.State
	render func()

	mu     sync.Mutex
	cc     *grpc.ClientConn
	stream browserpb.Browser_SessionClient
	open_  bool
}

func newConn(url string, state *scene.State, render func()) *conn {
	return &conn{url: url, state: state, render: render}
}

// withLock runs fn holding the conn lock — the one gate for scene access.
func (c *conn) withLock(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fn()
}

// open dials the proxy over grpc-transports/websocket and starts the receive
// loop. A construction failure (or a later stream error) leaves the client in
// the offline state. The browser WebSocket global is required — the transport's
// js/wasm client is built on it — so its absence is treated as offline.
func (c *conn) open() {
	if js.Global().Get("WebSocket").IsUndefined() {
		c.fail()
		return
	}
	opt, err := wstransport.DialOption(c.url, wstransport.ClientConfig{})
	if err != nil {
		c.fail()
		return
	}
	cc, err := grpc.NewClient("passthrough:///browserproxy",
		grpc.WithTransportCredentials(insecure.NewCredentials()), opt)
	if err != nil {
		c.fail()
		return
	}
	stream, err := browserpb.NewBrowserClient(cc).Session(context.Background())
	if err != nil {
		_ = cc.Close()
		c.fail()
		return
	}

	c.withLock(func() {
		c.cc, c.stream, c.open_ = cc, stream, true
		c.state.SetConnected(true)
		// Ask the proxy to render at our content-area size, and show the loading
		// skeleton until that first streamed frame arrives.
		cw, ch := c.state.ContentSize()
		c.sendStreamLocked(resizeMsg(cw, ch))
		c.state.BeginLoad()
		c.render()
	})

	go c.recvLoop()
}

// fail transitions the client to the offline state.
func (c *conn) fail() {
	c.withLock(func() {
		c.open_ = false
		c.state.SetConnected(false)
		c.render()
	})
}

// recvLoop reads server messages until the stream ends, applying each to the
// scene under the lock. It never holds the lock across Recv, so an in-flight
// send (from an input callback) never blocks the receive path and vice versa.
func (c *conn) recvLoop() {
	for {
		msg, err := c.stream.Recv()
		if err != nil {
			c.fail()
			return
		}
		c.withLock(func() {
			c.apply(msg)
			c.render()
		})
	}
}

// apply updates the scene from one server message (lock held).
func (c *conn) apply(msg *browserpb.ServerMsg) {
	switch m := msg.GetMsg().(type) {
	case *browserpb.ServerMsg_Frame:
		if rgba, w, h, ok := decodeFrame(m.Frame.GetPng()); ok {
			c.state.SetFrame(rgba, w, h)
		}
	case *browserpb.ServerMsg_State:
		s := m.State
		c.state.SetState(s.GetUrl(), s.GetTitle(), s.GetLoading(), s.GetCanBack(), s.GetCanForward())
	case *browserpb.ServerMsg_Error:
		c.state.SetError(m.Error.GetMessage())
	}
}

// sendLocked sends an intent; it is called from the scene On* callbacks, which
// always run inside an input handler already holding the lock.
func (c *conn) sendLocked(m *browserpb.ClientMsg) { c.sendStreamLocked(m) }

// sendStreamLocked writes m to the stream if it is open (lock held). A send
// error is left to the receive loop to surface as a disconnect.
func (c *conn) sendStreamLocked(m *browserpb.ClientMsg) {
	if !c.open_ || c.stream == nil {
		return
	}
	_ = c.stream.Send(m)
}

// --- ClientMsg builders -------------------------------------------------------

func navigateMsg(url string) *browserpb.ClientMsg {
	return &browserpb.ClientMsg{Msg: &browserpb.ClientMsg_Navigate{Navigate: &browserpb.Navigate{Url: url}}}
}
func clickMsg(x, y int) *browserpb.ClientMsg {
	return &browserpb.ClientMsg{Msg: &browserpb.ClientMsg_Click{Click: &browserpb.Click{X: int32(x), Y: int32(y)}}}
}
func scrollMsg(dy int) *browserpb.ClientMsg {
	return &browserpb.ClientMsg{Msg: &browserpb.ClientMsg_Scroll{Scroll: &browserpb.Scroll{Dy: int32(dy)}}}
}
func keyMsg(key string) *browserpb.ClientMsg {
	return &browserpb.ClientMsg{Msg: &browserpb.ClientMsg_Key{Key: &browserpb.Key{Key: key}}}
}
func resizeMsg(w, h int) *browserpb.ClientMsg {
	return &browserpb.ClientMsg{Msg: &browserpb.ClientMsg_Resize{Resize: &browserpb.Resize{W: int32(w), H: int32(h)}}}
}
func backMsg() *browserpb.ClientMsg {
	return &browserpb.ClientMsg{Msg: &browserpb.ClientMsg_Back{Back: &browserpb.Back{}}}
}
func forwardMsg() *browserpb.ClientMsg {
	return &browserpb.ClientMsg{Msg: &browserpb.ClientMsg_Forward{Forward: &browserpb.Forward{}}}
}

// decodeFrame turns raw PNG bytes into a tightly-packed RGBA byte buffer.
func decodeFrame(raw []byte) (rgba []byte, w, h int, ok bool) {
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, 0, 0, false
	}
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), img, b.Min, draw.Src)
	return out.Pix, b.Dx(), b.Dy(), true
}
