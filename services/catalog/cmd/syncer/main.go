package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"
)

const maxResponseBytes = 10 << 20

var httpClient = &http.Client{Timeout: 30 * time.Second}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	receiverURL := envOr("RECEIVER_URL", "http://localhost:8081")
	discoverQueue := envOr("DISCOVER_QUEUE", "catalog-discover")
	namespace := envOr("REGISTRY_NAMESPACE", "quay.io/hummingbird")

	parts := strings.SplitN(namespace, "/", 2)
	if len(parts) != 2 {
		slog.Error("REGISTRY_NAMESPACE must be registry/org format", "value", namespace)
		os.Exit(1)
	}
	registry, org := parts[0], parts[1]

	client := reconciler.NewEnqueueClient(receiverURL)

	repos, err := listOrgRepositories(ctx, registry, org)
	if err != nil {
		slog.Error("list repositories failed", "registry", registry, "org", org, "error", err)
		os.Exit(1)
	}
	slog.Info("discovered repositories", "org", org, "count", len(repos))

	for _, repo := range repos {
		ref := registry + "/" + repo
		if err := client.Enqueue(ctx, discoverQueue, ref, 0); err != nil {
			slog.Error("enqueue failed", "repo", ref, "error", err)
			continue
		}
		slog.Info("enqueued", "repo", ref, "queue", discoverQueue)
	}

	slog.Info("sync complete", "org", org, "repos", len(repos))
}

func listOrgRepositories(ctx context.Context, registry, org string) ([]string, error) {
	if strings.Contains(registry, "quay.io") {
		return listQuayRepositories(ctx, registry, org)
	}
	return listV2Repositories(ctx, registry, org)
}

func listQuayRepositories(ctx context.Context, registry, org string) ([]string, error) {
	var allRepos []string
	nextPage := ""

	for {
		reqURL := fmt.Sprintf("https://%s/api/v1/repository?namespace=%s&public=true",
			registry, url.QueryEscape(org))
		if nextPage != "" {
			reqURL += "&next_page=" + url.QueryEscape(nextPage)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, err
		}
		if token := os.Getenv("QUAY_TOKEN"); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("GET %s: %w", reqURL, err)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("GET %s: status %d", reqURL, resp.StatusCode)
		}

		var result struct {
			Repositories []struct {
				Namespace string `json:"namespace"`
				Name      string `json:"name"`
			} `json:"repositories"`
			NextPage string `json:"next_page"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&result)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decode: %w", err)
		}

		for _, r := range result.Repositories {
			allRepos = append(allRepos, r.Namespace+"/"+r.Name)
		}

		if result.NextPage == "" {
			break
		}
		nextPage = result.NextPage
	}

	return allRepos, nil
}

func listV2Repositories(ctx context.Context, registry, org string) ([]string, error) {
	reqURL := fmt.Sprintf("https://%s/v2/_catalog", registry)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", reqURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", reqURL, resp.StatusCode)
	}

	var result struct {
		Repositories []string `json:"repositories"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	var filtered []string
	for _, r := range result.Repositories {
		if strings.HasPrefix(r, org+"/") {
			filtered = append(filtered, r)
		}
	}
	return filtered, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
