// SPDX-License-Identifier: GPL-3.0-only
package packaging

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBundleLauncher(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux bundle launcher requires a POSIX shell and symlinks")
	}
	launcher, err := os.ReadFile("../../scripts/bundle-launch.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, layout := range []string{
		"usr/lib/ld-linux-aarch64.so.1",
		"usr/lib/aarch64-linux-gnu/ld-linux-aarch64.so.1",
		"usr/lib64/ld-linux-x86-64.so.2",
		"",
	} {
		name := layout
		if name == "" {
			name = "missing-loader"
		}
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "bundle check")
			write := func(path, contents string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(root, "launch"), string(launcher))
			if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
				t.Fatal(err)
			}
			for link, target := range map[string]string{
				"quasard": "launch", "quasard-tools": "launch", "bin/ffmpeg": "../launch",
			} {
				if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
					t.Fatal(err)
				}
			}
			libraryDir := filepath.Join(root, "runtime/usr/lib/aarch64-linux-gnu")
			if err := os.MkdirAll(libraryDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if layout != "" {
				// Fake loader checks launcher arguments without executing Linux ELF on the host.
				write(filepath.Join(root, "runtime", layout), "#!/bin/sh\nset -eu\n[ \"${LD_LIBRARY_PATH+x}\" != x ]\n[ \"${LD_PRELOAD+x}\" != x ]\nprintf '%s\\n' \"$PWD\" \"$QUASARD_ESPEAK_DATA\" \"$@\"\n")
			}
			for _, entry := range []struct {
				path, program string
				args          []string
			}{
				{"launch", "quasard", []string{"--version"}},
				{"quasard", "quasard", []string{"--version"}},
				{"quasard-tools", "quasard-tools", []string{"--version"}},
				{"bin/ffmpeg", "ffmpeg", []string{"-version"}},
				{"launch", "ffmpeg", []string{"--ffmpeg", "-version"}},
			} {
				cmd := exec.Command(filepath.Join(root, entry.path), entry.args...)
				cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH=/unused", "LD_PRELOAD=")
				out, err := cmd.CombinedOutput()
				if layout == "" {
					if err == nil || !strings.Contains(string(out), "Bundled Linux loader missing") {
						t.Fatalf("%s: expected missing loader, got %s, %v", entry.path, out, err)
					}
					continue
				}
				if err != nil {
					t.Fatalf("%s: %v: %s", entry.path, err, out)
				}
				libraries := filepath.Join(root, "runtime/opt/quasard/lib") + ":" + libraryDir
				if strings.HasPrefix(layout, "usr/lib64/") {
					libraries += ":" + filepath.Join(root, "runtime/usr/lib64")
				}
				want := strings.Join([]string{
					root, filepath.Join(root, "runtime/opt/quasard/share/espeak-ng-data"),
					"--library-path", libraries,
					filepath.Join(root, "runtime/usr/local/bin", entry.program),
					entry.args[len(entry.args)-1], "",
				}, "\n")
				if string(out) != want {
					t.Fatalf("%s: got %q, want %q", entry.path, out, want)
				}
			}
		})
	}
}
