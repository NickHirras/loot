package server

// Where a drop points, filled in on the way out.
//
// core.Drop.Link is derived, never stored (see internal/links), so every
// response that carries drops to a client has to ask for it — there is no
// column that would have come along for the ride. This file is the whole of
// the server's share of that: one rule, applied at every exit.
//
// The rule has one wrinkle, and it is demo mode. A demo Loot is full of
// plausible, entirely fictional apps and repositories, and a card that
// invites you to click through to a repository that does not exist is worse
// than a card that does not invite you anywhere. So the demo keeps its
// *internal* links — "#/quests" goes to the demo's own Quests board, which is
// real enough — and drops the external ones on the floor.

import (
	"strings"

	"github.com/nickhirras/loot/internal/bus"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/links"
	"github.com/nickhirras/loot/internal/store"
)

// linkFor is where ev happened, as this server is willing to advertise it.
func (s *Server) linkFor(ev core.Event) string {
	link := links.For(ev)
	if s.Cfg.Demo.Enabled && !strings.HasPrefix(link, "#") {
		return ""
	}
	return link
}

// linkDrops fills in the Link of a page of feed drops, in place. Like
// localizeDrops, the slice always comes straight from a query — nothing
// memoizes /api/drops or a chest — so there is nothing shared to copy first.
func (s *Server) linkDrops(drops []store.DropView) {
	for i := range drops {
		drops[i].Link = s.linkFor(drops[i].Event())
	}
}

// linkMsg returns msg with its drop's Link filled in.
//
// The message is fanned out to every connection, so the drop is copied before
// it is touched, for exactly the reason localizeMsg copies it: the pointer on
// the bus is shared by every subscriber, `loot tail` included.
//
// It is a step of its own rather than a line inside localizeMsg because that
// one returns early for English, and a link is not prose — an English reader
// wants it every bit as much as a German one.
func (s *Server) linkMsg(msg bus.Message) bus.Message {
	if msg.Drop == nil || msg.Event == nil {
		return msg
	}
	link := s.linkFor(*msg.Event)
	if link == "" {
		return msg
	}
	linked := *msg.Drop
	linked.Link = link
	msg.Drop = &linked
	return msg
}
