package main

import (
	"mime"
	"os"
	"os/exec"
	"testing"
)

func TestDetectSendFileMIMEUsesCompatibleM4AType(t *testing.T) {
	// Confine the synthetic system MIME table to a subprocess so other tests
	// cannot observe a changed process-global extension mapping.
	if os.Getenv("WACLI_TEST_M4A_MIME") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestDetectSendFileMIMEUsesCompatibleM4AType$", "-test.v")
		cmd.Env = append(os.Environ(), "WACLI_TEST_M4A_MIME=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("M4A detection subprocess: %v\n%s", err, output)
		}
		return
	}
	// Reproduce the incompatible mapping independently of the host OS table.
	if err := mime.AddExtensionType(".m4a", "audio/mp4a-latm"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, override, want string
	}{
		{"recording.m4a", "", "audio/mp4"},
		{"RECORDING.M4A", "", "audio/mp4"},
		{"recording.m4a", "audio/aac", "audio/aac"},
		{"recording.m4a", " application/octet-stream ", "application/octet-stream"},
	} {
		if got := detectSendFileMIME(tc.path, tc.override, nil); got != tc.want {
			t.Errorf("detectSendFileMIME(%q, %q) = %q, want %q", tc.path, tc.override, got, tc.want)
		}
	}
}
