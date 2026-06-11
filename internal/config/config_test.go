package config

import "testing"

func TestHDMIAppID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"hdmi2", "com.webos.app.hdmi2"},
		{"HDMI3", "com.webos.app.hdmi3"},
		{"2", "com.webos.app.hdmi2"},
		{"  hdmi1 ", "com.webos.app.hdmi1"},
		{"com.webos.app.hdmi4", "com.webos.app.hdmi4"}, // appId completo
		{"com.webos.app.livetv", "com.webos.app.livetv"},
	}
	for _, c := range cases {
		got := (&Config{HDMIInput: c.in}).HDMIAppID()
		if got != c.want {
			t.Errorf("HDMIAppID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
