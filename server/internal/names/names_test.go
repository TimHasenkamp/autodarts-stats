package names

import "testing"

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"Tim":           "tim",
		"  tim  ":       "tim",
		"TIM":           "tim",
		"Jürgen Müller": "juergen mueller",
		"Jörg  Straße":  "joerg strasse",
		"JÜRGEN":        "juergen",
		"René":          "rene",
		"Ünal\tÖz":      "uenal oez",
		"":              "",
		"   ":           "",
		"Zoë Ångström":  "zoe angstroem",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeIdempotent(t *testing.T) {
	for _, s := range []string{"Jürgen Müller", "  A  B ", "ßß"} {
		once := Normalize(s)
		if twice := Normalize(once); twice != once {
			t.Errorf("not idempotent: %q -> %q -> %q", s, once, twice)
		}
	}
}
