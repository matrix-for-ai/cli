package main

import (
	"fmt"
	"log"
	"time"
)

var retryAfter = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour}

const handlerTimeout = 30 * time.Second

func (s *server) sweep() {
	cfg, err := s.config()
	if err != nil {
		log.Printf("config: %v", err)
		return
	}
	pending := listPending(s.state)
	now := s.now()
	mutes := loadMutes(s.state)
	for mxid, e := range cfg.Entities {
		if !e.hasWebhook() {
			s.fallBehind(mxid)
			continue
		}
		if mutes.muted(mxid, now) {
			s.fallBehind(mxid)
			continue
		}
		catchUp := !s.isCaughtUp(mxid)
		due := catchUp
		for _, p := range pending[mxid] {
			if p.retryDue(now) {
				due = true
			}
		}
		if !due {
			continue
		}
		inbox, err := s.fetch(*e)
		if err != nil {
			log.Printf("%s: unread: %v", mxid, err)
			continue
		}
		known := map[string]Pending{}
		for _, p := range pending[mxid] {
			known[p.Room] = p
			if _, ok := inbox[p.Room]; !ok {
				removePending(s.state, mxid, p.Room) // read since
			}
		}
		for room, r := range inbox {
			p, had := known[room]
			n := Notice{Identity: mxid, Room: room, Sender: r.LastUnread.Sender, Unread: r.Unread, TS: now.UnixMilli()}
			switch {
			case p.retryDue(now):
				n.Reason = "retry"
			case catchUp && p.NextAt == 0 && (!had || r.LastUnread.TS > p.DeliveredAt):
				n.Reason = "catch-up"
			default:
				continue
			}
			s.notify(mxid, *e, n)
		}
		s.markCaughtUp(mxid)
	}
}

func (p Pending) retryDue(now time.Time) bool {
	return p.NextAt > 0 && now.UnixMilli() >= p.NextAt
}

func (s *server) notify(mxid string, e Entity, n Notice) {
	p := s.roomState(mxid, n.Room)
	p.Room, p.Sender, p.Unread = n.Room, n.Sender, n.Unread
	if loadMutes(s.state).muted(mxid, s.now()) {
		log.Printf("%s: muted, notice from %s in %s kept for unmute", mxid, n.Sender, n.Room)
		s.fallBehind(mxid)
		s.save(mxid, p)
		return
	}
	// In flight: if the client dies before the handler returns, this retry picks it up.
	p.NextAt = s.now().Add(handlerTimeout + time.Minute).UnixMilli()
	s.save(mxid, p)
	s.enqueue(mxid, job{e, n})
}

func (s *server) delivered(mxid string, n Notice, err error) {
	p := s.roomState(mxid, n.Room)
	p.Room = n.Room
	now := s.now()
	if err == nil {
		p.DeliveredAt, p.Attempts, p.NextAt = now.UnixMilli(), 0, 0
		s.save(mxid, p)
		return
	}
	if p.Attempts < len(retryAfter) {
		p.NextAt = now.Add(retryAfter[p.Attempts]).UnixMilli()
		p.Attempts++
		s.save(mxid, p)
		return
	}
	msg := fmt.Sprintf("could not deliver %s's notice in %s to %s (%d attempts)", n.Sender, n.Room, mxid, p.Attempts+1)
	log.Print(msg)
	s.alert("matrix-for-ai", msg)
	// Closed as if delivered, so a restart does not start over; something new in the room will.
	p.DeliveredAt, p.Attempts, p.NextAt = now.UnixMilli(), 0, 0
	s.save(mxid, p)
}

func (s *server) markCaughtUp(mxid string) {
	s.mu.Lock()
	s.caughtUp[mxid] = true
	s.mu.Unlock()
}

func (s *server) fallBehind(mxid string) {
	s.mu.Lock()
	delete(s.caughtUp, mxid)
	s.mu.Unlock()
}

func (s *server) isCaughtUp(mxid string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.caughtUp[mxid]
}

func (s *server) roomState(mxid, room string) Pending {
	for _, p := range listPending(s.state)[mxid] {
		if p.Room == room {
			return p
		}
	}
	return Pending{Room: room}
}

func (s *server) save(mxid string, p Pending) {
	if err := savePending(s.state, mxid, p); err != nil {
		log.Printf("%s: pending: %v", mxid, err)
	}
}

func (s *server) retrying() bool {
	now := s.now()
	mutes := loadMutes(s.state)
	if mutes.anyEndsByItself(now) {
		return true
	}
	for mxid, ps := range listPending(s.state) {
		if mutes.muted(mxid, now) {
			continue
		}
		for _, p := range ps {
			if p.NextAt > 0 {
				return true
			}
		}
	}
	return false
}
