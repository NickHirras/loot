package server_test

import (
	"context"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// websocketDial and readJSON keep the websocket library out of the test bodies
// so the assertions read as protocol expectations rather than plumbing.
func websocketDial(ctx context.Context, url string) (*websocket.Conn, *websocket.DialOptions, error) {
	conn, _, err := websocket.Dial(ctx, url, nil)
	return conn, nil, err
}

func readJSON(ctx context.Context, conn *websocket.Conn, v any) error {
	return wsjson.Read(ctx, conn, v)
}
