// SPDX-License-Identifier: GPL-3.0-only
package config

import "testing"

func TestGigaAMConfig(t *testing.T) {
	valid := "[asr]\nengine='gigaam'\nmodel='v3_e2e_rnnt'\nmodel_path='/models/gigaam'\nlanguage='ru'\nthreads=2\n"
	config, err := loadDeviceConfig(t, valid)
	if err != nil || config.ASR.Engine != "gigaam" || config.ASR.Model != "v3_e2e_rnnt" {
		t.Fatalf("GigaAM config: %v", err)
	}
	for _, suffix := range []string{"language='auto'", "language='en'", "threads=0", "threads=9", "max_utterance_ms=25001"} {
		_, err := loadDeviceConfig(t, "[asr]\nengine='gigaam'\nmodel='v3_e2e_rnnt'\nmodel_path='/models/gigaam'\n"+suffix)
		if err == nil {
			t.Fatalf("invalid GigaAM config accepted: %s", suffix)
		}
	}
}
