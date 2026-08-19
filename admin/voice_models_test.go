package main

import "testing"

func TestSafeVoiceArchivePath(t *testing.T) {
	root := "model-root"
	cases := []struct {
		name string
		want string
		ok   bool
	}{
		{"model-root/model.int8.onnx", "model.int8.onnx", true},
		{"./model-root/test_wavs/ja.wav", "test_wavs/ja.wav", true},
		{"model-root", "", true},
		{"model-root/../evil", "", false},
		{"other/file", "", false},
		{"/model-root/file", "", false},
	}
	for _, tc := range cases {
		got, ok := safeVoiceArchivePath(root, tc.name)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("safeVoiceArchivePath(%q)=(%q,%v), want (%q,%v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}
