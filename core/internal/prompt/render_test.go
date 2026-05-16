package prompt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderer_Basic(t *testing.T) {
	// Create temp directory for templates
	tmpDir := t.TempDir()
	promptPath := filepath.Join(tmpDir, "test.md")

	template := `# Test Prompt
Parameter: {{param "test_param"}}
`
	if err := os.WriteFile(promptPath, []byte(template), 0644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	renderer := NewRenderer(tmpDir, nil, "")
	data := TemplateData{
		Params: map[string]string{
			"test_param": "value123",
		},
	}

	result, err := renderer.Render(context.Background(), "test.md", data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(result, "value123") {
		t.Errorf("expected result to contain value123, got: %s", result)
	}
}

func TestRenderer_Include(t *testing.T) {
	tmpDir := t.TempDir()

	// Create common.md
	commonPath := filepath.Join(tmpDir, "common.md")
	common := "Common content from include"
	if err := os.WriteFile(commonPath, []byte(common), 0644); err != nil {
		t.Fatalf("write common: %v", err)
	}

	// Create main template
	mainPath := filepath.Join(tmpDir, "main.md")
	main := `Main template
{{include "common.md"}}
End`
	if err := os.WriteFile(mainPath, []byte(main), 0644); err != nil {
		t.Fatalf("write main: %v", err)
	}

	renderer := NewRenderer(tmpDir, nil, "")
	data := TemplateData{}

	result, err := renderer.Render(context.Background(), "main.md", data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(result, "Common content from include") {
		t.Errorf("expected included content, got: %s", result)
	}
}

func TestRenderer_Include_PathTraversal(t *testing.T) {
	tmpDir := t.TempDir()

	// Try to include file outside pipeline dir
	mainPath := filepath.Join(tmpDir, "main.md")
	main := `{{include "../../etc/passwd"}}`
	if err := os.WriteFile(mainPath, []byte(main), 0644); err != nil {
		t.Fatalf("write main: %v", err)
	}

	renderer := NewRenderer(tmpDir, nil, "")
	data := TemplateData{}

	_, err := renderer.Render(context.Background(), "main.md", data)
	if err == nil {
		t.Error("expected error for path traversal")
	}
	if !strings.Contains(err.Error(), "outside pipeline directory") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRenderer_PromptPath_PathTraversal(t *testing.T) {
	tmpDir := t.TempDir()

	renderer := NewRenderer(tmpDir, nil, "")
	data := TemplateData{}

	// Try to read prompt outside pipeline dir
	_, err := renderer.Render(context.Background(), "../../etc/passwd", data)
	if err == nil {
		t.Error("expected error for path traversal in prompt path")
	}
	if !strings.Contains(err.Error(), "outside pipeline directory") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRenderer_Param(t *testing.T) {
	tmpDir := t.TempDir()

	template := `CVE: {{param "cve_id"}}
Severity: {{param "severity"}}
Missing: {{param "nonexistent"}}`

	promptPath := filepath.Join(tmpDir, "test.md")
	if err := os.WriteFile(promptPath, []byte(template), 0644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	renderer := NewRenderer(tmpDir, nil, "")
	data := TemplateData{
		Params: map[string]string{
			"cve_id":   "CVE-2026-12345",
			"severity": "critical",
		},
	}

	result, err := renderer.Render(context.Background(), "test.md", data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(result, "CVE-2026-12345") {
		t.Error("expected cve_id in result")
	}
	if !strings.Contains(result, "critical") {
		t.Error("expected severity in result")
	}
}
