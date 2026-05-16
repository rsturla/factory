import { pipeline, stage, resource, git } from "@hummingbird/factory-sdk";

const spec = pipeline({
  name: "code-review",

  resources: {
    targetRepo: resource(git({ access: "read-write" })),
  },

  stages: [
    stage("review", {
      agent: {
        image: "quay.io/hummingbird/agent-claude-code:latest",
        command: ["claude-code", "--print", "--prompt-file", "/workspace/.prompt.md"],
        prompt: "prompts/review.md",
        model: "claude-sonnet-4",
        credentials: [
          { name: "anthropic", provider: "anthropic" }
        ],
        resources: ["targetRepo"],
      },
      output: {
        type: "review",
        target: "pull_request",
      },
    }),
  ],
});

// Output JSON to stdout for Factory loader
console.log(JSON.stringify(spec));
