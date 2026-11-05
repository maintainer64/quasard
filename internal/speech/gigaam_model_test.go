// SPDX-License-Identifier: GPL-3.0-only
package speech

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGigaModelIntegrity(t *testing.T) {
	dir := t.TempDir()
	m := gigaManifest{Version: 1, Model: "v3_e2e_rnnt", Quantization: "int8_encoder",
		SampleRate: 16000, FFTSize: 320, HopSize: 160, MelBins: 64,
		EncoderDim: 768, HiddenSize: 320, Layers: 1, BlankID: 1024, MaxSymbols: 10,
		MelFilters: make([]float32, 64*161), Tokens: make([]gigaToken, 1024), Files: map[string]gigaFile{}}
	for i := range m.Tokens {
		m.Tokens[i] = gigaToken{Piece: "а", Kind: "normal"}
	}
	for _, name := range []string{"encoder.onnx", "decoder.onnx", "joiner.onnx"} {
		data := []byte("test graph " + name)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		m.Files[name] = gigaFile{SHA256: hex.EncodeToString(hash[:]), Bytes: int64(len(data))}
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadGigaManifest(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "encoder.onnx"), []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadGigaManifest(dir); err == nil {
		t.Fatal("corrupt model accepted")
	}
}

func TestGigaSentencePieces(t *testing.T) {
	m := gigaManifest{UnknownSurface: " ⁇ ", Tokens: []gigaToken{
		{Piece: "▁Сколько", Kind: "normal"}, {Piece: "▁градусов", Kind: "normal"},
		{Piece: "?", Kind: "normal"}, {Piece: "<s>", Kind: "control"},
		{Piece: "<0xD0>", Kind: "byte"}, {Piece: "<0x90>", Kind: "byte"},
	}}
	for _, test := range []struct {
		ids  []int32
		want string
	}{
		{[]int32{3, 0, 1, 2}, "Сколько градусов?"},
		{[]int32{4, 5}, "А"},
	} {
		got, err := m.decode(test.ids)
		if err != nil || got != test.want {
			t.Fatalf("pieces: %q %v", got, err)
		}
	}
	for _, bad := range [][]int32{{-1}, {6}, {4}} {
		if _, err := m.decode(bad); err == nil {
			t.Fatal("invalid token/UTF-8 accepted")
		}
	}
}
