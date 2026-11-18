package payloads

import (
	"strings"
	"testing"

	"github.com/nyimbi/afford/internal/model"
)

func el(tag, name, typ, nameAttr string) model.Element {
	return model.Element{
		Tag: tag, Name: name, Type: typ, NameAttr: nameAttr, Visible: true,
		Attrs: map[string]string{},
	}
}

func TestInferKindPrefersDeclaration(t *testing.T) {
	if got := InferKind(el("input", "Email address", "email", "e")); got != "email" {
		t.Errorf("declared email inferred as %q", got)
	}
	if got := InferKind(el("select", "Country", "", "country")); got != "select" {
		t.Errorf("select inferred as %q", got)
	}
	if got := InferKind(el("input", "Amount", "text", "amount")); got != "number" {
		t.Errorf("amount inferred as %q", got)
	}
	if got := InferKind(el("input", "Nickname", "text", "nickname")); got != "text" {
		t.Errorf("nickname inferred as %q", got)
	}
}

func TestValidValue(t *testing.T) {
	amount := el("input", "Amount", "number", "amount")
	amount.Attrs["min"] = "5"
	amount.Attrs["max"] = "10"
	if got := ValidValue(amount); got != "5" {
		t.Errorf("number with min 5 = %q", got)
	}
	if got := ValidValue(el("input", "Mail", "email", "email")); !strings.Contains(got, "@") {
		t.Errorf("email value %q", got)
	}
	if got := ValidValue(el("input", "Go", "submit", "go")); got != "" {
		t.Errorf("submit value should be empty, got %q", got)
	}
}

func TestSweepMatchesFieldType(t *testing.T) {
	emailLabels := labels(Sweep(el("input", "Email", "email", "email"), "standard"))
	if !emailLabels["malformed"] {
		t.Error("email sweep must include a malformed address")
	}
	dateLabels := labels(Sweep(el("input", "Due", "date", "due"), "standard"))
	if !dateLabels["impossible"] {
		t.Error("date sweep must include an impossible date")
	}
	if got := Sweep(el("select", "Country", "", "country"), "deep"); len(got) != 0 {
		t.Errorf("select should not be text-swept, got %d payloads", len(got))
	}
	smoke := Sweep(el("input", "Name", "text", "name"), "smoke")
	deep := Sweep(el("input", "Name", "text", "name"), "deep")
	if len(deep) <= len(smoke) {
		t.Errorf("deep sweep (%d) should exceed smoke (%d)", len(deep), len(smoke))
	}
}

func labels(ps []Payload) map[string]bool {
	out := map[string]bool{}
	for _, p := range ps {
		out[p.Label] = true
	}
	return out
}

func TestIsDestructive(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"Delete account", true},
		{"Log out", true},
		{"Sign out", true},
		{"sign-out", true},
		{"Reset filters", true},
		{"Save draft", false},
		{"Dropdown", false},
		{"dropdown menu", false},
		{"Drop shipment", true},
	}
	for _, c := range cases {
		if got := IsDestructive(el("button", c.name, "button", ""), nil); got != c.want {
			t.Errorf("IsDestructive(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}
