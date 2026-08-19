package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/nickhirras/loot/internal/bus"
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
			if err := s.writeMsg(ctx, conn, msg); err != nil {
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
