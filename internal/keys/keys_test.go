package keys

import "testing"

func TestParseKey(t *testing.T) {
	cases := []struct {
		in      string
		want    uint16
		wantErr bool
	}{
		{"rightctrl", 97, false},
		{"RightShift", 54, false}, // case-insensitive
		{"  pause ", 119, false},  // trim
		{"f13", 183, false},
		{"f24", 194, false},
		{"97", 97, false},   // decimal
		{"0x61", 97, false}, // hex
		{"", 0, true},       // vacío
		{"nope", 0, true},   // desconocido
		{"0", 0, true},      // código 0 inválido
		{"99999", 0, true},  // fuera de rango
	}
	for _, c := range cases {
		got, err := ParseKey(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseKey(%q): esperaba error, got %d", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseKey(%q): error inesperado: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseKey(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
