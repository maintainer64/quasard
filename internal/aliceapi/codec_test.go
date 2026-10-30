// SPDX-License-Identifier: GPL-3.0-only
package aliceapi

import (
	"github.com/maintainer64/quasard/internal/protocol"
	"testing"
)

func TestCodec(t *testing.T) {
	c, e := Load("../../alice_server/aliceapi.desc")
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range []Map{protocol.Sync("ref", "session"), protocol.InputAck("ref"), protocol.ASRResult("ref", "привет", 1, true, 100), protocol.Speak("ref", 2), protocol.Vins("ref", "req", "sid", 1, "Привет", "Привет", nil, false), protocol.Control(2, 0, 0)} {
		b, e := c.EncodeServer(ServerMessage(v))
		if e != nil {
			t.Fatalf("encode %v: %v", v, e)
		}
		if _, e = c.DecodeServer(b); e != nil {
			t.Fatal(e)
		}
	}
	b, e := c.EncodeClient(Map{"Event": Map{"Header": Map{"MessageId": "ref"}, "SynchronizeStateRequest": Map{}}})
	if e != nil {
		t.Fatal(e)
	}
	raw, e := c.DecodeClient(b)
	if e != nil {
		t.Fatal(e)
	}
	v := ClientToJSON(raw, "ref")
	if obj(obj(v["event"])["header"])["name"] != "SynchronizeState" {
		t.Fatal(v)
	}
	if _, e = c.DecodeClient([]byte("bad")); e == nil {
		t.Fatal("accepted bad prefix")
	}
}
