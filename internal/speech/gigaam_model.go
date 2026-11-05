// SPDX-License-Identifier: GPL-3.0-only
package speech

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

type gigaToken struct {
	Piece string `json:"piece"`
	Kind  string `json:"kind"`
}

type gigaFile struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type gigaManifest struct {
	Quantization   string              `json:"quantization"`
	Version        int                 `json:"schema_version"`
	Model          string              `json:"model"`
	SampleRate     int                 `json:"sample_rate"`
	FFTSize        int                 `json:"fft_size"`
	HopSize        int                 `json:"hop_size"`
	MelBins        int                 `json:"mel_bins"`
	Center         bool                `json:"center"`
	MelFilters     []float32           `json:"mel_filters"`
	EncoderDim     int                 `json:"encoder_dim"`
	HiddenSize     int                 `json:"hidden_size"`
	Layers         int                 `json:"layers"`
	BlankID        int                 `json:"blank_id"`
	MaxSymbols     int                 `json:"max_symbols"`
	UnknownSurface string              `json:"unknown_surface"`
	Tokens         []gigaToken         `json:"tokens"`
	Files          map[string]gigaFile `json:"files"`
	Source         json.RawMessage     `json:"source"`
}

func loadGigaManifest(directory string) (*gigaManifest, error) {
	f, err := os.Open(filepath.Join(directory, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("GigaAM manifest: %w", err)
	}
	defer func() { _ = f.Close() }()
	var model gigaManifest
	decoder := json.NewDecoder(io.LimitReader(f, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&model); err != nil {
		return nil, fmt.Errorf("GigaAM manifest: %w", err)
	}
	if (model.Quantization != "float32" && model.Quantization != "int8_encoder") || model.Version != 1 || model.Model != "v3_e2e_rnnt" || model.SampleRate != 16000 ||
		model.FFTSize != 320 || model.HopSize != 160 || model.MelBins != 64 || model.Center ||
		model.EncoderDim != 768 || model.HiddenSize != 320 || model.Layers != 1 ||
		model.BlankID != 1024 || model.MaxSymbols != 10 || len(model.Tokens) != 1024 || len(model.MelFilters) != 64*161 {
		return nil, fmt.Errorf("unsupported GigaAM manifest: export v3_e2e_rnnt with native/export-gigaam.py")
	}
	for _, value := range model.MelFilters {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) || value < 0 {
			return nil, fmt.Errorf("invalid GigaAM mel filter")
		}
	}
	for _, token := range model.Tokens {
		if !utf8.ValidString(token.Piece) {
			return nil, fmt.Errorf("invalid GigaAM token")
		}
		switch token.Kind {
		case "normal", "unknown", "control":
		case "byte":
			if _, err := gigaByte(token.Piece); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unsupported GigaAM token kind")
		}
	}
	if len(model.Files) != 3 {
		return nil, fmt.Errorf("GigaAM manifest requires three ONNX graphs")
	}
	for _, name := range []string{"encoder.onnx", "decoder.onnx", "joiner.onnx"} {
		info, ok := model.Files[name]
		if !ok || info.Bytes <= 0 || info.Bytes > 2<<30 || len(info.SHA256) != 64 {
			return nil, fmt.Errorf("invalid GigaAM file metadata for %s", name)
		}
		input, err := os.Open(filepath.Join(directory, name))
		if err != nil {
			return nil, fmt.Errorf("GigaAM %s: %w", name, err)
		}
		hash := sha256.New()
		size, copyErr := io.Copy(hash, io.LimitReader(input, info.Bytes+1))
		closeErr := input.Close()
		if copyErr != nil || closeErr != nil || size != info.Bytes || hex.EncodeToString(hash.Sum(nil)) != info.SHA256 {
			return nil, fmt.Errorf("GigaAM checksum/size mismatch for %s", name)
		}
	}
	return &model, nil
}

func gigaByte(piece string) (byte, error) {
	if len(piece) != 6 || !strings.HasPrefix(piece, "<0x") || piece[5] != '>' {
		return 0, fmt.Errorf("invalid GigaAM byte token")
	}
	value, err := strconv.ParseUint(piece[3:5], 16, 8)
	return byte(value), err
}

func (m *gigaManifest) decode(ids []int32) (string, error) {
	var text strings.Builder
	for _, id := range ids {
		if id < 0 || int(id) >= len(m.Tokens) {
			return "", fmt.Errorf("invalid GigaAM output token")
		}
		token := m.Tokens[id]
		switch token.Kind {
		case "normal":
			text.WriteString(strings.ReplaceAll(token.Piece, "▁", " "))
		case "unknown":
			text.WriteString(m.UnknownSurface)
		case "byte":
			value, err := gigaByte(token.Piece)
			if err != nil {
				return "", err
			}
			text.WriteByte(value)
		case "control":
		}
	}
	result := strings.TrimSpace(text.String())
	if !utf8.ValidString(result) {
		return "", fmt.Errorf("GigaAM produced invalid UTF-8")
	}
	return result, nil
}
