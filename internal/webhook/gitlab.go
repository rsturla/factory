package webhook

import (
	"encoding/json"
	"fmt"
)

// GitLabKeyExtractor extracts a queue key from GitLab webhook events.
// The key format is "{project_path}!{iid}" for MR events, or "{project_path}@{ref}" for push events.
func GitLabKeyExtractor(eventType string, payload []byte) (string, int, error) {
	switch eventType {
	case "Merge Request Hook":
		return extractGitLabMR(payload)
	case "Push Hook":
		return extractGitLabPush(payload)
	default:
		return "", 0, nil
	}
}

func extractGitLabMR(payload []byte) (string, int, error) {
	var event struct {
		ObjectAttributes struct {
			IID int `json:"iid"`
		} `json:"object_attributes"`
		Project struct {
			PathWithNamespace string `json:"path_with_namespace"`
		} `json:"project"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return "", 0, fmt.Errorf("parse MR event: %w", err)
	}
	key := fmt.Sprintf("%s!%d", event.Project.PathWithNamespace, event.ObjectAttributes.IID)
	return key, 0, nil
}

func extractGitLabPush(payload []byte) (string, int, error) {
	var event struct {
		Ref     string `json:"ref"`
		Project struct {
			PathWithNamespace string `json:"path_with_namespace"`
		} `json:"project"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return "", 0, fmt.Errorf("parse push event: %w", err)
	}
	key := fmt.Sprintf("%s@%s", event.Project.PathWithNamespace, event.Ref)
	return key, 0, nil
}
