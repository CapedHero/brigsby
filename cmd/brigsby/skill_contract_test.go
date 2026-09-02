package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type skillContract struct {
	SkillPath             string             `json:"skill_path"`
	Name                  string             `json:"name"`
	DescriptionContains   string             `json:"description_contains"`
	RequiredReferences    []string           `json:"required_references"`
	RequiredPhrases       []string           `json:"required_phrases"`
	RequiredREADMEPhrases []string           `json:"required_readme_phrases"`
	HelpCommands          []skillHelpCommand `json:"help_commands"`
}

type skillHelpCommand struct {
	Arguments []string `json:"argv"`
	Contains  string   `json:"contains"`
}

func TestFirstPartySkillContract(t *testing.T) {
	contract := loadSkillContract(t)
	repositoryRoot := filepath.Join("..", "..")
	skillPath := filepath.Join(repositoryRoot, filepath.FromSlash(contract.SkillPath))
	skill, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read first-party Skill: %v", err)
	}
	contents := string(skill)
	if strings.Contains(contents, "disable-model-invocation:") {
		t.Fatal("first-party Skill must remain model-invoked")
	}
	for _, expected := range []string{"name: " + contract.Name, "description: ", contract.DescriptionContains} {
		if !strings.Contains(contents, expected) {
			t.Errorf("Skill must contain %q", expected)
		}
	}
	for _, reference := range contract.RequiredReferences {
		if !strings.Contains(contents, "[workflows.md]("+reference+")") {
			t.Errorf("Skill must link progressive reference %q", reference)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(skillPath), filepath.FromSlash(reference))); err != nil {
			t.Errorf("progressive reference %q is unavailable: %v", reference, err)
		}
	}
	for _, phrase := range contract.RequiredPhrases {
		if !strings.Contains(contents, phrase) {
			t.Errorf("Skill must contain safety guidance %q", phrase)
		}
	}

	readme, err := os.ReadFile(filepath.Join(repositoryRoot, "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	for _, phrase := range contract.RequiredREADMEPhrases {
		if !strings.Contains(string(readme), phrase) {
			t.Errorf("README must document first-party Skill discovery with %q", phrase)
		}
	}

	for _, command := range contract.HelpCommands {
		var stdout, stderr bytes.Buffer
		if exitCode := run(command.Arguments, &stdout, &stderr); exitCode != 0 {
			t.Errorf("brigsby %s --help exit=%d stderr=%s", strings.Join(command.Arguments, " "), exitCode, stderr.String())
			continue
		}
		if !strings.Contains(stdout.String(), command.Contains) {
			t.Errorf("brigsby %s help does not contain %q:\n%s", strings.Join(command.Arguments, " "), command.Contains, stdout.String())
		}
	}
}

func loadSkillContract(t *testing.T) skillContract {
	t.Helper()

	contents, err := os.ReadFile(filepath.Join("testdata", "brigsby-skill-contract.json"))
	if err != nil {
		t.Fatalf("read Skill contract fixture: %v", err)
	}
	var contract skillContract
	if err := json.Unmarshal(contents, &contract); err != nil {
		t.Fatalf("parse Skill contract fixture: %v", err)
	}
	return contract
}
