// Package sarif renders findings as SARIF 2.1.0 so GitHub code scanning, GitLab
// and IDEs can ingest them. A web finding has a URL, not a file, so the URL is
// the artifact location and the fingerprint carries identity across runs.
package sarif

import (
	"encoding/json"

	"afford/internal/model"
)

type log struct {
	Schema  string `json:"$schema"`
	Version string `json:"version"`
	Runs    []run  `json:"runs"`
}

type run struct {
	Tool    tool     `json:"tool"`
	Results []result `json:"results"`
}

type tool struct {
	Driver driver `json:"driver"`
}

type driver struct {
	Name           string `json:"name"`
	Version        string `json:"version,omitempty"`
	InformationURI string `json:"informationUri,omitempty"`
	Rules          []rule `json:"rules"`
}

type text struct {
	Text string `json:"text"`
}

type rule struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	ShortDescription text    `json:"shortDescription"`
	FullDescription  text    `json:"fullDescription"`
	DefaultConfig    ruleCfg `json:"defaultConfiguration"`
}

type ruleCfg struct {
	Level string `json:"level"`
}

type result struct {
	RuleID        string            `json:"ruleId"`
	Level         string            `json:"level"`
	Message       text              `json:"message"`
	Locations     []location        `json:"locations"`
	Fingerprints  map[string]string `json:"partialFingerprints,omitempty"`
	BaselineState string            `json:"baselineState,omitempty"`
	Suppressions  []suppression     `json:"suppressions,omitempty"`
	Properties    map[string]any    `json:"properties,omitempty"`
}

type location struct {
	Physical physical `json:"physicalLocation"`
}

type physical struct {
	Artifact artifact `json:"artifactLocation"`
}

type artifact struct {
	URI string `json:"uri"`
}

type suppression struct {
	Kind          string `json:"kind"`
	Justification string `json:"justification,omitempty"`
}

// Level maps afford severities onto SARIF's three result levels.
func Level(s model.Severity) string {
	switch s {
	case model.Blocker, model.Critical:
		return "error"
	case model.Major:
		return "warning"
	}
	return "note"
}

func message(f model.Finding) string {
	msg := f.Title
	if f.Element != "" {
		msg += ": " + f.Element
	}
	if f.Confidence == "unconfirmed" {
		msg += " (unconfirmed: did not reproduce on a second load)"
	}
	if len(f.Evidence) > 0 {
		msg += ". " + f.Evidence[0]
	}
	return msg
}

// Render encodes findings as one SARIF run. Every catalogued code appears as a
// rule so a viewer can describe a code that did not fire this time.
func Render(findings []model.Finding, version string) ([]byte, error) {
	specs := model.Specs()
	rules := make([]rule, 0, len(specs))
	for _, s := range specs {
		rules = append(rules, rule{
			ID: s.Code, Name: s.Code,
			ShortDescription: text{s.Title}, FullDescription: text{s.Why},
			DefaultConfig: ruleCfg{Level: Level(s.Severity)},
		})
	}
	results := make([]result, 0, len(findings))
	for _, f := range findings {
		r := result{
			RuleID: f.Code, Level: Level(f.Severity), Message: text{message(f)},
			Locations: []location{{physical{artifact{f.PageURL}}}},
			Properties: map[string]any{
				"severity": string(f.Severity), "gates": f.Gates(),
			},
		}
		if f.Fingerprint != "" {
			r.Fingerprints = map[string]string{"afford/v1": f.Fingerprint}
		}
		switch f.Status {
		case "new":
			r.BaselineState = "new"
		case "known":
			r.BaselineState = "unchanged"
		case "suppressed":
			r.Suppressions = []suppression{{Kind: "external", Justification: f.Suppression}}
		}
		if f.Confidence != "" {
			r.Properties["confidence"] = f.Confidence
		}
		if len(f.AlsoOn) > 0 {
			r.Properties["also_on"] = f.AlsoOn
		}
		results = append(results, r)
	}
	doc := log{
		Schema:  "https://json.schemastore.org/sarif-2.1.0.json",
		Version: "2.1.0",
		Runs: []run{{
			Tool:    tool{driver{Name: "afford", Version: version, InformationURI: "https://github.com/nyimbi/afford", Rules: rules}},
			Results: results,
		}},
	}
	return json.MarshalIndent(doc, "", "  ")
}
