package auth

import (
	"strings"
	"testing"
)

func TestCompileFieldMatcherExact(t *testing.T) {
	m, err := compileFieldMatcher("User.email")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if m.AnyType || m.AnyField {
		t.Errorf("exact pattern should not be wildcard: %+v", m)
	}
	if !m.Match("User", "email") {
		t.Errorf("User.email must match (User, email)")
	}
	if m.Match("User", "name") {
		t.Errorf("User.email must not match (User, name)")
	}
	if m.Match("Customer", "email") {
		t.Errorf("User.email must not match (Customer, email)")
	}
}

func TestCompileFieldMatcherAnyType(t *testing.T) {
	m, err := compileFieldMatcher("*.password")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if !m.AnyType || m.AnyField {
		t.Errorf("*.password should be AnyType only: %+v", m)
	}
	for _, ty := range []string{"User", "Admin", "ServiceAccount"} {
		if !m.Match(ty, "password") {
			t.Errorf("*.password must match (%s, password)", ty)
		}
	}
	if m.Match("User", "passwordHash") {
		t.Errorf("*.password must not match different field")
	}
}

func TestCompileFieldMatcherAnyField(t *testing.T) {
	m, err := compileFieldMatcher("BillingAccount.*")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if m.AnyType || !m.AnyField {
		t.Errorf("BillingAccount.* should be AnyField only: %+v", m)
	}
	if !m.Match("BillingAccount", "cardLast4") {
		t.Errorf("BillingAccount.* must match every field")
	}
	if m.Match("User", "cardLast4") {
		t.Errorf("BillingAccount.* must not match other types")
	}
}

func TestCompileFieldMatcherRejectsStarStar(t *testing.T) {
	_, err := compileFieldMatcher("*.*")
	if err == nil {
		t.Errorf("*.* must be rejected — use Tools list to deny query_execute")
	}
}

func TestCompileFieldMatcherRejectsMalformed(t *testing.T) {
	for _, bad := range []string{
		"",
		"NoDot",
		".field",
		"Type.",
		"Type..field",
		"Cust*.email", // partial wildcard in type
		"User.email*", // partial wildcard in field
	} {
		_, err := compileFieldMatcher(bad)
		if err == nil {
			t.Errorf("expected reject for %q", bad)
		}
	}
}

func TestCompileFieldMatchersList(t *testing.T) {
	patterns := []string{"User.email", "*.password", "BillingAccount.*"}
	got, err := CompileFieldMatchers(patterns)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
}

func TestCompileFieldMatchersEmpty(t *testing.T) {
	got, err := CompileFieldMatchers(nil)
	if err != nil {
		t.Fatalf("nil list should be ok: %v", err)
	}
	if got != nil {
		t.Errorf("empty list should return nil matchers, got %+v", got)
	}
}

func TestCompileFieldMatchersSurfaceBadPattern(t *testing.T) {
	_, err := CompileFieldMatchers([]string{"User.email", "garbage"})
	if err == nil {
		t.Fatalf("expected error for malformed pattern in list")
	}
	if !strings.Contains(err.Error(), "garbage") {
		t.Errorf("error should name offender; got %q", err)
	}
}

func TestMatchAny(t *testing.T) {
	matchers, err := CompileFieldMatchers([]string{
		"User.email",
		"*.password",
		"BillingAccount.*",
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	for _, c := range []struct {
		typeName, fieldName string
		wantHit             string // matched Pattern, or "" for no hit
	}{
		{"User", "email", "User.email"},
		{"Customer", "password", "*.password"},
		{"BillingAccount", "cardLast4", "BillingAccount.*"},
		{"User", "name", ""},
		{"Order", "total", ""},
	} {
		got := MatchAny(matchers, c.typeName, c.fieldName)
		if c.wantHit == "" && got != nil {
			t.Errorf("(%s, %s): expected no hit, got %q", c.typeName, c.fieldName, got.Pattern)
			continue
		}
		if c.wantHit != "" && (got == nil || got.Pattern != c.wantHit) {
			pat := "<nil>"
			if got != nil {
				pat = got.Pattern
			}
			t.Errorf("(%s, %s): expected hit on %q, got %q", c.typeName, c.fieldName, c.wantHit, pat)
		}
	}
}

func TestBuildScopeCompilesDenyFields(t *testing.T) {
	c := Client{
		Name: "limited", Token: "t",
		Tools: []string{"*"}, Servers: []string{"*"},
		DenyFields: []string{"User.email", "*.password"},
	}
	scope, err := c.BuildScope(nil)
	if err != nil {
		t.Fatalf("BuildScope: %v", err)
	}
	if len(scope.DeniedFieldMatchers) != 2 {
		t.Errorf("DeniedFieldMatchers len = %d, want 2", len(scope.DeniedFieldMatchers))
	}
	hit := MatchAny(scope.DeniedFieldMatchers, "User", "email")
	if hit == nil || hit.Pattern != "User.email" {
		t.Errorf("expected hit on User.email; got %+v", hit)
	}
}

func TestBuildScopeRejectsInvalidDenyPattern(t *testing.T) {
	c := Client{
		Name: "broken", Token: "t",
		Tools: []string{"*"}, Servers: []string{"*"},
		DenyFields: []string{"garbage"},
	}
	_, err := c.BuildScope(nil)
	if err == nil {
		t.Errorf("expected error for malformed deny pattern")
	}
}
