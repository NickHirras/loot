package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/nickhirras/loot/internal/bus"
	"github.com/nickhirras/loot/internal/i18n"
)

// wsPingInterval keeps intermediaries from closing an idle stream.
const wsPingInterval = 30 * time.Second

// handleWS upgrades to a websocket and streams every new drop as
// {"type":"drop","drop":{...},"event":{...}}.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Loot is a self-hosted dashboard, and the Vite dev server proxies the
		// socket from a different origin, so origin checking is disabled.
		// Anything sensitive belongs behind your own reverse proxy auth.
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		s.Logger.Debug("websocket upgrade failed", "error", err)
		return
	}
	defer conn.CloseNow()

	// Loot never expects client messages; CloseRead handles pings/pongs and
	// cancels ctx when the peer goes away.
	ctx := conn.CloseRead(r.Context())

	// The socket carries prose — a drop's title — so it needs a language, and
	// unlike a fetch it gets no headers of its own after the handshake. The
	// dashboard puts the language it resolved on the URL (`/ws?lang=de`), so
	// the feed a tab streams matches the feed it fetched; without it the
	// connection falls back to the same negotiation every other request uses.
	lang := s.socketLang(r)

	msgs, unsubscribe := s.Bus.Subscribe()
	defer unsubscribe()

	if err := s.writeMsg(ctx, conn, bus.Message{Type: "hello"}); err != nil {
		return
	}

	ping := time.NewTicker(wsPingInterval)
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-msgs:
			if !ok {
				return
			}
			if err := s.writeMsg(ctx, conn, s.localizeMsg(ctx, lang, msg)); err != nil {
				return
			}
		case <-ping.C:
			pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Ping(pingCtx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

// socketLang is the language one connection speaks: `?lang=` when the tab
// named one Loot can render, and the ordinary per-request answer otherwise.
func (s *Server) socketLang(r *http.Request) string {
	if want := r.URL.Query().Get("lang"); want != "" {
		if match := matchTag(want, s.knownLangs()); match != "" {
			return match
		}
	}
	return s.requestLang(r)
}

// localizeMsg returns msg with its drop rendered in lang.
//
// The message is fanned out to every connection, so the drop is copied before
// it is touched: one German tab must not translate the drop another tab is
// about to be sent. Everything else on the bus is either data or a bare nudge
// to refetch, and refetching is already localized.
func (s *Server) localizeMsg(ctx context.Context, lang string, msg bus.Message) bus.Message {
	if s.Rules == nil || msg.Drop == nil || msg.Event == nil || lang == "" || lang == i18n.BaseLang {
		return msg
	}
	title, subtitle, ok := s.Rules.Localize(ctx, *msg.Drop, *msg.Event, nil, lang)
	if !ok {
		return msg
	}
	localized := *msg.Drop
	localized.Title, localized.Subtitle = title, subtitle
	msg.Drop = &localized
	return msg
}

func (s *Server) writeMsg(ctx context.Context, conn *websocket.Conn, msg bus.Message) error {
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := wsjson.Write(writeCtx, conn, msg); err != nil {
		if !errors.Is(err, context.Canceled) {
			s.Logger.Debug("websocket write failed", "error", err)
		}
		return err
	}
	return nil
}
