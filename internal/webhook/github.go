package webhook

import (
	"encoding/json"
	"fmt"
)

// GitHubKeyExtractor extracts a queue key from GitHub webhook events.
// The key format is "{repo}#{number}" for PR/issue events, or "{repo}@{ref}" for push events.
func GitHubKeyExtractor(eventType string, payload []byte) (string, int, error) {
	switch eventType {
	case "pull_request":
		return extractGitHubPR(payload)
	case "push":
		return extractGitHubPush(payload)
	case "issues":
		return extractGitHubIssue(payload)
	default:
		// Unknown event type — skip.
		return "", 0, nil
	}
}

func extractGitHubPR(payload []byte) (string, int, error) {
	var event struct {
		Action string `json:"action"`
		Number int    `json:"number"`
		Repo   struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return "", 0, fmt.Errorf("parse PR event: %w", err)
	}
	key := fmt.Sprintf("%s#%d", event.Repo.FullName, event.Number)
	return key, 0, nil
}

func extractGitHubPush(payload []byte) (string, int, error) {
	var event struct {
		Ref  string `json:"ref"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return "", 0, fmt.Errorf("parse push event: %w", err)
	}
	key := fmt.Sprintf("%s@%s", event.Repo.FullName, event.Ref)
	return key, 0, nil
}

func extractGitHubIssue(payload []byte) (string, int, error) {
	var event struct {
		Action string `json:"action"`
		Issue  struct {
			Number int `json:"number"`
		} `json:"issue"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return "", 0, fmt.Errorf("parse issue event: %w", err)
	}
	key := fmt.Sprintf("%s#%d", event.Repo.FullName, event.Issue.Number)
	return key, 0, nil
}
