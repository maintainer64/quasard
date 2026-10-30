// SPDX-License-Identifier: GPL-3.0-only
package aliceapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"os"
)

type Map = map[string]any

var Magic = []byte("AAPI")

type Codec struct {
	client, server protoreflect.MessageDescriptor
}

func Load(path string) (*Codec, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ds descriptorpb.FileDescriptorSet
	if err = proto.Unmarshal(b, &ds); err != nil {
		return nil, err
	}
	files, err := protodesc.NewFiles(&ds)
	if err != nil {
		return nil, err
	}
	client, err := files.FindDescriptorByName("NAlice.NAliceApi.TClientMessage")
	if err != nil {
		return nil, err
	}
	server, err := files.FindDescriptorByName("NAlice.NAliceApi.TServerMessage")
	if err != nil {
		return nil, err
	}
	return &Codec{client.(protoreflect.MessageDescriptor), server.(protoreflect.MessageDescriptor)}, nil
}
func decode(frame []byte, d protoreflect.MessageDescriptor) (Map, error) {
	if !bytes.HasPrefix(frame, Magic) {
		return nil, fmt.Errorf("missing AAPI magic")
	}
	msg := dynamicpb.NewMessage(d)
	if err := proto.Unmarshal(frame[4:], msg); err != nil {
		return nil, err
	}
	b, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(msg)
	if err != nil {
		return nil, err
	}
	var v Map
	err = json.Unmarshal(b, &v)
	return v, err
}
func encode(v Map, d protoreflect.MessageDescriptor) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	msg := dynamicpb.NewMessage(d)
	if err = (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(b, msg); err != nil {
		return nil, err
	}
	b, err = proto.Marshal(msg)
	return append(append([]byte{}, Magic...), b...), err
}
func (c *Codec) DecodeClient(b []byte) (Map, error) { return decode(b, c.client) }
func (c *Codec) DecodeServer(b []byte) (Map, error) { return decode(b, c.server) }
func (c *Codec) EncodeClient(v Map) ([]byte, error) { return encode(v, c.client) }
func (c *Codec) EncodeServer(v Map) ([]byte, error) { return encode(v, c.server) }
func obj(v any) Map {
	m, _ := v.(map[string]any)
	if m == nil {
		return Map{}
	}
	return m
}
func str(v any) string { s, _ := v.(string); return s }
