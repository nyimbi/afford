package config

import "testing"

func TestMutationGuard(t *testing.T) {
	cases := []struct {
		name    string
		base    string
		safety  SafetyConfig
		blocked bool
	}{
		{"loopback submit", "http://127.0.0.1:3000", SafetyConfig{SubmitForms: true}, false},
		{"localhost", "http://localhost:8080", SafetyConfig{SubmitForms: true}, false},
		{"staging name", "https://staging.shop.example.com", SafetyConfig{SubmitForms: true}, false},
		{"private net", "http://10.4.5.6", SafetyConfig{AllowDelete: true}, false},
		{"production submit", "https://shop.example.com", SafetyConfig{SubmitForms: true}, true},
		{"production grant", "https://shop.example.com", SafetyConfig{AllowSideEffects: true}, true},
		{"production read only", "https://shop.example.com", SafetyConfig{}, false},
		{"staging label anywhere in prefix", "https://staging.evil.com.shop.example.org", SafetyConfig{SubmitForms: true}, false},
		{"not 172 private", "http://172.32.0.1", SafetyConfig{SubmitForms: true}, true},
		{"explicit host", "https://shop.example.com", SafetyConfig{SubmitForms: true, MutationHosts: []string{"shop.example.com"}}, false},
		{"explicit host narrows default", "http://localhost", SafetyConfig{SubmitForms: true, MutationHosts: []string{"shop.example.com"}}, true},
		{"wildcard lifts", "https://shop.example.com", SafetyConfig{SubmitForms: true, MutationHosts: []string{"*"}}, false},
	}
	for _, c := range cases {
		cfg := Default()
		cfg.Run.BaseURL = c.base
		cfg.Safety = c.safety
		err := cfg.CheckMutationGuard()
		if (err != nil) != c.blocked {
			t.Errorf("%s: blocked=%v, err=%v", c.name, c.blocked, err)
		}
	}
}
