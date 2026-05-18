package auth

import "testing"

func TestAuthHeaderAndScheme(t *testing.T) {
	empty := ""
	custom := "Token"
	cases := []struct {
		name       string
		auth       Auth
		wantHeader string
		wantScheme string
		wantHasSch bool
	}{
		{
			name:       "defaults",
			auth:       Auth{},
			wantHeader: "Authorization",
			wantScheme: "Bearer",
			wantHasSch: true,
		},
		{
			name:       "explicit empty scheme means no prefix",
			auth:       Auth{Scheme: &empty},
			wantHeader: "Authorization",
			wantScheme: "",
			wantHasSch: false,
		},
		{
			name:       "custom scheme + custom header",
			auth:       Auth{HeaderName: "X-API-Key", Scheme: &custom},
			wantHeader: "X-API-Key",
			wantScheme: "Token",
			wantHasSch: true,
		},
		{
			name:       "custom header keeps default scheme",
			auth:       Auth{HeaderName: "X-Custom"},
			wantHeader: "X-Custom",
			wantScheme: "Bearer",
			wantHasSch: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, s, has := c.auth.HeaderAndScheme()
			if h != c.wantHeader {
				t.Errorf("header = %q, want %q", h, c.wantHeader)
			}
			if s != c.wantScheme {
				t.Errorf("scheme = %q, want %q", s, c.wantScheme)
			}
			if has != c.wantHasSch {
				t.Errorf("hasScheme = %v, want %v", has, c.wantHasSch)
			}
		})
	}
}
