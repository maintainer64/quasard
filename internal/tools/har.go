// SPDX-License-Identifier: GPL-3.0-only
package tools

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

type harEntry struct {
	Mock    bool `json:"_mock"`
	Request struct {
		URL string `json:"url"`
	} `json:"request"`
	Response struct {
		Status  int `json:"status"`
		Content struct {
			Text     string `json:"text"`
			Encoding string `json:"encoding"`
		} `json:"content"`
	} `json:"response"`
	WebSocket []struct {
		Type   string `json:"type"`
		Opcode int    `json:"opcode"`
		Data   string `json:"data"`
	} `json:"_webSocketMessages"`
}

// Keep Python's device IDs for normal names; strip all separators and dots.
func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	v := b.String()
	if len(v) > 120 {
		v = v[:120]
		for !utf8.ValidString(v) {
			v = v[:len(v)-1]
		}
	}
	if v == "" {
		v = "unknown"
	}
	return v
}
func importHAR(f *flag.FlagSet, args []string, out io.Writer) error {
	dir := f.String("out", "data", "private data directory")
	if e := parseFlags(f, args); e != nil {
		return e
	}
	if f.NArg() != 1 {
		return fmt.Errorf("usage: import-har [--out data] capture.har")
	}
	file, e := os.Open(f.Arg(0))
	if e != nil {
		return fmt.Errorf("cannot open HAR")
	}
	defer func() { _ = file.Close() }()
	var doc struct {
		Log struct {
			Entries []harEntry `json:"entries"`
		} `json:"log"`
	}
	dec := json.NewDecoder(io.LimitReader(file, 256<<20))
	if e = dec.Decode(&doc); e != nil {
		return fmt.Errorf("invalid HAR JSON")
	}
	captured := map[string]string{"/get_sync_info": "get_sync_info", "/glagol/device_list": "device_list", "/get_user_details": "user_details"}
	count, samples := 0, 0
	for n, entry := range doc.Log.Entries {
		u, e := url.Parse(entry.Request.URL)
		if e != nil {
			return fmt.Errorf("HAR entry %d: invalid URL", n)
		}
		if name, ok := captured[u.Path]; ok && entry.Response.Status == 200 && !entry.Mock {
			b := []byte(entry.Response.Content.Text)
			if entry.Response.Content.Encoding == "base64" {
				b, e = base64.StdEncoding.DecodeString(string(b))
				if e != nil {
					return fmt.Errorf("HAR entry %d: invalid response encoding", n)
				}
			}
			var data map[string]any
			if e = json.Unmarshal(b, &data); e != nil {
				return fmt.Errorf("HAR entry %d: invalid response JSON", n)
			}
			if e = privateJSON(filepath.Join(*dir, "captured", name+".json"), data); e != nil {
				return e
			}
			count++
			if u.Path == "/get_sync_info" {
				if g, ok := data["glagol"]; ok {
					id := u.Query().Get("device_id")
					p := filepath.Join(*dir, "devices", safeName(id)+".json")
					device := map[string]any{}
					old, err := os.ReadFile(p)
					if err == nil {
						if json.Unmarshal(old, &device) != nil {
							return fmt.Errorf("invalid existing device JSON")
						}
					} else if !os.IsNotExist(err) {
						return fmt.Errorf("cannot read device file")
					}
					device["id"] = id
					device["glagol"] = g
					if e = privateJSON(p, device); e != nil {
						return e
					}
				}
			}
		}
		if strings.Contains(entry.Request.URL, "uni.ws") {
			var audio bytes.Buffer
			flush := func() error {
				if audio.Len() == 0 {
					return nil
				}
				samples++
				p := filepath.Join(*dir, "samples", fmt.Sprintf("voice_%d_%d.ogg", n, samples))
				e := privateWrite(p, audio.Bytes())
				audio.Reset()
				return e
			}
			for _, m := range entry.WebSocket {
				if m.Type != "send" {
					continue
				}
				if m.Opcode == 1 {
					if e = flush(); e != nil {
						return e
					}
					continue
				}
				if m.Opcode != 2 {
					continue
				}
				raw, err := base64.StdEncoding.DecodeString(m.Data)
				if err != nil {
					continue
				}
				if len(raw) < 4 || binary.BigEndian.Uint32(raw[:4]) != 1 {
					continue
				}
				audio.Write(raw[4:])
			}
			if e = flush(); e != nil {
				return e
			}
		}
	}
	_, e = fmt.Fprintf(out, "Imported %d responses, %d audio samples; private output.\n", count, samples)
	return e
}
