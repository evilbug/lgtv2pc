package discover

import "testing"

func TestParseHeader(t *testing.T) {
	resp := "HTTP/1.1 200 OK\r\n" +
		"CACHE-CONTROL: max-age=1800\r\n" +
		"LOCATION: http://192.168.1.50:1754/\r\n" +
		"SERVER: WebOS/1.0 UPnP/1.0\r\n" +
		"ST: urn:lge-com:service:webos-second-screen:1\r\n\r\n"

	if got := parseHeader(resp, "SERVER"); got != "WebOS/1.0 UPnP/1.0" {
		t.Errorf("SERVER = %q", got)
	}
	if got := parseHeader(resp, "location"); got != "http://192.168.1.50:1754/" {
		t.Errorf("LOCATION (case-insensitive) = %q", got)
	}
	if got := parseHeader(resp, "AUSENTE"); got != "" {
		t.Errorf("cabecera ausente debería ser vacía, got %q", got)
	}
}
