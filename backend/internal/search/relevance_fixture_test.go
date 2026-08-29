package search

import (
	"encoding/json"
	"os"
	"testing"
)

type relevanceCase struct {
	ID            string               `json:"id"`
	Description   string               `json:"description"`
	Query         string               `json:"query"`
	Kind          string               `json:"kind"`
	Candidates    []relevanceCandidate `json:"candidates"`
	ExpectedTopID string               `json:"expectedTopId"`
}

type relevanceCandidate struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

func TestRelevanceFixtureSchema(t *testing.T) {
	body, err := os.ReadFile("testdata/relevance_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []relevanceCase
	if err := json.Unmarshal(body, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 5 {
		t.Fatalf("expected at least 5 relevance cases, got %d", len(cases))
	}
	seen := make(map[string]bool, len(cases))
	allowedKinds := map[string]bool{"users": true, "posts": true, "topics": true, "moims": true}
	for _, testCase := range cases {
		if testCase.ID == "" || seen[testCase.ID] || testCase.Description == "" || !allowedKinds[testCase.Kind] {
			t.Errorf("invalid relevance case metadata: %+v", testCase)
		}
		seen[testCase.ID] = true
		if _, err := Parse(testCase.Query, false); err != nil {
			t.Errorf("case %s has invalid query: %v", testCase.ID, err)
		}
		foundExpected := false
		for _, candidate := range testCase.Candidates {
			if candidate.ID == "" || candidate.Text == "" {
				t.Errorf("case %s has empty candidate: %+v", testCase.ID, candidate)
			}
			foundExpected = foundExpected || candidate.ID == testCase.ExpectedTopID
		}
		if len(testCase.Candidates) < 2 || !foundExpected {
			t.Errorf("case %s must contain expected top and a distractor", testCase.ID)
		}
	}
}
