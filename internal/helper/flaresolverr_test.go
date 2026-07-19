package helper

import "testing"

func TestNormalizeFlareSolverrEndpoint(t *testing.T) {
	cases := map[string]string{
		"":                          "",
		"http://localhost:8191":     "http://localhost:8191/v1",
		"http://flaresolverr:8191/": "http://flaresolverr:8191/v1",
		"http://localhost:8191/v1":  "http://localhost:8191/v1",
	}
	for input, want := range cases {
		if got := normalizeFlareSolverrEndpoint(input); got != want {
			t.Fatalf("normalizeFlareSolverrEndpoint(%q) = %q, want %q", input, got, want)
		}
	}
}
